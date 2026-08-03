package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

var _ OwnedDispatch = (*PostgresAuthority)(nil)

func TestExecuteBuildsImmutableCapsuleAndOwnedDispatchWithoutLeakingPrivateCanaries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 30, 9, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "capsule-authority", 7)
	start := standardStart(t, now, authority, "capsule-tracer")
	start.Trace.TraceID = TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "capsule-tracer-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "capsule-binding-evidence", digest(81)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "capsule-input-evidence", digest(82)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !accepted.Snapshot.Readiness.CapsuleReady ||
		accepted.Snapshot.Capsule.State != CapsulePrepared ||
		accepted.Snapshot.Capsule.CapsuleID == (ExecutionCapsuleID{}) ||
		accepted.Snapshot.Capsule.Digest == (Digest{}) {
		t.Fatalf("public capsule facts = %+v readiness=%+v", accepted.Snapshot.Capsule, accepted.Snapshot.Readiness)
	}

	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID,
		CapsuleID:    accepted.Snapshot.Capsule.CapsuleID,
		Digest:       accepted.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatalf("claim dispatch: %v", err)
	}
	if !validOpaqueID(delivery.OperationID.String()) ||
		delivery.CapsuleID != accepted.Snapshot.Capsule.CapsuleID ||
		delivery.CapsuleDigest != accepted.Snapshot.Capsule.Digest ||
		digestBytes(delivery.Capsule) != delivery.CapsuleDigest {
		t.Fatalf("dispatch changed immutable capsule binding: %+v", delivery)
	}
	immutablePayload := append([]byte(nil), delivery.Capsule...)
	ackRequest := DispatchAcknowledgementRequest{
		OperationID: delivery.OperationID, RuntimeRunID: delivery.RuntimeRunID,
		CapsuleID: delivery.CapsuleID, CapsuleDigest: delivery.CapsuleDigest, AckDigest: digest(83),
	}
	acknowledged, err := harness.Dispatch.AcknowledgeDispatch(context.Background(), ackRequest)
	if err != nil || acknowledged.Disposition != DispatchAcknowledged || acknowledged.AckDigest != ackRequest.AckDigest {
		t.Fatalf("acknowledge dispatch: %+v err=%v", acknowledged, err)
	}
	replayedAck, err := harness.Dispatch.AcknowledgeDispatch(context.Background(), ackRequest)
	if err != nil || replayedAck != acknowledged {
		t.Fatalf("ack replay changed disposition: first=%+v replay=%+v err=%v", acknowledged, replayedAck, err)
	}
	if _, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: delivery.RuntimeRunID, CapsuleID: delivery.CapsuleID, Digest: delivery.CapsuleDigest,
	}); err == nil {
		t.Fatal("acknowledged delivery was claimed again")
	}
	if !bytes.Equal(delivery.Capsule, immutablePayload) || digestBytes(delivery.Capsule) != delivery.CapsuleDigest {
		t.Fatal("delivery acknowledgement mutated Capsule payload")
	}
	decoded, err := decodeExecutionCapsule(delivery.Capsule)
	if err != nil {
		t.Fatalf("strict capsule decode: %v", err)
	}
	if decoded.RuntimeRunID != start.RuntimeRunID || decoded.OperationID != start.OperationID ||
		decoded.SandboxLeaseID != accepted.Snapshot.Lease.LeaseID ||
		decoded.LeaseGeneration != accepted.Snapshot.Lease.Generation ||
		decoded.LeaseFence != accepted.Snapshot.Lease.Fence ||
		decoded.OutputContractDigest != start.OutputContractDigest ||
		decoded.EvidenceContractDigest != start.EvidenceContractDigest || decoded.Trace != start.Trace {
		t.Fatalf("capsule lost exact authority binding: %+v", decoded)
	}
	if decoded.SecurityAcceptance.ActualImageDigest == (Digest{}) ||
		decoded.SecurityAcceptance.ActualExecutorDigest == (Digest{}) ||
		len(decoded.Inputs.Entries) != 1 || !safeSandboxLogicalPath(decoded.Inputs.Entries[0].LogicalLocation) ||
		decoded.Inputs.Entries[0].ReadCapabilityID == (InputReadCapabilityID{}) ||
		len(decoded.Outputs.Channels) != 1 || !decoded.Security.Network.DefaultDenyEgress ||
		!decoded.Security.Network.DefaultDenyIngress {
		t.Fatalf("capsule omitted resolved input/output/security facts: %+v", decoded)
	}
	var readOnlyWire map[string]json.RawMessage
	if err := json.Unmarshal(delivery.Capsule, &readOnlyWire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"runtime_view_id", "runtime_view_open_operation_id", "runtime_view_open_request_digest",
		"runtime_view_lease_authority_digest", "runtime_view_expires_at",
		"runtime_view_lifecycle_generation", "runtime_view_lifecycle_fence",
	} {
		if _, exists := readOnlyWire[field]; exists {
			t.Fatalf("read-only Capsule carried mutating Runtime View field %q", field)
		}
	}
	readOnlyWire["runtime_view_lifecycle_fence"] = json.RawMessage("1")
	unexpectedRuntimeViewField, err := json.Marshal(readOnlyWire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeExecutionCapsule(unexpectedRuntimeViewField); err == nil {
		t.Fatal("strict Capsule decoder accepted a mutating Runtime View field on a read-only Capsule")
	}
	delete(readOnlyWire, "runtime_view_lifecycle_fence")
	readOnlyWire["trace_id"] = json.RawMessage(`"raw-user-prompt-content"`)
	invalidTrace, err := json.Marshal(readOnlyWire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeExecutionCapsule(invalidTrace); err == nil {
		t.Fatal("strict Capsule decoder accepted non-TraceID trace content")
	}
	strictMutations := []struct {
		name string
		edit func(*executionCapsuleWireV1)
	}{
		{name: "unknown output class", edit: func(wire *executionCapsuleWireV1) {
			wire.OutputChannels[0].Class = 255
		}},
		{name: "network default allow", edit: func(wire *executionCapsuleWireV1) {
			wire.DefaultDenyEgress = false
		}},
		{name: "missing network generation", edit: func(wire *executionCapsuleWireV1) {
			wire.NetworkGeneration = 0
		}},
		{name: "missing secret generation", edit: func(wire *executionCapsuleWireV1) {
			wire.SecretGeneration = 0
		}},
	}
	for _, mutation := range strictMutations {
		t.Run(mutation.name, func(t *testing.T) {
			var wire executionCapsuleWireV1
			if err := json.Unmarshal(delivery.Capsule, &wire); err != nil {
				t.Fatal(err)
			}
			mutation.edit(&wire)
			changed, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeExecutionCapsule(changed); err == nil {
				t.Fatal("strict Capsule decoder accepted malformed required variant")
			}
		})
	}
	unknown := append([]byte(nil), delivery.Capsule...)
	unknown = bytes.Replace(unknown, []byte(`{"schema_version"`), []byte(`{"unknown_required":true,"schema_version"`), 1)
	if _, err := decodeExecutionCapsule(unknown); err == nil {
		t.Fatal("strict Capsule decoder accepted an unknown required field")
	}
	duplicateSchema := bytes.Replace(
		delivery.Capsule,
		[]byte(`{"schema_version":"`+executionCapsuleWireSchemaV1+`"`),
		[]byte(`{"schema_version":"`+executionCapsuleWireSchemaV1+`","schema_version":"`+executionCapsuleWireSchemaV1+`"`),
		1,
	)
	if bytes.Equal(duplicateSchema, delivery.Capsule) {
		t.Fatal("duplicate-field Capsule mutation did not enter the decoder seam")
	}
	if _, err := decodeExecutionCapsule(duplicateSchema); err == nil {
		t.Fatal("strict Capsule decoder accepted a duplicate required field")
	}
	nullNetworkRules := bytes.Replace(delivery.Capsule, []byte(`"network_rules":[]`), []byte(`"network_rules":null`), 1)
	if bytes.Equal(nullNetworkRules, delivery.Capsule) {
		t.Fatal("null-array Capsule mutation did not enter the decoder seam")
	}
	if _, err := decodeExecutionCapsule(nullNetworkRules); err == nil {
		t.Fatal("strict Capsule decoder accepted null for a required rule collection")
	}

	for _, canary := range [][]byte{
		[]byte("/host/private/runtime.sock"),
		[]byte("s3://private-bucket/object-key"),
		[]byte("postgres://platform-admin:secret@db/platform"),
		[]byte("raw-user-prompt-content"),
	} {
		if bytes.Contains(delivery.Capsule, canary) {
			t.Fatalf("capsule leaked private canary %q", canary)
		}
	}

	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.Snapshot.Capsule != accepted.Snapshot.Capsule {
		t.Fatalf("replay allocated a different capsule: first=%+v replay=%+v",
			accepted.Snapshot.Capsule, replayed.Snapshot.Capsule)
	}
}

func TestClaimDispatchBindsEveryExactRuntimeViewCapabilityFact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		suffix string
		mutate func(*RuntimeViewBindingSnapshot)
	}{
		{name: "open operation", suffix: "open-operation", mutate: func(binding *RuntimeViewBindingSnapshot) {
			binding.OpenOperationID = mustOperationID(t, "stale-runtime-view-open-operation")
		}},
		{name: "open request digest", suffix: "open-request-digest", mutate: func(binding *RuntimeViewBindingSnapshot) {
			binding.OpenRequestDigest = digest(221)
		}},
		{name: "lease authority digest", suffix: "lease-authority-digest", mutate: func(binding *RuntimeViewBindingSnapshot) {
			binding.SandboxLeaseAuthorityDigest = digest(222)
		}},
		{name: "expiry", suffix: "expiry", mutate: func(binding *RuntimeViewBindingSnapshot) {
			binding.ExpiresAt = time.Date(2026, time.July, 30, 9, 4, 0, 0, time.UTC)
		}},
		{name: "lifecycle generation", suffix: "lifecycle-generation", mutate: func(binding *RuntimeViewBindingSnapshot) {
			binding.LifecycleGeneration++
		}},
		{name: "lifecycle fence", suffix: "lifecycle-fence", mutate: func(binding *RuntimeViewBindingSnapshot) {
			binding.LifecycleFence++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 30, 9, 5, 0, 0, time.UTC)
			authority := mustTaskOrchestrationAuthority(t, "runtime-view-capsule-authority-"+test.suffix, 7)
			start, grant, node := mutatingPrerequisiteStart(
				t, now, authority, "runtime-view-capsule-"+test.suffix, digest(220),
			)
			lifecycle := RuntimeViewPrerequisiteAdapter{
				OpenRuntimeViewFunc: func(
					_ context.Context,
					request taskworkspace.OpenRuntimeViewRequest,
				) (taskworkspace.OpenRuntimeViewResult, error) {
					return acceptedRuntimeViewResult(request, "exact-runtime-view"), nil
				},
			}
			harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
			accepted, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
				t.Fatalf("prepare exact Runtime View Capsule: %+v err=%v", accepted, err)
			}
			claimed, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
				RuntimeRunID: start.RuntimeRunID, CapsuleID: accepted.Snapshot.Capsule.CapsuleID,
				Digest: accepted.Snapshot.Capsule.Digest,
			})
			if err != nil {
				t.Fatalf("claim current Runtime View Capsule: %v", err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(claimed.Capsule, &wire); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{
				"runtime_view_id", "runtime_view_open_operation_id", "runtime_view_open_request_digest",
				"runtime_view_lease_authority_digest", "runtime_view_expires_at",
				"runtime_view_lifecycle_generation", "runtime_view_lifecycle_fence",
			} {
				if _, exists := wire[field]; !exists {
					t.Fatalf("mutating Capsule omitted exact Runtime View field %q", field)
				}
			}
			decoded, err := decodeExecutionCapsule(claimed.Capsule)
			if err != nil {
				t.Fatal(err)
			}
			binding := accepted.Snapshot.RuntimeViewBinding
			if decoded.RuntimeViewID != binding.RuntimeViewID ||
				decoded.RuntimeViewOpenOperationID != binding.OpenOperationID ||
				decoded.RuntimeViewOpenRequestDigest != binding.OpenRequestDigest ||
				decoded.RuntimeViewLeaseAuthorityDigest != binding.SandboxLeaseAuthorityDigest ||
				!decoded.RuntimeViewExpiresAt.Equal(binding.ExpiresAt) ||
				decoded.RuntimeViewLifecycleGeneration != binding.LifecycleGeneration ||
				decoded.RuntimeViewLifecycleFence != binding.LifecycleFence {
				t.Fatalf("Capsule lost exact Runtime View binding: capsule=%+v binding=%+v", decoded, binding)
			}
			if test.suffix == "open-operation" {
				delete(wire, "runtime_view_open_request_digest")
				missingRequired, err := json.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := decodeExecutionCapsule(missingRequired); err == nil {
					t.Fatal("strict Capsule decoder accepted a missing mutating Runtime View field")
				}
			}

			harness.store.mu.Lock()
			test.mutate(&harness.store.runtimes[start.RuntimeRunID].runtimeViewBinding)
			harness.store.mu.Unlock()
			denied, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
				RuntimeRunID: start.RuntimeRunID, CapsuleID: accepted.Snapshot.Capsule.CapsuleID,
				Digest: accepted.Snapshot.Capsule.Digest,
			})
			assertErrorCode(t, err, ErrorAuthorizationDenied)
			if len(denied.Capsule) != 0 {
				t.Fatal("stale Runtime View authority returned Capsule bytes")
			}
		})
	}
}

