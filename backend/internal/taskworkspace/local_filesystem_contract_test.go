package taskworkspace_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestLifecycleAdaptersPassValidatedCommitTracer(t *testing.T) {
	for _, adapter := range lifecycleContractAdapters() {
		adapter := adapter
		t.Run(adapter.name, func(t *testing.T) {
			lifecycle := adapter.new(t)
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
				t.Fatalf("commit validated Runtime View: %v", err)
			}
			if committed.RevisionID == "" || committed.CheckpointID == "" ||
				committed.ContentEvidenceRoot == "" || committed.DurabilityEvidenceRoot == "" {
				t.Fatal("commit omitted authoritative Revision, Checkpoint, or durability evidence")
			}
		})
	}
}

func TestLocalFilesystemReservesBytesAndInodesBeforeWriting(t *testing.T) {
	writeStarted := false
	lifecycle := newLocalContractLifecycle(t, func(config *taskworkspace.LocalFilesystemConfig) {
		config.Capacity = func() (taskworkspace.LocalFilesystemCapacity, error) {
			return taskworkspace.LocalFilesystemCapacity{AvailableBytes: 1, AvailableInodes: 1}, nil
		}
		config.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
			if event.Point == taskworkspace.LocalFaultBeforeWrite {
				writeStarted = true
			}
			return nil
		}
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)

	result, err := lifecycle.CommitRuntimeView(
		context.Background(),
		commitRequest(confirmed, view, manifest, validation, "commit-capacity"),
	)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorResourceExhausted)
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("resource exhaustion returned committed authority")
	}
	if writeStarted {
		t.Fatal("local adapter started a temporary write before reserving bytes and inodes")
	}
}

func TestLocalFilesystemReservesTemporaryPeakBeforeMaterialization(t *testing.T) {
	maximum := ^uint64(0)
	for _, test := range []struct {
		name     string
		capacity taskworkspace.LocalFilesystemCapacity
	}{
		{
			name: "temporary peak bytes",
			capacity: taskworkspace.LocalFilesystemCapacity{
				AvailableBytes: 20, AvailableInodes: maximum,
			},
		},
		{
			name: "temporary peak inodes",
			capacity: taskworkspace.LocalFilesystemCapacity{
				AvailableBytes: maximum, AvailableInodes: 4,
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			available := taskworkspace.LocalFilesystemCapacity{
				AvailableBytes: maximum, AvailableInodes: maximum,
			}
			promotionStarted := false
			lifecycle := newLocalContractLifecycle(t, func(config *taskworkspace.LocalFilesystemConfig) {
				config.Capacity = func() (taskworkspace.LocalFilesystemCapacity, error) {
					return available, nil
				}
				config.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
					if event.OperationID == "materialize-capacity-low" &&
						event.Point == taskworkspace.LocalFaultBeforePromotion {
						promotionStarted = true
					}
					return nil
				}
			})
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-capacity", "materialize-capacity-base", "open-capacity",
			)
			manifest := declaredStateManifest("content-1")
			if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
				confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
				"commit-capacity-materialization",
			)); err != nil {
				t.Fatalf("commit Checkpoint: %v", err)
			}
			current, err := lifecycle.ConfirmTaskWorkspace(
				context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-capacity-current"),
			)
			if err != nil {
				t.Fatalf("confirm committed state: %v", err)
			}

			available = test.capacity
			result, err := lifecycle.Materialize(context.Background(), materializeRequest(
				"policy-domain-1", "task-1", current, "materialize-capacity-low",
			))
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorResourceExhausted)
			if result.MaterializationID != "" || promotionStarted {
				t.Fatal("resource exhaustion exposed or began promoting a physical generation")
			}

			available = taskworkspace.LocalFilesystemCapacity{
				AvailableBytes: maximum, AvailableInodes: maximum,
			}
			recovered, err := lifecycle.Materialize(context.Background(), materializeRequest(
				"policy-domain-1", "task-1", current, "materialize-capacity-recovered",
			))
			if err != nil || recovered.MaterializationID == "" {
				t.Fatalf("materialize after restoring capacity: %#v, err = %v", recovered, err)
			}
		})
	}
}

