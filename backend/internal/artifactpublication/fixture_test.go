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

// scopeRegistry is the deterministic Identity & Ownership / Sharing double.
// It resolves the current availability fact of one owner/share-link/
// break-glass scope instance for one exact Artifact Version. Revocation or
// rotation advances the generation or removes the fact, which makes content
// targets and C04 capabilities bound to the old generation stale. C05 never
// owns the scope lifecycle; it only binds and compares the generation.
type scopeRegistry struct {
	facts map[ContentScopeKey]ContentScope
}

func newScopeRegistry() *scopeRegistry {
	return &scopeRegistry{facts: make(map[ContentScopeKey]ContentScope)}
}

func (r *scopeRegistry) register(key ContentScopeKey, scope ContentScope) {
	r.facts[key] = scope
}

func (r *scopeRegistry) resolve(key ContentScopeKey) (ContentScope, bool) {
	fact, ok := r.facts[key]
	return fact, ok
}

// revoke removes the current fact: the scope is no longer available and
// every target/capability bound to it fails closed.
func (r *scopeRegistry) revoke(key ContentScopeKey) {
	delete(r.facts, key)
}

// rotate advances the current availability generation: targets/capabilities
// bound to the older generation become stale.
func (r *scopeRegistry) rotate(key ContentScopeKey, generation Generation) {
	if fact, ok := r.facts[key]; ok {
		fact.AvailabilityGeneration = generation
		r.facts[key] = fact
	}
}

// fixture is the deterministic test authority over the public seam.
type fixture struct {
	core         PublicationCore
	persistence  *InMemoryPersistence
	registry     *capabilityRegistry
	scopes       *scopeRegistry
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
	publicationAuthority       AuthorityID

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
		publicationAuthority:       "artifact-publication-authority",
		safetyEpoch:                7, generation: 3, fence: 4,
	}
	f.registry = newCapabilityRegistry()
	f.scopes = newScopeRegistry()
	f.persistence = newPersistence()
	f.core = NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		PublicationAuthorityID:       f.publicationAuthority,
		CurrentContentCapability:     f.registry.resolve,
		CurrentContentScope:          f.scopes.resolve,
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
		PublicationAuthorityID:       f.publicationAuthority,
		CurrentContentCapability:     f.registry.resolve,
		CurrentContentScope:          f.scopes.resolve,
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
	case ActivatePublication:
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

// exportEvidence builds the ValidatedExportEvidence binding an activated
// parent Artifact Version so a manual-edit child's C04 commit evidence can
// be validated against the exact parent (C04 reconstruction/export source).
func (f *fixture) exportEvidence(parent ArtifactVersionID, validation ValidationEvidence) *ValidatedExportEvidence {
	export := &ValidatedExportEvidence{
		ID:                       "export-1",
		PublicationAuthorityID:   f.c04Authority,
		PolicyDomainID:           f.policyDomain,
		TaskID:                   f.taskID,
		TaskWorkspaceID:          "task-workspace-1",
		SourceArtifactVersionID:  parent,
		ReconstructionEvidenceID: "reconstruction-1",
		RevisionID:               "revision-3",
		CheckpointID:             "checkpoint-3",
		ValidationEvidenceID:     validation.ID,
		Generation:               f.generation,
		Fence:                    f.fence,
	}
	export.Digest = export.CanonicalDigest()
	return export
}

// childEvidenceSet builds one internally consistent manual-edit evidence
// set whose C04 commit evidence binds the exact activated parent through
// ValidatedExportEvidence, and registers its Durable Object capabilities as
// currently valid.
func (f *fixture) childEvidenceSet(t *testing.T, parent ArtifactVersionID, operationID string) *evidenceSet {
	t.Helper()
	member := f.deckMemberSpec()
	set := &evidenceSet{proposalDigest: testDigest("proposal-manifest-child")}
	set.runtimeEvidence = []RuntimeEvidence{f.runtimeEvidence(ChannelDeck, set.proposalDigest)}
	set.validation = f.validationEvidence("publication-contract-1", set.runtimeEvidence, set.proposalDigest)
	set.c04 = f.c04Commit(set.validation, f.exportEvidence(parent, set.validation))
	set.c04.RevisionID = "revision-3"
	set.c04.CheckpointID = "checkpoint-3"
	set.c04.Digest = set.c04.CanonicalDigest()
	capability := f.contentCapability(
		"capability-child-"+operationID, string(member.Slot), "content-child-"+operationID,
		member.ContentDigest, member.Size,
	)
	set.capabilities = []ContentCapabilityEvidence{capability}
	f.registry.register(capability, true)
	return set
}

