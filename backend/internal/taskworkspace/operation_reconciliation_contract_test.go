package taskworkspace_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestCommitResponseLossReplaysAcrossModuleRestart(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &happyDurableObject{}
	faultAt := taskworkspace.FaultPoint("")
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.OperationID == "commit-1" && event.Point == faultAt {
			faultAt = ""
			return errors.New("simulated response loss")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	request := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)

	faultAt = taskworkspace.FaultBeforeResponse
	result, err := lifecycle.CommitRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("ambiguous response exposed an unacknowledged terminal result")
	}

	restarted := taskworkspace.NewInMemory(config)
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect committed operation after restart: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal || inspection.CommitRuntimeView == nil {
		t.Fatalf("inspection disposition = %q, want terminal commit", inspection.Disposition)
	}

	replayed, err := restarted.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("replay committed operation after restart: %v", err)
	}
	if !reflect.DeepEqual(replayed, *inspection.CommitRuntimeView) {
		t.Fatal("exact replay did not return the committed operation result")
	}
	if durable.prepared != 1 {
		t.Fatalf("Durable Object prepare count = %d, want one", durable.prepared)
	}
}

func TestVerifiedCommitReconcilesAfterCrashBeforeAuthoritativeTransaction(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &operationTrackingDurableObject{}
	faultAt := taskworkspace.FaultBeforeAuthoritativeTransaction
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.OperationID == "commit-1" && event.Point == faultAt {
			faultAt = ""
			return errors.New("simulated crash before activation")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	request := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)

	result, err := lifecycle.CommitRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("crash before activation returned authoritative identities")
	}

	restarted := taskworkspace.NewInMemory(config)
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect pending commit after restart: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationReconciliationRequired || inspection.CommitRuntimeView != nil {
		t.Fatalf("inspection = %#v, want inaccessible reconciliation-required staging", inspection)
	}
	current, err := restarted.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-current"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	if current.CurrentRevisionID != confirmed.CurrentRevisionID || current.CurrentCheckpointID != "" {
		t.Fatal("crash before activation changed authoritative Task Workspace state")
	}

	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("reconcile commit after restart: %v", err)
	}
	if reconciled.Disposition != taskworkspace.OperationTerminal || reconciled.CommitRuntimeView == nil {
		t.Fatalf("reconciled disposition = %q, want terminal commit", reconciled.Disposition)
	}
	if len(durable.operations) != 2 || durable.operations[0] != request.Operation.ID || durable.operations[1] != request.Operation.ID {
		t.Fatalf("Durable Object operations = %v, want exact OperationID reuse", durable.operations)
	}
}

func TestDurableObjectAcknowledgementLossRemainsReconcilable(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &acknowledgementLossDurableObject{}
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	request := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-ack-loss",
	)

	_, err := lifecycle.CommitRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationReconciliationRequired {
		t.Fatalf("inspect acknowledgement loss = %#v, %v", inspection, err)
	}
	restarted := taskworkspace.NewInMemory(config)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil || reconciled.CommitRuntimeView == nil {
		t.Fatalf("reconcile acknowledgement loss = %#v, %v", reconciled, err)
	}
	if durable.prepared != 2 || durable.operations[0] != request.Operation.ID ||
		durable.operations[1] != request.Operation.ID || durable.checkpointIDs[0] != durable.checkpointIDs[1] {
		t.Fatal("acknowledgement reconciliation did not replay the exact durable operation")
	}
}

func TestDurableObjectVerificationAcknowledgementLossRemainsReconcilable(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &verificationAcknowledgementLossDurableObject{}
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit checkpoint for verification: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-current"),
	)
	if err != nil {
		t.Fatalf("confirm committed workspace: %v", err)
	}
	request := materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-ack-loss",
	)

	_, err = lifecycle.Materialize(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationReconciliationRequired {
		t.Fatalf("inspect verification acknowledgement loss = %#v, %v", inspection, err)
	}
	restarted := taskworkspace.NewInMemory(config)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil || reconciled.Materialize == nil || reconciled.Disposition != taskworkspace.OperationTerminal {
		t.Fatalf("reconcile verification acknowledgement loss = %#v, %v", reconciled, err)
	}
	if durable.verified != 2 || len(durable.verifyOperations) != 2 ||
		durable.verifyOperations[0] != request.Operation.ID ||
		durable.verifyOperations[1] != request.Operation.ID {
		t.Fatal("verification acknowledgement reconciliation did not replay the exact operation")
	}
}

