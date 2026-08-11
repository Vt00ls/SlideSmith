package artifactpublication

import (
	"context"
	"errors"
	"testing"
)

// isEvidenceError reports whether err is a fail-closed evidence error.
func isEvidenceError(err error) bool {
	var publicationError *Error
	if !errors.As(err, &publicationError) {
		return false
	}
	switch publicationError.Code {
	case ErrorIntegrityFailure, ErrorEvidenceMissing, ErrorEvidenceCorrupt:
		return true
	default:
		return false
	}
}

func isCode(err error, want ErrorCode) bool {
	var publicationError *Error
	return errors.As(err, &publicationError) && publicationError.Code == want
}

// verifyWithMutation applies a mutation to the verify payload and expects a
// fail-closed verification error (the operation stays non-terminal).
func verifyWithMutation(t *testing.T, f *fixture, operationID string, set *evidenceSet, mutate func(*VerifyPublicationPayload)) error {
	t.Helper()
	payload := f.verifyPayload(set)
	mutate(&payload)
	intent := bindDigest(NewVerifyPublication(f.header(operationID), payload))
	_, err := f.core.Mutate(context.Background(), intent)
	return err
}

func expectVerifyFailure(t *testing.T, f *fixture, operationID string, set *evidenceSet, mutate func(*VerifyPublicationPayload), want ErrorCode) {
	t.Helper()
	err := verifyWithMutation(t, f, operationID, set, mutate)
	if !isCode(err, want) {
		t.Fatalf("error = %v, want %s", err, want)
	}
	// The operation must remain non-terminal and never create a version.
	view, queryErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: PublicationOperationID(operationID),
	})
	if queryErr != nil {
		t.Fatalf("query operation: %v", queryErr)
	}
	if view.State == OperationVerified || view.State == OperationRejected {
		t.Fatalf("failed verification must not advance the operation: %#v", view)
	}
}

// TestEvidenceMatrixFailClosed drives the evidence matrix item by item:
// unauthorized producer, cross-scope, wrong output manifest, validation not
// binding runtime, C04 not binding validation, wrong purpose/domain/digest/
// size, and missing/partial/duplicate evidence all fail closed.
func TestEvidenceMatrixFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*VerifyPublicationPayload)
	}{
		{"runtime unauthorized producer", func(p *VerifyPublicationPayload) {
			p.RuntimeEvidence[0].Producer.AuthorityID = "unknown-authority"
		}},
		{"runtime cross-task scope", func(p *VerifyPublicationPayload) {
			p.RuntimeEvidence[0].TaskID = "task-other"
		}},
		{"runtime wrong phase run", func(p *VerifyPublicationPayload) {
			p.RuntimeEvidence[0].PhaseRunID = "phase-run-other"
		}},
		{"runtime wrong safety epoch", func(p *VerifyPublicationPayload) {
			p.RuntimeEvidence[0].SafetyEpoch = 99
		}},
		{"runtime wrong outcome", func(p *VerifyPublicationPayload) {
			p.RuntimeEvidence[0].Outcome = "failed"
		}},
		{"runtime corrupted digest", func(p *VerifyPublicationPayload) {
			p.RuntimeEvidence[0].ProposalRef = "tampered"
			p.RuntimeEvidence[0].Digest = p.RuntimeEvidence[0].CanonicalDigest()
		}},
		{"runtime evidence missing", func(p *VerifyPublicationPayload) {
			p.RuntimeEvidence = nil
		}},
		{"runtime extra evidence", func(p *VerifyPublicationPayload) {
			extra := p.RuntimeEvidence[0]
			extra.ID = "runtime-evidence-extra"
			extra.Channel = ChannelDeck
			extra.Digest = extra.CanonicalDigest()
			p.RuntimeEvidence = append(p.RuntimeEvidence, extra)
		}},
		{"validation unauthorized producer", func(p *VerifyPublicationPayload) {
			p.ValidationEvidence.Producer.AuthorityID = "unknown-authority"
		}},
		{"validation wrong contract", func(p *VerifyPublicationPayload) {
			p.ValidationEvidence.ContractID = "other-contract"
		}},
		{"validation not binding runtime", func(p *VerifyPublicationPayload) {
			p.ValidationEvidence.RuntimeEvidenceRefs = nil
		}},
		{"validation wrong proposal manifest", func(p *VerifyPublicationPayload) {
			p.ValidationEvidence.OutputProposalManifestDigest = testDigest("other-proposal")
		}},
		{"validation cross-task scope", func(p *VerifyPublicationPayload) {
			p.ValidationEvidence.TaskID = "task-other"
		}},
		{"c04 unauthorized producer", func(p *VerifyPublicationPayload) {
			p.C04CommitEvidence.Producer.AuthorityID = "unknown-authority"
		}},
		{"c04 not binding validation", func(p *VerifyPublicationPayload) {
			p.C04CommitEvidence.ValidationEvidenceID = "validation-evidence-other"
		}},
		{"c04 cross-workspace", func(p *VerifyPublicationPayload) {
			p.C04CommitEvidence.TaskWorkspaceID = "workspace-other"
		}},
		{"c04 missing roots", func(p *VerifyPublicationPayload) {
			p.C04CommitEvidence.ContentEvidenceRoot = ""
		}},
		{"c04 wrong safety epoch", func(p *VerifyPublicationPayload) {
			p.C04CommitEvidence.SafetyEpoch = 99
		}},
		{"capability unauthorized producer", func(p *VerifyPublicationPayload) {
			p.ContentCapabilities[0].Producer.AuthorityID = "unknown-authority"
		}},
		{"capability wrong purpose", func(p *VerifyPublicationPayload) {
			p.ContentCapabilities[0].Purpose = ContentPurpose("other")
		}},
		{"capability wrong policy domain", func(p *VerifyPublicationPayload) {
			p.ContentCapabilities[0].PolicyDomainID = "domain-other"
		}},
		{"capability wrong content digest", func(p *VerifyPublicationPayload) {
			p.ContentCapabilities[0].ContentDigest = testDigest("other-content")
		}},
		{"capability wrong size", func(p *VerifyPublicationPayload) {
			p.ContentCapabilities[0].Size = 999
		}},
		{"capability wrong member slot", func(p *VerifyPublicationPayload) {
			p.ContentCapabilities[0].MemberSlot = "slot-other"
		}},
		{"capability missing", func(p *VerifyPublicationPayload) {
			p.ContentCapabilities = nil
		}},
		{"capability duplicate", func(p *VerifyPublicationPayload) {
			duplicate := p.ContentCapabilities[0]
			duplicate.ID = "capability-duplicate"
			duplicate.Digest = duplicate.CanonicalDigest()
			p.ContentCapabilities = append(p.ContentCapabilities, duplicate)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			operationID := "op-matrix-" + test.name
			set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
			f.mustPrepare(t, operationID, set)
			switch test.name {
			case "runtime evidence missing", "capability missing", "capability wrong purpose":
				// An empty or unknown-enum evidence set is an invalid intent,
				// not an evidence failure: the payload is malformed.
				expectVerifyFailure(t, f, operationID, set, test.mutate, ErrorInvalidIntent)
			default:
				err := verifyWithMutation(t, f, operationID, set, test.mutate)
				if !isEvidenceError(err) {
					t.Fatalf("error = %v, want a fail-closed evidence error", err)
				}
			}
		})
	}
}

