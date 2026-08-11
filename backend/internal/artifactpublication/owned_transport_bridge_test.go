package artifactpublication

// Cross-module publication bridge integration (child SPEC #109 / C05-06).
// Task Orchestration and Artifact Publication are deliberately isolated deep
// modules (their packages do not import each other); this test file is the
// ONLY place the two meet. It wires the Task Orchestration
// PublicationTransportPort to the C05 owned transport client and proves the
// full chain: the complete canonical C05 request is fixed at outbox-commit
// time (OperationID + payload digest equal the C05 request's exact
// identity/digest), the owned adapter delivers the committed binding without
// re-assembling it, ambiguous delivery returns to the ORIGINAL OperationID,
// and C05 activation evidence binds the exact OperationID, Phase Run
// generation/fence, activity generation, safety epoch, ArtifactVersionID and
// manifest digest. The same contract runs against real PostgreSQL in
// owned_transport_bridge_postgres_test.go.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

// ownedTransportBridgePort is the wiring adapter: it converts the committed
// canonical binding into the typed C05 PreparePublication intent and maps
// the C05 safe-error surface back to the closed bridge outcomes. It never
// assembles request fields — it only mirrors the committed binding.
type ownedTransportBridgePort struct {
	f      *fixture
	client *OwnedTransportClient
}

func (p *ownedTransportBridgePort) DeliverPublication(
	ctx context.Context,
	binding taskorchestration.PublicationRequestBinding,
) (taskorchestration.PublicationDelivery, error) {
	intent, err := publicationPrepareFromBinding(p.f, binding)
	if err != nil {
		return taskorchestration.PublicationDelivery{
			OperationID: binding.OperationID, Outcome: taskorchestration.PublicationPoisoned,
		}, nil
	}
	decision, mutateErr := p.client.Mutate(ctx, intent)
	if mutateErr != nil {
		var publicationError *Error
		if errors.As(mutateErr, &publicationError) {
			switch publicationError.Code {
			case ErrorReconciliationRequired:
				return taskorchestration.PublicationDelivery{
					OperationID: binding.OperationID,
					Outcome:     taskorchestration.PublicationReconciliationRequired,
					Digest:      binding.CanonicalDigest(),
				}, nil
			case ErrorIntegrityConflict:
				return taskorchestration.PublicationDelivery{
					OperationID: binding.OperationID,
					Outcome:     taskorchestration.PublicationIntegrityConflict,
					Digest:      binding.CanonicalDigest(),
				}, nil
			case ErrorUnsupportedSchema:
				return taskorchestration.PublicationDelivery{
					OperationID: binding.OperationID,
					Outcome:     taskorchestration.PublicationUnsupported,
				}, nil
			default:
				return taskorchestration.PublicationDelivery{
					OperationID: binding.OperationID,
					Outcome:     taskorchestration.PublicationPoisoned,
				}, nil
			}
		}
		return taskorchestration.PublicationDelivery{
			OperationID: binding.OperationID, Outcome: taskorchestration.PublicationPoisoned,
		}, nil
	}
	if decision.Operation.ID != PublicationOperationID(binding.OperationID.String()) {
		return taskorchestration.PublicationDelivery{
			OperationID: binding.OperationID, Outcome: taskorchestration.PublicationPoisoned,
		}, nil
	}
	return taskorchestration.PublicationDelivery{
		OperationID: binding.OperationID, Outcome: taskorchestration.PublicationDelivered,
		Digest: binding.CanonicalDigest(), OccurredAt: time.Unix(int64(p.f.now), 0).UTC(),
	}, nil
}

func (p *ownedTransportBridgePort) InspectPublication(
	ctx context.Context,
	operationID taskorchestration.OperationID,
) (taskorchestration.PublicationDelivery, error) {
	view, err := p.client.Query(ctx, PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: p.f.policyDomain, TaskID: p.f.taskID,
		OperationID: PublicationOperationID(operationID.String()),
	})
	if err != nil {
		return taskorchestration.PublicationDelivery{
			OperationID: operationID, Outcome: taskorchestration.PublicationPoisoned,
		}, nil
	}
	if view.State == "" {
		return taskorchestration.PublicationDelivery{
			OperationID: operationID, Outcome: taskorchestration.PublicationUnknown,
		}, nil
	}
	return taskorchestration.PublicationDelivery{
		OperationID: operationID, Outcome: taskorchestration.PublicationDelivered,
		OccurredAt: time.Unix(int64(p.f.now), 0).UTC(),
	}, nil
}