func TestReconcilePersistentAmbiguityReturnsStoredTypedDisposition(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &alwaysAmbiguousDurableObject{}
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	request := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-always-ambiguous",
	)

	_, err := lifecycle.CommitRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	inspection, err := lifecycle.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if inspection.Disposition != taskworkspace.OperationReconciliationRequired ||
		inspection.IntentState != taskworkspace.OperationIntentActing || inspection.Operation != request.Operation {
		t.Fatalf("persistent ambiguity disposition = %#v", inspection)
	}
}

func TestCommitPersistsBoundIntentBeforeDurableContentAction(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &operationTrackingDurableObject{}
	faultAt := taskworkspace.FaultAfterIntentPersistence
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.OperationID == "commit-1" && event.Point == faultAt {
			faultAt = ""
			return errors.New("simulated crash after intent persistence")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	request := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)

	_, err := lifecycle.CommitRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if len(durable.operations) != 0 {
		t.Fatal("Durable Object was called before the lifecycle intent was safely persisted")
	}

	restarted := taskworkspace.NewInMemory(config)
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect persisted intent after restart: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationPending ||
		inspection.IntentState != taskworkspace.OperationIntentPersisted ||
		inspection.Operation != request.Operation ||
		inspection.ExpectedRevisionID != request.ExpectedCurrentRevision ||
		inspection.Generation != request.Generation || inspection.Fence != request.Fence ||
		inspection.AuthorityBindingsDigest == "" {
		t.Fatalf("persisted intent omitted request authority facts: %#v", inspection)
	}
	conflicting := request
	conflicting.DeclaredStateManifest = declaredStateManifest("content-2")
	conflicting.ValidationEvidence = acceptedValidationEvidence(confirmed, view, conflicting.DeclaredStateManifest)
	conflicting.Operation.RequestDigest = conflicting.CanonicalRequestDigest()
	_, err = restarted.CommitRuntimeView(context.Background(), conflicting)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	if len(durable.operations) != 0 {
		t.Fatal("same OperationID with a different request reached Durable Object")
	}

	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal {
		t.Fatalf("reconcile persisted intent = %#v, %v", reconciled, err)
	}
}

