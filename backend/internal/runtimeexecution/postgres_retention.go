package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"time"
)

type HeartbeatHistoryReason uint8

const (
	HeartbeatTerminalHistory HeartbeatHistoryReason = iota + 1
)

type heartbeatCompactionRequest struct {
	RuntimeRunID    RuntimeRunID
	LeaseID         SandboxLeaseID
	LeaseGeneration LeaseGeneration
	LeaseFence      LeaseFence
	Reason          HeartbeatHistoryReason
	EvidenceRoot    EvidenceRootSnapshot
}

type heartbeatCompactionView struct {
	RuntimeRunID                 RuntimeRunID
	LeaseID                      SandboxLeaseID
	LeaseGeneration              LeaseGeneration
	LeaseFence                   LeaseFence
	CompactedCount               uint64
	FirstObservedAt              time.Time
	LastObservedAt               time.Time
	AuthenticatedDigest          Digest
	PreservedConflictCount       uint64
	PreservedUncontainedCount    uint64
	PreservedReconciliationCount uint64
	OpenCleanupDebtCount         uint64
}

func (authority *PostgresAuthority) compactTerminalHeartbeatHistory(
	ctx context.Context,
	request heartbeatCompactionRequest,
) (heartbeatCompactionView, error) {
	if ctx == nil || ctx.Err() != nil {
		return heartbeatCompactionView{}, newError(ErrorDependencyUnavailable)
	}
	if !validOpaqueID(request.RuntimeRunID.String()) || !validOpaqueID(request.LeaseID.String()) ||
		request.LeaseGeneration == 0 || request.LeaseFence == 0 || request.Reason != HeartbeatTerminalHistory ||
		!knownEvidenceRoot(request.EvidenceRoot) || request.EvidenceRoot.EvidenceRootID == (EvidenceRootID{}) {
		return heartbeatCompactionView{}, newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, request.RuntimeRunID)
	if err == sql.ErrNoRows {
		return heartbeatCompactionView{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
	}
	if record.fixture.State != RuntimeTerminal || record.fixture.Outcome == RuntimeOutcomeNone ||
		record.lease.LeaseID != request.LeaseID || record.lease.Generation != request.LeaseGeneration ||
		record.lease.Fence != request.LeaseFence || record.evidenceRoot != request.EvidenceRoot {
		return heartbeatCompactionView{}, newError(ErrorIntegrityConflict)
	}
	var currentLeaseRoots int
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE runtime_run_id=$1
		AND lease_id=$2 AND lease_generation=$3 AND lease_fence=$4 AND current_fact
		AND evidence_root_id=$5 AND evidence_root_digest=$6`, authority.table("runtime_execution_lease_roots")),
		request.RuntimeRunID.String(), request.LeaseID.String(), request.LeaseGeneration, request.LeaseFence,
		request.EvidenceRoot.EvidenceRootID.String(), request.EvidenceRoot.Digest[:]).Scan(&currentLeaseRoots)
	if err != nil {
		return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
	}
	if currentLeaseRoots != 1 {
		return heartbeatCompactionView{}, newError(ErrorIntegrityConflict)
	}

	view, found, err := authority.loadHeartbeatCompaction(ctx, tx, request)
	if err != nil {
		return heartbeatCompactionView{}, err
	}
	if !found {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT observed_at FROM %s
			WHERE runtime_run_id=$1 AND lease_id=$2 AND lease_generation=$3 AND lease_fence=$4
			AND reason=$5 AND evidence_root_id=$6 AND evidence_root_digest=$7
			AND terminal_history AND NOT conflict AND NOT uncontained AND NOT unresolved_reconciliation
			ORDER BY heartbeat_sequence FOR UPDATE`, authority.table("runtime_execution_heartbeat_history")),
			request.RuntimeRunID.String(), request.LeaseID.String(), request.LeaseGeneration, request.LeaseFence,
			request.Reason, request.EvidenceRoot.EvidenceRootID.String(), request.EvidenceRoot.Digest[:])
		if err != nil {
			return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
		}
		var observations []time.Time
		for rows.Next() {
			var observedAt time.Time
			if err := rows.Scan(&observedAt); err != nil {
				_ = rows.Close()
				return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
			}
			observations = append(observations, observedAt.UTC())
		}
		if err := rows.Close(); err != nil {
			return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
		}
		if len(observations) > 0 {
			view = heartbeatCompactionView{
				RuntimeRunID: request.RuntimeRunID, LeaseID: request.LeaseID,
				LeaseGeneration: request.LeaseGeneration, LeaseFence: request.LeaseFence,
				CompactedCount: uint64(len(observations)), FirstObservedAt: observations[0],
				LastObservedAt: observations[len(observations)-1],
			}
			view.AuthenticatedDigest = heartbeatSummaryDigest(request, view)
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
				runtime_run_id, lease_id, lease_generation, lease_fence, reason,
				evidence_root_id, evidence_root_digest, first_observed_at, last_observed_at,
				observation_count, authenticated_digest
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, authority.table("runtime_execution_heartbeat_compaction")),
				request.RuntimeRunID.String(), request.LeaseID.String(), request.LeaseGeneration, request.LeaseFence,
				request.Reason, request.EvidenceRoot.EvidenceRootID.String(), request.EvidenceRoot.Digest[:],
				view.FirstObservedAt, view.LastObservedAt, view.CompactedCount, view.AuthenticatedDigest[:]); err != nil {
				return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
			}
			result, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s
				WHERE runtime_run_id=$1 AND lease_id=$2 AND lease_generation=$3 AND lease_fence=$4
				AND reason=$5 AND evidence_root_id=$6 AND evidence_root_digest=$7
				AND terminal_history AND NOT conflict AND NOT uncontained AND NOT unresolved_reconciliation`,
				authority.table("runtime_execution_heartbeat_history")), request.RuntimeRunID.String(), request.LeaseID.String(),
				request.LeaseGeneration, request.LeaseFence, request.Reason,
				request.EvidenceRoot.EvidenceRootID.String(), request.EvidenceRoot.Digest[:])
			if err != nil {
				return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
			}
			if deleted, err := result.RowsAffected(); err != nil || uint64(deleted) != view.CompactedCount {
				return heartbeatCompactionView{}, newError(ErrorIntegrityConflict)
			}
		}
	}
	if err := authority.addPreservedRetentionCounts(ctx, tx, request.RuntimeRunID, &view); err != nil {
		return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return heartbeatCompactionView{}, normalizeRuntimePersistenceFailure(err)
	}
	return view, nil
}

func (authority *PostgresAuthority) loadHeartbeatCompaction(
	ctx context.Context,
	tx *sql.Tx,
	request heartbeatCompactionRequest,
) (heartbeatCompactionView, bool, error) {
	view := heartbeatCompactionView{
		RuntimeRunID: request.RuntimeRunID, LeaseID: request.LeaseID,
		LeaseGeneration: request.LeaseGeneration, LeaseFence: request.LeaseFence,
	}
	var authenticated, evidenceRootDigest []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT first_observed_at, last_observed_at,
		observation_count, authenticated_digest, evidence_root_digest FROM %s WHERE runtime_run_id=$1 AND lease_id=$2
		AND lease_generation=$3 AND lease_fence=$4 AND reason=$5 AND evidence_root_id=$6`,
		authority.table("runtime_execution_heartbeat_compaction")), request.RuntimeRunID.String(), request.LeaseID.String(),
		request.LeaseGeneration, request.LeaseFence, request.Reason, request.EvidenceRoot.EvidenceRootID.String()).
		Scan(&view.FirstObservedAt, &view.LastObservedAt, &view.CompactedCount, &authenticated, &evidenceRootDigest)
	if err == sql.ErrNoRows {
		return view, false, nil
	}
	if err != nil {
		return heartbeatCompactionView{}, false, normalizeRuntimePersistenceFailure(err)
	}
	if len(authenticated) != len(view.AuthenticatedDigest) || !bytes.Equal(evidenceRootDigest, request.EvidenceRoot.Digest[:]) {
		return heartbeatCompactionView{}, false, newError(ErrorIntegrityConflict)
	}
	copy(view.AuthenticatedDigest[:], authenticated)
	view.FirstObservedAt = view.FirstObservedAt.UTC()
	view.LastObservedAt = view.LastObservedAt.UTC()
	if view.AuthenticatedDigest != heartbeatSummaryDigest(request, view) {
		return heartbeatCompactionView{}, false, newError(ErrorIntegrityConflict)
	}
	return view, true, nil
}