// childPreparePayload builds the canonical manual-edit child prepare
// request: kind manual-edit, exact parent, and the pinned references of the
// child evidence set. The caller must advance the header's expected stream
// revision/head to the parent's committed state.
func (f *fixture) childPreparePayload(operationID string, parent ArtifactVersionID, set *evidenceSet) PreparePublicationPayload {
	capability := set.capabilities[0]
	staging := []StagingReference{{
		MemberSlot: capability.MemberSlot, ContentID: capability.ContentID,
		ContentDigest: capability.ContentDigest, Size: capability.Size,
		Purpose: ContentPurposePublicationMember, PhysicalGeneration: capability.PhysicalGeneration,
		AdapterID: capability.AdapterID,
	}}
	capabilityRefs := []ContentCapabilityRef{{
		MemberSlot: capability.MemberSlot, CapabilityID: capability.ID, Digest: capability.Digest,
	}}
	runtimeRefs := make([]RuntimeEvidenceRef, 0, len(set.runtimeEvidence))
	for _, evidence := range set.runtimeEvidence {
		runtimeRefs = append(runtimeRefs, RuntimeEvidenceRef{
			Channel: evidence.Channel, EvidenceID: evidence.ID, Digest: evidence.Digest,
		})
	}
	return PreparePublicationPayload{
		ContractID:            set.validation.ContractID,
		Kind:                  PublicationKindManualEdit,
		Parent:                parent,
		PhaseRunID:            f.phaseRunID,
		Members:               []ArtifactMemberSpec{f.deckMemberSpec()},
		Staging:               staging,
		RequiredChannels:      []ChannelKind{ChannelDeck},
		RuntimeRefs:           runtimeRefs,
		ValidationRef:         EvidenceRef{EvidenceID: set.validation.ID, Digest: set.validation.Digest},
		C04CommitRef:          EvidenceRef{EvidenceID: set.c04.ID, Digest: set.c04.Digest},
		ContentCapabilityRefs: capabilityRefs,
	}
}

// activateAndReturn drives the standard first-generation happy path through
// activation and returns the evidence set and activation decision.
func (f *fixture) prepareVerifyActivate(t *testing.T, operationID string) (*evidenceSet, PublicationDecision) {
	t.Helper()
	set, _, _ := f.prepareAndVerify(t, operationID)
	activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("activate %s: %v", operationID, err)
	}
	return set, activated
}

func (f *fixture) prepareIntent(operationID string, payload PreparePublicationPayload) PublicationIntent {
	return bindDigest(NewPreparePublication(f.header(operationID), payload))
}

func (f *fixture) verifyIntent(operationID string, payload VerifyPublicationPayload) PublicationIntent {
	return bindDigest(NewVerifyPublication(f.header(operationID), payload))
}

func (f *fixture) activateIntent(operationID string) PublicationIntent {
	return bindDigest(NewActivatePublication(f.header(operationID)))
}

