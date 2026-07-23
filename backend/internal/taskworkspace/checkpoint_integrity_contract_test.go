package taskworkspace_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestCommitRejectsUnsafeOrExcludedCheckpointMembersBeforeDurablePreparation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*taskworkspace.DeclaredStateManifest)
	}{
		{
			name: "duplicate logical member",
			edit: func(manifest *taskworkspace.DeclaredStateManifest) {
				duplicate := manifest.Members[0]
				duplicate.ID = "state-member-2"
				manifest.Members = append(manifest.Members, duplicate)
			},
		},
		{
			name: "parent traversal logical member",
			edit: func(manifest *taskworkspace.DeclaredStateManifest) {
				manifest.Members[0].LogicalMember = "../secret"
			},
		},
		{
			name: "absolute logical member",
			edit: func(manifest *taskworkspace.DeclaredStateManifest) {
				manifest.Members[0].LogicalMember = "/state/deck.json"
			},
		},
		{
			name: "symbolic link",
			edit: func(manifest *taskworkspace.DeclaredStateManifest) {
				manifest.Members[0].Type = taskworkspace.StateMemberSymbolicLink
			},
		},
		{
			name: "hard link",
			edit: func(manifest *taskworkspace.DeclaredStateManifest) {
				manifest.Members[0].Type = taskworkspace.StateMemberHardLink
			},
		},
		{
			name: "unsafe member type",
			edit: func(manifest *taskworkspace.DeclaredStateManifest) {
				manifest.Members[0].Type = "device"
			},
		},
		{
			name: "unsafe mode",
			edit: func(manifest *taskworkspace.DeclaredStateManifest) {
				manifest.Members[0].Mode = 0o4600
			},
		},
	}

	excluded := []taskworkspace.StateMemberClass{
		taskworkspace.StateMemberRuntimeRelease,
		taskworkspace.StateMemberCoreSkill,
		taskworkspace.StateMemberTemplateVersion,
		taskworkspace.StateMemberResourceBundle,
		taskworkspace.StateMemberSourceMaterial,
		taskworkspace.StateMemberImmutableInput,
		taskworkspace.StateMemberSharedCache,
		taskworkspace.StateMemberAgentComposeSession,
		taskworkspace.StateMemberSecret,
		taskworkspace.StateMemberLog,
		taskworkspace.StateMemberFailedResidue,
		taskworkspace.StateMemberPublicationStaging,
	}
	for _, class := range excluded {
		class := class
		tests = append(tests, struct {
			name string
			edit func(*taskworkspace.DeclaredStateManifest)
		}{
			name: string(class),
			edit: func(manifest *taskworkspace.DeclaredStateManifest) {
				manifest.Members[0].Class = class
			},
		})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			durable := &happyDurableObject{}
			lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
				ValidationAuthorityID: "validation-authority-1",
				DurabilityAuthorityID: "durability-authority-1",
				DurableObject:         durable,
			})
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
			)
			manifest := declaredStateManifest("content-1")
			test.edit(&manifest)
			manifest.Digest = manifest.CanonicalDigest()
			validation := acceptedValidationEvidence(confirmed, view, manifest)
			request := commitRequest(confirmed, view, manifest, validation, "commit-1")

			result, err := lifecycle.CommitRuntimeView(context.Background(), request)
			var lifecycleError *taskworkspace.Error
			if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
				t.Fatalf("commit error = %T/%v, want typed integrity failure", err, err)
			}
			if result.CheckpointID != "" || result.RevisionID != "" {
				t.Fatal("rejected manifest returned authoritative identities")
			}
			if durable.prepared != 0 {
				t.Fatal("C04 passed an unsafe or excluded manifest to Durable Object preparation")
			}
		})
	}
}

