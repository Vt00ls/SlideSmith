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
// non-activated operation reaches a terminal disposition. It records the
// typed staging references to release, the owning module, generation/fence,
// and a content-free estimate. Residue is not an Artifact Version and is
// never visible to ordinary queries.
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
	rejectReason       RejectReason
	cancelReason       CancelReason
	reconcileMode      ReconcileMode
	residue            *residueRecord
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