// activateIntentWithHeader activates with an explicit header so tests can
// pin an expected stream revision/head, generation/fence, or authority.
func (f *fixture) activateIntentWithHeader(header PublicationIntentHeader) PublicationIntent {
	return bindDigest(NewActivatePublication(header))
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

// scopeKey builds the registry key for one scope instance of one exact
// Artifact Version in the fixture's policy domain and Task.
func (f *fixture) scopeKey(versionID ArtifactVersionID, kind ContentScopeKind, id ScopeID) ContentScopeKey {
	return ContentScopeKey{
		PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, Kind: kind, ID: id,
	}
}

// ownerScope is the standard owner availability scope for one version.
func (f *fixture) ownerScope(versionID ArtifactVersionID) ContentScope {
	return ContentScope{Kind: ContentScopeOwner, ID: "owner-principal-1", AvailabilityGeneration: 1}
}

// shareScope is the standard Share Grant availability scope for one version.
func (f *fixture) shareScope(versionID ArtifactVersionID) ContentScope {
	return ContentScope{Kind: ContentScopeShareLink, ID: "share-grant-1", AvailabilityGeneration: 5}
}

// breakGlassScope is the standard BreakGlass grant availability scope for
// one version.
func (f *fixture) breakGlassScope(versionID ArtifactVersionID) ContentScope {
	return ContentScope{Kind: ContentScopeBreakGlass, ID: "break-glass-grant-1", AvailabilityGeneration: 3}
}

// registerScope records the current availability fact of one scope for one
// exact version in the Identity & Ownership / Sharing double.
func (f *fixture) registerScope(versionID ArtifactVersionID, scope ContentScope) {
	f.scopes.register(f.scopeKey(versionID, scope.Kind, scope.ID), scope)
}

// revokeScope removes the current availability fact (revocation).
func (f *fixture) revokeScope(versionID ArtifactVersionID, scope ContentScope) {
	f.scopes.revoke(f.scopeKey(versionID, scope.Kind, scope.ID))
}

// rotateScope advances the availability generation (rotation/revocation
// epoch advance).
func (f *fixture) rotateScope(versionID ArtifactVersionID, scope ContentScope, generation Generation) {
	f.scopes.rotate(f.scopeKey(versionID, scope.Kind, scope.ID), generation)
}

// platformAuthority is the Task Orchestration authority presented by the
// Platform when selecting the exact Artifact Version for a C04
// reconstruction capability.
func (f *fixture) platformAuthority() PublicationAuthority {
	return PublicationAuthority{
		Kind: AuthorityTaskOrchestration, ID: f.taskOrchestrationAuthority, Generation: 1,
	}
}

// resolveTarget drives the standard content-target resolution happy path
// for the exact member of the activated version under the owner scope with
// the download intent.
func (f *fixture) resolveTarget(t *testing.T, versionID ArtifactVersionID, artifactID ArtifactID, scope ContentScope) *ArtifactContentTarget {
	t.Helper()
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, ArtifactID: artifactID,
		Scope: scope, ContentIntent: ContentIntentDownload,
	})
	if err != nil {
		t.Fatalf("resolve content target: %v", err)
	}
	if view.ContentTarget == nil {
		t.Fatal("resolve content target returned no target")
	}
	return view.ContentTarget
}

// verifyTarget drives content-target verification with the presented scope.
func (f *fixture) verifyTarget(t *testing.T, target *ArtifactContentTarget, scope ContentScope) error {
	t.Helper()
	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVerifyContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		Scope: scope, ContentTarget: target,
	})
	return err
}

// issueC04Capability drives the standard C04 reconstruction capability
// issuance happy path: the Platform selects the exact version under the
// owner scope with a future expiry.
func (f *fixture) issueC04Capability(t *testing.T, versionID ArtifactVersionID, scope ContentScope, expiresAt Instant) *C04ReconstructionCapability {
	t.Helper()
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: versionID, Scope: scope, ExpiresAt: expiresAt,
		Authority: f.platformAuthority(),
	})
	if err != nil {
		t.Fatalf("issue C04 reconstruction capability: %v", err)
	}
	if view.C04Capability == nil {
		t.Fatal("issue C04 reconstruction capability returned no capability")
	}
	return view.C04Capability
}

// verifyC04Capability drives C04 capability verification with the presented
// scope.
func (f *fixture) verifyC04Capability(t *testing.T, capability *C04ReconstructionCapability, scope ContentScope) error {
	t.Helper()
	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVerifyC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		Scope: scope, C04Capability: capability,
	})
	return err
}