// TestCapabilityNotCurrentRequiresReconciliation proves a pinned capability
// that is not currently resolvable in the Durable Object authority fails
// closed as ambiguous and requires reconciliation, never as a silent success
// or a determinate failure.
func TestCapabilityNotCurrentRequiresReconciliation(t *testing.T) {
	f := newFixture(t)
	operationID := "op-not-current"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	capability := set.capabilities[0]
	f.registry.register(capability, false) // in-flight / not yet current

	f.mustPrepare(t, operationID, set)
	verify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verify.State != OperationReconciliationRequired {
		t.Fatalf("expected reconciliation required, got %#v", verify)
	}
	if verify.Verification == nil || verify.Verification.State != VerificationAmbiguous {
		t.Fatalf("expected ambiguous verification, got %#v", verify.Verification)
	}
}

// TestUnregisteredCapabilityFailsClosed proves an unknown capability
// identity is durability-unverified and never accepted.
func TestUnregisteredCapabilityFailsClosed(t *testing.T) {
	f := newFixture(t)
	operationID := "op-unregistered"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	// The pinned capability is not registered in the Durable Object
	// authority at all.
	delete(f.registry.facts, set.capabilities[0].ID)
	delete(f.registry.current, set.capabilities[0].ID)

	f.mustPrepare(t, operationID, set)
	verify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verify.State != OperationReconciliationRequired {
		t.Fatalf("expected reconciliation required for unregistered capability, got %#v", verify)
	}
}

// TestRejectBindsExactEvidenceFailure proves a rejection carries the exact
// evidence failure and remains terminal, and rejection is the caller's
// decision after a determinate verification failure.
func TestRejectBindsExactEvidenceFailure(t *testing.T) {
	f := newFixture(t)
	operationID := "op-reject-evidence"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	f.mustPrepare(t, operationID, set)
	expectVerifyFailure(t, f, operationID, set, func(p *VerifyPublicationPayload) {
		p.RuntimeEvidence[0].Producer.AuthorityID = "unknown-authority"
		p.RuntimeEvidence[0].Digest = p.RuntimeEvidence[0].CanonicalDigest()
	}, ErrorIntegrityFailure)

	failure := &EvidenceFailure{Kind: "runtime_evidence_scope", EvidenceID: "runtime-evidence-1"}
	rejected, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectEvidenceFailure, failure))
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.State != OperationRejected || rejected.RejectReason != RejectEvidenceFailure {
		t.Fatalf("unexpected reject decision: %#v", rejected)
	}
}

