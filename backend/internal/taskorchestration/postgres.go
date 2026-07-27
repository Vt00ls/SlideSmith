package taskorchestration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

const postgresIdentityBlockSize uint64 = 1_000_000

type PersistenceErrorCode uint8

const (
	PersistenceInvalidConfiguration PersistenceErrorCode = iota + 1
	PersistenceUnavailable
	PersistenceStateCorrupt
)

// PersistenceError never retains SQL, driver details, locators, content, or
// credentials from the owned adapter.
type PersistenceError struct {
	code PersistenceErrorCode
}

func (err *PersistenceError) Error() string {
	if err == nil {
		return "task orchestration persistence is unavailable"
	}
	switch err.code {
	case PersistenceInvalidConfiguration:
		return "task orchestration persistence configuration is invalid"
	case PersistenceStateCorrupt:
		return "task orchestration persistence state is invalid"
	default:
		return "task orchestration persistence is unavailable"
	}
}

func (err *PersistenceError) Code() PersistenceErrorCode {
	if err == nil {
		return PersistenceUnavailable
	}
	return err.code
}

func newPersistenceError(code PersistenceErrorCode) *PersistenceError {
	return &PersistenceError{code: code}
}

type PersistenceFaultPoint uint8

const (
	PersistenceFaultBeforeMandatoryAudit PersistenceFaultPoint = iota + 1
	PersistenceFaultAfterMandatoryAudit
	PersistenceFaultBeforeOutbox
	PersistenceFaultBeforeCommit
	PersistenceFaultAfterCommit
	PersistenceFaultBeforeResponse
)

type PersistenceFaultInjector interface {
	FailAt(PersistenceFaultPoint) bool
}

// PersistenceFaultController is deterministic test support for transaction
// and response crash boundaries. It has no mutation authority of its own.
type PersistenceFaultController struct {
	mu   sync.Mutex
	next PersistenceFaultPoint
}

func (controller *PersistenceFaultController) FailNextAt(point PersistenceFaultPoint) error {
	if point < PersistenceFaultBeforeMandatoryAudit || point > PersistenceFaultBeforeResponse {
		return newPersistenceError(PersistenceInvalidConfiguration)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.next = point
	return nil
}

func (controller *PersistenceFaultController) FailAt(point PersistenceFaultPoint) bool {
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.next != point {
		return false
	}
	controller.next = 0
	return true
}

type SchedulerEnqueueFact struct {
	OperationID        OperationID
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	RuntimeRunID       RuntimeRunID
	DecisionID         DecisionID
	TaskRevision       TaskRevision
	Kind               EnactmentKind
	PayloadDigest      EnactmentPayloadDigest
	ActivityGeneration ActivityGeneration
	FenceKind          EnactmentFenceKind
	Fence              uint64
	CausationID        CausationID
}

// SchedulerTransaction exposes one call to a configured Scheduler-owned
// PostgreSQL function. It exposes neither SQL nor Task persistence.
type SchedulerTransaction interface {
	Enqueue(context.Context) error
}

type SchedulerTransactionalParticipant interface {
	Participate(context.Context, SchedulerTransaction, SchedulerEnqueueFact) error
}

type SchedulerTransactionalParticipantFunc func(
	context.Context,
	SchedulerTransaction,
	SchedulerEnqueueFact,
) error

func (function SchedulerTransactionalParticipantFunc) Participate(
	ctx context.Context,
	transaction SchedulerTransaction,
	fact SchedulerEnqueueFact,
) error {
	return function(ctx, transaction, fact)
}

type DecisionCommitObserver interface {
	ObserveCommittedDecision(context.Context, TransitionDecision) error
}

type DecisionCommitObserverFunc func(context.Context, TransitionDecision) error

func (function DecisionCommitObserverFunc) ObserveCommittedDecision(
	ctx context.Context,
	decision TransitionDecision,
) error {
	return function(ctx, decision)
}

type PostgresConfig struct {
	Schema                   string
	Now                      func() time.Time
	Faults                   PersistenceFaultInjector
	SchedulerParticipant     SchedulerTransactionalParticipant
	SchedulerEnqueueFunction string
	CommitObserver           DecisionCommitObserver
}

type PersistenceView struct {
	TaskRevision            TaskRevision
	DecisionCount           uint64
	RevisionCount           uint64
	MandatoryAuditFactCount uint64
	OutboxCount             uint64
	PhaseRunCount           uint64
	RuntimeRunCount         uint64
	EvidenceRefCount        uint64
	EvidenceDiagnosticCount uint64
}

type PostgresAdapter struct {
	db                       *sql.DB
	schema                   string
	now                      func() time.Time
	faults                   PersistenceFaultInjector
	schedulerParticipant     SchedulerTransactionalParticipant
	schedulerEnqueueFunction string
	commitObserver           DecisionCommitObserver
}

func NewPostgresAdapter(db *sql.DB, config PostgresConfig) (*PostgresAdapter, error) {
	if db == nil {
		return nil, newPersistenceError(PersistenceInvalidConfiguration)
	}
	schema := config.Schema
	if schema == "" {
		schema = "public"
	}
	if !validPostgresIdentifier(schema) {
		return nil, newPersistenceError(PersistenceInvalidConfiguration)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	if config.SchedulerParticipant == nil {
		return nil, newPersistenceError(PersistenceInvalidConfiguration)
	}
	if !validPostgresQualifiedIdentifier(config.SchedulerEnqueueFunction) {
		return nil, newPersistenceError(PersistenceInvalidConfiguration)
	}
	return &PostgresAdapter{
		db: db, schema: schema, now: now, faults: config.Faults,
		schedulerParticipant:     config.SchedulerParticipant,
		schedulerEnqueueFunction: config.SchedulerEnqueueFunction,
		commitObserver:           config.CommitObserver,
	}, nil
}

func validPostgresIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char == '_' ||
			index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func validPostgresQualifiedIdentifier(value string) bool {
	parts := strings.Split(value, ".")
	return len(parts) == 2 && validPostgresIdentifier(parts[0]) && validPostgresIdentifier(parts[1])
}

func (adapter *PostgresAdapter) table(name string) string {
	return adapter.schema + "." + name
}

func (adapter *PostgresAdapter) Migrate(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	var version int
	if err := adapter.db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version < 120000 {
		return newPersistenceError(PersistenceUnavailable)
	}
	tx, err := adapter.db.BeginTx(ctx, nil)
	if err != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(7560419505986)"); err != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	for _, statement := range adapter.migrationStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return newPersistenceError(PersistenceUnavailable)
		}
	}
	if err := tx.Commit(); err != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	return nil
}