func TestLocalFilesystemDurabilityFaultsFailClosedAtDeterministicBoundaries(t *testing.T) {
	tests := []struct {
		point taskworkspace.LocalFilesystemFaultPoint
		code  taskworkspace.ErrorCode
	}{
		{taskworkspace.LocalFaultBeforeWrite, taskworkspace.ErrorDurabilityUnverified},
		{taskworkspace.LocalFaultAfterWrite, taskworkspace.ErrorDurabilityUnverified},
		{taskworkspace.LocalFaultBeforeFileSync, taskworkspace.ErrorDurabilityUnverified},
		{taskworkspace.LocalFaultAfterFileSync, taskworkspace.ErrorDurabilityUnverified},
		{taskworkspace.LocalFaultBeforePromotion, taskworkspace.ErrorDurabilityUnverified},
		{taskworkspace.LocalFaultBeforeDirectorySync, taskworkspace.ErrorReconciliationRequired},
		{taskworkspace.LocalFaultAfterDirectorySync, taskworkspace.ErrorReconciliationRequired},
		{taskworkspace.LocalFaultAfterPromotion, taskworkspace.ErrorReconciliationRequired},
		{taskworkspace.LocalFaultBeforeReadback, taskworkspace.ErrorDurabilityUnverified},
		{taskworkspace.LocalFaultAfterReadback, taskworkspace.ErrorDurabilityUnverified},
	}
	for _, test := range tests {
		t.Run(string(test.point), func(t *testing.T) {
			faulted := false
			lifecycle := newLocalContractLifecycle(t, func(config *taskworkspace.LocalFilesystemConfig) {
				config.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
					if !faulted && event.Point == test.point && event.OperationID == "commit-fault" {
						faulted = true
						return errors.New("injected local adapter fault with path-do-not-disclose")
					}
					return nil
				}
			})
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
			)
			manifest := declaredStateManifest("content-1")
			validation := acceptedValidationEvidence(confirmed, view, manifest)

			result, err := lifecycle.CommitRuntimeView(
				context.Background(),
				commitRequest(confirmed, view, manifest, validation, "commit-fault"),
			)
			assertLifecycleErrorCode(t, err, test.code)
			if !faulted {
				t.Fatalf("fault point %q was not reached", test.point)
			}
			if result.RevisionID != "" || result.CheckpointID != "" {
				t.Fatal("faulted local durability returned committed authority")
			}
			if strings.Contains(err.Error(), "path-do-not-disclose") {
				t.Fatal("local filesystem error leaked adapter-private detail")
			}
		})
	}
}

func TestLocalFilesystemReceiptsRequireIndependentReadback(t *testing.T) {
	lifecycle := newLocalContractLifecycle(t, nil)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	committed, err := lifecycle.CommitRuntimeView(
		context.Background(),
		commitRequest(confirmed, view, manifest, validation, "commit-readback"),
	)
	if err != nil {
		t.Fatalf("commit validated Runtime View: %v", err)
	}
	for _, receipt := range committed.CheckpointEvidence.DurabilityReceipts {
		if receipt.VerificationMethod != taskworkspace.VerificationIndependentReadback ||
			receipt.DurabilityGenerationID == "" || receipt.ContentDigest == "" || receipt.Size == 0 {
			t.Fatal("local receipt omitted immutable generation or independent digest/size readback")
		}
	}
}

func TestLocalFilesystemMaterializationUsesAtomicPromotion(t *testing.T) {
	var points []taskworkspace.LocalFilesystemFaultPoint
	lifecycle := newLocalContractLifecycle(t, func(config *taskworkspace.LocalFilesystemConfig) {
		config.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
			if event.OperationID == "materialize-promoted" {
				points = append(points, event.Point)
			}
			return nil
		}
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-promoted", "materialize-promoted-base", "open-promoted",
	)
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-promoted",
	)); err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	current, _ := lifecycle.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-promoted-current"),
	)
	if _, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-promoted",
	)); err != nil {
		t.Fatalf("materialize Checkpoint after points %v: %v", points, err)
	}
	wantOrder := []taskworkspace.LocalFilesystemFaultPoint{
		taskworkspace.LocalFaultBeforeReadback,
		taskworkspace.LocalFaultAfterReadback,
		taskworkspace.LocalFaultBeforeDirectorySync,
		taskworkspace.LocalFaultAfterDirectorySync,
		taskworkspace.LocalFaultBeforePromotion,
		taskworkspace.LocalFaultAfterPromotion,
		taskworkspace.LocalFaultBeforeReadback,
		taskworkspace.LocalFaultAfterReadback,
	}
	if !faultPointsAppearInOrder(points, wantOrder) {
		t.Fatalf("materialization durability points = %v, want ordered subset %v", points, wantOrder)
	}
}

