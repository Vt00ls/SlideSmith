package taskworkspace_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

type integrityDurableObjectDouble struct {
	mu                     sync.Mutex
	nextID                 uint64
	sources                map[taskworkspace.Digest][]byte
	contents               map[string]*integrityContent
	byID                   map[taskworkspace.ContentID]*integrityContent
	repairs                map[taskworkspace.ContentID][]byte
	orphans                [][]byte
	prepareReceiptOverride *taskworkspace.DurabilityReceipt
}

func TestOrphanObservationCannotBecomeCheckpointOrRepairAuthority(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		durable := newIntegrityDurableObjectDouble()
		contentDigest, _ := declaredContentFacts("content-1")
		durable.removeSource(contentDigest)
		durable.observeOrphan([]byte("task-owned-state-one"))
		lifecycle := newLifecycleWithDurableObject(durable)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
		)
		manifest := declaredStateManifest("content-1")
		validation := acceptedValidationEvidence(confirmed, view, manifest)

		result, err := lifecycle.CommitRuntimeView(
			context.Background(),
			commitRequest(confirmed, view, manifest, validation, "commit-1"),
		)
		if err == nil {
			t.Fatal("orphan observation was adopted as declared business content")
		}
		if result.CheckpointID != "" || result.RevisionID != "" {
			t.Fatal("orphan observation created a Checkpoint or Revision")
		}
	})

	t.Run("repair", func(t *testing.T) {
		durable := newIntegrityDurableObjectDouble()
		lifecycle := newLifecycleWithDurableObject(durable)
		_, committed := commitTaskContent(t, lifecycle, "policy-domain-1", "task-1", "one")
		member := committed.CheckpointEvidence.ContentReferences[0]
		durable.damage(member.ContentID, []byte("corrupt-state"))
		durable.observeOrphan([]byte("task-owned-state-one"))
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(),
			confirmRequest("policy-domain-1", "task-1", "confirm-current"),
		)
		if err != nil {
			t.Fatalf("confirm current Task Workspace: %v", err)
		}

		result, err := lifecycle.Materialize(
			context.Background(),
			materializeRequest("policy-domain-1", "task-1", current, "materialize-orphan"),
		)
		if err == nil {
			t.Fatal("orphan observation was adopted as repair authority")
		}
		if result.MaterializationID != "" || result.CheckpointID != "" {
			t.Fatal("orphan observation returned recovery authority")
		}
	})
}

type integrityContent struct {
	id      taskworkspace.ContentID
	domain  taskworkspace.PolicyDomainID
	digest  taskworkspace.Digest
	size    uint64
	payload []byte
	receipt taskworkspace.DurabilityReceipt
}

func newIntegrityDurableObjectDouble() *integrityDurableObjectDouble {
	return &integrityDurableObjectDouble{
		sources: map[taskworkspace.Digest][]byte{
			"sha256:c23e70927230be9d39b8237ab27c9a45cec5e1dafac3941a1dabf1df748656ca": []byte("task-owned-state-one"),
			"sha256:1dde25249fd4b6cbedb58974a4e89c06c5741fee860b2e7faf35cd9bfd3debaf": []byte("task-owned-state-two"),
		},
		contents: make(map[string]*integrityContent),
		byID:     make(map[taskworkspace.ContentID]*integrityContent),
		repairs:  make(map[taskworkspace.ContentID][]byte),
	}
}

func TestExactRepairPreservesCheckpointContentAndPolicyFacts(t *testing.T) {
	durable := newIntegrityDurableObjectDouble()
	lifecycle := newLifecycleWithDurableObject(durable)
	_, committed := commitTaskContent(t, lifecycle, "policy-domain-1", "task-1", "one")
	original := committed.CheckpointEvidence
	member := original.ContentReferences[0]
	durable.damage(member.ContentID, []byte("corrupt-state"))
	durable.setRepair(member.ContentID, []byte("task-owned-state-one"))
	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-current"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}

	materialized, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-repaired"),
	)
	if err != nil {
		t.Fatalf("materialize exactly repaired Checkpoint: %v", err)
	}
	repaired := materialized.CheckpointEvidence
	if materialized.CheckpointID != committed.CheckpointID || repaired.Manifest.Digest != original.Manifest.Digest ||
		repaired.ManifestReference.ContentID != original.ManifestReference.ContentID ||
		repaired.ContentReferences[0].ContentID != member.ContentID ||
		repaired.ContentReferences[0].ContentDigest != member.ContentDigest ||
		repaired.ContentReferences[0].Size != member.Size ||
		repaired.ContentReferences[0].PolicyDomainID != member.PolicyDomainID {
		t.Fatal("exact repair changed a ContentID, digest, size, manifest, Checkpoint, or policy fact")
	}
	if repaired.IntegrityEvidence.DurabilityEvidenceRoot == original.IntegrityEvidence.DurabilityEvidenceRoot {
		t.Fatal("exact repair did not bind the replacement durability receipt")
	}
}