// publicationPrepareFromBinding converts the committed canonical binding
// into the typed C05 PreparePublication intent. It is the only conversion;
// the adapter can never assemble a request at delivery time.
func publicationPrepareFromBinding(
	f *fixture,
	binding taskorchestration.PublicationRequestBinding,
) (PublicationIntent, error) {
	header := f.header(binding.OperationID.String())
	header.RequestID = PublicationRequestID(binding.RequestID.String())
	header.ExpectedStreamRevision = StreamRevision(binding.ExpectedStreamRevision)
	header.ExpectedHead = ArtifactVersionID(binding.ExpectedHead.String())
	header.ActivityGeneration = Generation(binding.ActivityGeneration)
	header.Generation = Generation(binding.Generation)
	header.Fence = Fence(binding.Fence)
	header.SafetyEpoch = SafetyEpoch(binding.SafetyEpoch)

	payload, err := preparePayloadFromBinding(binding)
	if err != nil {
		return nil, err
	}
	return bindDigest(NewPreparePublication(header, payload)), nil
}

func preparePayloadFromBinding(binding taskorchestration.PublicationRequestBinding) (PreparePublicationPayload, error) {
	kind := PublicationKindFirstGeneration
	if binding.Spec.Kind == taskorchestration.PublicationKindManualEdit {
		kind = PublicationKindManualEdit
	}
	members := make([]ArtifactMemberSpec, 0, len(binding.Spec.Members))
	for _, member := range binding.Spec.Members {
		artifactKind := artifactKindFromPublication(member.Kind)
		if artifactKind == "" {
			return PreparePublicationPayload{}, &Error{Code: ErrorInvalidIntent}
		}
		members = append(members, ArtifactMemberSpec{
			Slot:          MemberSlotID(member.Slot.String()),
			Kind:          artifactKind,
			LogicalName:   member.LogicalName,
			MediaType:     MediaType(member.MediaType),
			Size:          member.Size,
			ContentDigest: toPublicationDigest(member.ContentDigest),
		})
	}
	staging := make([]StagingReference, 0, len(binding.Spec.Staging))
	for _, ref := range binding.Spec.Staging {
		staging = append(staging, StagingReference{
			MemberSlot:         MemberSlotID(ref.MemberSlot.String()),
			ContentID:          ContentID(ref.ContentID.String()),
			ContentDigest:      toPublicationDigest(ref.ContentDigest),
			Size:               ref.Size,
			Purpose:            ContentPurposePublicationMember,
			PhysicalGeneration: ref.PhysicalGeneration,
			AdapterID:          AdapterID(ref.AdapterID.String()),
		})
	}
	channels := make([]ChannelKind, 0, len(binding.Spec.RequiredChannels))
	runtimeRefs := make([]RuntimeEvidenceRef, 0, len(binding.Spec.RuntimeRefs))
	for _, ref := range binding.Spec.RuntimeRefs {
		channel := channelKindFromPublication(ref.Channel)
		if channel == "" {
			return PreparePublicationPayload{}, &Error{Code: ErrorInvalidIntent}
		}
		channels = append(channels, channel)
		runtimeRefs = append(runtimeRefs, RuntimeEvidenceRef{
			Channel: channel, EvidenceID: EvidenceID(ref.EvidenceID.String()),
			Digest: toPublicationDigest(ref.Digest),
		})
	}
	capabilityRefs := make([]ContentCapabilityRef, 0, len(binding.Spec.ContentCapabilityRefs))
	for _, ref := range binding.Spec.ContentCapabilityRefs {
		capabilityRefs = append(capabilityRefs, ContentCapabilityRef{
			MemberSlot:   MemberSlotID(ref.MemberSlot.String()),
			CapabilityID: ContentCapabilityID(ref.CapabilityID.String()),
			Digest:       toPublicationDigest(ref.Digest),
		})
	}
	return PreparePublicationPayload{
		ContractID:            PublicationContractID(binding.Spec.ContractID.String()),
		Kind:                  kind,
		Parent:                ArtifactVersionID(binding.Spec.Parent.String()),
		PhaseRunID:            PhaseRunID(binding.PhaseRunID.String()),
		Members:               members,
		Staging:               staging,
		RequiredChannels:      channels,
		RuntimeRefs:           runtimeRefs,
		ValidationRef:         EvidenceRef{EvidenceID: EvidenceID(binding.Spec.ValidationRef.EvidenceID.String()), Digest: toPublicationDigest(binding.Spec.ValidationRef.Digest)},
		C04CommitRef:          EvidenceRef{EvidenceID: EvidenceID(binding.Spec.C04CommitRef.EvidenceID.String()), Digest: toPublicationDigest(binding.Spec.C04CommitRef.Digest)},
		ContentCapabilityRefs: capabilityRefs,
	}, nil
}

