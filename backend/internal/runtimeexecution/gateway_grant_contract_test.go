package runtimeexecution

import (
	"context"
	"strings"
	"testing"
	"time"
)

type recordingGatewayGrantAdapter struct {
	requests []GatewayGrantRequest
	decide   func(GatewayGrantRequest) (GatewayGrantDecision, error)
	inspect  func(GatewayGrantOperationRef) (GatewayGrantDecision, error)
}

func acceptedGatewayPrerequisiteValidators(t *testing.T, suffix string) (RuntimeBindingValidator, ImmutableInputValidator) {
	t.Helper()
	return RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, suffix+"-runtime-binding-evidence", digest(251)), nil
		}), ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, suffix+"-immutable-input-evidence", digest(252)), nil
		})
}

func writableGatewayRecoveryAuthority(now time.Time) GatewayRecoveryAuthority {
	return GatewayRecoveryAuthorityFunc(func(context.Context) (GatewayRecoverySnapshot, error) {
		return GatewayRecoverySnapshot{
			Generation: 7, Mode: GatewayRecoveryWritable, ExpiresAt: now.Add(9 * time.Minute),
		}, nil
	})
}

func gatewayCallExternalAuthorityForStart(
	recovery GatewayRecoveryAuthority,
	start StartRuntimeRun,
) GatewayCallExternalAuthority {
	return GatewayCallExternalAuthorityFunc(func(
		ctx context.Context,
		fact GatewayCallExternalAuthorityFact,
		accept GatewayCallAcceptance,
	) error {
		current, err := inspectGatewayRecovery(ctx, recovery)
		binding := start.ProviderBinding
		if err != nil || binding == nil || current.Generation != fact.RecoveryGeneration ||
			current.Mode != fact.RecoveryMode || !fact.GrantExpiresAt.After(fact.ValidAt) ||
			fact.GrantExpiresAt.After(current.ExpiresAt) ||
			fact.GatewayRoutePolicyID != binding.GatewayRoutePolicyID ||
			fact.GatewayRoutePolicyGeneration != binding.GatewayRoutePolicyGeneration ||
			fact.CapabilityScope != binding.CapabilityScope ||
			fact.GrantExpiresAt.After(binding.RoutePolicyExpiresAt) || !binding.RoutePolicyExpiresAt.After(fact.ValidAt) {
			return newError(ErrorIntegrityConflict)
		}
		return accept()
	})
}

func (adapter *recordingGatewayGrantAdapter) DecideGatewayGrant(
	_ context.Context,
	request GatewayGrantRequest,
) (GatewayGrantDecision, error) {
	adapter.requests = append(adapter.requests, request)
	return adapter.decide(request)
}

func (adapter *recordingGatewayGrantAdapter) InspectGatewayGrant(
	_ context.Context,
	request GatewayGrantOperationRef,
) (GatewayGrantDecision, error) {
	if adapter.inspect == nil {
		return GatewayGrantDecision{}, newError(ErrorDependencyUnavailable)
	}
	return adapter.inspect(request)
}

