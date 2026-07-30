package runtimeexecution

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type gatewayCallAcceptanceBarrier struct {
	delegate GatewayCallAuthorityValidator
	entered  chan struct{}
	release  chan struct{}
}

type atomicGatewayCallExternalAuthority struct {
	mu                    sync.Mutex
	recovery              GatewayRecoverySnapshot
	routePolicyID         GatewayRoutePolicyID
	routePolicyGeneration GatewayRoutePolicyGeneration
	capabilityScope       ProviderCapabilityScope
	routeExpiresAt        time.Time
	blockNext             bool
	entered               chan struct{}
	release               chan struct{}
}

func newAtomicGatewayCallExternalAuthority(
	start StartRuntimeRun,
	recovery GatewayRecoverySnapshot,
) *atomicGatewayCallExternalAuthority {
	return &atomicGatewayCallExternalAuthority{
		recovery: recovery, routePolicyID: start.ProviderBinding.GatewayRoutePolicyID,
		routePolicyGeneration: start.ProviderBinding.GatewayRoutePolicyGeneration,
		capabilityScope:       start.ProviderBinding.CapabilityScope,
		routeExpiresAt:        start.ProviderBinding.RoutePolicyExpiresAt,
	}
}

func (authority *atomicGatewayCallExternalAuthority) InspectGatewayRecovery(
	context.Context,
) (GatewayRecoverySnapshot, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.recovery, nil
}

func (authority *atomicGatewayCallExternalAuthority) ValidateGatewayCallExternalAuthority(
	ctx context.Context,
	fact GatewayCallExternalAuthorityFact,
	accept GatewayCallAcceptance,
) error {
	if ctx == nil || ctx.Err() != nil || accept == nil {
		return newError(ErrorDependencyUnavailable)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.recovery.Generation != fact.RecoveryGeneration || authority.recovery.Mode != fact.RecoveryMode ||
		!authority.recovery.ExpiresAt.After(fact.ValidAt) || fact.GrantExpiresAt.After(authority.recovery.ExpiresAt) ||
		authority.routePolicyID != fact.GatewayRoutePolicyID ||
		authority.routePolicyGeneration != fact.GatewayRoutePolicyGeneration ||
		fact.CapabilityScope&^authority.capabilityScope != 0 || !authority.routeExpiresAt.After(fact.ValidAt) ||
		fact.GrantExpiresAt.After(authority.routeExpiresAt) {
		return newError(ErrorIntegrityConflict)
	}
	if authority.blockNext {
		authority.blockNext = false
		close(authority.entered)
		<-authority.release
	}
	return accept()
}

func (authority *atomicGatewayCallExternalAuthority) blockNextValidation() (<-chan struct{}, chan<- struct{}) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.blockNext = true
	authority.entered = make(chan struct{})
	authority.release = make(chan struct{})
	return authority.entered, authority.release
}

func (authority *atomicGatewayCallExternalAuthority) advanceRecovery() {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.recovery.Generation++
	authority.recovery.Mode = GatewayRecoveryDegradedReadOnly
}

func (authority *atomicGatewayCallExternalAuthority) advanceRoutePolicy() {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.routePolicyGeneration++
}

var _ GatewayRecoveryAuthority = (*atomicGatewayCallExternalAuthority)(nil)
var _ GatewayCallExternalAuthority = (*atomicGatewayCallExternalAuthority)(nil)

func (barrier *gatewayCallAcceptanceBarrier) ValidateGatewayCall(
	ctx context.Context,
	fact GatewayCallAuthorityFact,
	accept GatewayCallAcceptance,
) error {
	return barrier.delegate.ValidateGatewayCall(ctx, fact, func() error {
		close(barrier.entered)
		<-barrier.release
		return accept()
	})
}

