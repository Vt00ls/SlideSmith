package runtimeexecution

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PostgresReconciliationStore implements ReconciliationStore on PostgreSQL.
type PostgresReconciliationStore struct {
	authority *PostgresAuthority
}

func newPostgresReconciliationStore(authority *PostgresAuthority) *PostgresReconciliationStore {
	return &PostgresReconciliationStore{authority: authority}
}

func (store *PostgresReconciliationStore) LoadReconciliationObligation(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
) (*ReconciliationObligation, error) {
	table := store.authority.table("runtime_execution_reconciliation_obligations")

	var (
		operationID, decisionID, evidenceRootID string
		authorityKind                           int16
		authorityIDStr                          string
		authorityGeneration                     AuthorizationGeneration
		runtimeRevision                         RuntimeRevision
		operationGeneration                     OperationGeneration
		runtimeFence                            RuntimeFence
		reason                                  uint8
		status                                  uint8
		result                                  uint8
		firstRecordedAt                         time.Time
		lastRecordedAt                          time.Time
		observationCount                        uint64
		unresolved                              bool
		nextRetryAt                             sql.NullTime
		safeFailureCount                        uint64
		staleEvidenceCount                      uint64
		evidenceRootDigest                      []byte
		diagnosticJSON                          sql.NullString
	)

	row := store.authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		operation_id, decision_id, owner_authority_kind, owner_authority_id,
		owner_authority_generation, runtime_revision, operation_generation,
		runtime_fence, reason, status, result,
		first_recorded_at, last_recorded_at, observation_count, unresolved,
		next_retry_at, safe_failure_count, stale_evidence_count,
		evidence_root_id, evidence_root_digest, diagnostic_observations
		FROM %s WHERE runtime_run_id=$1 AND unresolved=true
		ORDER BY last_recorded_at DESC LIMIT 1`, table),
		runtimeRunID.String(),
	)

	err := row.Scan(
		&operationID, &decisionID, &authorityKind, &authorityIDStr,
		&authorityGeneration, &runtimeRevision, &operationGeneration,
		&runtimeFence, &reason, &status, &result,
		&firstRecordedAt, &lastRecordedAt, &observationCount, &unresolved,
		&nextRetryAt, &safeFailureCount, &staleEvidenceCount,
		&evidenceRootID, &evidenceRootDigest, &diagnosticJSON,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}

	authID, _ := NewAuthorityID(authorityIDStr)
	opID, _ := NewOperationID(operationID)
	decID := RuntimeDecisionID{value: decisionID}
	evID, _ := NewEvidenceID(evidenceRootID)

	obligation := &ReconciliationObligation{
		RuntimeRunID:        runtimeRunID,
		OperationID:         opID,
		DecisionID:          decID,
		AuthorityKind:       AuthorityKind(authorityKind),
		AuthorityID:         authID,
		AuthorityGeneration: authorityGeneration,
		RuntimeRevision:     runtimeRevision,
		OperationGeneration: operationGeneration,
		RuntimeFence:        runtimeFence,
		Reason:              ReconciliationReason(reason),
		Status:              ReconciliationStatus(status),
		Result:              ReconcilingResult(result),
		FirstRecordedAt:     firstRecordedAt,
		LastRecordedAt:      lastRecordedAt,
		ObservationCount:    observationCount,
		Unresolved:          unresolved,
		SafeFailureCount:    safeFailureCount,
		StaleEvidenceCount:  staleEvidenceCount,
		EvidenceRootID:      evID,
	}
	if len(evidenceRootDigest) == 32 {
		copy(obligation.EvidenceRootDigest[:], evidenceRootDigest)
	}
	if nextRetryAt.Valid {
		obligation.NextRetryAt = nextRetryAt.Time
	}
	if diagnosticJSON.Valid && diagnosticJSON.String != "" {
		var diag []ReconciliationObservation
		if err := json.Unmarshal([]byte(diagnosticJSON.String), &diag); err == nil {
			obligation.DiagnosticObservations = diag
		}
	}
	return obligation, nil
}

func (store *PostgresReconciliationStore) RecordReconciliationObservation(
	ctx context.Context,
	obligation ReconciliationObligation,
	observation ReconciliationObservation,
) error {
	now := postgresTimestamp(store.authority.now())
	table := store.authority.table("runtime_execution_reconciliation_obligations")

	diagnosticJSON, err := json.Marshal(obligation.DiagnosticObservations)
	if err != nil {
		return newError(ErrorInvalidRequest)
	}

	var nextRetryAt interface{}
	if !obligation.NextRetryAt.IsZero() {
		nextRetryAt = obligation.NextRetryAt
	}

	// Try UPDATE first (obligation already exists).
	result, dbErr := store.authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		observation_count=$1, last_recorded_at=$2, safe_failure_count=$3,
		stale_evidence_count=$4, next_retry_at=$5, diagnostic_observations=$6,
		status=$7, result=$8
		WHERE operation_id=$9 AND unresolved=true`, table),
		obligation.ObservationCount, now, obligation.SafeFailureCount,
		obligation.StaleEvidenceCount, nextRetryAt, string(diagnosticJSON),
		int16(obligation.Status), uint8(obligation.Result),
		obligation.OperationID.String(),
	)
	if dbErr != nil {
		return normalizeRuntimePersistenceFailure(dbErr)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		// INSERT new obligation.
		var evidenceDigest interface{}
		if obligation.EvidenceRootDigest != (Digest{}) {
			evidenceDigest = obligation.EvidenceRootDigest[:]
		}
		_, dbErr = store.authority.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			operation_id, runtime_run_id, decision_id, owner_authority_kind,
			owner_authority_id, owner_authority_generation, runtime_revision,
			operation_generation, runtime_fence, reason, status, result,
			first_recorded_at, last_recorded_at, observation_count, unresolved,
			next_retry_at, safe_failure_count, stale_evidence_count,
			evidence_root_id, evidence_root_digest, diagnostic_observations
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		ON CONFLICT (operation_id) DO UPDATE SET
			observation_count=EXCLUDED.observation_count,
			last_recorded_at=EXCLUDED.last_recorded_at,
			safe_failure_count=EXCLUDED.safe_failure_count,
			stale_evidence_count=EXCLUDED.stale_evidence_count,
			next_retry_at=EXCLUDED.next_retry_at,
			diagnostic_observations=EXCLUDED.diagnostic_observations,
			status=EXCLUDED.status,
			result=EXCLUDED.result`, table),
			obligation.OperationID.String(), obligation.RuntimeRunID.String(),
			obligation.DecisionID.String(), int16(obligation.AuthorityKind),
			obligation.AuthorityID.String(), obligation.AuthorityGeneration,
			obligation.RuntimeRevision, obligation.OperationGeneration,
			obligation.RuntimeFence, uint8(obligation.Reason),
			int16(obligation.Status), uint8(obligation.Result),
			now, now, obligation.ObservationCount, obligation.Unresolved,
			nextRetryAt, obligation.SafeFailureCount, obligation.StaleEvidenceCount,
			obligation.EvidenceRootID.String(), evidenceDigest,
			string(diagnosticJSON),
		)
		if dbErr != nil {
			return normalizeRuntimePersistenceFailure(dbErr)
		}
	}
	return nil
}

func (store *PostgresReconciliationStore) ResolveReconciliation(
	ctx context.Context,
	obligation ReconciliationObligation,
	result ReconcilingResult,
) error {
	now := postgresTimestamp(store.authority.now())
	table := store.authority.table("runtime_execution_reconciliation_obligations")

	_, dbErr := store.authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		status=$1, result=$2, unresolved=false, last_recorded_at=$3,
		next_retry_at=NULL
		WHERE operation_id=$4 AND unresolved=true`, table),
		int16(ReconciliationStable), uint8(result), now,
		obligation.OperationID.String(),
	)
	if dbErr != nil {
		return normalizeRuntimePersistenceFailure(dbErr)
	}
	return nil
}

func (store *PostgresReconciliationStore) LoadRuntimeSnapshot(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
) (RuntimeSnapshot, error) {
	return store.authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion:       SchemaV1,
		ProjectionVersion:   SnapshotSchemaCurrent,
		PersonalWorkspaceID: PersonalWorkspaceID{value: ""},
		RuntimeRunID:        runtimeRunID,
		Authority:           RuntimeAuthority{kind: AuthorityTaskOrchestration},
	})
}

func (store *PostgresReconciliationStore) ReplayExactCommand(
	ctx context.Context,
	command RuntimeCommand,
) (RuntimeDecision, error) {
	return store.authority.Execute(ctx, command)
}

// Ensure interface conformance.
var _ ReconciliationStore = (*PostgresReconciliationStore)(nil)
