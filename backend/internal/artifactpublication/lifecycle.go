package artifactpublication

import (
	"context"
	"sort"
	"strings"
)

// verify accepts the exact upstream evidence for the pinned candidate and
// records a replayable verification result. The candidate manifest is never
// patched; a changed evidence binding requires a new request/operation.
func (m *inMemory) verify(
	ctx context.Context,
	intent VerifyPublication,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := m.injectFault(FaultBeforeEvidenceAcceptance, operationID, IntentVerifyPublication, ""); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	if err := m.ensureAuthority(header.Authority, IntentVerifyPublication); err != nil {
		return PublicationDecision{}, err
	}
	if record.candidate == nil {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	if header.Generation != record.generation || header.Fence != record.fence {
		// Stale generation/fence on the verification intent fails closed.
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	switch record.state {
	case OperationCancelled, OperationRejected, OperationActivated:
		// Late verify on an already terminal operation stays stale/terminal.
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationVerified:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	}

	result, ambiguous, failure, err := m.evaluateEvidence(record.candidate, header, intent)
	if err != nil {
		// Retryable/unavailable faults are not recorded: a retry with the
		// same payload re-runs instead of replaying a transient failure.
		return PublicationDecision{}, err
	}
	if err := m.injectFault(FaultBeforeVerificationResult, operationID, IntentVerifyPublication, ""); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}

	accepted := make([]evidenceAcceptedRecord, 0, len(result.accepted))
	accepted = append(accepted, result.accepted...)
	if ambiguous {
		record.verification = &verificationRecord{
			state: VerificationAmbiguous, accepted: accepted,
			pendingCapabilitySlots: result.pendingSlots,
		}
		record.state = OperationReconciliationRequired
	} else if failure != nil {
		// Determinate evidence failure: the caller decides reject/cancel.
		// The failed attempt is recorded so exact replay returns the same
		// outcome; the operation stays non-terminal.
		record.verification = &verificationRecord{state: VerificationFailed, failure: failure}
		record.state = OperationPrepared
		verifyErr := &Error{Code: failureErrorCode(failure.Kind)}
		decision := decisionForRecord(record, false, m.now())
		m.recordOutcome(record, IntentVerifyPublication, digest, record.state, decision, verifyErr)
		if err := m.injectFault(FaultBeforeResponse, operationID, IntentVerifyPublication, ""); err != nil {
			return PublicationDecision{}, normalizeError(err)
		}
		return decision, verifyErr
	} else {
		record.verification = &verificationRecord{state: VerificationVerified, accepted: accepted}
		record.state = OperationVerified
	}
	decision := decisionForRecord(record, false, m.now())
	m.recordOutcome(record, IntentVerifyPublication, digest, record.state, decision, nil)

	if err := m.injectFault(FaultBeforeResponse, operationID, IntentVerifyPublication, ""); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	return decision, nil
}

// verificationEvaluation is the intermediate result of evidence evaluation.
type verificationEvaluation struct {
	accepted     []evidenceAcceptedRecord
	pendingSlots []MemberSlotID
}

// evaluateEvidence runs the closed evidence matrix: producer, scope,
// operation, manifest/member facts, policy domain, generation, fence, and
// safety epoch are checked item by item; missing, extra, partial,
// cross-scope, or unknown evidence fails closed. The returned failure binds
// the exact evidence that failed; ambiguous returns true when a Durable
// Object capability is not currently resolvable and reconciliation is
// required.
func (m *inMemory) evaluateEvidence(
	candidate *candidateRecord,
	header PublicationIntentHeader,
	intent VerifyPublication,
) (verificationEvaluation, bool, *EvidenceFailure, error) {
	evaluation := verificationEvaluation{}
	candidateBySlot := make(map[MemberSlotID]memberRecord, len(candidate.members))
	for _, member := range candidate.members {
		candidateBySlot[member.slot] = member
	}

	// ---- Runtime Evidence: one exact record per required channel ----
	if len(intent.RuntimeEvidence) != len(candidate.requiredChannels) {
		return evaluation, false, &EvidenceFailure{Kind: "runtime_evidence_count"}, nil
	}
	runtimeByChannel := make(map[ChannelKind]RuntimeEvidence, len(intent.RuntimeEvidence))
	for _, evidence := range intent.RuntimeEvidence {
		if existing, duplicate := runtimeByChannel[evidence.Channel]; duplicate && existing.ID != evidence.ID {
			return evaluation, false, &EvidenceFailure{Kind: "duplicate_runtime_evidence"}, nil
		}
		runtimeByChannel[evidence.Channel] = evidence
	}
	proposalDigests := make([]Digest, 0, len(intent.RuntimeEvidence))
	for _, ref := range candidate.runtimeRefs {
		evidence, ok := runtimeByChannel[ref.Channel]
		if !ok {
			return evaluation, false, &EvidenceFailure{Kind: "runtime_evidence_missing", EvidenceID: ref.EvidenceID}, nil
		}
		if evidence.ID != ref.EvidenceID || evidence.Digest != ref.Digest {
			return evaluation, false, &EvidenceFailure{Kind: "runtime_evidence_mismatch", EvidenceID: evidence.ID}, nil
		}
		if !validDigest(evidence.Digest) || evidence.Digest != evidence.CanonicalDigest() {
			return evaluation, false, &EvidenceFailure{Kind: "runtime_evidence_corrupt", EvidenceID: evidence.ID}, nil
		}
		if evidence.Producer.AuthorityID != m.config.RuntimeAuthorityID ||
			evidence.PolicyDomainID != header.PolicyDomainID || evidence.TaskID != header.TaskID ||
			evidence.PhaseRunID != candidate.phaseRunID || evidence.SafetyEpoch != header.SafetyEpoch {
			return evaluation, false, &EvidenceFailure{Kind: "runtime_evidence_scope", EvidenceID: evidence.ID}, nil
		}
		if evidence.Outcome != "completed" {
			return evaluation, false, &EvidenceFailure{Kind: "runtime_evidence_outcome", EvidenceID: evidence.ID}, nil
		}
		evaluation.accepted = append(evaluation.accepted, evidenceAcceptedRecord{
			kind: "runtime", evidenceID: evidence.ID, digest: evidence.Digest,
		})
		proposalDigests = append(proposalDigests, evidence.OutputProposalManifestDigest)
	}
	// Every required runtime channel must reference the same output-proposal
	// manifest facts; the validation evidence must accept that same manifest.
	referenceProposal := proposalDigests[0]
	for _, digest := range proposalDigests[1:] {
		if digest != referenceProposal {
			return evaluation, false, &EvidenceFailure{Kind: "runtime_evidence_manifest"}, nil
		}
	}

	// ---- Validation Evidence: accepts the exact contract and binds every
	// required Runtime Evidence plus the same output-proposal manifest ----
	validation := intent.ValidationEvidence
	if validation.ID != candidate.validationRef.EvidenceID || validation.Digest != candidate.validationRef.Digest {
		return evaluation, false, &EvidenceFailure{Kind: "validation_evidence_mismatch", EvidenceID: validation.ID}, nil
	}
	if !validDigest(validation.Digest) || validation.Digest != validation.CanonicalDigest() {
		return evaluation, false, &EvidenceFailure{Kind: "validation_evidence_corrupt", EvidenceID: validation.ID}, nil
	}
	if validation.Producer.AuthorityID != m.config.ValidationAuthorityID ||
		validation.PolicyDomainID != header.PolicyDomainID || validation.TaskID != header.TaskID ||
		validation.ContractID != candidate.contractID || validation.SafetyEpoch != header.SafetyEpoch {
		return evaluation, false, &EvidenceFailure{Kind: "validation_evidence_scope", EvidenceID: validation.ID}, nil
	}
	if validation.OutputProposalManifestDigest != referenceProposal {
		return evaluation, false, &EvidenceFailure{Kind: "validation_evidence_manifest", EvidenceID: validation.ID}, nil
	}
	if !sameEvidenceRefSet(validation.RuntimeEvidenceRefs, runtimeEvidenceRefs(intent.RuntimeEvidence)) {
		return evaluation, false, &EvidenceFailure{Kind: "validation_evidence_binding", EvidenceID: validation.ID}, nil
	}
	evaluation.accepted = append(evaluation.accepted, evidenceAcceptedRecord{
		kind: "validation", evidenceID: validation.ID, digest: validation.Digest,
	})

	// ---- C04 commit evidence: binds the exact validation evidence, the
	// resulting Revision/Checkpoint, and the declared-state manifest ----
	commit := intent.C04CommitEvidence
	if commit.ID != candidate.c04Ref.EvidenceID || commit.Digest != candidate.c04Ref.Digest {
		return evaluation, false, &EvidenceFailure{Kind: "c04_evidence_mismatch", EvidenceID: commit.ID}, nil
	}
	if !validDigest(commit.Digest) || commit.Digest != commit.CanonicalDigest() {
		return evaluation, false, &EvidenceFailure{Kind: "c04_evidence_corrupt", EvidenceID: commit.ID}, nil
	}
	if commit.Producer.AuthorityID != m.config.C04AuthorityID ||
		commit.PolicyDomainID != header.PolicyDomainID || commit.TaskID != header.TaskID ||
		commit.ValidationEvidenceID != validation.ID ||
		commit.ValidationEvidenceDigest != validation.Digest ||
		commit.SafetyEpoch != header.SafetyEpoch {
		return evaluation, false, &EvidenceFailure{Kind: "c04_evidence_scope", EvidenceID: commit.ID}, nil
	}
	if commit.ContentEvidenceRoot == "" || commit.DurabilityEvidenceRoot == "" {
		return evaluation, false, &EvidenceFailure{Kind: "c04_evidence_roots", EvidenceID: commit.ID}, nil
	}
	if candidate.kind == PublicationKindManualEdit {
		if commit.ValidatedExportEvidence == nil {
			return evaluation, false, &EvidenceFailure{Kind: "c04_export_missing", EvidenceID: commit.ID}, nil
		}
		export := commit.ValidatedExportEvidence
		if export.PublicationAuthorityID != m.config.C04AuthorityID ||
			export.SourceArtifactVersionID != candidate.parent ||
			export.PolicyDomainID != header.PolicyDomainID || export.TaskID != header.TaskID ||
			export.ValidationEvidenceID != validation.ID ||
			// The export must bind the same new Revision/Checkpoint the C04
			// commit evidence binds: the child cannot reconstruct from an
			// arbitrary workspace revision (SPEC #105 manual-edit child
			// must match the new Revision/Checkpoint).
			export.RevisionID != commit.RevisionID || export.CheckpointID != commit.CheckpointID ||
			!validDigest(export.Digest) || export.Digest != export.CanonicalDigest() {
			return evaluation, false, &EvidenceFailure{Kind: "c04_export_scope", EvidenceID: commit.ID}, nil
		}
	}
	evaluation.accepted = append(evaluation.accepted, evidenceAcceptedRecord{
		kind: "c04_commit", evidenceID: commit.ID, digest: commit.Digest,
	})

	// ---- Durable Object verified-content capabilities: one exact
	// capability per member, matching the staged reference and the pinned
	// capability reference, and currently valid in the authority registry ----
	if len(intent.ContentCapabilities) != len(candidate.members) {
		return evaluation, false, &EvidenceFailure{Kind: "capability_count"}, nil
	}
	capabilityBySlot := make(map[MemberSlotID]ContentCapabilityEvidence, len(intent.ContentCapabilities))
	for _, capability := range intent.ContentCapabilities {
		if existing, duplicate := capabilityBySlot[capability.MemberSlot]; duplicate && existing.ID != capability.ID {
			return evaluation, false, &EvidenceFailure{Kind: "duplicate_capability"}, nil
		}
		capabilityBySlot[capability.MemberSlot] = capability
	}
	ambiguousSlots := make([]MemberSlotID, 0)
	for _, ref := range candidate.capabilityRefs {
		capability, ok := capabilityBySlot[ref.MemberSlot]
		if !ok {
			return evaluation, false, &EvidenceFailure{Kind: "capability_missing", CapabilityID: ref.CapabilityID}, nil
		}
		member := candidateBySlot[ref.MemberSlot]
		staged := candidate.stagingBySlot(ref.MemberSlot)
		if staged == nil {
			return evaluation, false, &EvidenceFailure{Kind: "capability_staging", CapabilityID: capability.ID}, nil
		}
		if capability.ID != ref.CapabilityID || capability.Digest != ref.Digest {
			return evaluation, false, &EvidenceFailure{Kind: "capability_mismatch", CapabilityID: capability.ID}, nil
		}
		if !validDigest(capability.Digest) || capability.Digest != capability.CanonicalDigest() {
			return evaluation, false, &EvidenceFailure{Kind: "capability_corrupt", CapabilityID: capability.ID}, nil
		}
		if capability.Producer.AuthorityID != m.config.DurableObjectAuthorityID ||
			capability.PolicyDomainID != header.PolicyDomainID ||
			capability.Purpose != ContentPurposePublicationMember ||
			capability.ContentID != staged.contentID ||
			capability.ContentDigest != member.contentDigest || capability.Size != member.size ||
			capability.WriteIntent != WriteIntentImmutable ||
			capability.VerificationMethod != VerificationMethodReceiptBound ||
			capability.AdapterID != staged.adapterID ||
			capability.SafetyEpoch != header.SafetyEpoch {
			return evaluation, false, &EvidenceFailure{Kind: "capability_facts", CapabilityID: capability.ID}, nil
		}
		current, currentOK := m.currentContentCapability(capability.ID)
		if !currentOK {
			// The capability is not currently resolvable in the Durable
			// Object authority: durability is unverified and the operation
			// requires reconciliation (never a silent success or failure).
			ambiguousSlots = append(ambiguousSlots, ref.MemberSlot)
			continue
		}
		if current.Digest != capability.Digest {
			return evaluation, false, &EvidenceFailure{Kind: "capability_stale", CapabilityID: capability.ID}, nil
		}
		evaluation.accepted = append(evaluation.accepted, evidenceAcceptedRecord{
			kind: "content_capability", evidenceID: EvidenceID(capability.ID), digest: capability.Digest,
		})
	}
	if len(ambiguousSlots) > 0 {
		return evaluation, true, nil, nil
	}
	return evaluation, false, nil, nil
}

func failureErrorCode(kind string) ErrorCode {
	switch {
	case strings.HasSuffix(kind, "_missing"), kind == "runtime_evidence_count", kind == "capability_count":
		return ErrorEvidenceMissing
	case strings.HasSuffix(kind, "_corrupt"):
		return ErrorEvidenceCorrupt
	default:
		return ErrorIntegrityFailure
	}
}

func runtimeEvidenceRefs(evidence []RuntimeEvidence) []EvidenceRef {
	refs := make([]EvidenceRef, 0, len(evidence))
	for _, item := range evidence {
		refs = append(refs, EvidenceRef{EvidenceID: item.ID, Digest: item.Digest})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].EvidenceID < refs[j].EvidenceID })
	return refs
}

