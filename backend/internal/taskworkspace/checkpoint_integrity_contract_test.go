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

func TestCommitRejectsDurableObjectMutationOfDeclaredSemanticManifest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*taskworkspace.DeclaredStateMember)
	}{
		{
			name: "logical path",
			mutate: func(member *taskworkspace.DeclaredStateMember) {
				member.LogicalMember = "state/renamed.json"
			},
		},
		{
			name: "unsafe logical path",
			mutate: func(member *taskworkspace.DeclaredStateMember) {
				member.LogicalMember = "../secret"
			},
		},
		{
			name: "excluded class",
			mutate: func(member *taskworkspace.DeclaredStateMember) {
				member.Class = taskworkspace.StateMemberRuntimeRelease
			},
		},
		{
			name: "content digest",
			mutate: func(member *taskworkspace.DeclaredStateMember) {
				member.ContentDigest = "sha256:1dde25249fd4b6cbedb58974a4e89c06c5741fee860b2e7faf35cd9bfd3debaf"
			},
		},
		{
			name: "content size",
			mutate: func(member *taskworkspace.DeclaredStateMember) {
				member.Size++
			},
		},
		{
			name: "unsafe member type",
			mutate: func(member *taskworkspace.DeclaredStateMember) {
				member.Type = taskworkspace.StateMemberSymbolicLink
			},
		},
		{
			name: "unsafe member mode",
			mutate: func(member *taskworkspace.DeclaredStateMember) {
				member.Mode = 0o4600
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			durable := &happyDurableObject{
				mutateRequest: func(manifest *taskworkspace.DeclaredStateManifest) {
					test.mutate(&manifest.Members[0])
				},
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
			originalMember := manifest.Members[0]
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
				t.Fatal("adapter-mutated semantic manifest returned Checkpoint or Revision authority")
			}
			if manifest.Members[0] != originalMember {
				t.Fatal("Durable Object mutation escaped its copied manifest input")
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
			name: "canonical manifest omits member content identity",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.Manifest.Members[0].ContentID = ""
				content.Manifest.Digest = content.Manifest.CanonicalDigest()
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
			name: "receipt without opaque adapter identity",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].DurabilityAdapterID = ""
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
			name: "first receipt claims an unknown predecessor",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].Replaces = taskworkspace.DurabilityReplacementProof{
					ReceiptID:    "receipt-not-current",
					GenerationID: "durability-generation-not-current",
				}
				content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
			},
		},
		{
			name: "receipt carries an incomplete replacement proof",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].Replaces.ReceiptID = "receipt-not-current"
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
			name: "canonical manifest changed after commit",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.Manifest.Members[0].ContentID = "content-not-authoritative"
				content.Manifest.Digest = content.Manifest.CanonicalDigest()
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
			name: "receipt for stale immutable generation",
			mutate: func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1].DurabilityGenerationID = "durability-generation-stale"
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

func TestMaterializeRejectsAReceiptFromTheGenerationBeforeExactRepair(t *testing.T) {
	durable := &happyDurableObject{}
	lifecycle, current, committed := committedCheckpointForReceiptAuthority(t, durable)
	staleReceipt := committed.CheckpointEvidence.DurabilityReceipts[1]

	durable.verifyMutate = func(content *taskworkspace.VerifiedCheckpointContent) {
		prior := content.DurabilityReceipts[1]
		repaired := prior
		repaired.ID = "receipt-member-repaired"
		repaired.DurableWriteID = "durable-write-member-repaired"
		repaired.DurabilityGenerationID = "durability-generation-repaired"
		repaired.Replaces = taskworkspace.DurabilityReplacementProof{
			ReceiptID:    prior.ID,
			GenerationID: prior.DurabilityGenerationID,
		}
		repaired.VerifiedAt = repaired.VerifiedAt.Add(time.Minute)
		repaired.EvidenceDigest = repaired.CanonicalDigest()
		content.DurabilityReceipts[1] = repaired
	}
	if _, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-repaired"),
	); err != nil {
		t.Fatalf("materialize exactly repaired Checkpoint: %v", err)
	}

	durable.verifyMutate = func(content *taskworkspace.VerifiedCheckpointContent) {
		content.DurabilityReceipts[1] = staleReceipt
	}
	result, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-stale-generation"),
	)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
		t.Fatalf("materialize error = %T/%v, want typed integrity failure", err, err)
	}
	if result.MaterializationID != "" || result.CheckpointID != "" {
		t.Fatal("stale pre-repair receipt returned materialization or Checkpoint authority")
	}
}

