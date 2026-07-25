package taskworkspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestOwnedTransportAuthenticatesAndBindsCanonicalEnvelope(t *testing.T) {
	now := taskworkspace.Instant(100)
	authority := taskworkspace.OwnedTransportMachineAuthority{
		ID:         "task-orchestration-machine-1",
		Generation: 7,
		ExpiresAt:  1_000,
	}
	key := []byte("owned-transport-test-key")
	harness := taskworkspace.NewOwnedTransportHarness(taskworkspace.OwnedTransportHarnessConfig{
		Lifecycle:         taskworkspace.NewInMemory(taskworkspaceTestConfig(&happyDurableObject{})),
		MachineAuthority:  authority,
		AuthenticationKey: key,
		Authorize: func(scope taskworkspace.OwnedTransportAuthorityScope) bool {
			return scope.PolicyDomainID == "policy-domain-1" && scope.TaskID == "task-1"
		},
		Now:             func() taskworkspace.Instant { return now },
		RequestLifetime: 50,
	})

	result, err := harness.Lifecycle().ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-owned-1"),
	)
	if err != nil || result.TaskWorkspaceID == "" {
		t.Fatalf("confirm through owned transport = %#v, err = %v", result, err)
	}

	requests := harness.Requests()
	if len(requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(requests))
	}
	envelope := requests[0].Envelope
	if envelope.ScopeKind != taskworkspace.OwnedTransportTaskScope ||
		envelope.MachineAuthorityID != authority.ID ||
		envelope.MachineAuthorityGeneration != authority.Generation ||
		envelope.PolicyDomainID != "policy-domain-1" || envelope.TaskID != "task-1" ||
		envelope.OperationID != "confirm-owned-1" ||
		envelope.CanonicalRequestDigest != confirmRequest("policy-domain-1", "task-1", "confirm-owned-1").Operation.RequestDigest ||
		envelope.Deadline != 150 || envelope.Generation != 0 || envelope.Fence != 0 {
		t.Fatalf("owned transport envelope did not preserve exact authority binding: %#v", envelope)
	}
}

func TestOwnedTransportUsesAnExplicitModuleScopeForModuleWideRebuild(t *testing.T) {
	projection := &projectionCaptureDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = projection
	harness := taskworkspace.NewOwnedTransportHarness(taskworkspace.OwnedTransportHarnessConfig{
		Lifecycle: taskworkspace.NewInMemory(config),
		MachineAuthority: taskworkspace.OwnedTransportMachineAuthority{
			ID: "projection-machine-1", Generation: 3, ExpiresAt: 1_000,
		},
		AuthenticationKey: []byte("projection-transport-key"),
		Authorize: func(scope taskworkspace.OwnedTransportAuthorityScope) bool {
			return scope.ScopeKind == taskworkspace.OwnedTransportModuleScope
		},
		Now: func() taskworkspace.Instant { return 100 }, RequestLifetime: 50,
	})

	result, err := harness.Lifecycle().RebuildProjections(
		context.Background(), taskworkspace.ProjectionRebuildRequest{SchemaRevision: taskworkspace.ProjectionSchemaV1},
	)
	if err != nil || !result.Projected.Known {
		t.Fatalf("module-scoped projection rebuild = %#v, err = %v", result, err)
	}
	requests := harness.Requests()
	if len(requests) != 1 || requests[0].Envelope.ScopeKind != taskworkspace.OwnedTransportModuleScope ||
		requests[0].Envelope.PolicyDomainID != "" || requests[0].Envelope.TaskID != "" {
		t.Fatalf("module-wide rebuild used fabricated Task scope: %#v", requests)
	}
}