func TestUsageReceiptEvidenceRootHasNoArbitraryReferenceCap(t *testing.T) {
	references := make([]UsageReceiptReference, 65)
	for index := range references {
		references[index] = UsageReceiptReference{
			UsageReceiptID:   mustUsageReceiptID(t, fmt.Sprintf("usage-receipt-%03d", index)),
			GatewayAttemptID: mustGatewayAttemptID(t, fmt.Sprintf("gateway-attempt-%03d", index)),
			Disposition:      UsageEvidenceKnown,
			CanonicalDigest:  digest(byte(index + 1)),
		}
	}

	root, err := NewUsageReceiptReferenceSet(references)
	if err != nil {
		t.Fatalf("build receipt evidence root: %v", err)
	}
	if root.Count != 65 {
		t.Fatalf("receipt evidence count = %d, want 65", root.Count)
	}
}

func TestGatewayCallExternalAuthorityClosesDelayedAcceptanceAfterReturn(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 1, 0, 0, time.UTC)
	routePolicyID, err := NewGatewayRoutePolicyID("gateway-delayed-callback-route")
	if err != nil {
		t.Fatal(err)
	}
	fact := GatewayCallExternalAuthorityFact{
		RecoveryGeneration: 7, RecoveryMode: GatewayRecoveryWritable,
		GatewayRoutePolicyID: routePolicyID, GatewayRoutePolicyGeneration: 3,
		CapabilityScope: ProviderScopeTextGeneration, GrantExpiresAt: now.Add(time.Minute), ValidAt: now,
	}
	var delayed GatewayCallAcceptance
	authority := GatewayCallExternalAuthorityFunc(func(
		_ context.Context,
		_ GatewayCallExternalAuthorityFact,
		accept GatewayCallAcceptance,
	) error {
		delayed = accept
		return nil
	})
	accepted := 0
	err = validateGatewayCallExternalAuthority(context.Background(), authority, fact, func() error {
		accepted++
		return nil
	})
	if errorCode(err) != ErrorIntegrityConflict || accepted != 0 || delayed == nil {
		t.Fatalf("participant return without acceptance: accepted=%d delayed=%v err=%v", accepted, delayed != nil, err)
	}
	if err := delayed(); errorCode(err) != ErrorIntegrityConflict || accepted != 0 {
		t.Fatalf("delayed callback crossed closed authority fence: accepted=%d err=%v", accepted, err)
	}
}

func TestNonProviderGatewayAndUsageAreExplicitlyNotApplicable(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "non-provider-prerequisite-authority", 7)
	start := standardStart(t, now, authority, "non-provider-prerequisite")
	harness := harnessForStart(t, now, authority, start)

	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Snapshot.Gateway.Applicability != GatewayPrerequisiteNotApplicable ||
		decision.Snapshot.Gateway.Status != GatewayGrantNotApplicable || !decision.Snapshot.Gateway.Ready ||
		decision.Snapshot.Readiness.LLMGateway.State != PrerequisiteNotApplicable ||
		decision.Snapshot.Usage.Disposition != UsageEvidenceNotApplicable ||
		decision.Snapshot.Usage.Receipts != (UsageReceiptReferenceSet{}) {
		t.Fatalf("non-provider prerequisite/evidence = %+v/%+v", decision.Snapshot.Gateway, decision.Snapshot.Usage)
	}
}