func TestConfirmIntentIsDurableBeforeAuthoritativeWorkspaceCreation(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	faultAt := taskworkspace.FaultAfterIntentPersistence
	config := taskworkspaceTestConfig(&operationTrackingDurableObject{})
	config.Persistence = persistence
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.OperationID == "confirm-pending" && event.Point == faultAt {
			faultAt = ""
			return errors.New("simulated crash after confirmation intent persistence")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	request := confirmRequest("policy-domain-1", "task-1", "confirm-pending")

	_, err := lifecycle.ConfirmTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	restarted := taskworkspace.NewInMemory(config)
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect pending confirmation: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationPending || inspection.ExpectedRevisionID == "" ||
		inspection.Generation != 1 || inspection.Fence != 1 || inspection.AuthorityBindingsDigest == "" ||
		inspection.ConfirmTaskWorkspace != nil {
		t.Fatalf("pending confirmation intent = %#v", inspection)
	}

	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
		reconciled.ConfirmTaskWorkspace == nil {
		t.Fatalf("reconcile confirmation = %#v, %v", reconciled, err)
	}
	if reconciled.ConfirmTaskWorkspace.CurrentRevisionID != inspection.ExpectedRevisionID {
		t.Fatal("confirmation reconciliation did not activate its reserved initial Revision")
	}
	replayed, err := restarted.ConfirmTaskWorkspace(context.Background(), request)
	if err != nil || !reflect.DeepEqual(replayed, *reconciled.ConfirmTaskWorkspace) {
		t.Fatal("confirmation exact replay changed the terminal result")
	}
}

func TestConfirmTransactionAndResponseFaultsAreRestartSafe(t *testing.T) {
	for _, test := range []struct {
		point       taskworkspace.FaultPoint
		disposition taskworkspace.OperationDisposition
	}{
		{taskworkspace.FaultBeforeAuthoritativeTransaction, taskworkspace.OperationPending},
		{taskworkspace.FaultAfterAuthoritativeTransaction, taskworkspace.OperationTerminal},
		{taskworkspace.FaultBeforeResponse, taskworkspace.OperationTerminal},
		{taskworkspace.FaultAfterResponse, taskworkspace.OperationTerminal},
	} {
		t.Run(string(test.point), func(t *testing.T) {
			persistence := taskworkspace.NewInMemoryPersistence()
			faultAt := test.point
			config := taskworkspaceTestConfig(&operationTrackingDurableObject{})
			config.Persistence = persistence
			config.FaultHook = func(event taskworkspace.FaultEvent) error {
				if event.OperationID == "confirm-restart" && event.Point == faultAt {
					faultAt = ""
					return errors.New("simulated confirmation boundary crash")
				}
				return nil
			}
			lifecycle := taskworkspace.NewInMemory(config)
			request := confirmRequest("policy-domain-1", "task-1", "confirm-restart")

			_, err := lifecycle.ConfirmTaskWorkspace(context.Background(), request)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
			restarted := taskworkspace.NewInMemory(config)
			inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil || inspection.Disposition != test.disposition {
				t.Fatalf("inspect confirmation = %#v, %v", inspection, err)
			}
			if test.disposition == taskworkspace.OperationPending {
				winner, winnerErr := restarted.ConfirmTaskWorkspace(
					context.Background(),
					confirmRequest("policy-domain-1", "task-1", "confirm-winner"),
				)
				if winnerErr != nil {
					t.Fatalf("confirm workspace after pre-transaction crash: %v", winnerErr)
				}
				if winner.CurrentRevisionID == inspection.ExpectedRevisionID {
					t.Fatal("pre-transaction crash produced the reserved authoritative Revision")
				}
			} else if inspection.ConfirmTaskWorkspace == nil {
				t.Fatal("post-transaction inspection omitted confirmation result")
			}

			reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
				reconciled.ConfirmTaskWorkspace == nil {
				t.Fatalf("reconcile confirmation = %#v, %v", reconciled, err)
			}
			replayed, err := restarted.ConfirmTaskWorkspace(context.Background(), request)
			if err != nil || !reflect.DeepEqual(replayed, *reconciled.ConfirmTaskWorkspace) {
				t.Fatal("confirmation replay created a second Workspace or Revision")
			}
		})
	}
}

func TestLifecycleResponseFaultsBracketObservableDelivery(t *testing.T) {
	for _, operationKind := range []string{"confirm", "materialize", "open", "commit", "discard", "fence"} {
		for _, point := range []taskworkspace.FaultPoint{
			taskworkspace.FaultBeforeResponse,
			taskworkspace.FaultAfterResponse,
		} {
			t.Run(operationKind+"-"+string(point), func(t *testing.T) {
				persistence := taskworkspace.NewInMemoryPersistence()
				targetOperationID := taskworkspace.OperationID(operationKind + "-response")
				armed := false
				deliveries := 0
				config := taskworkspaceTestConfig(&operationTrackingDurableObject{})
				config.Persistence = persistence
				config.ResponseDelivery = func(event taskworkspace.ResponseDeliveryEvent) {
					if armed && event.OperationID == targetOperationID {
						deliveries++
					}
				}
				config.FaultHook = func(event taskworkspace.FaultEvent) error {
					if armed && event.OperationID == targetOperationID && event.Point == point {
						armed = false
						return errors.New("simulated response boundary crash")
					}
					return nil
				}
				lifecycle := taskworkspace.NewInMemory(config)
				invoke := responseFaultInvocation(t, lifecycle, operationKind, string(targetOperationID))
				armed = true

				err := invoke()
				assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
				wantDeliveries := 0
				if point == taskworkspace.FaultAfterResponse {
					wantDeliveries = 1
				}
				if deliveries != wantDeliveries {
					t.Fatalf("response deliveries = %d, want %d", deliveries, wantDeliveries)
				}
				inspection, inspectErr := lifecycle.InspectOperation(
					context.Background(),
					taskworkspace.InspectOperationRequest{
						PolicyDomainID: "policy-domain-1",
						TaskID:         "task-1",
						OperationID:    targetOperationID,
					},
				)
				if inspectErr != nil || inspection.Disposition != taskworkspace.OperationTerminal {
					t.Fatalf("inspect response-loss operation = %#v, %v", inspection, inspectErr)
				}
			})
		}
	}
}

func TestCrashBeforeIntentPersistenceLeavesNoOperationOrMutation(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &operationTrackingDurableObject{}
	armed := false
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if armed && event.OperationID == "commit-before-intent" && event.Point == taskworkspace.FaultBeforeIntentPersistence {
			armed = false
			return errors.New("simulated crash before intent persistence")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	request := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-before-intent",
	)
	armed = true

	_, err := lifecycle.CommitRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if durable.prepared != 0 {
		t.Fatal("physical content action happened before intent persistence")
	}
	restarted := taskworkspace.NewInMemory(config)
	_, err = restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorInvalidIntent)
	current, err := restarted.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-current"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	if current.CurrentRevisionID != confirmed.CurrentRevisionID || current.CurrentCheckpointID != "" {
		t.Fatal("crash before intent persistence produced an authoritative mutation")
	}
	committed, err := restarted.CommitRuntimeView(context.Background(), request)
	if err != nil || committed.CheckpointID == "" {
		t.Fatalf("retry original operation after pre-intent crash: %#v, %v", committed, err)
	}
}