func TestOwnedTransportResponseLossRequiresOperationReconciliation(t *testing.T) {
	harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
	lifecycle := harness.Lifecycle()
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-owned-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View through owned transport: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	commit := commitRequest(
		confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-owned-1",
	)

	harness.FailNext(taskworkspace.OwnedTransportResponseLoss)
	_, err = lifecycle.CommitRuntimeView(context.Background(), commit)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)

	inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    commit.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationTerminal ||
		inspection.CommitRuntimeView == nil {
		t.Fatalf("inspect response-lost commit = %#v, err = %v", inspection, err)
	}
	reconciled, err := lifecycle.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    commit.Operation.ID,
	})
	if err != nil || !reflect.DeepEqual(reconciled, inspection) {
		t.Fatalf("reconcile response-lost commit = %#v, err = %v", reconciled, err)
	}

	replayed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	if err != nil || !reflect.DeepEqual(replayed, *inspection.CommitRuntimeView) {
		t.Fatalf("exact replay after response loss = %#v, err = %v", replayed, err)
	}
}

func TestOwnedTransportRedeliveryModesReuseTheExactOperation(t *testing.T) {
	for _, failure := range []taskworkspace.OwnedTransportFailure{
		taskworkspace.OwnedTransportDuplicateDelivery,
		taskworkspace.OwnedTransportOutOfOrderDelivery,
		taskworkspace.OwnedTransportQueueClaimLoss,
		taskworkspace.OwnedTransportCallbackReplay,
	} {
		t.Run(string(failure), func(t *testing.T) {
			harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
			lifecycle := harness.Lifecycle()
			confirmed, materialized := materializedTaskUsing(t, lifecycle)
			request := openRuntimeViewRequest(
				"policy-domain-1", "task-1", confirmed, materialized,
				"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-redelivered-1",
			)
			deliveryOffset := len(harness.Deliveries())
			callbackOffset := len(harness.Callbacks())
			acceptedCallbackOffset := len(harness.AcceptedCallbacks())
			harness.FailNext(failure)
			first, err := lifecycle.OpenRuntimeView(context.Background(), request)
			if err != nil {
				t.Fatalf("redelivered Runtime View open: %v", err)
			}
			deliveries := harness.Deliveries()[deliveryOffset:]
			callbacks := harness.Callbacks()[callbackOffset:]
			acceptedCallbacks := harness.AcceptedCallbacks()[acceptedCallbackOffset:]
			switch failure {
			case taskworkspace.OwnedTransportDuplicateDelivery:
				assertOwnedTransportDeliveryKinds(t, deliveries,
					taskworkspace.OwnedTransportDeliveryOriginal,
					taskworkspace.OwnedTransportDeliveryDuplicate,
				)
			case taskworkspace.OwnedTransportOutOfOrderDelivery:
				assertOwnedTransportDeliveryKinds(t, deliveries,
					taskworkspace.OwnedTransportDeliveryRedelivery,
					taskworkspace.OwnedTransportDeliveryOriginal,
				)
			case taskworkspace.OwnedTransportQueueClaimLoss:
				assertOwnedTransportDeliveryKinds(t, deliveries,
					taskworkspace.OwnedTransportDeliveryOriginal,
					taskworkspace.OwnedTransportDeliveryRedelivery,
				)
				if deliveries[0].ClaimGeneration != 1 || deliveries[1].ClaimGeneration != 2 {
					t.Fatalf("claim-loss delivery generations = %#v", deliveries)
				}
			case taskworkspace.OwnedTransportCallbackReplay:
				assertOwnedTransportDeliveryKinds(t, deliveries, taskworkspace.OwnedTransportDeliveryOriginal)
				if len(callbacks) != 2 || len(acceptedCallbacks) != 1 {
					t.Fatalf("callback replay deliveries=%d accepted=%d, want 2/1", len(callbacks), len(acceptedCallbacks))
				}
			}
			for _, delivery := range deliveries {
				if delivery.OperationID != request.Operation.ID {
					t.Fatalf("redelivery changed OperationID: %#v", deliveries)
				}
			}
			replayed, err := lifecycle.OpenRuntimeView(context.Background(), request)
			if err != nil || !reflect.DeepEqual(replayed, first) {
				t.Fatalf("redelivered operation replay = %#v, err = %v", replayed, err)
			}

			inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
				PolicyDomainID: "policy-domain-1", TaskID: "task-1", OperationID: request.Operation.ID,
			})
			if err != nil || inspection.OpenRuntimeView == nil ||
				inspection.OpenRuntimeView.RuntimeViewID != first.RuntimeViewID {
				t.Fatalf("redelivered operation inspection = %#v, err = %v", inspection, err)
			}
		})
	}
}

