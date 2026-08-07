package runtimeexecution

import (
	"context"
	"database/sql"
	"fmt"
)

// PostgresAuthority implements the read-only OperationalDiagnostics seam. It
// only reads exact identities and never enumerates the debt, node, or runtime
// population. Every view is content-free and bounded.

func (authority *PostgresAuthority) Diagnose(
	ctx context.Context,
	query OperationalDiagnosticQuery,
) (OperationalDiagnosticView, error) {
	if ctx == nil || ctx.Err() != nil {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	if !validOperationalDiagnosticQuery(query) {
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	switch query.Lookup {
	case DiagnosticLookupCleanupDebt:
		tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		defer func() { _ = tx.Rollback() }()
		record, found, err := authority.loadCleanupDebtRow(ctx, tx, query.DebtID, false)
		if err != nil {
			return OperationalDiagnosticView{}, err
		}
		if !found || record.PersonalWorkspaceID != query.PersonalWorkspaceID ||
			record.RuntimeRunID != query.RuntimeRunID {
			return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
		}
		runtime, err := authority.loadRuntimeForRead(ctx, tx, record.RuntimeRunID)
		if err == sql.ErrNoRows || err == nil &&
			runtime.fixture.PersonalWorkspaceID != query.PersonalWorkspaceID {
			return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
		}
		if err != nil {
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		if err := tx.Commit(); err != nil {
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		return OperationalDiagnosticView{
			Lookup: query.Lookup,
			Debt: &CleanupDebtDiagnosticView{
				DebtID: record.DebtID, DebtRevision: record.Revision, OwnerModule: record.OwnerModule,
				ResourceClass: record.ResourceClass, Status: record.Status, Unresolved: record.Unresolved,
				RetryDisposition: record.RetryDisposition, LastError: record.LastErrorCategory,
				EstimateState: record.Estimation.State, Blockers: record.Blockers.Classes,
				ResolutionClass: record.ResolutionClass, ExceptionUntil: record.ResolutionExpiresAt,
				AttemptCount: record.AttemptCount,
			},
		}, nil
	case DiagnosticLookupExecutionNode:
		tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		defer func() { _ = tx.Rollback() }()
		var node postgresExecutionNodeRecord
		err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT execution_node_id, node_generation, readiness,
			attestation_id, attestation_generation, attested_at, expires_at, resource_class_id,
			execution_policy_id, node_authority_id, worker_authority_id, worker_generation,
			authorization_generation, authorization_expires_at, release_safety_epoch,
			catalog_safety_epoch, occupancy, quarantined, containment, reset_status
			FROM %s WHERE execution_node_id=$1`, authority.table("runtime_execution_nodes")),
			query.ExecutionNodeID.String()).Scan(
			&node.ExecutionNodeID.value, &node.Generation, &node.Readiness, &node.AttestationID.value,
			&node.AttestationGeneration, &node.AttestedAt, &node.ExpiresAt, &node.ResourceClassID.value,
			&node.ExecutionPolicyID.value, &node.NodeAuthorityID.value, &node.WorkerAuthorityID.value,
			&node.WorkerGeneration, &node.AuthorizationGeneration, &node.AuthorizationExpiresAt,
			&node.ReleaseSafetyEpoch, &node.CatalogSafetyEpoch, &node.Occupancy, &node.Quarantined,
			&node.Containment, &node.Reset)
		if err != nil {
			if err == sql.ErrNoRows {
				return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
			}
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		if err := tx.Commit(); err != nil {
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		return OperationalDiagnosticView{
			Lookup: query.Lookup,
			Node: &NodeDiagnosticView{
				ExecutionNodeID: node.ExecutionNodeID, Generation: node.Generation,
				Readiness: node.Readiness, Occupancy: node.Occupancy, Quarantined: node.Quarantined,
				Containment: node.Containment, Reset: node.Reset,
			},
		}, nil
	case DiagnosticLookupRuntimeLease:
		tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		defer func() { _ = tx.Rollback() }()
		record, err := authority.loadRuntimeForRead(ctx, tx, query.RuntimeRunID)
		if err == sql.ErrNoRows || err == nil &&
			record.fixture.PersonalWorkspaceID != query.PersonalWorkspaceID {
			return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
		}
		if err != nil {
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		projected, representable := renderSnapshot(record, SnapshotSchemaCurrent)
		if !representable {
			return OperationalDiagnosticView{}, newError(ErrorIntegrityConflict)
		}
		if err := tx.Commit(); err != nil {
			return OperationalDiagnosticView{}, normalizeRuntimePersistenceFailure(err)
		}
		return OperationalDiagnosticView{
			Lookup: query.Lookup,
			Runtime: &RuntimeDiagnosticView{
				RuntimeRunID: projected.RuntimeRunID, RuntimeRevision: projected.RuntimeRevision,
				State: projected.State, Outcome: projected.Outcome,
				LeaseDisposition: projected.Lease.Disposition, Physical: projected.Capacity.Physical,
				Cleanup: projected.Cleanup.Status, Quarantined: projected.Node.Quarantined,
				Containment: projected.Node.Containment, Reset: projected.Node.Reset,
			},
		}, nil
	default:
		return OperationalDiagnosticView{}, newError(ErrorInvalidRequest)
	}
}

var _ OperationalDiagnostics = (*PostgresAuthority)(nil)