func (adapter *PostgresAdapter) migrationStatements() []string {
	tasks := adapter.table("task_orchestration_tasks")
	decisions := adapter.table("task_orchestration_decisions")
	requests := adapter.table("task_orchestration_decision_requests")
	revisions := adapter.table("task_orchestration_revisions")
	evidence := adapter.table("task_orchestration_evidence_refs")
	diagnostics := adapter.table("task_orchestration_evidence_diagnostics")
	audit := adapter.table("task_orchestration_mandatory_audit_facts")
	outbox := adapter.table("task_orchestration_outbox")
	delivery := adapter.table("task_orchestration_outbox_delivery")
	phaseRuns := adapter.table("task_orchestration_phase_runs")
	runtimeRuns := adapter.table("task_orchestration_runtime_runs")
	recovery := adapter.table("task_orchestration_recovery_state")
	immutableFunction := adapter.table("task_orchestration_reject_immutable_mutation")
	return []string{
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", adapter.table("task_orchestration_decision_blocks")),
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", adapter.table("task_orchestration_audit_blocks")),
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", adapter.table("task_orchestration_phase_run_blocks")),
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", adapter.table("task_orchestration_runtime_run_blocks")),
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", adapter.table("task_orchestration_operation_blocks")),
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", adapter.table("task_orchestration_causation_blocks")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			task_id text PRIMARY KEY,
			revision bigint NOT NULL CHECK (revision > 0),
			owner_authority_id text NOT NULL,
			owner_generation bigint NOT NULL CHECK (owner_generation > 0),
			latest_decision_id text NOT NULL,
			state jsonb NOT NULL,
			updated_at timestamptz NOT NULL
		)`, tasks),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			decision_id text PRIMARY KEY,
			task_id text NOT NULL,
			decision_request_id text NOT NULL,
			canonical_request_digest bytea NOT NULL CHECK (octet_length(canonical_request_digest) = 32),
			previous_revision bigint NOT NULL,
			accepted_revision bigint NOT NULL,
			committed_at timestamptz NOT NULL,
			decision_state jsonb NOT NULL,
			UNIQUE (task_id, accepted_revision)
		)`, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			task_id text NOT NULL,
			authority_kind smallint NOT NULL,
			authority_id text NOT NULL,
			authority_generation bigint NOT NULL,
			authority_reason smallint NOT NULL,
			decision_request_id text NOT NULL,
			canonical_request_digest bytea NOT NULL CHECK (octet_length(canonical_request_digest) = 32),
			decision_id text NOT NULL REFERENCES %s(decision_id),
			PRIMARY KEY (task_id, authority_kind, authority_id, authority_generation, authority_reason, decision_request_id)
		)`, requests, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			task_id text NOT NULL,
			revision bigint NOT NULL,
			decision_id text NOT NULL UNIQUE REFERENCES %s(decision_id),
			projection jsonb NOT NULL,
			PRIMARY KEY (task_id, revision)
		)`, revisions, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			task_id text NOT NULL,
			evidence_id text NOT NULL,
			kind smallint NOT NULL,
			digest bytea NOT NULL CHECK (octet_length(digest) = 32),
			replay_digest bytea NOT NULL CHECK (octet_length(replay_digest) = 32),
			decision_id text NOT NULL REFERENCES %s(decision_id),
			PRIMARY KEY (task_id, evidence_id)
		)`, evidence, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			task_id text NOT NULL,
			observation_sequence bigint NOT NULL CHECK (observation_sequence > 0),
			evidence_id text NOT NULL,
			disposition smallint NOT NULL,
			reason smallint NOT NULL,
			observed_at timestamptz NOT NULL,
			PRIMARY KEY (task_id, observation_sequence)
		)`, diagnostics),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			audit_fact_id text PRIMARY KEY,
			decision_id text NOT NULL UNIQUE REFERENCES %s(decision_id),
			task_id text NOT NULL,
			decision_request_id text NOT NULL,
			committed_at timestamptz NOT NULL
		)`, audit, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			operation_id text PRIMARY KEY,
			decision_id text NOT NULL REFERENCES %s(decision_id),
			task_id text NOT NULL,
			phase_run_id text NOT NULL DEFAULT '',
			runtime_run_id text NOT NULL DEFAULT '',
			kind smallint NOT NULL,
			payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
			activity_generation bigint NOT NULL,
			safety_epoch bigint NOT NULL CHECK (safety_epoch > 0),
			fence_kind smallint NOT NULL,
			fence bigint NOT NULL,
			causation_id text NOT NULL,
			prerequisite_bindings jsonb NOT NULL,
			committed_at timestamptz NOT NULL
		)`, outbox, decisions),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS safety_epoch bigint NOT NULL DEFAULT 1 CHECK (safety_epoch > 0)", outbox),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			operation_id text PRIMARY KEY REFERENCES %s(operation_id),
			disposition smallint NOT NULL,
			lease_authority_kind smallint NOT NULL DEFAULT 0,
			lease_authority_id text NOT NULL DEFAULT '',
			lease_authority_generation bigint NOT NULL DEFAULT 0,
			lease_authority_reason smallint NOT NULL DEFAULT 0,
				lease_fence bigint NOT NULL DEFAULT 0 CHECK (lease_fence >= 0),
				lease_expires_at timestamptz,
				delivery_count bigint NOT NULL DEFAULT 0 CHECK (delivery_count >= 0),
				send_started boolean NOT NULL DEFAULT FALSE,
				terminal boolean NOT NULL DEFAULT FALSE,
			result_digest bytea CHECK (result_digest IS NULL OR octet_length(result_digest) = 32),
			retry_at timestamptz,
			deferral_reason smallint NOT NULL DEFAULT 0,
			reconcile_fence bigint NOT NULL DEFAULT 0 CHECK (reconcile_fence >= 0),
			updated_at timestamptz NOT NULL
			)`, delivery, outbox),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS send_started boolean NOT NULL DEFAULT FALSE", delivery),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS task_orchestration_outbox_delivery_claimable
			ON %s (terminal, disposition, retry_at, lease_expires_at, operation_id)`, delivery),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			task_id text NOT NULL,
			phase_run_id text NOT NULL,
			phase_key text NOT NULL,
			attempt integer NOT NULL,
			generation bigint NOT NULL,
			fence bigint NOT NULL,
			outcome smallint NOT NULL,
			last_decision_id text NOT NULL REFERENCES %s(decision_id),
			PRIMARY KEY (task_id, phase_run_id),
			UNIQUE (task_id, phase_key, attempt)
		)`, phaseRuns, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			task_id text NOT NULL,
			phase_run_id text NOT NULL,
			runtime_run_id text NOT NULL,
			operation_id text NOT NULL,
			outcome smallint NOT NULL,
			last_decision_id text NOT NULL REFERENCES %s(decision_id),
			PRIMARY KEY (task_id, runtime_run_id),
			FOREIGN KEY (task_id, phase_run_id) REFERENCES %s(task_id, phase_run_id)
		)`, runtimeRuns, decisions, phaseRuns),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			singleton boolean PRIMARY KEY DEFAULT TRUE CHECK (singleton),
			authority_kind smallint NOT NULL DEFAULT 0,
			authority_id text NOT NULL DEFAULT '',
			authority_generation bigint NOT NULL DEFAULT 0,
			authority_reason smallint NOT NULL DEFAULT 0,
			generation bigint NOT NULL DEFAULT 0,
			fence bigint NOT NULL DEFAULT 0,
			safety_epoch bigint NOT NULL DEFAULT 0,
			activity_generation_fence bigint NOT NULL DEFAULT 0,
			mode smallint NOT NULL
		)`, recovery),
		fmt.Sprintf("INSERT INTO %s (singleton, mode) VALUES (TRUE, %d) ON CONFLICT (singleton) DO NOTHING", recovery, OperationalFullReady),
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'immutable task orchestration fact'; END $$`, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_mutation ON %s", decisions),
		fmt.Sprintf("CREATE TRIGGER reject_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", decisions, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_mutation ON %s", requests),
		fmt.Sprintf("CREATE TRIGGER reject_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", requests, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_mutation ON %s", revisions),
		fmt.Sprintf("CREATE TRIGGER reject_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", revisions, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_mutation ON %s", evidence),
		fmt.Sprintf("CREATE TRIGGER reject_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", evidence, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_mutation ON %s", diagnostics),
		fmt.Sprintf("CREATE TRIGGER reject_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", diagnostics, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_mutation ON %s", audit),
		fmt.Sprintf("CREATE TRIGGER reject_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", audit, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_mutation ON %s", outbox),
		fmt.Sprintf("CREATE TRIGGER reject_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", outbox, immutableFunction),
	}
}

func (adapter *PostgresAdapter) Decide(
	ctx context.Context,
	intent TransitionIntent,
) (TransitionDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	digest, err := canonicalizeIntent(intent)
	if err != nil {
		return TransitionDecision{}, err
	}
	typed := intent.(intentValue)
	header := intent.Header()
	tx, err := adapter.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", header.TaskID.value); err != nil {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	var replayed TransitionDecision
	replayFound := false
	if committed, committedDigest, found, lookupErr := adapter.lookupRequest(ctx, tx, typed); lookupErr != nil {
		return TransitionDecision{}, normalizePersistenceFailure(lookupErr)
	} else if found {
		if committedDigest != digest {
			return TransitionDecision{}, newError(ErrorIntegrityConflict)
		}
		replayed = committed
		replayFound = true
	}

	record, taskExists, err := adapter.loadTask(ctx, tx, header.TaskID)
	if err != nil {
		return TransitionDecision{}, normalizePersistenceFailure(err)
	}
	if replayFound {
		if !taskExists {
			return TransitionDecision{}, newPersistenceError(PersistenceStateCorrupt)
		}
		return replayed, nil
	}
	recovery, err := adapter.loadRecovery(ctx, tx, intent.Kind() == IntentApplyOperationalFence)
	if err != nil {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	ids, err := adapter.allocateIdentityBlock(ctx, tx)
	if err != nil {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	persistence := &memoryPersistence{
		tasks: make(map[TaskID]taskRecord), decisions: make(map[decisionRequestScope]committedDecision),
		acceptedEvidence: make(map[evidenceScope]committedEvidence),
		outbox:           make(map[OperationID]authoritativeOutboxRecord),
		deliveries:       make(map[OperationID]memoryDeliveryState),
		ids:              ids, recovery: recovery,
	}
	if taskExists {
		persistence.tasks[header.TaskID] = record
	}
	evidenceID, replayDigest, isEvidence, evidenceErr := computeEvidenceReplayDigest(intent)
	if evidenceErr != nil {
		return TransitionDecision{}, evidenceErr
	}
	if isEvidence {
		committed, committedReplayDigest, found, lookupErr := adapter.lookupEvidence(ctx, tx, header.TaskID, evidenceID)
		if lookupErr != nil {
			return TransitionDecision{}, normalizePersistenceFailure(lookupErr)
		}
		if found {
			persistence.acceptedEvidence[evidenceScope{taskID: header.TaskID, evidenceID: evidenceID}] = committedEvidence{
				replayDigest: committedReplayDigest, decision: committed,
			}
		}
	}
	clock := &controlledClock{now: adapter.now().UTC()}
	engine := &decisionEngine{clock: clock, persistence: persistence, controls: &harnessControls{}}
	decision, decisionErr := engine.Decide(ctx, intent)
	if decisionErr != nil {
		updatedRecord, stillExists := persistence.tasks[header.TaskID]
		if taskExists && stillExists &&
			updatedRecord.evidenceDiagnosticCount > record.evidenceDiagnosticCount {
			if updatedRecord.evidenceDiagnosticCount != record.evidenceDiagnosticCount+1 ||
				adapter.persistEvidenceDiagnostic(ctx, tx, header.TaskID, updatedRecord) != nil {
				return TransitionDecision{}, newPersistenceError(PersistenceUnavailable)
			}
			if err := tx.Commit(); err != nil {
				return TransitionDecision{}, newPersistenceError(PersistenceUnavailable)
			}
		}
		return TransitionDecision{}, decisionErr
	}
	updatedRecord := persistence.tasks[header.TaskID]
	previousRevision := TaskRevision(0)
	if taskExists {
		previousRevision = record.revision
	}
	freshDecision := decision.AcceptedTaskRevision == previousRevision+1
	if freshDecision {
		if err := adapter.persistFreshDecision(
			ctx, tx, typed, digest, decision, updatedRecord, persistence.recovery,
			isEvidence, replayDigest,
		); err != nil {
			return TransitionDecision{}, err
		}
	} else if err := adapter.insertRequestLookup(ctx, tx, typed, digest, decision.DecisionID); err != nil {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	if adapter.failAt(PersistenceFaultBeforeCommit) {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return TransitionDecision{}, newError(ErrorReconciliationRequired)
	}
	if adapter.failAt(PersistenceFaultAfterCommit) {
		return TransitionDecision{}, newError(ErrorReconciliationRequired)
	}
	if adapter.commitObserver != nil {
		_ = adapter.commitObserver.ObserveCommittedDecision(
			context.WithoutCancel(ctx), cloneTransitionDecision(decision),
		)
	}
	if adapter.failAt(PersistenceFaultBeforeResponse) {
		return TransitionDecision{}, newError(ErrorReconciliationRequired)
	}
	return cloneTransitionDecision(decision), nil
}

func (adapter *PostgresAdapter) persistFreshDecision(
	ctx context.Context,
	tx *sql.Tx,
	intent intentValue,
	digest CanonicalRequestDigest,
	decision TransitionDecision,
	record taskRecord,
	recovery recoveryBinding,
	isEvidence bool,
	replayDigest [32]byte,
) error {
	state, err := encodePostgresTaskState(record)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	decisionState, err := json.Marshal(postgresDecisionStateFromDecision(decision))
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	projection, err := json.Marshal(postgresTaskProjectionStateFromProjection(decision.TaskProjection))
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	header := intent.header
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		task_id, revision, owner_authority_id, owner_generation, latest_decision_id, state, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)
	ON CONFLICT (task_id) DO UPDATE SET
		revision=EXCLUDED.revision,
		owner_authority_id=EXCLUDED.owner_authority_id,
		owner_generation=EXCLUDED.owner_generation,
		latest_decision_id=EXCLUDED.latest_decision_id,
		state=EXCLUDED.state,
		updated_at=EXCLUDED.updated_at
	WHERE %s.revision=$8`, adapter.table("task_orchestration_tasks"), adapter.table("task_orchestration_tasks")),
		header.TaskID.value, decision.AcceptedTaskRevision, record.owner.authorityID.value,
		record.owner.generation, decision.DecisionID.value, state, decision.CommittedAt,
		decision.PreviousTaskRevision,
	)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return newError(ErrorStaleTaskRevision)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		decision_id, task_id, decision_request_id, canonical_request_digest,
		previous_revision, accepted_revision, committed_at, decision_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb)`, adapter.table("task_orchestration_decisions")),
		decision.DecisionID.value, header.TaskID.value, header.DecisionRequestID.value, digest[:],
		decision.PreviousTaskRevision, decision.AcceptedTaskRevision, decision.CommittedAt, decisionState,
	); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if err := adapter.insertRequestLookup(ctx, tx, intent, digest, decision.DecisionID); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		task_id, revision, decision_id, projection
	) VALUES ($1,$2,$3,$4::jsonb)`, adapter.table("task_orchestration_revisions")),
		header.TaskID.value, decision.AcceptedTaskRevision, decision.DecisionID.value, projection,
	); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if isEvidence {
		for _, evidence := range decision.AcceptedEvidenceRefs {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
				task_id, evidence_id, kind, digest, replay_digest, decision_id
			) VALUES ($1,$2,$3,$4,$5,$6)`, adapter.table("task_orchestration_evidence_refs")),
				header.TaskID.value, evidence.ID.value, evidence.Kind, evidence.Digest[:],
				replayDigest[:], decision.DecisionID.value,
			); err != nil {
				return newError(ErrorDependencyUnavailable)
			}
		}
	}
	if err := adapter.persistAggregateRelationships(ctx, tx, header.TaskID, decision.DecisionID, record); err != nil {
		return err
	}
	if adapter.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return newError(ErrorDependencyUnavailable)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, decision_id, task_id, decision_request_id, committed_at
	) VALUES ($1,$2,$3,$4,$5)`, adapter.table("task_orchestration_mandatory_audit_facts")),
		decision.MandatoryAuditFactRef.AuditFactID.value, decision.DecisionID.value,
		header.TaskID.value, header.DecisionRequestID.value, decision.CommittedAt,
	); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if adapter.failAt(PersistenceFaultAfterMandatoryAudit) || adapter.failAt(PersistenceFaultBeforeOutbox) {
		return newError(ErrorDependencyUnavailable)
	}
	for _, enactment := range decision.EnactmentRefs {
		inserted, fact, insertErr := adapter.insertOutbox(ctx, tx, decision, record, enactment)
		if insertErr != nil {
			return insertErr
		}
		if !inserted {
			if intent.kind != IntentReconcileEnactment {
				return newError(ErrorIntegrityConflict)
			}
			if err := adapter.validateExistingOutboxBinding(ctx, tx, record, enactment); err != nil {
				return err
			}
			continue
		}
		schedulerTx := &postgresSchedulerTransaction{
			tx: tx, enqueueFunction: adapter.schedulerEnqueueFunction, fact: fact,
		}
		if err := adapter.schedulerParticipant.Participate(ctx, schedulerTx, fact); err != nil {
			return newError(ErrorQueueRejected)
		}
		if !schedulerTx.enqueued {
			return newError(ErrorQueueRejected)
		}
	}
	if intent.kind == IntentApplyOperationalFence {
		if err := adapter.persistRecovery(ctx, tx, recovery); err != nil {
			return newError(ErrorDependencyUnavailable)
		}
	}
	return nil
}

func (adapter *PostgresAdapter) persistAggregateRelationships(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
	decisionID DecisionID,
	record taskRecord,
) error {
	if record.aggregate == nil {
		return nil
	}
	for _, run := range record.aggregate.phaseRuns {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			task_id, phase_run_id, phase_key, attempt, generation, fence, outcome, last_decision_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (task_id, phase_run_id) DO UPDATE SET
			outcome=EXCLUDED.outcome, last_decision_id=EXCLUDED.last_decision_id`, adapter.table("task_orchestration_phase_runs")),
			taskID.value, run.id.value, run.phaseKey.value, run.attempt, run.generation,
			run.fence, run.outcome, decisionID.value,
		); err != nil {
			return newError(ErrorDependencyUnavailable)
		}
		for _, runtimeRun := range run.runtimeRuns {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
				task_id, phase_run_id, runtime_run_id, operation_id, outcome, last_decision_id
			) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (task_id, runtime_run_id) DO UPDATE SET
				outcome=EXCLUDED.outcome, last_decision_id=EXCLUDED.last_decision_id`, adapter.table("task_orchestration_runtime_runs")),
				taskID.value, run.id.value, runtimeRun.id.value, runtimeRun.operationID.value,
				runtimeRun.outcome, decisionID.value,
			); err != nil {
				return newError(ErrorDependencyUnavailable)
			}
		}
	}
	return nil
}

func (adapter *PostgresAdapter) persistEvidenceDiagnostic(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
	record taskRecord,
) error {
	diagnostic := record.latestEvidenceDiagnostic
	if record.evidenceDiagnosticCount == 0 || !validOpaqueID(diagnostic.EvidenceID.value) ||
		diagnostic.Disposition != EvidenceDispositionNonAuthoritative ||
		diagnostic.Reason < EvidenceDiagnosticScopeConflict ||
		diagnostic.Reason > EvidenceDiagnosticUnauthorized {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		task_id, observation_sequence, evidence_id, disposition, reason, observed_at
	) VALUES ($1,$2,$3,$4,$5,$6)`, adapter.table("task_orchestration_evidence_diagnostics")),
		taskID.value, record.evidenceDiagnosticCount, diagnostic.EvidenceID.value,
		diagnostic.Disposition, diagnostic.Reason, adapter.now().UTC(),
	)
	if err != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	return nil
}