func artifactKindFromPublication(kind taskorchestration.PublicationMemberKind) ArtifactKind {
	switch kind {
	case taskorchestration.PublicationMemberDeck:
		return ArtifactKindDeck
	case taskorchestration.PublicationMemberPreview:
		return ArtifactKindPreview
	case taskorchestration.PublicationMemberPlan:
		return ArtifactKindPlan
	case taskorchestration.PublicationMemberValidationReport:
		return ArtifactKindValidationReport
	default:
		return ""
	}
}

func channelKindFromPublication(channel taskorchestration.PublicationChannel) ChannelKind {
	switch channel {
	case taskorchestration.PublicationChannelDeck:
		return ChannelDeck
	default:
		return ""
	}
}

func toPublicationDigest(digest taskorchestration.EvidenceDigest) Digest {
	return Digest("sha256:" + digest.String())
}

// bridgeBindingFromIntent mirrors a C05 PreparePublication intent into the
// Task Orchestration committed binding (digests converted to the
// Task Orchestration encoding).
func bridgeBindingFromIntent(
	t *testing.T,
	intent PreparePublication,
) taskorchestration.PublicationRequestBinding {
	t.Helper()
	header := intent.header()
	kind := taskorchestration.PublicationKindFirstGeneration
	if intent.Kind == PublicationKindManualEdit {
		kind = taskorchestration.PublicationKindManualEdit
	}
	members := make([]taskorchestration.PublicationMemberSpec, 0, len(intent.Members))
	for _, member := range intent.Members {
		members = append(members, taskorchestration.PublicationMemberSpec{
			Slot:          mustPublicationSlot(t, string(member.Slot)),
			Kind:          publicationMemberKindFromArtifact(t, member.Kind),
			LogicalName:   member.LogicalName,
			MediaType:     string(member.MediaType),
			Size:          member.Size,
			ContentDigest: mustEvidenceDigest(t, member.ContentDigest),
		})
	}
	staging := make([]taskorchestration.PublicationStagingReference, 0, len(intent.Staging))
	for _, ref := range intent.Staging {
		staging = append(staging, taskorchestration.PublicationStagingReference{
			MemberSlot:         mustPublicationSlot(t, string(ref.MemberSlot)),
			ContentID:          mustPublicationContentID(t, string(ref.ContentID)),
			ContentDigest:      mustEvidenceDigest(t, ref.ContentDigest),
			Size:               ref.Size,
			Purpose:            taskorchestration.PublicationStagingPurposeMember,
			PhysicalGeneration: ref.PhysicalGeneration,
			AdapterID:          mustPublicationAdapterID(t, string(ref.AdapterID)),
		})
	}
	channels := make([]taskorchestration.PublicationChannel, 0, len(intent.RequiredChannels))
	runtimeRefs := make([]taskorchestration.PublicationRuntimeEvidenceRef, 0, len(intent.RuntimeRefs))
	for _, ref := range intent.RuntimeRefs {
		channel := publicationChannelFromArtifact(t, ref.Channel)
		channels = append(channels, channel)
		runtimeRefs = append(runtimeRefs, taskorchestration.PublicationRuntimeEvidenceRef{
			Channel: channel, EvidenceID: mustEvidenceID(t, string(ref.EvidenceID)),
			Digest: mustEvidenceDigest(t, ref.Digest),
		})
	}
	capabilityRefs := make([]taskorchestration.PublicationCapabilityRef, 0, len(intent.ContentCapabilityRefs))
	for _, ref := range intent.ContentCapabilityRefs {
		capabilityRefs = append(capabilityRefs, taskorchestration.PublicationCapabilityRef{
			MemberSlot:   mustPublicationSlot(t, string(ref.MemberSlot)),
			CapabilityID: mustPublicationCapabilityID(t, string(ref.CapabilityID)),
			Digest:       mustEvidenceDigest(t, ref.Digest),
		})
	}
	operationID := mustOperationID(t, string(header.Operation.ID))
	return taskorchestration.PublicationRequestBinding{
		SchemaVersion:          taskorchestration.PublicationSchemaV1,
		RequestID:              mustPublicationRequestID(t, string(header.RequestID)),
		OperationID:            operationID,
		TaskID:                 mustTaskID(t, string(header.TaskID)),
		PhaseRunID:             mustPhaseRunID(t, string(intent.PhaseRunID)),
		ActivityGeneration:     taskorchestration.ActivityGeneration(header.ActivityGeneration),
		Generation:             taskorchestration.ProducerGeneration(header.Generation),
		Fence:                  taskorchestration.PublicationFence(header.Fence),
		SafetyEpoch:            taskorchestration.SafetyEpoch(header.SafetyEpoch),
		ExpectedStreamRevision: uint64(header.ExpectedStreamRevision),
		ExpectedHead:           optionalArtifactVersionID(t, string(header.ExpectedHead)),
		Spec: taskorchestration.NewPublicationRequestSpec(
			mustPublicationContractID(t, string(intent.ContractID)),
			kind,
			optionalArtifactVersionID(t, string(intent.Parent)),
			members, staging, channels, runtimeRefs,
			taskorchestration.PublicationEvidenceRef{
				EvidenceID: mustEvidenceID(t, string(intent.ValidationRef.EvidenceID)),
				Digest:     mustEvidenceDigest(t, intent.ValidationRef.Digest),
			},
			taskorchestration.PublicationEvidenceRef{
				EvidenceID: mustEvidenceID(t, string(intent.C04CommitRef.EvidenceID)),
				Digest:     mustEvidenceDigest(t, intent.C04CommitRef.Digest),
			},
			capabilityRefs,
		),
	}
}