func TestCommitFailsClosedForUnverifiedOrIncompletelyBoundDurableContent(t *testing.T) {
	tests := []struct {
		name         string
		prepareError error
		mutate       func(*taskworkspace.VerifiedCheckpointContent)
	}{
		{
			name:         "missing content",
			prepareError: errors.New("missing content at bucket/object-key-do-not-disclose"),
		},
		{
			name:         "corrupt content",
			prepareError: errors.New("corrupt content from vendor-do-not-disclose"),
		},
		{
			name: "manifest digest mismatch",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ManifestReference.ContentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				content.ManifestReference.EvidenceDigest = content.ManifestReference.CanonicalDigest()
			},
		},
		{
			name: "manifest size mismatch",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ManifestReference.Size++
				content.ManifestReference.EvidenceDigest = content.ManifestReference.CanonicalDigest()
			},
		},
		{
			name: "member size mismatch",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].Size++
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "unverified payload",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].Decision = "unverified"
				content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
			},
		},
		{
			name: "partial content set",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences = nil
			},
		},
		{
			name: "wrong policy domain",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].PolicyDomainID = "policy-domain-do-not-disclose"
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "wrong resulting Revision",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].RevisionID = "revision-not-selected-by-c04"
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "wrong resulting Checkpoint",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].CheckpointID = "checkpoint-not-selected-by-c04"
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "wrong typed reference",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].Type = taskworkspace.CheckpointManifestReference
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "wrong attachment operation",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].OperationID = "operation-not-selected-by-c04"
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "duplicate reference identity",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].ID = content.ManifestReference.ID
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "missing receipt",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts = content.DurabilityReceipts[:1]
			},
		},
		{
			name: "receipt without verification method",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].VerificationMethod = ""
				content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
			},
		},
		{
			name: "receipt without verification time",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].VerifiedAt = time.Time{}
				content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
			},
		},
		{
			name: "stale receipt",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].Decision = "stale"
				content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			durable := &happyDurableObject{
				prepareError: test.prepareError,
				mutate:       test.mutate,
			}
			lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
				ValidationAuthorityID: "validation-authority-1",
				DurabilityAuthorityID: "durability-authority-1",
				DurableObject:         durable,
			})
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
			)
			manifest := declaredStateManifest("content-1")
			validation := acceptedValidationEvidence(confirmed, view, manifest)
			result, err := lifecycle.CommitRuntimeView(
				context.Background(),
				commitRequest(confirmed, view, manifest, validation, "commit-1"),
			)

			var lifecycleError *taskworkspace.Error
			if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
				t.Fatalf("commit error = %T/%v, want typed integrity failure", err, err)
			}
			if result.CheckpointID != "" || result.RevisionID != "" {
				t.Fatal("rejected durable content returned authoritative identities")
			}
			for _, leaked := range []string{"bucket", "object-key", "vendor", "do-not-disclose"} {
				if strings.Contains(strings.ToLower(err.Error()), leaked) {
					t.Fatal("lifecycle error leaked Durable Object implementation detail")
				}
			}
		})
	}
}

func TestMaterializeReverifiesTheCurrentCheckpointThroughDurableObjectPort(t *testing.T) {
	durable := &happyDurableObject{}
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
		DurabilityAuthorityID: "durability-authority-1",
		DurableObject:         durable,
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	committed, err := lifecycle.CommitRuntimeView(
		context.Background(),
		commitRequest(confirmed, view, manifest, validation, "commit-1"),
	)
	if err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-2"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}

	materialized, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-2"),
	)
	if err != nil {
		t.Fatalf("materialize current Checkpoint: %v", err)
	}

	if durable.verified != 1 {
		t.Fatal("current Checkpoint was not reverified through the Durable Object port")
	}
	if materialized.CheckpointID != committed.CheckpointID ||
		materialized.ManifestDigest != manifest.Digest ||
		materialized.ContentEvidenceRoot == "" || materialized.DurabilityEvidenceRoot == "" ||
		materialized.CheckpointEvidence.IntegrityEvidence.CheckpointID != committed.CheckpointID {
		t.Fatal("materialization omitted the exact verified Checkpoint and integrity evidence")
	}
}

func TestMaterializeRequiresTheExactPlatformReferencedCheckpoint(t *testing.T) {
	durable := &happyDurableObject{}
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
		DurabilityAuthorityID: "durability-authority-1",
		DurableObject:         durable,
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	committed, err := lifecycle.CommitRuntimeView(
		context.Background(),
		commitRequest(confirmed, view, manifest, validation, "commit-1"),
	)
	if err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-2"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	if current.CurrentCheckpointID != committed.CheckpointID {
		t.Fatal("confirmation omitted the Platform-referenced current Checkpoint")
	}
	request := materializeRequest("policy-domain-1", "task-1", current, "materialize-2")
	request.CheckpointID = "checkpoint-not-current"
	request.Operation.RequestDigest = request.CanonicalRequestDigest()

	result, err := lifecycle.Materialize(context.Background(), request)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorStaleAuthority {
		t.Fatalf("materialize error = %T/%v, want typed stale authority", err, err)
	}
	if result.MaterializationID != "" || result.CheckpointID != "" || durable.verified != 0 {
		t.Fatal("wrong Checkpoint target reached Durable Object verification or returned authority")
	}
}