func TestOwnedTransportRedeliveryCannotDuplicateRevisionCheckpointOrCleanupDebt(t *testing.T) {
	t.Run("Revision and Checkpoint", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		lifecycle := harness.Lifecycle()
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-duplicate-commit-1",
		))
		if err != nil {
			t.Fatalf("open Runtime View: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		commit := commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-duplicate-1",
		)
		harness.FailNext(taskworkspace.OwnedTransportDuplicateDelivery)
		first, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil {
			t.Fatalf("duplicate-delivered commit: %v", err)
		}
		replayed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil || !reflect.DeepEqual(replayed, first) {
			t.Fatalf("commit replay after duplicate delivery = %#v, err = %v", replayed, err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-after-duplicate-commit-1"),
		)
		if err != nil || current.CurrentRevisionID != first.RevisionID ||
			current.CurrentCheckpointID != first.CheckpointID {
			t.Fatalf("state after duplicate commit = %#v, err = %v", current, err)
		}
	})

	t.Run("Cleanup Debt", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		lifecycle := harness.Lifecycle()
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-duplicate-debt-1"),
		)
		if err != nil {
			t.Fatalf("confirm Task Workspace: %v", err)
		}
		create := createCleanupObligationRequest(confirmed, "create-duplicate-debt-1")
		harness.FailNext(taskworkspace.OwnedTransportQueueClaimLoss)
		first, err := lifecycle.CreateCleanupObligation(context.Background(), create)
		if err != nil {
			t.Fatalf("claim-loss Cleanup Debt create: %v", err)
		}
		replayed, err := lifecycle.CreateCleanupObligation(context.Background(), create)
		if err != nil || replayed.DebtID != first.DebtID {
			t.Fatalf("Cleanup Debt replay after claim loss = %#v, err = %v", replayed, err)
		}
		inspected, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
			PolicyDomainID: "policy-domain-1", TaskID: "task-1", DebtID: first.DebtID,
		})
		if err != nil || inspected.DebtID != first.DebtID || inspected.RetryGeneration != first.RetryGeneration {
			t.Fatalf("Cleanup Debt after claim loss = %#v, err = %v", inspected, err)
		}
	})
}

func TestOwnedTransportReconnectKeepsOriginalDeadlineGenerationAndFence(t *testing.T) {
	now := taskworkspace.Instant(100)
	harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return now })
	lifecycle := harness.Lifecycle()
	confirmed, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-reconnect-1"),
	)
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	request := materializeRequest("policy-domain-1", "task-1", confirmed, "materialize-reconnect-1")
	harness.SetConnected(false)
	_, err = lifecycle.Materialize(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorRetryableUnavailable)

	weakened := request
	weakened.Generation++
	weakened.Fence++
	weakened.Operation.RequestDigest = weakened.CanonicalRequestDigest()
	_, err = lifecycle.Materialize(context.Background(), weakened)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)

	harness.SetConnected(true)
	now = 149
	if _, err := lifecycle.Materialize(context.Background(), request); err != nil {
		t.Fatalf("retry before original deadline: %v", err)
	}

	lateRequest := materializeRequest("policy-domain-1", "task-1", confirmed, "materialize-late-1")
	harness.SetConnected(false)
	now = 150
	_, err = lifecycle.Materialize(context.Background(), lateRequest)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorRetryableUnavailable)
	harness.SetConnected(true)
	now = 201
	_, err = lifecycle.Materialize(context.Background(), lateRequest)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
}

