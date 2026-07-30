package runtimeexecution

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProviderCapableAdmissionRequiresExactActiveQuotaReservation(t *testing.T) {
	now := time.Date(2026, time.July, 29, 20, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name    string
		missing bool
		mutate  func(*QuotaReservationFixture)
		want    DecisionDisposition
	}{
		{name: "missing", missing: true, want: DecisionRejected},
		{name: "inactive", mutate: func(value *QuotaReservationFixture) { value.State = QuotaReservationInactive }, want: DecisionRejected},
		{name: "wrong workspace", mutate: func(value *QuotaReservationFixture) {
			value.PersonalWorkspaceID = mustPersonalWorkspaceID(t, "foreign-workspace")
		}, want: DecisionRejected},
		{name: "wrong task", mutate: func(value *QuotaReservationFixture) { value.TaskID = mustTaskID(t, "foreign-task") }, want: DecisionRejected},
		{name: "wrong phase", mutate: func(value *QuotaReservationFixture) { value.PhaseRunID = mustPhaseRunID(t, "foreign-phase") }, want: DecisionRejected},
		{name: "wrong generation", mutate: func(value *QuotaReservationFixture) { value.Generation++ }, want: DecisionRejected},
		{name: "wrong mode", mutate: func(value *QuotaReservationFixture) { value.Mode = QuotaReservationEnforced }, want: DecisionRejected},
		{name: "wrong authorization generation", mutate: func(value *QuotaReservationFixture) { value.AuthorizationGeneration++ }, want: DecisionRejected},
		{name: "wrong route policy", mutate: func(value *QuotaReservationFixture) { value.GatewayRoutePolicyGeneration++ }, want: DecisionRejected},
		{name: "wrong capability scope", mutate: func(value *QuotaReservationFixture) { value.CapabilityScope = ProviderScopeImageGeneration }, want: DecisionRejected},
		{name: "expired", mutate: func(value *QuotaReservationFixture) { value.ExpiresAt = now }, want: DecisionRejected},
		{name: "exact active", want: DecisionAccepted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			suffix := strings.ReplaceAll(testCase.name, " ", "-")
			authority := mustTaskOrchestrationAuthority(t, "provider-admission-authority-"+suffix, 7)
			input := standardStart(t, now, authority, "provider-admission-"+suffix).StartRuntimeRunInput
			input.ProviderCapability = ProviderCapabilityRequired
			input.ProviderBinding = &ProviderExecutionBinding{
				QuotaReservationID:           mustQuotaReservationID(t, "reservation-provider-admission-"+suffix),
				Generation:                   4,
				Mode:                         QuotaReservationObservation,
				GatewayRoutePolicyID:         mustGatewayRoutePolicyID(t, "route-provider-admission-"+suffix),
				GatewayRoutePolicyGeneration: 3,
				CapabilityScope:              ProviderScopeTextGeneration,
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
			if testCase.mutate != nil {
				testCase.mutate(&reservation)
			}
			reservations := []QuotaReservationFixture{reservation}
			if testCase.missing {
				reservations = nil
			}
			leaseObservations := 0
			harness, err := NewDeterministicHarness(HarnessConfig{
				Now:               now,
				IDs:               DeterministicIDConfig{DecisionStart: 1},
				Runtimes:          []RuntimeFixture{runtimeFixtureForStart(start, authority)},
				AdmissionGrants:   []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(10*time.Minute), true)},
				QuotaReservations: reservations,
				LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
					leaseObservations++
					return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionTemporaryUnavailable}, nil
				}),
				RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
			})
			if err != nil {
				t.Fatalf("new harness: %v", err)
			}

			decision, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil {
				t.Fatalf("execute provider start: %v", err)
			}
			if decision.Fact.Disposition != testCase.want {
				t.Fatalf("admission disposition = %v, want %v: %+v", decision.Fact.Disposition, testCase.want, decision)
			}
			if testCase.want == DecisionRejected {
				if decision.Snapshot.State != RuntimeCreated || decision.Snapshot.Lease.AcquireStatus != LeaseNotRequested || leaseObservations != 0 {
					t.Fatalf("invalid Reservation crossed admission: decision=%+v lease calls=%d", decision, leaseObservations)
				}
				return
			}
			if decision.Snapshot.State != RuntimeWaitingForLease || leaseObservations != 1 {
				t.Fatalf("exact Reservation did not cross admission once: decision=%+v lease calls=%d", decision, leaseObservations)
			}
		})
	}
}