func TestMaterializeFailsClosedWhenCheckpointContentCannotBeReverified(t *testing.T) {
	tests := []struct {
		name        string
		verifyError error
		mutate      func(*taskworkspace.VerifiedCheckpointContent)
	}{
		{
			name:        "missing content",
			verifyError: errors.New("missing content at bucket/object-key-do-not-disclose"),
		},
		{
			name:        "corrupt content",
			verifyError: errors.New("corrupt content from vendor-do-not-disclose"),
		},
		{
			name: "manifest digest mismatch",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ManifestReference.ContentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				content.ManifestReference.EvidenceDigest = content.ManifestReference.CanonicalDigest()
			},
		},
		{
			name: "size mismatch",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].Size++
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "unverified payload",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].Decision = "unverified"
				content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
			},
		},
		{
			name: "partial content set",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences = nil
			},
		},
		{
			name: "wrong policy domain",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.ContentReferences[0].PolicyDomainID = "policy-domain-do-not-disclose"
				content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
			},
		},
		{
			name: "stale receipt",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].Decision = "stale"
				content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			durable := &happyDurableObject{}
			lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
				ValidationAuthorityID: "validation-authority-1",
				DurabilityAuthorityID: "durability-authority-1",
				DurableObject:         durable,
			})
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
			)
			manifest := declaredStateManifest("content-1")
			validation := acceptedValidationEvidence(confirmed, view, manifest)
			if _, err := lifecycle.CommitRuntimeView(
				context.Background(),
				commitRequest(confirmed, view, manifest, validation, "commit-1"),
			); err != nil {
				t.Fatalf("commit Checkpoint: %v", err)
			}
			current, err := lifecycle.ConfirmTaskWorkspace(
				context.Background(),
				confirmRequest("policy-domain-1", "task-1", "confirm-2"),
			)
			if err != nil {
				t.Fatalf("confirm current Task Workspace: %v", err)
			}
			durable.verifyError = test.verifyError
			durable.verifyMutate = test.mutate

			result, err := lifecycle.Materialize(
				context.Background(),
				materializeRequest("policy-domain-1", "task-1", current, "materialize-2"),
			)
			var lifecycleError *taskworkspace.Error
			if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
				t.Fatalf("materialize error = %T/%v, want typed integrity failure", err, err)
			}
			if result.MaterializationID != "" || result.CheckpointID != "" {
				t.Fatal("failed re-verification returned materialization or Checkpoint authority")
			}
			for _, leaked := range []string{"bucket", "object-key", "vendor", "do-not-disclose"} {
				if strings.Contains(strings.ToLower(err.Error()), leaked) {
					t.Fatal("lifecycle error leaked Durable Object implementation detail")
				}
			}
		})
	}
}

func TestCommitRequestCannotCarryCallerMintedDurabilityEvidence(t *testing.T) {
	requestType := reflect.TypeOf(taskworkspace.CommitRuntimeViewRequest{})
	if _, exposed := requestType.FieldByName("DurabilityEvidence"); exposed {
		t.Fatal("lifecycle caller can still supply or self-sign durability evidence")
	}
	memberType := reflect.TypeOf(taskworkspace.DeclaredStateMember{})
	if _, exposed := memberType.FieldByName("ContentID"); exposed {
		t.Fatal("lifecycle caller can still mint a Durable Object ContentID")
	}
}

func TestCommitFailsClosedWithoutATrustedDurableObjectPort(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
		DurabilityAuthorityID: "durability-authority-1",
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)

	result, err := lifecycle.CommitRuntimeView(
		context.Background(),
		commitRequest(confirmed, view, manifest, validation, "commit-1"),
	)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
		t.Fatalf("commit error = %T/%v, want typed integrity failure", err, err)
	}
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("commit without trusted Durable Object port returned authority")
	}
}