func TestGatewayGrantResponseLossInspectsOriginalOperation(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-loss-authority", 7)
	start, reservation, grant, node := providerGatewayFixture(t, now, authority, "gateway-loss")
	var harness *DeterministicHarness
	gateway, err := NewDeterministicGateway(func() time.Time {
		if harness == nil {
			return now
		}
		return harness.clock.current()
	})
	if err != nil {
		t.Fatalf("new Gateway: %v", err)
	}
	gateway.LoseNextGrantResponse()
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-loss")
	harness, err = NewDeterministicHarness(HarnessConfig{
		Now:               now,
		IDs:               DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:          []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:   []AdmissionGrantFixture{grant},
		QuotaReservations: []QuotaReservationFixture{reservation},
		Nodes:             []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		ImmutableInputValidator: inputValidator,
		GatewayGrants:           gateway,
		GatewayRecovery:         writableGatewayRecoveryAuthority(now),
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("response loss was not reconciled through Inspect: %v", err)
	}
	if !decision.Snapshot.Gateway.Ready || decision.Snapshot.Gateway.CurrentGrant.Generation != 1 {
		t.Fatalf("original Gateway decision was not durably accepted: %+v", decision.Snapshot.Gateway)
	}
	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed.Fact != decision.Fact ||
		replayed.Snapshot.Gateway.CurrentGrant != decision.Snapshot.Gateway.CurrentGrant ||
		replayed.Snapshot.Gateway.OperationID != decision.Snapshot.Gateway.OperationID {
		t.Fatalf("exact replay changed original grant: replay=%+v err=%v", replayed, err)
	}
}

func TestGatewayGrantReconciliationRetainsOriginalRequestAcrossTimeAndRestart(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 35, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-reconcile-restart-authority", 7)
	start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-reconcile-restart")
	available := false
	var original GatewayGrantRequest
	adapter := &recordingGatewayGrantAdapter{
		decide: func(request GatewayGrantRequest) (GatewayGrantDecision, error) {
			if original == (GatewayGrantRequest{}) {
				original = request
			}
			return GatewayGrantDecision{}, newError(ErrorDependencyUnavailable)
		},
		inspect: func(ref GatewayGrantOperationRef) (GatewayGrantDecision, error) {
			if !available {
				return GatewayGrantDecision{}, newError(ErrorDependencyUnavailable)
			}
			if ref.OperationID != original.OperationID || ref.CanonicalRequestDigest != original.CanonicalRequestDigest {
				return GatewayGrantDecision{}, newError(ErrorIntegrityConflict)
			}
			grant, err := NewGatewayGrant(gatewayGrantInputForRequest(
				mustGatewayGrantID(t, "gateway-reconcile-restart-grant"), original,
			))
			if err != nil {
				t.Fatal(err)
			}
			return GatewayGrantDecision{
				OperationID: original.OperationID, CanonicalRequestDigest: original.CanonicalRequestDigest,
				Disposition: GatewayGrantDecisionAccepted, Grant: grant,
			}, nil
		},
	}
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-reconcile-restart")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
		QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		ImmutableInputValidator: inputValidator,
		GatewayGrants:           adapter,
		GatewayRecovery:         writableGatewayRecoveryAuthority(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Runtime.Execute(context.Background(), start); errorCode(err) != ErrorReconciliationRequired {
		t.Fatalf("initial reconciliation error = %v", err)
	}
	available = true
	if err := harness.AdvanceClock(time.Second); err != nil {
		t.Fatal(err)
	}
	restarted := harness.Restart()
	decision, err := restarted.Runtime.Execute(context.Background(), start)
	if err != nil || decision.Snapshot.Gateway.Status != GatewayGrantCurrent ||
		decision.Snapshot.Gateway.OperationID != original.OperationID ||
		decision.Snapshot.Readiness.LLMGateway.State != PrerequisiteAccepted {
		t.Fatalf("reconciliation did not retain original request: decision=%+v err=%v original=%+v",
			decision, err, original)
	}
}

func TestGatewayGrantRefreshUsesMonotonicGenerationAndActivationCAS(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 45, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-refresh-authority", 7)
	start, reservation, grant, node := providerGatewayFixture(t, now, authority, "gateway-refresh")
	var harness *DeterministicHarness
	gateway, err := NewDeterministicGateway(func() time.Time {
		if harness == nil {
			return now
		}
		return harness.clock.current()
	})
	if err != nil {
		t.Fatalf("new Gateway: %v", err)
	}
	recorder := &recordingGatewayGrantAdapter{
		decide: func(request GatewayGrantRequest) (GatewayGrantDecision, error) {
			return gateway.DecideGatewayGrant(context.Background(), request)
		},
		inspect: func(ref GatewayGrantOperationRef) (GatewayGrantDecision, error) {
			return gateway.InspectGatewayGrant(context.Background(), ref)
		},
	}
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-refresh")
	harness, err = NewDeterministicHarness(HarnessConfig{
		Now:               now,
		IDs:               DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:          []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:   []AdmissionGrantFixture{grant},
		QuotaReservations: []QuotaReservationFixture{reservation},
		Nodes:             []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		ImmutableInputValidator: inputValidator,
		GatewayGrants:           recorder,
		GatewayRecovery:         writableGatewayRecoveryAuthority(now),
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	initial, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("initial grant: %v", err)
	}
	conflicting := recorder.requests[0]
	conflicting.NotAfter = conflicting.NotAfter.Add(-time.Second)
	conflicting, err = NewCanonicalGatewayGrantRequest(conflicting, now)
	if err != nil {
		t.Fatal(err)
	}
	if conflicting.OperationID != recorder.requests[0].OperationID ||
		conflicting.CanonicalRequestDigest == recorder.requests[0].CanonicalRequestDigest {
		t.Fatalf("one requested generation did not retain a stable operation key: original=%+v conflict=%+v",
			recorder.requests[0], conflicting)
	}
	if _, err := gateway.DecideGatewayGrant(context.Background(), conflicting); errorCode(err) != ErrorIntegrityConflict {
		t.Fatalf("same-key/different-payload error = %v", err)
	}
	original, err := gateway.DecideGatewayGrant(context.Background(), recorder.requests[0])
	if err != nil || original.Grant != initial.Snapshot.Gateway.CurrentGrant {
		t.Fatalf("conflict changed original grant: decision=%+v err=%v", original, err)
	}
	if err := harness.AdvanceClock(45 * time.Second); err != nil {
		t.Fatal(err)
	}
	rotated, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("refresh grant: %v", err)
	}
	if len(recorder.requests) != 2 || recorder.requests[1].Kind != GatewayGrantRefresh ||
		recorder.requests[1].PreviousGeneration != 1 || recorder.requests[1].RequestedGeneration != 2 ||
		recorder.requests[1].PreviousGrantID != initial.Snapshot.Gateway.CurrentGrant.GatewayGrantID ||
		rotated.Snapshot.Gateway.CurrentGrant.Generation != 2 ||
		rotated.Snapshot.Gateway.OperationID == initial.Snapshot.Gateway.OperationID {
		t.Fatalf("refresh lost stable generation/CAS binding: requests=%+v rotated=%+v", recorder.requests, rotated.Snapshot.Gateway)
	}
	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || len(recorder.requests) != 2 ||
		replayed.Snapshot.Gateway.CurrentGrant != rotated.Snapshot.Gateway.CurrentGrant {
		t.Fatalf("exact refresh replay minted another generation: requests=%d replay=%+v err=%v", len(recorder.requests), replayed, err)
	}
}

func TestGatewayRefreshRejectsScopeExpansion(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-scope-authority", 7)
	start, reservation, grant, node := providerGatewayFixture(t, now, authority, "gateway-scope")
	input := start.StartRuntimeRunInput
	provider := *input.ProviderBinding
	provider.CapabilityScope = ProviderScopeTextGeneration
	input.ProviderBinding = &provider
	start = mustStart(t, input)
	reservation.CapabilityScope = ProviderScopeTextGeneration
	grant = grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = node.ExecutionNodeID
	grant.NodeCapacityGeneration = uint64(node.Generation)
	node = executionNodeFixtureForStart(t, start, grant, now)
	var harness *DeterministicHarness
	gateway, err := NewDeterministicGateway(func() time.Time {
		if harness == nil {
			return now
		}
		return harness.clock.current()
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingGatewayGrantAdapter{
		decide: func(request GatewayGrantRequest) (GatewayGrantDecision, error) {
			return gateway.DecideGatewayGrant(context.Background(), request)
		},
		inspect: func(ref GatewayGrantOperationRef) (GatewayGrantDecision, error) {
			return gateway.InspectGatewayGrant(context.Background(), ref)
		},
	}
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-scope")
	harness, err = NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{grant},
		QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		ImmutableInputValidator: inputValidator,
		GatewayGrants:           recorder,
		GatewayRecovery:         writableGatewayRecoveryAuthority(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	request := recorder.requests[0]
	request.Kind = GatewayGrantRefresh
	request.PreviousGeneration = initial.Snapshot.Gateway.CurrentGrant.Generation
	request.PreviousGrantID = initial.Snapshot.Gateway.CurrentGrant.GatewayGrantID
	request.RequestedGeneration = request.PreviousGeneration + 1
	request.CapabilityScope = ProviderScopeTextGeneration | ProviderScopeImageGeneration
	request, err = NewCanonicalGatewayGrantRequest(request, now)
	if err != nil {
		t.Fatalf("canonical expanded request: %v", err)
	}
	if _, err := gateway.DecideGatewayGrant(context.Background(), request); errorCode(err) != ErrorIntegrityConflict {
		t.Fatalf("scope expansion error = %v", err)
	}
	retained, err := gateway.InspectGatewayGrant(context.Background(), GatewayGrantOperationRef{
		OperationID:            initial.Snapshot.Gateway.OperationID,
		CanonicalRequestDigest: initial.Snapshot.Gateway.CanonicalRequestDigest,
	})
	if err != nil || retained.Grant != initial.Snapshot.Gateway.CurrentGrant {
		t.Fatalf("scope expansion changed current grant: retained=%+v err=%v", retained, err)
	}
}

func errorCode(err error) ErrorCode {
	if typed, ok := err.(*Error); ok {
		return typed.Code()
	}
	return 0
}

func TestGatewayGrantIsRequestedOnlyAfterLeaseAndBindsExactAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-grant-authority", 7)
	start, reservation, grant, node := providerGatewayFixture(t, now, authority, "gateway-grant")
	node.AuthorizationGeneration = authority.generation + 4
	adapter := &recordingGatewayGrantAdapter{}
	adapter.decide = func(request GatewayGrantRequest) (GatewayGrantDecision, error) {
		issued, err := NewGatewayGrant(gatewayGrantInputForRequest(
			mustGatewayGrantID(t, "gateway-grant-1"), request,
		))
		if err != nil {
			t.Fatalf("issue grant: %v", err)
		}
		return GatewayGrantDecision{
			OperationID: request.OperationID, CanonicalRequestDigest: request.CanonicalRequestDigest,
			Disposition: GatewayGrantDecisionAccepted, Grant: issued,
		}, nil
	}
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-grant")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now:               now,
		IDs:               DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:          []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:   []AdmissionGrantFixture{grant},
		QuotaReservations: []QuotaReservationFixture{reservation},
		Nodes:             []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		ImmutableInputValidator: inputValidator,
		GatewayGrants:           adapter,
		GatewayRecovery:         writableGatewayRecoveryAuthority(now),
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute provider start: %v", err)
	}
	if len(adapter.requests) != 1 {
		t.Fatalf("Gateway received %d requests, want one post-lease request", len(adapter.requests))
	}
	request := adapter.requests[0]
	if decision.Snapshot.Lease.Disposition != LeaseActive || request.LeaseID != decision.Snapshot.Lease.LeaseID ||
		request.LeaseGeneration != decision.Snapshot.Lease.Generation || request.LeaseFence != decision.Snapshot.Lease.Fence ||
		request.RuntimeFence != decision.Snapshot.RuntimeFence ||
		request.PersonalWorkspaceID != start.PersonalWorkspaceID || request.TaskID != start.TaskID ||
		request.PhaseRunID != start.PhaseRunID || request.RuntimeRunID != start.RuntimeRunID ||
		request.StartOperationID != start.OperationID || request.QuotaReservationID != reservation.QuotaReservationID ||
		request.RuntimeBindingID != start.RuntimeBindingID || request.RuntimeBindingDigest != start.RuntimeBindingDigest ||
		request.ReleaseSafetyEpoch != start.ReleaseSafetyEpoch ||
		request.QuotaReservationGeneration != reservation.Generation || request.QuotaReservationMode != reservation.Mode ||
		request.OwnerAuthorityGeneration != start.Authority.generation ||
		request.AuthorizationGeneration != decision.Snapshot.Lease.AuthorizationGeneration ||
		request.OwnerAuthorityGeneration == request.AuthorizationGeneration ||
		request.GatewayRoutePolicyID != start.ProviderBinding.GatewayRoutePolicyID ||
		request.GatewayRoutePolicyGeneration != start.ProviderBinding.GatewayRoutePolicyGeneration ||
		request.CapabilityScope != start.ProviderBinding.CapabilityScope || request.RecoveryGeneration != 7 ||
		request.RecoveryMode != GatewayRecoveryWritable || !request.RecoveryExpiresAt.Equal(now.Add(9*time.Minute)) {
		t.Fatalf("Gateway request lost exact authority: request=%+v snapshot=%+v", request, decision.Snapshot)
	}
	if !request.NotAfter.Equal(now.Add(time.Minute)) {
		t.Fatalf("grant upper bound = %v, want configured short lifetime %v", request.NotAfter, now.Add(time.Minute))
	}
	if request.CanonicalRequestDigest == (Digest{}) || request.OperationID.String() == "" ||
		decision.Snapshot.Gateway.Applicability != GatewayPrerequisiteRequired ||
		decision.Snapshot.Gateway.Status != GatewayGrantCurrent || !decision.Snapshot.Gateway.Ready ||
		decision.Snapshot.Gateway.CurrentGrant.Generation != 1 ||
		decision.Snapshot.Gateway.CurrentGrant.OwnerAuthorityGeneration != start.Authority.generation ||
		decision.Snapshot.Gateway.CurrentGrant.AuthorizationGeneration != node.AuthorizationGeneration ||
		decision.Snapshot.Gateway.CurrentGrant.CanonicalDigest == (Digest{}) {
		t.Fatalf("current durable Gateway prerequisite missing: %+v", decision.Snapshot.Gateway)
	}
	gatewayFact := decision.Snapshot.Readiness.LLMGateway
	if gatewayFact.State != PrerequisiteAccepted || gatewayFact.OperationID != request.OperationID ||
		gatewayFact.RequestDigest != request.CanonicalRequestDigest ||
		gatewayFact.EvidenceID.String() != "gateway-grant-evidence-"+decision.Snapshot.Gateway.CurrentGrant.GatewayGrantID.String() ||
		gatewayFact.EvidenceDigest != decision.Snapshot.Gateway.CurrentGrant.CanonicalDigest ||
		!decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("durable Gateway grant did not satisfy the sole readiness accumulator: readiness=%+v gateway=%+v",
			decision.Snapshot.Readiness, decision.Snapshot.Gateway)
	}
}

func TestInspectProjectsCurrentGatewayAuthorityWithoutPersistingDrift(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		drift func(*DeterministicHarness, *QuotaReservationFixture, *GatewayRecoverySnapshot) error
		reset func(*DeterministicHarness, QuotaReservationFixture, *GatewayRecoverySnapshot) error
	}{
		{
			name: "Reservation",
			drift: func(harness *DeterministicHarness, reservation *QuotaReservationFixture, _ *GatewayRecoverySnapshot) error {
				reservation.Generation++
				return harness.ReplaceQuotaReservation(*reservation)
			},
			reset: func(harness *DeterministicHarness, reservation QuotaReservationFixture, _ *GatewayRecoverySnapshot) error {
				return harness.ReplaceQuotaReservation(reservation)
			},
		},
		{
			name: "recovery",
			drift: func(_ *DeterministicHarness, _ *QuotaReservationFixture, recovery *GatewayRecoverySnapshot) error {
				recovery.Generation++
				recovery.Mode = GatewayRecoveryDegradedReadOnly
				return nil
			},
			reset: func(_ *DeterministicHarness, _ QuotaReservationFixture, recovery *GatewayRecoverySnapshot) error {
				recovery.Generation--
				recovery.Mode = GatewayRecoveryWritable
				return nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 21, 2, 0, 0, time.UTC)
			authority := mustTaskOrchestrationAuthority(t, "gateway-inspect-authority-"+testCase.name, 7)
			start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-inspect-"+testCase.name)
			recovery := GatewayRecoverySnapshot{
				Generation: 7, Mode: GatewayRecoveryWritable, ExpiresAt: now.Add(9 * time.Minute),
			}
			recoveryAuthority := GatewayRecoveryAuthorityFunc(func(context.Context) (GatewayRecoverySnapshot, error) {
				return recovery, nil
			})
			gateway, err := NewDeterministicGateway(func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-inspect-"+testCase.name)
			harness, err := NewDeterministicHarness(HarnessConfig{
				Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
				Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
				QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
				LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
					return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
				}),
				RuntimeBindingValidator: bindingValidator, ImmutableInputValidator: inputValidator,
				GatewayGrants: gateway, GatewayRecovery: recoveryAuthority,
				GatewayCallAuthority: gatewayCallExternalAuthorityForStart(recoveryAuthority, start),
			})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
				t.Fatalf("prepare current Gateway: snapshot=%+v err=%v", accepted.Snapshot, err)
			}
			originalReservation := reservation
			if err := testCase.drift(harness, &reservation, &recovery); err != nil {
				t.Fatal(err)
			}
			stale, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, authority))
			if err != nil || stale.Gateway.Status != GatewayGrantStale || stale.Gateway.Ready ||
				stale.Readiness.LLMGateway.State != PrerequisitePending || stale.Readiness.CapsuleReady {
				t.Fatalf("Inspect projected current authority drift as ready: snapshot=%+v err=%v", stale, err)
			}
			if err := testCase.reset(harness, originalReservation, &recovery); err != nil {
				t.Fatal(err)
			}
			restored, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, authority))
			if err != nil || restored.Gateway.Status != GatewayGrantCurrent || !restored.Gateway.Ready ||
				!restored.Readiness.CapsuleReady {
				t.Fatalf("Inspect persisted stale projection: snapshot=%+v err=%v", restored, err)
			}
		})
	}
}

