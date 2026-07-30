package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type postgresGatewayGrantRequestState struct {
	Kind                         GatewayGrantRequestKind      `json:"kind"`
	OperationID                  string                       `json:"operation_id"`
	CanonicalRequestDigest       Digest                       `json:"canonical_request_digest"`
	PersonalWorkspaceID          string                       `json:"personal_workspace_id"`
	TaskID                       string                       `json:"task_id"`
	PhaseRunID                   string                       `json:"phase_run_id"`
	RuntimeRunID                 string                       `json:"runtime_run_id"`
	StartOperationID             string                       `json:"start_operation_id"`
	RuntimeBindingID             string                       `json:"runtime_binding_id"`
	RuntimeBindingDigest         Digest                       `json:"runtime_binding_digest"`
	ReleaseSafetyEpoch           ReleaseSafetyEpoch           `json:"release_safety_epoch"`
	LeaseID                      string                       `json:"lease_id"`
	LeaseGeneration              LeaseGeneration              `json:"lease_generation"`
	LeaseFence                   LeaseFence                   `json:"lease_fence"`
	RuntimeFence                 RuntimeFence                 `json:"runtime_fence"`
	QuotaReservationID           string                       `json:"quota_reservation_id"`
	QuotaReservationGeneration   QuotaReservationGeneration   `json:"quota_reservation_generation"`
	QuotaReservationMode         QuotaReservationMode         `json:"quota_reservation_mode"`
	OwnerAuthorityGeneration     AuthorizationGeneration      `json:"owner_authority_generation"`
	AuthorizationGeneration      AuthorizationGeneration      `json:"authorization_generation"`
	GatewayRoutePolicyID         string                       `json:"gateway_route_policy_id"`
	GatewayRoutePolicyGeneration GatewayRoutePolicyGeneration `json:"gateway_route_policy_generation"`
	CapabilityScope              ProviderCapabilityScope      `json:"capability_scope"`
	RecoveryGeneration           GatewayRecoveryGeneration    `json:"recovery_generation"`
	RecoveryMode                 GatewayRecoveryMode          `json:"recovery_mode"`
	RequestedGeneration          GatewayGrantGeneration       `json:"requested_generation"`
	PreviousGeneration           GatewayGrantGeneration       `json:"previous_generation"`
	PreviousGrantID              string                       `json:"previous_grant_id"`
	RuntimeDeadline              time.Time                    `json:"runtime_deadline"`
	LeaseExpiresAt               time.Time                    `json:"lease_expires_at"`
	AuthorizationExpiresAt       time.Time                    `json:"authorization_expires_at"`
	ReservationExpiresAt         time.Time                    `json:"reservation_expires_at"`
	RoutePolicyExpiresAt         time.Time                    `json:"route_policy_expires_at"`
	RecoveryExpiresAt            time.Time                    `json:"recovery_expires_at"`
	NotAfter                     time.Time                    `json:"not_after"`
}

type postgresGatewayGrantDecisionState struct {
	OperationID            string                          `json:"operation_id"`
	CanonicalRequestDigest Digest                          `json:"canonical_request_digest"`
	Disposition            GatewayGrantDecisionDisposition `json:"disposition"`
	Grant                  postgresGatewayGrantState       `json:"grant"`
}

func encodePostgresGatewayGrantRequest(request GatewayGrantRequest) ([]byte, error) {
	return json.Marshal(postgresGatewayGrantRequestState{
		Kind: request.Kind, OperationID: request.OperationID.String(),
		CanonicalRequestDigest: request.CanonicalRequestDigest,
		PersonalWorkspaceID:    request.PersonalWorkspaceID.String(), TaskID: request.TaskID.String(),
		PhaseRunID: request.PhaseRunID.String(), RuntimeRunID: request.RuntimeRunID.String(),
		StartOperationID: request.StartOperationID.String(), LeaseID: request.LeaseID.String(),
		RuntimeBindingID: request.RuntimeBindingID.String(), RuntimeBindingDigest: request.RuntimeBindingDigest,
		ReleaseSafetyEpoch: request.ReleaseSafetyEpoch,
		LeaseGeneration:    request.LeaseGeneration, LeaseFence: request.LeaseFence, RuntimeFence: request.RuntimeFence,
		QuotaReservationID:         request.QuotaReservationID.String(),
		QuotaReservationGeneration: request.QuotaReservationGeneration, QuotaReservationMode: request.QuotaReservationMode,
		OwnerAuthorityGeneration:     request.OwnerAuthorityGeneration,
		AuthorizationGeneration:      request.AuthorizationGeneration,
		GatewayRoutePolicyID:         request.GatewayRoutePolicyID.String(),
		GatewayRoutePolicyGeneration: request.GatewayRoutePolicyGeneration,
		CapabilityScope:              request.CapabilityScope, RecoveryGeneration: request.RecoveryGeneration,
		RecoveryMode: request.RecoveryMode, RequestedGeneration: request.RequestedGeneration,
		PreviousGeneration: request.PreviousGeneration, PreviousGrantID: request.PreviousGrantID.String(),
		RuntimeDeadline: request.RuntimeDeadline.UTC(), LeaseExpiresAt: request.LeaseExpiresAt.UTC(),
		AuthorizationExpiresAt: request.AuthorizationExpiresAt.UTC(),
		ReservationExpiresAt:   request.ReservationExpiresAt.UTC(), RoutePolicyExpiresAt: request.RoutePolicyExpiresAt.UTC(),
		RecoveryExpiresAt: request.RecoveryExpiresAt.UTC(),
		NotAfter:          request.NotAfter.UTC(),
	})
}