func TestProviderCapableLeaseRevalidatesReservationAfterAdmission(t *testing.T) {
	now := time.Date(2026, time.July, 29, 20, 30, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name   string
		mutate func(*QuotaReservationFixture)
	}{
		{name: "inactive", mutate: func(value *QuotaReservationFixture) { value.State = QuotaReservationInactive }},
		{name: "wrong phase", mutate: func(value *QuotaReservationFixture) {
			value.PhaseRunID = mustPhaseRunID(t, "foreign-provider-lease-phase")
		}},
		{name: "wrong generation", mutate: func(value *QuotaReservationFixture) { value.Generation++ }},
		{name: "wrong mode", mutate: func(value *QuotaReservationFixture) { value.Mode = QuotaReservationEnforced }},
		{name: "expired", mutate: func(value *QuotaReservationFixture) { value.ExpiresAt = now }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertProviderLeaseRevalidationRejects(t, now, strings.ReplaceAll(testCase.name, " ", "-"), testCase.mutate)
		})
	}
}

func assertProviderLeaseRevalidationRejects(
	t *testing.T,
	now time.Time,
	suffix string,
	mutate func(*QuotaReservationFixture),
) {
	t.Helper()
	authority := mustTaskOrchestrationAuthority(t, "provider-lease-revalidation-authority-"+suffix, 7)
	input := standardStart(t, now, authority, "provider-lease-revalidation-"+suffix).StartRuntimeRunInput
	input.ProviderCapability = ProviderCapabilityRequired
	input.ProviderBinding = &ProviderExecutionBinding{
		QuotaReservationID:           mustQuotaReservationID(t, "reservation-provider-lease-revalidation-"+suffix),
		Generation:                   4,
		Mode:                         QuotaReservationObservation,
		GatewayRoutePolicyID:         mustGatewayRoutePolicyID(t, "route-provider-lease-revalidation-"+suffix),
		GatewayRoutePolicyGeneration: 3,
		CapabilityScope:              ProviderScopeTextGeneration,
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
	grant.ExecutionNodeID = startNodeID(t, "provider-lease-revalidation-node-"+suffix)
	grant.NodeCapacityGeneration = 1
	var harness *DeterministicHarness
	harnessConfig := HarnessConfig{
		Now:               now,
		IDs:               DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:          []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:   []AdmissionGrantFixture{grant},
		QuotaReservations: []QuotaReservationFixture{reservation},
		Nodes:             []ExecutionNodeFixture{executionNodeFixtureForStart(t, start, grant, now)},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
			stale := reservation
			mutate(&stale)
			if err := harness.ReplaceQuotaReservation(stale); err != nil {
				t.Fatalf("replace Reservation between admission and lease: %v", err)
			}
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
	}
	var err error
	harness, err = NewDeterministicHarness(harnessConfig)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute provider start: %v", err)
	}
	if decision.Fact.Disposition != DecisionAccepted || decision.Snapshot.State != RuntimeTerminal ||
		decision.Snapshot.Outcome != RuntimeRejected || decision.Snapshot.PreLeaseTerminalReason != PreLeaseTerminalReservation ||
		decision.Snapshot.Capacity.NoLease != NoLeaseDispositionRecorded {
		t.Fatalf("stale Reservation crossed lease commit: %+v", decision)
	}
}