func TestMaterializationFaultBoundariesResumeTheSameOperation(t *testing.T) {
	for _, point := range []taskworkspace.FaultPoint{
		taskworkspace.FaultBeforeBaseMaterialization,
		taskworkspace.FaultAfterBaseMaterialization,
	} {
		t.Run(string(point), func(t *testing.T) {
			persistence := taskworkspace.NewInMemoryPersistence()
			faultAt := taskworkspace.FaultPoint("")
			var createdSubject string
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Persistence = persistence
			config.FaultHook = func(event taskworkspace.FaultEvent) error {
				if event.OperationID == "materialize-restart" && event.Point == faultAt {
					createdSubject = event.SubjectID
					faultAt = ""
					return errors.New("simulated materialization crash")
				}
				return nil
			}
			lifecycle := taskworkspace.NewInMemory(config)
			confirmed, err := lifecycle.ConfirmTaskWorkspace(
				context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-1"),
			)
			if err != nil {
				t.Fatalf("confirm Task Workspace: %v", err)
			}
			request := materializeRequest(
				"policy-domain-1", "task-1", confirmed, "materialize-restart",
			)
			faultAt = point

			_, err = lifecycle.Materialize(context.Background(), request)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
			restarted := taskworkspace.NewInMemory(config)
			inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil {
				t.Fatalf("inspect materialization operation: %v", err)
			}
			wantDisposition := taskworkspace.OperationPending
			if point == taskworkspace.FaultAfterBaseMaterialization {
				wantDisposition = taskworkspace.OperationReconciliationRequired
			}
			if inspection.Disposition != wantDisposition || inspection.Materialize != nil {
				t.Fatalf("inspection = %#v, want %q without result", inspection, wantDisposition)
			}

			reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil || reconciled.Materialize == nil {
				t.Fatalf("reconcile materialization = %#v, %v", reconciled, err)
			}
			if createdSubject != "" && string(reconciled.Materialize.MaterializationID) != createdSubject {
				t.Fatal("reconciliation created a second materialization identity")
			}
			replayed, err := restarted.Materialize(context.Background(), request)
			if err != nil || !reflect.DeepEqual(replayed, *reconciled.Materialize) {
				t.Fatal("materialization exact replay changed the terminal result")
			}
		})
	}
}

func TestRuntimeViewCreationFaultBoundariesResumeTheSameOperation(t *testing.T) {
	for _, point := range []taskworkspace.FaultPoint{
		taskworkspace.FaultBeforeRuntimeViewCreation,
		taskworkspace.FaultAfterRuntimeViewCreation,
	} {
		t.Run(string(point), func(t *testing.T) {
			persistence := taskworkspace.NewInMemoryPersistence()
			faultAt := taskworkspace.FaultPoint("")
			var createdSubject string
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Persistence = persistence
			config.FaultHook = func(event taskworkspace.FaultEvent) error {
				if event.OperationID == "open-view-restart" && event.Point == faultAt {
					createdSubject = event.SubjectID
					faultAt = ""
					return errors.New("simulated Runtime View crash")
				}
				return nil
			}
			lifecycle := taskworkspace.NewInMemory(config)
			confirmed, materialized := materializedTaskUsing(t, lifecycle)
			request := openRuntimeViewRequest(
				"policy-domain-1", "task-1", confirmed, materialized,
				"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-restart",
			)
			faultAt = point

			_, err := lifecycle.OpenRuntimeView(context.Background(), request)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
			restarted := taskworkspace.NewInMemory(config)
			inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil {
				t.Fatalf("inspect Runtime View operation: %v", err)
			}
			wantDisposition := taskworkspace.OperationPending
			if point == taskworkspace.FaultAfterRuntimeViewCreation {
				wantDisposition = taskworkspace.OperationReconciliationRequired
			}
			if inspection.Disposition != wantDisposition || inspection.OpenRuntimeView != nil {
				t.Fatalf("inspection = %#v, want %q without result", inspection, wantDisposition)
			}

			reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil || reconciled.OpenRuntimeView == nil {
				t.Fatalf("reconcile Runtime View = %#v, %v", reconciled, err)
			}
			if createdSubject != "" && string(reconciled.OpenRuntimeView.RuntimeViewID) != createdSubject {
				t.Fatal("reconciliation created a second Runtime View")
			}
			replayed, err := restarted.OpenRuntimeView(context.Background(), request)
			if err != nil || !reflect.DeepEqual(replayed, *reconciled.OpenRuntimeView) {
				t.Fatal("Runtime View exact replay changed the terminal result")
			}
		})
	}
}