func TestMaterializeRejectsReplacementReceiptWithoutCurrentSupersessionProof(t *testing.T) {
	durable := &happyDurableObject{}
	lifecycle, current, _ := committedCheckpointForReceiptAuthority(t, durable)
	durable.verifyMutate = func(content *taskworkspace.VerifiedCheckpointContent) {
		replacement := content.DurabilityReceipts[1]
		replacement.ID = "receipt-member-replacement"
		replacement.DurableWriteID = "durable-write-member-replacement"
		replacement.DurabilityGenerationID = "durability-generation-replacement"
		replacement.VerifiedAt = replacement.VerifiedAt.Add(time.Minute)
		replacement.EvidenceDigest = replacement.CanonicalDigest()
		content.DurabilityReceipts[1] = replacement
	}

	result, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-without-supersession"),
	)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
		t.Fatalf("materialize error = %T/%v, want typed integrity failure", err, err)
	}
	if result.MaterializationID != "" || result.CheckpointID != "" {
		t.Fatal("unproven replacement receipt returned materialization or Checkpoint authority")
	}
}

func TestReceiptAuthorityRejectsStaleGenerationAndReverseReplacement(t *testing.T) {
	tests := []struct {
		name      string
		candidate func(taskworkspace.DurabilityReceipt, taskworkspace.DurabilityReceipt) taskworkspace.DurabilityReceipt
	}{
		{
			name: "first-seen receipt reuses a stale generation",
			candidate: func(initial, current taskworkspace.DurabilityReceipt) taskworkspace.DurabilityReceipt {
				return replacementReceipt(current, "receipt-member-stale-unseen", initial.DurabilityGenerationID)
			},
		},
		{
			name: "replacement points behind current receipt",
			candidate: func(initial, current taskworkspace.DurabilityReceipt) taskworkspace.DurabilityReceipt {
				reverse := replacementReceipt(current, "receipt-member-reverse", "durability-generation-reverse")
				reverse.Replaces = taskworkspace.DurabilityReplacementProof{
					ReceiptID:    initial.ID,
					GenerationID: initial.DurabilityGenerationID,
				}
				reverse.EvidenceDigest = reverse.CanonicalDigest()
				return reverse
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			durable := &happyDurableObject{}
			lifecycle, current, committed := committedCheckpointForReceiptAuthority(t, durable)
			initial := committed.CheckpointEvidence.DurabilityReceipts[1]
			durable.verifyMutate = func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1] = replacementReceipt(
					content.DurabilityReceipts[1],
					"receipt-member-current",
					"durability-generation-current",
				)
			}
			replaced, err := lifecycle.Materialize(
				context.Background(),
				materializeRequest("policy-domain-1", "task-1", current, "materialize-replacement"),
			)
			if err != nil {
				t.Fatalf("materialize verified replacement: %v", err)
			}
			currentReceipt := replaced.CheckpointEvidence.DurabilityReceipts[1]
			durable.verifyMutate = func(content *taskworkspace.VerifiedCheckpointContent) {
				content.DurabilityReceipts[1] = test.candidate(initial, currentReceipt)
			}

			result, err := lifecycle.Materialize(
				context.Background(),
				materializeRequest("policy-domain-1", "task-1", current, "materialize-invalid-replacement"),
			)
			var lifecycleError *taskworkspace.Error
			if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
				t.Fatalf("materialize error = %T/%v, want typed integrity failure", err, err)
			}
			if result.MaterializationID != "" || result.CheckpointID != "" {
				t.Fatal("invalid receipt replacement returned materialization or Checkpoint authority")
			}
		})
	}
}