func TestOwnedTransportAdapterRestartKeepsPersistedDeadline(t *testing.T) {
	now := taskworkspace.Instant(100)
	authority := taskworkspace.OwnedTransportMachineAuthority{
		ID: "task-orchestration-machine-1", Generation: 7, ExpiresAt: 10_000,
	}
	key := []byte("owned-transport-restart-key")
	bindings := taskworkspace.NewOwnedTransportBindingStore()
	backend := taskworkspace.NewInMemory(taskworkspaceTestConfig(&happyDurableObject{}))
	config := taskworkspace.OwnedTransportHarnessConfig{
		Lifecycle: backend, MachineAuthority: authority, AuthenticationKey: key,
		Authorize: func(scope taskworkspace.OwnedTransportAuthorityScope) bool {
			return scope.PolicyDomainID == "policy-domain-1" && scope.TaskID == "task-1"
		},
		Now: func() taskworkspace.Instant { return now }, RequestLifetime: 50,
		BindingStore: bindings,
	}
	first := taskworkspace.NewOwnedTransportHarness(config)
	confirmed, err := first.Lifecycle().ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-adapter-restart-1"),
	)
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	request := materializeRequest("policy-domain-1", "task-1", confirmed, "materialize-adapter-restart-1")
	first.SetConnected(false)
	_, err = first.Lifecycle().Materialize(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorRetryableUnavailable)
	if got := first.Requests()[len(first.Requests())-1].Envelope.Deadline; got != 150 {
		t.Fatalf("first adapter deadline = %d, want 150", got)
	}

	now = 151
	restarted := taskworkspace.NewOwnedTransportHarness(config)
	_, err = restarted.Lifecycle().Materialize(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	if got := restarted.Requests()[0].Envelope.Deadline; got != 150 {
		t.Fatalf("restarted adapter weakened deadline to %d, want persisted 150", got)
	}
}

func TestOwnedTransportSameOperationDifferentCanonicalPayloadFailsClosed(t *testing.T) {
	harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
	lifecycle := harness.Lifecycle()
	confirmed, err := lifecycle.ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-payload-1"),
	)
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	first := materializeRequest("policy-domain-1", "task-1", confirmed, "materialize-payload-1")
	if _, err := lifecycle.Materialize(context.Background(), first); err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	different := first
	different.CheckpointID = "different-checkpoint"
	different.Operation.RequestDigest = different.CanonicalRequestDigest()
	_, err = lifecycle.Materialize(context.Background(), different)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
}

func TestOwnedTransportCanonicalizationTimeoutAndResponseAuthentication(t *testing.T) {
	t.Run("non-canonical payload", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		harness.FailNext(taskworkspace.OwnedTransportNonCanonicalPayload)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-non-canonical-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})

	t.Run("timeout after delivery", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		request := confirmRequest("policy-domain-1", "task-1", "confirm-timeout-1")
		harness.FailNext(taskworkspace.OwnedTransportTimeoutAfterDelivery)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(context.Background(), request)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
		inspection, inspectErr := harness.Lifecycle().InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
			PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, OperationID: request.Operation.ID,
		})
		if inspectErr != nil || inspection.Disposition != taskworkspace.OperationTerminal ||
			inspection.ConfirmTaskWorkspace == nil {
			t.Fatalf("inspect timed-out operation = %#v, err = %v", inspection, inspectErr)
		}
	})

	t.Run("round trip deadline ambiguity", func(t *testing.T) {
		client := taskworkspace.NewOwnedTransportClient(taskworkspace.OwnedTransportClientConfig{
			Transport: ownedTransportRoundTripperFunc(func(
				context.Context, taskworkspace.OwnedTransportRequest,
			) (taskworkspace.OwnedTransportResponse, error) {
				return taskworkspace.OwnedTransportResponse{}, context.DeadlineExceeded
			}),
			MachineAuthority: taskworkspace.OwnedTransportMachineAuthority{
				ID: "deadline-machine-1", Generation: 1, ExpiresAt: 1_000,
			},
			AuthenticationKey: []byte("deadline-key"),
			Now:               func() taskworkspace.Instant { return 100 }, RequestLifetime: 50,
		})
		_, err := client.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-deadline-ambiguity-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	})

	t.Run("forged response", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		harness.FailNext(taskworkspace.OwnedTransportForgedResponse)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-forged-response-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})

	t.Run("forged callback", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		harness.FailNext(taskworkspace.OwnedTransportForgedCallback)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-forged-callback-1"),
		)
		if err != nil {
			t.Fatalf("response paired with forged callback: %v", err)
		}
		if len(harness.Callbacks()) != 1 || len(harness.AcceptedCallbacks()) != 0 {
			t.Fatalf("forged callback deliveries=%d accepted=%d, want 1/0", len(harness.Callbacks()), len(harness.AcceptedCallbacks()))
		}
	})

	t.Run("forged error response payload", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		harness.FailNext(taskworkspace.OwnedTransportForgedErrorResponse)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-forged-error-response-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})

	t.Run("forged error callback payload", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		harness.FailNext(taskworkspace.OwnedTransportForgedErrorCallback)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-forged-error-callback-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorRetryableUnavailable)
		if len(harness.Callbacks()) != 1 || len(harness.AcceptedCallbacks()) != 0 {
			t.Fatalf("forged error callback deliveries=%d accepted=%d, want 1/0", len(harness.Callbacks()), len(harness.AcceptedCallbacks()))
		}
	})

	t.Run("unknown wire error code", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		harness.FailNext(taskworkspace.OwnedTransportUnknownWireError)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-unknown-wire-error-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})
}

