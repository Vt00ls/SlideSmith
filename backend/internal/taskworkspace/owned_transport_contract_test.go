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
	adapters := []struct {
		name    string
		factory func(taskworkspace.InMemoryConfig) taskworkspace.Lifecycle
	}{
		{
			name: "in_memory",
			factory: func(config taskworkspace.InMemoryConfig) taskworkspace.Lifecycle {
				return taskworkspace.NewInMemory(config)
			},
		},
		{
			name:    "owned_transport",
			factory: ownedTransportLifecycle,
		},
	}
	for _, adapter := range adapters {
		t.Run(adapter.name, func(t *testing.T) {
			runTaskWorkspaceLifecycleExternalContract(t, adapter.factory)
		})
	}
}

func runTaskWorkspaceLifecycleExternalContract(
	t *testing.T,
	factory func(taskworkspace.InMemoryConfig) taskworkspace.Lifecycle,
) {
	t.Helper()
	t.Run("initialize materialize isolate commit discard idempotency fencing and integrity", func(t *testing.T) {
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.CurrentSandboxLeaseAuthorities = append(
			config.CurrentSandboxLeaseAuthorities,
			sandboxLeaseAuthority("policy-domain-1", "task-1", "phase-run-1", "runtime-run-3", "sandbox-lease-4"),
		)
		lifecycle := factory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		stable, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-contract-stable-1"),
		)
		if err != nil || stable.TaskWorkspaceID != confirmed.TaskWorkspaceID {
			t.Fatalf("stable Task Workspace = %#v, err = %v", stable, err)
		}

		discardedView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-discard-1",
		))
		if err != nil {
			t.Fatalf("open discard proposal: %v", err)
		}
		winningView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-contract-winner-1",
		))
		if err != nil {
			t.Fatalf("open winning proposal: %v", err)
		}
		staleView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-3", "sandbox-lease-4", "open-contract-stale-1",
		))
		if err != nil {
			t.Fatalf("open stale proposal: %v", err)
		}

		discard := discardRequest(
			confirmed, discardedView, taskworkspace.RuntimeViewValidationRejected, "discard-contract-1",
		)
		firstDiscard, err := lifecycle.DiscardRuntimeView(context.Background(), discard)
		if err != nil {
			t.Fatalf("discard rejected proposal: %v", err)
		}
		replayedDiscard, err := lifecycle.DiscardRuntimeView(context.Background(), discard)
		if err != nil || !reflect.DeepEqual(firstDiscard, replayedDiscard) {
			t.Fatalf("discard replay = %#v, err = %v", replayedDiscard, err)
		}

		manifest := declaredStateManifest("content-1")
		commit := commitRequest(
			confirmed,
			winningView,
			manifest,
			acceptedValidationEvidence(confirmed, winningView, manifest),
			"commit-contract-1",
		)
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil || committed.RevisionID == "" || committed.CheckpointID == "" ||
			committed.RevisionID == confirmed.CurrentRevisionID {
			t.Fatalf("validated commit = %#v, err = %v", committed, err)
		}
		replayedCommit, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil || !reflect.DeepEqual(replayedCommit, committed) {
			t.Fatalf("commit replay = %#v, err = %v", replayedCommit, err)
		}

		staleManifest := declaredStateManifest("content-2")
		staleCommit := commitRequest(
			confirmed,
			staleView,
			staleManifest,
			acceptedValidationEvidence(confirmed, staleView, staleManifest),
			"commit-contract-stale-1",
		)
		_, err = lifecycle.CommitRuntimeView(context.Background(), staleCommit)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)

		differentPayload := commit
		differentPayload.DeclaredStateManifest = staleManifest
		differentPayload.ValidationEvidence = acceptedValidationEvidence(confirmed, winningView, staleManifest)
		differentPayload.Operation.RequestDigest = differentPayload.CanonicalRequestDigest()
		_, err = lifecycle.CommitRuntimeView(context.Background(), differentPayload)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)

		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-contract-current-1"),
		)
		if err != nil || current.CurrentRevisionID != committed.RevisionID ||
			current.CurrentCheckpointID != committed.CheckpointID {
			t.Fatalf("authoritative state after commit = %#v, err = %v", current, err)
		}
		currentMaterialization, err := lifecycle.Materialize(
			context.Background(), materializeRequest("policy-domain-1", "task-1", current, "materialize-contract-current-1"),
		)
		if err != nil {
			t.Fatalf("materialize committed state: %v", err)
		}
		fencedView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", current, currentMaterialization,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-fenced-1",
		))
		if err != nil {
			t.Fatalf("open proposal to fence: %v", err)
		}
		if _, err := lifecycle.FenceRuntimeView(context.Background(), fenceRequest(
			current, fencedView, taskworkspace.RuntimeViewCancelled, "fence-contract-1",
		)); err != nil {
			t.Fatalf("fence Runtime View: %v", err)
		}
		lateManifest := declaredStateManifest("content-2")
		_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			current,
			fencedView,
			lateManifest,
			acceptedValidationEvidence(current, fencedView, lateManifest),
			"commit-contract-after-fence-1",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	})

	t.Run("expire and restore exact Checkpoint without changing identity", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Now = func() taskworkspace.Instant { return now }
		config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
			ID: "expiry-policy-1", MaterializationLifetime: 10, RuntimeViewLifetime: 10,
		}
		config.RecoveryAuthorityID = "recovery-authority-1"
		config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
			return recoveryIntent, id != "" && id == recoveryIntent.ID
		}
		lifecycle := factory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-restore-1",
		))
		if err != nil {
			t.Fatalf("open restore proposal: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-contract-restore-1",
		))
		if err != nil {
			t.Fatalf("commit restorable Checkpoint: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-contract-restore-current-1"),
		)
		if err != nil {
			t.Fatalf("confirm restorable state: %v", err)
		}
		currentMaterialization, err := lifecycle.Materialize(
			context.Background(), materializeRequest(
				"policy-domain-1", "task-1", current, "materialize-contract-expiring-view-1",
			),
		)
		if err != nil {
			t.Fatalf("materialize expiring Runtime View base: %v", err)
		}
		expiringView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", current, currentMaterialization,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-expiring-view-1",
		))
		if err != nil {
			t.Fatalf("open expiring Runtime View: %v", err)
		}
		now = 301
		if _, err := lifecycle.ExpireMaterialization(
			context.Background(), expireMaterializationRequest(confirmed, materialized, "expire-contract-materialization-1"),
		); err != nil {
			t.Fatalf("expire disposable materialization: %v", err)
		}
		expireView := taskworkspace.ExpireRuntimeViewRequest{
			PolicyDomainID:    "policy-domain-1",
			TaskID:            "task-1",
			TaskWorkspaceID:   current.TaskWorkspaceID,
			RuntimeViewID:     expiringView.RuntimeViewID,
			MaterializationID: currentMaterialization.MaterializationID,
			BaseRevisionID:    current.CurrentRevisionID,
			Generation:        current.Generation,
			Fence:             current.Fence,
			ExpiryPolicyID:    "expiry-policy-1",
			Operation:         taskworkspace.Operation{ID: "expire-contract-runtime-view-1"},
		}
		expireView.Operation.RequestDigest = expireView.CanonicalRequestDigest()
		expiredView, err := lifecycle.ExpireRuntimeView(context.Background(), expireView)
		if err != nil || expiredView.RuntimeViewID != expiringView.RuntimeViewID {
			t.Fatalf("expire Runtime View = %#v, err = %v", expiredView, err)
		}
		recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-contract-1")
		recoveryIntent.ExpiresAt = 1_000
		recoveryIntent.Digest = recoveryIntent.CanonicalDigest()
		restore := taskworkspace.RestoreTaskWorkspaceRequest{
			Intent: recoveryIntent, Operation: taskworkspace.Operation{ID: "restore-contract-1"},
		}
		restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
		restored, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
		if err != nil || restored.TaskWorkspaceID != confirmed.TaskWorkspaceID ||
			restored.RevisionID != committed.RevisionID || restored.CheckpointID != committed.CheckpointID ||
			restored.Generation <= current.Generation || restored.Fence <= current.Fence {
			t.Fatalf("restored exact Checkpoint = %#v, err = %v", restored, err)
		}
		replayed, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
		if err != nil || !reflect.DeepEqual(replayed, restored) {
			t.Fatalf("restore replay = %#v, err = %v", replayed, err)
		}
	})

	t.Run("reconstruct exact Artifact Version without changing authoritative history", func(t *testing.T) {
		var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
		reconstruction := &reconstructionInputDouble{}
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.RecoveryAuthorityID = "recovery-authority-1"
		config.ReconstructionInput = reconstruction
		config.CurrentRecoveryIntent = func(
			id taskworkspace.RecoveryIntentID,
		) (taskworkspace.AuthorizedRecoveryIntent, bool) {
			return recoveryIntent, recoveryIntent.ID == id
		}
		lifecycle := factory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-reconstruct-1",
		))
		if err != nil {
			t.Fatalf("open reconstruction base: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-contract-reconstruct-1",
		)); err != nil {
			t.Fatalf("commit reconstruction base: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-contract-reconstruct-1"),
		)
		if err != nil {
			t.Fatalf("confirm reconstruction base: %v", err)
		}
		recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-contract-artifact-1")
		request := taskworkspace.ReconstructTaskWorkspaceRequest{
			Intent: recoveryIntent, Operation: taskworkspace.Operation{ID: "reconstruct-contract-artifact-1"},
		}
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		reconstructed, err := lifecycle.ReconstructTaskWorkspace(context.Background(), request)
		if err != nil || reconstructed.TaskWorkspaceID != current.TaskWorkspaceID ||
			reconstructed.CurrentRevisionID != current.CurrentRevisionID ||
			reconstructed.CurrentCheckpointID != current.CurrentCheckpointID ||
			reconstructed.Generation != current.Generation+1 || reconstructed.Fence != current.Fence+1 ||
			reconstruction.artifactVerifications != 1 {
			t.Fatalf("reconstruct exact Artifact Version = %#v, err = %v", reconstructed, err)
		}
		replayed, err := lifecycle.ReconstructTaskWorkspace(context.Background(), request)
		if err != nil || !reflect.DeepEqual(replayed, reconstructed) || reconstruction.artifactVerifications != 1 {
			t.Fatalf("reconstruction replay = %#v, err = %v", replayed, err)
		}
	})

	t.Run("retention grace and exact-generation reclamation", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		mechanics := &checkpointReclamationMechanics{present: true}
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Now = func() taskworkspace.Instant { return now }
		config.CheckpointReclamation = mechanics
		lifecycle := factory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-retention-1",
		))
		if err != nil {
			t.Fatalf("open first retention proposal: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		older, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-contract-retention-1",
		))
		if err != nil {
			t.Fatalf("commit first retained Checkpoint: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-contract-retention-1"),
		)
		if err != nil {
			t.Fatalf("confirm first retained state: %v", err)
		}
		secondMaterialization, err := lifecycle.Materialize(
			context.Background(), materializeRequest("policy-domain-1", "task-1", current, "materialize-contract-retention-2"),
		)
		if err != nil {
			t.Fatalf("materialize for second Checkpoint: %v", err)
		}
		secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", current, secondMaterialization,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-contract-retention-2",
		))
		if err != nil {
			t.Fatalf("open second retention proposal: %v", err)
		}
		secondManifest := declaredStateManifest("content-2")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			current,
			secondView,
			secondManifest,
			acceptedValidationEvidence(current, secondView, secondManifest),
			"commit-contract-retention-2",
		)); err != nil {
			t.Fatalf("commit second retained Checkpoint: %v", err)
		}
		current, err = lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-contract-retention-2"),
		)
		if err != nil {
			t.Fatalf("confirm second retained state: %v", err)
		}
		released := releaseFinalCheckpointAuthority(
			t, lifecycle, current, older.CheckpointID, "release-contract-retention-1",
		)
		beforeGrace, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
			current, older.CheckpointID, released.RetentionGeneration, "reclaim-contract-before-grace-1",
		))
		if err != nil || beforeGrace.Outcome != taskworkspace.CheckpointRetainedByAuthority {
			t.Fatalf("reclaim before grace = %#v, err = %v", beforeGrace, err)
		}
		now = released.EligibleAt
		reclaimed, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
			current, older.CheckpointID, released.RetentionGeneration, "reclaim-contract-after-grace-1",
		))
		if err != nil || reclaimed.Outcome != taskworkspace.CheckpointReclaimed || mechanics.calls != 1 {
			t.Fatalf("exact-generation reclaim = %#v, calls = %d, err = %v", reclaimed, mechanics.calls, err)
		}
	})

	t.Run("Cleanup Debt retries one identity and mandatory audit protects exception", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		administrator := platformAdministratorAuthority(now)
		cleanup := &exactGenerationCleanupDouble{
			inspectionDisposition: taskworkspace.CleanupInspectionEligible,
			attemptOutcome:        taskworkspace.CleanupReclaimed,
		}
		audit := &cleanupAuditDouble{}
		externalAudit := &auditDeliveryDouble{}
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Now = func() taskworkspace.Instant { return now }
		config.Cleanup = cleanup
		config.CleanupAudit = audit
		config.AuditDelivery = externalAudit
		config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
		config.CurrentPlatformAdministratorAuthority = func(
			id taskworkspace.PlatformAdministratorID,
		) (taskworkspace.PlatformAdministratorAuthority, bool) {
			return administrator, id == administrator.ID
		}
		lifecycle := factory(config)
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-contract-cleanup-1"),
		)
		if err != nil {
			t.Fatalf("confirm Cleanup Debt workspace: %v", err)
		}
		create := createCleanupObligationRequest(confirmed, "create-contract-cleanup-1")
		debt, err := lifecycle.CreateCleanupObligation(context.Background(), create)
		if err != nil {
			t.Fatalf("create Cleanup Debt: %v", err)
		}
		replayedDebt, err := lifecycle.CreateCleanupObligation(context.Background(), create)
		if err != nil || replayedDebt.DebtID != debt.DebtID {
			t.Fatalf("Cleanup Debt replay = %#v, err = %v", replayedDebt, err)
		}
		claimed, err := lifecycle.ClaimCleanupDebt(
			context.Background(), claimCleanupDebtRequest(debt, debt.RetryGeneration, "claim-contract-cleanup-1"),
		)
		if err != nil {
			t.Fatalf("claim Cleanup Debt: %v", err)
		}
		resolved, err := lifecycle.ReconcileCleanupDebt(
			context.Background(), reconcileCleanupDebtRequest(claimed, "reconcile-contract-cleanup-1"),
		)
		if err != nil || resolved.DebtID != debt.DebtID ||
			resolved.State != taskworkspace.CleanupDebtResolved || resolved.Resolution != taskworkspace.CleanupReclaimed {
			t.Fatalf("reconciled Cleanup Debt = %#v, err = %v", resolved, err)
		}

		exceptionDebt, err := lifecycle.CreateCleanupObligation(
			context.Background(), func() taskworkspace.CreateCleanupObligationRequest {
				request := createCleanupObligationRequest(confirmed, "create-contract-exception-1")
				request.ResourceID = "opaque-cleanup-resource-2"
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
				return request
			}(),
		)
		if err != nil {
			t.Fatalf("create exception Cleanup Debt: %v", err)
		}
		accepted, err := lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
			exceptionDebt, administrator, "resolve-contract-exception-1",
		))
		if err != nil || accepted.Resolution != taskworkspace.CleanupAcceptedException ||
			accepted.ResolutionAuditEvidenceRoot == "" || audit.calls != 1 || externalAudit.calls != 1 {
			t.Fatalf("audited Cleanup Debt exception = %#v, audit calls = %d, err = %v", accepted, audit.calls, err)
		}
		backlog, err := lifecycle.RebuildAuditDelivery(
			context.Background(), taskworkspace.AuditDeliveryRebuildRequest{},
		)
		if err != nil || !backlog.Pending.Known || backlog.Pending.Value != 0 ||
			!backlog.Delivered.Known || backlog.Delivered.Value != 1 || len(backlog.Evidence) != 1 {
			t.Fatalf("rebuilt audit delivery backlog = %#v, err = %v", backlog, err)
		}
	})

	t.Run("telemetry outage cannot roll back authoritative commit", func(t *testing.T) {
		telemetry := &telemetryDouble{failure: errors.New("collector unavailable")}
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
		config.ProjectionSchemaRevision = taskworkspace.ProjectionSchemaV1
		lifecycle := factory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-telemetry-1",
		))
		if err != nil {
			t.Fatalf("open telemetry proposal: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed,
			view,
			manifest,
			acceptedValidationEvidence(confirmed, view, manifest),
			"commit-contract-telemetry-1",
		))
		if err != nil || committed.RevisionID == "" || telemetry.calls == 0 {
			t.Fatalf("commit through telemetry outage = %#v, calls = %d, err = %v", committed, telemetry.calls, err)
		}
	})

	t.Run("dependency failures do not leak content path session mount locator vendor or credentials", func(t *testing.T) {
		canary := "raw vendor content /private/path session mount locator credential=do-not-disclose"
		config := taskworkspaceTestConfig(&happyDurableObject{prepareError: errors.New(canary)})
		lifecycle := factory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-non-leakage-1",
		))
		if err != nil {
			t.Fatalf("open non-leakage proposal: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-contract-non-leakage-1",
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
