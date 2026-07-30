package runtimeexecution

import (
	"context"
	"database/sql"
	"time"
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

func (transaction *postgresQuotaReservationTransaction) ValidateQuotaReservation(
	ctx context.Context,
) (QuotaReservationValidationResult, error) {
	if transaction.called || ctx == nil || ctx.Err() != nil {
		return QuotaReservationValidationResult{}, newError(ErrorDependencyUnavailable)
	}
	fact := transaction.fact
	var expiresAt time.Time
	if err := transaction.tx.QueryRowContext(ctx, "SELECT "+transaction.function+`(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, fact.QuotaReservationID.String(), fact.Generation,
		fact.Mode, fact.PersonalWorkspaceID.String(), fact.TaskID.String(), fact.PhaseRunID.String(),
		fact.AuthorizationGeneration, fact.Capability, fact.GatewayRoutePolicyID.String(),
		fact.GatewayRoutePolicyGeneration, fact.CapabilityScope, fact.ValidAt).Scan(&expiresAt); err != nil {
		return QuotaReservationValidationResult{}, newError(ErrorIntegrityConflict)
	}
	transaction.called = true
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(fact.ValidAt) {
		return QuotaReservationValidationResult{}, newError(ErrorIntegrityConflict)
	}
	return QuotaReservationValidationResult{ExpiresAt: expiresAt}, nil
}

var _ SchedulerLeaseAttachmentTransaction = (*postgresSchedulerLeaseAttachmentTransaction)(nil)
var _ QuotaReservationValidationTransaction = (*postgresQuotaReservationTransaction)(nil)

func (authority *PostgresAuthority) validatePostgresQuotaReservation(
	ctx context.Context,
	tx *sql.Tx,
	start StartRuntimeRun,
	validAt time.Time,
) (QuotaReservationValidationResult, error) {
	if start.ProviderCapability == ProviderCapabilityNone {
		if start.ProviderBinding != nil {
			return QuotaReservationValidationResult{}, newError(ErrorIntegrityConflict)
		}
		return QuotaReservationValidationResult{}, nil
	}
	if start.ProviderCapability != ProviderCapabilityRequired || start.ProviderBinding == nil ||
		authority.quotaReservationParticipant == nil {
		return QuotaReservationValidationResult{}, newError(ErrorIntegrityConflict)
	}
	fact := QuotaReservationValidationFact{
		QuotaReservationID:           start.ProviderBinding.QuotaReservationID,
		Generation:                   start.ProviderBinding.Generation,
		Mode:                         start.ProviderBinding.Mode,
		PersonalWorkspaceID:          start.PersonalWorkspaceID,
		TaskID:                       start.TaskID,
		PhaseRunID:                   start.PhaseRunID,
		AuthorizationGeneration:      start.Authority.generation,
		Capability:                   start.ProviderCapability,
		GatewayRoutePolicyID:         start.ProviderBinding.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: start.ProviderBinding.GatewayRoutePolicyGeneration,
		CapabilityScope:              start.ProviderBinding.CapabilityScope,
		ValidAt:                      validAt.UTC(),
	}
	return authority.validatePostgresQuotaReservationFact(ctx, tx, fact)
}

func (authority *PostgresAuthority) validatePostgresQuotaReservationFact(
	ctx context.Context,
	tx *sql.Tx,
	fact QuotaReservationValidationFact,
) (QuotaReservationValidationResult, error) {
	if authority.quotaReservationParticipant == nil ||
		fact.Capability != ProviderCapabilityRequired || !validOpaqueID(fact.QuotaReservationID.String()) ||
		fact.Generation == 0 || quotaReservationModeName(fact.Mode) == "" ||
		!validOpaqueID(fact.PersonalWorkspaceID.String()) || !validOpaqueID(fact.TaskID.String()) ||
		!validOpaqueID(fact.PhaseRunID.String()) || fact.AuthorizationGeneration == 0 ||
		!validOpaqueID(fact.GatewayRoutePolicyID.String()) || fact.GatewayRoutePolicyGeneration == 0 ||
		fact.CapabilityScope == 0 || fact.CapabilityScope&^knownProviderCapabilityScope != 0 ||
		fact.ValidAt.IsZero() {
		return QuotaReservationValidationResult{}, newError(ErrorIntegrityConflict)
	}
	transaction := &postgresQuotaReservationTransaction{
		tx: tx, function: authority.quotaReservationFunction, fact: fact,
	}
	result, err := authority.quotaReservationParticipant.ParticipateQuotaReservation(ctx, transaction, fact)
	result.ExpiresAt = result.ExpiresAt.UTC()
	if err != nil || !transaction.called || !result.ExpiresAt.After(fact.ValidAt) {
		return QuotaReservationValidationResult{}, newError(ErrorIntegrityConflict)
	}
	return result, nil
}