// TestOwnedTransportBridgeCommitFixesCompleteCanonicalRequest proves the
// committed binding carries the exact C05 request identity/digest: the
// reconstructed C05 intent from the committed binding has the SAME
// canonical digest as the original C05 request, and the binding digest is
// fixed at commit.
func TestOwnedTransportBridgeCommitFixesCompleteCanonicalRequest(t *testing.T) {
	f := newFixture(t)
	operationID := "bridge-commit-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
	originalDigest := CanonicalRequestDigest(intent)

	binding := bridgeBindingFromIntent(t, mustPrepareIntent(t, intent))
	if !binding.Valid() {
		t.Fatal("committed binding must be valid")
	}
	// The reconstructed C05 request from the committed binding has the same
	// canonical digest: the binding commits the C05 request's exact
	// identity/digest.
	reconstructed, err := publicationPrepareFromBinding(f, binding)
	if err != nil {
		t.Fatalf("reconstruct intent from committed binding: %v", err)
	}
	if CanonicalRequestDigest(reconstructed) != originalDigest {
		t.Fatalf("committed binding digest = %v, want C05 request digest %v",
			CanonicalRequestDigest(reconstructed), originalDigest)
	}

	now := time.Unix(int64(f.now), 0).UTC()
	transport := taskorchestration.NewDeterministicPublicationTransport(func() time.Time { return now })
	adapter := taskorchestration.NewPublicationBridgeAdapter(transport)
	bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("commit complete canonical request: %v", err)
	}
	claimed := bridge.Claim(1)
	delivery, err := bridge.Deliver(context.Background(), claimed[0])
	if err != nil || delivery.Outcome != taskorchestration.PublicationDelivered {
		t.Fatalf("bridge deliver = %#v, err = %v", delivery, err)
	}
	if delivery.OperationID != binding.OperationID || delivery.Digest != binding.CanonicalDigest() {
		t.Fatalf("bridge delivery changed identity/digest: %#v", delivery)
	}
}

