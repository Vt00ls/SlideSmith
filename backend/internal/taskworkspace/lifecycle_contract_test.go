package taskworkspace_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestConfirmReturnsStableTaskWorkspaceIdentity(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})

	first, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	second, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-2",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace again: %v", err)
	}

	if first.TaskWorkspaceID == "" {
		t.Fatal("confirmed Task Workspace identity is empty")
	}
	if second.TaskWorkspaceID != first.TaskWorkspaceID {
		t.Fatal("the same Task received a different Task Workspace identity")
	}
}

func TestConfirmRejectsCrossPolicyDomainBindingWithoutLeakingIdentity(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	if _, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	)); err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}

	_, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"do-not-disclose-policy-domain", "task-1", "confirm-2",
	))
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorOwnershipDenied {
		t.Fatalf("cross-policy binding error = %T, want typed ownership denial", err)
	}
	if strings.Contains(err.Error(), "do-not-disclose-policy-domain") || strings.Contains(err.Error(), "task-1") {
		t.Fatal("ownership denial disclosed submitted identity")
	}
}

func TestTaskWorkspaceIdentitiesAreNotReusedAcrossTasks(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	first, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm first Task Workspace: %v", err)
	}
	second, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-2", "confirm-2",
	))
	if err != nil {
		t.Fatalf("confirm second Task Workspace: %v", err)
	}

	if first.TaskWorkspaceID == second.TaskWorkspaceID || first.CurrentRevisionID == second.CurrentRevisionID {
		t.Fatal("different Tasks reused a Task Workspace or Revision identity")
	}
}

func TestMaterializeRejectsCrossTaskWorkspaceBindingWithoutLeakingIdentity(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	first, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-do-not-disclose-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm first Task Workspace: %v", err)
	}
	if _, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-do-not-disclose-2", "confirm-2",
	)); err != nil {
		t.Fatalf("confirm second Task Workspace: %v", err)
	}
	request := materializeRequest(
		"policy-domain-1", "task-do-not-disclose-2", first, "materialize-1",
	)

	_, err = lifecycle.Materialize(context.Background(), request)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorOwnershipDenied {
		t.Fatalf("cross-Task binding error = %T, want typed ownership denial", err)
	}
	for _, secret := range []string{
		"task-do-not-disclose-1",
		"task-do-not-disclose-2",
		string(first.TaskWorkspaceID),
		string(first.CurrentRevisionID),
	} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("ownership denial disclosed an identity")
		}
	}
}

func TestMaterializeUsesTheAuthoritativeBaseRevision(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	if confirmed.CurrentRevisionID == "" || confirmed.Generation == 0 || confirmed.Fence == 0 {
		t.Fatal("confirmation omitted authoritative base facts")
	}

	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", confirmed, "materialize-1",
	))
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	if materialized.MaterializationID == "" {
		t.Fatal("materialization identity is empty")
	}
	if materialized.TaskWorkspaceID != confirmed.TaskWorkspaceID ||
		materialized.RevisionID != confirmed.CurrentRevisionID ||
		materialized.Generation != confirmed.Generation ||
		materialized.Fence != confirmed.Fence {
		t.Fatal("materialization is not bound to the authoritative base")
	}
}

func TestMaterializeExactReplayReturnsTheOriginalResult(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	request := materializeRequest("policy-domain-1", "task-1", confirmed, "materialize-1")

	first, err := lifecycle.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	second, err := lifecycle.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("replay materialization: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatal("exact replay did not return the original materialization result")
	}
}

func TestOpenRuntimeViewIsolatesMutatingRuntimeRuns(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)

	first, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	second, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}

	if first.RuntimeViewID == "" || second.RuntimeViewID == "" || first.RuntimeViewID == second.RuntimeViewID {
		t.Fatal("mutating Runtime Runs did not receive isolated Runtime View identities")
	}
	if first.BaseRevisionID != confirmed.CurrentRevisionID || first.RuntimeRunID != "runtime-run-1" ||
		first.Generation != confirmed.Generation || first.Fence != confirmed.Fence {
		t.Fatal("first Runtime View omitted an exact authority binding")
	}
	if second.BaseRevisionID != confirmed.CurrentRevisionID || second.RuntimeRunID != "runtime-run-2" ||
		second.Generation != confirmed.Generation || second.Fence != confirmed.Fence {
		t.Fatal("second Runtime View omitted an exact authority binding")
	}
}

