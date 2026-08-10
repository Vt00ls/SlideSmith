package artifactpublication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

// testDigest returns a valid canonical sha256 digest derived from seed.
func testDigest(seed string) Digest {
	sum := sha256.Sum256([]byte(seed))
	return Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// capabilityRegistry is the deterministic Durable Object authority double.
// It resolves a capability only when it is registered AND currently valid,
// so in-flight or expired capabilities fail closed as durability-unverified.
type capabilityRegistry struct {
	facts   map[ContentCapabilityID]ContentCapabilityEvidence
	current map[ContentCapabilityID]bool
}

func newCapabilityRegistry() *capabilityRegistry {
	return &capabilityRegistry{facts: make(map[ContentCapabilityID]ContentCapabilityEvidence), current: make(map[ContentCapabilityID]bool)}
}

func (r *capabilityRegistry) register(capability ContentCapabilityEvidence, current bool) {
	r.facts[capability.ID] = capability
	r.current[capability.ID] = current
}

func (r *capabilityRegistry) resolve(id ContentCapabilityID) (ContentCapabilityEvidence, bool) {
	fact, ok := r.facts[id]
	if !ok || !r.current[id] {
		return ContentCapabilityEvidence{}, false
	}
	return fact, true
}

// fixture is the deterministic test authority over the public seam.
type fixture struct {
	core         PublicationCore
	persistence  *InMemoryPersistence
	registry     *capabilityRegistry
	now          Instant
	policyDomain PolicyDomainID
	taskID       TaskID
	phaseRunID   PhaseRunID

	taskOrchestrationAuthority AuthorityID
	runtimeAuthority           AuthorityID
	validationAuthority        AuthorityID
	c04Authority               AuthorityID
	durableObjectAuthority     AuthorityID
	recoveryAuthority          AuthorityID

	safetyEpoch SafetyEpoch
	generation  Generation
	fence       Fence
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	now := Instant(time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC).Unix())
	f := &fixture{
		now: now, policyDomain: "policy-domain-1", taskID: "task-1", phaseRunID: "phase-run-1",
		taskOrchestrationAuthority: "task-orchestration-authority",
		runtimeAuthority:           "runtime-authority",
		validationAuthority:        "validation-authority",
		c04Authority:               "c04-authority",
		durableObjectAuthority:     "durable-object-authority",
		recoveryAuthority:          "recovery-authority",
		safetyEpoch:                7, generation: 3, fence: 4,
	}
	f.registry = newCapabilityRegistry()
	f.persistence = newPersistence()
	f.core = NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CurrentContentCapability:     f.registry.resolve,
	}, f.persistence)
	return f
}

// rebuild resumes the authority from the same persistence (restart).
func (f *fixture) rebuild() {
	f.core = NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CurrentContentCapability:     f.registry.resolve,
	}, f.persistence)
}

func (f *fixture) orchestrationAuthority() PublicationAuthority {
	return PublicationAuthority{
		Kind: AuthorityTaskOrchestration, ID: f.taskOrchestrationAuthority, Generation: 1,
	}
}

func (f *fixture) recoveryAuthorityValue() PublicationAuthority {
	return PublicationAuthority{
		Kind: AuthorityRecovery, ID: f.recoveryAuthority, Generation: 1,
	}
}

func (f *fixture) header(operationID string) PublicationIntentHeader {
	return PublicationIntentHeader{
		SchemaVersion: SchemaV1, RequestID: PublicationRequestID("request-" + operationID),
		Operation:              Operation{ID: PublicationOperationID(operationID)},
		PolicyDomainID:         f.policyDomain,
		TaskID:                 f.taskID,
		ExpectedStreamRevision: 0,
		ActivityGeneration:     f.generation,
		Generation:             f.generation,
		Fence:                  f.fence,
		SafetyEpoch:            f.safetyEpoch,
		Authority:              f.orchestrationAuthority(),
		OccurredAt:             f.now,
	}
}

// bindDigest finalizes an intent by computing and binding its canonical
// request digest (the digest does not include the digest field itself).
func bindDigest(intent PublicationIntent) PublicationIntent {
	digest := CanonicalRequestDigest(intent)
	header := intent.header()
	header.Operation.RequestDigest = digest
	switch typed := intent.(type) {
	case PreparePublication:
		typed.intentHeader = header
		return typed
	case VerifyPublication:
		typed.intentHeader = header
		return typed
	case RejectPublication:
		typed.intentHeader = header
		return typed
	case CancelPublication:
		typed.intentHeader = header
		return typed
	case ReconcilePublication:
		typed.intentHeader = header
		return typed
	default:
		panic("unknown intent kind")
	}
}

// deckMemberSpec is the standard first-generation member for the tracer
// bullet.
func (f *fixture) deckMemberSpec() ArtifactMemberSpec {
	return ArtifactMemberSpec{
		Slot: "slot-deck", Kind: ArtifactKindDeck, LogicalName: "Deck.pptx",
		MediaType: MediaTypePPTX, Size: 1024, ContentDigest: testDigest("deck-content"),
	}
}

