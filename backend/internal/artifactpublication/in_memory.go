package artifactpublication

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// InMemoryConfig configures the deterministic in-memory authority.
// All authorities are registered explicitly so evidence doubles cannot
// self-verify; the Durable Object registry double provides the current
// validity fact for verified-content capabilities.
type InMemoryConfig struct {
	// Now returns the controlled diagnostic clock.
	Now func() Instant
	// RuntimeAuthorityID registers the Runtime Execution authority.
	RuntimeAuthorityID AuthorityID
	// ValidationAuthorityID registers the Platform validation authority.
	ValidationAuthorityID AuthorityID
	// C04AuthorityID registers the Task Workspace Lifecycle authority.
	C04AuthorityID AuthorityID
	// DurableObjectAuthorityID registers the Durable Object authority.
	DurableObjectAuthorityID AuthorityID
	// TaskOrchestrationAuthorityID registers the Task Orchestration
	// authority that submits prepare/verify/reject/cancel/reconcile.
	TaskOrchestrationAuthorityID AuthorityID
	// RecoveryAuthorityID registers the protected recovery authority that
	// may cancel an operation.
	RecoveryAuthorityID AuthorityID
	// CurrentContentCapability resolves the current verified-content
	// capability for a capability identity. The double returns (fact, true)
	// only for capabilities that are currently valid in the Durable Object
	// authority; (zero, false) means the capability is not currently
	// resolvable (in-flight, expired, or unknown), which fails closed as
	// durability-unverified and requires reconciliation.
	CurrentContentCapability func(ContentCapabilityID) (ContentCapabilityEvidence, bool)
	// PublicationAuthorityID is C05's own authority identity, bound by every
	// C04 reconstruction capability as the publication authority. When it is
	// empty, C04 capability issuance fails closed.
	PublicationAuthorityID AuthorityID
	// CurrentContentScope resolves the current availability fact of one
	// owner/share-link/break-glass scope instance for one exact Artifact
	// Version. It is the narrow black-box port to Identity & Ownership /
	// Sharing: C05 never creates a principal, share token, Access Code,
	// Verification Session, or implicit administrator content authority, and
	// scopes can never union. (zero, false) means the scope is unknown or
	// revoked, which fails closed non-enumerating.
	CurrentContentScope func(ContentScopeKey) (ContentScope, bool)
	// FaultHook injects faults at bounded points. A non-nil error aborts
	// the mutation exactly at that point; points before persistence leave
	// the operation absent (retry re-runs), points after persistence leave
	// the operation durable (retry exact-replays).
	FaultHook func(FaultEvent) error
	// ScheduleHook is invoked at the start of each mutation before the
	// authority lock is taken, allowing deterministic race scheduling.
	ScheduleHook func(IntentScheduleEvent)
}

// IntentScheduleEvent reports one mutation about to enter the engine.
type IntentScheduleEvent struct {
	Kind           PublicationIntentKind
	OperationID    PublicationOperationID
	PolicyDomainID PolicyDomainID
	TaskID         TaskID
}

// FaultPoint is the closed set of fault injection points.
type FaultPoint uint8

const (
	// FaultBeforeOperationJournal aborts before the operation is journaled.
	FaultBeforeOperationJournal FaultPoint = iota + 1
	// FaultBeforeCandidatePersistence aborts before the candidate manifest
	// and lineage are persisted.
	FaultBeforeCandidatePersistence
	// FaultBeforeEvidenceAcceptance aborts before evidence is evaluated.
	FaultBeforeEvidenceAcceptance
	// FaultBeforeVerificationResult aborts after evidence evaluation but
	// before the verification result is recorded.
	FaultBeforeVerificationResult
	// FaultBeforeActivationCommit aborts after the activation revalidation
	// but before the Artifact Version, members, lineage, stream revision,
	// current head, and activation evidence are committed.
	FaultBeforeActivationCommit
	// FaultBeforeResponse aborts after durable persistence but before the
	// decision is returned (response loss).
	FaultBeforeResponse
	// FaultAfterResponse aborts after the decision is durable and the
	// response hook is invoked.
	FaultAfterResponse
)