func TestClaimDispatchRevalidatesCurrentQuotaReservationAuthority(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 30, 9, 10, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*QuotaReservationFixture)
	}{
		{name: "generation", mutate: func(reservation *QuotaReservationFixture) { reservation.Generation++ }},
		{name: "state", mutate: func(reservation *QuotaReservationFixture) { reservation.State = QuotaReservationInactive }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness, start, reservation, accepted := newReadyProviderCapsuleHarness(
				t, now, "dispatch-reservation-"+test.name,
			)
			test.mutate(&reservation)
			if err := harness.ReplaceQuotaReservation(reservation); err != nil {
				t.Fatal(err)
			}
			assertDispatchAuthorizationDeniedWithoutPayload(
				t, harness.Dispatch, start.RuntimeRunID, accepted.Snapshot.Capsule,
			)
		})
	}
}

func TestCapsuleCreationRevalidatesCurrentQuotaReservationAuthority(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 30, 9, 10, 30, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "create-reservation-authority", 7)
	start, reservation, admission, node := providerGatewayFixture(t, now, authority, "create-reservation")
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "create-reservation")
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
	resolver := deterministicCapsuleResolver{}
	harness, err = NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{admission}, QuotaReservations: []QuotaReservationFixture{reservation},
		Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator, ImmutableInputValidator: inputValidator,
		GatewayGrants: gateway, GatewayRecovery: writableGatewayRecoveryAuthority(now),
		ExecutionCapsuleResolver: ExecutionCapsuleResolverFunc(func(
			ctx context.Context,
			request ExecutionCapsuleResolutionRequest,
		) (ExecutionCapsuleResolution, error) {
			resolution, err := resolver.ResolveExecutionCapsule(ctx, request)
			reservation.State = QuotaReservationInactive
			if replaceErr := harness.ReplaceQuotaReservation(reservation); replaceErr != nil {
				return ExecutionCapsuleResolution{}, replaceErr
			}
			return resolution, err
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	decision, err := harness.Runtime.Execute(context.Background(), start)
	assertErrorCode(t, err, ErrorAuthorizationDenied)
	if decision.Snapshot.Capsule.State == CapsulePrepared || decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("stale Reservation prepared in-memory Capsule: %+v", decision.Snapshot)
	}
}