func TestOpenRuntimeViewExactReplayDoesNotCreateAnotherView(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)
	request := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	)

	first, err := lifecycle.OpenRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	replayed, err := lifecycle.OpenRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("replay Runtime View open: %v", err)
	}
	if replayed != first {
		t.Fatal("exact replay did not return the original Runtime View")
	}

	changed := request
	changed.RuntimeRunID = "runtime-run-2"
	changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
	_, err = lifecycle.OpenRuntimeView(context.Background(), changed)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityConflict {
		t.Fatalf("changed replay error = %T, want typed integrity conflict", err)
	}
}

func TestCommitValidatedRuntimeViewProducesRevisionAndCheckpoint(t *testing.T) {
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, view, manifest)
	request := commitRequest(confirmed, view, manifest, evidence, "commit-1")

	committed, err := lifecycle.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("commit validated Runtime View: %v", err)
	}

	if committed.RevisionID == "" || committed.RevisionID == confirmed.CurrentRevisionID {
		t.Fatal("changed state did not produce a resulting Task Workspace Revision")
	}
	if committed.CheckpointID == "" {
		t.Fatal("commit did not produce a Checkpoint")
	}
	if committed.TaskWorkspaceID != confirmed.TaskWorkspaceID ||
		committed.BaseRevisionID != confirmed.CurrentRevisionID ||
		committed.PredecessorRevisionID != confirmed.CurrentRevisionID ||
		committed.ManifestDigest != manifest.Digest ||
		committed.ValidationEvidenceID != evidence.ID ||
		committed.ValidationEvidenceDigest != evidence.Digest ||
		committed.ContentEvidenceRoot == "" || committed.DurabilityEvidenceRoot == "" ||
		committed.Generation != confirmed.Generation || committed.Fence != confirmed.Fence {
		t.Fatal("commit result omitted required lineage, manifest, evidence, generation, or fence bindings")
	}
	if committed.Operation.ID != request.Operation.ID ||
		committed.Operation.RequestDigest != request.Operation.RequestDigest {
		t.Fatal("commit result omitted the OperationID or canonical request digest")
	}
}

func TestCommitExactReplayReturnsTheOriginalTerminalResult(t *testing.T) {
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, view, manifest)
	request := commitRequest(confirmed, view, manifest, evidence, "commit-1")

	first, err := lifecycle.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("commit validated Runtime View: %v", err)
	}
	second, err := lifecycle.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("replay committed Runtime View: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatal("exact replay did not return the original terminal commit result")
	}
}

func TestCommitAllowsOnlyOneWriterFromTheSameBaseRevision(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)
	firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-2", "runtime-run-2", "sandbox-lease-2", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}

	firstManifest := declaredStateManifest("content-1")
	firstEvidence := acceptedValidationEvidence(confirmed, firstView, firstManifest)
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, firstView, firstManifest, firstEvidence, "commit-1",
	)); err != nil {
		t.Fatalf("commit first Runtime View: %v", err)
	}

	secondManifest := declaredStateManifest("content-2")
	secondEvidence := acceptedValidationEvidence(confirmed, secondView, secondManifest)
	secondEvidence.ID = "validation-evidence-2"
	secondEvidence.Digest = secondEvidence.CanonicalDigest()
	result, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, secondView, secondManifest, secondEvidence, "commit-2",
	))
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorStaleAuthority {
		t.Fatalf("second writer error = %T/%v, want typed stale authority", err, err)
	}
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("stale writer returned authoritative result identities")
	}
}

func TestOperationIDDifferentCanonicalDigestReturnsTypedIntegrityConflict(t *testing.T) {
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, view, manifest)
	request := commitRequest(confirmed, view, manifest, evidence, "commit-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), request); err != nil {
		t.Fatalf("commit validated Runtime View: %v", err)
	}

	changed := request
	changed.DeclaredStateManifest = declaredStateManifest("content-2")
	changed.ValidationEvidence.ManifestDigest = changed.DeclaredStateManifest.Digest
	changed.ValidationEvidence.Digest = changed.ValidationEvidence.CanonicalDigest()
	changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
	_, err := lifecycle.CommitRuntimeView(context.Background(), changed)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityConflict {
		t.Fatalf("OperationID reuse error = %T, want typed integrity conflict", err)
	}
}