// evidenceSet is one complete, internally consistent upstream evidence set.
// The prepare request pins the exact identities and digests of this set;
// verify accepts only evidence matching them.
type evidenceSet struct {
	runtimeEvidence []RuntimeEvidence
	validation      ValidationEvidence
	c04             C04CommitEvidence
	capabilities    []ContentCapabilityEvidence
	proposalDigest  Digest
}

func (f *fixture) contentCapability(
	capabilityID, slot, contentID string, contentDigest Digest, size uint64,
) ContentCapabilityEvidence {
	capability := ContentCapabilityEvidence{
		ID: ContentCapabilityID(capabilityID),
		Producer: EvidenceProducer{
			AuthorityID: f.durableObjectAuthority, Generation: 1,
		},
		PolicyDomainID: f.policyDomain, Purpose: ContentPurposePublicationMember,
		ContentID: ContentID(contentID), MemberSlot: MemberSlotID(slot),
		ContentDigest: contentDigest, Size: size,
		WriteIntent: WriteIntentImmutable, PhysicalGeneration: 2,
		VerificationMethod: VerificationMethodReceiptBound,
		AdapterID:          "do-adapter-1",
		Generation:         f.generation, Fence: f.fence, SafetyEpoch: f.safetyEpoch,
	}
	capability.Digest = capability.CanonicalDigest()
	return capability
}