func TestOwnedTransportWireErrorsPreserveSafeSemanticsWithoutRawFailure(t *testing.T) {
	harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
	harness.FailNext(taskworkspace.OwnedTransportUnsafeFailure)
	_, err := harness.Lifecycle().ConfirmTaskWorkspace(
		context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-unsafe-failure-1"),
	)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorRetryableUnavailable)
	if lifecycleError := err.(*taskworkspace.Error); lifecycleError.SafeCategory() != taskworkspace.SafeErrorRetryableUnavailable ||
		!lifecycleError.Retryable() || lifecycleError.ReconciliationRequired() {
		t.Fatalf("unsafe dependency semantics = %#v", lifecycleError)
	}

	callbacks := harness.Callbacks()
	if len(callbacks) != 1 || callbacks[0].Error == nil ||
		callbacks[0].Error.SafeCategory != taskworkspace.SafeErrorRetryableUnavailable ||
		!callbacks[0].Error.Retryable || callbacks[0].Error.ReconciliationRequired {
		t.Fatalf("normalized wire error = %#v", callbacks)
	}

	captured, marshalErr := json.Marshal(struct {
		Requests  []taskworkspace.OwnedTransportRequest
		Responses []taskworkspace.OwnedTransportResponse
		Callbacks []taskworkspace.OwnedTransportCallback
		Signals   []taskworkspace.OwnedTransportOperationalSignal
	}{harness.Requests(), harness.Responses(), callbacks, harness.OperationalSignals()})
	if marshalErr != nil {
		t.Fatalf("marshal captured owned transport traffic: %v", marshalErr)
	}
	for _, secret := range []string{
		"unsafe vendor", "path", "session", "mount", "locator", "credential", "content",
		"owned-transport-test-key",
	} {
		if strings.Contains(strings.ToLower(string(captured)), strings.ToLower(secret)) ||
			strings.Contains(strings.ToLower(err.Error()), strings.ToLower(secret)) {
			t.Fatalf("owned transport traffic or error leaked %q", secret)
		}
	}
}