func TestLocalFilesystemRestartReconcilesPromotedAmbiguityFromLifecycleJournal(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	faulted := false
	first := newLocalContractLifecycleAtRoot(t, root, config, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
			if !faulted && event.OperationID == "commit-restart" &&
				event.Point == taskworkspace.LocalFaultAfterPromotion {
				faulted = true
				return errors.New("simulated acknowledgement loss")
			}
			return nil
		}
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, first, "task-1", "confirm-restart", "materialize-restart-base", "open-restart",
	)
	manifest := declaredStateManifest("content-1")
	commit := commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-restart",
	)
	_, err := first.CommitRuntimeView(context.Background(), commit)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if !faulted {
		t.Fatal("restart scenario did not cross the promoted-generation ambiguity boundary")
	}

	restarted := newLocalContractLifecycleAtRoot(t, root, config, nil)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: commit.PolicyDomainID,
		TaskID:         commit.TaskID,
		OperationID:    commit.Operation.ID,
	})
	if err != nil || reconciled.CommitRuntimeView == nil || reconciled.Disposition != taskworkspace.OperationTerminal {
		t.Fatalf("restart reconciliation = %#v, err = %v", reconciled, err)
	}
	replayed, err := restarted.CommitRuntimeView(context.Background(), commit)
	if err != nil || !reflect.DeepEqual(replayed, *reconciled.CommitRuntimeView) {
		t.Fatal("restart replay changed the authoritative commit decision")
	}
}

func TestLocalFilesystemRestoredMaterializationCleansUpItsExactPhysicalGeneration(t *testing.T) {
	now := taskworkspace.Instant(100)
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	config := taskworkspaceTestConfig(nil)
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID: "expiry-policy-1", MaterializationLifetime: 10, RuntimeViewLifetime: 10,
	}
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id && id != ""
	}
	cleanupStarted := false
	lifecycle := newLocalContractLifecycleWithConfig(t, config, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
			if event.OperationID == "expire-restored" && event.Point == taskworkspace.LocalFaultBeforeCleanup {
				cleanupStarted = true
			}
			return nil
		}
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-restore-cleanup", "materialize-restore-cleanup-base", "open-restore-cleanup",
	)
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
		"commit-restore-cleanup",
	)); err != nil {
		t.Fatalf("commit restore Checkpoint: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-restore-cleanup-current"),
	)
	if err != nil {
		t.Fatalf("confirm committed state: %v", err)
	}
	recoveryIntent = authorizedCheckpointRestoreIntent(current, "restore-cleanup-intent")
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent: recoveryIntent, Operation: taskworkspace.Operation{ID: "restore-cleanup"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
	restored, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
	if err != nil {
		t.Fatalf("restore exact Checkpoint: %v", err)
	}

	now = 110
	expire := taskworkspace.ExpireMaterializationRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: restored.TaskWorkspaceID, MaterializationID: restored.MaterializationID,
		RevisionID: restored.RevisionID, CheckpointID: restored.CheckpointID,
		Generation: restored.Generation, Fence: restored.Fence, ExpiryPolicyID: "expiry-policy-1",
		Operation: taskworkspace.Operation{ID: "expire-restored"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	if _, err := lifecycle.ExpireMaterialization(context.Background(), expire); err != nil {
		t.Fatalf("expire restored materialization: %v", err)
	}
	if !cleanupStarted {
		t.Fatal("restored materialization did not target its exact local physical generation")
	}
}

func faultPointsAppearInOrder(
	points []taskworkspace.LocalFilesystemFaultPoint,
	want []taskworkspace.LocalFilesystemFaultPoint,
) bool {
	index := 0
	for _, point := range points {
		if index < len(want) && point == want[index] {
			index++
		}
	}
	return index == len(want)
}

type lifecycleContractAdapter struct {
	name                string
	new                 func(*testing.T) taskworkspace.Lifecycle
	newConfigured       func(*testing.T, taskworkspace.InMemoryConfig) taskworkspace.Lifecycle
	newAmbiguousCleanup func(*testing.T, taskworkspace.InMemoryConfig) (taskworkspace.Lifecycle, func())
}

func lifecycleContractAdapters() []lifecycleContractAdapter {
	return []lifecycleContractAdapter{
		{
			name: "deterministic in-memory",
			new: func(_ *testing.T) taskworkspace.Lifecycle {
				return taskworkspace.NewInMemory(taskworkspaceTestConfig(&happyDurableObject{}))
			},
			newConfigured: func(_ *testing.T, config taskworkspace.InMemoryConfig) taskworkspace.Lifecycle {
				if config.DurableObject == nil {
					config.DurableObject = &happyDurableObject{}
				}
				return taskworkspace.NewInMemory(config)
			},
			newAmbiguousCleanup: func(_ *testing.T, config taskworkspace.InMemoryConfig) (taskworkspace.Lifecycle, func()) {
				cleanup := &exactGenerationCleanupDouble{
					inspectionDisposition: taskworkspace.CleanupInspectionEligible,
					attemptError:          taskworkspace.ErrCleanupResultAmbiguous,
				}
				config.Cleanup = cleanup
				if config.DurableObject == nil {
					config.DurableObject = &happyDurableObject{}
				}
				return taskworkspace.NewInMemory(config), func() {
					cleanup.attemptError = nil
					cleanup.attemptOutcome = taskworkspace.CleanupAlreadyAbsent
				}
			},
		},
		{
			name: "local filesystem",
			new: func(t *testing.T) taskworkspace.Lifecycle {
				return newLocalContractLifecycle(t, nil)
			},
			newConfigured: func(t *testing.T, config taskworkspace.InMemoryConfig) taskworkspace.Lifecycle {
				return newLocalContractLifecycleWithConfig(t, config, nil)
			},
			newAmbiguousCleanup: func(t *testing.T, config taskworkspace.InMemoryConfig) (taskworkspace.Lifecycle, func()) {
				faulted := false
				lifecycle := newLocalContractLifecycleWithConfig(t, config, func(local *taskworkspace.LocalFilesystemConfig) {
					local.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
						if !faulted && event.Point == taskworkspace.LocalFaultBeforeCleanup {
							faulted = true
							return errors.New("simulated ambiguous cleanup inspection")
						}
						return nil
					}
				})
				return lifecycle, func() {}
			},
		},
		{
			name: "owned transport",
			new: func(t *testing.T) taskworkspace.Lifecycle {
				return ownedTransportContractLifecycle(t, taskworkspaceTestConfig(&happyDurableObject{}))
			},
			newConfigured: ownedTransportContractLifecycle,
			newAmbiguousCleanup: func(t *testing.T, config taskworkspace.InMemoryConfig) (taskworkspace.Lifecycle, func()) {
				cleanup := &exactGenerationCleanupDouble{
					inspectionDisposition: taskworkspace.CleanupInspectionEligible,
					attemptError:          taskworkspace.ErrCleanupResultAmbiguous,
				}
				config.Cleanup = cleanup
				lifecycle := ownedTransportContractLifecycle(t, config)
				return lifecycle, func() {
					cleanup.attemptError = nil
					cleanup.attemptOutcome = taskworkspace.CleanupAlreadyAbsent
				}
			},
		},
	}
}