func TestReplacementReceiptExactReplayDoesNotAdvanceAuthorityAgain(t *testing.T) {
	durable := &happyDurableObject{}
	lifecycle, current, _ := committedCheckpointForReceiptAuthority(t, durable)
	durable.verifyMutate = func(content *taskworkspace.VerifiedCheckpointContent) {
		content.DurabilityReceipts[1] = replacementReceipt(
			content.DurabilityReceipts[1],
			"receipt-member-current",
			"durability-generation-current",
		)
	}
	request := materializeRequest("policy-domain-1", "task-1", current, "materialize-replacement")
	first, err := lifecycle.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("materialize verified replacement: %v", err)
	}
	replayed, err := lifecycle.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("replay verified replacement: %v", err)
	}
	if !reflect.DeepEqual(replayed, first) || durable.verified != 1 {
		t.Fatal("exact replay changed replacement evidence or repeated receipt advancement")
	}

	durable.verifyMutate = nil
	currentResult, err := lifecycle.Materialize(
		context.Background(),
		materializeRequest("policy-domain-1", "task-1", current, "materialize-current-receipt"),
	)
	if err != nil {
		t.Fatalf("materialize current receipt after replay: %v", err)
	}
	if currentResult.CheckpointEvidence.DurabilityReceipts[1] != first.CheckpointEvidence.DurabilityReceipts[1] ||
		durable.verified != 2 {
		t.Fatal("exact replay corrupted current receipt authority")
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
		committed.CheckpointEvidence.IntegrityEvidence.ManifestDigest != committed.CheckpointEvidence.Manifest.Digest ||
		committed.CheckpointEvidence.IntegrityEvidence.ValidationEvidenceID != validation.ID {
		t.Fatal("Checkpoint integrity evidence is not bound to the resulting Checkpoint, Revision, manifest, and validation evidence")
	}
	manifestReference := committed.CheckpointEvidence.ManifestReference
	memberReference := committed.CheckpointEvidence.ContentReferences[0]
	checkpointManifest := committed.CheckpointEvidence.Manifest
	if checkpointManifest.DeclaredStateDigest != manifest.Digest ||
		checkpointManifest.Digest != checkpointManifest.CanonicalDigest() ||
		len(checkpointManifest.Members) != 1 ||
		checkpointManifest.Members[0].ContentID != memberReference.ContentID ||
		checkpointManifest.Members[0].ContentDigest != manifest.Members[0].ContentDigest ||
		manifestReference.ContentDigest != checkpointManifest.Digest ||
		manifestReference.Size != uint64(len(checkpointManifest.CanonicalBytes())) ||
		manifestReference.Type != taskworkspace.CheckpointManifestReference ||
		manifestReference.CheckpointID != committed.CheckpointID || manifestReference.RevisionID != committed.RevisionID ||
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
		if receipt.DurableWriteID == "" || receipt.DurabilityAdapterID == "" || receipt.DurabilityGenerationID == "" ||
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

func TestCheckpointEvidenceReturnedByLifecycleCannotMutateAuthorityOrReplay(t *testing.T) {
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
	committed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	if err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	originalLogicalMember := committed.CheckpointEvidence.Manifest.Members[0].LogicalMember
	originalContentReference := committed.CheckpointEvidence.ContentReferences[0]
	originalReceipt := committed.CheckpointEvidence.DurabilityReceipts[0]

	committed.CheckpointEvidence.Manifest.Members[0].LogicalMember = "caller-mutated"
	committed.CheckpointEvidence.ContentReferences[0].ContentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	committed.CheckpointEvidence.DurabilityReceipts[0].Decision = "caller-mutated"

	replayedCommit, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	if err != nil {
		t.Fatalf("replay Checkpoint commit: %v", err)
	}
	if replayedCommit.CheckpointEvidence.Manifest.Members[0].LogicalMember != originalLogicalMember ||
		replayedCommit.CheckpointEvidence.ContentReferences[0] != originalContentReference ||
		replayedCommit.CheckpointEvidence.DurabilityReceipts[0] != originalReceipt {
		t.Fatal("caller mutation changed the authoritative exact-replay result")
	}

	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-2"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	materialize := materializeRequest("policy-domain-1", "task-1", current, "materialize-2")
	materialized, err := lifecycle.Materialize(context.Background(), materialize)
	if err != nil {
		t.Fatalf("materialize after mutating returned evidence: %v", err)
	}
	originalMaterializedMember := materialized.CheckpointEvidence.Manifest.Members[0]
	originalMaterializedReference := materialized.CheckpointEvidence.ContentReferences[0]
	originalMaterializedReceipt := materialized.CheckpointEvidence.DurabilityReceipts[0]
	materialized.CheckpointEvidence.Manifest.Members[0].LogicalMember = "caller-mutated-again"
	materialized.CheckpointEvidence.ContentReferences[0].Size++
	materialized.CheckpointEvidence.DurabilityReceipts[0].DurabilityGenerationID = "caller-mutated"

	replayedMaterialization, err := lifecycle.Materialize(context.Background(), materialize)
	if err != nil {
		t.Fatalf("replay Checkpoint materialization: %v", err)
	}
	if replayedMaterialization.CheckpointEvidence.Manifest.Members[0] != originalMaterializedMember ||
		replayedMaterialization.CheckpointEvidence.ContentReferences[0] != originalMaterializedReference ||
		replayedMaterialization.CheckpointEvidence.DurabilityReceipts[0] != originalMaterializedReceipt {
		t.Fatal("caller mutation changed the authoritative materialization replay")
	}
}

func TestLifecycleErrorReturnedByCommitCannotMutateExactReplay(t *testing.T) {
	durable := &happyDurableObject{prepareError: errors.New("durable fault")}
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
	request := commitRequest(confirmed, view, manifest, validation, "commit-1")
	_, err := lifecycle.CommitRuntimeView(context.Background(), request)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
		t.Fatalf("commit error = %T/%v, want typed integrity failure", err, err)
	}
	lifecycleError.Code = taskworkspace.ErrorInvalidIntent

	_, replayErr := lifecycle.CommitRuntimeView(context.Background(), request)
	var replayLifecycleError *taskworkspace.Error
	if !errors.As(replayErr, &replayLifecycleError) || replayLifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
		t.Fatalf("replay error = %T/%v, want original typed integrity failure", replayErr, replayErr)
	}
	if durable.prepared != 1 {
		t.Fatal("exact failure replay repeated Durable Object preparation")
	}
}

