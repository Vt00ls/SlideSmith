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
	current, err := restarted.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-reconciled-current"),
	)
	if err != nil {
		t.Fatalf("confirm reconciled current workspace: %v", err)
	}
	materialized, err := restarted.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-reconciled-commit",
	))
	if err != nil || materialized.CheckpointID != reconciled.CommitRuntimeView.CheckpointID {
		t.Fatalf("materialize immediately after reconciliation = %#v, err = %v", materialized, err)
	}
	replayed, err := restarted.CommitRuntimeView(context.Background(), commit)
	if err != nil || !reflect.DeepEqual(replayed, *reconciled.CommitRuntimeView) {
		t.Fatal("restart replay changed the authoritative commit decision")
	}
}

func TestLocalFilesystemCoreTerminalFaultRequiresRestartReconciliation(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	faulted := false
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if !faulted && event.OperationID == "commit-core-terminal-crash" &&
			event.Point == taskworkspace.FaultAfterAuthoritativeTransaction {
			faulted = true
			return errors.New("simulated crash after core terminal")
		}
		return nil
	}
	first := newLocalContractLifecycleAtRoot(t, root, config, nil)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, first, "task-1", "confirm-core-terminal-crash", "materialize-core-terminal-crash",
		"open-core-terminal-crash",
	)
	manifest := declaredStateManifest("content-1")
	commit := commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
		"commit-core-terminal-crash",
	)
	_, err := first.CommitRuntimeView(context.Background(), commit)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	inspection, err := first.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: commit.PolicyDomainID, TaskID: commit.TaskID, OperationID: commit.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationReconciliationRequired ||
		inspection.CommitRuntimeView != nil {
		t.Fatalf("post-core terminal inspection = %#v, err = %v", inspection, err)
	}

	restartedConfig := config
	restartedConfig.FaultHook = nil
	restarted := newLocalContractLifecycleAtRoot(t, root, restartedConfig, nil)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: commit.PolicyDomainID, TaskID: commit.TaskID, OperationID: commit.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
		reconciled.CommitRuntimeView == nil {
		t.Fatalf("restart core-terminal reconciliation = %#v, err = %v", reconciled, err)
	}
	current, err := restarted.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-core-terminal-current"),
	)
	if err != nil {
		t.Fatalf("confirm core-terminal current workspace: %v", err)
	}
	materialized, err := restarted.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-core-terminal-result",
	))
	if err != nil || materialized.CheckpointID != reconciled.CommitRuntimeView.CheckpointID {
		t.Fatalf("materialize reconciled core terminal = %#v, err = %v", materialized, err)
	}
}