func TestGatewayGrantExpiryUsesEveryAuthorityUpperBound(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 5, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name              string
		lifetime          time.Duration
		mutateStart       func(*StartRuntimeRunInput)
		mutateReservation func(*QuotaReservationFixture)
		mutateNode        func(*ExecutionNodeFixture)
		recoveryExpiresAt time.Time
	}{
		{name: "configured lifetime", lifetime: 30 * time.Second},
		{name: "Runtime deadline", mutateStart: func(input *StartRuntimeRunInput) {
			input.Deadline = now.Add(30 * time.Second)
		}},
		{name: "Sandbox Lease expiry", mutateNode: func(node *ExecutionNodeFixture) {
			node.ExpiresAt = now.Add(30 * time.Second)
		}},
		{name: "machine authorization expiry", mutateNode: func(node *ExecutionNodeFixture) {
			node.AuthorizationExpiresAt = now.Add(30 * time.Second)
		}},
		{name: "Reservation expiry", mutateReservation: func(reservation *QuotaReservationFixture) {
			reservation.ExpiresAt = now.Add(30 * time.Second)
		}},
		{name: "route policy expiry", mutateStart: func(input *StartRuntimeRunInput) {
			binding := *input.ProviderBinding
			binding.RoutePolicyExpiresAt = now.Add(30 * time.Second)
			input.ProviderBinding = &binding
		}},
		{name: "recovery allowance expiry", recoveryExpiresAt: now.Add(30 * time.Second)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			suffix := strings.ReplaceAll(testCase.name, " ", "-")
			authority := mustTaskOrchestrationAuthority(t, "gateway-expiry-bound-authority-"+suffix, 7)
			start, reservation, _, node := providerGatewayFixture(t, now, authority, "gateway-expiry-bound-"+suffix)
			if testCase.mutateStart != nil {
				input := start.StartRuntimeRunInput
				testCase.mutateStart(&input)
				start = mustStart(t, input)
			}
			if testCase.mutateReservation != nil {
				testCase.mutateReservation(&reservation)
			}
			admission := grantFixtureForStart(start, now.Add(10*time.Minute), true)
			admission.ExecutionNodeID = node.ExecutionNodeID
			admission.NodeCapacityGeneration = uint64(node.Generation)
			node = executionNodeFixtureForStart(t, start, admission, now)
			if testCase.mutateNode != nil {
				testCase.mutateNode(&node)
			}
			recoveryExpiresAt := testCase.recoveryExpiresAt
			if recoveryExpiresAt.IsZero() {
				recoveryExpiresAt = now.Add(9 * time.Minute)
			}
			recovery := GatewayRecoveryAuthorityFunc(func(context.Context) (GatewayRecoverySnapshot, error) {
				return GatewayRecoverySnapshot{
					Generation: 7, Mode: GatewayRecoveryWritable, ExpiresAt: recoveryExpiresAt,
				}, nil
			})
			var request GatewayGrantRequest
			adapter := &recordingGatewayGrantAdapter{decide: func(value GatewayGrantRequest) (GatewayGrantDecision, error) {
				request = value
				grant, err := NewGatewayGrant(gatewayGrantInputForRequest(
					mustGatewayGrantID(t, "gateway-expiry-bound-grant-"+suffix), value,
				))
				if err != nil {
					t.Fatal(err)
				}
				return GatewayGrantDecision{
					OperationID: value.OperationID, CanonicalRequestDigest: value.CanonicalRequestDigest,
					Disposition: GatewayGrantDecisionAccepted, Grant: grant,
				}, nil
			}}
			bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-expiry-bound-"+suffix)
			lifetime := testCase.lifetime
			if lifetime == 0 {
				lifetime = time.Minute
			}
			harness, err := NewDeterministicHarness(HarnessConfig{
				Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
				Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
				QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
				LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
					return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
				}),
				RuntimeBindingValidator: bindingValidator,
				ImmutableInputValidator: inputValidator,
				GatewayGrants:           adapter, GatewayRecovery: recovery, GatewayGrantLifetime: lifetime,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := harness.Runtime.Execute(context.Background(), start); err != nil {
				t.Fatal(err)
			}
			if !request.NotAfter.Equal(now.Add(30 * time.Second)) {
				t.Fatalf("NotAfter = %v, want %v; request=%+v", request.NotAfter, now.Add(30*time.Second), request)
			}
		})
	}
}