func (adapter *PostgresAdapter) insertOutbox(
	ctx context.Context,
	tx *sql.Tx,
	decision TransitionDecision,
	record taskRecord,
	enactment EnactmentRef,
) (bool, SchedulerEnqueueFact, error) {
	phaseRunID, runtimeRunID := enactmentScope(record, enactment.OperationID)
	fenceKind, fence := postgresFenceValue(enactment.Fence)
	prerequisites, err := json.Marshal(map[string]any{
		"decision_id":           decision.DecisionID.value,
		"task_revision":         uint64(decision.AcceptedTaskRevision),
		"accepted_evidence_ids": evidenceIDStrings(decision.AcceptedEvidenceRefs),
	})
	if err != nil {
		return false, SchedulerEnqueueFact{}, newError(ErrorDependencyUnavailable)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, decision_id, task_id, phase_run_id, runtime_run_id, kind,
		payload_digest, activity_generation, fence_kind, fence, causation_id,
		safety_epoch, prerequisite_bindings, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14)
	ON CONFLICT (operation_id) DO NOTHING`, adapter.table("task_orchestration_outbox")),
		enactment.OperationID.value, decision.DecisionID.value, decision.TaskProjection.TaskID.value,
		phaseRunID.value, runtimeRunID.value, enactment.Kind, enactment.PayloadDigest[:],
		enactment.ActivityGeneration, fenceKind, fence, enactment.CausationID.value,
		decision.TaskProjection.SafetyEpoch, prerequisites, decision.CommittedAt,
	)
	if err != nil {
		return false, SchedulerEnqueueFact{}, newError(ErrorDependencyUnavailable)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, SchedulerEnqueueFact{}, newError(ErrorDependencyUnavailable)
	}
	return rows == 1, SchedulerEnqueueFact{
		OperationID: enactment.OperationID, TaskID: decision.TaskProjection.TaskID,
		PhaseRunID: phaseRunID, RuntimeRunID: runtimeRunID, DecisionID: decision.DecisionID,
		TaskRevision: decision.AcceptedTaskRevision, Kind: enactment.Kind,
		PayloadDigest: enactment.PayloadDigest, ActivityGeneration: enactment.ActivityGeneration,
		FenceKind: fenceKind, Fence: fence, CausationID: enactment.CausationID,
	}, nil
}

func (adapter *PostgresAdapter) validateExistingOutboxBinding(
	ctx context.Context,
	tx *sql.Tx,
	record taskRecord,
	enactment EnactmentRef,
) error {
	var decisionID, taskID, phaseRunID, runtimeRunID, causationID string
	var kind EnactmentKind
	var payloadDigest, prerequisites, decisionState []byte
	var activityGeneration ActivityGeneration
	var safetyEpoch SafetyEpoch
	var fenceKind EnactmentFenceKind
	var fence uint64
	var committedAt time.Time
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT outbox.decision_id, outbox.task_id,
		outbox.phase_run_id, outbox.runtime_run_id, outbox.kind, outbox.payload_digest,
		outbox.activity_generation, outbox.fence_kind, outbox.fence, outbox.causation_id,
		outbox.safety_epoch, outbox.prerequisite_bindings, outbox.committed_at, decision.decision_state
		FROM %s AS outbox
		JOIN %s AS decision ON decision.decision_id=outbox.decision_id
		WHERE outbox.operation_id=$1`, adapter.table("task_orchestration_outbox"),
		adapter.table("task_orchestration_decisions")), enactment.OperationID.value,
	).Scan(&decisionID, &taskID, &phaseRunID, &runtimeRunID, &kind, &payloadDigest,
		&activityGeneration, &fenceKind, &fence, &causationID, &safetyEpoch,
		&prerequisites, &committedAt, &decisionState)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return newPersistenceError(PersistenceStateCorrupt)
		}
		return newError(ErrorDependencyUnavailable)
	}
	expectedPhaseRunID, expectedRuntimeRunID := enactmentScope(record, enactment.OperationID)
	expectedFenceKind, expectedFence := postgresFenceValue(enactment.Fence)
	if taskID != record.latestDecision.TaskProjection.TaskID.value ||
		phaseRunID != expectedPhaseRunID.value || runtimeRunID != expectedRuntimeRunID.value ||
		kind != enactment.Kind || len(payloadDigest) != len(enactment.PayloadDigest) ||
		!bytes.Equal(payloadDigest, enactment.PayloadDigest[:]) ||
		activityGeneration != enactment.ActivityGeneration || fenceKind != expectedFenceKind ||
		fence != expectedFence || causationID != enactment.CausationID.value {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	var persistedDecision postgresDecisionState
	if json.Unmarshal(decisionState, &persistedDecision) != nil {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	originalDecision := persistedDecision.decision()
	if !validPersistedDecision(originalDecision) || originalDecision.DecisionID.value != decisionID ||
		originalDecision.TaskProjection.TaskID.value != taskID ||
		safetyEpoch != originalDecision.TaskProjection.SafetyEpoch ||
		!committedAt.Equal(originalDecision.CommittedAt.Truncate(time.Microsecond)) {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	found := false
	for _, original := range originalDecision.EnactmentRefs {
		if reflect.DeepEqual(original, enactment) {
			found = true
			break
		}
	}
	if !found {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	var binding struct {
		DecisionID          string   `json:"decision_id"`
		TaskRevision        uint64   `json:"task_revision"`
		AcceptedEvidenceIDs []string `json:"accepted_evidence_ids"`
	}
	if json.Unmarshal(prerequisites, &binding) != nil ||
		binding.DecisionID != decisionID ||
		binding.TaskRevision != uint64(originalDecision.AcceptedTaskRevision) ||
		!reflect.DeepEqual(binding.AcceptedEvidenceIDs, evidenceIDStrings(originalDecision.AcceptedEvidenceRefs)) {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	return nil
}

func enactmentScope(record taskRecord, operationID OperationID) (PhaseRunID, RuntimeRunID) {
	if operation, exists := record.runtimeOperations[operationID]; exists {
		return operation.phaseRunID, operation.runtimeRunID
	}
	if operation, exists := record.lifecycleOperations[operationID]; exists {
		return operation.phaseRunID, RuntimeRunID{}
	}
	if operation, exists := record.publicationOperations[operationID]; exists {
		return operation.phaseRunID, RuntimeRunID{}
	}
	if operation, exists := record.schedulingOperations[operationID]; exists {
		return operation.phaseRunID, RuntimeRunID{}
	}
	return PhaseRunID{}, RuntimeRunID{}
}

func evidenceIDStrings(refs []EvidenceRef) []string {
	values := make([]string, 0, len(refs))
	for _, ref := range refs {
		values = append(values, ref.ID.value)
	}
	return values
}

type postgresSchedulerTransaction struct {
	tx              *sql.Tx
	enqueueFunction string
	fact            SchedulerEnqueueFact
	enqueued        bool
}

func (transaction *postgresSchedulerTransaction) Enqueue(ctx context.Context) error {
	if transaction.enqueued || ctx == nil || ctx.Err() != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	fact := transaction.fact
	if _, err := transaction.tx.ExecContext(ctx, "SELECT "+transaction.enqueueFunction+`(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		fact.OperationID.value, fact.TaskID.value, fact.PhaseRunID.value,
		fact.RuntimeRunID.value, fact.DecisionID.value, fact.TaskRevision, fact.Kind,
		fact.PayloadDigest[:], fact.ActivityGeneration, fact.FenceKind, fact.Fence,
		fact.CausationID.value,
	); err != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	transaction.enqueued = true
	return nil
}

func (adapter *PostgresAdapter) lookupRequest(
	ctx context.Context,
	tx *sql.Tx,
	intent intentValue,
) (TransitionDecision, CanonicalRequestDigest, bool, error) {
	var digestBytes []byte
	var encoded []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT request.canonical_request_digest, decision.decision_state
		FROM %s AS request
		JOIN %s AS decision ON decision.decision_id=request.decision_id
		WHERE request.task_id=$1 AND request.authority_kind=$2 AND request.authority_id=$3
		AND request.authority_generation=$4 AND request.authority_reason=$5
		AND request.decision_request_id=$6`,
		adapter.table("task_orchestration_decision_requests"),
		adapter.table("task_orchestration_decisions")),
		intent.header.TaskID.value, intent.authority.kind, intent.authority.id.value,
		intent.authority.generation, intent.authority.reason, intent.header.DecisionRequestID.value,
	).Scan(&digestBytes, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return TransitionDecision{}, CanonicalRequestDigest{}, false, nil
	}
	if err != nil {
		return TransitionDecision{}, CanonicalRequestDigest{}, false, newPersistenceError(PersistenceUnavailable)
	}
	if len(digestBytes) != 32 {
		return TransitionDecision{}, CanonicalRequestDigest{}, false, newPersistenceError(PersistenceStateCorrupt)
	}
	var state postgresDecisionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return TransitionDecision{}, CanonicalRequestDigest{}, false, newPersistenceError(PersistenceStateCorrupt)
	}
	decision := state.decision()
	if !validPersistedDecision(decision) {
		return TransitionDecision{}, CanonicalRequestDigest{}, false, newPersistenceError(PersistenceStateCorrupt)
	}
	var digest CanonicalRequestDigest
	copy(digest[:], digestBytes)
	return decision, digest, true, nil
}

func (adapter *PostgresAdapter) insertRequestLookup(
	ctx context.Context,
	tx *sql.Tx,
	intent intentValue,
	digest CanonicalRequestDigest,
	decisionID DecisionID,
) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		task_id, authority_kind, authority_id, authority_generation, authority_reason,
		decision_request_id, canonical_request_digest, decision_id
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, adapter.table("task_orchestration_decision_requests")),
		intent.header.TaskID.value, intent.authority.kind, intent.authority.id.value,
		intent.authority.generation, intent.authority.reason, intent.header.DecisionRequestID.value,
		digest[:], decisionID.value,
	)
	return err
}

