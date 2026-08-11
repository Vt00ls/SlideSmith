package artifactpublication

import (
	"context"
	"sort"
)

// activate is the single business linearization point (SPEC #105): it
// revalidates the exact expected publication-stream revision and expected
// current head, commits the immutable Artifact Version (members, manifest,
// lineage), advances the stream revision/current head, marks the operation
// terminal-activated, and returns the committed publication evidence bound
// to the OperationID, ArtifactVersionID, manifest digest, Phase Run
// generation/fence, activity generation, and safety epoch. All of this
// happens atomically under the authority lock; there is no remote I/O.
//
// Activation never patches the candidate, never selects a new parent, and
// never accepts evidence. A stale expected revision/head, a stale
// generation/fence, or a non-verified operation fails closed and the
// candidate stays non-active with a typed stale/conflict disposition.
func (m *inMemory) activate(
	ctx context.Context,
	intent ActivatePublication,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := m.ensureAuthority(header.Authority, IntentActivatePublication); err != nil {
		return PublicationDecision{}, err
	}
	if record.candidate == nil {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	if header.Generation != record.generation || header.Fence != record.fence {
		// A stale generation/fence on the activation intent fails closed:
		// late work from an older epoch can never advance the stream.
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	switch record.state {
	case OperationCancelled, OperationRejected, OperationReconciliationRequired:
		// Late activation on an already terminal operation stays
		// stale/terminal.
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationActivated:
		// Activation is terminal; a second activation with a different
		// payload cannot change the committed version. (The exact same
		// digest is replayed earlier by the outcomes map.)
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationPrepared:
		// Activation requires a verified candidate: evidence must have been
		// accepted before any version can be committed.
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	case OperationVerified:
		// fall through to the CAS revalidation.
	default:
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}

	// ---- CAS revalidation against the explicit stream facts ----
	taskKey := taskScope{policyDomainID: header.PolicyDomainID, taskID: header.TaskID}
	stream := m.persistence.streams[taskKey]
	if stream == nil {
		// No stream has ever been committed; activation requires the
		// expected revision/head to match this empty state.
		stream = &streamRecord{policyDomainID: header.PolicyDomainID, taskID: header.TaskID}
	}
	if header.ExpectedStreamRevision != stream.revision || header.ExpectedHead != stream.currentHead {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	candidate := record.candidate
	if candidate.kind == PublicationKindFirstGeneration {
		// First-generation activation requires no parent and an empty
		// expected head: a first version can only be committed onto an
		// empty stream.
		if candidate.parent != "" || header.ExpectedHead != "" {
			return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
		}
	}
	if candidate.kind == PublicationKindManualEdit {
		// Manual-edit activation requires the exact activated parent to be
		// the current head: the child must bind Task Orchestration's
		// selected parent, the C04 reconstruction/export source, and the
		// expected current head at the same linearization point.
		if candidate.parent == "" || candidate.parent != stream.currentHead {
			return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
		}
		if _, activated := m.persistence.activated[candidate.parent]; !activated {
			return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
		}
	}

	if err := m.injectFault(FaultBeforeActivationCommit, operationID, IntentActivatePublication, string(candidate.versionID)); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}

	// ---- Commit: immutable version + explicit stream advance + terminal
	// operation + activation evidence, all under one lock ----
	nextRevision := stream.revision + 1
	evidence := buildActivationEvidence(record, header, candidate, nextRevision, m.now())
	activated := activatedRecord{
		versionID: candidate.versionID, schemaVersion: candidate.schemaVersion,
		policyDomainID: header.PolicyDomainID, taskID: header.TaskID,
		kind: candidate.kind, parent: candidate.parent,
		contractID: candidate.contractID, phaseRunID: candidate.phaseRunID,
		operationID: operationID, streamRevision: nextRevision,
		manifestDigest: candidate.manifestDigest, lineageDigest: candidate.lineageDigest,
		members: cloneMemberRecords(candidate.members), evidence: cloneEvidence(evidence),
		occurredAt: m.now(),
	}
	m.persistence.activated[candidate.versionID] = activated
	stream.revision = nextRevision
	stream.currentHead = candidate.versionID
	m.persistence.streams[taskKey] = stream

	record.streamRevision = nextRevision
	record.activationEvidence = cloneEvidence(evidence)
	record.state = OperationActivated

	decision := decisionForRecord(record, false, m.now())
	m.recordOutcome(record, IntentActivatePublication, digest, record.state, decision, nil)

	if err := m.injectFault(FaultBeforeResponse, operationID, IntentActivatePublication, string(candidate.versionID)); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	return decision, nil
}

// buildActivationEvidence constructs the committed publication evidence. It
// binds only facts that are already durable at the linearization point: the
// operation identity and request digest, the ArtifactVersionID, manifest
// and lineage digests, the publication kind and exact parent, the committed
// stream revision and current head, the Phase Run identity with the
// publication generation/fence, the activity generation, and the safety
// epoch. Trace IDs, delivery attempts, claims, and telemetry attributes
// never enter the evidence. It is a pure shared function: the in-memory
// authority and the real PostgreSQL adapter must commit byte-identical
// evidence for the same linearization point.
func buildActivationEvidence(
	record *operationRecord,
	header PublicationIntentHeader,
	candidate *candidateRecord,
	streamRevision StreamRevision,
	now Instant,
) *PublicationEvidence {
	evidence := &PublicationEvidence{
		OperationID:        record.operationID,
		RequestDigest:      record.requestDigest,
		PolicyDomainID:     header.PolicyDomainID,
		TaskID:             header.TaskID,
		PhaseRunID:         candidate.phaseRunID,
		ArtifactVersionID:  candidate.versionID,
		ManifestDigest:     candidate.manifestDigest,
		LineageDigest:      candidate.lineageDigest,
		PublicationKind:    candidate.kind,
		Parent:             candidate.parent,
		StreamRevision:     streamRevision,
		CurrentHead:        candidate.versionID,
		ActivityGeneration: header.ActivityGeneration,
		Generation:         header.Generation,
		Fence:              header.Fence,
		SafetyEpoch:        header.SafetyEpoch,
		OccurredAt:         now,
	}
	return evidence
}

// cloneMemberRecords copies the immutable member facts of a candidate into
// the activated version so no later mutation can reach them through aliasing.
func cloneMemberRecords(members []memberRecord) []memberRecord {
	return append([]memberRecord(nil), members...)
}

// activatedVersionsForTask returns the committed Artifact Versions of one
// Task ordered exclusively by the explicit committed stream revision.
// Timestamps, ID ordering, row order, and file discovery never participate.
func (m *inMemory) activatedVersionsForTask(policyDomainID PolicyDomainID, taskID TaskID) []activatedRecord {
	versions := make([]activatedRecord, 0, len(m.persistence.activated))
	for _, activated := range m.persistence.activated {
		if activated.policyDomainID != policyDomainID || activated.taskID != taskID {
			continue
		}
		versions = append(versions, activated)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].streamRevision < versions[j].streamRevision })
	return versions
}