func TestLocalFilesystemAdapterCompletionPersistenceFaultsAreRestartSafe(t *testing.T) {
	for _, test := range []struct {
		name            string
		point           taskworkspace.LocalFilesystemFaultPoint
		ordinal         int
		wantDisposition taskworkspace.OperationDisposition
	}{
		{
			name:            "adapter state mutation before persist",
			point:           taskworkspace.LocalFaultAfterCompletionMutation,
			wantDisposition: taskworkspace.OperationReconciliationRequired,
		},
		{
			name:            "completion record mutation before persist",
			point:           taskworkspace.LocalFaultAfterCompletionMutation,
			ordinal:         2,
			wantDisposition: taskworkspace.OperationReconciliationRequired,
		},
		{
			name:            "completion persist before response acknowledgement",
			point:           taskworkspace.LocalFaultAfterCompletionPersistence,
			ordinal:         -1,
			wantDisposition: taskworkspace.OperationTerminal,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			persistence := taskworkspace.NewInMemoryPersistence()
			config := taskworkspaceTestConfig(nil)
			config.Persistence = persistence
			faulted := false
			operationID := "commit-completion-" + strings.ReplaceAll(test.name, " ", "-")
			first := newLocalContractLifecycleAtRoot(t, root, config, func(local *taskworkspace.LocalFilesystemConfig) {
				local.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
					if !faulted && event.OperationID == taskworkspace.OperationID(operationID) &&
						event.Point == test.point && (test.ordinal < 0 || event.Ordinal == test.ordinal) {
						faulted = true
						return errors.New("simulated adapter completion persistence fault")
					}
					return nil
				}
			})
			confirmed, view := openRuntimeViewWithLifecycle(
				t, first, "task-1", "confirm-"+operationID, "materialize-"+operationID,
				"open-"+operationID,
			)
			manifest := declaredStateManifest("content-1")
			commit := commitRequest(
				confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), operationID,
			)
			result, err := first.CommitRuntimeView(context.Background(), commit)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
			if !faulted || result.RevisionID != "" || result.CheckpointID != "" {
				t.Fatal("completion persistence fault exposed an unacknowledged terminal result")
			}
			conflict := commit
			conflict.Fence++
			conflict.Operation.RequestDigest = conflict.CanonicalRequestDigest()
			_, err = first.CommitRuntimeView(context.Background(), conflict)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)

			stale := commit
			stale.Operation.ID = taskworkspace.OperationID(operationID + "-stale")
			stale.Generation++
			stale.Fence++
			stale.Operation.RequestDigest = stale.CanonicalRequestDigest()
			_, err = first.CommitRuntimeView(context.Background(), stale)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)

			crossWorkspace := commit
			crossWorkspace.Operation.ID = taskworkspace.OperationID(operationID + "-cross-workspace")
			crossWorkspace.TaskWorkspaceID = "foreign-workspace-canary"
			crossWorkspace.Operation.RequestDigest = crossWorkspace.CanonicalRequestDigest()
			_, err = first.CommitRuntimeView(context.Background(), crossWorkspace)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorOwnershipDenied)

			inspection, err := first.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
				PolicyDomainID: commit.PolicyDomainID, TaskID: commit.TaskID, OperationID: commit.Operation.ID,
			})
			if err != nil || inspection.Disposition != test.wantDisposition {
				t.Fatalf("completion persistence inspection = %#v, err = %v", inspection, err)
			}

			restarted := newLocalContractLifecycleAtRoot(t, root, config, nil)
			restartInspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
				PolicyDomainID: commit.PolicyDomainID, TaskID: commit.TaskID, OperationID: commit.Operation.ID,
			})
			if err != nil || restartInspection.Disposition != test.wantDisposition {
				t.Fatalf("completion persistence restart inspection = %#v, err = %v", restartInspection, err)
			}
			reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
				PolicyDomainID: commit.PolicyDomainID, TaskID: commit.TaskID, OperationID: commit.Operation.ID,
			})
			if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
				reconciled.CommitRuntimeView == nil {
				t.Fatalf("completion persistence reconciliation = %#v, err = %v", reconciled, err)
			}
			repeated, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
				PolicyDomainID: commit.PolicyDomainID, TaskID: commit.TaskID, OperationID: commit.Operation.ID,
			})
			if err != nil || !reflect.DeepEqual(repeated, reconciled) {
				t.Fatalf("repeated completion reconciliation = %#v, err = %v", repeated, err)
			}
			current, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
				"policy-domain-1", "task-1", "confirm-current-"+operationID,
			))
			if err != nil {
				t.Fatalf("confirm completion current workspace: %v", err)
			}
			if current.CurrentRevisionID != reconciled.CommitRuntimeView.RevisionID ||
				current.CurrentCheckpointID != reconciled.CommitRuntimeView.CheckpointID {
				t.Fatal("fail-closed probes changed the reconciled Revision or Checkpoint")
			}
			materialized, err := restarted.Materialize(context.Background(), materializeRequest(
				"policy-domain-1", "task-1", current, "materialize-result-"+operationID,
			))
			if err != nil || materialized.CheckpointID != reconciled.CommitRuntimeView.CheckpointID {
				t.Fatalf("materialize completion result = %#v, err = %v", materialized, err)
			}
			replayed, err := restarted.CommitRuntimeView(context.Background(), commit)
			if err != nil || !reflect.DeepEqual(replayed, *reconciled.CommitRuntimeView) {
				t.Fatalf("completion exact replay = %#v, err = %v", replayed, err)
			}
		})
	}
}