func TestCommitRejectsDurableEvidenceIdentityRebindingAcrossCheckpoints(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*taskworkspace.VerifiedCheckpointContent, int)
	}{
		{
			name: "typed content reference identity",
			mutate: func() func(*taskworkspace.VerifiedCheckpointContent, int) {
				var first taskworkspace.ContentReferenceID
				return func(content *taskworkspace.VerifiedCheckpointContent, prepared int) {
					if prepared == 1 {
						first = content.ContentReferences[0].ID
						return
					}
					content.ContentReferences[0].ID = first
					content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
				}
			}(),
		},
		{
			name: "content identity facts",
			mutate: func() func(*taskworkspace.VerifiedCheckpointContent, int) {
				var first taskworkspace.ContentID
				return func(content *taskworkspace.VerifiedCheckpointContent, prepared int) {
					if prepared == 1 {
						first = content.ContentReferences[0].ContentID
						return
					}
					content.ContentReferences[0].ContentID = first
					content.ContentReferences[0].EvidenceDigest = content.ContentReferences[0].CanonicalDigest()
					content.DurabilityReceipts[1].ContentID = first
					content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
				}
			}(),
		},
		{
			name: "durability receipt identity",
			mutate: func() func(*taskworkspace.VerifiedCheckpointContent, int) {
				var first taskworkspace.DurabilityReceiptID
				return func(content *taskworkspace.VerifiedCheckpointContent, prepared int) {
					if prepared == 1 {
						first = content.DurabilityReceipts[1].ID
						return
					}
					content.DurabilityReceipts[1].ID = first
					content.DurabilityReceipts[1].EvidenceDigest = content.DurabilityReceipts[1].CanonicalDigest()
				}
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			durable := &happyDurableObject{}
			durable.mutate = func(content *taskworkspace.VerifiedCheckpointContent) {
				test.mutate(content, durable.prepared)
			}
			lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
				ValidationAuthorityID: "validation-authority-1",
				DurabilityAuthorityID: "durability-authority-1",
				DurableObject:         durable,
			})
			firstConfirmed, firstView := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
			)
			firstManifest := declaredStateManifest("content-1")
			firstValidation := acceptedValidationEvidence(firstConfirmed, firstView, firstManifest)
			if _, err := lifecycle.CommitRuntimeView(
				context.Background(),
				commitRequest(firstConfirmed, firstView, firstManifest, firstValidation, "commit-1"),
			); err != nil {
				t.Fatalf("commit first Checkpoint: %v", err)
			}

			secondConfirmed, secondView := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-2", "confirm-2", "materialize-2", "open-view-2",
			)
			secondManifest := declaredStateManifest("content-2")
			secondValidation := acceptedValidationEvidence(secondConfirmed, secondView, secondManifest)
			secondValidation.ID = "validation-evidence-2"
			secondValidation.TaskID = "task-2"
			secondValidation.Digest = secondValidation.CanonicalDigest()
			secondCommit := commitRequest(secondConfirmed, secondView, secondManifest, secondValidation, "commit-2")
			secondCommit.TaskID = "task-2"
			secondCommit.Operation.RequestDigest = secondCommit.CanonicalRequestDigest()
			result, err := lifecycle.CommitRuntimeView(
				context.Background(),
				secondCommit,
			)
			var lifecycleError *taskworkspace.Error
			if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityFailure {
				t.Fatalf("commit error = %T/%v, want typed integrity failure", err, err)
			}
			if result.CheckpointID != "" || result.RevisionID != "" {
				t.Fatal("identity rebinding returned Checkpoint or Revision authority")
			}
		})
	}
}