func (authority *PostgresAuthority) addPreservedRetentionCounts(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
	view *heartbeatCompactionView,
) error {
	return tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		(SELECT count(*) FROM %s WHERE runtime_run_id=$1 AND conflict),
		(SELECT count(*) FROM %s WHERE runtime_run_id=$1 AND uncontained),
		(SELECT count(*) FROM %s WHERE runtime_run_id=$1 AND unresolved_reconciliation),
		(SELECT count(*) FROM %s WHERE runtime_run_id=$1 AND unresolved)`,
		authority.table("runtime_execution_heartbeat_history"), authority.table("runtime_execution_heartbeat_history"),
		authority.table("runtime_execution_heartbeat_history"), authority.table("runtime_execution_cleanup_obligations")),
		runtimeRunID.String()).Scan(&view.PreservedConflictCount, &view.PreservedUncontainedCount,
		&view.PreservedReconciliationCount, &view.OpenCleanupDebtCount)
}

func heartbeatSummaryDigest(request heartbeatCompactionRequest, view heartbeatCompactionView) Digest {
	payload := fmt.Sprintf("slidesmith.runtime-execution.heartbeat-summary/v1\n%s\n%s\n%d\n%d\n%d\n%s\n%s\n%d\n%s\n%s",
		request.RuntimeRunID.String(), request.LeaseID.String(), request.LeaseGeneration, request.LeaseFence,
		request.Reason, request.EvidenceRoot.EvidenceRootID.String(), request.EvidenceRoot.Digest.String(),
		view.CompactedCount, view.FirstObservedAt.UTC().Format(canonicalTimeFormat),
		view.LastObservedAt.UTC().Format(canonicalTimeFormat))
	return digestBytes([]byte(payload))
}
