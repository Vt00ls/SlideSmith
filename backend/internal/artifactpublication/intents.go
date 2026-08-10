package artifactpublication

// PublicationIntentKind is the closed set of mutation intents accepted by
// Mutate. Every intent enters the same invariant engine; there is no second
// mutation surface.
type PublicationIntentKind string

const (
	IntentPreparePublication   PublicationIntentKind = "prepare"
	IntentVerifyPublication    PublicationIntentKind = "verify"
	IntentRejectPublication    PublicationIntentKind = "reject"
	IntentCancelPublication    PublicationIntentKind = "cancel"
	IntentReconcilePublication PublicationIntentKind = "reconcile"
)

func validIntentKind(kind PublicationIntentKind) bool {
	switch kind {
	case IntentPreparePublication, IntentVerifyPublication, IntentRejectPublication,
		IntentCancelPublication, IntentReconcilePublication:
		return true
	default:
		return false
	}
}

// PublicationAuthority is the typed authority that submitted an intent. Each
// intent binds exactly one typed authority; combining authorities is
// rejected by construction.
type PublicationAuthority struct {
	Kind       EvidenceAuthorityKind
	ID         AuthorityID
	Generation Generation
}

func (a PublicationAuthority) canonical() map[string]any {
	return map[string]any{
		"kind":       string(a.Kind),
		"id":         string(a.ID),
		"generation": uint64(a.Generation),
	}
}

func validMutationAuthority(authority PublicationAuthority) bool {
	switch authority.Kind {
	case AuthorityTaskOrchestration, AuthorityRecovery:
		return authority.ID != "" && authority.Generation != 0
	default:
		return false
	}
}

// PublicationIntentHeader binds the shared authoritative facts of every
// mutation: schema version, request identity, operation identity and
// canonical request digest, Task identity, expected publication-stream
// revision, optional expected current head, activity generation, publication
// generation/fence, safety epoch, exactly one typed authority, and a
// diagnostic occurred-at time. Trace IDs, delivery attempts, claims, and
// telemetry attributes never enter the business digest.
type PublicationIntentHeader struct {
	SchemaVersion          SchemaVersion
	RequestID              PublicationRequestID
	Operation              Operation
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	ExpectedStreamRevision StreamRevision
	ExpectedHead           ArtifactVersionID
	ActivityGeneration     Generation
	Generation             Generation
	Fence                  Fence
	SafetyEpoch            SafetyEpoch
	Authority              PublicationAuthority
	OccurredAt             Instant
}

func (h PublicationIntentHeader) canonical(kind PublicationIntentKind, payload map[string]any) map[string]any {
	encoded := map[string]any{
		"schema_version":      uint32(h.SchemaVersion),
		"request_id":          string(h.RequestID),
		"operation_id":        string(h.Operation.ID),
		"policy_domain_id":    string(h.PolicyDomainID),
		"task_id":             string(h.TaskID),
		"expected_revision":   uint64(h.ExpectedStreamRevision),
		"expected_head":       string(h.ExpectedHead),
		"activity_generation": uint64(h.ActivityGeneration),
		"generation":          uint64(h.Generation),
		"fence":               uint64(h.Fence),
		"safety_epoch":        uint64(h.SafetyEpoch),
		"authority":           h.Authority.canonical(),
		"intent_kind":         string(kind),
		"payload":             payload,
	}
	return encoded
}

// PublicationIntent is the closed typed mutation union. Only this package
// can implement it; the invariant engine rejects any other value as an
// invalid intent.
type PublicationIntent interface {
	kind() PublicationIntentKind
	header() PublicationIntentHeader
	canonicalPayload() map[string]any
	canonical() map[string]any
	valid() bool
	publicationIntent()
}

type intentBase struct {
	intentHeader PublicationIntentHeader
}

func (i intentBase) header() PublicationIntentHeader { return i.intentHeader }
func (i intentBase) publicationIntent()              {}

// PreparePublication submits a canonical publication request. Prepare
// persists the stable operation identity, request digest, expected
// head/revision, candidate ArtifactVersionID, immutable candidate
// manifest/lineage, and the typed Durable Object staging references before
// any external physical action. The candidate cannot be patched after
// prepare.
type PreparePublication struct {
	intentBase
	ContractID            PublicationContractID
	Kind                  PublicationKind
	Parent                ArtifactVersionID
	PhaseRunID            PhaseRunID
	Members               []ArtifactMemberSpec
	Staging               []StagingReference
	RequiredChannels      []ChannelKind
	RuntimeRefs           []RuntimeEvidenceRef
	ValidationRef         EvidenceRef
	C04CommitRef          EvidenceRef
	ContentCapabilityRefs []ContentCapabilityRef
}