func TestCommitBuildsCheckpointFromTrustedDurableObjectEvidence(t *testing.T) {
	durable := &happyDurableObject{}
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
		DurabilityAuthorityID: "durability-authority-1",
		DurableObject:         durable,
	})
	confirmed, view := openRuntimeViewWithLifecycle(t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	request := commitRequest(confirmed, view, manifest, validation, "commit-1")

	committed, err := lifecycle.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("commit trusted durable content: %v", err)
	}

	if durable.prepared != 1 {
		t.Fatal("commit did not prepare the declared Checkpoint through the Durable Object port")
	}
	if committed.CheckpointEvidence.ManifestReference.ContentID == "" ||
		len(committed.CheckpointEvidence.ContentReferences) != 1 ||
		committed.CheckpointEvidence.ContentReferences[0].ContentID == "" ||
		len(committed.CheckpointEvidence.DurabilityReceipts) != 2 {
		t.Fatal("Checkpoint result omitted trusted manifest, content reference, or durability receipt evidence")
	}
	if committed.CheckpointEvidence.IntegrityEvidence.CheckpointID != committed.CheckpointID ||
		committed.CheckpointEvidence.IntegrityEvidence.RevisionID != committed.RevisionID ||
		committed.CheckpointEvidence.IntegrityEvidence.ManifestDigest != manifest.Digest ||
		committed.CheckpointEvidence.IntegrityEvidence.ValidationEvidenceID != validation.ID {
		t.Fatal("Checkpoint integrity evidence is not bound to the resulting Checkpoint, Revision, manifest, and validation evidence")
	}
	manifestReference := committed.CheckpointEvidence.ManifestReference
	memberReference := committed.CheckpointEvidence.ContentReferences[0]
	if !reflect.DeepEqual(committed.CheckpointEvidence.Manifest, manifest) ||
		manifestReference.Type != taskworkspace.CheckpointManifestReference ||
		manifestReference.CheckpointID != committed.CheckpointID || manifestReference.RevisionID != committed.RevisionID ||
		manifestReference.ContentDigest != manifest.Digest ||
		memberReference.Type != taskworkspace.CheckpointMemberReference ||
		memberReference.CheckpointID != committed.CheckpointID || memberReference.RevisionID != committed.RevisionID ||
		memberReference.PolicyDomainID != request.PolicyDomainID || memberReference.TaskID != request.TaskID ||
		memberReference.TaskWorkspaceID != request.TaskWorkspaceID ||
		memberReference.ContentDigest != manifest.Members[0].ContentDigest || memberReference.Size != manifest.Members[0].Size ||
		committed.ContentEvidenceRoot != committed.CheckpointEvidence.IntegrityEvidence.ContentEvidenceRoot ||
		committed.DurabilityEvidenceRoot != committed.CheckpointEvidence.IntegrityEvidence.DurabilityEvidenceRoot ||
		committed.CheckpointEvidence.IntegrityEvidence.Digest != committed.CheckpointEvidence.IntegrityEvidence.CanonicalDigest() {
		t.Fatal("resulting Checkpoint is not completely bound to its canonical manifest, typed references, content facts, and evidence roots")
	}
	for _, receipt := range committed.CheckpointEvidence.DurabilityReceipts {
		if receipt.DurableWriteID == "" || receipt.DurabilityGenerationID == "" ||
			receipt.VerificationMethod == "" || receipt.VerifiedAt.IsZero() {
			t.Fatal("Durable Object returned a non-strict durability receipt")
		}
	}
}

func TestCheckpointCommitAndMaterializeExactReplayDoNotRepeatDurableObjectWork(t *testing.T) {
	durable := &happyDurableObject{}
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
		DurabilityAuthorityID: "durability-authority-1",
		DurableObject:         durable,
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	commit := commitRequest(confirmed, view, manifest, validation, "commit-1")
	firstCommit, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	if err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	replayedCommit, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	if err != nil {
		t.Fatalf("replay Checkpoint commit: %v", err)
	}
	if !reflect.DeepEqual(replayedCommit, firstCommit) || durable.prepared != 1 {
		t.Fatal("exact commit replay repeated Durable Object preparation or changed the terminal result")
	}
	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-2"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	materialize := materializeRequest("policy-domain-1", "task-1", current, "materialize-2")
	firstMaterialization, err := lifecycle.Materialize(context.Background(), materialize)
	if err != nil {
		t.Fatalf("materialize Checkpoint: %v", err)
	}
	replayedMaterialization, err := lifecycle.Materialize(context.Background(), materialize)
	if err != nil {
		t.Fatalf("replay Checkpoint materialization: %v", err)
	}
	if !reflect.DeepEqual(replayedMaterialization, firstMaterialization) || durable.verified != 1 {
		t.Fatal("exact materialize replay repeated Durable Object verification or changed the original result")
	}
}