func TestOwnedTransportPreservesAuthorizationIntegrityFencingAndAmbiguityCategories(t *testing.T) {
	t.Run("authorization", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-2", "task-2", "confirm-denied-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorOwnershipDenied)
		assertLastOwnedTransportWireCategory(t, harness, taskworkspace.SafeErrorAuthorizationDenied, false, false)
	})

	t.Run("integrity", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		harness.FailNext(taskworkspace.OwnedTransportNonCanonicalPayload)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-integrity-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
		assertLastOwnedTransportWireCategory(t, harness, taskworkspace.SafeErrorIdempotencyConflict, false, false)
	})

	t.Run("fencing", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		lifecycle := harness.Lifecycle()
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-stale-1"),
		)
		if err != nil {
			t.Fatalf("confirm Task Workspace: %v", err)
		}
		stale := materializeRequest("policy-domain-1", "task-1", confirmed, "materialize-stale-1")
		stale.Generation++
		stale.Operation.RequestDigest = stale.CanonicalRequestDigest()
		_, err = lifecycle.Materialize(context.Background(), stale)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
		assertLastOwnedTransportWireCategory(
			t, harness, taskworkspace.SafeErrorStaleRevisionGenerationFence, false, false,
		)
	})

	t.Run("ambiguity", func(t *testing.T) {
		harness := newOwnedTransportHarness(t, func() taskworkspace.Instant { return 100 })
		harness.FailNext(taskworkspace.OwnedTransportResponseLoss)
		_, err := harness.Lifecycle().ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-ambiguous-1"),
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
		lifecycleError := err.(*taskworkspace.Error)
		if lifecycleError.Retryable() || !lifecycleError.ReconciliationRequired() {
			t.Fatalf("ambiguous lifecycle error semantics = %#v", lifecycleError)
		}
		signals := harness.OperationalSignals()
		if len(signals) != 1 || signals[0].SafeCategory != taskworkspace.SafeErrorReconciliationRequired ||
			!signals[0].ReconciliationRequired || signals[0].Retryable {
			t.Fatalf("ambiguous transport signal = %#v", signals)
		}
	})
}

func TestTaskWorkspaceLifecycleExternalContractRunsAcrossOwnedTransport(t *testing.T) {
	for _, adapter := range lifecycleContractAdapters() {
		if adapter.name == "owned transport" {
			runLifecycleAdapterExternalContract(t, adapter)
			return
		}
	}
	t.Fatal("owned transport is not registered in the shared lifecycle contract suite")
}