func TestGatewayGrantIsNotRequestedBeforeLease(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 15, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-before-lease-authority", 7)
	start, reservation, grant, _ := providerGatewayFixture(t, now, authority, "gateway-before-lease")
	adapter := &recordingGatewayGrantAdapter{decide: func(GatewayGrantRequest) (GatewayGrantDecision, error) {
		t.Fatal("Gateway grant requested before lease")
		return GatewayGrantDecision{}, nil
	}}
	bindingValidator, _ := acceptedGatewayPrerequisiteValidators(t, "gateway-before-lease")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now:               now,
		IDs:               DeterministicIDConfig{DecisionStart: 1},
		Runtimes:          []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:   []AdmissionGrantFixture{grant},
		QuotaReservations: []QuotaReservationFixture{reservation},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionTemporaryUnavailable}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		GatewayGrants:           adapter,
		GatewayRecovery:         writableGatewayRecoveryAuthority(now),
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute provider start: %v", err)
	}
	if len(adapter.requests) != 0 || decision.Snapshot.Gateway.Ready ||
		decision.Snapshot.Gateway.Applicability != GatewayPrerequisiteRequired ||
		decision.Snapshot.Gateway.Status != GatewayGrantWaitingForLease ||
		decision.Snapshot.Readiness.LLMGateway.State != PrerequisitePending ||
		decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("Gateway prerequisite crossed lease ordering: requests=%d gateway=%+v", len(adapter.requests), decision.Snapshot.Gateway)
	}
}

