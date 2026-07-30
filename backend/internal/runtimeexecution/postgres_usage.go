package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

func (authority *PostgresAuthority) advancePostgresUsageEvidence(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	if decision.Snapshot.State == RuntimeTerminal &&
		decision.Snapshot.Gateway == (GatewayPrerequisiteSnapshot{}) &&
		decision.Snapshot.Usage == (RuntimeUsageEvidenceSnapshot{}) {
		return decision, nil
	}
	if decision.Snapshot.Gateway.Applicability == GatewayPrerequisiteNotApplicable {
		updated := RuntimeUsageEvidenceSnapshot{Disposition: UsageEvidenceNotApplicable}
		snapshot, err := authority.persistPostgresUsageEvidence(ctx, runtimeRunID, updated, nil)
		if err != nil {
			return RuntimeDecision{}, err
		}
		decision.Snapshot = snapshot
		return decision, nil
	}
	if decision.Snapshot.Gateway.Applicability != GatewayPrerequisiteRequired {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if authority.usageReceipts == nil {
		if decision.Snapshot.Usage == (RuntimeUsageEvidenceSnapshot{}) {
			updated := RuntimeUsageEvidenceSnapshot{Disposition: UsageEvidenceMissing}
			snapshot, err := authority.persistPostgresUsageEvidence(ctx, runtimeRunID, updated, nil)
			if err != nil {
				return RuntimeDecision{}, err
			}
			decision.Snapshot = snapshot
		}
		return decision, nil
	}
	evidence, err := authority.usageReceipts.QueryUsageReceiptEvidence(ctx, runtimeRunID)
	if err != nil {
		if decision.Snapshot.Usage.Disposition != UsageEvidenceMissing {
			return decision, nil
		}
		updated := RuntimeUsageEvidenceSnapshot{Disposition: UsageEvidenceUnknown}
		snapshot, persistErr := authority.persistPostgresUsageEvidence(ctx, runtimeRunID, updated, nil)
		if persistErr != nil {
			return RuntimeDecision{}, persistErr
		}
		decision.Snapshot = snapshot
		return decision, nil
	}
	set, err := NewUsageReceiptReferenceSet(evidence.References)
	if err != nil {
		return RuntimeDecision{}, err
	}
	updated := RuntimeUsageEvidenceSnapshot{Disposition: evidence.Disposition, Receipts: set}
	if !knownRuntimeUsageEvidence(updated) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	snapshot, err := authority.persistPostgresUsageEvidence(ctx, runtimeRunID, updated, evidence.References)
	if err != nil {
		return RuntimeDecision{}, err
	}
	decision.Snapshot = snapshot
	return decision, nil
}

func (authority *PostgresAuthority) persistPostgresUsageEvidence(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	updated RuntimeUsageEvidenceSnapshot,
	references []UsageReceiptReference,
) (RuntimeSnapshot, error) {
	if !knownRuntimeUsageEvidence(updated) {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, runtimeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if record.gateway.Applicability == GatewayPrerequisiteNotApplicable {
		if updated.Disposition != UsageEvidenceNotApplicable || len(references) != 0 {
			return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
		}
	} else if record.gateway.Applicability != GatewayPrerequisiteRequired ||
		updated.Disposition == UsageEvidenceNotApplicable {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeTerminal && record.gateway.Applicability == GatewayPrerequisiteRequired {
		record.gateway.Status = GatewayGrantStale
		record.gateway.Ready = false
	}
	recordedAt := postgresTimestamp(authority.now())
	for _, reference := range references {
		if !validUsageReceiptReference(reference) {
			return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			usage_receipt_id, runtime_run_id, gateway_attempt_id, disposition, canonical_digest, recorded_at
		) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (usage_receipt_id) DO NOTHING`,
			authority.table("runtime_execution_usage_receipts")), reference.UsageReceiptID.String(),
			runtimeRunID.String(), reference.GatewayAttemptID.String(), reference.Disposition,
			reference.CanonicalDigest[:], recordedAt); err != nil {
			return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
		}
		var retainedRuntime, retainedAttempt string
		var retainedDisposition UsageEvidenceDisposition
		var retainedDigest []byte
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT runtime_run_id, gateway_attempt_id,
			disposition, canonical_digest FROM %s WHERE usage_receipt_id=$1`,
			authority.table("runtime_execution_usage_receipts")), reference.UsageReceiptID.String()).Scan(
			&retainedRuntime, &retainedAttempt, &retainedDisposition, &retainedDigest); err != nil ||
			retainedRuntime != runtimeRunID.String() || retainedAttempt != reference.GatewayAttemptID.String() ||
			retainedDisposition != reference.Disposition || !bytes.Equal(retainedDigest, reference.CanonicalDigest[:]) {
			return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
		}
	}
	retainedReferences, err := authority.loadPostgresUsageReceiptReferences(ctx, tx, runtimeRunID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	retainedSet, err := NewUsageReceiptReferenceSet(retainedReferences)
	if err != nil || retainedSet != updated.Receipts {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if updated.Receipts.Count > 0 {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			runtime_run_id, disposition, receipt_count, receipt_root, recorded_at
		) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (runtime_run_id, receipt_root) DO NOTHING`,
			authority.table("runtime_execution_usage_evidence_roots")), runtimeRunID.String(),
			updated.Disposition, updated.Receipts.Count, updated.Receipts.RootDigest[:], recordedAt); err != nil {
			return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
		}
		var disposition UsageEvidenceDisposition
		var count uint64
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT disposition, receipt_count FROM %s
			WHERE runtime_run_id=$1 AND receipt_root=$2`,
			authority.table("runtime_execution_usage_evidence_roots")), runtimeRunID.String(),
			updated.Receipts.RootDigest[:]).Scan(&disposition, &count); err != nil ||
			disposition != updated.Disposition || count != updated.Receipts.Count {
			return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
		}
	}
	record.usage = updated
	if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
		return RuntimeSnapshot{}, err
	}
	if authority.failAt(PersistenceFaultBeforeUsageEvidenceCommit) {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterUsageEvidenceCommit) {
		return RuntimeSnapshot{}, newError(ErrorReconciliationRequired)
	}
	return snapshot(record, SnapshotSchemaCurrent), nil
}

func (authority *PostgresAuthority) loadPostgresUsageReceiptReferences(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
) ([]UsageReceiptReference, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT usage_receipt_id, gateway_attempt_id,
		disposition, canonical_digest FROM %s WHERE runtime_run_id=$1 ORDER BY usage_receipt_id`,
		authority.table("runtime_execution_usage_receipts")), runtimeRunID.String())
	if err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}
	defer rows.Close()
	references := make([]UsageReceiptReference, 0)
	for rows.Next() {
		var receiptID, attemptID string
		var disposition UsageEvidenceDisposition
		var digestBytes []byte
		if err := rows.Scan(&receiptID, &attemptID, &disposition, &digestBytes); err != nil || len(digestBytes) != len(Digest{}) {
			return nil, newError(ErrorIntegrityConflict)
		}
		var digest Digest
		copy(digest[:], digestBytes)
		reference := UsageReceiptReference{
			UsageReceiptID: UsageReceiptID{value: receiptID}, GatewayAttemptID: GatewayAttemptID{value: attemptID},
			Disposition: disposition, CanonicalDigest: digest,
		}
		if !validUsageReceiptReference(reference) {
			return nil, newError(ErrorIntegrityConflict)
		}
		references = append(references, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}
	sort.Slice(references, func(left, right int) bool {
		return references[left].UsageReceiptID.String() < references[right].UsageReceiptID.String()
	})
	return references, nil
}