func TestGatewayPerCallValidationRejectsLeaseRevokeAndTimeout(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		key    string
		revoke bool
	}{
		{name: "lease revoke", key: "revoke", revoke: true},
		{name: "lease timeout", key: "timeout"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 22, 15, 0, 0, time.UTC)
			authority := mustTaskOrchestrationAuthority(t, "gateway-lease-authority-"+testCase.key, 7)
			start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-"+testCase.key)
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
			fencingID, _ := NewAuthorityID("gateway-fencing-authority-" + testCase.key)
			fencing := NewSecurityLeaseFencingAuthority(fencingID, 3)
			bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-"+testCase.key)
			harness, err = NewDeterministicHarness(HarnessConfig{
				Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
				Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
				AdmissionGrants: []AdmissionGrantFixture{admission}, QuotaReservations: []QuotaReservationFixture{reservation},
				Nodes: []ExecutionNodeFixture{node},
				MaintenanceAuthorities: []RuntimeMaintenanceAuthorityBinding{
					BindLeaseFencingAuthority(node.ExecutionNodeID, fencing),
				},
				LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
					return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
				}),
				RuntimeBindingValidator: bindingValidator,
				ImmutableInputValidator: inputValidator,
				GatewayGrants:           gateway,
				GatewayRecovery:         writableGatewayRecoveryAuthority(now),
				GatewayCallAuthority: gatewayCallExternalAuthorityForStart(
					writableGatewayRecoveryAuthority(now), start,
				),
			})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil {
				t.Fatal(err)
			}
			if err := gateway.BindRuntimeAuthority(harness); err != nil {
				t.Fatal(err)
			}
			if testCase.revoke {
				lease := accepted.Snapshot.Lease
				fence, err := NewFenceSandboxLease(FenceSandboxLeaseInput{
					SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "gateway-call-revoke"),
					PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
					ExpectedRuntimeFence: accepted.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
					LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
					ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation,
					Reason: LeaseFenceRevoked, Authority: fencing,
					ReleaseSafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := harness.Maintenance.Maintain(context.Background(), fence); err != nil {
					t.Fatal(err)
				}
			} else if err := harness.AdvanceClock(91 * time.Second); err != nil {
				t.Fatal(err)
			}
			acceptedAt := now
			if !testCase.revoke {
				acceptedAt = now.Add(91 * time.Second)
			}
			call := gatewayCallForGrant(t, "gateway-call-after-"+testCase.key, accepted.Snapshot,
				start, ProviderScopeTextGeneration, acceptedAt)
			if _, err := gateway.AcceptGatewayCall(context.Background(), call); errorCode(err) != ErrorIntegrityConflict {
				t.Fatalf("new Call after %s error = %v", testCase.name, err)
			}
		})
	}
}