func TestPostgresClaimDispatchRevalidatesCurrentQuotaReservationAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 30, 9, 11, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate string
	}{
		{name: "generation", mutate: "generation=generation+1"},
		{name: "state", mutate: "state=2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, quotaTable, store, start, accepted := newPostgresReadyProviderCapsuleRuntime(
				t, now, "cap_pg_res_"+test.name,
			)
			current, err := store.ClaimDispatch(context.Background(), DispatchClaimRequest{
				RuntimeRunID: start.RuntimeRunID, CapsuleID: accepted.Snapshot.Capsule.CapsuleID,
				Digest: accepted.Snapshot.Capsule.Digest,
			})
			if err != nil || len(current.Capsule) == 0 {
				t.Fatalf("current PostgreSQL Reservation did not authorize dispatch: %+v err=%v", current, err)
			}
			if _, err := db.ExecContext(context.Background(), `UPDATE `+quotaTable+` SET `+test.mutate+
				` WHERE quota_reservation_id=$1`, accepted.Snapshot.Gateway.CurrentGrant.QuotaReservationID.String()); err != nil {
				t.Fatal(err)
			}
			assertDispatchAuthorizationDeniedWithoutPayload(
				t, store, start.RuntimeRunID, accepted.Snapshot.Capsule,
			)
		})
	}
}

func TestPostgresCapsuleCreationRevalidatesCurrentQuotaReservationAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 30, 9, 11, 30, 0, time.UTC)
	db, quotaTable, store, start, _ := newPostgresProviderCapsuleRuntime(
		t, now, "cap_pg_create_res", false,
	)
	resolver := deterministicCapsuleResolver{}
	store.executionCapsuleResolver = ExecutionCapsuleResolverFunc(func(
		ctx context.Context,
		request ExecutionCapsuleResolutionRequest,
	) (ExecutionCapsuleResolution, error) {
		resolution, err := resolver.ResolveExecutionCapsule(ctx, request)
		if err != nil {
			return ExecutionCapsuleResolution{}, err
		}
		if _, err := db.ExecContext(ctx, `UPDATE `+quotaTable+` SET state=2`); err != nil {
			return ExecutionCapsuleResolution{}, err
		}
		return resolution, nil
	})

	decision, err := store.Execute(context.Background(), start)
	assertErrorCode(t, err, ErrorAuthorizationDenied)
	if decision.Snapshot.Capsule.State == CapsulePrepared || decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("stale Reservation prepared Capsule: %+v", decision.Snapshot)
	}
	var capsules, audits, outbox, delivery int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+store.table("runtime_execution_capsules")+`),
		(SELECT count(*) FROM `+store.table("runtime_execution_capsule_audit")+`),
		(SELECT count(*) FROM `+store.table("runtime_execution_dispatch_outbox")+`),
		(SELECT count(*) FROM `+store.table("runtime_execution_dispatch_delivery")+`)`).
		Scan(&capsules, &audits, &outbox, &delivery); err != nil {
		t.Fatal(err)
	}
	if capsules != 0 || audits != 0 || outbox != 0 || delivery != 0 {
		t.Fatalf("stale Reservation persisted Capsule family: capsule=%d audit=%d outbox=%d delivery=%d",
			capsules, audits, outbox, delivery)
	}
}