func (f *fixture) runtimeEvidence(channel ChannelKind, proposalDigest Digest) RuntimeEvidence {
	evidence := RuntimeEvidence{
		ID: "runtime-evidence-1",
		Producer: EvidenceProducer{
			AuthorityID: f.runtimeAuthority, Generation: 1,
		},
		PolicyDomainID: f.policyDomain, TaskID: f.taskID, PhaseRunID: f.phaseRunID,
		RuntimeRunID: "runtime-run-1", RuntimeOperationID: "runtime-operation-1",
		RuntimeBindingDigest:         testDigest("runtime-binding"),
		Channel:                      channel,
		OutputProposalManifestDigest: proposalDigest,
		ProposalRef:                  "proposal-ref-1",
		Outcome:                      "completed",
		Generation:                   f.generation,
		Fence:                        f.fence,
		SafetyEpoch:                  f.safetyEpoch,
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence
}

func (f *fixture) validationEvidence(
	contractID PublicationContractID,
	runtimeEvidence []RuntimeEvidence,
	proposalDigest Digest,
) ValidationEvidence {
	refs := make([]EvidenceRef, 0, len(runtimeEvidence))
	for _, evidence := range runtimeEvidence {
		refs = append(refs, EvidenceRef{EvidenceID: evidence.ID, Digest: evidence.Digest})
	}
	evidence := ValidationEvidence{
		ID: "validation-evidence-1",
		Producer: EvidenceProducer{
			AuthorityID: f.validationAuthority, Generation: 1,
		},
		PolicyDomainID: f.policyDomain, TaskID: f.taskID, PhaseRunID: f.phaseRunID,
		ContractID:                   contractID,
		RuntimeEvidenceRefs:          refs,
		OutputProposalManifestDigest: proposalDigest,
		Decision:                     "accepted",
		Generation:                   f.generation,
		Fence:                        f.fence,
		SafetyEpoch:                  f.safetyEpoch,
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence
}

func (f *fixture) c04Commit(validation ValidationEvidence, export *ValidatedExportEvidence) C04CommitEvidence {
	commit := C04CommitEvidence{
		ID: "c04-commit-1",
		Producer: EvidenceProducer{
			AuthorityID: f.c04Authority, Generation: 1,
		},
		PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		TaskWorkspaceID: "task-workspace-1", RevisionID: "revision-2",
		CheckpointID: "checkpoint-2", ValidationEvidenceID: validation.ID,
		ValidationEvidenceDigest:    validation.Digest,
		DeclaredStateManifestDigest: testDigest("declared-state"),
		ContentEvidenceRoot:         string(testDigest("content-root")),
		DurabilityEvidenceRoot:      string(testDigest("durability-root")),
		OperationID:                 "c04-operation-1",
		Generation:                  f.generation,
		Fence:                       f.fence,
		SafetyEpoch:                 f.safetyEpoch,
		ValidatedExportEvidence:     export,
	}
	commit.Digest = commit.CanonicalDigest()
	return commit
}

// buildEvidence constructs one internally consistent evidence set for the
// given member specs and registers its Durable Object capabilities as
// currently valid.
func (f *fixture) buildEvidence(t *testing.T, members []ArtifactMemberSpec) *evidenceSet {
	t.Helper()
	set := &evidenceSet{proposalDigest: testDigest("proposal-manifest")}
	set.runtimeEvidence = []RuntimeEvidence{f.runtimeEvidence(ChannelDeck, set.proposalDigest)}
	set.validation = f.validationEvidence("publication-contract-1", set.runtimeEvidence, set.proposalDigest)
	set.c04 = f.c04Commit(set.validation, nil)
	for index, member := range members {
		capability := f.contentCapability(
			fmt.Sprintf("capability-%d", index+1), string(member.Slot), fmt.Sprintf("content-%d", index+1),
			member.ContentDigest, member.Size,
		)
		set.capabilities = append(set.capabilities, capability)
		f.registry.register(capability, true)
	}
	return set
}

// capabilityForSlot returns the capability bound to a member slot.
func (set *evidenceSet) capabilityForSlot(slot MemberSlotID) (ContentCapabilityEvidence, bool) {
	for _, capability := range set.capabilities {
		if capability.MemberSlot == slot {
			return capability, true
		}
	}
	return ContentCapabilityEvidence{}, false
}

// preparePayload builds the canonical prepare request that pins the exact
// evidence references of set.
func (f *fixture) preparePayload(operationID string, set *evidenceSet, members []ArtifactMemberSpec) PreparePublicationPayload {
	staging := make([]StagingReference, 0, len(members))
	capabilityRefs := make([]ContentCapabilityRef, 0, len(members))
	for _, member := range members {
		capability, ok := set.capabilityForSlot(member.Slot)
		if !ok {
			continue
		}
		staging = append(staging, StagingReference{
			MemberSlot: member.Slot, ContentID: capability.ContentID, ContentDigest: member.ContentDigest,
			Size: member.Size, Purpose: ContentPurposePublicationMember,
			PhysicalGeneration: capability.PhysicalGeneration, AdapterID: capability.AdapterID,
		})
		capabilityRefs = append(capabilityRefs, ContentCapabilityRef{
			MemberSlot: member.Slot, CapabilityID: capability.ID, Digest: capability.Digest,
		})
	}
	runtimeRefs := make([]RuntimeEvidenceRef, 0, len(set.runtimeEvidence))
	for _, evidence := range set.runtimeEvidence {
		runtimeRefs = append(runtimeRefs, RuntimeEvidenceRef{
			Channel: evidence.Channel, EvidenceID: evidence.ID, Digest: evidence.Digest,
		})
	}
	payload := PreparePublicationPayload{
		ContractID: set.validation.ContractID,
		Kind:       PublicationKindFirstGeneration,
		PhaseRunID: f.phaseRunID,
		Members:    members, Staging: staging,
		RequiredChannels:      []ChannelKind{ChannelDeck},
		RuntimeRefs:           runtimeRefs,
		ValidationRef:         EvidenceRef{EvidenceID: set.validation.ID, Digest: set.validation.Digest},
		C04CommitRef:          EvidenceRef{EvidenceID: set.c04.ID, Digest: set.c04.Digest},
		ContentCapabilityRefs: capabilityRefs,
	}
	payload.Parent = ""
	return payload
}

func (f *fixture) verifyPayload(set *evidenceSet) VerifyPublicationPayload {
	return VerifyPublicationPayload{
		RuntimeEvidence:     set.runtimeEvidence,
		ValidationEvidence:  set.validation,
		C04CommitEvidence:   set.c04,
		ContentCapabilities: set.capabilities,
	}
}

func (f *fixture) prepareIntent(operationID string, payload PreparePublicationPayload) PublicationIntent {
	return bindDigest(NewPreparePublication(f.header(operationID), payload))
}

func (f *fixture) verifyIntent(operationID string, payload VerifyPublicationPayload) PublicationIntent {
	return bindDigest(NewVerifyPublication(f.header(operationID), payload))
}

func (f *fixture) rejectIntent(operationID string, reason RejectReason, failure *EvidenceFailure) PublicationIntent {
	return bindDigest(NewRejectPublication(f.header(operationID), reason, failure))
}

func (f *fixture) cancelIntent(operationID string, reason CancelReason) PublicationIntent {
	return bindDigest(NewCancelPublication(f.header(operationID), reason))
}

func (f *fixture) reconcileIntent(operationID string, mode ReconcileMode) PublicationIntent {
	return bindDigest(NewReconcilePublication(f.header(operationID), mode))
}

// prepareAndVerify drives the standard first-generation happy path and
// returns the evidence set used, the prepare decision, and the verify
// decision.
func (f *fixture) prepareAndVerify(t *testing.T, operationID string) (*evidenceSet, PublicationDecision, PublicationDecision) {
	t.Helper()
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil {
		t.Fatalf("prepare %s: %v", operationID, err)
	}
	verify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("verify %s: %v", operationID, err)
	}
	return set, prepare, verify
}

func (f *fixture) mustPrepare(t *testing.T, operationID string, set *evidenceSet) PublicationDecision {
	t.Helper()
	return f.mustPreparePayload(t, operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
}

func (f *fixture) mustPreparePayload(t *testing.T, operationID string, payload PreparePublicationPayload) PublicationDecision {
	t.Helper()
	decision, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, payload))
	if err != nil {
		t.Fatalf("prepare %s: %v", operationID, err)
	}
	return decision
}

func (f *fixture) mustVerify(t *testing.T, operationID string, set *evidenceSet) PublicationDecision {
	t.Helper()
	decision, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("verify %s: %v", operationID, err)
	}
	return decision
}