// TestOwnedTransportBridgeDeliversThroughOwnedTransportToC05 wires the
// committed publication outbox to the C05 owned transport (in-memory) and
// proves the full canonical request reaches the C05 seam with the exact
// OperationID and digest, and that activation evidence binds the exact
// facts.
func TestOwnedTransportBridgeDeliversThroughOwnedTransportToC05(t *testing.T) {
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	client := harness.Client()
	operationID := "bridge-c05-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))

	binding := bridgeBindingFromIntent(t, mustPrepareIntent(t, intent))
	now := time.Unix(int64(f.now), 0).UTC()
	port := &ownedTransportBridgePort{f: f, client: client}
	adapter := taskorchestration.NewPublicationBridgeAdapter(port)
	bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("commit: %v", err)
	}

	claimed := bridge.Claim(1)
	delivery, err := bridge.Deliver(context.Background(), claimed[0])
	if err != nil || delivery.Outcome != taskorchestration.PublicationDelivered {
		t.Fatalf("deliver through owned transport = %#v, err = %v", delivery, err)
	}

	// The envelope carried the exact OperationID and the C05 canonical
	// request digest.
	requests := harness.Requests()
	if len(requests) != 1 {
		t.Fatalf("transport requests = %d, want 1", len(requests))
	}
	envelope := requests[0].Envelope
	if envelope.OperationID != PublicationOperationID(operationID) ||
		envelope.CanonicalRequestDigest != CanonicalRequestDigest(intent) {
		t.Fatalf("transport envelope identity/digest = %#v", envelope)
	}

	// The C05 operation is durably prepared.
	view, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: PublicationOperationID(operationID),
	})
	if err != nil || view.State != OperationPrepared {
		t.Fatalf("C05 operation after bridge delivery = %#v, err = %v", view, err)
	}

	// Verify and activate through the same transport, then prove the
	// activation evidence binds the exact OperationID, Phase Run
	// generation/fence, activity generation, safety epoch,
	// ArtifactVersionID and manifest digest.
	if _, err := client.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
		t.Fatalf("verify through transport: %v", err)
	}
	activated, err := client.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || activated.ActivationEvidence == nil {
		t.Fatalf("activate through transport = %#v, err = %v", activated, err)
	}
	evidence := activated.ActivationEvidence
	if evidence.OperationID != PublicationOperationID(operationID) ||
		evidence.PhaseRunID != f.phaseRunID ||
		evidence.ActivityGeneration != f.generation ||
		evidence.Generation != f.generation || evidence.Fence != f.fence ||
		evidence.SafetyEpoch != f.safetyEpoch ||
		evidence.ArtifactVersionID != view.ArtifactVersionID ||
		evidence.ManifestDigest != view.ManifestDigest {
		t.Fatalf("activation evidence did not bind the exact facts: %#v", evidence)
	}
}