func NewPreparePublication(header PublicationIntentHeader, request PreparePublicationPayload) PublicationIntent {
	return PreparePublication{
		intentBase: intentBase{intentHeader: header},
		ContractID: request.ContractID, Kind: request.Kind, Parent: request.Parent,
		PhaseRunID: request.PhaseRunID,
		Members:    request.Members, Staging: request.Staging,
		RequiredChannels: request.RequiredChannels, RuntimeRefs: request.RuntimeRefs,
		ValidationRef: request.ValidationRef, C04CommitRef: request.C04CommitRef,
		ContentCapabilityRefs: request.ContentCapabilityRefs,
	}
}

type PreparePublicationPayload struct {
	ContractID            PublicationContractID
	Kind                  PublicationKind
	Parent                ArtifactVersionID
	PhaseRunID            PhaseRunID
	Members               []ArtifactMemberSpec
	Staging               []StagingReference
	RequiredChannels      []ChannelKind
	RuntimeRefs           []RuntimeEvidenceRef
	ValidationRef         EvidenceRef
	C04CommitRef          EvidenceRef
	ContentCapabilityRefs []ContentCapabilityRef
}

func (p PreparePublication) kind() PublicationIntentKind { return IntentPreparePublication }

func (p PreparePublication) canonicalPayload() map[string]any {
	members := make([]map[string]any, 0, len(p.Members))
	for _, member := range p.Members {
		members = append(members, map[string]any{
			"slot":           string(member.Slot),
			"kind":           string(member.Kind),
			"logical_name":   member.LogicalName,
			"media_type":     string(member.MediaType),
			"size":           member.Size,
			"content_digest": string(member.ContentDigest),
		})
	}
	staging := make([]map[string]any, 0, len(p.Staging))
	for _, ref := range p.Staging {
		staging = append(staging, ref.canonical())
	}
	channels := make([]string, 0, len(p.RequiredChannels))
	for _, channel := range p.RequiredChannels {
		channels = append(channels, string(channel))
	}
	runtimeRefs := make([]map[string]any, 0, len(p.RuntimeRefs))
	for _, ref := range p.RuntimeRefs {
		runtimeRefs = append(runtimeRefs, ref.canonical())
	}
	capabilityRefs := make([]map[string]any, 0, len(p.ContentCapabilityRefs))
	for _, ref := range p.ContentCapabilityRefs {
		capabilityRefs = append(capabilityRefs, ref.canonical())
	}
	return map[string]any{
		"contract_id":             string(p.ContractID),
		"kind":                    string(p.Kind),
		"parent":                  string(p.Parent),
		"phase_run_id":            string(p.PhaseRunID),
		"members":                 members,
		"staging":                 staging,
		"required_channels":       channels,
		"runtime_refs":            runtimeRefs,
		"validation_ref":          p.ValidationRef.canonical(),
		"c04_commit_ref":          p.C04CommitRef.canonical(),
		"content_capability_refs": capabilityRefs,
	}
}

func (p PreparePublication) canonical() map[string]any {
	return p.header().canonical(p.kind(), p.canonicalPayload())
}

func (p PreparePublication) valid() bool {
	return validPreparePayload(p)
}

// VerifyPublication accepts the exact upstream evidence: Runtime Evidence,
// Platform validation evidence, C04 commit evidence, and one Durable Object
// verified-content capability per member. Verify only accepts evidence
// matching the pinned references and records a replayable verification
// result. The candidate manifest is never patched.
type VerifyPublication struct {
	intentBase
	RuntimeEvidence     []RuntimeEvidence
	ValidationEvidence  ValidationEvidence
	C04CommitEvidence   C04CommitEvidence
	ContentCapabilities []ContentCapabilityEvidence
}

func NewVerifyPublication(header PublicationIntentHeader, payload VerifyPublicationPayload) PublicationIntent {
	return VerifyPublication{
		intentBase:          intentBase{intentHeader: header},
		RuntimeEvidence:     payload.RuntimeEvidence,
		ValidationEvidence:  payload.ValidationEvidence,
		C04CommitEvidence:   payload.C04CommitEvidence,
		ContentCapabilities: payload.ContentCapabilities,
	}
}