func TestClaimDispatchDeniesStaleAuthorityMatrixWithoutCapsuleBytes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 30, 9, 12, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*runtimeRecord)
	}{
		{name: "lease expiry", mutate: func(record *runtimeRecord) { record.lease.ExpiresAt = now }},
		{name: "lease generation", mutate: func(record *runtimeRecord) { record.lease.Generation++ }},
		{name: "lease fence", mutate: func(record *runtimeRecord) { record.lease.Fence++ }},
		{name: "lease revoke", mutate: func(record *runtimeRecord) { record.lease.Disposition = LeaseRevoked }},
		{name: "runtime fence", mutate: func(record *runtimeRecord) { record.fixture.RuntimeFence++ }},
		{name: "release safety epoch", mutate: func(record *runtimeRecord) { record.fixture.SafetyEpoch++ }},
		{name: "catalog safety epoch", mutate: func(record *runtimeRecord) { record.catalogSafetyEpoch++ }},
		{name: "node generation", mutate: func(record *runtimeRecord) { record.node.Generation++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness, start, accepted := newReadyReadOnlyCapsuleHarness(
				t, now, "stale-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			harness.store.mu.Lock()
			test.mutate(harness.store.runtimes[start.RuntimeRunID])
			harness.store.mu.Unlock()
			assertDispatchAuthorizationDeniedWithoutPayload(
				t, harness.Dispatch, start.RuntimeRunID, accepted.Snapshot.Capsule,
			)
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*GatewayGrant)
	}{
		{name: "Gateway Grant expiry", mutate: func(grant *GatewayGrant) { grant.ExpiresAt = now }},
		{name: "Gateway Grant generation", mutate: func(grant *GatewayGrant) { grant.Generation++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness, start, _, accepted := newReadyProviderCapsuleHarness(
				t, now, "stale-"+strings.ReplaceAll(strings.ToLower(test.name), " ", "-"),
			)
			harness.store.mu.Lock()
			test.mutate(&harness.store.runtimes[start.RuntimeRunID].gateway.CurrentGrant)
			harness.store.mu.Unlock()
			assertDispatchAuthorizationDeniedWithoutPayload(
				t, harness.Dispatch, start.RuntimeRunID, accepted.Snapshot.Capsule,
			)
		})
	}
}

func TestClaimDispatchNonEnumerationMakesMissingWrongAndCrossWorkspaceIdentitiesIndistinguishable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 30, 9, 14, 0, 0, time.UTC)
	firstAuthority := mustTaskOrchestrationAuthority(t, "dispatch-nonenum-first-authority", 7)
	secondAuthority := mustTaskOrchestrationAuthority(t, "dispatch-nonenum-second-authority", 8)
	first := standardStart(t, now, firstAuthority, "dispatch-nonenum-first")
	second := standardStart(t, now, secondAuthority, "dispatch-nonenum-second")
	firstGrant := grantFixtureForStart(first, now.Add(10*time.Minute), true)
	firstGrant.ExecutionNodeID, firstGrant.NodeCapacityGeneration = startNodeID(t, "dispatch-nonenum-first-node"), 1
	secondGrant := grantFixtureForStart(second, now.Add(10*time.Minute), true)
	secondGrant.ExecutionNodeID, secondGrant.NodeCapacityGeneration = startNodeID(t, "dispatch-nonenum-second-node"), 1
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes: []RuntimeFixture{
			runtimeFixtureForStart(first, firstAuthority), runtimeFixtureForStart(second, secondAuthority),
		},
		AdmissionGrants: []AdmissionGrantFixture{firstGrant, secondGrant},
		Nodes: []ExecutionNodeFixture{
			executionNodeFixtureForStart(t, first, firstGrant, now), executionNodeFixtureForStart(t, second, secondGrant, now),
		},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "dispatch-nonenum-input", digest(223)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstAccepted, err := harness.Runtime.Execute(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	secondAccepted, err := harness.Runtime.Execute(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	requests := []DispatchClaimRequest{
		{RuntimeRunID: mustRuntimeRunID(t, "missing-dispatch-runtime"), CapsuleID: firstAccepted.Snapshot.Capsule.CapsuleID, Digest: firstAccepted.Snapshot.Capsule.Digest},
		{RuntimeRunID: first.RuntimeRunID, CapsuleID: ExecutionCapsuleID{value: "wrong-capsule-identity"}, Digest: digest(224)},
		{RuntimeRunID: second.RuntimeRunID, CapsuleID: firstAccepted.Snapshot.Capsule.CapsuleID, Digest: firstAccepted.Snapshot.Capsule.Digest},
	}
	var firstError string
	for index, request := range requests {
		delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), request)
		assertErrorCode(t, err, ErrorAuthorizationDenied)
		if !emptyDispatchDelivery(delivery) {
			t.Fatalf("non-enumerating denial %d returned dispatch facts: %+v", index, delivery)
		}
		if index == 0 {
			firstError = err.Error()
		} else if err.Error() != firstError {
			t.Fatalf("non-enumerating denial %d differed: %q vs %q", index, err.Error(), firstError)
		}
	}
	if secondAccepted.Snapshot.Capsule.State != CapsulePrepared {
		t.Fatal("cross-Workspace fixture did not contain an independently existing Capsule")
	}
}

func TestCapsuleResolverRawFailureIsNormalizedWithoutReadinessDispatchOrCanaryLeakage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 30, 9, 20, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "capsule-error-authority", 7)
	start := standardStart(t, now, authority, "capsule-error")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "capsule-error-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	canary := "postgres://platform-admin:raw-secret@private-db/platform"
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context, RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "capsule-error-binding", digest(85)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "capsule-error-input", digest(86)), nil
		}),
		ExecutionCapsuleResolver: ExecutionCapsuleResolverFunc(func(
			context.Context, ExecutionCapsuleResolutionRequest,
		) (ExecutionCapsuleResolution, error) {
			return ExecutionCapsuleResolution{}, errors.New(canary)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.Runtime.Execute(context.Background(), start)
	assertErrorCode(t, err, ErrorDependencyUnavailable)
	if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "private-db") {
		t.Fatalf("raw resolver error escaped private adapter: %v", err)
	}
	inspected, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	})
	if err != nil || inspected.Readiness.CapsuleReady || inspected.Capsule.State == CapsulePrepared {
		t.Fatalf("resolver failure prepared Capsule or readiness: %+v err=%v", inspected, err)
	}
	if _, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: ExecutionCapsuleID{value: "unprepared-capsule"}, Digest: digest(87),
	}); err == nil {
		t.Fatal("resolver failure produced a dispatchable Capsule")
	}
}

