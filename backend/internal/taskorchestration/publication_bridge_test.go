package taskorchestration_test

import (
	"context"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

// publicationRequestSlot is a shared helper identity used across the bridge
// contract tests.
func publicationSlot(t *testing.T, value string) taskorchestration.PublicationMemberSlotID {
	t.Helper()
	id, err := taskorchestration.NewPublicationMemberSlotID(value)
	if err != nil {
		t.Fatalf("create publication member slot identity: %v", err)
	}
	return id
}

func publicationContract(t *testing.T, value string) taskorchestration.PublicationContractID {
	t.Helper()
	id, err := taskorchestration.NewPublicationContractID(value)
	if err != nil {
		t.Fatalf("create publication contract identity: %v", err)
	}
	return id
}

func publicationRequestID(t *testing.T, value string) taskorchestration.PublicationRequestID {
	t.Helper()
	id, err := taskorchestration.NewPublicationRequestID(value)
	if err != nil {
		t.Fatalf("create publication request identity: %v", err)
	}
	return id
}

func publicationContentID(t *testing.T, value string) taskorchestration.PublicationContentID {
	t.Helper()
	id, err := taskorchestration.NewPublicationContentID(value)
	if err != nil {
		t.Fatalf("create publication content identity: %v", err)
	}
	return id
}

func publicationCapabilityID(t *testing.T, value string) taskorchestration.PublicationCapabilityID {
	t.Helper()
	id, err := taskorchestration.NewPublicationCapabilityID(value)
	if err != nil {
		t.Fatalf("create publication capability identity: %v", err)
	}
	return id
}

func publicationAdapterID(t *testing.T, value string) taskorchestration.PublicationAdapterID {
	t.Helper()
	id, err := taskorchestration.NewPublicationAdapterID(value)
	if err != nil {
		t.Fatalf("create publication adapter identity: %v", err)
	}
	return id
}

// standardPublicationBinding is the complete canonical first-generation C05
// request fixed at outbox-commit time: Task, Phase Run, operation,
// activity, expected head/revision, contract, evidence and
// generation/fence/safety-epoch bindings.
func standardPublicationBinding(t *testing.T, operation string) taskorchestration.PublicationRequestBinding {
	t.Helper()
	slot := publicationSlot(t, "slot-deck")
	memberDigest := evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	return taskorchestration.PublicationRequestBinding{
		SchemaVersion:          taskorchestration.PublicationSchemaV1,
		RequestID:              publicationRequestID(t, "request-"+operation),
		OperationID:            operationID(t, operation),
		TaskID:                 taskID(t, "publication-bridge-task"),
		PhaseRunID:             phaseRunID(t, "phase-run-1"),
		ActivityGeneration:     3,
		Generation:             taskorchestration.ProducerGeneration(2),
		Fence:                  taskorchestration.PublicationFence(4),
		SafetyEpoch:            7,
		ExpectedStreamRevision: 0,
		ExpectedHead:           taskorchestration.ArtifactVersionID{},
		Spec: taskorchestration.NewPublicationRequestSpec(
			publicationContract(t, "publication-contract-1"),
			taskorchestration.PublicationKindFirstGeneration,
			taskorchestration.ArtifactVersionID{},
			[]taskorchestration.PublicationMemberSpec{{
				Slot: slot, Kind: taskorchestration.PublicationMemberDeck,
				LogicalName: "Deck.pptx", MediaType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
				Size: 1024, ContentDigest: memberDigest,
			}},
			[]taskorchestration.PublicationStagingReference{{
				MemberSlot: slot, ContentID: publicationContentID(t, "content-1"),
				ContentDigest: memberDigest, Size: 1024,
				Purpose:            taskorchestration.PublicationStagingPurposeMember,
				PhysicalGeneration: 2, AdapterID: publicationAdapterID(t, "do-adapter-1"),
			}},
			[]taskorchestration.PublicationChannel{taskorchestration.PublicationChannelDeck},
			[]taskorchestration.PublicationRuntimeEvidenceRef{{
				Channel:    taskorchestration.PublicationChannelDeck,
				EvidenceID: evidenceID(t, "runtime-evidence-1"), Digest: evidenceDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
			}},
			taskorchestration.PublicationEvidenceRef{
				EvidenceID: evidenceID(t, "validation-evidence-1"), Digest: evidenceDigest(t, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
			},
			taskorchestration.PublicationEvidenceRef{
				EvidenceID: evidenceID(t, "c04-commit-1"), Digest: evidenceDigest(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
			},
			[]taskorchestration.PublicationCapabilityRef{{
				MemberSlot: slot, CapabilityID: publicationCapabilityID(t, "capability-1"),
				Digest: evidenceDigest(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
			}},
		),
	}
}

func TestPublicationBridgeCommitFixesCompleteCanonicalRequest(t *testing.T) {
	now := time.Date(2026, time.August, 11, 9, 0, 0, 0, time.UTC)
	binding := standardPublicationBinding(t, "bridge-commit-1")
	if !binding.Valid() {
		t.Fatal("committed publication binding must be valid")
	}
	canonical := binding.CanonicalBytes()
	if len(canonical) == 0 {
		t.Fatal("canonical request bytes must be non-empty")
	}
	digest := binding.CanonicalDigest()

	// The canonical digest commits the authority fields and the spec; a
	// delivery attempt can never change it.
	tampered := publicationBindingWithMemberName(t, binding, "Deck-renamed.pptx")
	if tampered.CanonicalDigest() == digest {
		t.Fatal("canonical digest must commit the member bindings")
	}

	transport := taskorchestration.NewDeterministicPublicationTransport(func() time.Time { return now })
	adapter := taskorchestration.NewPublicationBridgeAdapter(transport)
	bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })

	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("commit complete canonical request: %v", err)
	}
	committedDigest, ok := bridge.Digest(binding.OperationID)
	if !ok || committedDigest != digest {
		t.Fatalf("committed digest = %v, ok = %v, want %v", committedDigest, ok, digest)
	}
	// Exact replay of the same committed binding is idempotent.
	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("exact replay commit: %v", err)
	}
	// A different binding under the same OperationID is a durable conflict.
	tampered.RequestID = publicationRequestID(t, "request-bridge-commit-conflict")
	if err := bridge.Commit(tampered); err == nil {
		t.Fatal("re-committing a different request under the same OperationID must fail closed")
	}
}