// TestOwnedTransportBridgeAmbiguityAndConflictProveNoSecondOperation wires
// response loss and same-OperationID/different-request conflicts through
// the full bridge and proves they never create a second publication
// operation or Artifact Version.
func TestOwnedTransportBridgeAmbiguityAndConflictProveNoSecondOperation(t *testing.T) {
	t.Run("response loss reconciles by original OperationID", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		client := harness.Client()
		operationID := "bridge-response-loss-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
		binding := bridgeBindingFromIntent(t, mustPrepareIntent(t, intent))
		now := time.Unix(int64(f.now), 0).UTC()
		port := &ownedTransportBridgePort{f: f, client: client}
		adapter := taskorchestration.NewPublicationBridgeAdapter(port)
		bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
		if err := bridge.Commit(binding); err != nil {
			t.Fatalf("commit: %v", err)
		}
		claimed := bridge.Claim(1)
		harness.FailNext(OwnedTransportResponseLoss)
		delivery, err := bridge.Deliver(context.Background(), claimed[0])
		if err != nil || delivery.Outcome != taskorchestration.PublicationReconciliationRequired {
			t.Fatalf("response-lost delivery = %#v, err = %v", delivery, err)
		}
		// The operation committed durably; reconcile by the ORIGINAL
		// OperationID and exact-replay the same committed binding.
		reconciled, err := bridge.Reconcile(context.Background(), binding.OperationID)
		if err != nil || reconciled.OperationID != binding.OperationID ||
			reconciled.Outcome != taskorchestration.PublicationDelivered {
			t.Fatalf("reconcile original operation = %#v, err = %v", reconciled, err)
		}
		view, err := client.Query(context.Background(), PublicationQuery{
			Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			OperationID: PublicationOperationID(operationID),
		})
		if err != nil || view.State != OperationPrepared {
			t.Fatalf("C05 operation after response loss = %#v, err = %v", view, err)
		}
	})

	t.Run("conflicting binding never creates a second operation", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		client := harness.Client()
		operationID := "bridge-conflict-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
		binding := bridgeBindingFromIntent(t, mustPrepareIntent(t, intent))
		now := time.Unix(int64(f.now), 0).UTC()
		port := &ownedTransportBridgePort{f: f, client: client}
		adapter := taskorchestration.NewPublicationBridgeAdapter(port)
		bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
		if err := bridge.Commit(binding); err != nil {
			t.Fatalf("commit: %v", err)
		}
		_ = bridge.Claim(1)

		conflicting := binding
		conflicting.Spec = taskorchestration.NewPublicationRequestSpec(
			conflicting.Spec.ContractID, conflicting.Spec.Kind, conflicting.Spec.Parent,
			[]taskorchestration.PublicationMemberSpec{{
				Slot: conflicting.Spec.Members[0].Slot, Kind: taskorchestration.PublicationMemberDeck,
				LogicalName: "Deck-renamed.pptx", MediaType: conflicting.Spec.Members[0].MediaType,
				Size: conflicting.Spec.Members[0].Size, ContentDigest: conflicting.Spec.Members[0].ContentDigest,
			}},
			conflicting.Spec.Staging, conflicting.Spec.RequiredChannels,
			conflicting.Spec.RuntimeRefs, conflicting.Spec.ValidationRef,
			conflicting.Spec.C04CommitRef, conflicting.Spec.ContentCapabilityRefs,
		)
		delivery, err := bridge.Deliver(context.Background(), conflicting)
		if err != nil || delivery.Outcome != taskorchestration.PublicationIntegrityConflict {
			t.Fatalf("conflicting delivery = %#v, err = %v", delivery, err)
		}
		// No second operation or version exists: the publication stream is
		// untouched (absent or still at revision zero with no head).
		view, streamErr := client.Query(context.Background(), PublicationQuery{
			Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		})
		if streamErr == nil {
			if view.StreamRevision != 0 || view.CurrentHead != "" {
				t.Fatalf("conflicting binding must not create a version: %#v", view)
			}
		} else if !isCode(streamErr, ErrorNotFound) {
			t.Fatalf("conflicting binding stream query: %v", streamErr)
		}
	})
}

func mustPrepareIntent(t *testing.T, intent PublicationIntent) PreparePublication {
	t.Helper()
	prepare, ok := intent.(PreparePublication)
	if !ok {
		t.Fatalf("intent is %T, want PreparePublication", intent)
	}
	return prepare
}

func mustPublicationSlot(t *testing.T, value string) taskorchestration.PublicationMemberSlotID {
	t.Helper()
	id, err := taskorchestration.NewPublicationMemberSlotID(value)
	if err != nil {
		t.Fatalf("publication slot identity %q: %v", value, err)
	}
	return id
}

func mustPublicationContentID(t *testing.T, value string) taskorchestration.PublicationContentID {
	t.Helper()
	id, err := taskorchestration.NewPublicationContentID(value)
	if err != nil {
		t.Fatalf("publication content identity %q: %v", value, err)
	}
	return id
}

func mustPublicationCapabilityID(t *testing.T, value string) taskorchestration.PublicationCapabilityID {
	t.Helper()
	id, err := taskorchestration.NewPublicationCapabilityID(value)
	if err != nil {
		t.Fatalf("publication capability identity %q: %v", value, err)
	}
	return id
}