func (adapter *PostgresAdapter) lookupEvidence(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
	evidenceID EvidenceID,
) (TransitionDecision, [32]byte, bool, error) {
	var replayDigest []byte
	var encoded []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT evidence.replay_digest, decision.decision_state
		FROM %s AS evidence
		JOIN %s AS decision ON decision.decision_id=evidence.decision_id
		WHERE evidence.task_id=$1 AND evidence.evidence_id=$2`,
		adapter.table("task_orchestration_evidence_refs"), adapter.table("task_orchestration_decisions")),
		taskID.value, evidenceID.value,
	).Scan(&replayDigest, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return TransitionDecision{}, [32]byte{}, false, nil
	}
	if err != nil {
		return TransitionDecision{}, [32]byte{}, false, newPersistenceError(PersistenceUnavailable)
	}
	if len(replayDigest) != 32 {
		return TransitionDecision{}, [32]byte{}, false, newPersistenceError(PersistenceStateCorrupt)
	}
	var state postgresDecisionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return TransitionDecision{}, [32]byte{}, false, newPersistenceError(PersistenceStateCorrupt)
	}
	decision := state.decision()
	if !validPersistedDecision(decision) {
		return TransitionDecision{}, [32]byte{}, false, newPersistenceError(PersistenceStateCorrupt)
	}
	var digest [32]byte
	copy(digest[:], replayDigest)
	return decision, digest, true, nil
}

func (adapter *PostgresAdapter) loadTask(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
) (taskRecord, bool, error) {
	return adapter.loadTaskState(ctx, tx, taskID, " FOR UPDATE")
}

func (adapter *PostgresAdapter) loadRecovery(
	ctx context.Context,
	tx *sql.Tx,
	exclusive bool,
) (recoveryBinding, error) {
	lock := "FOR SHARE"
	if exclusive {
		lock = "FOR UPDATE"
	}
	var state recoveryBinding
	var kind, reason int16
	var authorityID string
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT authority_kind, authority_id,
		authority_generation, authority_reason, generation, fence, safety_epoch,
		activity_generation_fence, mode FROM %s WHERE singleton=TRUE %s`,
		adapter.table("task_orchestration_recovery_state"), lock),
	).Scan(&kind, &authorityID, &state.authority.generation, &reason, &state.generation,
		&state.fence, &state.safetyEpoch, &state.activityGenerationFence, &state.mode)
	if err != nil {
		return recoveryBinding{}, err
	}
	state.authority.kind = AuthorityKind(kind)
	state.authority.id = AuthorityID{authorityID}
	state.authority.reason = AdministratorReason(reason)
	return state, nil
}