// TestManualEditExportLineageEvidenceFailsClosed proves a manual-edit
// candidate's C04 commit evidence must bind the exact parent via the
// validated export evidence. The candidate is constructed directly because
// manual-edit prepare requires an activated parent (child SPEC #105); the
// evidence engine must still enforce the export binding deterministically,
// including when Task Orchestration pins a malformed manual-edit C04
// evidence.
func TestManualEditExportLineageEvidenceFailsClosed(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	parent := ArtifactVersionID("artifact-version-parent")
	baseExport := &ValidatedExportEvidence{
		ID: "validated-export-1", PublicationAuthorityID: f.c04Authority,
		PolicyDomainID: f.policyDomain, TaskID: f.taskID, TaskWorkspaceID: "task-workspace-1",
		SourceArtifactVersionID:  parent,
		ReconstructionEvidenceID: "reconstruction-1", RevisionID: "revision-2",
		CheckpointID: "checkpoint-2", ValidationEvidenceID: set.validation.ID,
		Generation: f.generation, Fence: f.fence,
	}
	baseExport.Digest = baseExport.CanonicalDigest()

	// The pinned parent and the export must agree on the source version.
	completeSet := cloneEvidenceSet(set)
	completeSet.c04.ValidatedExportEvidence = baseExport
	completeSet.c04.Digest = completeSet.c04.CanonicalDigest()

	payload := f.preparePayload("op-export-unit", completeSet, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload.Kind = PublicationKindManualEdit
	payload.Parent = parent
	candidate := manualEditCandidate(payload, completeSet)

	// Complete manual-edit evidence passes the engine.
	complete := f.verifyPayload(completeSet)
	complete.C04CommitEvidence.ValidatedExportEvidence = baseExport
	complete.C04CommitEvidence.Digest = complete.C04CommitEvidence.CanonicalDigest()
	if failure := evaluateManualEdit(t, f, candidate, complete); failure != nil {
		t.Fatalf("complete manual-edit evidence failed: %#v", failure)
	}

	// A malformed pin with the export removed fails closed.
	nilExportSet := cloneEvidenceSet(set)
	payloadMissing := f.preparePayload("op-export-unit", nilExportSet, []ArtifactMemberSpec{f.deckMemberSpec()})
	payloadMissing.Kind = PublicationKindManualEdit
	payloadMissing.Parent = parent
	candidateMissing := manualEditCandidate(payloadMissing, nilExportSet)
	missingExport := f.verifyPayload(nilExportSet) // no export bound
	if failure := evaluateManualEdit(t, f, candidateMissing, missingExport); failure == nil || failure.Kind != "c04_export_missing" {
		t.Fatalf("missing export failure = %#v, want c04_export_missing", failure)
	}

	// A pin whose export binds a different source version fails closed.
	wrongSet := cloneEvidenceSet(set)
	wrongExport := &ValidatedExportEvidence{ID: "validated-export-1", PublicationAuthorityID: f.c04Authority,
		PolicyDomainID: f.policyDomain, TaskID: f.taskID, TaskWorkspaceID: "task-workspace-1",
		SourceArtifactVersionID:  "artifact-version-other",
		ReconstructionEvidenceID: "reconstruction-1", RevisionID: "revision-2",
		CheckpointID: "checkpoint-2", ValidationEvidenceID: set.validation.ID,
		Generation: f.generation, Fence: f.fence,
	}
	wrongExport.Digest = wrongExport.CanonicalDigest()
	wrongSet.c04.ValidatedExportEvidence = wrongExport
	wrongSet.c04.Digest = wrongSet.c04.CanonicalDigest()

	payloadWrong := f.preparePayload("op-export-unit", wrongSet, []ArtifactMemberSpec{f.deckMemberSpec()})
	payloadWrong.Kind = PublicationKindManualEdit
	payloadWrong.Parent = parent
	candidateWrong := manualEditCandidate(payloadWrong, wrongSet)
	wrongPayload := f.verifyPayload(wrongSet)
	wrongPayload.C04CommitEvidence.ValidatedExportEvidence = wrongExport
	wrongPayload.C04CommitEvidence.Digest = wrongPayload.C04CommitEvidence.CanonicalDigest()
	if failure := evaluateManualEdit(t, f, candidateWrong, wrongPayload); failure == nil || failure.Kind != "c04_export_scope" {
		t.Fatalf("wrong source export failure = %#v, want c04_export_scope", failure)
	}

	// A pin whose export binds a different Revision/Checkpoint than the C04
	// commit evidence fails closed: the child must match the new
	// Revision/Checkpoint the commit bound (SPEC #105).
	revisionSet := cloneEvidenceSet(set)
	revisionExport := &ValidatedExportEvidence{ID: "validated-export-1", PublicationAuthorityID: f.c04Authority,
		PolicyDomainID: f.policyDomain, TaskID: f.taskID, TaskWorkspaceID: "task-workspace-1",
		SourceArtifactVersionID:  parent,
		ReconstructionEvidenceID: "reconstruction-1", RevisionID: "revision-other",
		CheckpointID: "checkpoint-other", ValidationEvidenceID: set.validation.ID,
		Generation: f.generation, Fence: f.fence,
	}
	revisionExport.Digest = revisionExport.CanonicalDigest()
	revisionSet.c04.ValidatedExportEvidence = revisionExport
	revisionSet.c04.Digest = revisionSet.c04.CanonicalDigest()

	payloadRevision := f.preparePayload("op-export-unit", revisionSet, []ArtifactMemberSpec{f.deckMemberSpec()})
	payloadRevision.Kind = PublicationKindManualEdit
	payloadRevision.Parent = parent
	candidateRevision := manualEditCandidate(payloadRevision, revisionSet)
	revisionPayload := f.verifyPayload(revisionSet)
	revisionPayload.C04CommitEvidence.ValidatedExportEvidence = revisionExport
	revisionPayload.C04CommitEvidence.Digest = revisionPayload.C04CommitEvidence.CanonicalDigest()
	if failure := evaluateManualEdit(t, f, candidateRevision, revisionPayload); failure == nil || failure.Kind != "c04_export_scope" {
		t.Fatalf("revision mismatch export failure = %#v, want c04_export_scope", failure)
	}
}

// cloneEvidenceSet returns a shallow copy of an evidence set with fresh
// slices so mutations do not alias the original.
func cloneEvidenceSet(set *evidenceSet) *evidenceSet {
	clone := *set
	clone.runtimeEvidence = append([]RuntimeEvidence(nil), set.runtimeEvidence...)
	clone.capabilities = append([]ContentCapabilityEvidence(nil), set.capabilities...)
	return &clone
}

// manualEditCandidate builds a manual-edit candidate from a first-generation
// payload and evidence set with the pinned parent.
func manualEditCandidate(payload PreparePublicationPayload, set *evidenceSet) *candidateRecord {
	return &candidateRecord{
		versionID: "artifact-version-1", schemaVersion: SchemaV1,
		kind: PublicationKindManualEdit, parent: "artifact-version-parent",
		contractID: payload.ContractID, phaseRunID: payload.PhaseRunID,
		members: []memberRecord{{
			slot: payload.Members[0].Slot, artifactID: "artifact-1",
			kind: payload.Members[0].Kind, logicalName: payload.Members[0].LogicalName,
			mediaType: payload.Members[0].MediaType, size: payload.Members[0].Size,
			contentDigest: payload.Members[0].ContentDigest,
		}},
		staging: []stagingRecord{{
			slot: payload.Staging[0].MemberSlot, contentID: payload.Staging[0].ContentID,
			contentDigest: payload.Staging[0].ContentDigest, size: payload.Staging[0].Size,
			purpose:            payload.Staging[0].Purpose,
			physicalGeneration: payload.Staging[0].PhysicalGeneration,
			adapterID:          payload.Staging[0].AdapterID,
		}},
		runtimeRefs:      payload.RuntimeRefs,
		validationRef:    payload.ValidationRef,
		c04Ref:           payload.C04CommitRef,
		capabilityRefs:   payload.ContentCapabilityRefs,
		requiredChannels: payload.RequiredChannels,
	}
}

// evaluateManualEdit runs the evidence engine against a manual-edit
// candidate and returns the failure, if any.
func evaluateManualEdit(t *testing.T, f *fixture, candidate *candidateRecord, payload VerifyPublicationPayload) *EvidenceFailure {
	t.Helper()
	authority := &inMemory{persistence: f.persistence, config: InMemoryConfig{
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CurrentContentCapability:     f.registry.resolve,
	}}
	_, _, failure, err := authority.evaluateEvidence(candidate, f.header("op-export-unit"), VerifyPublication{
		intentBase:          intentBase{intentHeader: f.header("op-export-unit")},
		RuntimeEvidence:     payload.RuntimeEvidence,
		ValidationEvidence:  payload.ValidationEvidence,
		C04CommitEvidence:   payload.C04CommitEvidence,
		ContentCapabilities: payload.ContentCapabilities,
	})
	if err != nil {
		t.Fatalf("evaluate evidence: %v", err)
	}
	return failure
}