// FaultEvent reports one fault injection.
type FaultEvent struct {
	Point       FaultPoint
	OperationID PublicationOperationID
	IntentKind  PublicationIntentKind
	SubjectID   string
}

// InMemoryPersistence is the opaque, restartable persistence handle. It
// holds every journal, stream, candidate, allocation, and residue record so
// a new authority can resume from it with the same deterministic facts.
type InMemoryPersistence struct {
	mu            sync.Mutex
	nextVersion   uint64
	nextArtifact  uint64
	operations    map[operationScope]*operationRecord
	streams       map[taskScope]*streamRecord
	versionFacts  map[ArtifactVersionID]contentFact
	artifactFacts map[ArtifactID]contentFact
	versionIndex  map[ArtifactVersionID]operationScope
	// activated is the immutable set of committed Artifact Versions. It is
	// written exactly once per activation and only read afterwards.
	activated map[ArtifactVersionID]activatedRecord
}

func newPersistence() *InMemoryPersistence {
	return &InMemoryPersistence{
		operations:    make(map[operationScope]*operationRecord),
		streams:       make(map[taskScope]*streamRecord),
		versionFacts:  make(map[ArtifactVersionID]contentFact),
		artifactFacts: make(map[ArtifactID]contentFact),
		versionIndex:  make(map[ArtifactVersionID]operationScope),
		activated:     make(map[ArtifactVersionID]activatedRecord),
	}
}

// inMemory is the deterministic, restartable in-memory authority. It is the
// reference implementation of the invariant engine that later adapters must
// reuse through the public Mutate/Query seam.
type inMemory struct {
	persistence *InMemoryPersistence
	config      InMemoryConfig
}

// NewInMemory constructs an Artifact Publication authority over an optional
// existing persistence. When persistence is nil a fresh one is created;
// when it is non-nil the authority resumes the exact prior state (restart).
func NewInMemory(config InMemoryConfig, persistence *InMemoryPersistence) PublicationCore {
	if persistence == nil {
		persistence = newPersistence()
	}
	return &inMemory{persistence: persistence, config: config}
}

func (m *inMemory) now() Instant {
	if m.config.Now != nil {
		return m.config.Now()
	}
	return 0
}

func (m *inMemory) injectFault(point FaultPoint, operationID PublicationOperationID, kind PublicationIntentKind, subject string) error {
	if m.config.FaultHook == nil {
		return nil
	}
	return m.config.FaultHook(FaultEvent{
		Point: point, OperationID: operationID, IntentKind: kind, SubjectID: subject,
	})
}

func (m *inMemory) currentContentCapability(id ContentCapabilityID) (ContentCapabilityEvidence, bool) {
	if m.config.CurrentContentCapability == nil {
		return ContentCapabilityEvidence{}, false
	}
	return m.config.CurrentContentCapability(id)
}

func (m *inMemory) currentContentScope(key ContentScopeKey) (ContentScope, bool) {
	if m.config.CurrentContentScope == nil {
		return ContentScope{}, false
	}
	return m.config.CurrentContentScope(key)
}

