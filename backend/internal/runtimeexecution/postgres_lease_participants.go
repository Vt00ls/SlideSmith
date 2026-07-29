package runtimeexecution

import (
	"context"
	"database/sql"
)

type postgresSchedulerLeaseAttachmentTransaction struct {
	tx       *sql.Tx
	function string
	fact     SchedulerLeaseAttachmentFact
	called   bool
}

func (transaction *postgresSchedulerLeaseAttachmentTransaction) AttachLease(ctx context.Context) error {
	if transaction.called || ctx == nil || ctx.Err() != nil {
		return newError(ErrorDependencyUnavailable)
	}
	fact := transaction.fact
	if _, err := transaction.tx.ExecContext(ctx, "SELECT "+transaction.function+`(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		fact.WorkItemID.String(), fact.AdmissionGrantID.String(), fact.GrantGeneration,
		fact.RuntimeRunID.String(), fact.StartOperationID.String(), fact.StartDigest[:],
		fact.RuntimeRevision, fact.RuntimeFence, fact.LeaseAcquireOperationID.String(),
		fact.LeaseAcquireDigest[:], fact.SandboxLeaseID.String(), fact.LeaseGeneration,
		fact.LeaseFence, fact.SandboxID.String(), fact.SandboxGeneration, fact.SandboxFence,
		fact.ExecutionNodeID.String(), fact.NodeCapacityGeneration,
		fact.ResourceClassID.String(), fact.ExecutionPolicyID.String(), fact.SchedulerEpoch,
		fact.PolicyVersion, fact.AttachedAt); err != nil {
		return newError(ErrorIntegrityConflict)
	}
	transaction.called = true
	return nil
}

type postgresQuotaReservationTransaction struct {
	tx       *sql.Tx
	function string
	fact     QuotaReservationValidationFact
	called   bool
}

func (transaction *postgresQuotaReservationTransaction) ValidateQuotaReservation(ctx context.Context) error {
	if transaction.called || ctx == nil || ctx.Err() != nil {
		return newError(ErrorDependencyUnavailable)
	}
	fact := transaction.fact
	if _, err := transaction.tx.ExecContext(ctx, "SELECT "+transaction.function+`(
		$1,$2,$3,$4,$5,$6,$7)`, fact.QuotaReservationID.String(), fact.Generation,
		fact.Mode, fact.PersonalWorkspaceID.String(), fact.PhaseRunID.String(),
		fact.Capability, fact.ValidAt); err != nil {
		return newError(ErrorIntegrityConflict)
	}
	transaction.called = true
	return nil
}

var _ SchedulerLeaseAttachmentTransaction = (*postgresSchedulerLeaseAttachmentTransaction)(nil)
var _ QuotaReservationValidationTransaction = (*postgresQuotaReservationTransaction)(nil)