func TestGatewayPerCallValidationRejectsRecoveryReadOnlyButAcceptedAttemptSettles(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 20, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-recovery-authority", 7)
	start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-recovery")
	recovery := GatewayRecoverySnapshot{
		Generation: 7, Mode: GatewayRecoveryWritable, ExpiresAt: now.Add(9 * time.Minute),
	}
	recoveryAuthority := GatewayRecoveryAuthorityFunc(func(context.Context) (GatewayRecoverySnapshot, error) {
		return recovery, nil
	})
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
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-recovery")
	harness, err = NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
		QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		ImmutableInputValidator: inputValidator,
		GatewayGrants:           gateway, GatewayRecovery: recoveryAuthority,
		GatewayCallAuthority: gatewayCallExternalAuthorityForStart(recoveryAuthority, start),
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.BindRuntimeAuthority(harness); err != nil {
		t.Fatal(err)
	}
	first := gatewayCallForGrant(t, "gateway-call-before-read-only", accepted.Snapshot,
		start, ProviderScopeTextGeneration, now)
	firstDecision, err := gateway.AcceptGatewayCall(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	recovery.Generation++
	recovery.Mode = GatewayRecoveryDegradedReadOnly
	second := gatewayCallForGrant(t, "gateway-call-after-read-only", accepted.Snapshot,
		start, ProviderScopeTextGeneration, now)
	if _, err := gateway.AcceptGatewayCall(context.Background(), second); errorCode(err) != ErrorIntegrityConflict {
		t.Fatalf("recovery read-only new Call error = %v", err)
	}
	receipt := settleAttempt(t, gateway, firstDecision.GatewayAttemptID,
		"usage-receipt-after-read-only", UsageEvidenceKnown, now.Add(time.Second))
	if receipt.Disposition != UsageEvidenceKnown {
		t.Fatalf("accepted Attempt did not settle after recovery read-only: %+v", receipt)
	}
}

func TestGatewayCallAcceptanceLinearizesBeforeConcurrentCancel(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 25, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-linearization-authority", 7)
	start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-linearization")
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
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-linearization")
	harness, err = NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
		QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator, ImmutableInputValidator: inputValidator,
		GatewayGrants: gateway, GatewayRecovery: writableGatewayRecoveryAuthority(now),
		GatewayCallAuthority: gatewayCallExternalAuthorityForStart(
			writableGatewayRecoveryAuthority(now), start,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare Gateway Call linearization: snapshot=%+v err=%v", started.Snapshot, err)
	}
	barrier := &gatewayCallAcceptanceBarrier{
		delegate: harness, entered: make(chan struct{}), release: make(chan struct{}),
	}
	if err := gateway.BindRuntimeAuthority(barrier); err != nil {
		t.Fatal(err)
	}
	call := gatewayCallForGrant(t, "gateway-linearized-call", started.Snapshot,
		start, ProviderScopeTextGeneration, now)
	type callResult struct {
		decision GatewayCallDecision
		err      error
	}
	callDone := make(chan callResult, 1)
	go func() {
		decision, acceptErr := gateway.AcceptGatewayCall(context.Background(), call)
		callDone <- callResult{decision: decision, err: acceptErr}
	}()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Gateway Call never reached its authority-fenced acceptance point")
	}
	if harness.store.mu.TryLock() {
		harness.store.mu.Unlock()
		t.Fatal("Runtime authority lock was released before Gateway Call acceptance")
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "cancel-linearized-gateway-call"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID, PhaseRunID: start.PhaseRunID,
		RuntimeRunID: start.RuntimeRunID, ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision,
		ExpectedStartOperationID: start.OperationID, ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
		ExpectedRuntimeFence: started.Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	type runtimeResult struct {
		decision RuntimeDecision
		err      error
	}
	cancelDone := make(chan runtimeResult, 1)
	go func() {
		decision, cancelErr := harness.Runtime.Execute(context.Background(), cancel)
		cancelDone <- runtimeResult{decision: decision, err: cancelErr}
	}()
	close(barrier.release)
	var accepted callResult
	select {
	case accepted = <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Gateway Call acceptance did not complete")
	}
	if accepted.err != nil || accepted.decision.Disposition != GatewayCallAccepted {
		t.Fatalf("authority-first Call was not accepted: decision=%+v err=%v", accepted.decision, accepted.err)
	}
	var cancelled runtimeResult
	select {
	case cancelled = <-cancelDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent cancel did not complete after Call acceptance")
	}
	if cancelled.err != nil || cancelled.decision.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("cancel after accepted Call: snapshot=%+v err=%v", cancelled.decision.Snapshot, cancelled.err)
	}
	settled := settleAttempt(t, gateway, accepted.decision.GatewayAttemptID,
		"usage-receipt-linearized-call", UsageEvidenceKnown, now.Add(time.Second))
	if settled.Disposition != UsageEvidenceKnown {
		t.Fatalf("linearized accepted Attempt did not settle: %+v", settled)
	}
}