func TestGatewayGrantWaitsForAllPostLeasePrerequisites(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 15, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-after-prerequisites-authority", 7)
	start, reservation, _, _ := providerGatewayFixture(t, now, authority, "gateway-after-prerequisites")
	input := start.StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:         mustTaskWorkspaceID(t, "gateway-after-prerequisites-workspace"),
		MaterializationID:       mustTaskWorkspaceMaterializationID(t, "gateway-after-prerequisites-materialization"),
		BaseRevisionID:          mustTaskWorkspaceRevisionID(t, "gateway-after-prerequisites-revision"),
		LifecycleGeneration:     4,
		LifecycleFence:          5,
		ExpiryPolicy:            RuntimeViewExpiryAtDeadline,
		OpenOperationDerivation: digest(253),
	}
	start = mustStart(t, input)
	admission := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	admission.ExecutionNodeID = startNodeID(t, "gateway-after-prerequisites-node")
	admission.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, admission, now)
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-after-prerequisites")
	adapter := &recordingGatewayGrantAdapter{decide: func(GatewayGrantRequest) (GatewayGrantDecision, error) {
		t.Fatal("Gateway grant requested before C04 Runtime View acceptance")
		return GatewayGrantDecision{}, nil
	}}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
		QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		ImmutableInputValidator: inputValidator,
		GatewayGrants:           adapter,
		GatewayRecovery:         writableGatewayRecoveryAuthority(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) != 0 || decision.Snapshot.Readiness.RuntimeView.State != PrerequisitePending ||
		decision.Snapshot.Readiness.LLMGateway.State != PrerequisitePending ||
		decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("Gateway crossed pending post-lease prerequisites: requests=%d readiness=%+v gateway=%+v",
			len(adapter.requests), decision.Snapshot.Readiness, decision.Snapshot.Gateway)
	}
}

