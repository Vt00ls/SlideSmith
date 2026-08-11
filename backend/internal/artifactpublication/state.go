package artifactpublication

// operationScope identifies one operation within one Task in one policy
// domain. All retries reference the original operation identity.
type operationScope struct {
	policyDomainID PolicyDomainID
	taskID         TaskID
	operationID    PublicationOperationID
}

// taskScope identifies one Task's publication stream.
type taskScope struct {
	policyDomainID PolicyDomainID
	taskID         TaskID
}

// streamRecord is the explicit per-Task publication stream. The current
// head is an explicit pointer maintained by CAS; it is never inferred from
// time, ID order, version strings, row insertion, or file discovery.
type streamRecord struct {
	policyDomainID PolicyDomainID
	taskID         TaskID
	revision       StreamRevision
	currentHead    ArtifactVersionID
}

// memberRecord is the persisted canonical member fact of a candidate.
type memberRecord struct {
	slot          MemberSlotID
	artifactID    ArtifactID
	kind          ArtifactKind
	logicalName   string
	mediaType     MediaType
	size          uint64
	contentDigest Digest
}

// stagingRecord is one persisted typed Durable Object staging reference.
type stagingRecord struct {
	slot               MemberSlotID
	contentID          ContentID
	contentDigest      Digest
	size               uint64
	purpose            ContentPurpose
	physicalGeneration uint64
	adapterID          AdapterID
}

// candidateRecord is the immutable candidate persisted at prepare. It can
// never be patched after prepare; any member, parent, contract, or evidence
// binding change requires a new request and a new operation.
type candidateRecord struct {
	versionID        ArtifactVersionID
	schemaVersion    SchemaVersion
	kind             PublicationKind
	parent           ArtifactVersionID
	contractID       PublicationContractID
	phaseRunID       PhaseRunID
	members          []memberRecord
	staging          []stagingRecord
	runtimeRefs      []RuntimeEvidenceRef
	validationRef    EvidenceRef
	c04Ref           EvidenceRef
	capabilityRefs   []ContentCapabilityRef
	requiredChannels []ChannelKind
	manifestDigest   Digest
	lineageDigest    Digest
}

// activatedRecord is the immutable committed Artifact Version created by
// atomic activation. It is a snapshot of the candidate at the linearization
// point: members, manifest digest, and lineage digest are copied and can
// never be modified or deleted by any ordinary operation. It also records
// the committed publication-stream revision and the activation evidence
// bound to the operation. Physical retention follows ADR 0012; this SPEC
// has no ordinary deletion mutation.
type activatedRecord struct {
	versionID      ArtifactVersionID
	schemaVersion  SchemaVersion
	policyDomainID PolicyDomainID
	taskID         TaskID
	kind           PublicationKind
	parent         ArtifactVersionID
	contractID     PublicationContractID
	phaseRunID     PhaseRunID
	operationID    PublicationOperationID
	streamRevision StreamRevision
	manifestDigest Digest
	lineageDigest  Digest
	members        []memberRecord
	evidence       *PublicationEvidence
	occurredAt     Instant
}

// evidenceAcceptedRecord is one accepted evidence reference.
type evidenceAcceptedRecord struct {
	kind       string
	evidenceID EvidenceID
	digest     Digest
}

// verificationRecord is the replayable verification result.
type verificationRecord struct {
	state    VerificationState
	accepted []evidenceAcceptedRecord
	failure  *EvidenceFailure
	// pendingCapabilitySlots records the member slots whose Durable Object
	// capability was ambiguous (not currently resolvable) so reconcile can
	// re-evaluate them against the current authority registry.
	pendingCapabilitySlots []MemberSlotID
}