func TestUnchangedContentCommitCreatesADistinctCheckpoint(t *testing.T) {
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	firstEvidence := acceptedValidationEvidence(confirmed, view, manifest)
	first, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, firstEvidence, "commit-1",
	))
	if err != nil {
		t.Fatalf("commit changed state: %v", err)
	}

	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-2",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-2",
	))
	if err != nil {
		t.Fatalf("materialize current Revision: %v", err)
	}
	secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, materialized,
		"phase-run-2", "runtime-run-2", "sandbox-lease-2", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}
	secondManifest := declaredStateManifest("content-1")
	secondEvidence := acceptedValidationEvidence(current, secondView, secondManifest)
	secondEvidence.ID = "validation-evidence-2"
	secondEvidence.Digest = secondEvidence.CanonicalDigest()
	secondRequest := commitRequest(current, secondView, secondManifest, secondEvidence, "commit-2")
	second, err := lifecycle.CommitRuntimeView(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("commit unchanged state: %v", err)
	}

	if second.RevisionID != first.RevisionID {
		t.Fatal("unchanged content unexpectedly created a different Task Workspace Revision")
	}
	if second.CheckpointID == "" || second.CheckpointID == first.CheckpointID {
		t.Fatal("unchanged-content commit did not create a distinct Checkpoint")
	}
	if second.PredecessorRevisionID == second.RevisionID {
		t.Fatal("unchanged-content commit reported a self-predecessor")
	}
}

func TestCommitFailsClosedUnlessEveryAuthorityAndIntegrityBindingIsExact(t *testing.T) {
	tests := []struct {
		name string
		code taskworkspace.ErrorCode
		edit func(*taskworkspace.CommitRuntimeViewRequest)
	}{
		{
			name: "missing Runtime View",
			code: taskworkspace.ErrorInvalidIntent,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.RuntimeViewID = ""
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different Runtime View",
			code: taskworkspace.ErrorOwnershipDenied,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.RuntimeViewID = "runtime-view-not-owned"
				request.ValidationEvidence.RuntimeViewID = request.RuntimeViewID
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different base Revision",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.BaseRevisionID = "revision-not-current"
				request.ExpectedCurrentRevision = request.BaseRevisionID
				request.ValidationEvidence.BaseRevisionID = request.BaseRevisionID
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different current Revision",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.ExpectedCurrentRevision = "revision-not-current"
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different generation",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.Generation++
				request.ValidationEvidence.Generation = request.Generation
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different fence",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.Fence++
				request.ValidationEvidence.Fence = request.Fence
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "missing validation evidence",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.ValidationEvidence = taskworkspace.ValidationEvidence{}
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different validation authority",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.ValidationEvidence.ValidationAuthorityID = "validation-authority-2"
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "validation not accepted",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.ValidationEvidence.Decision = "rejected"
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "manifest digest mismatch",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.DeclaredStateManifest.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				request.ValidationEvidence.ManifestDigest = request.DeclaredStateManifest.Digest
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "duplicate declared member",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.DeclaredStateManifest.Members = append(
					request.DeclaredStateManifest.Members,
					request.DeclaredStateManifest.Members[0],
				)
				request.DeclaredStateManifest.Digest = request.DeclaredStateManifest.CanonicalDigest()
				request.ValidationEvidence.ManifestDigest = request.DeclaredStateManifest.Digest
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "missing canonical request digest",
			code: taskworkspace.ErrorInvalidIntent,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.Operation.RequestDigest = ""
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
			manifest := declaredStateManifest("content-1")
			evidence := acceptedValidationEvidence(confirmed, view, manifest)
			request := commitRequest(confirmed, view, manifest, evidence, "commit-1")
			test.edit(&request)

			result, err := lifecycle.CommitRuntimeView(context.Background(), request)
			var lifecycleError *taskworkspace.Error
			if !errors.As(err, &lifecycleError) || lifecycleError.Code != test.code {
				t.Fatalf("commit error = %T/%v, want code %q", err, err, test.code)
			}
			if result.RevisionID != "" || result.CheckpointID != "" {
				t.Fatal("rejected commit returned authoritative result identities")
			}
		})
	}
}