type VerifyPublicationPayload struct {
	RuntimeEvidence     []RuntimeEvidence
	ValidationEvidence  ValidationEvidence
	C04CommitEvidence   C04CommitEvidence
	ContentCapabilities []ContentCapabilityEvidence
}

func (v VerifyPublication) kind() PublicationIntentKind { return IntentVerifyPublication }

func (v VerifyPublication) canonicalPayload() map[string]any {
	runtime := make([]any, 0, len(v.RuntimeEvidence))
	for _, evidence := range v.RuntimeEvidence {
		runtime = append(runtime, string(evidence.CanonicalDigest()))
	}
	capabilities := make([]any, 0, len(v.ContentCapabilities))
	for _, capability := range v.ContentCapabilities {
		capabilities = append(capabilities, string(capability.CanonicalDigest()))
	}
	export := any(nil)
	if v.C04CommitEvidence.ValidatedExportEvidence != nil {
		export = string(v.C04CommitEvidence.ValidatedExportEvidence.CanonicalDigest())
	}
	return map[string]any{
		"runtime_evidence":          runtime,
		"validation_evidence":       string(v.ValidationEvidence.CanonicalDigest()),
		"c04_commit_evidence":       string(v.C04CommitEvidence.CanonicalDigest()),
		"content_capabilities":      capabilities,
		"validated_export_evidence": export,
	}
}

func (v VerifyPublication) canonical() map[string]any {
	return v.header().canonical(v.kind(), v.canonicalPayload())
}

func (v VerifyPublication) valid() bool {
	return validVerifyPayload(v)
}

// RejectReason is the closed set of safe, content-free rejection reasons.
type RejectReason string

const (
	RejectEvidenceFailure     RejectReason = "evidence_failure"
	RejectCandidateSuperseded RejectReason = "candidate_superseded"
)

func validRejectReason(reason RejectReason) bool {
	switch reason {
	case RejectEvidenceFailure, RejectCandidateSuperseded:
		return true
	default:
		return false
	}
}

// EvidenceFailure binds the exact evidence that failed verification. It is
// content-free and references only opaque identities and safe failure
// classes.
type EvidenceFailure struct {
	Kind         string
	EvidenceID   EvidenceID
	CapabilityID ContentCapabilityID
}

func (f EvidenceFailure) canonical() map[string]any {
	return map[string]any{
		"kind":          f.Kind,
		"evidence_id":   string(f.EvidenceID),
		"capability_id": string(f.CapabilityID),
	}
}

// RejectPublication is the terminal non-activation decision for an already
// recognized canonical operation. It binds a closed safe reason and an exact
// evidence failure. Rejection never creates an Artifact Version, a member,
// or a current-head mutation.
type RejectPublication struct {
	intentBase
	Reason          RejectReason
	EvidenceFailure *EvidenceFailure
}

func NewRejectPublication(header PublicationIntentHeader, reason RejectReason, failure *EvidenceFailure) PublicationIntent {
	return RejectPublication{intentBase: intentBase{intentHeader: header}, Reason: reason, EvidenceFailure: failure}
}

func (r RejectPublication) kind() PublicationIntentKind { return IntentRejectPublication }

func (r RejectPublication) canonicalPayload() map[string]any {
	failure := map[string]any(nil)
	if r.EvidenceFailure != nil {
		failure = r.EvidenceFailure.canonical()
	}
	return map[string]any{
		"reason":  string(r.Reason),
		"failure": failure,
	}
}

func (r RejectPublication) canonical() map[string]any {
	return r.header().canonical(r.kind(), r.canonicalPayload())
}

func (r RejectPublication) valid() bool {
	return validRejectPayload(r)
}

// CancelReason is the closed set of safe cancellation reasons.
type CancelReason string

const (
	CancelTaskOrchestration CancelReason = "task_orchestration"
	CancelRecovery          CancelReason = "protected_recovery"
)

func validCancelReason(reason CancelReason) bool {
	switch reason {
	case CancelTaskOrchestration, CancelRecovery:
		return true
	default:
		return false
	}
}

// CancelPublication accepts only the exact Task Orchestration or protected
// recovery authority for the current operation with a matching generation
// and fence. Cancel-first linearizes later verify/activate as stale;
// activation-first linearizes cancel as an exact replay of an
// already-terminal result. Cancel never deletes an activated version.
type CancelPublication struct {
	intentBase
	Reason CancelReason
}