// Mutate is the single mutation seam of the Artifact Publication authority.
// Every intent enters this invariant engine; there is no second mutation
// surface.
func (m *inMemory) Mutate(ctx context.Context, intent PublicationIntent) (PublicationDecision, error) {
	if intent == nil {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	kind := intent.kind()
	if !validIntentKind(kind) {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	header := intent.header()
	if header.SchemaVersion.Major() != SchemaV1.Major() {
		return PublicationDecision{}, &Error{Code: ErrorUnsupportedSchema}
	}
	if !intent.valid() {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	digest := CanonicalRequestDigest(intent)
	if digest == "" || header.Operation.RequestDigest != digest {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	if m.config.ScheduleHook != nil {
		m.config.ScheduleHook(IntentScheduleEvent{
			Kind: kind, OperationID: header.Operation.ID,
			PolicyDomainID: header.PolicyDomainID, TaskID: header.TaskID,
		})
	}

	m.persistence.mu.Lock()
	defer m.persistence.mu.Unlock()

	scope := operationScope{policyDomainID: header.PolicyDomainID, taskID: header.TaskID, operationID: header.Operation.ID}
	record := m.persistence.operations[scope]
	if record != nil {
		if kind == IntentReconcilePublication {
			// Reconcile always re-evaluates the original operation against
			// current authority state; it never returns a historical
			// snapshot, because its job is to inspect/replay the original
			// operation after ambiguous or stale dispositions.
			return m.continueOperation(ctx, intent, header, digest, scope, record)
		}
		if byKind := record.outcomes[kind]; byKind != nil {
			// Exact replay takes priority over fresh-state validation.
			if outcome, ok := byKind[digest]; ok {
				return replayOutcome(outcome)
			}
			// The same intent kind already entered this operation with a
			// different canonical payload: durable integrity conflict.
			record.integrityConflict = true
			return PublicationDecision{}, &Error{Code: ErrorIntegrityConflict}
		}
		if kind == IntentPreparePublication {
			// An operation identity is minted once at prepare; a second
			// prepare under the same identity is always a conflict.
			record.integrityConflict = true
			return PublicationDecision{}, &Error{Code: ErrorIntegrityConflict}
		}
		return m.continueOperation(ctx, intent, header, digest, scope, record)
	}
	if kind != IntentPreparePublication {
		// Verify, reject, cancel, and reconcile always reference an
		// original operation; unknown operations are not-found and never
		// enumerate other scopes.
		return PublicationDecision{}, &Error{Code: ErrorNotFound}
	}

	return m.prepare(ctx, intent.(PreparePublication), header, digest, scope)
}

// continueOperation processes the first submission of an intent kind against
// an already prepared operation through the same invariant engine.
func (m *inMemory) continueOperation(
	ctx context.Context,
	intent PublicationIntent,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	switch intent.kind() {
	case IntentVerifyPublication:
		return m.verify(ctx, intent.(VerifyPublication), header, digest, scope, record)
	case IntentActivatePublication:
		return m.activate(ctx, intent.(ActivatePublication), header, digest, scope, record)
	case IntentRejectPublication:
		return m.reject(ctx, intent.(RejectPublication), header, digest, scope, record)
	case IntentCancelPublication:
		return m.cancel(ctx, intent.(CancelPublication), header, digest, scope, record)
	case IntentReconcilePublication:
		return m.reconcile(ctx, intent.(ReconcilePublication), header, digest, scope, record)
	default:
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
}

func replayOutcome(outcome intentOutcome) (PublicationDecision, error) {
	decision := outcome.decision
	decision.Replay = true
	if outcome.err != nil {
		return decision, cloneError(outcome.err)
	}
	return decision, nil
}

func cloneError(err *Error) *Error {
	if err == nil {
		return nil
	}
	clone := *err
	return &clone
}

func (m *inMemory) recordOutcome(record *operationRecord, kind PublicationIntentKind, digest Digest, state PublicationOperationState, decision PublicationDecision, err *Error) {
	if record.outcomes == nil {
		record.outcomes = make(map[PublicationIntentKind]map[Digest]intentOutcome)
	}
	byKind := record.outcomes[kind]
	if byKind == nil {
		byKind = make(map[Digest]intentOutcome)
		record.outcomes[kind] = byKind
	}
	byKind[digest] = intentOutcome{
		digest: digest, state: state, decision: decision, err: cloneError(err), recordedAt: m.now(),
	}
	record.state = state
}

func decisionForRecord(record *operationRecord, replay bool, occurredAt Instant) PublicationDecision {
	decision := PublicationDecision{
		Operation:          Operation{ID: record.operationID, RequestDigest: record.requestDigest},
		State:              record.state,
		StreamRevision:     record.streamRevision,
		ArtifactVersionID:  m0VersionID(record),
		ManifestDigest:     m0ManifestDigest(record),
		LineageDigest:      m0LineageDigest(record),
		Verification:       m0Verification(record),
		ActivationEvidence: cloneEvidence(record.activationEvidence),
		Replay:             replay,
		IntegrityConflict:  record.integrityConflict,
		RejectReason:       record.rejectReason,
		CancelReason:       record.cancelReason,
		ReconcileMode:      record.reconcileMode,
		ResidueRelease:     record.residue != nil,
		OccurredAt:         occurredAt,
	}
	return decision
}

func cloneEvidence(evidence *PublicationEvidence) *PublicationEvidence {
	if evidence == nil {
		return nil
	}
	clone := *evidence
	return &clone
}

func m0VersionID(record *operationRecord) ArtifactVersionID {
	if record.candidate != nil {
		return record.candidate.versionID
	}
	return ""
}
func m0ManifestDigest(record *operationRecord) Digest {
	if record.candidate != nil {
		return record.candidate.manifestDigest
	}
	return ""
}
func m0LineageDigest(record *operationRecord) Digest {
	if record.candidate != nil {
		return record.candidate.lineageDigest
	}
	return ""
}
func m0Verification(record *operationRecord) *VerificationResult {
	if record.verification == nil {
		return nil
	}
	accepted := make([]EvidenceAccepted, 0, len(record.verification.accepted))
	for _, item := range record.verification.accepted {
		accepted = append(accepted, EvidenceAccepted{Kind: item.kind, EvidenceID: item.evidenceID, Digest: item.digest})
	}
	failure := record.verification.failure
	return &VerificationResult{State: record.verification.state, AcceptedEvidence: accepted, Failure: failure}
}

// prepare persists the stable operation identity, request digest, expected
// head/revision, candidate identity, immutable candidate manifest/lineage,
// and typed staging references before any external physical action.
func (m *inMemory) prepare(ctx context.Context, intent PreparePublication, header PublicationIntentHeader, digest Digest, scope operationScope) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := m.injectFault(FaultBeforeOperationJournal, operationID, IntentPreparePublication, ""); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	if err := m.ensureAuthority(header.Authority, IntentPreparePublication); err != nil {
		return PublicationDecision{}, err
	}

	taskKey := taskScope{policyDomainID: header.PolicyDomainID, taskID: header.TaskID}
	stream := m.persistence.streams[taskKey]
	if stream == nil {
		stream = &streamRecord{policyDomainID: header.PolicyDomainID, taskID: header.TaskID}
	}
	if header.ExpectedStreamRevision != stream.revision {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	if header.ExpectedHead != stream.currentHead {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	if intent.Kind == PublicationKindManualEdit {
		if intent.Parent != stream.currentHead {
			return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
		}
		if _, activated := m.persistence.versionFacts[intent.Parent]; !activated {
			// No activated parent exists yet in this authority. Manual-edit
			// child activation requires an exact activated parent (#105).
			return PublicationDecision{}, &Error{Code: ErrorIntegrityFailure}
		}
	}

	if !validPrepareBindings(intent) {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}

	record := &operationRecord{
		scope: scope, operationID: operationID, requestDigest: digest,
		state: OperationPrepared, generation: header.Generation, fence: header.Fence,
		safetyEpoch: header.SafetyEpoch, activityGeneration: header.ActivityGeneration,
		streamRevision: stream.revision, occurredAt: m.now(),
		outcomes: make(map[PublicationIntentKind]map[Digest]intentOutcome),
	}

	versionID := m.mintVersionID(header.PolicyDomainID, header.TaskID)
	// ArtifactIDs are minted in slot-sorted order so the canonical manifest
	// is stable regardless of the order members are declared in the request.
	specs := append([]ArtifactMemberSpec(nil), intent.Members...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].Slot < specs[j].Slot })
	members := make([]memberRecord, 0, len(specs))
	for _, spec := range specs {
		name, ok := normalizedLogicalName(spec.LogicalName)
		if !ok {
			return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
		}
		members = append(members, memberRecord{
			slot: spec.Slot, artifactID: m.mintArtifactID(header.PolicyDomainID, header.TaskID),
			kind: spec.Kind, logicalName: name, mediaType: spec.MediaType,
			size: spec.Size, contentDigest: spec.ContentDigest,
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].slot == members[j].slot {
			return members[i].artifactID < members[j].artifactID
		}
		return members[i].slot < members[j].slot
	})
	staging := make([]stagingRecord, 0, len(intent.Staging))
	for _, ref := range intent.Staging {
		staging = append(staging, stagingRecord{
			slot: ref.MemberSlot, contentID: ref.ContentID, contentDigest: ref.ContentDigest,
			size: ref.Size, purpose: ref.Purpose, physicalGeneration: ref.PhysicalGeneration,
			adapterID: ref.AdapterID,
		})
	}
	sort.Slice(staging, func(i, j int) bool { return staging[i].slot < staging[j].slot })

	manifestMembers := make([]ArtifactMember, 0, len(members))
	for _, member := range members {
		manifestMembers = append(manifestMembers, ArtifactMember{
			ArtifactID: member.artifactID, Kind: member.kind, LogicalName: member.logicalName,
			MediaType: member.mediaType, Size: member.size, ContentDigest: member.contentDigest,
		})
	}
	lineage := Lineage{
		SchemaVersion: header.SchemaVersion, VersionID: versionID, TaskID: header.TaskID,
		Kind: intent.Kind, Parent: intent.Parent, OperationID: operationID,
		PhaseRunID: intent.PhaseRunID, ContractID: intent.ContractID,
		RuntimeEvidenceRoot:   runtimeEvidenceRoot(intent.RuntimeRefs),
		ValidationEvidenceRef: intent.ValidationRef,
		C04CommitEvidenceRef:  intent.C04CommitRef,
		ContentCapabilityRoot: contentCapabilityRoot(intent.ContentCapabilityRefs),
		ActivityGeneration:    header.ActivityGeneration,
		Generation:            header.Generation,
		Fence:                 header.Fence,
		SafetyEpoch:           header.SafetyEpoch,
	}
	lineageDigest := lineage.CanonicalDigest()
	manifest := ArtifactManifest{
		SchemaVersion: header.SchemaVersion, VersionID: versionID, TaskID: header.TaskID,
		Kind: intent.Kind, Parent: intent.Parent, LineageDigest: lineageDigest,
		Members: manifestMembers,
	}
	manifestDigest := manifest.CanonicalDigest()

	candidate := &candidateRecord{
		versionID: versionID, schemaVersion: header.SchemaVersion, kind: intent.Kind,
		parent: intent.Parent, contractID: intent.ContractID, phaseRunID: intent.PhaseRunID,
		members: members, staging: staging,
		runtimeRefs: cloneRuntimeRefs(intent.RuntimeRefs), validationRef: intent.ValidationRef,
		c04Ref: intent.C04CommitRef, capabilityRefs: cloneCapabilityRefs(intent.ContentCapabilityRefs),
		requiredChannels: cloneChannels(intent.RequiredChannels),
		manifestDigest:   manifestDigest, lineageDigest: lineageDigest,
	}

	if err := m.injectFault(FaultBeforeCandidatePersistence, operationID, IntentPreparePublication, string(versionID)); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	record.candidate = candidate
	m.persistence.operations[scope] = record
	m.persistence.streams[taskKey] = stream
	m.persistence.versionFacts[versionID] = contentFact{policyDomainID: header.PolicyDomainID, taskID: header.TaskID, versionID: versionID}
	m.persistence.versionIndex[versionID] = scope

	decision := decisionForRecord(record, false, m.now())
	m.recordOutcome(record, IntentPreparePublication, digest, OperationPrepared, decision, nil)

	if err := m.injectFault(FaultBeforeResponse, operationID, IntentPreparePublication, string(versionID)); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	return decision, nil
}

func cloneRuntimeRefs(refs []RuntimeEvidenceRef) []RuntimeEvidenceRef {
	return append([]RuntimeEvidenceRef(nil), refs...)
}

func cloneCapabilityRefs(refs []ContentCapabilityRef) []ContentCapabilityRef {
	return append([]ContentCapabilityRef(nil), refs...)
}

func cloneChannels(channels []ChannelKind) []ChannelKind {
	return append([]ChannelKind(nil), channels...)
}

func (m *inMemory) mintVersionID(policyDomainID PolicyDomainID, taskID TaskID) ArtifactVersionID {
	for {
		m.persistence.nextVersion++
		id := ArtifactVersionID(fmt.Sprintf("artifact-version-%016x", m.persistence.nextVersion))
		if _, reused := m.persistence.versionFacts[id]; !reused {
			return id
		}
	}
}

func (m *inMemory) mintArtifactID(policyDomainID PolicyDomainID, taskID TaskID) ArtifactID {
	for {
		m.persistence.nextArtifact++
		id := ArtifactID(fmt.Sprintf("artifact-%016x", m.persistence.nextArtifact))
		if _, reused := m.persistence.artifactFacts[id]; !reused {
			m.persistence.artifactFacts[id] = contentFact{policyDomainID: policyDomainID, taskID: taskID}
			return id
		}
	}
}

// validPrepareBindings enforces the fixed request schema facts that the
// payload-level validator cannot express: per-slot consistency between the
// declared member facts, the Durable Object staging receipts, and the pinned
// capability references.
func validPrepareBindings(intent PreparePublication) bool {
	specBySlot := make(map[MemberSlotID]ArtifactMemberSpec, len(intent.Members))
	for _, spec := range intent.Members {
		specBySlot[spec.Slot] = spec
	}
	stagedBySlot := make(map[MemberSlotID]StagingReference, len(intent.Staging))
	for _, ref := range intent.Staging {
		stagedBySlot[ref.MemberSlot] = ref
	}
	capabilityBySlot := make(map[MemberSlotID]ContentCapabilityRef, len(intent.ContentCapabilityRefs))
	for _, ref := range intent.ContentCapabilityRefs {
		capabilityBySlot[ref.MemberSlot] = ref
	}
	for slot, spec := range specBySlot {
		staged, ok := stagedBySlot[slot]
		if !ok {
			return false
		}
		if staged.ContentDigest != spec.ContentDigest || staged.Size != spec.Size {
			return false
		}
		capability, ok := capabilityBySlot[slot]
		if !ok {
			return false
		}
		if capability.CapabilityID == "" || !validDigest(capability.Digest) {
			return false
		}
	}
	channelRefs := make(map[ChannelKind]bool, len(intent.RequiredChannels))
	for _, channel := range intent.RequiredChannels {
		if channelRefs[channel] {
			return false
		}
		channelRefs[channel] = true
	}
	for _, ref := range intent.RuntimeRefs {
		if !channelRefs[ref.Channel] {
			return false
		}
	}
	return true
}

func (m *inMemory) ensureAuthority(authority PublicationAuthority, kind PublicationIntentKind) *Error {
	if authority.Kind == AuthorityRecovery {
		if kind != IntentCancelPublication {
			return &Error{Code: ErrorOwnershipDenied}
		}
		if authority.ID != m.config.RecoveryAuthorityID {
			return &Error{Code: ErrorOwnershipDenied}
		}
		return nil
	}
	if authority.Kind != AuthorityTaskOrchestration {
		return &Error{Code: ErrorOwnershipDenied}
	}
	if authority.ID != m.config.TaskOrchestrationAuthorityID {
		return &Error{Code: ErrorOwnershipDenied}
	}
	return nil
}