func TestOwnedTransportPreservesSupplementalLifecycleSemantics(t *testing.T) {
	t.Run("exact-generation reclamation dispatches once", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		mechanics := &checkpointReclamationMechanics{present: true}
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Now = func() taskworkspace.Instant { return now }
		config.CheckpointReclamation = mechanics
		lifecycle := ownedTransportLifecycle(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-transport-retention-1",
		))
		if err != nil {
			t.Fatalf("open first retention proposal: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		older, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-transport-retention-1",
		))
		if err != nil {
			t.Fatalf("commit first retained Checkpoint: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-transport-retention-1"),
		)
		if err != nil {
			t.Fatalf("confirm first retained state: %v", err)
		}
		secondMaterialization, err := lifecycle.Materialize(
			context.Background(), materializeRequest(
				"policy-domain-1", "task-1", current, "materialize-transport-retention-2",
			),
		)
		if err != nil {
			t.Fatalf("materialize for second Checkpoint: %v", err)
		}
		secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", current, secondMaterialization,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-transport-retention-2",
		))
		if err != nil {
			t.Fatalf("open second retention proposal: %v", err)
		}
		secondManifest := declaredStateManifest("content-2")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			current, secondView, secondManifest,
			acceptedValidationEvidence(current, secondView, secondManifest),
			"commit-transport-retention-2",
		)); err != nil {
			t.Fatalf("commit second retained Checkpoint: %v", err)
		}
		current, err = lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-transport-retention-2"),
		)
		if err != nil {
			t.Fatalf("confirm second retained state: %v", err)
		}
		released := releaseFinalCheckpointAuthority(
			t, lifecycle, current, older.CheckpointID, "release-transport-retention-1",
		)
		beforeGrace, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
			current, older.CheckpointID, released.RetentionGeneration, "reclaim-transport-before-grace-1",
		))
		if err != nil || beforeGrace.Outcome != taskworkspace.CheckpointRetainedByAuthority {
			t.Fatalf("reclaim before grace = %#v, err = %v", beforeGrace, err)
		}
		now = released.EligibleAt
		reclaimed, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
			current, older.CheckpointID, released.RetentionGeneration, "reclaim-transport-after-grace-1",
		))
		if err != nil || reclaimed.Outcome != taskworkspace.CheckpointReclaimed || mechanics.calls != 1 {
			t.Fatalf("exact-generation reclaim = %#v, calls = %d, err = %v", reclaimed, mechanics.calls, err)
		}
	})

	t.Run("Cleanup Debt preserves reclaimed disposition", func(t *testing.T) {
		cleanup := &exactGenerationCleanupDouble{
			inspectionDisposition: taskworkspace.CleanupInspectionEligible,
			attemptOutcome:        taskworkspace.CleanupReclaimed,
		}
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Cleanup = cleanup
		lifecycle := ownedTransportLifecycle(config)
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-transport-cleanup-1"),
		)
		if err != nil {
			t.Fatalf("confirm Cleanup Debt workspace: %v", err)
		}
		create := createCleanupObligationRequest(confirmed, "create-transport-cleanup-1")
		debt, err := lifecycle.CreateCleanupObligation(context.Background(), create)
		if err != nil {
			t.Fatalf("create Cleanup Debt: %v", err)
		}
		replayedDebt, err := lifecycle.CreateCleanupObligation(context.Background(), create)
		if err != nil || replayedDebt.DebtID != debt.DebtID {
			t.Fatalf("Cleanup Debt replay = %#v, err = %v", replayedDebt, err)
		}
		claimed, err := lifecycle.ClaimCleanupDebt(
			context.Background(), claimCleanupDebtRequest(debt, debt.RetryGeneration, "claim-transport-cleanup-1"),
		)
		if err != nil {
			t.Fatalf("claim Cleanup Debt: %v", err)
		}
		resolved, err := lifecycle.ReconcileCleanupDebt(
			context.Background(), reconcileCleanupDebtRequest(claimed, "reconcile-transport-cleanup-1"),
		)
		if err != nil || resolved.DebtID != debt.DebtID ||
			resolved.State != taskworkspace.CleanupDebtResolved ||
			resolved.Resolution != taskworkspace.CleanupReclaimed {
			t.Fatalf("reconciled Cleanup Debt = %#v, err = %v", resolved, err)
		}
	})

	t.Run("dependency failure remains non-leaking", func(t *testing.T) {
		const canary = "raw vendor content /private/path session mount locator credential=do-not-disclose"
		config := taskworkspaceTestConfig(&happyDurableObject{prepareError: errors.New(canary)})
		lifecycle := ownedTransportLifecycle(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-transport-non-leakage-1",
		))
		if err != nil {
			t.Fatalf("open non-leakage proposal: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-transport-non-leakage-1",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
		for _, forbidden := range []string{
			canary, "vendor", "/private", "path", "session", "mount", "locator", "credential", "do-not-disclose",
		} {
			if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(forbidden)) {
				t.Fatalf("lifecycle error leaked %q: %v", forbidden, err)
			}
		}
	})
}

func ownedTransportLifecycle(config taskworkspace.InMemoryConfig) taskworkspace.Lifecycle {
	now := config.Now
	if now == nil {
		now = func() taskworkspace.Instant { return 100 }
		config.Now = now
	}
	harness := taskworkspace.NewOwnedTransportHarness(taskworkspace.OwnedTransportHarnessConfig{
		Lifecycle: taskworkspace.NewInMemory(config),
		MachineAuthority: taskworkspace.OwnedTransportMachineAuthority{
			ID: "task-orchestration-contract-machine", Generation: 11, ExpiresAt: 1<<62 - 1,
		},
		AuthenticationKey: []byte("owned-transport-contract-key"),
		Authorize: func(scope taskworkspace.OwnedTransportAuthorityScope) bool {
			return scope.ScopeKind == taskworkspace.OwnedTransportModuleScope ||
				(scope.ScopeKind == taskworkspace.OwnedTransportTaskScope &&
					scope.PolicyDomainID != "" && scope.TaskID != "")
		},
		Now: now, RequestLifetime: 1 << 40,
	})
	return harness.Lifecycle()
}