func (adapter *PostgresAdapter) loadRecoveryReadOnly(
	ctx context.Context,
	tx *sql.Tx,
) (recoveryBinding, error) {
	var state recoveryBinding
	var kind, reason int16
	var authorityID string
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT authority_kind, authority_id,
		authority_generation, authority_reason, generation, fence, safety_epoch,
		activity_generation_fence, mode FROM %s WHERE singleton=TRUE`,
		adapter.table("task_orchestration_recovery_state")),
	).Scan(&kind, &authorityID, &state.authority.generation, &reason, &state.generation,
		&state.fence, &state.safetyEpoch, &state.activityGenerationFence, &state.mode)
	if err != nil {
		return recoveryBinding{}, err
	}
	state.authority.kind = AuthorityKind(kind)
	state.authority.id = AuthorityID{authorityID}
	state.authority.reason = AdministratorReason(reason)
	return state, nil
}

func (adapter *PostgresAdapter) persistRecovery(
	ctx context.Context,
	tx *sql.Tx,
	state recoveryBinding,
) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		authority_kind=$1, authority_id=$2, authority_generation=$3, authority_reason=$4,
		generation=$5, fence=$6, safety_epoch=$7, activity_generation_fence=$8, mode=$9
		WHERE singleton=TRUE`, adapter.table("task_orchestration_recovery_state")),
		state.authority.kind, state.authority.id.value, state.authority.generation,
		state.authority.reason, state.generation, state.fence, state.safetyEpoch,
		state.activityGenerationFence, state.mode,
	)
	return err
}