func TestCommitRejectsReceiptFromSupersededGenerationOfSharedContent(t *testing.T) {
	durable := newIntegrityDurableObjectDouble()
	lifecycle := newLifecycleWithDurableObject(durable)
	_, first := commitTaskContent(t, lifecycle, "policy-domain-1", "task-1", "one")
	member := first.CheckpointEvidence.ContentReferences[0]
	staleReceipt := first.CheckpointEvidence.DurabilityReceipts[1]
	durable.damage(member.ContentID, []byte("corrupt-state"))
	durable.setRepair(member.ContentID, []byte("task-owned-state-one"))
	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-current"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	repaired, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-repaired"),
	)
	if err != nil {
		t.Fatalf("materialize exactly repaired Checkpoint: %v", err)
	}
	currentReceipt := repaired.CheckpointEvidence.DurabilityReceipts[1]
	staleUnseen := currentReceipt
	staleUnseen.ID = "receipt-stale-unseen"
	staleUnseen.DurableWriteID = "durable-write-stale-unseen"
	staleUnseen.DurabilityGenerationID = staleReceipt.DurabilityGenerationID
	staleUnseen.Replaces = taskworkspace.DurabilityReplacementProof{
		ReceiptID:    currentReceipt.ID,
		GenerationID: currentReceipt.DurabilityGenerationID,
	}
	staleUnseen.EvidenceDigest = staleUnseen.CanonicalDigest()
	durable.setPrepareReceiptOverride(staleUnseen)

	_, request := taskContentCommitRequest(t, lifecycle, "policy-domain-1", "task-2", "two")

	result, err := lifecycle.CommitRuntimeView(context.Background(), request)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
		t.Fatalf("commit error = %T/%v, want typed integrity failure", err, err)
	}
	if result.CheckpointID != "" || result.RevisionID != "" {
		t.Fatal("superseded receipt returned Checkpoint or Revision authority")
	}
}

func TestDifferentByteRepairIsRejectedWithoutRewritingAuthoritativeFacts(t *testing.T) {
	durable := newIntegrityDurableObjectDouble()
	lifecycle := newLifecycleWithDurableObject(durable)
	_, committed := commitTaskContent(t, lifecycle, "policy-domain-1", "task-1", "one")
	original := committed.CheckpointEvidence
	member := original.ContentReferences[0]
	durable.damage(member.ContentID, []byte("corrupt-state"))
	durable.setRepair(member.ContentID, []byte("task-owned-state-two"))
	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-current"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}

	rejected, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-different-repair"),
	)
	if err == nil {
		t.Fatal("different-byte repair unexpectedly materialized the Checkpoint")
	}
	if rejected.MaterializationID != "" || rejected.CheckpointID != "" {
		t.Fatal("different-byte repair returned recovery authority")
	}

	durable.setRepair(member.ContentID, []byte("task-owned-state-one"))
	materialized, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-exact-repair"),
	)
	if err != nil {
		t.Fatalf("materialize after exact repair: %v", err)
	}
	repaired := materialized.CheckpointEvidence
	if repaired.Manifest.Digest != original.Manifest.Digest ||
		repaired.ContentReferences[0].ContentID != member.ContentID ||
		repaired.ContentReferences[0].ContentDigest != member.ContentDigest ||
		repaired.ContentReferences[0].Size != member.Size {
		t.Fatal("rejected different-byte repair rewrote an expected digest or authoritative Checkpoint fact")
	}
}

