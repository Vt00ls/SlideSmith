package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const (
	postgresMandatoryAuditIntegrityVersion uint16 = 1
	postgresMandatoryAuditOwningModule            = "runtime_execution"
	postgresMandatoryAuditSourceClock             = "platform_control_plane"
	postgresMandatoryAuditDomain                  = "slidesmith.runtime-execution.mandatory-audit/v1\n"
)

type postgresMandatoryAuditAction uint8

const (
	postgresAuditStartAccepted postgresMandatoryAuditAction = iota + 1
	postgresAuditCancelAccepted
	postgresAuditReconciliationRequired
	postgresAuditPreLeaseTerminal
	postgresAuditLeaseCommitted
)

type postgresMandatoryAuditResult uint8

const (
	postgresAuditAccepted postgresMandatoryAuditResult = iota + 1
)

// postgresMandatoryAuditState is the complete, content-free authoritative
// AuditFact persisted with a protected Runtime Execution decision.
type postgresMandatoryAuditState struct {
	AuditFactID               string                       `json:"audit_fact_id"`
	SchemaVersion             SchemaVersion                `json:"schema_version"`
	IntegrityVersion          uint16                       `json:"integrity_version"`
	OwningModule              string                       `json:"owning_module"`
	DecisionID                string                       `json:"decision_id"`
	RuntimeRunID              string                       `json:"runtime_run_id"`
	OperationID               string                       `json:"operation_id"`
	RequestDigest             string                       `json:"request_digest"`
	AuthorityKind             AuthorityKind                `json:"authority_kind"`
	AuthorityID               string                       `json:"authority_id"`
	AuthorityGeneration       AuthorizationGeneration      `json:"authority_generation"`
	Action                    postgresMandatoryAuditAction `json:"action"`
	Result                    postgresMandatoryAuditResult `json:"result"`
	ReasonCode                uint8                        `json:"reason_code"`
	BeforeRevision            RuntimeRevision              `json:"before_revision"`
	AfterRevision             RuntimeRevision              `json:"after_revision"`
	BeforeState               RuntimeState                 `json:"before_state"`
	AfterState                RuntimeState                 `json:"after_state"`
	BeforeOperationGeneration OperationGeneration          `json:"before_operation_generation"`
	AfterOperationGeneration  OperationGeneration          `json:"after_operation_generation"`
	BeforeRuntimeFence        RuntimeFence                 `json:"before_runtime_fence"`
	AfterRuntimeFence         RuntimeFence                 `json:"after_runtime_fence"`
	PolicyEpoch               uint64                       `json:"policy_epoch"`
	AuthorizationEpoch        AuthorizationGeneration      `json:"authorization_epoch"`
	RecoveryGeneration        uint64                       `json:"recovery_generation"`
	BeforeSafetyEpoch         ReleaseSafetyEpoch           `json:"before_safety_epoch"`
	AfterSafetyEpoch          ReleaseSafetyEpoch           `json:"after_safety_epoch"`
	OccurredAt                string                       `json:"occurred_at"`
	RecordedAt                string                       `json:"recorded_at"`
	SourceClockID             string                       `json:"source_clock_id"`
	EvidenceRootID            string                       `json:"evidence_root_id"`
	EvidenceRootDigest        string                       `json:"evidence_root_digest"`
	ApprovalReference         string                       `json:"approval_reference"`
	IdempotencyReference      string                       `json:"idempotency_reference"`
	RetryDisposition          RetryDisposition             `json:"retry_disposition"`
	ReconciliationDisposition ReconciliationDisposition    `json:"reconciliation_disposition"`
	SupersedingAuditFactID    string                       `json:"superseding_audit_fact_id"`
	SafeErrorCode             ErrorCode                    `json:"safe_error_code"`
}

type postgresMandatoryAuditInput struct {
	AuditFactID               string
	Action                    postgresMandatoryAuditAction
	ReasonCode                uint8
	Decision                  RuntimeDecisionFact
	RuntimeRunID              RuntimeRunID
	RequestDigest             Digest
	Authority                 RuntimeAuthority
	BeforeState               RuntimeState
	AfterState                RuntimeState
	BeforeOperationGeneration OperationGeneration
	AfterOperationGeneration  OperationGeneration
	BeforeRuntimeFence        RuntimeFence
	AfterRuntimeFence         RuntimeFence
	PolicyEpoch               uint64
	BeforeSafetyEpoch         ReleaseSafetyEpoch
	AfterSafetyEpoch          ReleaseSafetyEpoch
	OccurredAt                time.Time
	RecordedAt                time.Time
	EvidenceRoot              EvidenceRootSnapshot
}