type happyDurableObject struct {
	prepared      int
	verified      int
	prepareError  error
	mutateRequest func(*taskworkspace.DeclaredStateManifest)
	mutate        func(*taskworkspace.VerifiedCheckpointContent)
	verifyError   error
	verifyMutate  func(*taskworkspace.VerifiedCheckpointContent)
}

func (d *happyDurableObject) PrepareCheckpoint(
	_ context.Context,
	request taskworkspace.PrepareCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	d.prepared++
	if d.prepareError != nil {
		return taskworkspace.VerifiedCheckpointContent{}, d.prepareError
	}
	if d.mutateRequest != nil {
		d.mutateRequest(&request.Manifest)
	}
	member := request.Manifest.Members[0]
	memberReference := durableReference(
		"reference-member-"+string(request.CheckpointID),
		taskworkspace.CheckpointMemberReference,
		request,
		member.ID,
		member.LogicalMember,
		"content-member-"+string(request.CheckpointID),
		member.ContentDigest,
		member.Size,
	)
	manifest := checkpointManifestFromDeclared(request.Manifest, []taskworkspace.ContentReference{memberReference})
	manifestReference := durableReference(
		"reference-manifest-"+string(request.CheckpointID),
		taskworkspace.CheckpointManifestReference,
		request,
		"",
		"",
		"content-manifest-"+string(request.CheckpointID),
		manifest.Digest,
		uint64(len(manifest.CanonicalBytes())),
	)
	content := taskworkspace.VerifiedCheckpointContent{
		Manifest:          manifest,
		ManifestReference: manifestReference,
		ContentReferences: []taskworkspace.ContentReference{memberReference},
		DurabilityReceipts: []taskworkspace.DurabilityReceipt{
			durableReceipt("receipt-manifest-"+string(request.CheckpointID), manifestReference),
			durableReceipt("receipt-member-"+string(request.CheckpointID), memberReference),
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

func checkpointManifestFromDeclared(
	declared taskworkspace.DeclaredStateManifest,
	references []taskworkspace.ContentReference,
) taskworkspace.CheckpointManifest {
	referencesByMember := make(map[taskworkspace.StateMemberID]taskworkspace.ContentReference, len(references))
	for _, reference := range references {
		referencesByMember[reference.StateMemberID] = reference
	}
	manifest := taskworkspace.CheckpointManifest{
		DeclaredStateDigest: declared.Digest,
		Members:             make([]taskworkspace.CheckpointManifestMember, len(declared.Members)),
	}
	for index, member := range declared.Members {
		manifest.Members[index] = taskworkspace.CheckpointManifestMember{
			ID:            member.ID,
			LogicalMember: member.LogicalMember,
			Type:          member.Type,
			Mode:          member.Mode,
			Class:         member.Class,
			ContentID:     referencesByMember[member.ID].ContentID,
			ContentDigest: member.ContentDigest,
			Size:          member.Size,
		}
	}
	manifest.Digest = manifest.CanonicalDigest()
	return manifest
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
		DurabilityAdapterID:    "durability-adapter-1",
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

func replacementReceipt(
	current taskworkspace.DurabilityReceipt,
	id taskworkspace.DurabilityReceiptID,
	generation taskworkspace.DurabilityGenerationID,
) taskworkspace.DurabilityReceipt {
	replacement := current
	replacement.ID = id
	replacement.DurableWriteID = taskworkspace.DurableWriteID("durable-write-" + string(id))
	replacement.DurabilityGenerationID = generation
	replacement.Replaces = taskworkspace.DurabilityReplacementProof{
		ReceiptID:    current.ID,
		GenerationID: current.DurabilityGenerationID,
	}
	replacement.VerifiedAt = current.VerifiedAt.Add(time.Minute)
	replacement.EvidenceDigest = replacement.CanonicalDigest()
	return replacement
}

func committedCheckpointForReceiptAuthority(
	t *testing.T,
	durable *happyDurableObject,
) (taskworkspace.Lifecycle, taskworkspace.ConfirmTaskWorkspaceResult, taskworkspace.CommitRuntimeViewResult) {
	t.Helper()
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
	return lifecycle, current, committed
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