func (adapter *PostgresAdapter) allocateIdentityBlock(
	ctx context.Context,
	tx *sql.Tx,
) (deterministicIDAllocator, error) {
	sequences := []string{
		"task_orchestration_decision_blocks", "task_orchestration_audit_blocks",
		"task_orchestration_phase_run_blocks", "task_orchestration_runtime_run_blocks",
		"task_orchestration_operation_blocks", "task_orchestration_causation_blocks",
	}
	starts := make([]uint64, len(sequences))
	for index, sequence := range sequences {
		var block uint64
		if err := tx.QueryRowContext(ctx, "SELECT nextval('"+adapter.table(sequence)+"')").Scan(&block); err != nil || block == 0 {
			return deterministicIDAllocator{}, newPersistenceError(PersistenceUnavailable)
		}
		starts[index] = (block-1)*postgresIdentityBlockSize + 1
	}
	return newDeterministicIDAllocator(DeterministicIDConfig{
		DecisionStart: starts[0], AuditFactStart: starts[1], PhaseRunStart: starts[2],
		RuntimeRunStart: starts[3], OperationStart: starts[4], CausationStart: starts[5],
	}), nil
}

func (adapter *PostgresAdapter) Query(
	ctx context.Context,
	query TaskQuery,
) (TaskOrchestrationView, error) {
	if ctx == nil || ctx.Err() != nil {
		return TaskOrchestrationView{}, newError(ErrorDependencyUnavailable)
	}
	tx, err := adapter.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return TaskOrchestrationView{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	record, exists, err := adapter.loadTaskReadOnly(ctx, tx, query.TaskID)
	if err != nil {
		return TaskOrchestrationView{}, normalizePersistenceFailure(err)
	}
	recovery, err := adapter.loadRecoveryReadOnly(ctx, tx)
	if err != nil {
		return TaskOrchestrationView{}, newError(ErrorDependencyUnavailable)
	}
	persistence := &memoryPersistence{
		tasks: make(map[TaskID]taskRecord), decisions: make(map[decisionRequestScope]committedDecision),
		acceptedEvidence: make(map[evidenceScope]committedEvidence), recovery: recovery,
	}
	if exists {
		persistence.tasks[query.TaskID] = record
	}
	engine := &decisionEngine{
		clock: &controlledClock{now: adapter.now().UTC()}, persistence: persistence, controls: &harnessControls{},
	}
	view, queryErr := engine.Query(ctx, query)
	if queryErr != nil {
		return TaskOrchestrationView{}, queryErr
	}
	if err := tx.Commit(); err != nil {
		return TaskOrchestrationView{}, newError(ErrorDependencyUnavailable)
	}
	return view, nil
}

func (adapter *PostgresAdapter) loadTaskReadOnly(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
) (taskRecord, bool, error) {
	return adapter.loadTaskState(ctx, tx, taskID, "")
}

func (adapter *PostgresAdapter) loadTaskState(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
	lock string,
) (taskRecord, bool, error) {
	var encoded []byte
	var rowRevision TaskRevision
	var ownerID, latestDecisionID string
	var ownerGeneration AuthorizationGeneration
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT revision, owner_authority_id,
		owner_generation, latest_decision_id, state FROM %s WHERE task_id=$1%s`,
		adapter.table("task_orchestration_tasks"), lock), taskID.value,
	).Scan(&rowRevision, &ownerID, &ownerGeneration, &latestDecisionID, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		orphaned, orphanErr := adapter.hasTaskJournalFacts(ctx, tx, taskID)
		if orphanErr != nil {
			return taskRecord{}, false, orphanErr
		}
		if orphaned {
			return taskRecord{}, false, newPersistenceError(PersistenceStateCorrupt)
		}
		return taskRecord{}, false, nil
	}
	if err != nil {
		return taskRecord{}, false, err
	}
	record, err := decodePostgresTaskState(encoded)
	if err != nil {
		return taskRecord{}, false, err
	}
	if record.revision != rowRevision || record.owner.authorityID.value != ownerID ||
		record.owner.generation != ownerGeneration ||
		record.latestDecision.DecisionID.value != latestDecisionID ||
		record.latestDecision.TaskProjection.TaskID != taskID ||
		record.latestDecision.AcceptedTaskRevision != rowRevision ||
		record.latestDecision.TaskProjection.TaskRevision != rowRevision {
		return taskRecord{}, false, newPersistenceError(PersistenceStateCorrupt)
	}
	if err := adapter.validateTaskJournal(ctx, tx, taskID, record); err != nil {
		return taskRecord{}, false, err
	}
	if err := adapter.overlayEvidenceDiagnostics(ctx, tx, taskID, &record); err != nil {
		return taskRecord{}, false, err
	}
	return record, true, nil
}

func (adapter *PostgresAdapter) hasTaskJournalFacts(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
) (bool, error) {
	var present bool
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1) OR
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1) OR
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1) OR
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1) OR
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1) OR
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1) OR
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1) OR
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1) OR
		EXISTS (SELECT 1 FROM %s WHERE task_id=$1)`,
		adapter.table("task_orchestration_decisions"),
		adapter.table("task_orchestration_decision_requests"),
		adapter.table("task_orchestration_revisions"),
		adapter.table("task_orchestration_evidence_refs"),
		adapter.table("task_orchestration_evidence_diagnostics"),
		adapter.table("task_orchestration_mandatory_audit_facts"),
		adapter.table("task_orchestration_outbox"),
		adapter.table("task_orchestration_phase_runs"),
		adapter.table("task_orchestration_runtime_runs")), taskID.value,
	).Scan(&present)
	return present, err
}