type postgresMandatoryAuditRef struct {
	DecisionID          RuntimeDecisionID
	PersonalWorkspaceID PersonalWorkspaceID
	RuntimeRunID        RuntimeRunID
	OperationID         OperationID
	RequestDigest       Digest
	Authority           RuntimeAuthority
}

type postgresMandatoryAuditView struct {
	State           postgresMandatoryAuditState
	CanonicalDigest Digest
	Projection      ProjectionFact
}

func (authority *PostgresAuthority) loadMandatoryAudit(
	ctx context.Context,
	ref postgresMandatoryAuditRef,
) (postgresMandatoryAuditView, error) {
	if ctx == nil || ctx.Err() != nil {
		return postgresMandatoryAuditView{}, newError(ErrorDependencyUnavailable)
	}
	if !validPostgresMandatoryAuditRef(ref) {
		return postgresMandatoryAuditView{}, newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return postgresMandatoryAuditView{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	runtime, err := authority.loadRuntimeForRead(ctx, tx, ref.RuntimeRunID)
	if err == sql.ErrNoRows || err == nil && !authorized(runtime, ref.PersonalWorkspaceID, ref.Authority) {
		return postgresMandatoryAuditView{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return postgresMandatoryAuditView{}, normalizeRuntimePersistenceFailure(err)
	}
	view, err := authority.loadMandatoryAuditInTransaction(ctx, tx, ref)
	if err != nil {
		return postgresMandatoryAuditView{}, err
	}
	if err := tx.Commit(); err != nil {
		return postgresMandatoryAuditView{}, normalizeRuntimePersistenceFailure(err)
	}
	return view, nil
}

func (authority *PostgresAuthority) loadMandatoryAuditInTransaction(
	ctx context.Context,
	tx *sql.Tx,
	ref postgresMandatoryAuditRef,
) (postgresMandatoryAuditView, error) {
	var auditFactID, runtimeRunID, operationID, authorityID string
	var owningModule, sourceClockID string
	var requestDigest, canonicalDigest, stateBytes []byte
	var schemaVersion SchemaVersion
	var integrityVersion uint16
	var authorityKind AuthorityKind
	var authorityGeneration AuthorizationGeneration
	var action postgresMandatoryAuditAction
	var result postgresMandatoryAuditResult
	var beforeRevision, afterRevision RuntimeRevision
	var occurredAt, recordedAt time.Time
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT audit_fact_id, runtime_run_id, operation_id,
		request_digest, schema_version, integrity_version, owning_module, canonical_digest,
		authority_kind, authority_id, authority_generation, action, result,
		before_revision, after_revision, occurred_at, recorded_at, source_clock_id, audit_state
		FROM %s WHERE decision_id=$1`, authority.table("runtime_execution_mandatory_audit")),
		ref.DecisionID.String()).Scan(
		&auditFactID, &runtimeRunID, &operationID, &requestDigest, &schemaVersion,
		&integrityVersion, &owningModule, &canonicalDigest, &authorityKind, &authorityID,
		&authorityGeneration, &action, &result, &beforeRevision, &afterRevision,
		&occurredAt, &recordedAt, &sourceClockID, &stateBytes,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return postgresMandatoryAuditView{}, newError(ErrorIntegrityConflict)
		}
		return postgresMandatoryAuditView{}, normalizeRuntimePersistenceFailure(err)
	}
	state, err := decodePostgresMandatoryAuditState(stateBytes)
	if err != nil {
		return postgresMandatoryAuditView{}, newError(ErrorIntegrityConflict)
	}
	wantDigest, err := state.canonicalDigest()
	if err != nil || auditFactID != state.AuditFactID || state.DecisionID != ref.DecisionID.String() ||
		runtimeRunID != ref.RuntimeRunID.String() || state.RuntimeRunID != ref.RuntimeRunID.String() ||
		operationID != ref.OperationID.String() || state.OperationID != ref.OperationID.String() ||
		!bytes.Equal(requestDigest, ref.RequestDigest[:]) || state.RequestDigest != ref.RequestDigest.String() ||
		schemaVersion != state.SchemaVersion || integrityVersion != state.IntegrityVersion ||
		owningModule != state.OwningModule || !bytes.Equal(canonicalDigest, wantDigest[:]) ||
		authorityKind != ref.Authority.kind || state.AuthorityKind != ref.Authority.kind ||
		authorityID != ref.Authority.id.String() || state.AuthorityID != ref.Authority.id.String() ||
		authorityGeneration != ref.Authority.generation || state.AuthorityGeneration != ref.Authority.generation ||
		action != state.Action || result != state.Result || beforeRevision != state.BeforeRevision ||
		afterRevision != state.AfterRevision || !occurredAt.Equal(mustParsePostgresAuditTime(state.OccurredAt)) ||
		!recordedAt.Equal(mustParsePostgresAuditTime(state.RecordedAt)) || sourceClockID != state.SourceClockID {
		return postgresMandatoryAuditView{}, newError(ErrorIntegrityConflict)
	}
	var projectionAuditFactID string
	var projectionAuditDigest []byte
	var projectionRevision RuntimeRevision
	var projectionSchema SchemaVersion
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT audit_fact_id, audit_canonical_digest,
		fact_revision, projection_schema_version FROM %s WHERE fact_id=$1`,
		authority.table("runtime_execution_projection_backlog")), ref.DecisionID.String()).Scan(
		&projectionAuditFactID, &projectionAuditDigest, &projectionRevision, &projectionSchema)
	if err != nil {
		if err == sql.ErrNoRows {
			return postgresMandatoryAuditView{}, newError(ErrorIntegrityConflict)
		}
		return postgresMandatoryAuditView{}, normalizeRuntimePersistenceFailure(err)
	}
	if projectionAuditFactID != auditFactID || !bytes.Equal(projectionAuditDigest, wantDigest[:]) ||
		projectionRevision != state.AfterRevision || projectionSchema != SchemaV1 {
		return postgresMandatoryAuditView{}, newError(ErrorIntegrityConflict)
	}
	return postgresMandatoryAuditView{
		State: state, CanonicalDigest: wantDigest,
		Projection: ProjectionFact{
			DecisionID: ref.DecisionID, RuntimeRunID: ref.RuntimeRunID, OperationID: ref.OperationID,
			CanonicalDigest: ref.RequestDigest, RuntimeRevision: state.AfterRevision,
			AuditFactID: state.AuditFactID, AuditCanonicalDigest: wantDigest,
			ProjectionSchemaVersion: projectionSchema,
		},
	}, nil
}

func validPostgresMandatoryAuditRef(ref postgresMandatoryAuditRef) bool {
	return validOpaqueID(ref.DecisionID.String()) && validOpaqueID(ref.PersonalWorkspaceID.String()) &&
		validOpaqueID(ref.RuntimeRunID.String()) && validOpaqueID(ref.OperationID.String()) &&
		ref.RequestDigest != (Digest{}) && validAuthority(ref.Authority)
}

func newPostgresMandatoryAuditState(input postgresMandatoryAuditInput) postgresMandatoryAuditState {
	occurredAt := postgresTimestamp(input.OccurredAt)
	recordedAt := postgresTimestamp(input.RecordedAt)
	return postgresMandatoryAuditState{
		AuditFactID: input.AuditFactID, SchemaVersion: SchemaV1,
		IntegrityVersion: postgresMandatoryAuditIntegrityVersion,
		OwningModule:     postgresMandatoryAuditOwningModule,
		DecisionID:       input.Decision.DecisionID.String(), RuntimeRunID: input.RuntimeRunID.String(),
		OperationID: input.Decision.OperationID.String(), RequestDigest: input.RequestDigest.String(),
		AuthorityKind: input.Authority.kind, AuthorityID: input.Authority.id.String(),
		AuthorityGeneration: input.Authority.generation,
		Action:              input.Action, Result: postgresAuditAccepted, ReasonCode: input.ReasonCode,
		BeforeRevision: input.Decision.PreviousRuntimeRevision,
		AfterRevision:  input.Decision.ResultingRuntimeRevision,
		BeforeState:    input.BeforeState, AfterState: input.AfterState,
		BeforeOperationGeneration: input.BeforeOperationGeneration,
		AfterOperationGeneration:  input.AfterOperationGeneration,
		BeforeRuntimeFence:        input.BeforeRuntimeFence,
		AfterRuntimeFence:         input.AfterRuntimeFence,
		PolicyEpoch:               input.PolicyEpoch, AuthorizationEpoch: input.Authority.generation,
		BeforeSafetyEpoch: input.BeforeSafetyEpoch, AfterSafetyEpoch: input.AfterSafetyEpoch,
		OccurredAt: occurredAt.Format(canonicalTimeFormat), RecordedAt: recordedAt.Format(canonicalTimeFormat),
		SourceClockID:        postgresMandatoryAuditSourceClock,
		EvidenceRootID:       input.EvidenceRoot.EvidenceRootID.String(),
		EvidenceRootDigest:   input.EvidenceRoot.Digest.String(),
		IdempotencyReference: input.Decision.OperationID.String(),
		RetryDisposition:     input.Decision.Retry, ReconciliationDisposition: input.Decision.Reconciliation,
	}
}

func (state postgresMandatoryAuditState) encode() ([]byte, error) {
	return json.Marshal(state)
}

func (state postgresMandatoryAuditState) canonicalDigest() (Digest, error) {
	encoded, err := state.encode()
	if err != nil {
		return Digest{}, err
	}
	return digestBytes(append([]byte(postgresMandatoryAuditDomain), encoded...)), nil
}

func decodePostgresMandatoryAuditState(encoded []byte) (postgresMandatoryAuditState, error) {
	var state postgresMandatoryAuditState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return postgresMandatoryAuditState{}, newPersistenceError(PersistenceStateCorrupt)
	}
	if err := ensureJSONEOF(decoder); err != nil || !validPostgresMandatoryAuditState(state) {
		return postgresMandatoryAuditState{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return state, nil
}

func validPostgresMandatoryAuditState(state postgresMandatoryAuditState) bool {
	if !validOpaqueID(state.AuditFactID) || state.SchemaVersion != SchemaV1 ||
		state.IntegrityVersion != postgresMandatoryAuditIntegrityVersion ||
		state.OwningModule != postgresMandatoryAuditOwningModule ||
		!validOpaqueID(state.DecisionID) || !validOpaqueID(state.RuntimeRunID) ||
		!validOpaqueID(state.OperationID) || !validDigestText(state.RequestDigest) ||
		!validAuthority(RuntimeAuthority{id: AuthorityID{value: state.AuthorityID}, generation: state.AuthorityGeneration, kind: state.AuthorityKind}) ||
		state.Action < postgresAuditStartAccepted || state.Action > postgresAuditLeaseCommitted ||
		state.Result != postgresAuditAccepted || state.BeforeRevision == 0 || state.AfterRevision == 0 ||
		!knownRuntimeState(state.BeforeState) || !knownRuntimeState(state.AfterState) ||
		state.BeforeOperationGeneration == 0 || state.AfterOperationGeneration == 0 ||
		state.BeforeRuntimeFence == 0 || state.AfterRuntimeFence == 0 ||
		state.AuthorizationEpoch != state.AuthorityGeneration ||
		state.BeforeSafetyEpoch == 0 || state.AfterSafetyEpoch == 0 ||
		state.SourceClockID != postgresMandatoryAuditSourceClock ||
		state.IdempotencyReference != state.OperationID ||
		state.SafeErrorCode != 0 ||
		state.RetryDisposition < RetryNever || state.RetryDisposition > RetryAfterDependency ||
		state.ReconciliationDisposition < ReconciliationNotRequired || state.ReconciliationDisposition > ReconciliationRequired {
		return false
	}
	switch state.Action {
	case postgresAuditStartAccepted:
		if state.ReasonCode != 0 {
			return false
		}
	case postgresAuditCancelAccepted:
		if state.ReasonCode < uint8(CancellationUserRequested) || state.ReasonCode > uint8(CancellationAdministratorRequested) {
			return false
		}
	case postgresAuditReconciliationRequired:
		if state.ReasonCode < uint8(ReconciliationTransportAmbiguous) || state.ReasonCode > uint8(ReconciliationProjectionDelivery) {
			return false
		}
	case postgresAuditPreLeaseTerminal:
		if !knownPreLeaseTerminalReason(PreLeaseTerminalReason(state.ReasonCode)) || state.ReasonCode == 0 {
			return false
		}
	case postgresAuditLeaseCommitted:
		if state.ReasonCode != 0 {
			return false
		}
	}
	occurredAt, err := time.Parse(canonicalTimeFormat, state.OccurredAt)
	if err != nil {
		return false
	}
	recordedAt, err := time.Parse(canonicalTimeFormat, state.RecordedAt)
	if err != nil || recordedAt.Before(occurredAt) {
		return false
	}
	if state.EvidenceRootID == "" {
		return state.EvidenceRootDigest == (Digest{}).String()
	}
	return validOpaqueID(state.EvidenceRootID) && validDigestText(state.EvidenceRootDigest)
}

func validDigestText(value string) bool {
	if len(value) != hex.EncodedLen(len(Digest{})) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == len(Digest{})
}