func (d *integrityDurableObjectDouble) PrepareCheckpoint(
	_ context.Context,
	request taskworkspace.PrepareCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if digestBytes(request.CanonicalManifest) != request.Manifest.Digest {
		return taskworkspace.VerifiedCheckpointContent{}, fmt.Errorf("manifest integrity failure")
	}
	contentReferences := make([]taskworkspace.ContentReference, 0, len(request.Manifest.Members))
	contents := make([]*integrityContent, 0, len(request.Manifest.Members)+1)
	for _, member := range request.Manifest.Members {
		payload, exists := d.sources[member.ContentDigest]
		if !exists || digestBytes(payload) != member.ContentDigest || uint64(len(payload)) != member.Size {
			return taskworkspace.VerifiedCheckpointContent{}, fmt.Errorf("declared content integrity failure")
		}
		content, err := d.prepareContent(request.PolicyDomainID, member.ContentDigest, member.Size, payload)
		if err != nil {
			return taskworkspace.VerifiedCheckpointContent{}, err
		}
		contentReferences = append(contentReferences, durableReference(
			d.nextOpaque("reference"),
			taskworkspace.CheckpointMemberReference,
			request,
			member.ID,
			member.LogicalMember,
			string(content.id),
			content.digest,
			content.size,
		))
		contents = append(contents, content)
	}
	manifest := checkpointManifestFromDeclared(request.Manifest, contentReferences)
	manifestBytes := manifest.CanonicalBytes()
	manifestContent, err := d.prepareContent(
		request.PolicyDomainID,
		manifest.Digest,
		uint64(len(manifestBytes)),
		manifestBytes,
	)
	if err != nil {
		return taskworkspace.VerifiedCheckpointContent{}, err
	}
	manifestReference := durableReference(
		d.nextOpaque("reference"),
		taskworkspace.CheckpointManifestReference,
		request,
		"",
		"",
		string(manifestContent.id),
		manifestContent.digest,
		manifestContent.size,
	)
	contents = append([]*integrityContent{manifestContent}, contents...)
	receipts := uniqueIntegrityReceipts(contents)
	if d.prepareReceiptOverride != nil {
		for index := range receipts {
			if receipts[index].ContentID == d.prepareReceiptOverride.ContentID {
				receipts[index] = *d.prepareReceiptOverride
			}
		}
	}
	return taskworkspace.VerifiedCheckpointContent{
		Manifest:           manifest,
		ManifestReference:  manifestReference,
		ContentReferences:  contentReferences,
		DurabilityReceipts: receipts,
	}, nil
}

func (d *integrityDurableObjectDouble) VerifyCheckpoint(
	_ context.Context,
	request taskworkspace.VerifyCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	contents := make([]*integrityContent, 0, len(request.Expected.ContentReferences)+1)
	for _, reference := range append(
		[]taskworkspace.ContentReference{request.Expected.ManifestReference},
		request.Expected.ContentReferences...,
	) {
		content, exists := d.byID[reference.ContentID]
		if !exists || content.domain != reference.PolicyDomainID || content.digest != reference.ContentDigest ||
			content.size != reference.Size {
			return taskworkspace.VerifiedCheckpointContent{}, fmt.Errorf("content integrity failure")
		}
		if digestBytes(content.payload) != reference.ContentDigest || uint64(len(content.payload)) != reference.Size {
			repair, available := d.repairs[reference.ContentID]
			if !available || digestBytes(repair) != reference.ContentDigest || uint64(len(repair)) != reference.Size {
				return taskworkspace.VerifiedCheckpointContent{}, fmt.Errorf("content integrity failure")
			}
			content.payload = append([]byte(nil), repair...)
			content.receipt = d.issueReceipt(content)
			delete(d.repairs, reference.ContentID)
		}
		contents = append(contents, content)
	}
	return taskworkspace.VerifiedCheckpointContent{
		Manifest:           request.Manifest,
		ManifestReference:  request.Expected.ManifestReference,
		ContentReferences:  append([]taskworkspace.ContentReference(nil), request.Expected.ContentReferences...),
		DurabilityReceipts: uniqueIntegrityReceipts(contents),
	}, nil
}