func TestCommitFaultBoundariesAreRestartSafeAndExactlyReconciled(t *testing.T) {
	tests := []struct {
		point           taskworkspace.FaultPoint
		ordinal         int
		disposition     taskworkspace.OperationDisposition
		contentPrepared int
	}{
		{taskworkspace.FaultBeforeDeclaredManifestVerification, -1, taskworkspace.OperationPending, 0},
		{taskworkspace.FaultAfterDeclaredManifestVerification, -1, taskworkspace.OperationPending, 0},
		{taskworkspace.FaultBeforeContentPrepare, 0, taskworkspace.OperationPending, 0},
		{taskworkspace.FaultBeforeContentPrepare, 1, taskworkspace.OperationReconciliationRequired, 1},
		{taskworkspace.FaultAfterContentPrepare, 0, taskworkspace.OperationReconciliationRequired, 1},
		{taskworkspace.FaultAfterContentPrepare, 1, taskworkspace.OperationReconciliationRequired, 2},
		{taskworkspace.FaultBeforeDurabilityReceiptVerification, 0, taskworkspace.OperationReconciliationRequired, 2},
		{taskworkspace.FaultBeforeDurabilityReceiptVerification, 1, taskworkspace.OperationReconciliationRequired, 2},
		{taskworkspace.FaultAfterDurabilityReceiptVerification, 0, taskworkspace.OperationReconciliationRequired, 2},
		{taskworkspace.FaultAfterDurabilityReceiptVerification, 1, taskworkspace.OperationReconciliationRequired, 2},
		{taskworkspace.FaultBeforeAuthoritativeTransaction, -1, taskworkspace.OperationReconciliationRequired, 2},
		{taskworkspace.FaultAfterAuthoritativeTransaction, -1, taskworkspace.OperationTerminal, 2},
		{taskworkspace.FaultBeforeResponse, -1, taskworkspace.OperationTerminal, 2},
		{taskworkspace.FaultAfterResponse, -1, taskworkspace.OperationTerminal, 2},
	}
	for _, test := range tests {
		name := string(test.point)
		if test.ordinal >= 0 {
			name += "-" + string(rune('0'+test.ordinal))
		}
		t.Run(name, func(t *testing.T) {
			persistence := taskworkspace.NewInMemoryPersistence()
			durable := &operationTrackingDurableObject{}
			armed := false
			config := taskworkspaceTestConfig(durable)
			config.Persistence = persistence
			config.FaultHook = func(event taskworkspace.FaultEvent) error {
				if armed && event.OperationID == "commit-restart" && event.Point == test.point &&
					(test.ordinal < 0 || event.Ordinal == test.ordinal) {
					armed = false
					return errors.New("simulated commit protocol crash")
				}
				return nil
			}
			lifecycle := taskworkspace.NewInMemory(config)
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
			)
			manifest := declaredStateManifest("content-1")
			request := commitRequest(
				confirmed,
				view,
				manifest,
				acceptedValidationEvidence(confirmed, view, manifest),
				"commit-restart",
			)
			armed = true

			_, err := lifecycle.CommitRuntimeView(context.Background(), request)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
			if durable.contentPrepared != test.contentPrepared {
				t.Fatalf(
					"prepared content count at fault = %d, want %d",
					durable.contentPrepared,
					test.contentPrepared,
				)
			}
			restarted := taskworkspace.NewInMemory(config)
			inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil {
				t.Fatalf("inspect commit operation: %v", err)
			}
			if inspection.Disposition != test.disposition {
				t.Fatalf("disposition = %q, want %q", inspection.Disposition, test.disposition)
			}
			if test.disposition == taskworkspace.OperationTerminal && inspection.CommitRuntimeView == nil {
				t.Fatal("terminal inspection omitted committed result")
			}
			if test.disposition != taskworkspace.OperationTerminal {
				current, confirmErr := restarted.ConfirmTaskWorkspace(
					context.Background(),
					confirmRequest("policy-domain-1", "task-1", "confirm-current"),
				)
				if confirmErr != nil {
					t.Fatalf("confirm current Task Workspace: %v", confirmErr)
				}
				if current.CurrentRevisionID != confirmed.CurrentRevisionID || current.CurrentCheckpointID != "" {
					t.Fatal("pre-transaction crash produced an authoritative result")
				}
			}

			reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil || reconciled.CommitRuntimeView == nil ||
				reconciled.Disposition != taskworkspace.OperationTerminal {
				t.Fatalf("reconcile commit = %#v, %v", reconciled, err)
			}
			for _, operationID := range durable.operations {
				if operationID != request.Operation.ID {
					t.Fatal("Durable Object retry used a different OperationID")
				}
			}
			for _, checkpointID := range durable.checkpointIDs {
				if checkpointID != reconciled.CommitRuntimeView.CheckpointID {
					t.Fatal("Durable Object retry used a different CheckpointID")
				}
			}
			for _, revisionID := range durable.revisionIDs {
				if revisionID != reconciled.CommitRuntimeView.RevisionID {
					t.Fatal("Durable Object retry used a different RevisionID")
				}
			}
			replayed, err := restarted.CommitRuntimeView(context.Background(), request)
			if err != nil || !reflect.DeepEqual(replayed, *reconciled.CommitRuntimeView) {
				t.Fatal("commit exact replay changed the terminal result")
			}
		})
	}
}