// residueRecord is the durable C05-owned publication residue created when a
// non-activated operation reaches a terminal disposition. It is recorded
// durably BEFORE any physical release action (child SPEC #108), together
// with the owner, the opaque typed staging references, the operation,
// generation/fence, expiry, retry state and the release disposition. It
// optionally carries the opaque C05-owned publication assembly resource and
// the C05-owned Cleanup Debt identity when C05 actually created a physical
// assembly resource; a staging-only residue never mints a DebtID. Residue
// is not an Artifact Version and is never visible to ordinary queries.
type residueRecord struct {
	operationID    PublicationOperationID
	policyDomainID PolicyDomainID
	taskID         TaskID
	owner          EvidenceAuthorityKind
	generation     Generation
	fence          Fence
	releaseIntent  string
	stagingRefs    []stagingRecord
	occurredAt     Instant
	// C05-05: durable residue lifecycle. expiry is the retention window
	// recorded at creation; passing it marks the residue expired but never
	// guesses absence. disposition is the closed release disposition;
	// requiresReconciliation is set only by an ambiguous Durable Object
	// receipt and cleared by an evidence-backed receipt.
	expiry                 Instant
	disposition            ResidueDisposition
	requiresReconciliation bool
	attemptCount           uint64
	consecutiveFailures    uint64
	nextRetryAt            Instant
	claimGeneration        Generation
	claimFence             Fence
	lastErrorCategory      ResidueErrorCategory
	releaseReceipt         *ReleaseReceipt
	assembly               *assemblyResource
	debtID                 CleanupDebtID
}

// cleanupDebtRecord is the durable C05-owned Cleanup Debt for one physical
// publication assembly resource C05 actually created. It is minted once per
// operation/resource (no duplicate DebtID), persisted before the first
// physical cleanup attempt, and supports claim, retry, backoff, blocker and
// safe error. It closes only on evidence-backed Reclaimed/AlreadyAbsent/
// RetainedByAuthority or an audited AcceptedException; path disappearance,
// empty directories, object listings, logs, metrics and operator assertions
// can never close it.
type cleanupDebtRecord struct {
	debtID                CleanupDebtID
	revision              uint64
	policyDomainID        PolicyDomainID
	taskID                TaskID
	operationID           PublicationOperationID
	owner                 EvidenceAuthorityKind
	resourceRef           string
	resourceDigest        Digest
	resourceGeneration    uint64
	resourceFence         uint64
	status                CleanupDebtStatus
	createdAt             Instant
	eligibleAt            Instant
	firstAttemptAt        Instant
	lastAttemptAt         Instant
	nextRetryAt           Instant
	attemptCount          uint64
	consecutiveFailures   uint64
	claimGeneration       Generation
	claimFence            Fence
	retryDisposition      CleanupRetryDisposition
	lastErrorCategory     ResidueErrorCategory
	blockers              CleanupBlockerClass
	resolvedAt            Instant
	resolutionClass       CleanupDebtResolutionClass
	resolutionReason      CleanupResolutionReason
	resolutionEvidence    *CleanupResolutionEvidence
	resolutionAuditFactID string
	resolutionApprovalRef string
	resolutionExpiresAt   Instant
}

// intentOutcome is the replayable outcome of one intent submission for one
// operation. A resubmission with the same intent digest replays it; a
// different digest for the same intent kind is a durable integrity conflict.
type intentOutcome struct {
	digest     Digest
	state      PublicationOperationState
	decision   PublicationDecision
	err        *Error
	recordedAt Instant
}

// operationRecord is the durable record of one publication operation and
// every intent outcome that entered its lifecycle.
type operationRecord struct {
	scope              operationScope
	operationID        PublicationOperationID
	requestDigest      Digest
	state              PublicationOperationState
	streamRevision     StreamRevision
	generation         Generation
	fence              Fence
	safetyEpoch        SafetyEpoch
	activityGeneration Generation
	occurredAt         Instant
	candidate          *candidateRecord
	verification       *verificationRecord
	activationEvidence *PublicationEvidence
	rejectReason       RejectReason
	cancelReason       CancelReason
	reconcileMode      ReconcileMode
	residue            *residueRecord
	debt               *cleanupDebtRecord
	integrityConflict  bool
	outcomes           map[PublicationIntentKind]map[Digest]intentOutcome
}

// contentFact is the persisted allocation fact that proves ArtifactVersionID
// and ArtifactID identities are opaque and never reused.
type contentFact struct {
	policyDomainID PolicyDomainID
	taskID         TaskID
	versionID      ArtifactVersionID
}