func TestPostgresCapsuleAuditAndDispatchCommitAtomicallyAndReplayAfterRestart(t *testing.T) {
	now := time.Date(2026, time.July, 30, 9, 30, 0, 0, time.UTC)
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, "postgres-capsule-view"), nil
		},
	}
	db, schema, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "runtime_capsule_test", now, func() time.Time { return now }, lifecycle, nil,
	)

	accepted, err := store.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !accepted.Snapshot.Readiness.CapsuleReady || accepted.Snapshot.Capsule.State != CapsulePrepared {
		t.Fatalf("PostgreSQL capsule facts = %+v readiness=%+v", accepted.Snapshot.Capsule, accepted.Snapshot.Readiness)
	}
	var capsules, audits, outbox, delivery int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_capsules),
		(SELECT count(*) FROM `+schema+`.runtime_execution_capsule_audit),
		(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_delivery)`).
		Scan(&capsules, &audits, &outbox, &delivery); err != nil {
		t.Fatal(err)
	}
	if capsules != 1 || audits != 1 || outbox != 1 || delivery != 1 {
		t.Fatalf("atomic capsule families: capsule=%d audit=%d outbox=%d delivery=%d",
			capsules, audits, outbox, delivery)
	}
	var originalPayload, originalPayloadDigest []byte
	if err := db.QueryRowContext(context.Background(), `SELECT payload, payload_digest FROM `+schema+
		`.runtime_execution_dispatch_outbox WHERE runtime_run_id=$1`, start.RuntimeRunID.String()).
		Scan(&originalPayload, &originalPayloadDigest); err != nil {
		t.Fatal(err)
	}
	assertPostgresMutationRejected(t, db, `UPDATE `+schema+`.runtime_execution_dispatch_outbox
		SET payload=$1 WHERE runtime_run_id=$2`, []byte(`{"tampered":true}`), start.RuntimeRunID.String())
	var retainedPayload, retainedPayloadDigest []byte
	if err := db.QueryRowContext(context.Background(), `SELECT payload, payload_digest FROM `+schema+
		`.runtime_execution_dispatch_outbox WHERE runtime_run_id=$1`, start.RuntimeRunID.String()).
		Scan(&retainedPayload, &retainedPayloadDigest); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retainedPayload, originalPayload) ||
		!bytes.Equal(retainedPayloadDigest, originalPayloadDigest) ||
		digestBytes(retainedPayload) != accepted.Snapshot.Capsule.Digest {
		t.Fatal("immutable dispatch trigger allowed payload or digest drift")
	}

	claimed, err := store.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: accepted.Snapshot.Capsule.CapsuleID,
		Digest: accepted.Snapshot.Capsule.Digest,
	})
	if err != nil || claimed.CapsuleDigest != accepted.Snapshot.Capsule.Digest {
		t.Fatalf("claim PostgreSQL dispatch: %+v err=%v", claimed, err)
	}
	ack, err := store.AcknowledgeDispatch(context.Background(), DispatchAcknowledgementRequest{
		OperationID: claimed.OperationID, RuntimeRunID: claimed.RuntimeRunID,
		CapsuleID: claimed.CapsuleID, CapsuleDigest: claimed.CapsuleDigest, AckDigest: digest(84),
	})
	if err != nil || ack.Disposition != DispatchAcknowledged {
		t.Fatalf("ack PostgreSQL dispatch: %+v err=%v", ack, err)
	}
	var persistedPayload []byte
	if err := db.QueryRowContext(context.Background(), `SELECT payload FROM `+schema+`.
		runtime_execution_dispatch_outbox WHERE operation_id=$1`, claimed.OperationID.String()).Scan(&persistedPayload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persistedPayload, claimed.Capsule) {
		t.Fatal("PostgreSQL acknowledgement rewrote immutable dispatch payload")
	}

	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if replayed.Snapshot.Capsule != accepted.Snapshot.Capsule {
		t.Fatalf("restart changed capsule: first=%+v replay=%+v", accepted.Snapshot.Capsule, replayed.Snapshot.Capsule)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_capsules),
		(SELECT count(*) FROM `+schema+`.runtime_execution_capsule_audit),
		(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_delivery)`).
		Scan(&capsules, &audits, &outbox, &delivery); err != nil {
		t.Fatal(err)
	}
	if capsules != 1 || audits != 1 || outbox != 1 || delivery != 1 {
		t.Fatalf("restart duplicated capsule families: capsule=%d audit=%d outbox=%d delivery=%d",
			capsules, audits, outbox, delivery)
	}
}

func TestPostgresCapsuleAuditUnknownFieldTamperFailsInspectClosedWithoutLeakage(t *testing.T) {
	now := time.Date(2026, time.July, 30, 9, 35, 0, 0, time.UTC)
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, "postgres-capsule-audit-tamper-view"), nil
		},
	}
	db, schema, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "cap_audit_tamper", now, func() time.Time { return now }, lifecycle, nil,
	)
	accepted, err := store.Execute(context.Background(), start)
	if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare Capsule audit tamper fixture: %+v err=%v", accepted, err)
	}
	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER reject_immutable_mutation ON `+schema+
		`.runtime_execution_capsule_audit`); err != nil {
		t.Fatal(err)
	}
	unknownField := "unknown_private_schema_canary"
	privateCanary := "postgres://platform-admin:raw-secret@private-db/runtime_execution_capsule_audit"
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_capsule_audit
		SET audit_state=audit_state || jsonb_build_object($1::text,$2::text)
		WHERE runtime_run_id=$3`, unknownField, privateCanary, start.RuntimeRunID.String()); err != nil {
		t.Fatal(err)
	}

	inspected, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	})
	assertErrorCode(t, err, ErrorIntegrityConflict)
	public := fmt.Sprintf("%+v %v", inspected, err)
	for _, forbidden := range []string{unknownField, privateCanary, schema, "runtime_execution_capsule_audit"} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("strict Capsule audit failure leaked private detail %q: snapshot=%+v err=%v", forbidden, inspected, err)
		}
	}
}

func TestPostgresClaimDispatchDeniesExpiredAndCancelledAuthorityWithoutPayload(t *testing.T) {
	now := time.Date(2026, time.July, 30, 9, 40, 0, 0, time.UTC)

	t.Run("expired lease and Runtime View", func(t *testing.T) {
		current := now
		lifecycle := RuntimeViewPrerequisiteAdapter{
			OpenRuntimeViewFunc: func(
				_ context.Context,
				request taskworkspace.OpenRuntimeViewRequest,
			) (taskworkspace.OpenRuntimeViewResult, error) {
				return acceptedRuntimeViewResult(request, "postgres-expired-dispatch-view"), nil
			},
		}
		_, _, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
			t, "cap_expired_claim", now, func() time.Time { return current }, lifecycle, nil,
		)
		accepted, err := store.Execute(context.Background(), start)
		if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
			t.Fatalf("prepare expiry fixture: %+v err=%v", accepted, err)
		}
		current = now.Add(91 * time.Second)
		assertDispatchAuthorizationDeniedWithoutPayload(t, store, start.RuntimeRunID, accepted.Snapshot.Capsule)
	})

	t.Run("cancel", func(t *testing.T) {
		lifecycle := RuntimeViewPrerequisiteAdapter{
			OpenRuntimeViewFunc: func(
				_ context.Context,
				request taskworkspace.OpenRuntimeViewRequest,
			) (taskworkspace.OpenRuntimeViewResult, error) {
				return acceptedRuntimeViewResult(request, "postgres-cancelled-dispatch-view"), nil
			},
			FenceRuntimeViewFunc: func(
				_ context.Context,
				request taskworkspace.FenceRuntimeViewRequest,
			) (taskworkspace.FenceRuntimeViewResult, error) {
				return acceptedFenceRuntimeViewResult(request), nil
			},
		}
		_, _, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
			t, "cap_cancel_claim", now, func() time.Time { return now }, lifecycle, nil,
		)
		accepted, err := store.Execute(context.Background(), start)
		if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
			t.Fatalf("prepare cancel fixture: %+v err=%v", accepted, err)
		}
		cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
			SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "postgres-cancel-dispatch-operation"),
			PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
			PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
			ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
			ExpectedOperationGeneration: accepted.Snapshot.Operation.Generation,
			ExpectedRuntimeFence:        accepted.Snapshot.RuntimeFence, Authority: start.Authority,
			Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := store.Execute(context.Background(), cancel)
		if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
			t.Fatalf("cancel prepared Capsule: %+v err=%v", cancelled, err)
		}
		assertDispatchAuthorizationDeniedWithoutPayload(t, store, start.RuntimeRunID, accepted.Snapshot.Capsule)
	})
}