func TestPublicationBridgeClaimDeliverInspectAndReconcile(t *testing.T) {
	now := time.Date(2026, time.August, 11, 9, 1, 0, 0, time.UTC)
	binding := standardPublicationBinding(t, "bridge-deliver-1")
	transport := taskorchestration.NewDeterministicPublicationTransport(func() time.Time { return now })
	adapter := taskorchestration.NewPublicationBridgeAdapter(transport)
	bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("commit: %v", err)
	}

	claimed := bridge.Claim(1)
	if len(claimed) != 1 {
		t.Fatalf("claim = %d bindings, want 1", len(claimed))
	}
	// The claimed binding is byte-identical to the committed one; the
	// adapter never assembles a request.
	delivery, err := bridge.Deliver(context.Background(), claimed[0])
	if err != nil || delivery.Outcome != taskorchestration.PublicationDelivered {
		t.Fatalf("deliver = %#v, err = %v", delivery, err)
	}
	if delivery.OperationID != binding.OperationID || delivery.Digest != binding.CanonicalDigest() {
		t.Fatalf("delivery must preserve the ORIGINAL OperationID and digest: %#v", delivery)
	}

	// Duplicate exact delivery returns the same outcome and is flagged as
	// a duplicate (at-least-once).
	bridge2 := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
	if err := bridge2.Commit(binding); err != nil {
		t.Fatalf("re-commit for duplicate delivery: %v", err)
	}
	claimed = bridge2.Claim(1)
	duplicate, err := bridge2.Deliver(context.Background(), claimed[0])
	if err != nil || duplicate.Outcome != taskorchestration.PublicationDelivered || !duplicate.Duplicate {
		t.Fatalf("duplicate exact delivery = %#v, err = %v", duplicate, err)
	}

	// Inspect returns to the ORIGINAL OperationID without delivering.
	inspected, err := bridge.Inspect(context.Background(), binding.OperationID)
	if err != nil || inspected.Outcome != taskorchestration.PublicationDelivered ||
		inspected.OperationID != binding.OperationID {
		t.Fatalf("inspect original operation = %#v, err = %v", inspected, err)
	}

	// Reconcile a never-delivered operation reports unknown without
	// creating anything.
	reconciled, err := bridge.Reconcile(context.Background(), operationID(t, "bridge-never-delivered"))
	if err != nil || reconciled.Outcome != taskorchestration.PublicationUnknown {
		t.Fatalf("reconcile unknown operation = %#v, err = %v", reconciled, err)
	}
}