func NewCancelPublication(header PublicationIntentHeader, reason CancelReason) PublicationIntent {
	return CancelPublication{intentBase: intentBase{intentHeader: header}, Reason: reason}
}

func (c CancelPublication) kind() PublicationIntentKind { return IntentCancelPublication }

func (c CancelPublication) canonicalPayload() map[string]any {
	return map[string]any{"reason": string(c.Reason)}
}

func (c CancelPublication) canonical() map[string]any {
	return c.header().canonical(c.kind(), c.canonicalPayload())
}

func (c CancelPublication) valid() bool {
	return validCancelPayload(c)
}

// ReconcileMode is the closed set of reconciliation modes. Reconcile only
// inspects or replays the original operation, its evidence, and its
// references; it cannot allocate a new ArtifactVersionID, modify the
// manifest or parent, or create a Task retry.
type ReconcileMode string

const (
	ReconcileInspect              ReconcileMode = "inspect"
	ReconcileCompleteVerification ReconcileMode = "complete_verification"
	ReconcileConfirmCancellation  ReconcileMode = "confirm_cancellation"
	ReconcileConfirmRejection     ReconcileMode = "confirm_rejection"
)

func validReconcileMode(mode ReconcileMode) bool {
	switch mode {
	case ReconcileInspect, ReconcileCompleteVerification,
		ReconcileConfirmCancellation, ReconcileConfirmRejection:
		return true
	default:
		return false
	}
}

// ReconcilePublication inspects or replays the original operation and its
// evidence references. CompleteVerification re-evaluates the already
// recorded evidence set against the current authority registries.
type ReconcilePublication struct {
	intentBase
	Mode ReconcileMode
}

func NewReconcilePublication(header PublicationIntentHeader, mode ReconcileMode) PublicationIntent {
	return ReconcilePublication{intentBase: intentBase{intentHeader: header}, Mode: mode}
}

func (r ReconcilePublication) kind() PublicationIntentKind { return IntentReconcilePublication }

func (r ReconcilePublication) canonicalPayload() map[string]any {
	return map[string]any{"mode": string(r.Mode)}
}

func (r ReconcilePublication) canonical() map[string]any {
	return r.header().canonical(r.kind(), r.canonicalPayload())
}

func (r ReconcilePublication) valid() bool {
	return validReconcilePayload(r)
}

// CanonicalRequestDigest computes the deterministic digest of an intent
// including its operation identity and payload but excluding the digest
// field of the operation binding itself.
func CanonicalRequestDigest(intent PublicationIntent) Digest {
	if intent == nil {
		return Digest("")
	}
	return canonicalDigest(intent.canonical())
}

func validHeader(header PublicationIntentHeader) bool {
	if header.SchemaVersion.Major() != SchemaV1.Major() || header.RequestID == "" ||
		header.Operation.ID == "" || header.PolicyDomainID == "" || header.TaskID == "" ||
		header.ActivityGeneration == 0 || header.Generation == 0 || header.Fence == 0 ||
		header.SafetyEpoch == 0 || header.OccurredAt == 0 {
		return false
	}
	return validMutationAuthority(header.Authority)
}