func TestLocalFilesystemCompletionIntentMutationFaultCannotBypassDurability(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	intentFaulted := false
	coreFaulted := false
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if intentFaulted && !coreFaulted && event.OperationID == "commit-intent-mutation-crash" &&
			event.Point == taskworkspace.FaultAfterAuthoritativeTransaction {
			coreFaulted = true
			return errors.New("simulated crash after core terminal")
		}
		return nil
	}
	first := newLocalContractLifecycleAtRoot(t, root, config, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
			if !intentFaulted && event.OperationID == "commit-intent-mutation-crash" &&
				event.Point == taskworkspace.LocalFaultAfterCompletionMutation && event.Ordinal == 1 {
				intentFaulted = true
				return errors.New("simulated crash after completion intent mutation")
			}
			return nil
		}
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, first, "task-1", "confirm-intent-mutation", "materialize-intent-mutation",
		"open-intent-mutation",
	)
	manifest := declaredStateManifest("content-1")
	commit := commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
		"commit-intent-mutation-crash",
	)
	_, err := first.CommitRuntimeView(context.Background(), commit)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if !intentFaulted || coreFaulted {
		t.Fatal("completion intent fault did not stop before core authority")
	}
	_, err = first.CommitRuntimeView(context.Background(), commit)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if !coreFaulted {
		t.Fatal("exact retry did not reach the core terminal boundary")
	}

	restartedConfig := config
	restartedConfig.FaultHook = nil
	restarted := newLocalContractLifecycleAtRoot(t, root, restartedConfig, nil)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: commit.PolicyDomainID, TaskID: commit.TaskID, OperationID: commit.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
		reconciled.CommitRuntimeView == nil {
		t.Fatalf("completion intent durability reconciliation = %#v, err = %v", reconciled, err)
	}
}

func TestLocalFilesystemOutOfOrderCompoundReplayDoesNotReactivateOlderCommit(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	faulted := false
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if !faulted && event.OperationID == "commit-out-of-order-older" &&
			event.Point == taskworkspace.FaultBeforeResponse {
			faulted = true
			return errors.New("simulated older commit response loss")
		}
		return nil
	}
	first := newLocalContractLifecycleAtRoot(t, root, config, nil)
	confirmed, olderView := openRuntimeViewWithLifecycle(
		t, first, "task-1", "confirm-out-of-order", "materialize-out-of-order-base",
		"open-out-of-order-older",
	)
	olderManifest := declaredStateManifest("content-1")
	olderRequest := commitRequest(
		confirmed, olderView, olderManifest, acceptedValidationEvidence(confirmed, olderView, olderManifest),
		"commit-out-of-order-older",
	)
	_, err := first.CommitRuntimeView(context.Background(), olderRequest)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if !faulted {
		t.Fatal("older commit did not reach response-loss boundary")
	}

	restartedConfig := config
	restartedConfig.FaultHook = nil
	restarted := newLocalContractLifecycleAtRoot(t, root, restartedConfig, nil)
	olderInspection, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: olderRequest.PolicyDomainID, TaskID: olderRequest.TaskID,
		OperationID: olderRequest.Operation.ID,
	})
	if err != nil || olderInspection.Disposition != taskworkspace.OperationTerminal ||
		olderInspection.CommitRuntimeView == nil {
		t.Fatalf("reconcile older compound commit = %#v, err = %v", olderInspection, err)
	}
	current, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-out-of-order-current",
	))
	if err != nil {
		t.Fatalf("confirm older commit: %v", err)
	}
	materialized, err := restarted.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-out-of-order-current",
	))
	if err != nil {
		t.Fatalf("materialize older commit: %v", err)
	}
	newerView, err := restarted.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, materialized,
		"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-out-of-order-newer",
	))
	if err != nil {
		t.Fatalf("open newer Runtime View: %v", err)
	}
	newerManifest := declaredStateManifest("content-2")
	newer, err := restarted.CommitRuntimeView(context.Background(), commitRequest(
		current, newerView, newerManifest, acceptedValidationEvidence(current, newerView, newerManifest),
		"commit-out-of-order-newer",
	))
	if err != nil {
		t.Fatalf("commit newer state: %v", err)
	}

	lateReconcile, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: olderRequest.PolicyDomainID, TaskID: olderRequest.TaskID,
		OperationID: olderRequest.Operation.ID,
	})
	if err != nil || !reflect.DeepEqual(lateReconcile, olderInspection) {
		t.Fatalf("late older reconciliation = %#v, err = %v", lateReconcile, err)
	}
	lateReplay, err := restarted.CommitRuntimeView(context.Background(), olderRequest)
	if err != nil || !reflect.DeepEqual(lateReplay, *olderInspection.CommitRuntimeView) {
		t.Fatalf("late older exact replay = %#v, err = %v", lateReplay, err)
	}
	after, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-out-of-order-after-late-replay",
	))
	if err != nil || after.CurrentRevisionID != newer.RevisionID ||
		after.CurrentCheckpointID != newer.CheckpointID {
		t.Fatalf("out-of-order replay changed current authority = %#v, err = %v", after, err)
	}
}

