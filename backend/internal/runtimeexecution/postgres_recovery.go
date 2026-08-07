package runtimeexecution

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PostgresRecoveryStore implements RecoveryStore on PostgreSQL.
type PostgresRecoveryStore struct {
	authority *PostgresAuthority
}

func newPostgresRecoveryStore(authority *PostgresAuthority) *PostgresRecoveryStore {
	return &PostgresRecoveryStore{authority: authority}
}

func (store *PostgresRecoveryStore) LoadRecoveryState(
	ctx context.Context,
) (*RecoveryState, error) {
	table := store.authority.table("runtime_execution_recovery_state")

	var (
		generation      RecoveryGeneration
		fence           RecoveryFence
		safetyEpoch     SafetyEpoch
		mode            uint8
		restoredAt      time.Time
		recoveryPointID string
	)

	row := store.authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		generation, fence, safety_epoch, mode, restored_at, recovery_point_id
		FROM %s ORDER BY generation DESC LIMIT 1`, table),
	)

	err := row.Scan(&generation, &fence, &safetyEpoch, &mode, &restoredAt, &recoveryPointID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}

	return &RecoveryState{
		Generation:      generation,
		Fence:           fence,
		SafetyEpoch:     safetyEpoch,
		Mode:            OperationalMode(mode),
		RestoredAt:      restoredAt,
		RecoveryPointID: recoveryPointID,
	}, nil
}

func (store *PostgresRecoveryStore) AdvanceRecoveryGeneration(
	ctx context.Context,
	decision RestoreDecision,
) error {
	now := postgresTimestamp(store.authority.now())
	table := store.authority.table("runtime_execution_recovery_state")

	_, err := store.authority.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		generation, fence, safety_epoch, mode, restored_at, recovery_point_id,
		previous_generation, previous_fence, previous_safety_epoch
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, table),
		decision.NewGeneration, decision.NewFence, decision.NewSafetyEpoch,
		int16(decision.Mode), now, decision.RecoveryPointID,
		decision.PreviousGeneration, decision.PreviousFence, decision.PreviousSafetyEpoch,
	)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (store *PostgresRecoveryStore) ListReconcilingRuntimes(
	ctx context.Context,
	beforeFence RuntimeFence,
) ([]RuntimeRunID, error) {
	table := store.authority.table("runtime_execution_runtimes")

	rows, err := store.authority.db.QueryContext(ctx, fmt.Sprintf(`SELECT runtime_run_id
		FROM %s WHERE runtime_state=$1 AND runtime_fence < $2`, table),
		int16(RuntimeReconciling), beforeFence,
	)
	if err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}
	defer rows.Close()

	var result []RuntimeRunID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, normalizeRuntimePersistenceFailure(err)
		}
		runID := RuntimeRunID{value: id}
		result = append(result, runID)
	}
	return result, rows.Err()
}

func (store *PostgresRecoveryStore) ListActiveLeaseRuntimes(
	ctx context.Context,
	beforeFence RuntimeFence,
) ([]RuntimeRunID, error) {
	table := store.authority.table("runtime_execution_runtimes")

	rows, err := store.authority.db.QueryContext(ctx, fmt.Sprintf(`SELECT runtime_run_id
		FROM %s WHERE runtime_state IN ($1,$2,$3,$4,$5) AND runtime_fence < $6`, table),
		int16(RuntimePreparingPrerequisites), int16(RuntimeStarting),
		int16(RuntimeRunning), int16(RuntimeStopping),
		int16(RuntimeReconciling), beforeFence,
	)
	if err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}
	defer rows.Close()

	var result []RuntimeRunID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, normalizeRuntimePersistenceFailure(err)
		}
		runID := RuntimeRunID{value: id}
		result = append(result, runID)
	}
	return result, rows.Err()
}

func (store *PostgresRecoveryStore) ClassifyRuntimeAfterRestore(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	decision RestoreDecision,
) (*RestoreRuntimeClassification, error) {
	stateResult, err := store.authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion:       SchemaV1,
		ProjectionVersion:   SnapshotSchemaCurrent,
		PersonalWorkspaceID: PersonalWorkspaceID{value: ""},
		RuntimeRunID:        runtimeRunID,
		Authority:           RuntimeAuthority{kind: AuthorityTaskOrchestration},
	})
	if err != nil {
		return nil, err
	}

	snapshot := stateResult
	classification := ClassifyPostRestore(
		snapshot.State,
		snapshot.Outcome,
		snapshot.Lease.AcquireStatus,
		snapshot.Lease.Disposition,
		snapshot.Worker.Status != WorkerOperationNone,
		snapshot.Capacity.NoLease == NoLeaseDispositionRecorded,
		snapshot.RuntimeFence,
		RuntimeFence(decision.NewFence),
	)

	runtimeClassification := &RestoreRuntimeClassification{
		RuntimeRunID:   runtimeRunID,
		Classification: classification,
		PreRestoreState: snapshot.State,
	}

	switch classification {
	case RestoreClassificationZeroLeaseRejected:
		runtimeClassification.PostRestoreState = RuntimeTerminal
		runtimeClassification.PostRestoreOutcome = RuntimeRejected
		runtimeClassification.NoLeaseDisposition = true
	case RestoreClassificationPossibleEffectLost:
		runtimeClassification.PostRestoreState = RuntimeTerminal
		runtimeClassification.PostRestoreOutcome = RuntimeLost
	case RestoreClassificationAmbiguousReconcile:
		runtimeClassification.PostRestoreState = RuntimeReconciling
	case RestoreClassificationAlreadyTerminal, RestoreClassificationFenced:
		runtimeClassification.PostRestoreState = snapshot.State
		runtimeClassification.PostRestoreOutcome = snapshot.Outcome
	}

	if runtimeClassification.PostRestoreState == RuntimeTerminal {
		runtimeClassification.CapacityReleased = true
	}

	return runtimeClassification, nil
}

func (store *PostgresRecoveryStore) FenceRuntime(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	fence RuntimeFence,
) error {
	now := postgresTimestamp(store.authority.now())
	table := store.authority.table("runtime_execution_runtimes")

	_, err := store.authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		runtime_fence=$1, updated_at=$2
		WHERE runtime_run_id=$3 AND runtime_fence < $1`, table),
		fence, now, runtimeRunID.String(),
	)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

// Ensure interface conformance.
var _ RecoveryStore = (*PostgresRecoveryStore)(nil)