func (adapter *PostgresAdapter) validateTaskJournal(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
	record taskRecord,
) error {
	var decisionTaskID, requestID, auditFactID string
	var digestBytes, decisionBytes, projectionBytes []byte
	var previousRevision, acceptedRevision TaskRevision
	var committedAt time.Time
	var decisionCount, revisionCount, auditCount, outboxCount uint64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT decision.task_id,
		decision.decision_request_id, decision.canonical_request_digest,
		decision.previous_revision, decision.accepted_revision, decision.committed_at,
		decision.decision_state, revision.projection, audit.audit_fact_id,
		(SELECT count(*) FROM %s WHERE task_id=$1),
		(SELECT count(*) FROM %s WHERE task_id=$1),
		(SELECT count(*) FROM %s WHERE task_id=$1),
		(SELECT count(*) FROM %s WHERE task_id=$1)
		FROM %s AS decision
		JOIN %s AS revision ON revision.task_id=decision.task_id
			AND revision.revision=decision.accepted_revision
			AND revision.decision_id=decision.decision_id
		JOIN %s AS audit ON audit.decision_id=decision.decision_id
		WHERE decision.decision_id=$2 AND decision.task_id=$1`,
		adapter.table("task_orchestration_decisions"),
		adapter.table("task_orchestration_revisions"),
		adapter.table("task_orchestration_mandatory_audit_facts"),
		adapter.table("task_orchestration_outbox"),
		adapter.table("task_orchestration_decisions"),
		adapter.table("task_orchestration_revisions"),
		adapter.table("task_orchestration_mandatory_audit_facts")),
		taskID.value, record.latestDecision.DecisionID.value,
	).Scan(&decisionTaskID, &requestID, &digestBytes, &previousRevision,
		&acceptedRevision, &committedAt, &decisionBytes, &projectionBytes, &auditFactID,
		&decisionCount, &revisionCount, &auditCount, &outboxCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return newPersistenceError(PersistenceStateCorrupt)
		}
		return err
	}
	decision := record.latestDecision
	if decisionTaskID != taskID.value || requestID != decision.DecisionRequestID.value ||
		len(digestBytes) != len(decision.CanonicalRequestDigest) ||
		!bytes.Equal(digestBytes, decision.CanonicalRequestDigest[:]) ||
		previousRevision != decision.PreviousTaskRevision ||
		acceptedRevision != decision.AcceptedTaskRevision ||
		!committedAt.Equal(decision.CommittedAt.Truncate(time.Microsecond)) ||
		auditFactID != decision.MandatoryAuditFactRef.AuditFactID.value ||
		decisionCount != record.decisionCount || revisionCount != uint64(record.revision) ||
		auditCount != record.decisionCount || outboxCount != record.enactmentCount {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	var persistedDecision postgresDecisionState
	var persistedProjection postgresTaskProjectionState
	if json.Unmarshal(decisionBytes, &persistedDecision) != nil ||
		json.Unmarshal(projectionBytes, &persistedProjection) != nil ||
		!reflect.DeepEqual(persistedDecision, postgresDecisionStateFromDecision(decision)) ||
		persistedProjection != postgresTaskProjectionStateFromProjection(decision.TaskProjection) {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	return nil
}

func (adapter *PostgresAdapter) overlayEvidenceDiagnostics(
	ctx context.Context,
	tx *sql.Tx,
	taskID TaskID,
	record *taskRecord,
) error {
	var count uint64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT count(*) FROM %s WHERE task_id=$1",
		adapter.table("task_orchestration_evidence_diagnostics"),
	), taskID.value).Scan(&count); err != nil {
		return err
	}
	if record.evidenceDiagnosticCount > count {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	if count == 0 {
		if record.evidenceDiagnosticCount != 0 {
			return newPersistenceError(PersistenceStateCorrupt)
		}
		return nil
	}
	var evidenceID string
	var disposition EvidenceDisposition
	var reason EvidenceDiagnosticReason
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT evidence_id, disposition, reason
		FROM %s WHERE task_id=$1 ORDER BY observation_sequence DESC LIMIT 1`,
		adapter.table("task_orchestration_evidence_diagnostics")), taskID.value,
	).Scan(&evidenceID, &disposition, &reason); err != nil {
		return err
	}
	latest := EvidenceDiagnostic{
		EvidenceID: EvidenceID{evidenceID}, Disposition: disposition, Reason: reason,
	}
	if !validOpaqueID(evidenceID) || disposition != EvidenceDispositionNonAuthoritative ||
		reason < EvidenceDiagnosticScopeConflict || reason > EvidenceDiagnosticUnauthorized ||
		record.evidenceDiagnosticCount == count && record.latestEvidenceDiagnostic != latest {
		return newPersistenceError(PersistenceStateCorrupt)
	}
	record.evidenceDiagnosticCount = count
	record.latestEvidenceDiagnostic = latest
	return nil
}