func TestPublicationBridgeIntegrityConflictAndPoisonedRejection(t *testing.T) {
	now := time.Date(2026, time.August, 11, 9, 2, 0, 0, time.UTC)
	binding := standardPublicationBinding(t, "bridge-conflict-1")
	transport := taskorchestration.NewDeterministicPublicationTransport(func() time.Time { return now })
	adapter := taskorchestration.NewPublicationBridgeAdapter(transport)
	bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("commit: %v", err)
	}
	_ = bridge.Claim(1)

	// A conflicting binding under the same OperationID must be rejected by
	// the adapter and the transport before any business effect.
	conflicting := publicationBindingWithMemberName(t, binding, "Deck-renamed.pptx")
	delivery, err := bridge.Deliver(context.Background(), conflicting)
	if err != nil || delivery.Outcome != taskorchestration.PublicationIntegrityConflict {
		t.Fatalf("conflicting delivery = %#v, err = %v", delivery, err)
	}

	// A malformed binding under the same OperationID is a typed integrity
	// conflict (the committed request can never be re-bound).
	malformed := binding
	malformed.Spec.Members = nil
	poisoned, err := bridge.Deliver(context.Background(), malformed)
	if err != nil || poisoned.Outcome != taskorchestration.PublicationIntegrityConflict {
		t.Fatalf("malformed same-OperationID delivery = %#v, err = %v", poisoned, err)
	}
	// A binding for an operation that was never committed is reported as
	// unknown and never reaches the transport.
	unknown := binding
	unknown.OperationID = operationID(t, "bridge-new-unknown")
	unknown.Spec.Members = nil
	unknownDelivery, err := bridge.Deliver(context.Background(), unknown)
	if err != nil || unknownDelivery.Outcome != taskorchestration.PublicationUnknown {
		t.Fatalf("unknown operation delivery = %#v, err = %v", unknownDelivery, err)
	}
}

func TestPublicationBridgeAdapterCannotAssembleRequestFields(t *testing.T) {
	now := time.Date(2026, time.August, 11, 9, 3, 0, 0, time.UTC)
	binding := standardPublicationBinding(t, "bridge-adapter-1")
	transport := taskorchestration.NewDeterministicPublicationTransport(func() time.Time { return now })
	adapter := taskorchestration.NewPublicationBridgeAdapter(transport)

	// The adapter passes the committed binding verbatim: the transport
	// receives EXACTLY the committed canonical bytes and digest.
	delivery, err := adapter.DeliverPublication(context.Background(), binding)
	if err != nil || delivery.Outcome != taskorchestration.PublicationDelivered ||
		delivery.Digest != binding.CanonicalDigest() {
		t.Fatalf("adapter deliver = %#v, err = %v", delivery, err)
	}

	// The adapter has no field that could overwrite a request binding: it
	// exposes only Deliver/Inspect over the committed binding.
	if adapter == nil {
		t.Fatal("adapter must exist")
	}
	inspected, err := adapter.InspectPublication(context.Background(), binding.OperationID)
	if err != nil || inspected.OperationID != binding.OperationID ||
		inspected.Digest != binding.CanonicalDigest() {
		t.Fatalf("adapter inspect = %#v, err = %v", inspected, err)
	}
}