func TestPostgresCapsuleFaultsRollbackAtomicallyOrReplayCommittedResponseLoss(t *testing.T) {
	tests := []struct {
		point         PersistenceFaultPoint
		wantCommitted bool
		wantError     ErrorCode
	}{
		{point: PersistenceFaultBeforeCapsuleAudit, wantError: ErrorDependencyUnavailable},
		{point: PersistenceFaultBeforeCapsuleOutbox, wantError: ErrorDependencyUnavailable},
		{point: PersistenceFaultBeforeCapsuleCommit, wantError: ErrorDependencyUnavailable},
		{point: PersistenceFaultAfterCapsuleCommit, wantCommitted: true, wantError: ErrorReconciliationRequired},
		{point: PersistenceFaultBeforeCapsuleResponse, wantCommitted: true, wantError: ErrorReconciliationRequired},
	}
	for _, test := range tests {
		t.Run(test.point.String(), func(t *testing.T) {
			now := time.Date(2026, time.July, 30, 9, 45, 0, 0, time.UTC)
			caseID := "cap_fault_" + strconv.Itoa(int(test.point))
			lifecycle := RuntimeViewPrerequisiteAdapter{
				OpenRuntimeViewFunc: func(
					_ context.Context,
					request taskworkspace.OpenRuntimeViewRequest,
				) (taskworkspace.OpenRuntimeViewResult, error) {
					return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID(caseID+"-view")), nil
				},
			}
			db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
				t, caseID, now, func() time.Time { return now }, lifecycle, nil,
			)
			faults := &PersistenceFaultController{}
			config.Faults = faults
			store, err := NewPostgresAuthority(db, config)
			if err != nil {
				t.Fatal(err)
			}
			if err := faults.FailNextAt(test.point); err != nil {
				t.Fatal(err)
			}
			_, err = store.Execute(context.Background(), start)
			assertErrorCode(t, err, test.wantError)

			var capsules, audits, outbox, delivery int
			var capsuleReady bool
			if err := db.QueryRowContext(context.Background(), `SELECT
				(SELECT count(*) FROM `+schema+`.runtime_execution_capsules),
				(SELECT count(*) FROM `+schema+`.runtime_execution_capsule_audit),
				(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_delivery),
				(aggregate_state->'readiness'->>'capsule_ready')::boolean
				FROM `+schema+`.runtime_execution_runtimes WHERE runtime_run_id=$1`, start.RuntimeRunID.String()).
				Scan(&capsules, &audits, &outbox, &delivery, &capsuleReady); err != nil {
				t.Fatal(err)
			}
			wantRows := 0
			if test.wantCommitted {
				wantRows = 1
			}
			if capsules != wantRows || audits != wantRows || outbox != wantRows || delivery != wantRows ||
				capsuleReady != test.wantCommitted {
				t.Fatalf("fault atomicity: capsule=%d audit=%d outbox=%d delivery=%d ready=%v want=%d",
					capsules, audits, outbox, delivery, capsuleReady, wantRows)
			}
			config.Faults = nil
			restarted, err := NewPostgresAuthority(db, config)
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := restarted.Execute(context.Background(), start)
			if err != nil || !replayed.Snapshot.Readiness.CapsuleReady ||
				replayed.Snapshot.Capsule.State != CapsulePrepared {
				t.Fatalf("fault replay did not converge: %+v err=%v", replayed, err)
			}
		})
	}
}