func TestDiscardEvidenceFaultBoundariesAreRestartSafe(t *testing.T) {
	for _, test := range []struct {
		point       taskworkspace.FaultPoint
		disposition taskworkspace.OperationDisposition
	}{
		{taskworkspace.FaultBeforeDiscardEvidencePersistence, taskworkspace.OperationPending},
		{taskworkspace.FaultAfterDiscardEvidencePersistence, taskworkspace.OperationTerminal},
	} {
		t.Run(string(test.point), func(t *testing.T) {
			persistence := taskworkspace.NewInMemoryPersistence()
			armed := false
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Persistence = persistence
			config.FaultHook = func(event taskworkspace.FaultEvent) error {
				if armed && event.OperationID == "discard-restart" && event.Point == test.point {
					armed = false
					return errors.New("simulated discard evidence crash")
				}
				return nil
			}
			lifecycle := taskworkspace.NewInMemory(config)
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
			)
			request := discardRequest(
				confirmed, view, taskworkspace.RuntimeViewValidationRejected, "discard-restart",
			)
			armed = true

			_, err := lifecycle.DiscardRuntimeView(context.Background(), request)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
			restarted := taskworkspace.NewInMemory(config)
			inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil || inspection.Disposition != test.disposition {
				t.Fatalf("inspect discard = %#v, %v; want %q", inspection, err, test.disposition)
			}
			if test.disposition == taskworkspace.OperationTerminal && inspection.DiscardRuntimeView == nil {
				t.Fatal("terminal discard inspection omitted its result")
			}

			reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
				PolicyDomainID: request.PolicyDomainID,
				TaskID:         request.TaskID,
				OperationID:    request.Operation.ID,
			})
			if err != nil || reconciled.DiscardRuntimeView == nil ||
				reconciled.Disposition != taskworkspace.OperationTerminal {
				t.Fatalf("reconcile discard = %#v, %v", reconciled, err)
			}
			replayed, err := restarted.DiscardRuntimeView(context.Background(), request)
			if err != nil || !reflect.DeepEqual(replayed, *reconciled.DiscardRuntimeView) {
				t.Fatal("discard exact replay changed the terminal result")
			}
			current, err := restarted.ConfirmTaskWorkspace(
				context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-current"),
			)
			if err != nil || current.CurrentRevisionID != confirmed.CurrentRevisionID || current.CurrentCheckpointID != "" {
				t.Fatal("discard reconciliation changed authoritative Task Workspace state")
			}
		})
	}
}