func TestLocalFilesystemMaterializeReconciliationBindsExactMaterialization(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	now := taskworkspace.Instant(100)
	armed := false
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID: "expiry-policy-1", MaterializationLifetime: 10, RuntimeViewLifetime: 100,
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if armed && event.OperationID == "materialize-binding-crash" &&
			event.Point == taskworkspace.FaultBeforeResponse {
			armed = false
			return errors.New("simulated materialize acknowledgement loss")
		}
		return nil
	}
	cleanupStarted := false
	filesystemFault := func(event taskworkspace.LocalFilesystemFaultEvent) error {
		if event.OperationID == "expire-reconciled-materialization" &&
			event.Point == taskworkspace.LocalFaultBeforeCleanup {
			cleanupStarted = true
		}
		return nil
	}
	first := newLocalContractLifecycleAtRoot(t, root, config, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = filesystemFault
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, first, "task-1", "confirm-materialize-binding", "materialize-binding-base",
		"open-materialize-binding",
	)
	manifest := declaredStateManifest("content-1")
	if _, err := first.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
		"commit-materialize-binding",
	)); err != nil {
		t.Fatalf("commit binding Checkpoint: %v", err)
	}
	current, err := first.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-materialize-binding-current"),
	)
	if err != nil {
		t.Fatalf("confirm binding current workspace: %v", err)
	}
	request := materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-binding-crash",
	)
	armed = true
	_, err = first.Materialize(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)

	restartedConfig := config
	restartedConfig.FaultHook = nil
	restarted := newLocalContractLifecycleAtRoot(t, root, restartedConfig, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = filesystemFault
	})
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, OperationID: request.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal || reconciled.Materialize == nil {
		t.Fatalf("materialize binding reconciliation = %#v, err = %v", reconciled, err)
	}
	now = 1_000
	if _, err := restarted.ExpireMaterialization(context.Background(), expireMaterializationRequest(
		current, *reconciled.Materialize, "expire-reconciled-materialization",
	)); err != nil {
		t.Fatalf("expire reconciled materialization: %v", err)
	}
	if !cleanupStarted {
		t.Fatal("reconciled MaterializationID was not bound to its exact local generation")
	}
}

func TestLocalFilesystemMaterializationBindingMutationFaultIsRestartSafe(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	now := taskworkspace.Instant(100)
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID: "expiry-policy-1", MaterializationLifetime: 10, RuntimeViewLifetime: 100,
	}
	faulted := false
	first := newLocalContractLifecycleAtRoot(t, root, config, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
			if !faulted && event.OperationID == "materialize-binding-mutation-crash" &&
				event.Point == taskworkspace.LocalFaultAfterCompletionMutation && event.Ordinal == 0 {
				faulted = true
				return errors.New("simulated binding mutation crash")
			}
			return nil
		}
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, first, "task-1", "confirm-binding-mutation", "materialize-binding-mutation-base",
		"open-binding-mutation",
	)
	manifest := declaredStateManifest("content-1")
	if _, err := first.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
		"commit-binding-mutation",
	)); err != nil {
		t.Fatalf("commit binding mutation Checkpoint: %v", err)
	}
	current, err := first.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-binding-mutation-current",
	))
	if err != nil {
		t.Fatalf("confirm binding mutation current workspace: %v", err)
	}
	request := materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-binding-mutation-crash",
	)
	result, err := first.Materialize(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if !faulted || result.MaterializationID != "" {
		t.Fatal("binding mutation fault exposed an unpersisted Materialization")
	}

	cleanupStarted := false
	restarted := newLocalContractLifecycleAtRoot(t, root, config, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = func(event taskworkspace.LocalFilesystemFaultEvent) error {
			if event.OperationID == "expire-binding-mutation-reconciled" &&
				event.Point == taskworkspace.LocalFaultBeforeCleanup {
				cleanupStarted = true
			}
			return nil
		}
	})
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, OperationID: request.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal || reconciled.Materialize == nil {
		t.Fatalf("binding mutation reconciliation = %#v, err = %v", reconciled, err)
	}
	now = 1_000
	if _, err := restarted.ExpireMaterialization(context.Background(), expireMaterializationRequest(
		current, *reconciled.Materialize, "expire-binding-mutation-reconciled",
	)); err != nil {
		t.Fatalf("expire reconciled binding mutation result: %v", err)
	}
	if !cleanupStarted {
		t.Fatal("reconciled binding mutation did not bind the exact local Materialization")
	}
}