func ownedTransportContractLifecycle(
	_ *testing.T,
	config taskworkspace.InMemoryConfig,
) taskworkspace.Lifecycle {
	if config.DurableObject == nil {
		config.DurableObject = &happyDurableObject{}
	}
	return ownedTransportLifecycle(config)
}

func localContractPayloads() map[taskworkspace.Digest][]byte {
	return map[taskworkspace.Digest][]byte{
		"sha256:c23e70927230be9d39b8237ab27c9a45cec5e1dafac3941a1dabf1df748656ca": []byte("task-owned-state-one"),
		"sha256:1dde25249fd4b6cbedb58974a4e89c06c5741fee860b2e7faf35cd9bfd3debaf": []byte("task-owned-state-two"),
	}
}

func newLocalContractLifecycle(
	t *testing.T,
	mutate func(*taskworkspace.LocalFilesystemConfig),
) taskworkspace.Lifecycle {
	t.Helper()
	return newLocalContractLifecycleWithConfig(t, taskworkspaceTestConfig(nil), mutate)
}

func newLocalContractLifecycleWithConfig(
	t *testing.T,
	config taskworkspace.InMemoryConfig,
	mutate func(*taskworkspace.LocalFilesystemConfig),
) taskworkspace.Lifecycle {
	t.Helper()
	return newLocalContractLifecycleAtRoot(t, t.TempDir(), config, mutate)
}

func newLocalContractLifecycleAtRoot(
	t *testing.T,
	root string,
	config taskworkspace.InMemoryConfig,
	mutate func(*taskworkspace.LocalFilesystemConfig),
) taskworkspace.Lifecycle {
	t.Helper()
	t.Cleanup(func() { makeLocalContractTreeRemovable(root) })
	localConfig := taskworkspace.LocalFilesystemConfig{
		Root:      root,
		Lifecycle: config,
		ContentSource: taskworkspace.LocalContentSourceFunc(func(
			_ context.Context,
			digest taskworkspace.Digest,
		) (io.ReadCloser, error) {
			payload, ok := localContractPayloads()[digest]
			if !ok {
				return nil, fmt.Errorf("content unavailable")
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		}),
	}
	if mutate != nil {
		mutate(&localConfig)
	}
	lifecycle, err := taskworkspace.NewLocalFilesystem(localConfig)
	if err != nil {
		t.Fatalf("create local filesystem adapter: %v", err)
	}
	return lifecycle
}

func makeLocalContractTreeRemovable(root string) {
	_ = filepath.WalkDir(root, func(entry string, item os.DirEntry, err error) error {
		if err == nil && item.IsDir() {
			_ = os.Chmod(entry, 0o700)
		}
		return nil
	})
}