func TestFenceResponseLossDoesNotAdvanceFenceTwiceAfterRestart(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	armed := false
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if armed && event.OperationID == "fence-restart" && event.Point == taskworkspace.FaultAfterAuthoritativeTransaction {
			armed = false
			return errors.New("simulated fence acknowledgement loss")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	request := fenceRequest(confirmed, view, taskworkspace.RuntimeViewCancelled, "fence-restart")
	armed = true

	_, err := lifecycle.FenceRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	restarted := taskworkspace.NewInMemory(config)
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.PolicyDomainID,
		TaskID:         request.TaskID,
		OperationID:    request.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationTerminal || inspection.FenceRuntimeView == nil {
		t.Fatalf("inspect fenced operation = %#v, %v", inspection, err)
	}
	replayed, err := restarted.FenceRuntimeView(context.Background(), request)
	if err != nil || !reflect.DeepEqual(replayed, *inspection.FenceRuntimeView) {
		t.Fatal("fence replay did not return the original terminal result")
	}
	current, err := restarted.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-current"),
	)
	if err != nil || current.Fence != replayed.Fence || current.Fence != confirmed.Fence+1 {
		t.Fatalf("current fence = %d, replayed = %d, initial = %d", current.Fence, replayed.Fence, confirmed.Fence)
	}
}

func TestDeliveryAndCallbackReplayReuseTheOriginalOperation(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &operationTrackingDurableObject{}
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, firstView := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	firstManifest := declaredStateManifest("content-1")
	firstRequest := commitRequest(
		confirmed,
		firstView,
		firstManifest,
		acceptedValidationEvidence(confirmed, firstView, firstManifest),
		"commit-delivery",
	)
	first, err := lifecycle.CommitRuntimeView(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("commit first operation: %v", err)
	}

	current, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-current"),
	)
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	materialized, err := lifecycle.Materialize(
		context.Background(), materializeRequest("policy-domain-1", "task-1", current, "materialize-current"),
	)
	if err != nil {
		t.Fatalf("materialize current Task Workspace: %v", err)
	}
	secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, materialized,
		"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}
	secondManifest := declaredStateManifest("content-2")
	secondValidation := acceptedValidationEvidence(current, secondView, secondManifest)
	secondValidation.ID = "validation-evidence-2"
	secondValidation.Digest = secondValidation.CanonicalDigest()
	if _, err := lifecycle.CommitRuntimeView(
		context.Background(),
		commitRequest(current, secondView, secondManifest, secondValidation, "commit-newer"),
	); err != nil {
		t.Fatalf("commit newer operation: %v", err)
	}
	preparedBeforeReplay := durable.prepared

	for _, delivery := range []string{
		"duplicate delivery",
		"worker claim loss",
		"callback duplicate",
		"callback out of order",
		"acknowledgement loss",
	} {
		t.Run(delivery, func(t *testing.T) {
			replayed, err := lifecycle.CommitRuntimeView(context.Background(), firstRequest)
			if err != nil || !reflect.DeepEqual(replayed, first) || replayed.Operation.ID != firstRequest.Operation.ID {
				t.Fatalf("delivery replay = %#v, %v", replayed, err)
			}
		})
	}
	if durable.prepared != preparedBeforeReplay {
		t.Fatal("delivery or callback replay repeated durable content preparation")
	}
}

func TestReconcileReportsTypedTerminalConflictAfterAuthorityMoves(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	armed := false
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if armed && event.OperationID == "commit-pending" && event.Point == taskworkspace.FaultAfterIntentPersistence {
			armed = false
			return errors.New("simulated claim loss")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, view := openRuntimeViewWithLifecycle(
		t, lifecycle, "task-1", "confirm-1", "materialize-1", "open-view-1",
	)
	manifest := declaredStateManifest("content-1")
	commit := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-pending",
	)
	armed = true
	_, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if _, err := lifecycle.FenceRuntimeView(
		context.Background(),
		fenceRequest(confirmed, view, taskworkspace.RuntimeViewCancelled, "fence-winner"),
	); err != nil {
		t.Fatalf("fence Runtime View before reconciliation: %v", err)
	}

	reconciled, err := lifecycle.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: commit.PolicyDomainID,
		TaskID:         commit.TaskID,
		OperationID:    commit.Operation.ID,
	})
	if err != nil {
		t.Fatalf("reconcile terminal conflict: %v", err)
	}
	if reconciled.Disposition != taskworkspace.OperationTerminal || reconciled.Error == nil ||
		(reconciled.Error.Code != taskworkspace.ErrorViewTerminalConflict &&
			reconciled.Error.Code != taskworkspace.ErrorStaleAuthority) {
		t.Fatalf("reconciled conflict = %#v, want typed terminal disposition", reconciled)
	}
}