func sameEvidenceRefSet(a, b []EvidenceRef) bool {
	sortedA := append([]EvidenceRef(nil), a...)
	sortedB := append([]EvidenceRef(nil), b...)
	sort.Slice(sortedA, func(i, j int) bool { return sortedA[i].EvidenceID < sortedA[j].EvidenceID })
	sort.Slice(sortedB, func(i, j int) bool { return sortedB[i].EvidenceID < sortedB[j].EvidenceID })
	if len(sortedA) != len(sortedB) {
		return false
	}
	for index := range sortedA {
		if sortedA[index] != sortedB[index] {
			return false
		}
	}
	return true
}

// reject is the terminal non-activation decision for an already recognized
// canonical operation. It never creates an Artifact Version, a member, or a
// current-head mutation, and it records the typed staging references as
// C05-owned publication residue.
func (m *inMemory) reject(
	ctx context.Context,
	intent RejectPublication,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := m.ensureAuthority(header.Authority, IntentRejectPublication); err != nil {
		return PublicationDecision{}, err
	}
	switch record.state {
	case OperationCancelled:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationRejected:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationActivated:
		// Rejection after an atomic activation cannot undo the committed
		// version; the operation is already terminal-active.
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	}
	// Reject, like cancel, accepts only the exact current operation
	// generation and fence; a stale reject fails closed.
	if header.Generation != record.generation || header.Fence != record.fence {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	record.rejectReason = intent.Reason
	record.state = OperationRejected
	record.residue = m.releaseResidue(record, header)
	decision := decisionForRecord(record, false, m.now())
	m.recordOutcome(record, IntentRejectPublication, digest, record.state, decision, nil)

	if err := m.injectFault(FaultBeforeResponse, operationID, IntentRejectPublication, ""); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	return decision, nil
}

// cancel accepts only the exact Task Orchestration or protected recovery
// authority for the current operation with a matching generation and fence.
// Cancel-first linearizes later verify/activate as stale; the operation
// becomes terminal and its staging references become residue.
func (m *inMemory) cancel(
	ctx context.Context,
	intent CancelPublication,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := m.ensureAuthority(header.Authority, IntentCancelPublication); err != nil {
		return PublicationDecision{}, err
	}
	if header.Authority.Kind == AuthorityRecovery && intent.Reason != CancelRecovery {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	if header.Authority.Kind == AuthorityTaskOrchestration && intent.Reason != CancelTaskOrchestration {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	switch record.state {
	case OperationRejected:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationCancelled:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationActivated:
		// Activation-first linearization: the operation is already terminal
		// with an active Artifact Version. Cancel returns the existing
		// active terminal result and must never delete the version or
		// release its references as residue.
		decision := decisionForRecord(record, true, m.now())
		return decision, nil
	}
	if header.Generation != record.generation || header.Fence != record.fence {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	record.cancelReason = intent.Reason
	record.state = OperationCancelled
	record.residue = m.releaseResidue(record, header)
	decision := decisionForRecord(record, false, m.now())
	m.recordOutcome(record, IntentCancelPublication, digest, record.state, decision, nil)

	if err := m.injectFault(FaultBeforeResponse, operationID, IntentCancelPublication, ""); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	return decision, nil
}

// releaseResidue records the typed staging references as C05-owned
// publication residue with a content-free release intent. It is the only
// residue transition in the canonical core; physical release is owned by the
// Durable Object authority.
func (m *inMemory) releaseResidue(record *operationRecord, header PublicationIntentHeader) *residueRecord {
	if record.candidate == nil {
		return nil
	}
	return &residueRecord{
		operationID: record.operationID, policyDomainID: header.PolicyDomainID,
		taskID: header.TaskID, owner: header.Authority.Kind,
		generation: record.generation, fence: record.fence,
		releaseIntent: "release_staging", stagingRefs: cloneStaging(record.candidate.staging),
		occurredAt: m.now(),
	}
}

func cloneStaging(staging []stagingRecord) []stagingRecord {
	return append([]stagingRecord(nil), staging...)
}

// reconcile only inspects or replays the original operation and its evidence
// references. It cannot allocate a new ArtifactVersionID, modify the
// manifest or parent, or create a Task retry.
func (m *inMemory) reconcile(
	ctx context.Context,
	intent ReconcilePublication,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	if err := m.ensureAuthority(header.Authority, IntentReconcilePublication); err != nil {
		return PublicationDecision{}, err
	}
	switch intent.Mode {
	case ReconcileInspect:
		record.reconcileMode = ReconcileInspect
		decision := decisionForRecord(record, false, m.now())
		m.recordOutcome(record, IntentReconcilePublication, digest, record.state, decision, nil)
		return decision, nil

	case ReconcileCompleteVerification:
		if record.state == OperationReconciliationRequired && record.verification != nil {
			resolved, failure := m.completeVerification(record)
			if failure != nil {
				record.verification.failure = failure
				record.verification.state = VerificationFailed
				record.state = OperationPrepared
			} else if resolved {
				record.verification.state = VerificationVerified
				record.state = OperationVerified
			} else {
				record.verification.state = VerificationAmbiguous
				record.state = OperationReconciliationRequired
			}
		}
		record.reconcileMode = ReconcileCompleteVerification
		decision := decisionForRecord(record, false, m.now())
		m.recordOutcome(record, IntentReconcilePublication, digest, record.state, decision, nil)
		return decision, nil

	case ReconcileConfirmCancellation:
		if record.state != OperationCancelled {
			return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
		}
		record.reconcileMode = ReconcileConfirmCancellation
		decision := decisionForRecord(record, false, m.now())
		m.recordOutcome(record, IntentReconcilePublication, digest, record.state, decision, nil)
		return decision, nil

	case ReconcileConfirmRejection:
		if record.state != OperationRejected {
			return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
		}
		record.reconcileMode = ReconcileConfirmRejection
		decision := decisionForRecord(record, false, m.now())
		m.recordOutcome(record, IntentReconcilePublication, digest, record.state, decision, nil)
		return decision, nil

	default:
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
}

// completeVerification re-evaluates the recorded pending capabilities
// against the current authority registry. It returns resolved=true only when
// every pending capability is now current and matches the pinned reference.
func (m *inMemory) completeVerification(record *operationRecord) (bool, *EvidenceFailure) {
	candidate := record.candidate
	if candidate == nil {
		return false, &EvidenceFailure{Kind: "candidate_missing"}
	}
	refBySlot := make(map[MemberSlotID]ContentCapabilityRef, len(candidate.capabilityRefs))
	for _, ref := range candidate.capabilityRefs {
		refBySlot[ref.MemberSlot] = ref
	}
	pending := record.verification.pendingCapabilitySlots
	if len(pending) == 0 {
		pending = make([]MemberSlotID, 0, len(candidate.capabilityRefs))
		for _, ref := range candidate.capabilityRefs {
			pending = append(pending, ref.MemberSlot)
		}
	}
	for _, slot := range pending {
		ref := refBySlot[slot]
		current, ok := m.currentContentCapability(ref.CapabilityID)
		if !ok {
			return false, nil // still ambiguous: not resolvable
		}
		if current.Digest != ref.Digest {
			return false, &EvidenceFailure{Kind: "capability_stale", CapabilityID: ref.CapabilityID}
		}
	}
	return true, nil
}

func (c *candidateRecord) stagingBySlot(slot MemberSlotID) *stagingRecord {
	for index := range c.staging {
		if c.staging[index].slot == slot {
			return &c.staging[index]
		}
	}
	return nil
}