func (d *integrityDurableObjectDouble) prepareContent(
	domain taskworkspace.PolicyDomainID,
	digest taskworkspace.Digest,
	size uint64,
	payload []byte,
) (*integrityContent, error) {
	key := fmt.Sprintf("%s|%s|%d", domain, digest, size)
	if existing, found := d.contents[key]; found {
		if existing.domain != domain || existing.digest != digest || existing.size != size ||
			!integrityReceiptMatchesContent(existing.receipt, existing) {
			return nil, fmt.Errorf("deduplicated content integrity failure")
		}
		if digestBytes(existing.payload) != digest || uint64(len(existing.payload)) != size {
			repair, available := d.repairs[existing.id]
			if !available || digestBytes(repair) != digest || uint64(len(repair)) != size {
				return nil, fmt.Errorf("deduplicated content integrity failure")
			}
			existing.payload = append([]byte(nil), repair...)
			existing.receipt = d.issueReceipt(existing)
			delete(d.repairs, existing.id)
		}
		return existing, nil
	}
	content := &integrityContent{
		id:      taskworkspace.ContentID(d.nextOpaque("content")),
		domain:  domain,
		digest:  digest,
		size:    size,
		payload: append([]byte(nil), payload...),
	}
	content.receipt = d.issueReceipt(content)
	d.contents[key] = content
	d.byID[content.id] = content
	return content, nil
}

func integrityReceiptMatchesContent(receipt taskworkspace.DurabilityReceipt, content *integrityContent) bool {
	replacementComplete := (receipt.Replaces.ReceiptID == "") == (receipt.Replaces.GenerationID == "")
	return receipt.ID != "" && receipt.DurabilityAuthorityID == "durability-authority-1" &&
		receipt.DurableWriteID != "" && receipt.DurabilityAdapterID == "durability-adapter-1" &&
		receipt.PolicyDomainID == content.domain && receipt.ContentID == content.id &&
		receipt.ContentDigest == content.digest && receipt.Size == content.size &&
		receipt.DurabilityGenerationID != "" &&
		(receipt.VerificationMethod == taskworkspace.VerificationEndToEndChecksum ||
			receipt.VerificationMethod == taskworkspace.VerificationIndependentReadback) &&
		replacementComplete && !receipt.VerifiedAt.IsZero() && receipt.Decision == taskworkspace.DurabilityVerified &&
		receipt.EvidenceDigest == receipt.CanonicalDigest()
}

func (d *integrityDurableObjectDouble) issueReceipt(content *integrityContent) taskworkspace.DurabilityReceipt {
	verifiedAt := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC).Add(time.Duration(d.nextID) * time.Second)
	prior := content.receipt
	receipt := taskworkspace.DurabilityReceipt{
		ID:                     taskworkspace.DurabilityReceiptID(d.nextOpaque("receipt")),
		DurabilityAuthorityID:  "durability-authority-1",
		DurableWriteID:         taskworkspace.DurableWriteID(d.nextOpaque("durable-write")),
		DurabilityAdapterID:    "durability-adapter-1",
		PolicyDomainID:         content.domain,
		ContentID:              content.id,
		ContentDigest:          content.digest,
		Size:                   content.size,
		DurabilityGenerationID: taskworkspace.DurabilityGenerationID(d.nextOpaque("durability-generation")),
		VerificationMethod:     taskworkspace.VerificationIndependentReadback,
		VerifiedAt:             verifiedAt,
		Decision:               taskworkspace.DurabilityVerified,
	}
	if prior.ID != "" {
		receipt.Replaces = taskworkspace.DurabilityReplacementProof{
			ReceiptID:    prior.ID,
			GenerationID: prior.DurabilityGenerationID,
		}
	}
	receipt.EvidenceDigest = receipt.CanonicalDigest()
	return receipt
}

func (d *integrityDurableObjectDouble) nextOpaque(kind string) string {
	d.nextID++
	return fmt.Sprintf("%s-%016x", kind, d.nextID)
}

func (d *integrityDurableObjectDouble) damage(contentID taskworkspace.ContentID, payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if content := d.byID[contentID]; content != nil {
		content.payload = append([]byte(nil), payload...)
	}
}

func (d *integrityDurableObjectDouble) setRepair(contentID taskworkspace.ContentID, payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.repairs[contentID] = append([]byte(nil), payload...)
}

func (d *integrityDurableObjectDouble) removeSource(digest taskworkspace.Digest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sources, digest)
}

func (d *integrityDurableObjectDouble) observeOrphan(payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.orphans = append(d.orphans, append([]byte(nil), payload...))
}