func responseFaultInvocation(
	t *testing.T,
	lifecycle taskworkspace.Lifecycle,
	operationKind string,
	operationID string,
) func() error {
	t.Helper()
	switch operationKind {
	case "confirm":
		request := confirmRequest("policy-domain-1", "task-1", operationID)
		return func() error {
			_, err := lifecycle.ConfirmTaskWorkspace(context.Background(), request)
			return err
		}
	case "materialize":
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-setup"),
		)
		if err != nil {
			t.Fatalf("confirm response-fault setup: %v", err)
		}
		request := materializeRequest("policy-domain-1", "task-1", confirmed, operationID)
		return func() error {
			_, err := lifecycle.Materialize(context.Background(), request)
			return err
		}
	case "open":
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-setup"),
		)
		if err != nil {
			t.Fatalf("confirm response-fault setup: %v", err)
		}
		materialized, err := lifecycle.Materialize(
			context.Background(), materializeRequest("policy-domain-1", "task-1", confirmed, "materialize-setup"),
		)
		if err != nil {
			t.Fatalf("materialize response-fault setup: %v", err)
		}
		request := openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-task-1", operationID,
		)
		return func() error {
			_, err := lifecycle.OpenRuntimeView(context.Background(), request)
			return err
		}
	case "commit", "discard", "fence":
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-setup", "materialize-setup", "open-setup",
		)
		switch operationKind {
		case "commit":
			manifest := declaredStateManifest("content-1")
			request := commitRequest(
				confirmed,
				view,
				manifest,
				acceptedValidationEvidence(confirmed, view, manifest),
				operationID,
			)
			return func() error {
				_, err := lifecycle.CommitRuntimeView(context.Background(), request)
				return err
			}
		case "discard":
			request := discardRequest(
				confirmed, view, taskworkspace.RuntimeViewRuntimeFailed, operationID,
			)
			return func() error {
				_, err := lifecycle.DiscardRuntimeView(context.Background(), request)
				return err
			}
		default:
			request := fenceRequest(
				confirmed, view, taskworkspace.RuntimeViewCancelled, operationID,
			)
			return func() error {
				_, err := lifecycle.FenceRuntimeView(context.Background(), request)
				return err
			}
		}
	default:
		t.Fatalf("unknown lifecycle operation kind %q", operationKind)
		return nil
	}
}

type operationTrackingDurableObject struct {
	happyDurableObject
	operations    []taskworkspace.OperationID
	checkpointIDs []taskworkspace.CheckpointID
	revisionIDs   []taskworkspace.RevisionID
}

type acknowledgementLossDurableObject struct {
	operationTrackingDurableObject
	lost bool
}

type verificationAcknowledgementLossDurableObject struct {
	operationTrackingDurableObject
	lost             bool
	verifyOperations []taskworkspace.OperationID
}

func (d *verificationAcknowledgementLossDurableObject) VerifyCheckpoint(
	ctx context.Context,
	request taskworkspace.VerifyCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	d.verifyOperations = append(d.verifyOperations, request.Operation.ID)
	content, err := d.happyDurableObject.VerifyCheckpoint(ctx, request)
	if err == nil && !d.lost {
		d.lost = true
		return content, taskworkspace.ErrDurableObjectResultAmbiguous
	}
	return content, err
}

type alwaysAmbiguousDurableObject struct {
	operationTrackingDurableObject
}

func (d *alwaysAmbiguousDurableObject) PrepareCheckpoint(
	ctx context.Context,
	request taskworkspace.PrepareCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	content, err := d.operationTrackingDurableObject.PrepareCheckpoint(ctx, request)
	if err != nil {
		return content, err
	}
	return content, taskworkspace.ErrDurableObjectResultAmbiguous
}

func (d *acknowledgementLossDurableObject) PrepareCheckpoint(
	ctx context.Context,
	request taskworkspace.PrepareCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	content, err := d.operationTrackingDurableObject.PrepareCheckpoint(ctx, request)
	if err == nil && !d.lost {
		d.lost = true
		return content, taskworkspace.ErrDurableObjectResultAmbiguous
	}
	return content, err
}

func (d *operationTrackingDurableObject) PrepareCheckpoint(
	ctx context.Context,
	request taskworkspace.PrepareCheckpointContentRequest,
) (taskworkspace.VerifiedCheckpointContent, error) {
	d.operations = append(d.operations, request.Operation.ID)
	d.checkpointIDs = append(d.checkpointIDs, request.CheckpointID)
	d.revisionIDs = append(d.revisionIDs, request.RevisionID)
	return d.happyDurableObject.PrepareCheckpoint(ctx, request)
}