func validPreparePayload(intent PreparePublication) bool {
	if !validHeader(intent.header()) || !validPublicationKind(intent.Kind) ||
		intent.ContractID == "" || intent.PhaseRunID == "" || len(intent.Members) == 0 || len(intent.Staging) == 0 ||
		len(intent.RequiredChannels) == 0 || len(intent.RuntimeRefs) == 0 ||
		len(intent.ContentCapabilityRefs) == 0 || intent.ValidationRef.EvidenceID == "" ||
		intent.C04CommitRef.EvidenceID == "" {
		return false
	}
	if intent.Kind == PublicationKindFirstGeneration && intent.Parent != "" {
		return false
	}
	if intent.Kind == PublicationKindManualEdit && intent.Parent == "" {
		return false
	}
	if len(intent.RequiredChannels) != len(intent.RuntimeRefs) {
		return false
	}
	if len(intent.Members) != len(intent.Staging) || len(intent.Members) != len(intent.ContentCapabilityRefs) {
		return false
	}
	for _, channel := range intent.RequiredChannels {
		if !validChannelKind(channel) {
			return false
		}
	}
	for _, ref := range intent.RuntimeRefs {
		if !validChannelKind(ref.Channel) || ref.EvidenceID == "" || !validDigest(ref.Digest) {
			return false
		}
	}
	if !validDigest(intent.ValidationRef.Digest) || !validDigest(intent.C04CommitRef.Digest) {
		return false
	}
	slots := make(map[MemberSlotID]bool, len(intent.Members))
	for _, member := range intent.Members {
		if member.Slot == "" || !validArtifactKind(member.Kind) ||
			!validMediaType(member.Kind, member.MediaType) || !validDigest(member.ContentDigest) {
			return false
		}
		if _, duplicate := slots[member.Slot]; duplicate {
			return false
		}
		slots[member.Slot] = true
		if _, ok := normalizedLogicalName(member.LogicalName); !ok {
			return false
		}
	}
	stagedSlots := make(map[MemberSlotID]bool, len(intent.Staging))
	for _, ref := range intent.Staging {
		if ref.MemberSlot == "" || ref.ContentID == "" || !validDigest(ref.ContentDigest) ||
			!validContentPurpose(ref.Purpose) || ref.AdapterID == "" {
			return false
		}
		if !slots[ref.MemberSlot] {
			return false
		}
		if _, duplicate := stagedSlots[ref.MemberSlot]; duplicate {
			return false
		}
		stagedSlots[ref.MemberSlot] = true
	}
	for slot := range slots {
		if !stagedSlots[slot] {
			return false
		}
	}
	capabilitySlots := make(map[MemberSlotID]bool, len(intent.ContentCapabilityRefs))
	for _, ref := range intent.ContentCapabilityRefs {
		if ref.MemberSlot == "" || ref.CapabilityID == "" || !validDigest(ref.Digest) {
			return false
		}
		if !slots[ref.MemberSlot] {
			return false
		}
		if _, duplicate := capabilitySlots[ref.MemberSlot]; duplicate {
			return false
		}
		capabilitySlots[ref.MemberSlot] = true
	}
	for slot := range slots {
		if !capabilitySlots[slot] {
			return false
		}
	}
	return true
}

func validVerifyPayload(intent VerifyPublication) bool {
	if !validHeader(intent.header()) {
		return false
	}
	if len(intent.RuntimeEvidence) == 0 || len(intent.ContentCapabilities) == 0 {
		return false
	}
	if intent.ValidationEvidence.ID == "" || intent.C04CommitEvidence.ID == "" {
		return false
	}
	for _, evidence := range intent.RuntimeEvidence {
		if evidence.ID == "" || !validDigest(evidence.Digest) || !validDigest(evidence.OutputProposalManifestDigest) ||
			evidence.Outcome == "" || !validChannelKind(evidence.Channel) ||
			evidence.Producer.AuthorityID == "" || evidence.Producer.Generation == 0 {
			return false
		}
	}
	if intent.ValidationEvidence.Producer.AuthorityID == "" || intent.ValidationEvidence.Producer.Generation == 0 ||
		intent.ValidationEvidence.ContractID == "" || intent.ValidationEvidence.Decision == "" {
		return false
	}
	if intent.C04CommitEvidence.Producer.AuthorityID == "" || intent.C04CommitEvidence.Producer.Generation == 0 ||
		intent.C04CommitEvidence.TaskWorkspaceID == "" || intent.C04CommitEvidence.RevisionID == "" ||
		intent.C04CommitEvidence.CheckpointID == "" || intent.C04CommitEvidence.ValidationEvidenceID == "" {
		return false
	}
	for _, capability := range intent.ContentCapabilities {
		if capability.ID == "" || !validDigest(capability.Digest) || capability.Producer.AuthorityID == "" ||
			capability.Producer.Generation == 0 || capability.ContentID == "" || capability.MemberSlot == "" ||
			!validDigest(capability.ContentDigest) || !validContentPurpose(capability.Purpose) ||
			!validWriteIntent(capability.WriteIntent) || !validVerificationMethod(capability.VerificationMethod) ||
			capability.AdapterID == "" {
			return false
		}
	}
	return true
}

func validRejectPayload(intent RejectPublication) bool {
	return validHeader(intent.header()) && validRejectReason(intent.Reason)
}

func validCancelPayload(intent CancelPublication) bool {
	return validHeader(intent.header()) && validCancelReason(intent.Reason)
}

func validReconcilePayload(intent ReconcilePublication) bool {
	return validHeader(intent.header()) && validReconcileMode(intent.Mode)
}