func mustPublicationAdapterID(t *testing.T, value string) taskorchestration.PublicationAdapterID {
	t.Helper()
	id, err := taskorchestration.NewPublicationAdapterID(value)
	if err != nil {
		t.Fatalf("publication adapter identity %q: %v", value, err)
	}
	return id
}

func mustPublicationContractID(t *testing.T, value string) taskorchestration.PublicationContractID {
	t.Helper()
	id, err := taskorchestration.NewPublicationContractID(value)
	if err != nil {
		t.Fatalf("publication contract identity %q: %v", value, err)
	}
	return id
}

func mustPublicationRequestID(t *testing.T, value string) taskorchestration.PublicationRequestID {
	t.Helper()
	id, err := taskorchestration.NewPublicationRequestID(value)
	if err != nil {
		t.Fatalf("publication request identity %q: %v", value, err)
	}
	return id
}

func mustTaskID(t *testing.T, value string) taskorchestration.TaskID {
	t.Helper()
	id, err := taskorchestration.NewTaskID(value)
	if err != nil {
		t.Fatalf("Task identity %q: %v", value, err)
	}
	return id
}

func mustPhaseRunID(t *testing.T, value string) taskorchestration.PhaseRunID {
	t.Helper()
	id, err := taskorchestration.NewPhaseRunID(value)
	if err != nil {
		t.Fatalf("Phase Run identity %q: %v", value, err)
	}
	return id
}

func mustOperationID(t *testing.T, value string) taskorchestration.OperationID {
	t.Helper()
	id, err := taskorchestration.NewOperationID(value)
	if err != nil {
		t.Fatalf("operation identity %q: %v", value, err)
	}
	return id
}

func optionalArtifactVersionID(t *testing.T, value string) taskorchestration.ArtifactVersionID {
	t.Helper()
	if value == "" {
		return taskorchestration.ArtifactVersionID{}
	}
	return mustArtifactVersionID(t, value)
}

func mustArtifactVersionID(t *testing.T, value string) taskorchestration.ArtifactVersionID {
	t.Helper()
	id, err := taskorchestration.NewArtifactVersionID(value)
	if err != nil {
		t.Fatalf("Artifact Version identity %q: %v", value, err)
	}
	return id
}

func mustEvidenceID(t *testing.T, value string) taskorchestration.EvidenceID {
	t.Helper()
	id, err := taskorchestration.NewEvidenceID(value)
	if err != nil {
		t.Fatalf("evidence identity %q: %v", value, err)
	}
	return id
}

func mustEvidenceDigest(t *testing.T, digest Digest) taskorchestration.EvidenceDigest {
	t.Helper()
	converted, err := taskorchestration.ParseEvidenceDigest(stringsTrimDigestPrefix(t, digest))
	if err != nil {
		t.Fatalf("evidence digest %q: %v", digest, err)
	}
	return converted
}

func stringsTrimDigestPrefix(t *testing.T, digest Digest) string {
	t.Helper()
	if len(digest) != len("sha256:")+64 || string(digest[:len("sha256:")]) != "sha256:" {
		t.Fatalf("digest %q is not a canonical sha256 digest", digest)
	}
	return string(digest[len("sha256:"):])
}

func publicationMemberKindFromArtifact(t *testing.T, kind ArtifactKind) taskorchestration.PublicationMemberKind {
	t.Helper()
	switch kind {
	case ArtifactKindDeck:
		return taskorchestration.PublicationMemberDeck
	case ArtifactKindPreview:
		return taskorchestration.PublicationMemberPreview
	case ArtifactKindPlan:
		return taskorchestration.PublicationMemberPlan
	case ArtifactKindValidationReport:
		return taskorchestration.PublicationMemberValidationReport
	default:
		t.Fatalf("unsupported Artifact kind %q", kind)
		return 0
	}
}

func publicationChannelFromArtifact(t *testing.T, channel ChannelKind) taskorchestration.PublicationChannel {
	t.Helper()
	switch channel {
	case ChannelDeck:
		return taskorchestration.PublicationChannelDeck
	default:
		t.Fatalf("unsupported channel %q", channel)
		return 0
	}
}