func TestOwnedTransportRejectsUnauthenticatedMachineWithoutDispatch(t *testing.T) {
	now := func() taskworkspace.Instant { return 100 }
	authority := taskworkspace.OwnedTransportMachineAuthority{
		ID: "task-orchestration-machine-1", Generation: 7, ExpiresAt: 1_000,
	}
	harness := taskworkspace.NewOwnedTransportHarness(taskworkspace.OwnedTransportHarnessConfig{
		Lifecycle:         taskworkspace.NewInMemory(taskworkspaceTestConfig(&happyDurableObject{})),
		MachineAuthority:  authority,
		AuthenticationKey: []byte("server-key"),
		Authorize:         func(taskworkspace.OwnedTransportAuthorityScope) bool { return true },
		Now:               now,
		RequestLifetime:   50,
	})
	client := taskworkspace.NewOwnedTransportClient(taskworkspace.OwnedTransportClientConfig{
		Transport:                 harness,
		MachineAuthority:          authority,
		AuthenticationKey:         []byte("wrong-key"),
		ResponseAuthenticationKey: []byte("server-key"),
		Now:                       now,
		RequestLifetime:           50,
	})

	_, err := client.ConfirmTaskWorkspace(
		context.Background(),
		confirmRequest("policy-domain-1", "task-1", "confirm-owned-unauthenticated"),
	)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorOwnershipDenied {
		t.Fatalf("unauthenticated transport error = %T/%v, want ownership denial", err, err)
	}

	inspection, inspectErr := harness.Lifecycle().InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    "confirm-owned-unauthenticated",
	})
	if inspectErr == nil || inspection.Disposition != "" {
		t.Fatalf("unauthenticated request reached lifecycle: inspection=%#v, err=%v", inspection, inspectErr)
	}
}

func newOwnedTransportHarness(
	t *testing.T,
	now func() taskworkspace.Instant,
) *taskworkspace.OwnedTransportHarness {
	t.Helper()
	authority := taskworkspace.OwnedTransportMachineAuthority{
		ID: "task-orchestration-machine-1", Generation: 7, ExpiresAt: 10_000,
	}
	return taskworkspace.NewOwnedTransportHarness(taskworkspace.OwnedTransportHarnessConfig{
		Lifecycle:         taskworkspace.NewInMemory(taskworkspaceTestConfig(&happyDurableObject{})),
		MachineAuthority:  authority,
		AuthenticationKey: []byte("owned-transport-test-key"),
		Authorize: func(scope taskworkspace.OwnedTransportAuthorityScope) bool {
			return scope.PolicyDomainID == "policy-domain-1" && scope.TaskID == "task-1"
		},
		Now: now, RequestLifetime: 50,
	})
}

func assertLastOwnedTransportWireCategory(
	t *testing.T,
	harness *taskworkspace.OwnedTransportHarness,
	category taskworkspace.SafeErrorCategory,
	retryable bool,
	reconciliationRequired bool,
) {
	t.Helper()
	callbacks := harness.Callbacks()
	if len(callbacks) == 0 {
		t.Fatal("owned transport did not capture a callback")
	}
	wireError := callbacks[len(callbacks)-1].Error
	if wireError == nil || wireError.SafeCategory != category ||
		wireError.Retryable != retryable || wireError.ReconciliationRequired != reconciliationRequired {
		t.Fatalf("owned transport wire error = %#v", wireError)
	}
}

func assertOwnedTransportDeliveryKinds(
	t *testing.T,
	deliveries []taskworkspace.OwnedTransportDelivery,
	want ...taskworkspace.OwnedTransportDeliveryKind,
) {
	t.Helper()
	if len(deliveries) != len(want) {
		t.Fatalf("delivery count = %d, want %d: %#v", len(deliveries), len(want), deliveries)
	}
	for index := range want {
		if deliveries[index].Kind != want[index] {
			t.Fatalf("delivery %d kind = %q, want %q: %#v", index, deliveries[index].Kind, want[index], deliveries)
		}
	}
}

type ownedTransportRoundTripperFunc func(
	context.Context,
	taskworkspace.OwnedTransportRequest,
) (taskworkspace.OwnedTransportResponse, error)

func (f ownedTransportRoundTripperFunc) RoundTrip(
	ctx context.Context,
	request taskworkspace.OwnedTransportRequest,
) (taskworkspace.OwnedTransportResponse, error) {
	return f(ctx, request)
}