func TestLocalFilesystemRestoreReconciliationBindsExactMaterialization(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	now := taskworkspace.Instant(100)
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	armed := false
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID: "expiry-policy-1", MaterializationLifetime: 10, RuntimeViewLifetime: 100,
	}
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id && id != ""
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if armed && event.OperationID == "restore-binding-crash" &&
			event.Point == taskworkspace.FaultBeforeResponse {
			armed = false
			return errors.New("simulated restore acknowledgement loss")
		}
		return nil
	}
	cleanupStarted := false
	filesystemFault := func(event taskworkspace.LocalFilesystemFaultEvent) error {
		if event.OperationID == "expire-reconciled-restore" &&
			event.Point == taskworkspace.LocalFaultBeforeCleanup {
			cleanupStarted = true
		}
		return nil
	}
	first := newLocalContractLifecycleAtRoot(t, root, config, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = filesystemFault
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, first, "task-1", "confirm-restore-binding", "materialize-restore-binding-base",
		"open-restore-binding",
	)
	manifest := declaredStateManifest("content-1")
	if _, err := first.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
		"commit-restore-binding",
	)); err != nil {
		t.Fatalf("commit restore binding Checkpoint: %v", err)
	}
	current, err := first.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-restore-binding-current"),
	)
	if err != nil {
		t.Fatalf("confirm restore binding current workspace: %v", err)
	}
	recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-binding")
	request := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent: recoveryIntent, Operation: taskworkspace.Operation{ID: "restore-binding-crash"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	armed = true
	_, err = first.RestoreTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	inspection, err := first.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		OperationID: request.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationReconciliationRequired ||
		inspection.RestoreTaskWorkspace != nil {
		t.Fatalf("restore adapter-pending inspection = %#v, err = %v", inspection, err)
	}

	restartedConfig := config
	restartedConfig.FaultHook = nil
	restarted := newLocalContractLifecycleAtRoot(t, root, restartedConfig, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = filesystemFault
	})
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		OperationID: request.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
		reconciled.RestoreTaskWorkspace == nil {
		t.Fatalf("restore binding reconciliation = %#v, err = %v", reconciled, err)
	}
	restored := *reconciled.RestoreTaskWorkspace
	now = 1_000
	expire := taskworkspace.ExpireMaterializationRequest{
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		TaskWorkspaceID: restored.TaskWorkspaceID, MaterializationID: restored.MaterializationID,
		RevisionID: restored.RevisionID, CheckpointID: restored.CheckpointID,
		Generation: restored.Generation, Fence: restored.Fence, ExpiryPolicyID: "expiry-policy-1",
		Operation: taskworkspace.Operation{ID: "expire-reconciled-restore"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	if _, err := restarted.ExpireMaterialization(context.Background(), expire); err != nil {
		t.Fatalf("expire reconciled restore: %v", err)
	}
	if !cleanupStarted {
		t.Fatal("reconciled restore MaterializationID was not bound to its exact local generation")
	}
}

func TestLocalFilesystemReconstructionReconciliationBindsExactMaterialization(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	now := taskworkspace.Instant(100)
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	reconstruction := &reconstructionInputDouble{}
	armed := false
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id && id != ""
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if armed && event.OperationID == "reconstruct-binding-crash" &&
			event.Point == taskworkspace.FaultBeforeResponse {
			armed = false
			return errors.New("simulated reconstruction acknowledgement loss")
		}
		return nil
	}
	first := newLocalContractLifecycleAtRoot(t, root, config, nil)
	confirmed, err := first.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-reconstruct-binding"),
	)
	if err != nil {
		t.Fatalf("confirm reconstruction binding workspace: %v", err)
	}
	recoveryIntent = authorizedArtifactReconstructionIntent(confirmed, "recovery-intent-reconstruct-binding")
	request := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent: recoveryIntent, Operation: taskworkspace.Operation{ID: "reconstruct-binding-crash"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	armed = true
	_, err = first.ReconstructTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	inspection, err := first.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		OperationID: request.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationReconciliationRequired ||
		inspection.ReconstructTaskWorkspace != nil {
		t.Fatalf("reconstruction adapter-pending inspection = %#v, err = %v", inspection, err)
	}

	restartedConfig := config
	restartedConfig.FaultHook = nil
	restarted := newLocalContractLifecycleAtRoot(t, root, restartedConfig, nil)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		OperationID: request.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
		reconciled.ReconstructTaskWorkspace == nil || reconstruction.artifactVerifications != 1 {
		t.Fatalf("reconstruction binding reconciliation = %#v, err = %v", reconciled, err)
	}
	repeated, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		OperationID: request.Operation.ID,
	})
	if err != nil || !reflect.DeepEqual(repeated, reconciled) || reconstruction.artifactVerifications != 1 {
		t.Fatalf("repeated reconstruction reconciliation = %#v, err = %v", repeated, err)
	}
	current, err := restarted.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-reconstruct-bound-current"),
	)
	if err != nil {
		t.Fatalf("confirm reconstructed current workspace: %v", err)
	}
	reconstructed := reconciled.ReconstructTaskWorkspace
	materialized := taskworkspace.MaterializeResult{
		MaterializationID: reconstructed.MaterializationID,
		TaskWorkspaceID:   reconstructed.TaskWorkspaceID,
		RevisionID:        reconstructed.CurrentRevisionID,
		CheckpointID:      reconstructed.CurrentCheckpointID,
		Generation:        reconstructed.Generation, Fence: reconstructed.Fence,
	}
	if _, err := restarted.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1",
		"open-reconciled-reconstruction",
	)); err != nil {
		t.Fatalf("open Runtime View from reconciled reconstruction: %v", err)
	}
}