func TestPostgresCapsuleRevalidatesFreshTimeAfterResolverReturns(t *testing.T) {
	now := time.Date(2026, time.July, 30, 10, 10, 0, 0, time.UTC)
	current := now
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, "capsule-resolver-expiry-view"), nil
		},
	}
	db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "cap_resolver_expiry", now, func() time.Time { return current }, lifecycle, nil,
	)
	resolver := deterministicCapsuleResolver{}
	config.ExecutionCapsuleResolver = ExecutionCapsuleResolverFunc(func(
		ctx context.Context,
		request ExecutionCapsuleResolutionRequest,
	) (ExecutionCapsuleResolution, error) {
		resolution, err := resolver.ResolveExecutionCapsule(ctx, request)
		current = now.Add(91 * time.Second)
		return resolution, err
	})
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}

	decision, err := store.Execute(context.Background(), start)
	assertErrorCode(t, err, ErrorIntegrityConflict)
	if decision.Snapshot.Capsule.State == CapsulePrepared || decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("expired resolver result prepared Capsule: %+v", decision.Snapshot)
	}
	var capsules, audits, outbox, delivery int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_capsules),
		(SELECT count(*) FROM `+schema+`.runtime_execution_capsule_audit),
		(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_delivery)`).
		Scan(&capsules, &audits, &outbox, &delivery); err != nil {
		t.Fatal(err)
	}
	if capsules != 0 || audits != 0 || outbox != 0 || delivery != 0 {
		t.Fatalf("stale resolver result persisted Capsule family: capsule=%d audit=%d outbox=%d delivery=%d",
			capsules, audits, outbox, delivery)
	}
}

func TestPostgresConcurrentCapsuleGenerationReplaysExactContentAndRejectsIdentityRebinding(t *testing.T) {
	for _, different := range []bool{false, true} {
		name := "exact_content"
		if different {
			name = "different_content"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, time.July, 30, 10, 15, 0, 0, time.UTC)
			caseID := "cap_conc_exact"
			if different {
				caseID = "cap_conc_diff"
			}
			lifecycle := RuntimeViewPrerequisiteAdapter{
				OpenRuntimeViewFunc: func(
					_ context.Context,
					request taskworkspace.OpenRuntimeViewRequest,
				) (taskworkspace.OpenRuntimeViewResult, error) {
					return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID(caseID+"-view")), nil
				},
			}
			db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
				t, caseID, now, func() time.Time { return now }, lifecycle, nil,
			)
			prepareConfig := config
			prepareConfig.ExecutionCapsuleResolver = nil
			preparer, err := NewPostgresAuthority(db, prepareConfig)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := preparer.Execute(context.Background(), start)
			if err != nil || prepared.Snapshot.Capsule.State == CapsulePrepared ||
				prepared.Snapshot.Readiness.RuntimeView.State != PrerequisiteAccepted ||
				prepared.Snapshot.Readiness.ImmutableInputs.State != PrerequisiteAccepted {
				t.Fatalf("prepare concurrent prerequisites: %+v err=%v", prepared, err)
			}
			entered := make(chan uint64, 2)
			firstRelease := make(chan struct{})
			secondRelease := make(chan struct{})
			firstReleased, secondReleased := false, false
			t.Cleanup(func() {
				if !firstReleased {
					close(firstRelease)
				}
				if !secondReleased {
					close(secondRelease)
				}
			})
			var calls atomic.Uint64
			config.ExecutionCapsuleResolver = ExecutionCapsuleResolverFunc(func(
				ctx context.Context,
				request ExecutionCapsuleResolutionRequest,
			) (ExecutionCapsuleResolution, error) {
				call := calls.Add(1)
				resolution, err := (deterministicCapsuleResolver{}).ResolveExecutionCapsule(ctx, request)
				if different && call == 2 {
					resolution.EvidenceDigest = digest(88)
				}
				entered <- call
				if call == 1 {
					<-firstRelease
				} else {
					<-secondRelease
				}
				return resolution, err
			})
			first, err := NewPostgresAuthority(db, config)
			if err != nil {
				t.Fatal(err)
			}
			second, err := NewPostgresAuthority(db, config)
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				decision RuntimeDecision
				err      error
			}
			results := make(chan result, 2)
			go func() {
				decision, executeErr := first.Execute(context.Background(), start)
				results <- result{decision: decision, err: executeErr}
			}()
			go func() {
				decision, executeErr := second.Execute(context.Background(), start)
				results <- result{decision: decision, err: executeErr}
			}()
			for entry := 0; entry < 2; entry++ {
				select {
				case <-entered:
				case early := <-results:
					t.Fatalf("concurrent caller returned before both resolvers entered: decision=%+v err=%v", early.decision, early.err)
				case <-time.After(5 * time.Second):
					t.Fatal("concurrent caller did not enter Capsule resolver")
				}
			}
			close(firstRelease)
			firstReleased = true
			var firstResult result
			select {
			case firstResult = <-results:
			case <-time.After(5 * time.Second):
				t.Fatal("first concurrent Capsule generation did not return")
			}
			close(secondRelease)
			secondReleased = true
			var secondResult result
			select {
			case secondResult = <-results:
			case <-time.After(5 * time.Second):
				t.Fatal("second concurrent Capsule generation did not return")
			}

			resultValues := []result{firstResult, secondResult}
			successes := make([]RuntimeCapsuleSnapshot, 0, 2)
			integrityFailures := 0
			for _, value := range resultValues {
				if value.err == nil {
					successes = append(successes, value.decision.Snapshot.Capsule)
					continue
				}
				failure, ok := value.err.(*Error)
				if ok && failure.Code() == ErrorIntegrityConflict {
					integrityFailures++
					continue
				}
				t.Fatalf("unexpected concurrent generation error: %T %v", value.err, value.err)
			}
			if different {
				if len(successes) != 1 || integrityFailures != 1 {
					t.Fatalf("different content did not fail closed: successes=%d integrity=%d", len(successes), integrityFailures)
				}
			} else if len(successes) != 2 || integrityFailures != 0 || successes[0] != successes[1] {
				t.Fatalf("exact concurrent generation did not replay: successes=%+v integrity=%d", successes, integrityFailures)
			}
			var capsules, audits, outbox, delivery int
			if err := db.QueryRowContext(context.Background(), `SELECT
				(SELECT count(*) FROM `+schema+`.runtime_execution_capsules),
				(SELECT count(*) FROM `+schema+`.runtime_execution_capsule_audit),
				(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_dispatch_delivery)`).
				Scan(&capsules, &audits, &outbox, &delivery); err != nil {
				t.Fatal(err)
			}
			if capsules != 1 || audits != 1 || outbox != 1 || delivery != 1 {
				t.Fatalf("concurrent generation duplicated families: %d %d %d %d", capsules, audits, outbox, delivery)
			}
		})
	}
}

func newReadyReadOnlyCapsuleHarness(
	t *testing.T,
	now time.Time,
	suffix string,
) (*DeterministicHarness, StartRuntimeRun, RuntimeDecision) {
	t.Helper()
	authority := mustTaskOrchestrationAuthority(t, suffix+"-authority", 7)
	start := standardStart(t, now, authority, suffix)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = startNodeID(t, suffix+"-node"), 1
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant},
		Nodes:           []ExecutionNodeFixture{executionNodeFixtureForStart(t, start, grant, now)},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, suffix+"-input-evidence", digest(225)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare read-only Capsule: %+v err=%v", accepted, err)
	}
	return harness, start, accepted
}

func newReadyProviderCapsuleHarness(
	t *testing.T,
	now time.Time,
	suffix string,
) (*DeterministicHarness, StartRuntimeRun, QuotaReservationFixture, RuntimeDecision) {
	t.Helper()
	authority := mustTaskOrchestrationAuthority(t, suffix+"-authority", 7)
	start, reservation, admission, node := providerGatewayFixture(t, now, authority, suffix)
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, suffix)
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
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{admission}, QuotaReservations: []QuotaReservationFixture{reservation},
		Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator, ImmutableInputValidator: inputValidator,
		GatewayGrants: gateway, GatewayRecovery: writableGatewayRecoveryAuthority(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare provider-capable Capsule: %+v err=%v", accepted, err)
	}
	return harness, start, reservation, accepted
}

func newPostgresProviderCapsuleRuntime(
	t *testing.T,
	now time.Time,
	schemaPrefix string,
	prepareCapsule bool,
) (*sql.DB, string, *PostgresAuthority, StartRuntimeRun, RuntimeDecision) {
	t.Helper()
	db, schema := testpostgres.Open(t, schemaPrefix)
	authority := mustTaskOrchestrationAuthority(t, schemaPrefix+"-authority", 9)
	start, reservation, _, node := providerGatewayFixture(t, now, authority, schemaPrefix)
	leaseOperationID, leaseDigest := stableLeaseAcquireBinding(start)
	lease := RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, AcquireOperationID: leaseOperationID, AcquireDigest: leaseDigest,
		LeaseID: SandboxLeaseID{value: schemaPrefix + "-lease"}, Generation: 1, Fence: 1,
		Disposition: LeaseActive, ExpiresAt: now.Add(90 * time.Second),
		SandboxID: SandboxID{value: schemaPrefix + "-sandbox"}, SandboxGeneration: 1, SandboxFence: 1,
		WorkerAuthorityID: node.WorkerAuthorityID, WorkerGeneration: node.WorkerGeneration,
		NodeAuthorityID:         node.NodeAuthorityID,
		AuthorizationGeneration: node.AuthorizationGeneration, AuthorizationExpiresAt: node.AuthorizationExpiresAt,
	}
	readiness := initialRuntimeReadiness(start)
	readiness.Lease = leasePrerequisiteFact(lease)
	fixture := acceptedPostgresRuntimeFixture(start, authority, now)
	fixture.RuntimeRevision++
	fixture.State = RuntimePreparingPrerequisites
	fixture.Operation.WorkItemID = start.AdmissionGrant.WorkItemID
	fixture.Operation.ExecutionNodeID = node.ExecutionNodeID
	fixture.Operation.NodeCapacityGeneration = uint64(node.Generation)
	fixture.Operation.ResourceClassID = start.ResourceClassID
	fixture.Operation.ExecutionPolicyID = start.ExecutionPolicyID
	fixture.Operation.SchedulerEpoch = 1
	fixture.Operation.PolicyVersion = 1
	fixture.Lease = lease
	fixture.Node = RuntimeNodeSnapshot{
		ExecutionNodeID: node.ExecutionNodeID, Generation: node.Generation, Readiness: node.Readiness,
		AttestationID: node.AttestationID, AttestationGeneration: node.AttestationGeneration,
		AttestedAt: node.AttestedAt, ExpiresAt: node.ExpiresAt, Occupancy: NodeOccupied,
		Containment: ContainmentPending, Reset: ResetRequired,
	}
	fixture.Capacity.Physical = PhysicalCapacityOccupied
	fixture.Readiness = readiness
	fixture.Gateway = GatewayPrerequisiteSnapshot{
		Applicability: GatewayPrerequisiteRequired, Status: GatewayGrantWaitingForLease,
	}
	fixture.Usage = RuntimeUsageEvidenceSnapshot{Disposition: UsageEvidenceMissing}

	quotaTable := schema + ".capsule_quota_reservations"
	quotaFunction := schema + ".capsule_validate_quota_reservation"
	participant := QuotaReservationParticipantFunc(func(
		ctx context.Context,
		transaction QuotaReservationValidationTransaction,
		_ QuotaReservationValidationFact,
	) (QuotaReservationValidationResult, error) {
		return transaction.ValidateQuotaReservation(ctx)
	})
	var store *PostgresAuthority
	gateway, err := NewDeterministicGateway(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	config := PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, ExecutionCapsuleResolver: deterministicCapsuleResolver{},
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, schemaPrefix+"-input-evidence", digest(226)), nil
		}),
		QuotaReservationParticipant: participant, QuotaReservationFunction: quotaFunction,
		GatewayGrants: gateway, GatewayRecovery: writableGatewayRecoveryAuthority(now),
	}
	store, err = NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), fmt.Sprintf(`CREATE TABLE %s (
		quota_reservation_id text PRIMARY KEY, generation bigint NOT NULL, mode smallint NOT NULL,
		state smallint NOT NULL, personal_workspace_id text NOT NULL, task_id text NOT NULL,
		phase_run_id text NOT NULL, authorization_generation bigint NOT NULL,
		capability smallint NOT NULL, gateway_route_policy_id text NOT NULL,
		gateway_route_policy_generation bigint NOT NULL, capability_scope bigint NOT NULL,
		valid_from timestamptz NOT NULL, expires_at timestamptz NOT NULL
	)`, quotaTable)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), fmt.Sprintf(`CREATE FUNCTION %s(
		p_id text, p_generation bigint, p_mode smallint, p_workspace text, p_task text,
		p_phase text, p_authorization_generation bigint, p_capability smallint,
		p_route_policy_id text, p_route_policy_generation bigint,
		p_capability_scope bigint, p_valid_at timestamptz
	) RETURNS timestamptz LANGUAGE plpgsql AS $quota$
	DECLARE retained %s%%ROWTYPE;
	BEGIN
		SELECT * INTO retained FROM %s WHERE quota_reservation_id=p_id FOR SHARE;
		IF retained.quota_reservation_id IS NULL OR retained.generation <> p_generation OR
			retained.mode <> p_mode OR retained.state <> 1 OR retained.personal_workspace_id <> p_workspace OR
			retained.task_id <> p_task OR retained.phase_run_id <> p_phase OR
			retained.authorization_generation <> p_authorization_generation OR retained.capability <> p_capability OR
			retained.gateway_route_policy_id <> p_route_policy_id OR
			retained.gateway_route_policy_generation <> p_route_policy_generation OR
			retained.capability_scope <> p_capability_scope OR retained.valid_from > p_valid_at OR
			retained.expires_at <= p_valid_at THEN
			RAISE EXCEPTION 'quota reservation binding conflict' USING ERRCODE = '23000';
		END IF;
		RETURN retained.expires_at;
	END $quota$`, quotaFunction, quotaTable, quotaTable)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO `+quotaTable+`
		VALUES ($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		reservation.QuotaReservationID.String(), reservation.Generation, reservation.Mode,
		reservation.PersonalWorkspaceID.String(), reservation.TaskID.String(), reservation.PhaseRunID.String(),
		reservation.AuthorizationGeneration, reservation.Capability, reservation.GatewayRoutePolicyID.String(),
		reservation.GatewayRoutePolicyGeneration, reservation.CapabilityScope,
		reservation.ValidFrom, reservation.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-"+schemaPrefix)
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)
	installPostgresActiveLeaseForPrerequisiteTest(t, db, schema, fixture, now)
	persistPostgresAcceptedRuntimeBindingFact(t, store, start)
	if !prepareCapsule {
		return db, quotaTable, store, start, RuntimeDecision{}
	}
	accepted, err := store.Execute(context.Background(), start)
	if err != nil || !accepted.Snapshot.Readiness.CapsuleReady || !accepted.Snapshot.Gateway.Ready {
		t.Fatalf("prepare PostgreSQL provider Capsule: %+v err=%v", accepted, err)
	}
	return db, quotaTable, store, start, accepted
}

func newPostgresReadyProviderCapsuleRuntime(
	t *testing.T,
	now time.Time,
	schemaPrefix string,
) (*sql.DB, string, *PostgresAuthority, StartRuntimeRun, RuntimeDecision) {
	t.Helper()
	return newPostgresProviderCapsuleRuntime(t, now, schemaPrefix, true)
}

func assertDispatchAuthorizationDeniedWithoutPayload(
	t *testing.T,
	dispatch OwnedDispatch,
	runtimeRunID RuntimeRunID,
	capsule RuntimeCapsuleSnapshot,
) {
	t.Helper()
	delivery, err := dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: runtimeRunID, CapsuleID: capsule.CapsuleID, Digest: capsule.Digest,
	})
	assertErrorCode(t, err, ErrorAuthorizationDenied)
	if !emptyDispatchDelivery(delivery) {
		t.Fatalf("stale or unauthorized dispatch returned Capsule facts: %+v", delivery)
	}
}

func emptyDispatchDelivery(delivery DispatchDelivery) bool {
	return delivery.OperationID == (OperationID{}) && delivery.RuntimeRunID == (RuntimeRunID{}) &&
		delivery.CapsuleID == (ExecutionCapsuleID{}) && delivery.CapsuleDigest == (Digest{}) &&
		len(delivery.Capsule) == 0 && delivery.Disposition == 0 && delivery.DeliveryCount == 0
}