func TestGatewayReadinessFailsClosedForPendingReconciliationExpiryAndStaleness(t *testing.T) {
	now := time.Date(2026, time.July, 29, 23, 0, 0, 0, time.UTC)

	t.Run("pending", func(t *testing.T) {
		authority := mustTaskOrchestrationAuthority(t, "gateway-pending-authority", 7)
		start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-pending")
		bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-pending")
		harness, err := NewDeterministicHarness(HarnessConfig{
			Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
			Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
			QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
			LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
				return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
			}),
			RuntimeBindingValidator: bindingValidator,
			ImmutableInputValidator: inputValidator,
		})
		if err != nil {
			t.Fatal(err)
		}
		decision, err := harness.Runtime.Execute(context.Background(), start)
		if err != nil || decision.Snapshot.Gateway.Status != GatewayGrantPending ||
			decision.Snapshot.Readiness.LLMGateway.State != PrerequisitePending ||
			decision.Snapshot.Readiness.CapsuleReady {
			t.Fatalf("pending Gateway readiness = %+v/%+v err=%v",
				decision.Snapshot.Gateway, decision.Snapshot.Readiness, err)
		}
	})

	t.Run("reconciliation", func(t *testing.T) {
		authority := mustTaskOrchestrationAuthority(t, "gateway-reconciliation-authority", 7)
		start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-reconciliation")
		bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-reconciliation")
		adapter := &recordingGatewayGrantAdapter{
			decide: func(GatewayGrantRequest) (GatewayGrantDecision, error) {
				return GatewayGrantDecision{}, newError(ErrorDependencyUnavailable)
			},
			inspect: func(GatewayGrantOperationRef) (GatewayGrantDecision, error) {
				return GatewayGrantDecision{}, newError(ErrorDependencyUnavailable)
			},
		}
		harness, err := NewDeterministicHarness(HarnessConfig{
			Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
			Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
			QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
			LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
				return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
			}),
			RuntimeBindingValidator: bindingValidator,
			ImmutableInputValidator: inputValidator,
			GatewayGrants:           adapter,
			GatewayRecovery:         writableGatewayRecoveryAuthority(now),
			GatewayCallAuthority: gatewayCallExternalAuthorityForStart(
				writableGatewayRecoveryAuthority(now), start,
			),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := harness.Runtime.Execute(context.Background(), start); errorCode(err) != ErrorReconciliationRequired {
			t.Fatalf("Gateway ambiguity error = %v", err)
		}
		inspected, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
			SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
		})
		if err != nil || inspected.Gateway.Status != GatewayGrantReconciliationRequired ||
			inspected.Readiness.LLMGateway.State != PrerequisiteReconciliationRequired ||
			inspected.Readiness.LLMGateway.Failure != PrerequisiteFailureDependencyUnavailable ||
			inspected.Readiness.CapsuleReady {
			t.Fatalf("reconciliation Gateway readiness = %+v/%+v err=%v", inspected.Gateway, inspected.Readiness, err)
		}
	})

	t.Run("expired and stale", func(t *testing.T) {
		authority := mustTaskOrchestrationAuthority(t, "gateway-expiry-authority", 7)
		start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-expiry")
		bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-expiry")
		var harness *DeterministicHarness
		gateway, err := NewDeterministicGateway(func() time.Time {
			if harness == nil {
				return now
			}
			return harness.clock.current()
		})
		if err != nil {
			t.Fatal(err)
		}
		harness, err = NewDeterministicHarness(HarnessConfig{
			Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
			Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
			QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
			LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
				return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
			}),
			RuntimeBindingValidator: bindingValidator,
			ImmutableInputValidator: inputValidator,
			GatewayGrants:           gateway,
			GatewayRecovery:         writableGatewayRecoveryAuthority(now),
		})
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := harness.Runtime.Execute(context.Background(), start)
		if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
			t.Fatalf("accept Gateway grant: decision=%+v err=%v", accepted, err)
		}
		if err := harness.AdvanceClock(time.Minute); err != nil {
			t.Fatal(err)
		}
		expired, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
			SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
		})
		if err != nil || expired.Gateway.CurrentGrant != accepted.Snapshot.Gateway.CurrentGrant ||
			expired.Readiness.LLMGateway.State != PrerequisitePending || expired.Readiness.CapsuleReady {
			t.Fatalf("expired Gateway readiness = %+v/%+v err=%v", expired.Gateway, expired.Readiness, err)
		}

		staleReservation := reservation
		staleReservation.Generation++
		if err := harness.ReplaceQuotaReservation(staleReservation); err != nil {
			t.Fatal(err)
		}
		stale, err := harness.Runtime.Execute(context.Background(), start)
		if err != nil || stale.Snapshot.Gateway.Status != GatewayGrantStale || stale.Snapshot.Gateway.Ready ||
			stale.Snapshot.Readiness.LLMGateway.State != PrerequisitePending || stale.Snapshot.Readiness.CapsuleReady {
			t.Fatalf("stale Gateway readiness = %+v/%+v err=%v", stale.Snapshot.Gateway, stale.Snapshot.Readiness, err)
		}
	})
}