func TestGatewayCallAcceptanceLinearizesExternalAuthorityMutation(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		key     string
		advance func(*atomicGatewayCallExternalAuthority)
	}{
		{name: "recovery", key: "recovery", advance: func(authority *atomicGatewayCallExternalAuthority) { authority.advanceRecovery() }},
		{name: "route policy", key: "route-policy", advance: func(authority *atomicGatewayCallExternalAuthority) { authority.advanceRoutePolicy() }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 22, 27, 0, 0, time.UTC)
			authority := mustTaskOrchestrationAuthority(t, "gateway-external-linearization-"+testCase.key, 7)
			start, reservation, admission, node := providerGatewayFixture(
				t, now, authority, "gateway-external-linearization-"+testCase.key,
			)
			external := newAtomicGatewayCallExternalAuthority(start, GatewayRecoverySnapshot{
				Generation: 7, Mode: GatewayRecoveryWritable, ExpiresAt: now.Add(9 * time.Minute),
			})
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
			bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(
				t, "gateway-external-linearization-"+testCase.key,
			)
			harness, err = NewDeterministicHarness(HarnessConfig{
				Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
				Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{admission},
				QuotaReservations: []QuotaReservationFixture{reservation}, Nodes: []ExecutionNodeFixture{node},
				LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
					return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
				}),
				RuntimeBindingValidator: bindingValidator, ImmutableInputValidator: inputValidator,
				GatewayGrants: gateway, GatewayRecovery: external, GatewayCallAuthority: external,
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil || !started.Snapshot.Readiness.CapsuleReady {
				t.Fatalf("prepare external authority linearization: snapshot=%+v err=%v", started.Snapshot, err)
			}
			if err := gateway.BindRuntimeAuthority(harness); err != nil {
				t.Fatal(err)
			}
			entered, release := external.blockNextValidation()
			call := gatewayCallForGrant(t, "gateway-call-before-"+testCase.key+"-advance",
				started.Snapshot, start, ProviderScopeTextGeneration, now)
			type callResult struct {
				decision GatewayCallDecision
				err      error
			}
			callDone := make(chan callResult, 1)
			go func() {
				decision, acceptErr := gateway.AcceptGatewayCall(context.Background(), call)
				callDone <- callResult{decision: decision, err: acceptErr}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Call never reached external authority-fenced acceptance")
			}
			mutationStarted := make(chan struct{})
			mutationDone := make(chan struct{})
			go func() {
				close(mutationStarted)
				testCase.advance(external)
				close(mutationDone)
			}()
			<-mutationStarted
			close(release)
			var accepted callResult
			select {
			case accepted = <-callDone:
			case <-time.After(5 * time.Second):
				t.Fatal("Call did not complete before external authority mutation")
			}
			if accepted.err != nil || accepted.decision.Disposition != GatewayCallAccepted {
				t.Fatalf("authority-first Call = %+v err=%v", accepted.decision, accepted.err)
			}
			select {
			case <-mutationDone:
			case <-time.After(5 * time.Second):
				t.Fatal("external authority mutation did not complete after accepted Call")
			}
			staleCall := gatewayCallForGrant(t, "gateway-call-after-"+testCase.key+"-advance",
				started.Snapshot, start, ProviderScopeTextGeneration, now)
			if _, err := gateway.AcceptGatewayCall(context.Background(), staleCall); errorCode(err) != ErrorIntegrityConflict {
				t.Fatalf("Call after %s advance error = %v", testCase.name, err)
			}
			inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, authority))
			if err != nil || inspected.Gateway.Status != GatewayGrantStale || inspected.Gateway.Ready ||
				inspected.Readiness.LLMGateway.State != PrerequisitePending || inspected.Readiness.CapsuleReady {
				t.Fatalf("Inspect after %s advance = %+v err=%v", testCase.name, inspected, err)
			}
			receipt := settleAttempt(t, gateway, accepted.decision.GatewayAttemptID,
				"usage-receipt-before-"+testCase.key+"-advance", UsageEvidenceKnown, now.Add(time.Second))
			if receipt.Disposition != UsageEvidenceKnown {
				t.Fatalf("accepted Attempt did not settle after %s advance: %+v", testCase.name, receipt)
			}
		})
	}
}