func TestPublicLifecycleContractAndErrorsDoNotExposePhysicalDetails(t *testing.T) {
	banned := []string{
		"path",
		"session",
		"mount",
		"objectlocator",
		"objectkey",
		"bucket",
		"vendor",
		"credential",
		"sdk",
		"filesystem",
	}
	seen := map[reflect.Type]bool{}
	var inspectType func(reflect.Type)
	inspectType = func(contractType reflect.Type) {
		for contractType.Kind() == reflect.Pointer || contractType.Kind() == reflect.Slice || contractType.Kind() == reflect.Array {
			contractType = contractType.Elem()
		}
		if seen[contractType] {
			return
		}
		seen[contractType] = true
		if contractType.PkgPath() != "github.com/slidesmith/slidesmith/backend/internal/taskworkspace" {
			return
		}
		assertSafeContractName(t, contractType.Name(), banned)
		if contractType.Kind() != reflect.Struct {
			return
		}
		for index := 0; index < contractType.NumField(); index++ {
			field := contractType.Field(index)
			assertSafeContractName(t, field.Name, banned)
			inspectType(field.Type)
		}
	}

	lifecycleType := reflect.TypeOf((*taskworkspace.Lifecycle)(nil)).Elem()
	for methodIndex := 0; methodIndex < lifecycleType.NumMethod(); methodIndex++ {
		method := lifecycleType.Method(methodIndex)
		assertSafeContractName(t, method.Name, banned)
		for inputIndex := 0; inputIndex < method.Type.NumIn(); inputIndex++ {
			inspectType(method.Type.In(inputIndex))
		}
		for outputIndex := 0; outputIndex < method.Type.NumOut(); outputIndex++ {
			inspectType(method.Type.Out(outputIndex))
		}
	}
	durableObjectType := reflect.TypeOf((*taskworkspace.DurableObjectPort)(nil)).Elem()
	if durableObjectType.NumMethod() != 2 {
		t.Fatal("Durable Object port exposes authority beyond prepare and verification mechanics")
	}
	for methodIndex := 0; methodIndex < durableObjectType.NumMethod(); methodIndex++ {
		method := durableObjectType.Method(methodIndex)
		if method.Name != "PrepareCheckpoint" && method.Name != "VerifyCheckpoint" {
			t.Fatal("Durable Object port exposes listing, adoption, retention, release, or business selection authority")
		}
		assertSafeContractName(t, method.Name, banned)
		for inputIndex := 0; inputIndex < method.Type.NumIn(); inputIndex++ {
			inspectType(method.Type.In(inputIndex))
		}
		for outputIndex := 0; outputIndex < method.Type.NumOut(); outputIndex++ {
			inspectType(method.Type.Out(outputIndex))
		}
	}
	inspectType(reflect.TypeOf(taskworkspace.InMemoryConfig{}))
	inspectType(reflect.TypeOf(taskworkspace.Error{}))

	for _, code := range []taskworkspace.ErrorCode{
		taskworkspace.ErrorInvalidIntent,
		taskworkspace.ErrorIntegrityConflict,
		taskworkspace.ErrorIntegrityFailure,
		taskworkspace.ErrorOwnershipDenied,
		taskworkspace.ErrorStaleAuthority,
		taskworkspace.ErrorViewTerminalConflict,
	} {
		message := (&taskworkspace.Error{Code: code}).Error()
		for _, term := range banned {
			if strings.Contains(strings.ToLower(message), term) {
				t.Fatal("ordinary lifecycle error exposes a physical implementation term")
			}
		}
	}
}

func assertSafeContractName(t *testing.T, name string, banned []string) {
	t.Helper()
	normalized := strings.ToLower(name)
	for _, term := range banned {
		if strings.Contains(normalized, term) {
			t.Fatal("public lifecycle contract exposes a physical implementation term")
		}
	}
}