func providerGatewayFixture(
	t *testing.T,
	now time.Time,
	authority RuntimeAuthority,
	suffix string,
) (StartRuntimeRun, QuotaReservationFixture, AdmissionGrantFixture, ExecutionNodeFixture) {
	t.Helper()
	input := standardStart(t, now, authority, suffix).StartRuntimeRunInput
	input.ProviderCapability = ProviderCapabilityRequired
	input.ProviderBinding = &ProviderExecutionBinding{
		QuotaReservationID:           mustQuotaReservationID(t, "reservation-"+suffix),
		Generation:                   4,
		Mode:                         QuotaReservationObservation,
		GatewayRoutePolicyID:         mustGatewayRoutePolicyID(t, "route-"+suffix),
		GatewayRoutePolicyGeneration: 3,
		CapabilityScope:              ProviderScopeTextGeneration | ProviderScopeImageGeneration,
		RoutePolicyExpiresAt:         now.Add(8 * time.Minute),
	}
	start := mustStart(t, input)
	reservation := QuotaReservationFixture{
		QuotaReservationID:           start.ProviderBinding.QuotaReservationID,
		Generation:                   start.ProviderBinding.Generation,
		Mode:                         start.ProviderBinding.Mode,
		State:                        QuotaReservationActive,
		PersonalWorkspaceID:          start.PersonalWorkspaceID,
		TaskID:                       start.TaskID,
		PhaseRunID:                   start.PhaseRunID,
		AuthorizationGeneration:      start.Authority.generation,
		Capability:                   start.ProviderCapability,
		GatewayRoutePolicyID:         start.ProviderBinding.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: start.ProviderBinding.GatewayRoutePolicyGeneration,
		CapabilityScope:              start.ProviderBinding.CapabilityScope,
		ValidFrom:                    now.Add(-time.Minute),
		ExpiresAt:                    now.Add(10 * time.Minute),
	}
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, suffix+"-node")
	grant.NodeCapacityGeneration = 1
	return start, reservation, grant, executionNodeFixtureForStart(t, start, grant, now)
}

func mustGatewayGrantID(t *testing.T, value string) GatewayGrantID {
	t.Helper()
	id, err := NewGatewayGrantID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