func mustUsageReceiptID(t *testing.T, value string) UsageReceiptID {
	t.Helper()
	id, err := NewUsageReceiptID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustGatewayAttemptID(t *testing.T, value string) GatewayAttemptID {
	t.Helper()
	id, err := NewGatewayAttemptID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGatewayPerCallValidationRejectsOldGenerationAndCancelButAcceptedAttemptsSettle(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "gateway-call-authority", 7)
	start, reservation, admission, node := providerGatewayFixture(t, now, authority, "gateway-call")
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
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "gateway-call")
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
		GatewayCallAuthority: gatewayCallExternalAuthorityForStart(
			writableGatewayRecoveryAuthority(now), start,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Snapshot.Usage.Disposition != UsageEvidenceMissing || initial.Snapshot.Usage.Receipts.Count != 0 {
		t.Fatalf("missing usage was not explicit: %+v", initial.Snapshot.Usage)
	}
	if err := gateway.BindRuntimeAuthority(harness); err != nil {
		t.Fatal(err)
	}
	firstCall := gatewayCallForGrant(t, "gateway-call-1", initial.Snapshot, start, ProviderScopeTextGeneration, now)
	firstAccepted, err := gateway.AcceptGatewayCall(context.Background(), firstCall)
	if err != nil || firstAccepted.Disposition != GatewayCallAccepted {
		t.Fatalf("accept first Call: decision=%+v err=%v", firstAccepted, err)
	}

	if err := harness.AdvanceClock(45 * time.Second); err != nil {
		t.Fatal(err)
	}
	rotated, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || rotated.Snapshot.Gateway.CurrentGrant.Generation != 2 {
		t.Fatalf("rotate grant: snapshot=%+v err=%v", rotated.Snapshot.Gateway, err)
	}
	oldGenerationCall := gatewayCallForGrant(
		t, "gateway-call-old-generation", initial.Snapshot, start, ProviderScopeTextGeneration, now.Add(45*time.Second),
	)
	if _, err := gateway.AcceptGatewayCall(context.Background(), oldGenerationCall); errorCode(err) != ErrorIntegrityConflict {
		t.Fatalf("old generation new Call error = %v", err)
	}
	secondCall := gatewayCallForGrant(
		t, "gateway-call-2", rotated.Snapshot, start, ProviderScopeTextGeneration, now.Add(45*time.Second),
	)
	secondAccepted, err := gateway.AcceptGatewayCall(context.Background(), secondCall)
	if err != nil || secondAccepted.Disposition != GatewayCallAccepted {
		t.Fatalf("accept second Call: decision=%+v err=%v", secondAccepted, err)
	}

	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "cancel-gateway-call"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID, PhaseRunID: start.PhaseRunID,
		RuntimeRunID: start.RuntimeRunID, ExpectedRuntimeRevision: rotated.Snapshot.RuntimeRevision,
		ExpectedStartOperationID:    start.OperationID,
		ExpectedOperationGeneration: rotated.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        rotated.Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch,
		OccurredAt: now.Add(46 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("cancel Runtime: snapshot=%+v err=%v", cancelled.Snapshot, err)
	}
	if cancelled.Snapshot.Gateway.Status != GatewayGrantStale || cancelled.Snapshot.Gateway.Ready ||
		cancelled.Snapshot.Readiness.LLMGateway.State != PrerequisitePending ||
		cancelled.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("cancel did not fail closed the Gateway prerequisite: gateway=%+v readiness=%+v",
			cancelled.Snapshot.Gateway, cancelled.Snapshot.Readiness)
	}
	newCallAfterCancel := gatewayCallForGrant(
		t, "gateway-call-after-cancel", rotated.Snapshot, start, ProviderScopeTextGeneration, now.Add(46*time.Second),
	)
	if _, err := gateway.AcceptGatewayCall(context.Background(), newCallAfterCancel); errorCode(err) != ErrorIntegrityConflict {
		t.Fatalf("post-cancel new Call error = %v", err)
	}

	firstReceipt := settleAttempt(t, gateway, firstAccepted.GatewayAttemptID, "usage-receipt-1", UsageEvidenceKnown, now.Add(47*time.Second))
	secondReceipt := settleAttempt(t, gateway, secondAccepted.GatewayAttemptID, "usage-receipt-2", UsageEvidenceUnknown, now.Add(48*time.Second))
	if firstReceipt.Disposition != UsageEvidenceKnown || secondReceipt.Disposition != UsageEvidenceUnknown ||
		firstReceipt.CanonicalDigest == (Digest{}) || secondReceipt.CanonicalDigest == (Digest{}) {
		t.Fatalf("accepted Attempts lost typed receipts: first=%+v second=%+v", firstReceipt, secondReceipt)
	}
	inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, authority))
	if err != nil || inspected.State != RuntimeTerminal || inspected.Outcome != RuntimeCancelled ||
		inspected.RuntimeRevision != cancelled.Snapshot.RuntimeRevision {
		t.Fatalf("usage settlement reopened Runtime: snapshot=%+v err=%v", inspected, err)
	}
	late, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || late.Snapshot.State != RuntimeTerminal || late.Snapshot.Outcome != RuntimeCancelled ||
		late.Snapshot.RuntimeRevision != cancelled.Snapshot.RuntimeRevision ||
		late.Snapshot.Usage.Disposition != UsageEvidenceUnknown || late.Snapshot.Usage.Receipts.Count != 2 {
		t.Fatalf("late receipts changed terminal outcome or lost disposition: snapshot=%+v err=%v", late.Snapshot, err)
	}
	expectedRoot, err := NewUsageReceiptReferenceSet([]UsageReceiptReference{firstReceipt, secondReceipt})
	if err != nil || late.Snapshot.Usage.Receipts != expectedRoot {
		t.Fatalf("late typed receipt root = %+v, want %+v, err=%v", late.Snapshot.Usage.Receipts, expectedRoot, err)
	}
	evidence, err := gateway.QueryUsageReceiptEvidence(context.Background(), start.RuntimeRunID)
	if err != nil || len(evidence.References) != 2 || evidence.References[0] != firstReceipt || evidence.References[1] != secondReceipt {
		t.Fatalf("query late typed receipt references = %+v, err=%v", evidence, err)
	}
	if evidence.Disposition != UsageEvidenceUnknown {
		t.Fatalf("late typed receipt disposition = %v", evidence.Disposition)
	}
}