func confirmRequest(policyDomainID, taskID, operationID string) taskworkspace.ConfirmTaskWorkspaceRequest {
	request := taskworkspace.ConfirmTaskWorkspaceRequest{
		PolicyDomainID: taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:         taskworkspace.TaskID(taskID),
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func materializeRequest(
	policyDomainID, taskID string,
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	operationID string,
) taskworkspace.MaterializeRequest {
	request := taskworkspace.MaterializeRequest{
		PolicyDomainID:  taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:          taskworkspace.TaskID(taskID),
		TaskWorkspaceID: confirmed.TaskWorkspaceID,
		RevisionID:      confirmed.CurrentRevisionID,
		CheckpointID:    confirmed.CurrentCheckpointID,
		Generation:      confirmed.Generation,
		Fence:           confirmed.Fence,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func materializedTask(t *testing.T) (
	taskworkspace.Lifecycle,
	taskworkspace.ConfirmTaskWorkspaceResult,
	taskworkspace.MaterializeResult,
) {
	t.Helper()
	lifecycle := newLifecycle()
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", confirmed, "materialize-1",
	))
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	return lifecycle, confirmed, materialized
}

func openRuntimeViewRequest(
	policyDomainID, taskID string,
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	materialized taskworkspace.MaterializeResult,
	phaseRunID, runtimeRunID, sandboxLeaseID, operationID string,
) taskworkspace.OpenRuntimeViewRequest {
	request := taskworkspace.OpenRuntimeViewRequest{
		PolicyDomainID:    taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:            taskworkspace.TaskID(taskID),
		TaskWorkspaceID:   confirmed.TaskWorkspaceID,
		MaterializationID: materialized.MaterializationID,
		BaseRevisionID:    confirmed.CurrentRevisionID,
		PhaseRunID:        taskworkspace.PhaseRunID(phaseRunID),
		RuntimeRunID:      taskworkspace.RuntimeRunID(runtimeRunID),
		SandboxLeaseID:    taskworkspace.SandboxLeaseID(sandboxLeaseID),
		Generation:        confirmed.Generation,
		Fence:             confirmed.Fence,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func openedRuntimeView(
	t *testing.T,
	runtimeRunID, operationID string,
) (
	taskworkspace.Lifecycle,
	taskworkspace.ConfirmTaskWorkspaceResult,
	taskworkspace.MaterializeResult,
	taskworkspace.OpenRuntimeViewResult,
) {
	t.Helper()
	lifecycle, confirmed, materialized := materializedTask(t)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", runtimeRunID, "sandbox-lease-1", operationID,
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	return lifecycle, confirmed, materialized, view
}

func declaredStateManifest(contentID string) taskworkspace.DeclaredStateManifest {
	contentDigest, size := declaredContentFacts(contentID)
	manifest := taskworkspace.DeclaredStateManifest{
		Members: []taskworkspace.DeclaredStateMember{
			{
				ID:            "state-member-1",
				LogicalMember: "state/deck.json",
				Type:          taskworkspace.StateMemberRegularFile,
				Mode:          0o600,
				Class:         taskworkspace.StateMemberTaskOwnedMutable,
				ContentDigest: contentDigest,
				Size:          size,
			},
		},
	}
	manifest.Digest = manifest.CanonicalDigest()
	return manifest
}

func declaredContentFacts(contentID string) (taskworkspace.Digest, uint64) {
	if contentID == "content-2" {
		return "sha256:1dde25249fd4b6cbedb58974a4e89c06c5741fee860b2e7faf35cd9bfd3debaf", 20
	}
	return "sha256:c23e70927230be9d39b8237ab27c9a45cec5e1dafac3941a1dabf1df748656ca", 20
}

func newLifecycle() taskworkspace.Lifecycle {
	return taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
		DurabilityAuthorityID: "durability-authority-1",
		DurableObject:         &happyDurableObject{},
	})
}

func acceptedValidationEvidence(
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	view taskworkspace.OpenRuntimeViewResult,
	manifest taskworkspace.DeclaredStateManifest,
) taskworkspace.ValidationEvidence {
	evidence := taskworkspace.ValidationEvidence{
		ID:                    "validation-evidence-1",
		ValidationAuthorityID: "validation-authority-1",
		PolicyDomainID:        "policy-domain-1",
		TaskID:                "task-1",
		TaskWorkspaceID:       confirmed.TaskWorkspaceID,
		RuntimeViewID:         view.RuntimeViewID,
		BaseRevisionID:        confirmed.CurrentRevisionID,
		PhaseRunID:            view.PhaseRunID,
		RuntimeRunID:          view.RuntimeRunID,
		ManifestDigest:        manifest.Digest,
		Generation:            confirmed.Generation,
		Fence:                 confirmed.Fence,
		Decision:              taskworkspace.ValidationAccepted,
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence
}

func commitRequest(
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	view taskworkspace.OpenRuntimeViewResult,
	manifest taskworkspace.DeclaredStateManifest,
	evidence taskworkspace.ValidationEvidence,
	operationID string,
) taskworkspace.CommitRuntimeViewRequest {
	request := taskworkspace.CommitRuntimeViewRequest{
		PolicyDomainID:          "policy-domain-1",
		TaskID:                  "task-1",
		TaskWorkspaceID:         confirmed.TaskWorkspaceID,
		RuntimeViewID:           view.RuntimeViewID,
		BaseRevisionID:          confirmed.CurrentRevisionID,
		ExpectedCurrentRevision: confirmed.CurrentRevisionID,
		Generation:              confirmed.Generation,
		Fence:                   confirmed.Fence,
		ValidationEvidence:      evidence,
		DeclaredStateManifest:   manifest,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}