func TestLocalFilesystemExpiryReconciliationCompletesExactCleanup(t *testing.T) {
	root := t.TempDir()
	persistence := taskworkspace.NewInMemoryPersistence()
	now := taskworkspace.Instant(100)
	armed := false
	config := taskworkspaceTestConfig(nil)
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID: "expiry-policy-1", MaterializationLifetime: 10, RuntimeViewLifetime: 100,
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if armed && event.OperationID == "expire-completion-crash" &&
			event.Point == taskworkspace.FaultBeforeResponse {
			armed = false
			return errors.New("simulated expiry acknowledgement loss")
		}
		return nil
	}
	cleanupAttempts := 0
	filesystemFault := func(event taskworkspace.LocalFilesystemFaultEvent) error {
		if event.OperationID == "expire-completion-crash" &&
			event.Point == taskworkspace.LocalFaultBeforeCleanup {
			cleanupAttempts++
		}
		return nil
	}
	first := newLocalContractLifecycleAtRoot(t, root, config, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = filesystemFault
	})
	confirmed, view := openRuntimeViewWithLifecycle(
		t, first, "task-1", "confirm-expiry-completion", "materialize-expiry-completion-base",
		"open-expiry-completion",
	)
	manifest := declaredStateManifest("content-1")
	if _, err := first.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
		"commit-expiry-completion",
	)); err != nil {
		t.Fatalf("commit expiry completion Checkpoint: %v", err)
	}
	current, err := first.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-expiry-completion-current"),
	)
	if err != nil {
		t.Fatalf("confirm expiry completion current workspace: %v", err)
	}
	materialized, err := first.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-expiry-completion-current",
	))
	if err != nil {
		t.Fatalf("materialize expiry completion Checkpoint: %v", err)
	}
	now = 1_000
	request := expireMaterializationRequest(current, materialized, "expire-completion-crash")
	armed = true
	_, err = first.ExpireMaterialization(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	inspection, err := first.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, OperationID: request.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationReconciliationRequired ||
		inspection.ExpireMaterialization != nil || cleanupAttempts != 0 {
		t.Fatalf("expiry adapter-pending inspection = %#v, cleanup attempts = %d, err = %v",
			inspection, cleanupAttempts, err)
	}

	restartedConfig := config
	restartedConfig.FaultHook = nil
	restarted := newLocalContractLifecycleAtRoot(t, root, restartedConfig, func(local *taskworkspace.LocalFilesystemConfig) {
		local.FilesystemFault = filesystemFault
	})
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, OperationID: request.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
		reconciled.ExpireMaterialization == nil || cleanupAttempts != 1 {
		t.Fatalf("expiry cleanup reconciliation = %#v, cleanup attempts = %d, err = %v",
			reconciled, cleanupAttempts, err)
	}
	repeated, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, OperationID: request.Operation.ID,
	})
	if err != nil || !reflect.DeepEqual(repeated, reconciled) || cleanupAttempts != 1 {
		t.Fatalf("repeated expiry reconciliation = %#v, cleanup attempts = %d, err = %v",
			repeated, cleanupAttempts, err)
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