type happyDurableObject struct {
	prepared     int
	verified     int
	prepareError error
	mutate       func(*taskworkspace.VerifiedCheckpointContent)
	verifyError  error
	verifyMutate func(*taskworkspace.VerifiedCheckpointContent)
}

func (d *happyDurableObject) PrepareCheckpoint(
	_ context.Context,
	request taskworkspace.PrepareCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	d.prepared++
	if d.prepareError != nil {
		return taskworkspace.VerifiedCheckpointContent{}, d.prepareError
	}
	manifestReference := durableReference(
		"reference-manifest-1",
		taskworkspace.CheckpointManifestReference,
		request,
		"",
		"",
		"content-manifest-1",
		request.Manifest.Digest,
		uint64(len(request.CanonicalManifest)),
	)
	member := request.Manifest.Members[0]
	memberReference := durableReference(
		"reference-member-1",
		taskworkspace.CheckpointMemberReference,
		request,
		member.ID,
		member.LogicalMember,
		"content-member-1",
		member.ContentDigest,
		member.Size,
	)
	content := taskworkspace.VerifiedCheckpointContent{
		ManifestReference: manifestReference,
		ContentReferences: []taskworkspace.ContentReference{memberReference},
		DurabilityReceipts: []taskworkspace.DurabilityReceipt{
			durableReceipt("receipt-manifest-1", manifestReference),
			durableReceipt("receipt-member-1", memberReference),
		},
	}
	if d.mutate != nil {
		d.mutate(&content)
	}
	return content, nil
}

func (d *happyDurableObject) VerifyCheckpoint(
	_ context.Context,
	request taskworkspace.VerifyCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	d.verified++
	if d.verifyError != nil {
		return taskworkspace.VerifiedCheckpointContent{}, d.verifyError
	}
	content := request.Expected
	if d.verifyMutate != nil {
		d.verifyMutate(&content)
	}
	return content, nil
}

func durableReference(
	id string,
	referenceType taskworkspace.ContentReferenceType,
	request taskworkspace.PrepareCheckpointContentRequest,
	memberID taskworkspace.StateMemberID,
	logicalMember taskworkspace.LogicalMember,
	contentID string,
	digest taskworkspace.Digest,
	size uint64,
) taskworkspace.ContentReference {
	reference := taskworkspace.ContentReference{
		ID:              taskworkspace.ContentReferenceID(id),
		Type:            referenceType,
		PolicyDomainID:  request.PolicyDomainID,
		TaskID:          request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID,
		RevisionID:      request.RevisionID,
		CheckpointID:    request.CheckpointID,
		StateMemberID:   memberID,
		LogicalMember:   logicalMember,
		ContentID:       taskworkspace.ContentID(contentID),
		ContentDigest:   digest,
		Size:            size,
		OperationID:     request.Operation.ID,
	}
	reference.EvidenceDigest = reference.CanonicalDigest()
	return reference
}

func durableReceipt(id string, reference taskworkspace.ContentReference) taskworkspace.DurabilityReceipt {
	receipt := taskworkspace.DurabilityReceipt{
		ID:                     taskworkspace.DurabilityReceiptID(id),
		DurabilityAuthorityID:  "durability-authority-1",
		DurableWriteID:         taskworkspace.DurableWriteID("durable-write-" + id),
		PolicyDomainID:         reference.PolicyDomainID,
		ContentID:              reference.ContentID,
		ContentDigest:          reference.ContentDigest,
		Size:                   reference.Size,
		DurabilityGenerationID: taskworkspace.DurabilityGenerationID("durability-generation-1"),
		VerificationMethod:     taskworkspace.VerificationEndToEndChecksum,
		VerifiedAt:             time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC),
		Decision:               taskworkspace.DurabilityVerified,
	}
	receipt.EvidenceDigest = receipt.CanonicalDigest()
	return receipt
}

func openRuntimeViewWithLifecycle(
	t *testing.T,
	lifecycle taskworkspace.Lifecycle,
	taskID, confirmOperationID, materializeOperationID, openOperationID string,
) (taskworkspace.ConfirmTaskWorkspaceResult, taskworkspace.OpenRuntimeViewResult) {
	t.Helper()
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", taskID, confirmOperationID,
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", taskID, confirmed, materializeOperationID,
	))
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", taskID, confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", openOperationID,
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	return confirmed, view
}