func decodePostgresGatewayGrantRequest(encoded []byte) (GatewayGrantRequest, error) {
	var state postgresGatewayGrantRequestState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return GatewayGrantRequest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return GatewayGrantRequest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	request := GatewayGrantRequest{
		Kind: state.Kind, OperationID: OperationID{value: state.OperationID},
		CanonicalRequestDigest: state.CanonicalRequestDigest,
		PersonalWorkspaceID:    PersonalWorkspaceID{value: state.PersonalWorkspaceID},
		TaskID:                 TaskID{value: state.TaskID}, PhaseRunID: PhaseRunID{value: state.PhaseRunID},
		RuntimeRunID:     RuntimeRunID{value: state.RuntimeRunID},
		StartOperationID: OperationID{value: state.StartOperationID}, LeaseID: SandboxLeaseID{value: state.LeaseID},
		RuntimeBindingID:     RuntimeBindingID{value: state.RuntimeBindingID},
		RuntimeBindingDigest: state.RuntimeBindingDigest, ReleaseSafetyEpoch: state.ReleaseSafetyEpoch,
		LeaseGeneration: state.LeaseGeneration, LeaseFence: state.LeaseFence, RuntimeFence: state.RuntimeFence,
		QuotaReservationID:         QuotaReservationID{value: state.QuotaReservationID},
		QuotaReservationGeneration: state.QuotaReservationGeneration, QuotaReservationMode: state.QuotaReservationMode,
		OwnerAuthorityGeneration:     state.OwnerAuthorityGeneration,
		AuthorizationGeneration:      state.AuthorizationGeneration,
		GatewayRoutePolicyID:         GatewayRoutePolicyID{value: state.GatewayRoutePolicyID},
		GatewayRoutePolicyGeneration: state.GatewayRoutePolicyGeneration,
		CapabilityScope:              state.CapabilityScope, RecoveryGeneration: state.RecoveryGeneration,
		RecoveryMode: state.RecoveryMode, RequestedGeneration: state.RequestedGeneration,
		PreviousGeneration: state.PreviousGeneration, PreviousGrantID: GatewayGrantID{value: state.PreviousGrantID},
		RuntimeDeadline: state.RuntimeDeadline.UTC(), LeaseExpiresAt: state.LeaseExpiresAt.UTC(),
		AuthorizationExpiresAt: state.AuthorizationExpiresAt.UTC(),
		ReservationExpiresAt:   state.ReservationExpiresAt.UTC(), RoutePolicyExpiresAt: state.RoutePolicyExpiresAt.UTC(),
		RecoveryExpiresAt: state.RecoveryExpiresAt.UTC(),
		NotAfter:          state.NotAfter.UTC(),
	}
	if request.NotAfter.IsZero() || !validGatewayGrantRequest(request, request.NotAfter.Add(-time.Nanosecond)) {
		return GatewayGrantRequest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return request, nil
}

func postgresGatewayGrantStateFromGrant(grant GatewayGrant) postgresGatewayGrantState {
	expiresAt := grant.ExpiresAt
	if !expiresAt.IsZero() {
		expiresAt = expiresAt.UTC()
	}
	return postgresGatewayGrantState{
		GatewayGrantID: grant.GatewayGrantID.String(), Generation: grant.Generation,
		PersonalWorkspaceID: grant.PersonalWorkspaceID.String(), TaskID: grant.TaskID.String(),
		PhaseRunID:   grant.PhaseRunID.String(),
		RuntimeRunID: grant.RuntimeRunID.String(), StartOperationID: grant.StartOperationID.String(),
		RuntimeBindingID: grant.RuntimeBindingID.String(), RuntimeBindingDigest: grant.RuntimeBindingDigest,
		ReleaseSafetyEpoch: grant.ReleaseSafetyEpoch,
		LeaseID:            grant.LeaseID.String(), LeaseGeneration: grant.LeaseGeneration, LeaseFence: grant.LeaseFence,
		RuntimeFence: grant.RuntimeFence, QuotaReservationID: grant.QuotaReservationID.String(),
		QuotaReservationGeneration: grant.QuotaReservationGeneration, QuotaReservationMode: grant.QuotaReservationMode,
		OwnerAuthorityGeneration:     grant.OwnerAuthorityGeneration,
		AuthorizationGeneration:      grant.AuthorizationGeneration,
		GatewayRoutePolicyID:         grant.GatewayRoutePolicyID.String(),
		GatewayRoutePolicyGeneration: grant.GatewayRoutePolicyGeneration,
		CapabilityScope:              grant.CapabilityScope, RecoveryGeneration: grant.RecoveryGeneration,
		RecoveryMode: grant.RecoveryMode, RecoveryExpiresAt: grant.RecoveryExpiresAt.UTC(),
		ExpiresAt: expiresAt, CanonicalDigest: grant.CanonicalDigest,
	}
}

func gatewayGrantFromPostgresState(state postgresGatewayGrantState) GatewayGrant {
	expiresAt := state.ExpiresAt
	if !expiresAt.IsZero() {
		expiresAt = expiresAt.UTC()
	}
	return GatewayGrant{
		GatewayGrantInput: GatewayGrantInput{
			GatewayGrantID: GatewayGrantID{value: state.GatewayGrantID}, Generation: state.Generation,
			PersonalWorkspaceID: PersonalWorkspaceID{value: state.PersonalWorkspaceID},
			TaskID:              TaskID{value: state.TaskID}, PhaseRunID: PhaseRunID{value: state.PhaseRunID},
			RuntimeRunID:         RuntimeRunID{value: state.RuntimeRunID},
			StartOperationID:     OperationID{value: state.StartOperationID},
			RuntimeBindingID:     RuntimeBindingID{value: state.RuntimeBindingID},
			RuntimeBindingDigest: state.RuntimeBindingDigest, ReleaseSafetyEpoch: state.ReleaseSafetyEpoch,
			LeaseID: SandboxLeaseID{value: state.LeaseID}, LeaseGeneration: state.LeaseGeneration,
			LeaseFence: state.LeaseFence, RuntimeFence: state.RuntimeFence,
			QuotaReservationID:         QuotaReservationID{value: state.QuotaReservationID},
			QuotaReservationGeneration: state.QuotaReservationGeneration, QuotaReservationMode: state.QuotaReservationMode,
			OwnerAuthorityGeneration:     state.OwnerAuthorityGeneration,
			AuthorizationGeneration:      state.AuthorizationGeneration,
			GatewayRoutePolicyID:         GatewayRoutePolicyID{value: state.GatewayRoutePolicyID},
			GatewayRoutePolicyGeneration: state.GatewayRoutePolicyGeneration,
			CapabilityScope:              state.CapabilityScope, RecoveryGeneration: state.RecoveryGeneration,
			RecoveryMode: state.RecoveryMode, RecoveryExpiresAt: state.RecoveryExpiresAt.UTC(),
			ExpiresAt: expiresAt,
		},
		CanonicalDigest: state.CanonicalDigest,
	}
}

func encodePostgresGatewayGrantDecision(decision GatewayGrantDecision) ([]byte, error) {
	return json.Marshal(postgresGatewayGrantDecisionState{
		OperationID: decision.OperationID.String(), CanonicalRequestDigest: decision.CanonicalRequestDigest,
		Disposition: decision.Disposition, Grant: postgresGatewayGrantStateFromGrant(decision.Grant),
	})
}

func decodePostgresGatewayGrantDecision(encoded []byte) (GatewayGrantDecision, error) {
	var state postgresGatewayGrantDecisionState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return GatewayGrantDecision{}, newPersistenceError(PersistenceStateCorrupt)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return GatewayGrantDecision{}, newPersistenceError(PersistenceStateCorrupt)
	}
	decision := GatewayGrantDecision{
		OperationID: OperationID{value: state.OperationID}, CanonicalRequestDigest: state.CanonicalRequestDigest,
		Disposition: state.Disposition, Grant: gatewayGrantFromPostgresState(state.Grant),
	}
	if !validOpaqueID(decision.OperationID.String()) || decision.CanonicalRequestDigest == (Digest{}) ||
		decision.Disposition != GatewayGrantDecisionAccepted || !validGatewayGrant(decision.Grant) ||
		decision.Grant.CanonicalDigest != canonicalGatewayGrantDigest(decision.Grant) {
		return GatewayGrantDecision{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return decision, nil
}

type postgresGatewayPreparation struct {
	request  GatewayGrantRequest
	snapshot RuntimeSnapshot
	dispatch bool
}

func (authority *PostgresAuthority) advancePostgresGatewayPrerequisite(
	ctx context.Context,
	start StartRuntimeRun,
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
	prepared, err := authority.preparePostgresGatewayRequest(ctx, start)
	if err != nil {
		return RuntimeDecision{}, err
	}
	decision.Snapshot = prepared.snapshot
	if !prepared.dispatch {
		return decision, nil
	}
	gatewayDecision, decideErr := authority.gatewayGrants.DecideGatewayGrant(ctx, prepared.request)
	if decideErr != nil {
		gatewayDecision, decideErr = authority.gatewayGrants.InspectGatewayGrant(ctx, GatewayGrantOperationRef{
			OperationID:            prepared.request.OperationID,
			CanonicalRequestDigest: prepared.request.CanonicalRequestDigest,
		})
	}
	if decideErr != nil {
		_ = authority.markPostgresGatewayReconciliation(ctx, start, prepared.request)
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	accepted, err := authority.acceptPostgresGatewayGrant(ctx, start, prepared.request, gatewayDecision)
	if err != nil {
		return RuntimeDecision{}, err
	}
	decision.Snapshot = accepted
	return decision, nil
}

func (authority *PostgresAuthority) preparePostgresGatewayRequest(
	ctx context.Context,
	start StartRuntimeRun,
) (postgresGatewayPreparation, error) {
	now := postgresTimestamp(authority.now())
	var recovery GatewayRecoverySnapshot
	if start.ProviderCapability == ProviderCapabilityRequired && authority.gatewayGrants != nil {
		var recoveryErr error
		recovery, recoveryErr = inspectGatewayRecovery(ctx, authority.gatewayRecovery)
		if recoveryErr != nil {
			return postgresGatewayPreparation{}, newError(ErrorReconciliationRequired)
		}
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, start.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !authorized(record, start.PersonalWorkspaceID, start.Authority) {
		return postgresGatewayPreparation{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
	}
	if record.fixture.TaskID != start.TaskID || record.fixture.PhaseRunID != start.PhaseRunID {
		return postgresGatewayPreparation{}, newError(ErrorIntegrityConflict)
	}
	if start.ProviderCapability == ProviderCapabilityNone {
		if record.gateway == (GatewayPrerequisiteSnapshot{}) && record.usage == (RuntimeUsageEvidenceSnapshot{}) {
			record.gateway = GatewayPrerequisiteSnapshot{
				Applicability: GatewayPrerequisiteNotApplicable, Status: GatewayGrantNotApplicable, Ready: true,
			}
			record.usage = RuntimeUsageEvidenceSnapshot{Disposition: UsageEvidenceNotApplicable}
			if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
				return postgresGatewayPreparation{}, err
			}
		}
		if record.gateway.Applicability != GatewayPrerequisiteNotApplicable || !record.gateway.Ready {
			return postgresGatewayPreparation{}, newError(ErrorIntegrityConflict)
		}
		if err := tx.Commit(); err != nil {
			return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresGatewayPreparation{snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	if start.ProviderCapability != ProviderCapabilityRequired || start.ProviderBinding == nil ||
		record.gateway.Applicability != GatewayPrerequisiteRequired {
		return postgresGatewayPreparation{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeTerminal || record.fixture.Outcome != RuntimeOutcomeNone {
		record.gateway.Status = GatewayGrantStale
		record.gateway.Ready = false
		if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
			return postgresGatewayPreparation{}, err
		}
		if err := tx.Commit(); err != nil {
			return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresGatewayPreparation{snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	if record.operation.OperationID != start.OperationID || record.operation.Digest != start.CanonicalRequestDigest {
		return postgresGatewayPreparation{}, newError(ErrorIntegrityConflict)
	}
	if record.lease.AcquireStatus != LeaseGranted || record.lease.Disposition != LeaseActive {
		if err := tx.Commit(); err != nil {
			return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresGatewayPreparation{snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	if !gatewayRequestPrerequisitesSatisfiedAt(snapshot(record, SnapshotSchemaCurrent), now) {
		record.gateway.Status = GatewayGrantPending
		record.gateway.Ready = false
		if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
			return postgresGatewayPreparation{}, err
		}
		if err := tx.Commit(); err != nil {
			return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresGatewayPreparation{snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	if authority.gatewayGrants == nil {
		record.gateway.Status = GatewayGrantPending
		record.gateway.Ready = false
		if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
			return postgresGatewayPreparation{}, err
		}
		if err := tx.Commit(); err != nil {
			return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresGatewayPreparation{snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	reservation, err := authority.validatePostgresQuotaReservation(ctx, tx, start, now)
	if err != nil {
		return postgresGatewayPreparation{}, err
	}
	if !gatewayRecoveryAllowsGrant(recovery, now) {
		if record.gateway.CurrentGrant == (GatewayGrant{}) {
			record.gateway.Status = GatewayGrantPending
		} else {
			record.gateway.Status = GatewayGrantStale
		}
		record.gateway.Ready = false
		if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
			return postgresGatewayPreparation{}, err
		}
		if err := tx.Commit(); err != nil {
			return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresGatewayPreparation{snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	if record.gateway.Status == GatewayGrantCurrent && record.gateway.Ready &&
		record.gateway.CurrentGrant.ExpiresAt.After(now.Add(20*time.Second)) &&
		gatewayGrantRecoveryCurrent(record.gateway.CurrentGrant, recovery, now) {
		if err := tx.Commit(); err != nil {
			return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresGatewayPreparation{snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	var request GatewayGrantRequest
	if (record.gateway.Status == GatewayGrantPending || record.gateway.Status == GatewayGrantReconciliationRequired ||
		record.gateway.Status == GatewayGrantExpired) &&
		validOpaqueID(record.gateway.OperationID.String()) && record.gateway.CanonicalRequestDigest != (Digest{}) {
		retained, loadErr := authority.loadPostgresGatewayRequest(ctx, tx, record.gateway.OperationID)
		if loadErr != nil {
			return postgresGatewayPreparation{}, loadErr
		}
		if !gatewayGrantRequestMatchesAuthority(retained, start, record.lease, record.fixture.RuntimeFence,
			record.gateway.CurrentGrant, reservation.ExpiresAt, recovery) {
			return postgresGatewayPreparation{}, newError(ErrorIntegrityConflict)
		}
		request = retained
	}
	if request == (GatewayGrantRequest{}) {
		request, err = stableGatewayGrantRequest(start, record.lease, record.fixture.RuntimeFence,
			reservation.ExpiresAt, recovery, now, authority.gatewayGrantLifetime, record.gateway.CurrentGrant)
		if err != nil {
			return postgresGatewayPreparation{}, err
		}
	}
	if !request.NotAfter.After(now) {
		record.gateway.Status = GatewayGrantExpired
		record.gateway.Ready = false
		if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
			return postgresGatewayPreparation{}, err
		}
		if err := tx.Commit(); err != nil {
			return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresGatewayPreparation{snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	encoded, err := encodePostgresGatewayGrantRequest(request)
	if err != nil {
		return postgresGatewayPreparation{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, canonical_request_digest, requested_generation,
		previous_generation, previous_grant_id, request_state, created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (operation_id) DO NOTHING`,
		authority.table("runtime_execution_gateway_outbox")), request.OperationID.String(),
		request.RuntimeRunID.String(), request.CanonicalRequestDigest[:], request.RequestedGeneration,
		request.PreviousGeneration, request.PreviousGrantID.String(), encoded, now); err != nil {
		return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
	}
	retained, err := authority.loadPostgresGatewayRequest(ctx, tx, request.OperationID)
	if err != nil || retained != request {
		return postgresGatewayPreparation{}, newError(ErrorIntegrityConflict)
	}
	record.gateway = GatewayPrerequisiteSnapshot{
		Applicability: GatewayPrerequisiteRequired, Status: GatewayGrantPending,
		OperationID: request.OperationID, CanonicalRequestDigest: request.CanonicalRequestDigest,
		RequestedGeneration: request.RequestedGeneration, CurrentGrant: record.gateway.CurrentGrant,
	}
	if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
		return postgresGatewayPreparation{}, err
	}
	if authority.failAt(PersistenceFaultBeforeGatewayRequestCommit) {
		return postgresGatewayPreparation{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return postgresGatewayPreparation{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterGatewayRequestCommit) {
		return postgresGatewayPreparation{}, newError(ErrorReconciliationRequired)
	}
	return postgresGatewayPreparation{request: request, snapshot: snapshot(record, SnapshotSchemaCurrent), dispatch: true}, nil
}

func (authority *PostgresAuthority) loadPostgresGatewayRequest(
	ctx context.Context,
	tx *sql.Tx,
	operationID OperationID,
) (GatewayGrantRequest, error) {
	var runtimeRunID string
	var digest, encoded []byte
	var requested, previous GatewayGrantGeneration
	var previousGrantID string
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT runtime_run_id, canonical_request_digest,
		requested_generation, previous_generation, previous_grant_id, request_state
		FROM %s WHERE operation_id=$1`, authority.table("runtime_execution_gateway_outbox")),
		operationID.String()).Scan(&runtimeRunID, &digest, &requested, &previous, &previousGrantID, &encoded)
	if err != nil {
		return GatewayGrantRequest{}, normalizeRuntimePersistenceFailure(err)
	}
	request, err := decodePostgresGatewayGrantRequest(encoded)
	if err != nil || request.OperationID != operationID || request.RuntimeRunID.String() != runtimeRunID ||
		!bytes.Equal(digest, request.CanonicalRequestDigest[:]) || request.RequestedGeneration != requested ||
		request.PreviousGeneration != previous || request.PreviousGrantID.String() != previousGrantID {
		return GatewayGrantRequest{}, newError(ErrorIntegrityConflict)
	}
	return request, nil
}

func (authority *PostgresAuthority) acceptPostgresGatewayGrant(
	ctx context.Context,
	start StartRuntimeRun,
	request GatewayGrantRequest,
	decision GatewayGrantDecision,
) (RuntimeSnapshot, error) {
	now := postgresTimestamp(authority.now())
	recovery, recoveryErr := inspectGatewayRecovery(ctx, authority.gatewayRecovery)
	if recoveryErr != nil {
		return RuntimeSnapshot{}, newError(ErrorReconciliationRequired)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, start.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !authorized(record, start.PersonalWorkspaceID, start.Authority) {
		return RuntimeSnapshot{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	retained, err := authority.loadPostgresGatewayRequest(ctx, tx, request.OperationID)
	if err != nil || retained != request {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if replay, found, err := authority.loadPostgresGatewayAcceptance(ctx, tx, request.OperationID); err != nil {
		return RuntimeSnapshot{}, err
	} else if found {
		if replay != decision {
			return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
		}
		if err := authority.verifyPostgresGatewayCurrent(ctx, tx, request.RuntimeRunID, request.OperationID,
			decision.Grant.GatewayGrantID, decision.Grant.Generation); err != nil {
			return RuntimeSnapshot{}, err
		}
		if gatewayGrantRecoveryCurrent(decision.Grant, recovery, now) {
			record.gateway = GatewayPrerequisiteSnapshot{
				Applicability: GatewayPrerequisiteRequired, Status: GatewayGrantCurrent, Ready: true,
				OperationID: request.OperationID, CanonicalRequestDigest: request.CanonicalRequestDigest,
				RequestedGeneration: request.RequestedGeneration, CurrentGrant: decision.Grant,
			}
		} else {
			record.gateway = GatewayPrerequisiteSnapshot{
				Applicability: GatewayPrerequisiteRequired, Status: GatewayGrantStale, Ready: false,
				OperationID: request.OperationID, CanonicalRequestDigest: request.CanonicalRequestDigest,
				RequestedGeneration: request.RequestedGeneration, CurrentGrant: decision.Grant,
			}
		}
		if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
			return RuntimeSnapshot{}, err
		}
		if err := tx.Commit(); err != nil {
			return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
		}
		return snapshot(record, SnapshotSchemaCurrent), nil
	}
	reservation, err := authority.validatePostgresQuotaReservation(ctx, tx, start, now)
	if err != nil ||
		!gatewayGrantRequestMatchesAuthority(request, start, record.lease, record.fixture.RuntimeFence,
			record.gateway.CurrentGrant, reservation.ExpiresAt, recovery) ||
		record.fixture.State == RuntimeTerminal || record.fixture.Outcome != RuntimeOutcomeNone ||
		record.lease.Disposition != LeaseActive || !record.lease.ExpiresAt.After(now) ||
		decision.OperationID != request.OperationID ||
		decision.CanonicalRequestDigest != request.CanonicalRequestDigest ||
		decision.Disposition != GatewayGrantDecisionAccepted ||
		!gatewayGrantMatchesRequest(decision.Grant, request, now) ||
		decision.Grant.ExpiresAt.After(reservation.ExpiresAt) {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if err := authority.verifyPostgresGatewayCASPredecessor(ctx, tx, request); err != nil {
		return RuntimeSnapshot{}, err
	}
	encoded, err := encodePostgresGatewayGrantDecision(decision)
	if err != nil {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, canonical_request_digest, gateway_grant_id,
		grant_generation, decision_state, accepted_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_gateway_grant_acceptances")),
		request.OperationID.String(), request.RuntimeRunID.String(), request.CanonicalRequestDigest[:],
		decision.Grant.GatewayGrantID.String(), decision.Grant.Generation, encoded, now); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if request.PreviousGeneration == 0 {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			runtime_run_id, operation_id, gateway_grant_id, grant_generation, activated_at
		) VALUES ($1,$2,$3,$4,$5)`, authority.table("runtime_execution_gateway_current")),
			request.RuntimeRunID.String(), request.OperationID.String(), decision.Grant.GatewayGrantID.String(),
			decision.Grant.Generation, now); err != nil {
			return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
		}
	} else {
		result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET operation_id=$1,
			gateway_grant_id=$2, grant_generation=$3, activated_at=$4
			WHERE runtime_run_id=$5 AND operation_id IN (
				SELECT operation_id FROM %s WHERE gateway_grant_id=$6 AND grant_generation=$7
			) AND grant_generation=$7`, authority.table("runtime_execution_gateway_current"),
			authority.table("runtime_execution_gateway_grant_acceptances")), request.OperationID.String(),
			decision.Grant.GatewayGrantID.String(), decision.Grant.Generation, now,
			request.RuntimeRunID.String(), request.PreviousGrantID.String(), request.PreviousGeneration)
		if err != nil {
			return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
		}
	}
	record.gateway = GatewayPrerequisiteSnapshot{
		Applicability: GatewayPrerequisiteRequired, Status: GatewayGrantCurrent, Ready: true,
		OperationID: request.OperationID, CanonicalRequestDigest: request.CanonicalRequestDigest,
		RequestedGeneration: request.RequestedGeneration, CurrentGrant: decision.Grant,
	}
	if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
		return RuntimeSnapshot{}, err
	}
	if authority.failAt(PersistenceFaultBeforeGatewayAcceptanceCommit) {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterGatewayAcceptanceCommit) {
		return RuntimeSnapshot{}, newError(ErrorReconciliationRequired)
	}
	return snapshot(record, SnapshotSchemaCurrent), nil
}

func (authority *PostgresAuthority) verifyPostgresGatewayCASPredecessor(
	ctx context.Context,
	tx *sql.Tx,
	request GatewayGrantRequest,
) error {
	var operationID, grantID string
	var generation GatewayGrantGeneration
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, gateway_grant_id, grant_generation
		FROM %s WHERE runtime_run_id=$1 FOR UPDATE`, authority.table("runtime_execution_gateway_current")),
		request.RuntimeRunID.String()).Scan(&operationID, &grantID, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		if request.PreviousGeneration == 0 && request.PreviousGrantID == (GatewayGrantID{}) {
			return nil
		}
		return newError(ErrorIntegrityConflict)
	}
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if generation != request.PreviousGeneration || grantID != request.PreviousGrantID.String() || operationID == "" {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) loadPostgresGatewayAcceptance(
	ctx context.Context,
	tx *sql.Tx,
	operationID OperationID,
) (GatewayGrantDecision, bool, error) {
	var runtimeRunID, grantID string
	var digest, encoded []byte
	var generation GatewayGrantGeneration
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT runtime_run_id, canonical_request_digest,
		gateway_grant_id, grant_generation, decision_state FROM %s WHERE operation_id=$1`,
		authority.table("runtime_execution_gateway_grant_acceptances")), operationID.String()).Scan(
		&runtimeRunID, &digest, &grantID, &generation, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayGrantDecision{}, false, nil
	}
	if err != nil {
		return GatewayGrantDecision{}, false, normalizeRuntimePersistenceFailure(err)
	}
	decision, err := decodePostgresGatewayGrantDecision(encoded)
	if err != nil || decision.OperationID != operationID || decision.Grant.RuntimeRunID.String() != runtimeRunID ||
		!bytes.Equal(digest, decision.CanonicalRequestDigest[:]) ||
		decision.Grant.GatewayGrantID.String() != grantID || decision.Grant.Generation != generation {
		return GatewayGrantDecision{}, false, newError(ErrorIntegrityConflict)
	}
	return decision, true, nil
}

func (authority *PostgresAuthority) verifyPostgresGatewayCurrent(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
	operationID OperationID,
	grantID GatewayGrantID,
	generation GatewayGrantGeneration,
) error {
	var retainedOperation, retainedGrant string
	var retainedGeneration GatewayGrantGeneration
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, gateway_grant_id, grant_generation
		FROM %s WHERE runtime_run_id=$1 FOR UPDATE`, authority.table("runtime_execution_gateway_current")),
		runtimeRunID.String()).Scan(&retainedOperation, &retainedGrant, &retainedGeneration)
	if err != nil || retainedOperation != operationID.String() || retainedGrant != grantID.String() ||
		retainedGeneration != generation {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) markPostgresGatewayReconciliation(
	ctx context.Context,
	start StartRuntimeRun,
	request GatewayGrantRequest,
) error {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, start.RuntimeRunID)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if record.gateway.OperationID != request.OperationID ||
		record.gateway.CanonicalRequestDigest != request.CanonicalRequestDigest {
		return newError(ErrorIntegrityConflict)
	}
	record.gateway.Status = GatewayGrantReconciliationRequired
	record.gateway.Ready = false
	if err := authority.updatePostgresGatewayAggregate(ctx, tx, record); err != nil {
		return err
	}
	return normalizeRuntimePersistenceFailure(tx.Commit())
}

func (authority *PostgresAuthority) updatePostgresGatewayAggregate(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
) error {
	synchronizeGatewayReadiness(record, postgresTimestamp(authority.now()))
	if !knownGatewayPrerequisite(record.gateway) || !knownRuntimeUsageEvidence(record.usage) {
		return newError(ErrorIntegrityConflict)
	}
	encoded, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET aggregate_state=$1, updated_at=$2
		WHERE runtime_run_id=$3 AND runtime_revision=$4 AND runtime_fence=$5`,
		authority.table("runtime_execution_runtimes")), encoded, postgresTimestamp(authority.now()),
		record.fixture.RuntimeRunID.String(), record.fixture.RuntimeRevision, record.fixture.RuntimeFence)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) ValidateGatewayCall(
	ctx context.Context,
	fact GatewayCallAuthorityFact,
	accept GatewayCallAcceptance,
) error {
	if ctx == nil || ctx.Err() != nil || accept == nil || fact.Capability == 0 ||
		fact.Capability&^knownProviderCapabilityScope != 0 {
		return newError(ErrorInvalidRequest)
	}
	now := postgresTimestamp(authority.now())
	if !fact.ValidAt.UTC().Equal(now) {
		return newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, fact.RuntimeRunID)
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	grant := record.gateway.CurrentGrant
	if record.fixture.State == RuntimeTerminal || record.fixture.Outcome != RuntimeOutcomeNone ||
		record.fixture.PersonalWorkspaceID != fact.PersonalWorkspaceID || record.fixture.TaskID != fact.TaskID ||
		record.fixture.PhaseRunID != fact.PhaseRunID || record.fixture.SafetyEpoch != fact.ReleaseSafetyEpoch ||
		record.fixture.Owner.generation != fact.OwnerAuthorityGeneration ||
		!record.deadline.After(now) || record.fixture.RuntimeFence != fact.RuntimeFence ||
		record.gateway.Status != GatewayGrantCurrent || !record.gateway.Ready ||
		!gatewayGrantAuthorizesCall(grant, fact, now) ||
		record.lease.AcquireStatus != LeaseGranted ||
		record.lease.Disposition != LeaseActive || record.lease.LeaseID != fact.LeaseID ||
		record.lease.Generation != fact.LeaseGeneration || record.lease.Fence != fact.LeaseFence ||
		record.lease.AuthorizationGeneration != fact.AuthorizationGeneration || !record.lease.ExpiresAt.After(now) {
		return newError(ErrorIntegrityConflict)
	}
	if err := authority.verifyPostgresGatewayCurrent(ctx, tx, fact.RuntimeRunID,
		record.gateway.OperationID, fact.GatewayGrantID, fact.GatewayGrantGeneration); err != nil {
		return err
	}
	reservationFact := QuotaReservationValidationFact{
		QuotaReservationID: fact.QuotaReservationID, Generation: fact.QuotaReservationGeneration,
		Mode: fact.QuotaReservationMode, PersonalWorkspaceID: record.fixture.PersonalWorkspaceID,
		TaskID: record.fixture.TaskID, PhaseRunID: record.fixture.PhaseRunID,
		AuthorizationGeneration: fact.OwnerAuthorityGeneration, Capability: ProviderCapabilityRequired,
		GatewayRoutePolicyID:         fact.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: fact.GatewayRoutePolicyGeneration,
		CapabilityScope:              grant.CapabilityScope, ValidAt: now,
	}
	if _, err := authority.validatePostgresQuotaReservationFact(ctx, tx, reservationFact); err != nil {
		return err
	}
	// The Runtime row remains locked until the Gateway-owned acceptance
	// callback returns, so a competing cancel/fence transaction linearizes
	// strictly before validation or after Call acceptance.
	return validateGatewayCallExternalAuthority(ctx, authority.gatewayCallAuthority,
		gatewayCallExternalAuthorityFact(grant, now), accept)
}