func (adapter *PostgresAdapter) InspectPersistence(
	ctx context.Context,
	query TaskQuery,
) (PersistenceView, error) {
	if ctx == nil || ctx.Err() != nil || !validOpaqueID(query.TaskID.value) ||
		!query.Authority.value.valid() || query.Authority.value.kind != AuthorityUser {
		return PersistenceView{}, newError(ErrorDependencyUnavailable)
	}
	tx, err := adapter.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return PersistenceView{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	record, exists, err := adapter.loadTaskReadOnly(ctx, tx, query.TaskID)
	if err != nil {
		return PersistenceView{}, normalizePersistenceFailure(err)
	}
	requestedOwner := userOwnershipBinding{
		authorityID: query.Authority.value.id, generation: query.Authority.value.generation,
	}
	if !exists || record.owner != requestedOwner {
		return PersistenceView{}, newError(ErrorAuthorizationDenied)
	}
	var view PersistenceView
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT task.revision,
		(SELECT count(*) FROM %s WHERE task_id=task.task_id),
		(SELECT count(*) FROM %s WHERE task_id=task.task_id),
		(SELECT count(*) FROM %s WHERE task_id=task.task_id),
		(SELECT count(*) FROM %s WHERE task_id=task.task_id),
		(SELECT count(*) FROM %s WHERE task_id=task.task_id),
		(SELECT count(*) FROM %s WHERE task_id=task.task_id),
		(SELECT count(*) FROM %s WHERE task_id=task.task_id),
		(SELECT count(*) FROM %s WHERE task_id=task.task_id)
		FROM %s AS task WHERE task.task_id=$1`,
		adapter.table("task_orchestration_decisions"),
		adapter.table("task_orchestration_revisions"),
		adapter.table("task_orchestration_mandatory_audit_facts"),
		adapter.table("task_orchestration_outbox"),
		adapter.table("task_orchestration_phase_runs"),
		adapter.table("task_orchestration_runtime_runs"),
		adapter.table("task_orchestration_evidence_refs"),
		adapter.table("task_orchestration_evidence_diagnostics"),
		adapter.table("task_orchestration_tasks")), query.TaskID.value,
	).Scan(&view.TaskRevision, &view.DecisionCount, &view.RevisionCount,
		&view.MandatoryAuditFactCount, &view.OutboxCount, &view.PhaseRunCount,
		&view.RuntimeRunCount, &view.EvidenceRefCount, &view.EvidenceDiagnosticCount)
	if err != nil {
		return PersistenceView{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return PersistenceView{}, newError(ErrorDependencyUnavailable)
	}
	return view, nil
}

func (adapter *PostgresAdapter) failAt(point PersistenceFaultPoint) bool {
	return adapter.faults != nil && adapter.faults.FailAt(point)
}

func normalizePersistenceFailure(err error) error {
	var persistenceError *PersistenceError
	if errors.As(err, &persistenceError) && persistenceError.Code() == PersistenceStateCorrupt {
		return persistenceError
	}
	return newError(ErrorDependencyUnavailable)
}