func (d *integrityDurableObjectDouble) setPrepareReceiptOverride(receipt taskworkspace.DurabilityReceipt) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prepareReceiptOverride = &receipt
}

func uniqueIntegrityReceipts(contents []*integrityContent) []taskworkspace.DurabilityReceipt {
	receipts := make([]taskworkspace.DurabilityReceipt, 0, len(contents))
	seen := make(map[taskworkspace.ContentID]struct{}, len(contents))
	for _, content := range contents {
		if _, duplicate := seen[content.id]; duplicate {
			continue
		}
		seen[content.id] = struct{}{}
		receipts = append(receipts, content.receipt)
	}
	return receipts
}

func digestBytes(payload []byte) taskworkspace.Digest {
	digest := sha256.Sum256(payload)
	return taskworkspace.Digest("sha256:" + hex.EncodeToString(digest[:]))
}

func TestEqualVerifiedPayloadDeduplicatesOnlyInsideOnePolicyDomain(t *testing.T) {
	durable := newIntegrityDurableObjectDouble()
	lifecycle := newLifecycleWithDurableObject(durable)

	firstWorkspace, first := commitTaskContent(
		t, lifecycle, "policy-domain-1", "task-1", "one",
	)
	secondWorkspace, second := commitTaskContent(
		t, lifecycle, "policy-domain-1", "task-2", "two",
	)
	thirdWorkspace, third := commitTaskContent(
		t, lifecycle, "policy-domain-2", "task-3", "three",
	)

	firstContent := first.CheckpointEvidence.ContentReferences[0]
	secondContent := second.CheckpointEvidence.ContentReferences[0]
	thirdContent := third.CheckpointEvidence.ContentReferences[0]
	if firstContent.ContentID == "" || firstContent.ContentID != secondContent.ContentID {
		t.Fatal("equal verified payload did not share one ContentID inside the policy domain")
	}
	if firstContent.ContentID == thirdContent.ContentID {
		t.Fatal("equal User content shared a ContentID across Personal Workspace policy domains")
	}
	if firstContent.ID == secondContent.ID || first.CheckpointID == second.CheckpointID ||
		first.RevisionID == second.RevisionID || firstWorkspace.TaskWorkspaceID == secondWorkspace.TaskWorkspaceID {
		t.Fatal("deduplication merged a typed reference, Checkpoint, Revision, or Task Workspace identity")
	}
	if third.CheckpointID == first.CheckpointID || third.RevisionID == first.RevisionID ||
		thirdWorkspace.TaskWorkspaceID == firstWorkspace.TaskWorkspaceID {
		t.Fatal("cross-domain commit reused a business or ownership identity")
	}
}

func TestCommitRejectsCorruptedDeduplicatedPayload(t *testing.T) {
	durable := newIntegrityDurableObjectDouble()
	lifecycle := newLifecycleWithDurableObject(durable)
	_, first := commitTaskContent(t, lifecycle, "policy-domain-1", "task-1", "one")
	durable.damage(first.CheckpointEvidence.ContentReferences[0].ContentID, []byte("corrupt-state"))
	_, request := taskContentCommitRequest(t, lifecycle, "policy-domain-1", "task-2", "two")

	result, err := lifecycle.CommitRuntimeView(context.Background(), request)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
		t.Fatalf("commit error = %T/%v, want typed integrity failure", err, err)
	}
	if result.CheckpointID != "" || result.RevisionID != "" {
		t.Fatal("corrupt deduplicated payload returned Checkpoint or Revision authority")
	}
}

func TestCommitUsesExactRepairReceiptForCorruptedDeduplicatedPayload(t *testing.T) {
	durable := newIntegrityDurableObjectDouble()
	lifecycle := newLifecycleWithDurableObject(durable)
	_, first := commitTaskContent(t, lifecycle, "policy-domain-1", "task-1", "one")
	firstMember := first.CheckpointEvidence.ContentReferences[0]
	firstReceipt := durabilityReceiptForContent(t, first.CheckpointEvidence, firstMember.ContentID)
	durable.damage(firstMember.ContentID, []byte("corrupt-state"))
	durable.setRepair(firstMember.ContentID, []byte("task-owned-state-one"))

	_, repaired := commitTaskContent(t, lifecycle, "policy-domain-1", "task-2", "two")
	repairedMember := repaired.CheckpointEvidence.ContentReferences[0]
	repairedReceipt := durabilityReceiptForContent(t, repaired.CheckpointEvidence, repairedMember.ContentID)
	if repairedMember.ContentID != firstMember.ContentID || repairedReceipt.ID == firstReceipt.ID ||
		repairedReceipt.DurabilityGenerationID == firstReceipt.DurabilityGenerationID ||
		repairedReceipt.Replaces.ReceiptID != firstReceipt.ID ||
		repairedReceipt.Replaces.GenerationID != firstReceipt.DurabilityGenerationID {
		t.Fatal("exact dedupe repair reused stale evidence or changed the verified ContentID")
	}
}