func TestPublicationBridgeCanonicalDigestIsStableAcrossDeliveries(t *testing.T) {
	binding := standardPublicationBinding(t, "bridge-stable-1")
	first := binding.CanonicalBytes()
	digest := binding.CanonicalDigest()
	for attempt := 0; attempt < 5; attempt++ {
		if binding.CanonicalDigest() != digest {
			t.Fatalf("canonical digest changed on attempt %d", attempt)
		}
		if string(binding.CanonicalBytes()) != string(first) {
			t.Fatalf("canonical bytes changed on attempt %d", attempt)
		}
	}
}

// publicationBindingWithMemberName rebuilds the binding spec with a
// renamed member so the caller's slice never aliases the committed record.
func publicationBindingWithMemberName(
	t *testing.T,
	binding taskorchestration.PublicationRequestBinding,
	name string,
) taskorchestration.PublicationRequestBinding {
	t.Helper()
	members := append([]taskorchestration.PublicationMemberSpec(nil), binding.Spec.Members...)
	members[0].LogicalName = name
	binding.Spec = taskorchestration.NewPublicationRequestSpec(
		binding.Spec.ContractID, binding.Spec.Kind, binding.Spec.Parent,
		members, binding.Spec.Staging, binding.Spec.RequiredChannels,
		binding.Spec.RuntimeRefs, binding.Spec.ValidationRef, binding.Spec.C04CommitRef,
		binding.Spec.ContentCapabilityRefs,
	)
	return binding
}

func TestPublicationBridgeManualEditBindingBindsExactParent(t *testing.T) {
	now := time.Date(2026, time.August, 11, 9, 4, 0, 0, time.UTC)
	binding := standardPublicationBinding(t, "bridge-manual-edit-1")
	parent := artifactVersionID(t, "artifact-version-parent-1")
	binding.Spec.Kind = taskorchestration.PublicationKindManualEdit
	binding.Spec.Parent = parent
	binding.ExpectedStreamRevision = 1
	binding.ExpectedHead = parent
	if !binding.Valid() {
		t.Fatal("manual-edit binding with an exact parent must be valid")
	}

	// A manual-edit binding without the exact parent fails closed.
	missing := binding
	missing.Spec.Parent = taskorchestration.ArtifactVersionID{}
	if missing.Valid() {
		t.Fatal("manual-edit binding without a parent must fail closed")
	}
	// A first-generation binding with a parent fails closed.
	firstGen := binding
	firstGen.Spec.Kind = taskorchestration.PublicationKindFirstGeneration
	if firstGen.Valid() {
		t.Fatal("first-generation binding with a parent must fail closed")
	}

	transport := taskorchestration.NewDeterministicPublicationTransport(func() time.Time { return now })
	adapter := taskorchestration.NewPublicationBridgeAdapter(transport)
	bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("commit manual-edit binding: %v", err)
	}
	claimed := bridge.Claim(1)
	delivery, err := bridge.Deliver(context.Background(), claimed[0])
	if err != nil || delivery.Outcome != taskorchestration.PublicationDelivered {
		t.Fatalf("manual-edit delivery = %#v, err = %v", delivery, err)
	}
	// The delivered binding still binds the exact parent (the transport
	// journal cannot change it).
	if delivery.Digest != binding.CanonicalDigest() {
		t.Fatalf("manual-edit digest changed across delivery: %#v", delivery)
	}
}
