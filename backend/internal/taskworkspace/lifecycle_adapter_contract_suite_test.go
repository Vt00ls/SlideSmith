package taskworkspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestTaskWorkspaceLifecycleExternalContract(t *testing.T) {
	for _, adapter := range lifecycleContractAdapters() {
		adapter := adapter
		t.Run(adapter.name, func(t *testing.T) {
			runLifecycleAdapterExternalContract(t, adapter)
		})
	}
}

func runLifecycleAdapterExternalContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	runLifecycleInvalidCompoundIntentContract(t, adapter)
	runLifecycleIdentityCommitAndTerminalContract(t, adapter)
	runLifecycleIntegrityContract(t, adapter)
	runLifecycleResponseLossReconciliationContract(t, adapter)
	runLifecycleOwnershipAndFenceContract(t, adapter)
	runLifecycleExpiryAndRestoreContract(t, adapter)
	runLifecycleReconstructionContract(t, adapter)
	runLifecycleRetentionAndReclaimContract(t, adapter)
	runLifecycleRetentionRaceContract(t, adapter)
	runLifecycleCleanupDebtContract(t, adapter)
	runLifecycleAmbiguousCleanupRetryContract(t, adapter)
	runLifecycleMandatoryAuditContract(t, adapter)
	runLifecycleProjectionAndNonLeakageContract(t, adapter)
	runLifecycleProjectionFailureNormalizationContract(t, adapter)
	runLifecycleProjectionPanicNormalizationContract(t, adapter)
	runLifecycleTelemetryFailureAuthorityContract(t, adapter)
	runLifecycleTelemetryPanicRetryContract(t, adapter)
}

func runLifecycleProjectionFailureNormalizationContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("projection rebuild failure is closed retryable and non-leaking", func(t *testing.T) {
		const canary = "host=/private/c04-canary session=session-c04-canary mount=/mnt/c04-canary " +
			"bucket=bucket-c04-canary locator=object-key-c04-canary vendor=vendor-c04-canary " +
			"credential=credential-c04-canary token=token-c04-canary content=user-content-c04-canary " +
			"foreign_workspace=workspace-exists-c04-canary"
		for _, test := range []struct {
			name string
			err  error
		}{
			{name: "raw", err: errors.New(canary)},
			{name: "wrapped raw", err: fmt.Errorf("wrapped projection adapter failure: %w", errors.New(canary))},
			{name: "wrapped closed typed", err: fmt.Errorf("%s: %w", canary, &taskworkspace.Error{
				Code: taskworkspace.ErrorRetryableUnavailable,
			})},
			{name: "wrapped foreign lifecycle typed", err: fmt.Errorf("%s: %w", canary, &taskworkspace.Error{
				Code: taskworkspace.ErrorOwnershipDenied,
			})},
			{name: "wrapped unknown typed", err: fmt.Errorf("%s: %w", canary, &taskworkspace.Error{
				Code: taskworkspace.ErrorCode("unknown-projection-adapter-code"),
			})},
			{name: "temporary", err: temporaryProjectionError{message: canary}},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				projection := &failingLifecycleProjection{err: test.err}
				config := taskworkspaceTestConfig(nil)
				config.Projection = projection
				lifecycle := adapter.newConfigured(t, config)

				result, err := lifecycle.RebuildProjections(
					context.Background(),
					taskworkspace.ProjectionRebuildRequest{SchemaRevision: taskworkspace.ProjectionSchemaV1},
				)
				assertLifecycleErrorCode(t, err, taskworkspace.ErrorRetryableUnavailable)
				failure := err.(*taskworkspace.Error)
				if !failure.Retryable() || failure.ReconciliationRequired() ||
					failure.SafeCategory() != taskworkspace.SafeErrorRetryableUnavailable {
					t.Fatalf("projection failure semantics = %#v", failure)
				}
				if result.Projected.Known || result.SourceWatermark.Known {
					t.Fatalf("failed rebuild fabricated zero or promoted a partial projection: %#v", result)
				}

				formatted := fmt.Sprintf("%v | %+v | %#v", err, err, result)
				for _, forbidden := range strings.Fields(canary) {
					if strings.Contains(formatted, forbidden) {
						t.Fatalf("formatted lifecycle result leaked %q: %s", forbidden, formatted)
					}
				}
			})
		}
	})
}

func runLifecycleProjectionPanicNormalizationContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("ordinary projection panic cannot change or escape a lifecycle decision", func(t *testing.T) {
		const canary = "panic-host-/private/c04 session-c04 mount-c04 bucket-c04 object-key-c04 " +
			"vendor-c04 credential-c04 token-c04 user-content-c04 foreign-workspace-exists-c04"
		config := taskworkspaceTestConfig(nil)
		config.Projection = &panickingLifecycleProjection{value: canary}
		lifecycle := adapter.newConfigured(t, config)

		var confirmed taskworkspace.ConfirmTaskWorkspaceResult
		var err error
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("projection panic crossed the public Lifecycle seam: %v", recovered)
				}
			}()
			confirmed, err = lifecycle.ConfirmTaskWorkspace(
				context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-projection-panic"),
			)
		}()
		if err != nil || confirmed.TaskWorkspaceID == "" || confirmed.CurrentRevisionID == "" ||
			confirmed.Generation == 0 || confirmed.Fence == 0 {
			t.Fatalf("ordinary projection panic changed the authoritative decision: %#v, err=%v", confirmed, err)
		}
		rebuilt, err := lifecycle.RebuildProjections(
			context.Background(),
			taskworkspace.ProjectionRebuildRequest{SchemaRevision: taskworkspace.ProjectionSchemaV1},
		)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorRetryableUnavailable)
		if rebuilt.Projected.Known || rebuilt.SourceWatermark.Known || strings.Contains(err.Error(), canary) {
			t.Fatalf("projection rebuild panic escaped or fabricated zero: result=%#v err=%v", rebuilt, err)
		}
	})
}

func runLifecycleTelemetryFailureAuthorityContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("telemetry failure preserves authority audit diagnostics and exact rebuild retry", func(t *testing.T) {
		const canary = "host-private-c04-canary session-c04-canary mount-c04-canary " +
			"bucket-c04-canary object-locator-c04-canary vendor-c04-canary credential-c04-canary " +
			"token-c04-canary user-content-c04-canary foreign-workspace-exists-c04-canary"
		now := taskworkspace.Instant(100)
		administrator := platformAdministratorAuthority(now)
		telemetry := &telemetryDouble{failure: fmt.Errorf("wrapped telemetry adapter failure: %w", errors.New(canary))}
		mandatoryAudit := &capturingCleanupAuditDouble{}
		externalAudit := &auditDeliveryDouble{}
		diagnostics := &diagnosticsDouble{status: taskworkspace.DiagnosticsSourceStatus{
			State: taskworkspace.DiagnosticsSourceCurrent,
		}}
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
		config.CleanupAudit = mandatoryAudit
		config.AuditDelivery = externalAudit
		config.Diagnostics = diagnostics
		config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
		config.CurrentPlatformAdministratorAuthority = func(
			id taskworkspace.PlatformAdministratorID,
		) (taskworkspace.PlatformAdministratorAuthority, bool) {
			return administrator, id == administrator.ID
		}
		lifecycle := adapter.newConfigured(t, config)

		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-telemetry-failure", "materialize-telemetry-failure",
			"open-telemetry-failure",
		)
		manifest := declaredStateManifest("content-1")
		commitRequest := commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-telemetry-failure",
		)
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest)
		if err != nil || committed.RevisionID == "" || committed.CheckpointID == "" {
			t.Fatalf("ordinary telemetry failure changed commit: %#v, err=%v", committed, err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-after-telemetry-failure",
		))
		if err != nil || current.CurrentRevisionID != committed.RevisionID ||
			current.CurrentCheckpointID != committed.CheckpointID {
			t.Fatalf("ordinary telemetry failure changed Revision or Checkpoint: %#v, err=%v", current, err)
		}
		inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
			PolicyDomainID: commitRequest.PolicyDomainID,
			TaskID:         commitRequest.TaskID,
			OperationID:    commitRequest.Operation.ID,
		})
		if err != nil || inspection.Disposition != taskworkspace.OperationTerminal ||
			inspection.CommitRuntimeView == nil || !reflect.DeepEqual(*inspection.CommitRuntimeView, committed) {
			t.Fatalf("ordinary telemetry failure changed terminal decision: %#v, err=%v", inspection, err)
		}

		debt, err := lifecycle.CreateCleanupObligation(
			context.Background(), createCleanupObligationRequest(current, "create-telemetry-failure-debt"),
		)
		if err != nil || debt.State != taskworkspace.CleanupDebtOpen {
			t.Fatalf("ordinary telemetry failure changed Cleanup Debt creation: %#v, err=%v", debt, err)
		}
		resolved, err := lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
			debt, administrator, "resolve-telemetry-failure-debt",
		))
		if err != nil || resolved.State != taskworkspace.CleanupDebtResolved ||
			resolved.Resolution != taskworkspace.CleanupAcceptedException ||
			resolved.ResolutionAuditEvidenceRoot == "" || len(mandatoryAudit.facts) != 1 {
			t.Fatalf("ordinary telemetry failure changed mandatory audit or resolution: %#v, err=%v", resolved, err)
		}
		diagnostic, err := lifecycle.QueryAdministratorDiagnostics(
			context.Background(),
			administratorDiagnosticsRequest(resolved, administrator, "query-telemetry-failure-diagnostics"),
		)
		if err != nil || diagnostic.LifecycleState != taskworkspace.DiagnosticLifecycleResolved ||
			diagnostic.SourceState != taskworkspace.DiagnosticsSourceCurrent || len(mandatoryAudit.facts) != 2 {
			t.Fatalf("ordinary telemetry failure changed diagnostics or mandatory audit: %#v, err=%v", diagnostic, err)
		}

		failedRebuild, rebuildErr := lifecycle.RebuildProjections(
			context.Background(),
			taskworkspace.ProjectionRebuildRequest{SchemaRevision: taskworkspace.ProjectionSchemaV1},
		)
		assertLifecycleErrorCode(t, rebuildErr, taskworkspace.ErrorReconciliationRequired)
		rebuildFailure := rebuildErr.(*taskworkspace.Error)
		if rebuildFailure.Retryable() || !rebuildFailure.ReconciliationRequired() ||
			failedRebuild.Projected.Known || failedRebuild.SourceWatermark.Known {
			t.Fatalf("failed telemetry rebuild semantics = %#v, result=%#v", rebuildFailure, failedRebuild)
		}
		telemetry.failure = nil
		rebuilt, err := lifecycle.RebuildProjections(
			context.Background(),
			taskworkspace.ProjectionRebuildRequest{SchemaRevision: taskworkspace.ProjectionSchemaV1},
		)
		if err != nil || !rebuilt.Projected.Known || rebuilt.Projected.Value == 0 ||
			!rebuilt.SourceWatermark.Known || rebuilt.SourceWatermark.Value == 0 {
			t.Fatalf("exact projection rebuild retry = %#v, err=%v", rebuilt, err)
		}

		encoded, marshalErr := json.Marshal(struct {
			Committed      taskworkspace.CommitRuntimeViewResult
			Current        taskworkspace.ConfirmTaskWorkspaceResult
			Inspection     taskworkspace.OperationInspection
			Debt           taskworkspace.CleanupDebt
			Diagnostics    taskworkspace.AdministratorDiagnostics
			Telemetry      []taskworkspace.OperationalSignals
			AuditIntents   []taskworkspace.CleanupAuditIntent
			AuditFacts     []taskworkspace.CleanupAuditEvidence
			ExternalAudit  []taskworkspace.AuditDeliveryFact
			FailedRebuild  taskworkspace.ProjectionRebuildResult
			RebuildFailure string
			Rebuilt        taskworkspace.ProjectionRebuildResult
		}{
			committed, current, inspection, resolved, diagnostic, telemetry.signals,
			mandatoryAudit.intents, mandatoryAudit.facts, externalAudit.facts,
			failedRebuild, fmt.Sprintf("%v | %+v", rebuildErr, rebuildErr), rebuilt,
		})
		if marshalErr != nil {
			t.Fatalf("marshal telemetry failure evidence: %v", marshalErr)
		}
		for _, forbidden := range strings.Fields(canary) {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("lifecycle, telemetry, audit, diagnostics, or formatted result leaked %q: %s",
					forbidden, encoded)
			}
		}
	})
}

func runLifecycleTelemetryPanicRetryContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("telemetry panic records degraded retry state and exact rebuild recovers", func(t *testing.T) {
		const canary = "telemetry-panic-/private/c04 session-c04 mount-c04 bucket-c04 object-c04 " +
			"vendor-c04 credential-c04 token-c04 user-content-c04 foreign-workspace-c04"
		telemetry := &panickingTelemetryDouble{value: canary, panics: true}
		projector := taskworkspace.NewDeterministicProjection(telemetry)
		config := taskworkspaceTestConfig(nil)
		config.Projection = projector
		lifecycle := adapter.newConfigured(t, config)

		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-telemetry-panic"),
		)
		if err != nil || confirmed.TaskWorkspaceID == "" {
			t.Fatalf("telemetry panic changed authoritative confirm: %#v, err=%v", confirmed, err)
		}
		cursor, err := projector.Cursor(taskworkspace.ProjectionSchemaV1)
		if err != nil || !cursor.RetryPending.Known || cursor.RetryPending.Value == 0 ||
			cursor.SafeError != taskworkspace.SafeErrorReconciliationRequired {
			t.Fatalf("telemetry panic omitted degraded retry evidence: %#v, err=%v", cursor, err)
		}

		telemetry.panics = false
		rebuilt, err := lifecycle.RebuildProjections(
			context.Background(),
			taskworkspace.ProjectionRebuildRequest{SchemaRevision: taskworkspace.ProjectionSchemaV1},
		)
		if err != nil || !rebuilt.Projected.Known || rebuilt.Projected.Value == 0 {
			t.Fatalf("exact rebuild after telemetry panic = %#v, err=%v", rebuilt, err)
		}
		cursor, err = projector.Cursor(taskworkspace.ProjectionSchemaV1)
		if err != nil || cursor.RetryPending.Value != 0 || cursor.SafeError != "" {
			t.Fatalf("successful rebuild retained degraded retry evidence: %#v, err=%v", cursor, err)
		}
		encoded, marshalErr := json.Marshal(telemetry.signals)
		if marshalErr != nil || strings.Contains(string(encoded), canary) {
			t.Fatalf("telemetry panic evidence leaked canary: %s, err=%v", encoded, marshalErr)
		}
	})
}

func runLifecycleInvalidCompoundIntentContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("invalid compound intent is rejected before journaling", func(t *testing.T) {
		lifecycle := adapter.new(t)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-invalid-compound-contract",
			"materialize-invalid-compound-contract", "open-invalid-compound-contract",
		)
		manifest := declaredStateManifest("content-1")
		request := commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-invalid-compound-contract",
		)
		request.RuntimeViewID = ""
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		_, err := lifecycle.CommitRuntimeView(context.Background(), request)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorInvalidIntent)
		_, err = lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
			PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, OperationID: request.Operation.ID,
		})
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorInvalidIntent)
	})
}

func runLifecycleAmbiguousCleanupRetryContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("ambiguous cleanup retains evidence and retries the same debt", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.CleanupRetryPolicy = taskworkspace.CleanupRetryPolicy{
			ClaimLifetime: 20, InitialBackoff: 10, MaximumBackoff: 100,
		}
		lifecycle, recoverCleanup := adapter.newAmbiguousCleanup(t, config)
		confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-ambiguous-cleanup-contract",
		))
		if err != nil {
			t.Fatalf("confirm cleanup workspace: %v", err)
		}
		debt, err := lifecycle.CreateCleanupObligation(context.Background(),
			createCleanupObligationRequest(confirmed, "create-ambiguous-cleanup-contract"))
		if err != nil {
			t.Fatalf("create cleanup obligation: %v", err)
		}
		claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
			debt, debt.RetryGeneration, "claim-ambiguous-cleanup-contract",
		))
		if err != nil {
			t.Fatalf("claim cleanup debt: %v", err)
		}
		reconcile := reconcileCleanupDebtRequest(claimed, "reconcile-ambiguous-cleanup-contract")
		retry, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
		if err != nil {
			t.Fatalf("record ambiguous cleanup result: %v", err)
		}
		if retry.State != taskworkspace.CleanupDebtRetryScheduled ||
			retry.LastFailureCategory != taskworkspace.CleanupFailureAmbiguous ||
			retry.SafeFailureEvidenceRoot == "" || retry.DebtID != debt.DebtID {
			t.Fatalf("ambiguous cleanup retry = %#v", retry)
		}
		replayed, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
		if err != nil || !reflect.DeepEqual(replayed, retry) {
			t.Fatal("ambiguous cleanup exact replay changed evidence or DebtID")
		}

		recoverCleanup()
		now = retry.NextRetryAt
		reclaimedClaim, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
			retry, retry.RetryGeneration, "claim-ambiguous-cleanup-retry-contract",
		))
		if err != nil {
			t.Fatalf("reclaim retry generation: %v", err)
		}
		resolved, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcileCleanupDebtRequest(
			reclaimedClaim, "reconcile-ambiguous-cleanup-retry-contract",
		))
		if err != nil || resolved.State != taskworkspace.CleanupDebtResolved ||
			resolved.Resolution != taskworkspace.CleanupAlreadyAbsent || resolved.DebtID != debt.DebtID {
			t.Fatalf("ambiguous cleanup retry resolution = %#v, err = %v", resolved, err)
		}
	})
}

func runLifecycleRetentionRaceContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("re-reference makes queued exact-generation cleanup stale", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.CheckpointRetentionPolicy = taskworkspace.CheckpointRetentionPolicy{
			ID: "contract-race-retention", ReclamationGrace: 10,
		}
		config.CheckpointReclamation = &checkpointReclamationMechanics{present: true}
		config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
			ID: "expiry-policy-1", MaterializationLifetime: 1, RuntimeViewLifetime: 100,
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-retention-race", "materialize-retention-race-base",
			"open-retention-race-1",
		)
		firstManifest := declaredStateManifest("content-1")
		older, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, firstManifest, acceptedValidationEvidence(confirmed, view, firstManifest),
			"commit-retention-race-1",
		))
		if err != nil {
			t.Fatalf("commit older Checkpoint: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-retention-race-current-1",
		))
		if err != nil {
			t.Fatalf("confirm first committed state: %v", err)
		}
		physical, err := lifecycle.Materialize(context.Background(), materializeRequest(
			"policy-domain-1", "task-1", current, "materialize-retention-race-1",
		))
		if err != nil {
			t.Fatalf("materialize older Checkpoint: %v", err)
		}
		materializedAuthority := current
		secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", current, physical,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-retention-race-2",
		))
		if err != nil {
			t.Fatalf("open second Runtime View: %v", err)
		}
		secondManifest := declaredStateManifest("content-2")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			current, secondView, secondManifest, acceptedValidationEvidence(current, secondView, secondManifest),
			"commit-retention-race-2",
		)); err != nil {
			t.Fatalf("commit newer Checkpoint: %v", err)
		}
		now = 1_000
		if _, err := lifecycle.ExpireMaterialization(context.Background(), expireMaterializationRequest(
			materializedAuthority, physical, "expire-retention-race-materialization",
		)); err != nil {
			t.Fatalf("expire superseded physical materialization: %v", err)
		}
		current, err = lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-retention-race-current-2",
		))
		if err != nil {
			t.Fatalf("confirm superseding state: %v", err)
		}
		released := releaseFinalCheckpointAuthority(
			t, lifecycle, current, older.CheckpointID, "release-retention-race",
		)
		queued := reclaimCheckpointRequest(
			current, older.CheckpointID, released.RetentionGeneration, "queued-retention-race",
		)
		attach := taskworkspace.AttachCheckpointRetentionRequest{
			PolicyDomainID: "policy-domain-1", TaskID: "task-1",
			TaskWorkspaceID: current.TaskWorkspaceID, CheckpointID: older.CheckpointID,
			ExpectedRetentionGeneration: released.RetentionGeneration,
			Generation:                  current.Generation, Fence: current.Fence,
			Authority: taskworkspace.CheckpointRetentionAuthority{
				ID: "contract-race-reference", Kind: taskworkspace.CheckpointExplicitReferenceAuthority,
			},
			Operation: taskworkspace.Operation{ID: "attach-retention-race"},
		}
		attach.Operation.RequestDigest = attach.CanonicalRequestDigest()
		if _, err := lifecycle.AttachCheckpointRetention(context.Background(), attach); err != nil {
			t.Fatalf("attach race-winning retention reference: %v", err)
		}
		_, err = lifecycle.ReclaimCheckpoint(context.Background(), queued)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	})
}

func runLifecycleOwnershipAndFenceContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("cross-scope evidence is non-leaking ownership denial", func(t *testing.T) {
		const canary = "/private/path/session-mount-object-locator-credential"
		lifecycle := adapter.new(t)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-cross-contract", "materialize-cross-contract", "open-cross-contract",
		)
		manifest := declaredStateManifest("content-1")
		base := commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-cross-contract",
		)
		foreign, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy"+canary, "task"+canary, "confirm-foreign-contract",
		))
		if err != nil {
			t.Fatalf("confirm foreign workspace: %v", err)
		}
		cross := base
		cross.PolicyDomainID = "policy" + canary
		cross.TaskID = "task" + canary
		cross.TaskWorkspaceID = foreign.TaskWorkspaceID
		cross.Operation.RequestDigest = cross.CanonicalRequestDigest()
		crossResult, crossErr := lifecycle.CommitRuntimeView(context.Background(), cross)
		assertLifecycleErrorCode(t, crossErr, taskworkspace.ErrorOwnershipDenied)

		unknown := base
		unknown.PolicyDomainID = "policy" + canary
		unknown.TaskID = "unknown-task" + canary
		unknown.TaskWorkspaceID = "unknown-workspace" + canary
		unknown.Operation.RequestDigest = unknown.CanonicalRequestDigest()
		unknownResult, unknownErr := lifecycle.CommitRuntimeView(context.Background(), unknown)
		assertLifecycleErrorCode(t, unknownErr, taskworkspace.ErrorOwnershipDenied)
		if crossErr.Error() != unknownErr.Error() || crossResult.RevisionID != "" ||
			unknownResult.RevisionID != "" || strings.Contains(crossErr.Error(), canary) {
			t.Fatal("cross-scope evidence disclosed authority or returned lifecycle identity")
		}
	})

	t.Run("expired lease and recovery generation reject stale writers", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-expired-contract", "materialize-expired-contract", "open-expired-contract",
		)
		now = 300
		manifest := declaredStateManifest("content-1")
		_, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-expired-contract",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)

		fresh := adapter.new(t)
		freshConfirmed, freshView := openRuntimeViewWithLifecycle(
			t, fresh, "task-1", "confirm-recovery-fence-contract",
			"materialize-recovery-fence-contract", "open-recovery-fence-contract",
		)
		if _, err := fresh.FenceRuntimeView(context.Background(), fenceRequest(
			freshConfirmed, freshView, taskworkspace.RuntimeViewRecoveryGenerationAdvanced,
			"advance-recovery-contract",
		)); err != nil {
			t.Fatalf("advance recovery generation: %v", err)
		}
		manifest = declaredStateManifest("content-1")
		_, err = fresh.CommitRuntimeView(context.Background(), commitRequest(
			freshConfirmed, freshView, manifest,
			acceptedValidationEvidence(freshConfirmed, freshView, manifest),
			"commit-pre-recovery-contract",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	})
}

func runLifecycleResponseLossReconciliationContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("response loss reconciles and exact replay activates once", func(t *testing.T) {
		faulted := false
		config := taskworkspaceTestConfig(nil)
		config.FaultHook = func(event taskworkspace.FaultEvent) error {
			if !faulted && event.OperationID == "commit-response-loss-contract" &&
				event.Point == taskworkspace.FaultBeforeResponse {
				faulted = true
				return errors.New("simulated response loss")
			}
			return nil
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-response-loss-contract",
			"materialize-response-loss-contract", "open-response-loss-contract",
		)
		manifest := declaredStateManifest("content-1")
		request := commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-response-loss-contract",
		)
		result, err := lifecycle.CommitRuntimeView(context.Background(), request)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
		if !faulted || result.RevisionID != "" || result.CheckpointID != "" {
			t.Fatal("response loss exposed an unacknowledged terminal result")
		}
		inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
			PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
			OperationID: request.Operation.ID,
		})
		if err != nil || inspection.Disposition != taskworkspace.OperationTerminal &&
			inspection.Disposition != taskworkspace.OperationReconciliationRequired {
			t.Fatalf("response-loss inspection = %#v, err = %v", inspection, err)
		}
		reconciled, err := lifecycle.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
			PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
			OperationID: request.Operation.ID,
		})
		if err != nil || reconciled.Disposition != taskworkspace.OperationTerminal ||
			reconciled.CommitRuntimeView == nil {
			t.Fatalf("response-loss reconciliation = %#v, err = %v", reconciled, err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-response-loss-current-contract",
		))
		if err != nil {
			t.Fatalf("confirm response-loss current workspace: %v", err)
		}
		materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
			"policy-domain-1", "task-1", current, "materialize-response-loss-reconciled-contract",
		))
		if err != nil || materialized.CheckpointID != reconciled.CommitRuntimeView.CheckpointID ||
			materialized.RevisionID != reconciled.CommitRuntimeView.RevisionID {
			t.Fatalf("materialize immediately after commit reconciliation = %#v, err = %v", materialized, err)
		}
		replayed, err := lifecycle.CommitRuntimeView(context.Background(), request)
		if err != nil || !reflect.DeepEqual(replayed, *reconciled.CommitRuntimeView) ||
			replayed.RevisionID == "" || replayed.CheckpointID == "" {
			t.Fatalf("response-loss replay = %#v, err = %v", replayed, err)
		}
	})
}

func runLifecycleMandatoryAuditContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("mandatory cleanup audit fails closed", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		administrator := platformAdministratorAuthority(now)
		audit := &cleanupAuditDouble{failure: errors.New("audit unavailable")}
		externalAudit := &auditDeliveryDouble{}
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.CleanupAudit = audit
		config.AuditDelivery = externalAudit
		config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
		config.CurrentPlatformAdministratorAuthority = func(
			id taskworkspace.PlatformAdministratorID,
		) (taskworkspace.PlatformAdministratorAuthority, bool) {
			return administrator, id == administrator.ID
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-audit-contract"),
		)
		if err != nil {
			t.Fatalf("confirm workspace for mandatory audit: %v", err)
		}
		debt, err := lifecycle.CreateCleanupObligation(
			context.Background(), createCleanupObligationRequest(confirmed, "create-audit-contract"),
		)
		if err != nil {
			t.Fatalf("create Cleanup Debt for mandatory audit: %v", err)
		}
		_, err = lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
			debt, administrator, "resolve-audit-contract-failed",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
		stillOpen, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
			PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, DebtID: debt.DebtID,
		})
		if err != nil || stillOpen.State != taskworkspace.CleanupDebtOpen || stillOpen.Resolution != "" {
			t.Fatalf("mandatory audit failure changed Cleanup Debt: %#v, err = %v", stillOpen, err)
		}

		audit.failure = nil
		resolved, err := lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
			debt, administrator, "resolve-audit-contract",
		))
		if err != nil || resolved.State != taskworkspace.CleanupDebtResolved ||
			resolved.Resolution != taskworkspace.CleanupAcceptedException ||
			resolved.ResolutionAuditEvidenceRoot == "" || audit.calls != 2 || externalAudit.calls != 1 {
			t.Fatalf("audited Cleanup Debt resolution = %#v, err = %v", resolved, err)
		}
		backlog, err := lifecycle.RebuildAuditDelivery(
			context.Background(), taskworkspace.AuditDeliveryRebuildRequest{},
		)
		if err != nil || !backlog.Pending.Known || backlog.Pending.Value != 0 ||
			!backlog.Delivered.Known || backlog.Delivered.Value != 1 || len(backlog.Evidence) != 1 {
			t.Fatalf("rebuilt audit delivery backlog = %#v, err = %v", backlog, err)
		}
	})
}

func runLifecycleReconstructionContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("reconstruct exact Artifact Version without changing authoritative history", func(t *testing.T) {
		var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
		reconstruction := &reconstructionInputDouble{}
		config := taskworkspaceTestConfig(nil)
		config.RecoveryAuthorityID = "recovery-authority-1"
		config.ReconstructionInput = reconstruction
		config.CurrentRecoveryIntent = func(
			id taskworkspace.RecoveryIntentID,
		) (taskworkspace.AuthorizedRecoveryIntent, bool) {
			return recoveryIntent, recoveryIntent.ID == id
		}
		lifecycle := adapter.newConfigured(t, config)
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
}

func runLifecycleIdentityCommitAndTerminalContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("identity commit discard idempotency and fencing", func(t *testing.T) {
		lifecycle := adapter.new(t)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		reconfirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-again"),
		)
		if err != nil || reconfirmed.TaskWorkspaceID != confirmed.TaskWorkspaceID {
			t.Fatal("Task Workspace identity was not stable")
		}
		firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-view-1",
		))
		if err != nil {
			t.Fatalf("open first Runtime View: %v", err)
		}
		secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-contract-view-2",
		))
		if err != nil || secondView.RuntimeViewID == firstView.RuntimeViewID {
			t.Fatal("mutating Runtime Views were not isolated")
		}
		manifest := declaredStateManifest("content-1")
		validation := acceptedValidationEvidence(confirmed, firstView, manifest)
		commit := commitRequest(confirmed, firstView, manifest, validation, "commit-contract-1")
		first, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil {
			t.Fatalf("commit first Runtime View: %v", err)
		}
		replayed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil || !reflect.DeepEqual(replayed, first) {
			t.Fatal("exact commit replay changed the terminal decision")
		}
		conflictManifest := declaredStateManifest("content-2")
		conflict := commitRequest(
			confirmed, firstView, conflictManifest,
			acceptedValidationEvidence(confirmed, firstView, conflictManifest),
			string(commit.Operation.ID),
		)
		_, err = lifecycle.CommitRuntimeView(context.Background(), conflict)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)

		secondManifest := declaredStateManifest("content-2")
		secondValidation := acceptedValidationEvidence(confirmed, secondView, secondManifest)
		_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, secondView, secondManifest, secondValidation, "commit-contract-stale",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)

		fresh := adapter.new(t)
		freshConfirmed, freshMaterialized := materializedTaskUsing(t, fresh)
		view, err := fresh.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", freshConfirmed, freshMaterialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-discard-contract",
		))
		if err != nil {
			t.Fatalf("open discard Runtime View: %v", err)
		}
		discard := discardRequest(freshConfirmed, view, taskworkspace.RuntimeViewValidationRejected, "discard-contract")
		firstDiscard, err := fresh.DiscardRuntimeView(context.Background(), discard)
		if err != nil {
			t.Fatalf("discard Runtime View: %v", err)
		}
		replayedDiscard, err := fresh.DiscardRuntimeView(context.Background(), discard)
		if err != nil || !reflect.DeepEqual(replayedDiscard, firstDiscard) {
			t.Fatal("exact discard replay changed the terminal decision")
		}
		manifest = declaredStateManifest("content-1")
		_, err = fresh.CommitRuntimeView(context.Background(), commitRequest(
			freshConfirmed, view, manifest, acceptedValidationEvidence(freshConfirmed, view, manifest),
			"commit-after-discard-contract",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorViewTerminalConflict)
	})
}

func runLifecycleIntegrityContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	for _, test := range []struct {
		name string
		edit func(*taskworkspace.DeclaredStateManifest)
	}{
		{"parent traversal", func(manifest *taskworkspace.DeclaredStateManifest) {
			manifest.Members[0].LogicalMember = "../escape"
		}},
		{"symbolic link", func(manifest *taskworkspace.DeclaredStateManifest) {
			manifest.Members[0].Type = taskworkspace.StateMemberSymbolicLink
		}},
		{"unsafe member type", func(manifest *taskworkspace.DeclaredStateManifest) {
			manifest.Members[0].Type = "device"
		}},
	} {
		test := test
		t.Run("integrity "+test.name, func(t *testing.T) {
			lifecycle := adapter.new(t)
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-integrity", "materialize-integrity", "open-integrity",
			)
			manifest := declaredStateManifest("content-1")
			test.edit(&manifest)
			manifest.Digest = manifest.CanonicalDigest()
			result, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
				confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
				"commit-integrity-"+strings.ReplaceAll(test.name, " ", "-"),
			))
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
			if result.RevisionID != "" || result.CheckpointID != "" {
				t.Fatal("unsafe member returned authoritative identities")
			}
		})
	}
}

func runLifecycleExpiryAndRestoreContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("expire and restore exact Checkpoint", func(t *testing.T) {
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
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-restore", "materialize-restore-base", "open-restore",
		)
		manifest := declaredStateManifest("content-1")
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-restore",
		))
		if err != nil {
			t.Fatalf("commit restore Checkpoint: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-restore-current"),
		)
		if err != nil {
			t.Fatalf("confirm current state: %v", err)
		}
		materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
			"policy-domain-1", "task-1", current, "materialize-restore-current",
		))
		if err != nil {
			t.Fatalf("materialize committed state: %v", err)
		}
		now = 110
		if _, err := lifecycle.ExpireMaterialization(
			context.Background(), expireMaterializationRequest(current, materialized, "expire-contract"),
		); err != nil {
			t.Fatalf("expire materialization: %v", err)
		}
		recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-contract")
		restore := taskworkspace.RestoreTaskWorkspaceRequest{
			Intent: recoveryIntent, Operation: taskworkspace.Operation{ID: "restore-contract"},
		}
		restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
		restored, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
		if err != nil {
			t.Fatalf("restore exact Checkpoint: %v", err)
		}
		if restored.TaskWorkspaceID != current.TaskWorkspaceID || restored.RevisionID != committed.RevisionID ||
			restored.CheckpointID != committed.CheckpointID || restored.Generation != current.Generation+1 ||
			restored.Fence != current.Fence+1 || restored.MaterializationID == materialized.MaterializationID {
			t.Fatal("restore changed history, reused physical identity, or omitted the new recovery fence")
		}
	})
}

func runLifecycleRetentionAndReclaimContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("retention grace and exact-generation reclaim", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 1, RuntimeViewLifetime: 100}
		config.CheckpointRetentionPolicy = taskworkspace.CheckpointRetentionPolicy{
			ID: "contract-retention", ReclamationGrace: 10,
		}
		config.CheckpointReclamation = &checkpointReclamationMechanics{present: true}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-reclaim", "materialize-reclaim-base", "open-reclaim-1",
		)
		firstManifest := declaredStateManifest("content-1")
		older, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, firstManifest, acceptedValidationEvidence(confirmed, view, firstManifest),
			"commit-reclaim-1",
		))
		if err != nil {
			t.Fatalf("commit older Checkpoint: %v", err)
		}
		current, _ := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-reclaim-current-1"),
		)
		physical, err := lifecycle.Materialize(context.Background(), materializeRequest(
			"policy-domain-1", "task-1", current, "materialize-reclaim-1",
		))
		if err != nil {
			t.Fatalf("materialize older Checkpoint: %v", err)
		}
		secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", current, physical,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-reclaim-2",
		))
		if err != nil {
			t.Fatalf("open second Runtime View: %v", err)
		}
		secondManifest := declaredStateManifest("content-2")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			current, secondView, secondManifest, acceptedValidationEvidence(current, secondView, secondManifest),
			"commit-reclaim-2",
		)); err != nil {
			t.Fatalf("commit newer Checkpoint: %v", err)
		}
		now = 1_000
		if _, err := lifecycle.ExpireMaterialization(
			context.Background(), expireMaterializationRequest(current, physical, "expire-reclaim-materialization"),
		); err != nil {
			t.Fatalf("expire old physical materialization: %v", err)
		}
		current, _ = lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-reclaim-current-2"),
		)
		released := releaseFinalCheckpointAuthority(
			t, lifecycle, current, older.CheckpointID, "release-reclaim-contract",
		)
		now = released.EligibleAt
		reclaim := reclaimCheckpointRequest(
			current, older.CheckpointID, released.RetentionGeneration, "reclaim-contract",
		)
		first, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaim)
		if err != nil {
			t.Fatalf("reclaim exact Checkpoint generation: %v", err)
		}
		if first.Outcome != taskworkspace.CheckpointReclaimed && first.Outcome != taskworkspace.CheckpointAlreadyAbsent {
			t.Fatalf("reclaim outcome = %q", first.Outcome)
		}
		replayed, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaim)
		if err != nil || !reflect.DeepEqual(replayed, first) {
			t.Fatal("repeated exact-generation reclaim was not idempotent")
		}
	})
}

func runLifecycleCleanupDebtContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("Cleanup Debt exact replay and resolution", func(t *testing.T) {
		config := taskworkspaceTestConfig(nil)
		config.Cleanup = &exactGenerationCleanupDouble{
			inspectionDisposition: taskworkspace.CleanupInspectionAlreadyAbsent,
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-cleanup-contract"),
		)
		if err != nil {
			t.Fatalf("confirm workspace for cleanup: %v", err)
		}
		debt, err := lifecycle.CreateCleanupObligation(
			context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-contract"),
		)
		if err != nil {
			t.Fatalf("create Cleanup Debt: %v", err)
		}
		createReplay, err := lifecycle.CreateCleanupObligation(
			context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-contract"),
		)
		if err != nil || createReplay.DebtID != debt.DebtID {
			t.Fatalf("Cleanup Debt creation replay = %#v, err = %v", createReplay, err)
		}
		claimed, err := lifecycle.ClaimCleanupDebt(
			context.Background(), claimCleanupDebtRequest(debt, debt.RetryGeneration, "claim-cleanup-contract"),
		)
		if err != nil {
			t.Fatalf("claim Cleanup Debt: %v", err)
		}
		reconcile := reconcileCleanupDebtRequest(claimed, "reconcile-cleanup-contract")
		resolved, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
		if err != nil {
			t.Fatalf("reconcile Cleanup Debt: %v", err)
		}
		if resolved.State != taskworkspace.CleanupDebtResolved ||
			(resolved.Resolution != taskworkspace.CleanupAlreadyAbsent && resolved.Resolution != taskworkspace.CleanupReclaimed) {
			t.Fatalf("Cleanup Debt resolution = %#v", resolved)
		}
		replayed, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
		if err != nil || !reflect.DeepEqual(replayed, resolved) {
			t.Fatal("Cleanup Debt reconciliation exact replay changed its result")
		}
	})
}

func runLifecycleProjectionAndNonLeakageContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("projection outage and non-leakage", func(t *testing.T) {
		projection := &failingLifecycleProjection{err: errors.New("path-do-not-disclose/session-do-not-disclose")}
		config := taskworkspaceTestConfig(nil)
		config.Projection = projection
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-projection", "materialize-projection", "open-projection",
		)
		manifest := declaredStateManifest("content-1")
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-projection-contract",
		))
		if err != nil {
			t.Fatalf("projection outage rolled back committed lifecycle fact: %v", err)
		}
		if len(projection.envelopes) == 0 {
			t.Fatal("committed lifecycle fact was not offered to the projection")
		}
		encoded, err := json.Marshal(struct {
			Result      taskworkspace.CommitRuntimeViewResult
			Projections []taskworkspace.ProjectionEnvelope
		}{committed, projection.envelopes})
		if err != nil {
			t.Fatalf("encode external evidence: %v", err)
		}
		for _, forbidden := range []string{"path-do-not-disclose", "session-do-not-disclose", "object-key", "bucket", "mount"} {
			if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
				t.Fatalf("external lifecycle evidence leaked %q", forbidden)
			}
		}
	})
}

type failingLifecycleProjection struct {
	err       error
	envelopes []taskworkspace.ProjectionEnvelope
}

type panickingLifecycleProjection struct {
	value any
}

type temporaryProjectionError struct {
	message string
}

type panickingTelemetryDouble struct {
	value   any
	panics  bool
	signals []taskworkspace.OperationalSignals
}

func (e temporaryProjectionError) Error() string { return e.message }

func (temporaryProjectionError) Temporary() bool { return true }

func (d *panickingTelemetryDouble) Emit(
	_ context.Context,
	signals taskworkspace.OperationalSignals,
) error {
	d.signals = append(d.signals, signals)
	if d.panics {
		panic(d.value)
	}
	return nil
}

func (p *panickingLifecycleProjection) Project(context.Context, taskworkspace.ProjectionEnvelope) error {
	panic(p.value)
}

func (p *panickingLifecycleProjection) Rebuild(
	context.Context,
	taskworkspace.ProjectionSchemaRevision,
	[]taskworkspace.ProjectionEnvelope,
) error {
	panic(p.value)
}

func (p *failingLifecycleProjection) Project(_ context.Context, envelope taskworkspace.ProjectionEnvelope) error {
	p.envelopes = append(p.envelopes, envelope)
	return p.err
}

func (p *failingLifecycleProjection) Rebuild(
	_ context.Context,
	_ taskworkspace.ProjectionSchemaRevision,
	envelopes []taskworkspace.ProjectionEnvelope,
) error {
	p.envelopes = append(p.envelopes, envelopes...)
	return p.err
}