func gatewayCallForGrant(
	t *testing.T,
	id string,
	snapshot RuntimeSnapshot,
	start StartRuntimeRun,
	capability ProviderCapabilityScope,
	acceptedAt time.Time,
) GatewayCallRequest {
	t.Helper()
	grant := snapshot.Gateway.CurrentGrant
	callID, err := NewGatewayCallID(id)
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewGatewayCallRequest(GatewayCallRequestInput{
		GatewayCallID: callID, RuntimeRunID: start.RuntimeRunID, StartOperationID: start.OperationID,
		GatewayGrantID: grant.GatewayGrantID, GatewayGrantGeneration: grant.Generation,
		LeaseID: grant.LeaseID, LeaseGeneration: grant.LeaseGeneration, LeaseFence: grant.LeaseFence,
		RuntimeFence: grant.RuntimeFence, QuotaReservationID: grant.QuotaReservationID,
		QuotaReservationGeneration:   grant.QuotaReservationGeneration,
		QuotaReservationMode:         grant.QuotaReservationMode,
		GatewayRoutePolicyID:         grant.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: grant.GatewayRoutePolicyGeneration,
		Capability:                   capability, AcceptedAt: acceptedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func settleAttempt(
	t *testing.T,
	gateway GatewayCallAccess,
	attemptID GatewayAttemptID,
	receiptValue string,
	disposition UsageEvidenceDisposition,
	observedAt time.Time,
) UsageReceiptReference {
	t.Helper()
	receiptID, err := NewUsageReceiptID(receiptValue)
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := NewGatewayAttemptSettlement(GatewayAttemptSettlementInput{
		GatewayAttemptID: attemptID, UsageReceiptID: receiptID, Disposition: disposition, ObservedAt: observedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := gateway.SettleGatewayAttempt(context.Background(), settlement)
	if err != nil {
		t.Fatalf("settle accepted Attempt: %v", err)
	}
	return reference
}