func commitTaskContent(
	t *testing.T,
	lifecycle taskworkspace.Lifecycle,
	policyDomainID, taskID, suffix string,
) (taskworkspace.ConfirmTaskWorkspaceResult, taskworkspace.CommitRuntimeViewResult) {
	t.Helper()
	confirmed, request := taskContentCommitRequest(t, lifecycle, policyDomainID, taskID, suffix)
	committed, err := lifecycle.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("commit Task Workspace: %v", err)
	}
	return confirmed, committed
}

func durabilityReceiptForContent(
	t *testing.T,
	evidence taskworkspace.CheckpointEvidence,
	contentID taskworkspace.ContentID,
) taskworkspace.DurabilityReceipt {
	t.Helper()
	for _, receipt := range evidence.DurabilityReceipts {
		if receipt.ContentID == contentID {
			return receipt
		}
	}
	t.Fatalf("Checkpoint evidence omitted receipt for ContentID %q", contentID)
	return taskworkspace.DurabilityReceipt{}
}

func taskContentCommitRequest(
	t *testing.T,
	lifecycle taskworkspace.Lifecycle,
	policyDomainID, taskID, suffix string,
) (taskworkspace.ConfirmTaskWorkspaceResult, taskworkspace.CommitRuntimeViewRequest) {
	t.Helper()
	confirmed, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest(policyDomainID, taskID, "confirm-"+suffix),
	)
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	materialized, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest(policyDomainID, taskID, confirmed, "materialize-"+suffix),
	)
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	view, err := lifecycle.OpenRuntimeView(
		context.Background(),
		openRuntimeViewRequest(
			policyDomainID,
			taskID,
			confirmed,
			materialized,
			"phase-run-"+suffix,
			"runtime-run-"+suffix,
			"sandbox-lease-"+suffix,
			"open-view-"+suffix,
		),
	)
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	validation := taskworkspace.ValidationEvidence{
		ID:                          taskworkspace.EvidenceID("validation-evidence-" + suffix),
		ValidationAuthorityID:       "validation-authority-1",
		PolicyDomainID:              taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:                      taskworkspace.TaskID(taskID),
		TaskWorkspaceID:             confirmed.TaskWorkspaceID,
		RuntimeViewID:               view.RuntimeViewID,
		BaseRevisionID:              confirmed.CurrentRevisionID,
		PhaseRunID:                  view.PhaseRunID,
		RuntimeRunID:                view.RuntimeRunID,
		RuntimeOperationID:          view.RuntimeOperationID,
		SandboxLeaseAuthorityDigest: view.SandboxLeaseAuthority.Digest,
		ManifestDigest:              manifest.Digest,
		Generation:                  confirmed.Generation,
		Fence:                       confirmed.Fence,
		Decision:                    taskworkspace.ValidationAccepted,
	}
	validation.Digest = validation.CanonicalDigest()
	request := taskworkspace.CommitRuntimeViewRequest{
		PolicyDomainID:          taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:                  taskworkspace.TaskID(taskID),
		TaskWorkspaceID:         confirmed.TaskWorkspaceID,
		RuntimeViewID:           view.RuntimeViewID,
		RuntimeOperationID:      view.RuntimeOperationID,
		SandboxLeaseAuthority:   view.SandboxLeaseAuthority,
		BaseRevisionID:          confirmed.CurrentRevisionID,
		ExpectedCurrentRevision: confirmed.CurrentRevisionID,
		Generation:              confirmed.Generation,
		Fence:                   confirmed.Fence,
		ValidationEvidence:      validation,
		DeclaredStateManifest:   manifest,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID("commit-" + suffix),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return confirmed, request
}
