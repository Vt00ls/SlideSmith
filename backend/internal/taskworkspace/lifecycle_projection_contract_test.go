package taskworkspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestAuthoritativeCommitSurvivesTelemetryOutage(t *testing.T) {
	telemetry := &telemetryDouble{failure: errors.New("collector unavailable")}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	config.ProjectionSchemaRevision = taskworkspace.ProjectionSchemaV1
	lifecycle := taskworkspace.NewInMemory(config)

	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-telemetry-outage-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-telemetry-outage-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)

	committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, validation, "commit-telemetry-outage-1",
	))
	if err != nil {
		t.Fatalf("telemetry outage rolled back commit: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-telemetry-outage-1",
	))
	if err != nil {
		t.Fatalf("confirm committed Task Workspace: %v", err)
	}
	if committed.RevisionID == "" || committed.CheckpointID == "" ||
		current.CurrentRevisionID != committed.RevisionID || current.CurrentCheckpointID != committed.CheckpointID {
		t.Fatalf("authoritative commit was not retained: committed=%#v current=%#v", committed, current)
	}
	if telemetry.calls == 0 {
		t.Fatal("telemetry outage was not exercised")
	}
}

func TestExternalAuditDeliveryFailureKeepsResolutionCommittedAndRebuildsBacklog(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	administrator := platformAdministratorAuthority(now)
	mandatoryAudit := &capturingCleanupAuditDouble{}
	externalErrorCanary := "raw external error /private/session s3://secret locator credential=do-not-disclose"
	failingDelivery := &auditDeliveryDouble{failure: errors.New(externalErrorCanary)}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Persistence = persistence
	config.Cleanup = &exactGenerationCleanupDouble{}
	config.CleanupAudit = mandatoryAudit
	config.AuditDelivery = failingDelivery
	config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
	config.CurrentPlatformAdministratorAuthority = func(
		id taskworkspace.PlatformAdministratorID,
	) (taskworkspace.PlatformAdministratorAuthority, bool) {
		return administrator, id == administrator.ID
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-audit-delivery-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	created, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-audit-delivery-debt-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}

	auditCanary := taskworkspace.OperationID("resolve-/private/session-s3-secret-locator-credential-canary")
	accepted, err := lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
		created, administrator, auditCanary,
	))
	if err != nil {
		t.Fatalf("external audit delivery failure rolled back resolution: %v", err)
	}
	if accepted.State != taskworkspace.CleanupDebtResolved ||
		accepted.Resolution != taskworkspace.CleanupAcceptedException || failingDelivery.calls != 1 {
		t.Fatalf("accepted resolution or delivery attempt missing: debt=%#v calls=%d", accepted, failingDelivery.calls)
	}
	authoritativeAudit, err := json.Marshal(struct {
		Intents []taskworkspace.CleanupAuditIntent
		Facts   []taskworkspace.CleanupAuditEvidence
	}{mandatoryAudit.intents, mandatoryAudit.facts})
	if err != nil {
		t.Fatalf("marshal authoritative audit facts: %v", err)
	}
	for _, forbidden := range []string{string(auditCanary), "/private", "session", "locator", "credential"} {
		if strings.Contains(string(authoritativeAudit), forbidden) {
			t.Fatalf("authoritative audit leaked forbidden value %q: %s", forbidden, authoritativeAudit)
		}
	}
	deliveredFacts, err := json.Marshal(failingDelivery.facts)
	if err != nil {
		t.Fatalf("marshal external audit delivery facts: %v", err)
	}
	for _, forbidden := range []string{externalErrorCanary, string(auditCanary), "/private", "session", "locator", "credential"} {
		if strings.Contains(string(deliveredFacts), forbidden) {
			t.Fatalf("external audit delivery leaked forbidden value %q: %s", forbidden, deliveredFacts)
		}
	}

	successfulDelivery := &auditDeliveryDouble{}
	restartConfig := config
	restartConfig.AuditDelivery = successfulDelivery
	restarted := taskworkspace.NewInMemory(restartConfig)
	backlog, err := restarted.RebuildAuditDelivery(context.Background(), taskworkspace.AuditDeliveryRebuildRequest{})
	if err != nil {
		t.Fatalf("rebuild external audit delivery backlog: %v", err)
	}
	if backlog.Pending.Known == false || backlog.Pending.Value != 0 ||
		backlog.Delivered.Known == false || backlog.Delivered.Value == 0 ||
		!backlog.Quarantined.Known || backlog.Quarantined.Value != 0 ||
		!backlog.SourceWatermark.Known || successfulDelivery.calls != 1 ||
		len(backlog.Evidence) != 1 || backlog.Evidence[0].AttemptCount != 2 ||
		backlog.Evidence[0].AttemptGeneration != 2 ||
		backlog.Evidence[0].FirstAttemptAt == 0 || backlog.Evidence[0].LastAttemptAt == 0 ||
		backlog.Evidence[0].LastResult != taskworkspace.ProjectionResultCommitted ||
		backlog.Evidence[0].SafeError != "" {
		t.Fatalf("rebuilt audit delivery backlog = %#v, delivery calls = %d", backlog, successfulDelivery.calls)
	}
}

func TestAuditDeliveryBacklogRebuildsAfterInterruptedInFlightAttempt(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	administrator := platformAdministratorAuthority(now)
	blockedDelivery := newBlockingAuditDeliveryDouble()
	blockedDelivery.failure = errors.New("interrupted attempt failed after restart delivery succeeded")
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Persistence = persistence
	config.Cleanup = &exactGenerationCleanupDouble{}
	config.CleanupAudit = &capturingCleanupAuditDouble{}
	config.AuditDelivery = blockedDelivery
	config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
	config.CurrentPlatformAdministratorAuthority = func(
		id taskworkspace.PlatformAdministratorID,
	) (taskworkspace.PlatformAdministratorAuthority, bool) {
		return administrator, id == administrator.ID
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-interrupted-audit-delivery-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-interrupted-audit-delivery-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	resolveDone := make(chan error, 1)
	go func() {
		_, resolveErr := lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
			debt, administrator, "resolve-interrupted-audit-delivery-1",
		))
		resolveDone <- resolveErr
	}()
	<-blockedDelivery.entered

	successfulDelivery := &auditDeliveryDouble{}
	restartConfig := config
	restartConfig.AuditDelivery = successfulDelivery
	restarted := taskworkspace.NewInMemory(restartConfig)
	backlog, err := restarted.RebuildAuditDelivery(context.Background(), taskworkspace.AuditDeliveryRebuildRequest{})
	if err != nil {
		t.Fatalf("rebuild interrupted audit delivery: %v", err)
	}
	if successfulDelivery.calls != 1 || backlog.Pending.Value != 0 || backlog.Delivered.Value == 0 {
		t.Fatalf("interrupted delivery was not rebuilt: backlog=%#v calls=%d", backlog, successfulDelivery.calls)
	}
	close(blockedDelivery.release)
	if err := <-resolveDone; err != nil {
		t.Fatalf("resolved authoritative decision after releasing old delivery: %v", err)
	}
	afterLateFailure, err := restarted.RebuildAuditDelivery(
		context.Background(), taskworkspace.AuditDeliveryRebuildRequest{},
	)
	if err != nil {
		t.Fatalf("inspect backlog after late interrupted failure: %v", err)
	}
	if afterLateFailure.Pending.Value != 0 || afterLateFailure.Delivered.Value == 0 ||
		successfulDelivery.calls != 1 {
		t.Fatalf("late interrupted failure regressed successful delivery: backlog=%#v calls=%d",
			afterLateFailure, successfulDelivery.calls)
	}
}

func TestProjectionRebuildUsesAuthoritativeFactsAfterTelemetryOutage(t *testing.T) {
	telemetry := &telemetryDouble{failure: errors.New("collector unavailable")}
	projector := taskworkspace.NewDeterministicProjection(telemetry)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = projector
	config.ProjectionSchemaRevision = taskworkspace.ProjectionSchemaV1
	lifecycle := taskworkspace.NewInMemory(config)

	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-projection-rebuild-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-projection-rebuild-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, validation, "commit-projection-rebuild-1",
	)); err != nil {
		t.Fatalf("commit during telemetry outage: %v", err)
	}

	telemetry.failure = nil
	beforeRebuild := telemetry.calls
	rebuilt, err := lifecycle.RebuildProjections(context.Background(), taskworkspace.ProjectionRebuildRequest{
		SchemaRevision: taskworkspace.ProjectionSchemaV1,
	})
	if err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	if !rebuilt.Projected.Known || rebuilt.Projected.Value == 0 ||
		!rebuilt.SourceWatermark.Known || rebuilt.SourceWatermark.Value == 0 ||
		telemetry.calls <= beforeRebuild {
		t.Fatalf("projection rebuild did not replay authoritative facts: result=%#v calls=%d", rebuilt, telemetry.calls)
	}
	afterRebuild := telemetry.calls
	firstRebuildIdentities := projectionSignalIdentities(telemetry.signals[beforeRebuild:afterRebuild])
	if _, err := lifecycle.RebuildProjections(context.Background(), taskworkspace.ProjectionRebuildRequest{
		SchemaRevision: taskworkspace.ProjectionSchemaV1,
	}); err != nil {
		t.Fatalf("repeat projection rebuild: %v", err)
	}
	secondRebuildIdentities := projectionSignalIdentities(telemetry.signals[afterRebuild:])
	if telemetry.calls <= afterRebuild || !reflect.DeepEqual(firstRebuildIdentities, secondRebuildIdentities) {
		t.Fatalf("repeat rebuild did not replay the same deduplicable fact identities: first=%#v second=%#v",
			firstRebuildIdentities, secondRebuildIdentities)
	}
	cursor, err := projector.Cursor(taskworkspace.ProjectionSchemaV1)
	if err != nil {
		t.Fatalf("inspect projection cursor: %v", err)
	}
	if cursor.SourcePartition != taskworkspace.ProjectionSourceC04 ||
		!cursor.SourceWatermark.Known || cursor.SourceWatermark.Value == 0 ||
		cursor.FirstFactID == "" || cursor.LastFactID == "" ||
		!cursor.RetryPending.Known || cursor.RetryPending.Value != 0 {
		t.Fatalf("projection cursor omitted rebuild/idempotency evidence: %#v", cursor)
	}
}

func TestDeterministicProjectionHandlesDuplicateOutOfOrderAndSchemaRevision(t *testing.T) {
	telemetry := &telemetryDouble{}
	projector := taskworkspace.NewDeterministicProjection(telemetry)
	newer := taskworkspace.ProjectionEnvelope{
		FactID: "fact-1", FactRevision: 2, SchemaRevision: taskworkspace.ProjectionSchemaV1,
		Kind: taskworkspace.ProjectionFactLifecycle, Operation: taskworkspace.ProjectionOperationCommit,
		Result: taskworkspace.ProjectionResultCommitted, LifecycleState: taskworkspace.ProjectionStateCommitted,
		ResourceClass: taskworkspace.ProjectionResourceTaskWorkspace,
		AdapterClass:  taskworkspace.ProjectionAdapterDeterministic,
		RecordedAt:    100,
	}
	if err := projector.Project(context.Background(), newer); err != nil {
		t.Fatalf("project newer fact: %v", err)
	}
	older := newer
	older.FactRevision = 1
	for name, fact := range map[string]taskworkspace.ProjectionEnvelope{
		"out_of_order": older,
		"duplicate":    newer,
	} {
		if err := projector.Project(context.Background(), fact); err != nil {
			t.Fatalf("%s projection: %v", name, err)
		}
	}
	if telemetry.calls != 1 {
		t.Fatalf("duplicate/out-of-order facts emitted %d telemetry deliveries, want 1", telemetry.calls)
	}

	conflict := newer
	conflict.Result = taskworkspace.ProjectionResultRejected
	conflict.LifecycleState = taskworkspace.ProjectionStateUnknown
	assertLifecycleErrorCode(t, projector.Project(context.Background(), conflict), taskworkspace.ErrorIntegrityConflict)
	cursor, err := projector.Cursor(taskworkspace.ProjectionSchemaV1)
	if err != nil {
		t.Fatalf("inspect conflict cursor: %v", err)
	}
	if !cursor.SourceWatermark.Known || cursor.SourceWatermark.Value != uint64(newer.FactRevision) ||
		cursor.DuplicateCount != 2 || cursor.SafeError != taskworkspace.SafeErrorIdempotencyConflict {
		t.Fatalf("live cursor omitted watermark, duplicate, or conflict evidence: %#v", cursor)
	}

	newSchema := newer
	newSchema.SchemaRevision = taskworkspace.ProjectionSchemaV1 + 1
	if err := projector.Project(context.Background(), newSchema); err != nil {
		t.Fatalf("project new schema revision: %v", err)
	}
	if telemetry.calls != 2 {
		t.Fatalf("new projection schema emitted %d telemetry deliveries, want 2", telemetry.calls)
	}
	firstSignals := telemetry.signals[0]
	if firstSignals.Identity.FactID != newer.FactID ||
		firstSignals.Identity.FactRevision != newer.FactRevision ||
		firstSignals.Identity.SchemaRevision != newer.SchemaRevision || len(firstSignals.Logs) != 1 ||
		firstSignals.Logs[0].SchemaRevision != taskworkspace.LogSchemaV1 ||
		firstSignals.Logs[0].Severity != taskworkspace.LogSeverityInfo ||
		!firstSignals.Logs[0].Timestamp.Known || firstSignals.Logs[0].Timestamp.Value != newer.RecordedAt {
		t.Fatalf("signals omitted idempotency identity or structured-log schema: %#v", firstSignals)
	}
}

func TestProjectionCursorTracksProcessingOrderRatherThanFactIDOrder(t *testing.T) {
	telemetry := &telemetryDouble{}
	projector := taskworkspace.NewDeterministicProjection(telemetry)
	first := projectionEnvelope("fact-z-first", taskworkspace.ProjectionOperationCommit)
	first.RecordedAt = 100
	last := projectionEnvelope("fact-a-last", taskworkspace.ProjectionOperationRestore)
	last.RecordedAt = 101
	if err := projector.Project(context.Background(), first); err != nil {
		t.Fatalf("project first fact: %v", err)
	}
	if err := projector.Project(context.Background(), last); err != nil {
		t.Fatalf("project last fact: %v", err)
	}
	cursor, err := projector.Cursor(taskworkspace.ProjectionSchemaV1)
	if err != nil {
		t.Fatalf("inspect projection cursor: %v", err)
	}
	if cursor.FirstFactID != first.FactID || cursor.LastFactID != last.FactID ||
		!cursor.SourceWatermark.Known || cursor.SourceWatermark.Value != 2 {
		t.Fatalf("projection cursor used FactID ordering or omitted live watermark: %#v", cursor)
	}
}

func TestDeterministicProjectionDoesNotSerializeUnrelatedTelemetryDelivery(t *testing.T) {
	telemetry := newBlockingTelemetryDouble()
	projector := taskworkspace.NewDeterministicProjection(telemetry)
	first := projectionEnvelope("fact-blocked-1", taskworkspace.ProjectionOperationCommit)
	second := projectionEnvelope("fact-independent-2", taskworkspace.ProjectionOperationRestore)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- projector.Project(context.Background(), first)
	}()
	<-telemetry.firstEntered

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- projector.Project(context.Background(), second)
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("project unrelated fact: %v", err)
		}
	case <-time.After(time.Second):
		close(telemetry.releaseFirst)
		<-firstDone
		t.Fatal("unrelated projection waited for a blocked telemetry delivery")
	}
	close(telemetry.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("project blocked fact: %v", err)
	}
}

func TestProjectionRebuildAttemptCannotBeRegressedByLateTelemetryFailure(t *testing.T) {
	telemetry := newStaleFailureTelemetryDouble()
	projector := taskworkspace.NewDeterministicProjection(telemetry)
	fact := projectionEnvelope("fact-interrupted-projection", taskworkspace.ProjectionOperationCommit)
	fact.RecordedAt = 100
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- projector.Project(context.Background(), fact)
	}()
	<-telemetry.firstEntered

	if err := projector.Rebuild(
		context.Background(), taskworkspace.ProjectionSchemaV1, []taskworkspace.ProjectionEnvelope{fact},
	); err != nil {
		t.Fatalf("rebuild interrupted projection: %v", err)
	}
	close(telemetry.releaseFirst)
	assertLifecycleErrorCode(t, <-firstDone, taskworkspace.ErrorReconciliationRequired)
	if err := projector.Project(context.Background(), fact); err != nil {
		t.Fatalf("project exact duplicate after successful rebuild: %v", err)
	}
	if telemetry.callCount() != 2 {
		t.Fatalf("late telemetry failure regressed successful rebuild; emits=%d", telemetry.callCount())
	}
	cursor, err := projector.Cursor(taskworkspace.ProjectionSchemaV1)
	if err != nil || cursor.RetryPending.Value != 0 || cursor.SafeError != "" {
		t.Fatalf("late telemetry failure regressed projection cursor: cursor=%#v err=%v", cursor, err)
	}
}

func TestTypedSafeErrorTaxonomyDistinguishesLifecycleFailures(t *testing.T) {
	tests := []struct {
		code taskworkspace.ErrorCode
		want taskworkspace.SafeErrorCategory
	}{
		{taskworkspace.ErrorOwnershipDenied, taskworkspace.SafeErrorAuthorizationDenied},
		{taskworkspace.ErrorInvalidIntent, taskworkspace.SafeErrorInvalidIntent},
		{taskworkspace.ErrorIntegrityConflict, taskworkspace.SafeErrorIdempotencyConflict},
		{taskworkspace.ErrorStaleAuthority, taskworkspace.SafeErrorStaleRevisionGenerationFence},
		{taskworkspace.ErrorViewTerminalConflict, taskworkspace.SafeErrorTerminalConflict},
		{taskworkspace.ErrorIntegrityFailure, taskworkspace.SafeErrorIntegrityUnavailableContent},
		{taskworkspace.ErrorDurabilityUnverified, taskworkspace.SafeErrorDurabilityUnverified},
		{taskworkspace.ErrorResourceExhausted, taskworkspace.SafeErrorResourceExhausted},
		{taskworkspace.ErrorRetryableUnavailable, taskworkspace.SafeErrorRetryableUnavailable},
		{taskworkspace.ErrorReconciliationRequired, taskworkspace.SafeErrorReconciliationRequired},
		{taskworkspace.ErrorCleanupDebt, taskworkspace.SafeErrorCleanupDebt},
	}
	for _, test := range tests {
		t.Run(string(test.want), func(t *testing.T) {
			failure := &taskworkspace.Error{Code: test.code}
			if got := failure.SafeCategory(); got != test.want {
				t.Fatalf("safe category = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAdministratorDiagnosticsRequireAuthorizationAndReportStaleSourceAsUnknown(t *testing.T) {
	now := taskworkspace.Instant(100)
	administrator := platformAdministratorAuthority(now)
	diagnostics := &diagnosticsDouble{status: taskworkspace.DiagnosticsSourceStatus{
		State:     taskworkspace.DiagnosticsSourceStale,
		Watermark: taskworkspace.SourceWatermark{Known: true, Value: 7},
	}}
	mandatoryAudit := &capturingCleanupAuditDouble{}
	externalAudit := &auditDeliveryDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
	config.CurrentPlatformAdministratorAuthority = func(
		id taskworkspace.PlatformAdministratorID,
	) (taskworkspace.PlatformAdministratorAuthority, bool) {
		return administrator, id == administrator.ID
	}
	config.Diagnostics = diagnostics
	config.CleanupAudit = mandatoryAudit
	config.AuditDelivery = externalAudit
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-diagnostics-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-diagnostics-debt-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	request := administratorDiagnosticsRequest(debt, administrator, "query-diagnostics-1")

	stale, err := lifecycle.QueryAdministratorDiagnostics(context.Background(), request)
	if err != nil {
		t.Fatalf("query stale diagnostics: %v", err)
	}
	if stale.SourceState != taskworkspace.DiagnosticsSourceStale ||
		!stale.SourceWatermark.Known || stale.SourceWatermark.Value != 7 ||
		stale.LifecycleState != taskworkspace.DiagnosticLifecycleUnknown ||
		stale.EstimatedBytes.Known || stale.EstimatedInodes.Known || stale.RetryAge.Known ||
		stale.Relationship.RevisionID != "" || stale.Relationship.OperationID != "" {
		t.Fatalf("stale diagnostics fabricated current or zero values: %#v", stale)
	}
	if mandatoryAudit.calls != 1 || len(mandatoryAudit.facts) != 1 {
		t.Fatalf("authorized exact diagnostics did not commit mandatory audit: %#v", mandatoryAudit)
	}
	if mandatoryAudit.intents[0].Action != taskworkspace.CleanupAuditQueryDiagnostics ||
		mandatoryAudit.intents[0].Resolution != "" {
		t.Fatalf("diagnostic audit invented a Cleanup Debt resolution: %#v", mandatoryAudit.intents[0])
	}
	if externalAudit.calls != 1 ||
		externalAudit.facts[0].Result != taskworkspace.AuditDeliveryDiagnosticsAccessed {
		t.Fatalf("diagnostic audit delivery omitted distinct audit result: %#v", externalAudit.facts)
	}

	diagnostics.denied = errors.New("raw policy backend says secret workspace exists")
	replayed, err := lifecycle.QueryAdministratorDiagnostics(context.Background(), request)
	if err != nil || !reflect.DeepEqual(replayed, stale) || mandatoryAudit.calls != 1 || externalAudit.calls != 1 {
		t.Fatalf("exact diagnostic replay did not return committed result: result=%#v err=%v audit=%d",
			replayed, err, mandatoryAudit.calls)
	}
	conflict := request
	conflict.DebtID = "different-debt-same-operation"
	conflict.Operation.RequestDigest = conflict.CanonicalRequestDigest()
	_, err = lifecycle.QueryAdministratorDiagnostics(context.Background(), conflict)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	unauthorizedProbe := conflict
	unauthorizedProbe.AdministratorAuthority = taskworkspace.PlatformAdministratorAuthority{}
	unauthorizedProbe.Operation.RequestDigest = unauthorizedProbe.CanonicalRequestDigest()
	_, err = lifecycle.QueryAdministratorDiagnostics(context.Background(), unauthorizedProbe)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorOwnershipDenied)

	deniedRequest := administratorDiagnosticsRequest(debt, administrator, "query-diagnostics-denied-2")
	_, err = lifecycle.QueryAdministratorDiagnostics(context.Background(), deniedRequest)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorOwnershipDenied)
	if stringsContainsAny(err.Error(), "raw policy backend", "secret workspace") {
		t.Fatalf("diagnostics denial leaked raw adapter error: %v", err)
	}
}

func TestAdministratorDiagnosticsAreContentFreeAndUnavailableIsUnknown(t *testing.T) {
	now := taskworkspace.Instant(100)
	administrator := platformAdministratorAuthority(now)
	diagnostics := &diagnosticsDouble{status: taskworkspace.DiagnosticsSourceStatus{
		State: taskworkspace.DiagnosticsSourceCurrent,
	}}
	mandatoryAudit := &capturingCleanupAuditDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
	config.CurrentPlatformAdministratorAuthority = func(
		id taskworkspace.PlatformAdministratorID,
	) (taskworkspace.PlatformAdministratorAuthority, bool) {
		return administrator, id == administrator.ID
	}
	config.Diagnostics = diagnostics
	config.CleanupAudit = mandatoryAudit
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-content-free-diagnostics-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-content-free-diagnostics-debt-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}

	current, err := lifecycle.QueryAdministratorDiagnostics(context.Background(), administratorDiagnosticsRequest(
		debt, administrator, "query-current-diagnostics-1",
	))
	if err != nil {
		t.Fatalf("query current diagnostics: %v", err)
	}
	if current.LifecycleState != taskworkspace.DiagnosticLifecycleOpen ||
		current.Relationship.RevisionID == "" || current.Relationship.OperationID == "" ||
		current.Relationship.Fence == 0 || current.CleanupOwner != taskworkspace.CleanupOwnerC04 ||
		!current.RetryAge.Known || current.EstimatedBytes.Known || current.EstimatedInodes.Known ||
		current.NextAction != taskworkspace.DiagnosticNextRetryCleanup ||
		len(current.EvidenceReferences) == 0 || !current.SourceWatermark.Known {
		t.Fatalf("current diagnostics omitted allowed lifecycle metadata: %#v", current)
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}
	for _, forbidden := range []string{
		"PolicyDomain", "TaskID", "DebtID", "ResourceID", "ResourceGeneration",
		"Content", "Path", "Session", "Mount", "Locator", "Credential",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("diagnostics schema leaked forbidden field %q: %s", forbidden, encoded)
		}
	}

	diagnostics.status = taskworkspace.DiagnosticsSourceStatus{State: taskworkspace.DiagnosticsSourceUnavailable}
	unavailableRequest := administratorDiagnosticsRequest(debt, administrator, "query-unavailable-diagnostics-1")
	unavailable, err := lifecycle.QueryAdministratorDiagnostics(context.Background(), unavailableRequest)
	if err != nil {
		t.Fatalf("query unavailable diagnostics: %v", err)
	}
	if unavailable.SourceState != taskworkspace.DiagnosticsSourceUnavailable ||
		unavailable.SourceWatermark.Known || unavailable.LifecycleState != taskworkspace.DiagnosticLifecycleUnknown ||
		unavailable.EstimatedBytes.Known || unavailable.EstimatedInodes.Known || unavailable.RetryAge.Known {
		t.Fatalf("unavailable diagnostics fabricated zero/current facts: %#v", unavailable)
	}

	diagnostics.status = taskworkspace.DiagnosticsSourceStatus{State: taskworkspace.DiagnosticsSourceCurrent}
	crossWorkspace := administratorDiagnosticsRequest(debt, administrator, "query-cross-workspace-canary-1")
	crossWorkspace.TaskID = "task-cross-workspace-existence-canary"
	crossWorkspace.Operation.RequestDigest = crossWorkspace.CanonicalRequestDigest()
	_, err = lifecycle.QueryAdministratorDiagnostics(context.Background(), crossWorkspace)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorOwnershipDenied)
	if strings.Contains(err.Error(), string(crossWorkspace.TaskID)) {
		t.Fatalf("cross-Workspace diagnostics denial leaked existence canary: %v", err)
	}
	if mandatoryAudit.calls != 2 {
		t.Fatalf("diagnostic audit calls = %d, want current and unavailable queries only", mandatoryAudit.calls)
	}
}

func TestAdministratorDiagnosticsFailClosedWhenMandatoryAuditCannotPersist(t *testing.T) {
	now := taskworkspace.Instant(100)
	administrator := platformAdministratorAuthority(now)
	mandatoryAudit := &capturingCleanupAuditDouble{failure: errors.New("audit persistence unavailable")}
	diagnostics := &diagnosticsDouble{status: taskworkspace.DiagnosticsSourceStatus{
		State: taskworkspace.DiagnosticsSourceCurrent,
	}}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
	config.CurrentPlatformAdministratorAuthority = func(
		id taskworkspace.PlatformAdministratorID,
	) (taskworkspace.PlatformAdministratorAuthority, bool) {
		return administrator, id == administrator.ID
	}
	config.Diagnostics = diagnostics
	config.CleanupAudit = mandatoryAudit
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-diagnostics-audit-failure-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-diagnostics-audit-failure-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	_, err = lifecycle.QueryAdministratorDiagnostics(context.Background(), administratorDiagnosticsRequest(
		debt, administrator, "query-diagnostics-audit-failure-1",
	))
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
}

func TestProjectionRebuildCoversEveryAuthoritativeC04FactFamily(t *testing.T) {
	now := taskworkspace.Instant(100)
	administrator := platformAdministratorAuthority(now)
	telemetry := &telemetryDouble{
		failure: errors.New("telemetry unavailable before rebuild"),
	}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	config.ProjectionSchemaRevision = taskworkspace.ProjectionSchemaV1
	config.Cleanup = &exactGenerationCleanupDouble{}
	config.CleanupAudit = &cleanupAuditDouble{}
	config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
	config.CurrentPlatformAdministratorAuthority = func(
		id taskworkspace.PlatformAdministratorID,
	) (taskworkspace.PlatformAdministratorAuthority, bool) {
		return administrator, id == administrator.ID
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-all-facts-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-all-facts-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, validation, "commit-all-facts-1",
	)); err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-all-facts-current-1",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(current, "create-all-facts-debt-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	if _, err := lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
		debt, administrator, "resolve-all-facts-debt-1",
	)); err != nil {
		t.Fatalf("resolve Cleanup Debt: %v", err)
	}

	telemetry.calls = 0
	telemetry.signals = nil
	telemetry.failure = nil
	rebuilt, err := lifecycle.RebuildProjections(context.Background(), taskworkspace.ProjectionRebuildRequest{
		SchemaRevision: taskworkspace.ProjectionSchemaV1,
	})
	if err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	got := make(map[taskworkspace.ProjectionFactKind]bool)
	cleanupOperation := false
	for _, signals := range telemetry.signals {
		for _, log := range signals.Logs {
			if !log.Timestamp.Known || log.Timestamp.Value == 0 {
				t.Fatalf("authoritative %q projection omitted persisted timestamp: %#v", log.FactKind, log)
			}
			got[log.FactKind] = true
			cleanupOperation = cleanupOperation ||
				(log.FactKind == taskworkspace.ProjectionFactOperation &&
					log.Operation == taskworkspace.ProjectionOperationCleanup)
		}
	}
	for _, want := range []taskworkspace.ProjectionFactKind{
		taskworkspace.ProjectionFactLifecycle,
		taskworkspace.ProjectionFactIntegrity,
		taskworkspace.ProjectionFactOperation,
		taskworkspace.ProjectionFactRetention,
		taskworkspace.ProjectionFactCleanupDebt,
		taskworkspace.ProjectionFactMandatoryAudit,
	} {
		if !got[want] {
			t.Fatalf("projection rebuild omitted %q facts: result=%#v got=%v", want, rebuilt, got)
		}
	}
	if !cleanupOperation {
		t.Fatalf("projection rebuild omitted the authoritative Cleanup Debt operation journal: %#v", telemetry.signals)
	}
}

func TestProjectionRebuildDoesNotChangeFactDigestOnlyBecauseWallClockAdvanced(t *testing.T) {
	now := taskworkspace.Instant(100)
	projection := &projectionCaptureDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Projection = projection
	config.ProjectionSchemaRevision = taskworkspace.ProjectionSchemaV1
	lifecycle, current, older := supersededCheckpointForRetention(t, config)
	retention := inspectCheckpointRetention(t, lifecycle, current, older.CheckpointID)
	attach := taskworkspace.AttachCheckpointRetentionRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: current.TaskWorkspaceID, CheckpointID: older.CheckpointID,
		ExpectedRetentionGeneration: retention.RetentionGeneration,
		Generation:                  current.Generation, Fence: current.Fence,
		Authority: taskworkspace.CheckpointRetentionAuthority{
			ID: "expiring-commit-lease-1", Kind: taskworkspace.CheckpointCommitLeaseAuthority,
			ExpiresAt: now + 10,
		},
		Operation: taskworkspace.Operation{ID: "attach-expiring-retention-projection-1"},
	}
	attach.Operation.RequestDigest = attach.CanonicalRequestDigest()
	retention, err := lifecycle.AttachCheckpointRetention(context.Background(), attach)
	if err != nil {
		t.Fatalf("attach expiring retention authority: %v", err)
	}
	var recoveryAuthority taskworkspace.CheckpointRetentionAuthority
	for _, authority := range retention.Authorities {
		if authority.Kind == taskworkspace.CheckpointRecoveryLineageAuthority {
			recoveryAuthority = authority
		}
	}
	release := taskworkspace.ReleaseCheckpointRetentionRequest{
		PolicyDomainID: "policy-domain-1", TaskID: "task-1",
		TaskWorkspaceID: current.TaskWorkspaceID, CheckpointID: older.CheckpointID,
		AuthorityID: recoveryAuthority.ID, ExpectedRetentionGeneration: retention.RetentionGeneration,
		Generation: current.Generation, Fence: current.Fence,
		Operation: taskworkspace.Operation{ID: "release-recovery-retention-projection-1"},
	}
	release.Operation.RequestDigest = release.CanonicalRequestDigest()
	if _, err := lifecycle.ReleaseCheckpointRetention(context.Background(), release); err != nil {
		t.Fatalf("release recovery retention authority: %v", err)
	}

	projection.rebuilds = nil
	if _, err := lifecycle.RebuildProjections(context.Background(), taskworkspace.ProjectionRebuildRequest{
		SchemaRevision: taskworkspace.ProjectionSchemaV1,
	}); err != nil {
		t.Fatalf("initial projection rebuild: %v", err)
	}
	now += 20
	if _, err := lifecycle.RebuildProjections(context.Background(), taskworkspace.ProjectionRebuildRequest{
		SchemaRevision: taskworkspace.ProjectionSchemaV1,
	}); err != nil {
		t.Fatalf("projection rebuild after clock advance: %v", err)
	}
	if len(projection.rebuilds) != 2 || !reflect.DeepEqual(projection.rebuilds[0], projection.rebuilds[1]) {
		t.Fatalf("wall clock changed an authoritative fact projection: %#v", projection.rebuilds)
	}
}

func TestLiveLifecycleOperationsProjectAuthoritativeOperationFacts(t *testing.T) {
	telemetry := &telemetryDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	if _, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-live-operation-facts-1",
	)); err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}

	got := make(map[taskworkspace.ProjectionOperation]bool)
	for _, signals := range telemetry.signals {
		for _, log := range signals.Logs {
			if log.FactKind == taskworkspace.ProjectionFactOperation {
				got[log.Operation] = true
			}
		}
	}
	for _, want := range []taskworkspace.ProjectionOperation{
		taskworkspace.ProjectionOperationConfirm,
		taskworkspace.ProjectionOperationMaterialize,
		taskworkspace.ProjectionOperationOpenView,
	} {
		if !got[want] {
			t.Fatalf("live projection omitted authoritative %q operation: %#v", want, telemetry.signals)
		}
	}
}

func TestOperationProjectionUsesTerminalRecordedTime(t *testing.T) {
	now := taskworkspace.Instant(100)
	telemetry := &telemetryDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultBeforeAuthoritativeTransaction {
			now = 200
		}
		return nil
	}
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	lifecycle := taskworkspace.NewInMemory(config)
	if _, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-terminal-timestamp-1",
	)); err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	for _, signals := range telemetry.signals {
		for _, log := range signals.Logs {
			if log.FactKind == taskworkspace.ProjectionFactOperation &&
				log.Operation == taskworkspace.ProjectionOperationConfirm {
				if !log.Timestamp.Known || log.Timestamp.Value != 200 {
					t.Fatalf("operation timestamp = %#v, want terminal recorded time 200", log.Timestamp)
				}
				return
			}
		}
	}
	t.Fatal("confirm operation projection not emitted")
}

func TestCleanupOperationProjectionUsesTransitionRecordedTime(t *testing.T) {
	now := taskworkspace.Instant(100)
	telemetry := &telemetryDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-cleanup-transition-timestamp-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-transition-timestamp-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	now = 200
	telemetry.signals = nil
	if _, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
		debt, debt.RetryGeneration, "claim-cleanup-transition-timestamp-1",
	)); err != nil {
		t.Fatalf("claim Cleanup Debt: %v", err)
	}
	for _, signals := range telemetry.signals {
		for _, log := range signals.Logs {
			if log.FactKind == taskworkspace.ProjectionFactOperation &&
				log.Operation == taskworkspace.ProjectionOperationCleanup {
				if !log.Timestamp.Known || log.Timestamp.Value != 200 {
					t.Fatalf("Cleanup operation timestamp = %#v, want transition recorded time 200", log.Timestamp)
				}
				return
			}
		}
	}
	t.Fatal("Cleanup claim operation projection not emitted")
}

func TestProjectionIdentityIncludesAuthorityScope(t *testing.T) {
	telemetry := &telemetryDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	lifecycle := taskworkspace.NewInMemory(config)
	const sharedOperationID = "same-operation-id-in-two-task-scopes"
	for _, taskID := range []taskworkspace.TaskID{"task-scope-1", "task-scope-2"} {
		request := confirmRequest("policy-domain-1", string(taskID), sharedOperationID)
		if _, err := lifecycle.ConfirmTaskWorkspace(context.Background(), request); err != nil {
			t.Fatalf("confirm %s: %v", taskID, err)
		}
	}
	confirmSignals := 0
	for _, signals := range telemetry.signals {
		for _, log := range signals.Logs {
			if log.FactKind == taskworkspace.ProjectionFactOperation &&
				log.Operation == taskworkspace.ProjectionOperationConfirm {
				confirmSignals++
			}
		}
	}
	if confirmSignals != 2 {
		t.Fatalf("scope-bound operations produced %d confirm projections, want 2", confirmSignals)
	}
}

func TestProtectedCleanupResolutionFailsClosedBeforeMandatoryAuditWhenAuthorityIsStale(t *testing.T) {
	now := taskworkspace.Instant(100)
	administrator := platformAdministratorAuthority(now)
	mandatoryAudit := &cleanupAuditDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Cleanup = &exactGenerationCleanupDouble{}
	config.CleanupAudit = mandatoryAudit
	config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
	config.CurrentPlatformAdministratorAuthority = func(
		taskworkspace.PlatformAdministratorID,
	) (taskworkspace.PlatformAdministratorAuthority, bool) {
		return taskworkspace.PlatformAdministratorAuthority{}, false
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-stale-audit-authority-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-stale-audit-authority-debt-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	_, err = lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
		debt, administrator, "resolve-stale-audit-authority-1",
	))
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorInvalidIntent)
	if mandatoryAudit.calls != 0 {
		t.Fatalf("mandatory audit adapter called %d times for stale authority", mandatoryAudit.calls)
	}
	stillOpen, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, DebtID: debt.DebtID,
	})
	if err != nil || stillOpen.State != taskworkspace.CleanupDebtOpen {
		t.Fatalf("stale authority changed Cleanup Debt: %#v, err=%v", stillOpen, err)
	}
}

func TestTelemetryRejectsCardinalityAttackAndRedactsCanaries(t *testing.T) {
	telemetry := &telemetryDouble{}
	projector := taskworkspace.NewDeterministicProjection(telemetry)
	attack := taskworkspace.ProjectionEnvelope{
		FactID: "fact-cardinality-attack", FactRevision: 1,
		SchemaRevision: taskworkspace.ProjectionSchemaV1,
		Kind:           taskworkspace.ProjectionFactLifecycle,
		Operation:      taskworkspace.ProjectionOperation("user-controlled-operation-series-999999"),
		Result:         taskworkspace.ProjectionResultCommitted,
		LifecycleState: taskworkspace.ProjectionStateCommitted,
		ResourceClass:  taskworkspace.ProjectionResourceTaskWorkspace,
		AdapterClass:   taskworkspace.ProjectionAdapterDeterministic,
	}
	assertLifecycleErrorCode(t, projector.Project(context.Background(), attack), taskworkspace.ErrorInvalidIntent)
	if telemetry.calls != 0 {
		t.Fatalf("cardinality attack reached telemetry %d times", telemetry.calls)
	}

	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = projector
	config.ProjectionSchemaRevision = taskworkspace.ProjectionSchemaV1
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-redaction-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	contentCanary := "content-cross-workspace-existence-canary"
	operationCanary := "commit-private-path-session-mount-object-locator-credential-canary"
	manifest := declaredStateManifest(contentCanary)
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, validation, operationCanary,
	)); err != nil {
		t.Fatalf("commit canary-bearing authoritative intent: %v", err)
	}
	encoded, err := json.Marshal(telemetry.signals)
	if err != nil {
		t.Fatalf("marshal telemetry signals: %v", err)
	}
	for _, canary := range []string{
		contentCanary,
		operationCanary,
		"/private/user/deck.pptx",
		"s3://secret-bucket/object-key",
		"credential=do-not-disclose",
		"cross-workspace-existence-canary",
	} {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("ordinary telemetry leaked canary %q: %s", canary, encoded)
		}
	}
}

func TestTelemetryCannotResolveCleanupDebtOrAuthorizeReclamation(t *testing.T) {
	telemetry := &telemetryDouble{}
	projector := taskworkspace.NewDeterministicProjection(telemetry)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = projector
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-no-telemetry-authority-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-no-telemetry-authority-debt-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	fabricated := taskworkspace.ProjectionEnvelope{
		FactID: "fabricated-cleanup-resolution", FactRevision: 99,
		SchemaRevision: taskworkspace.ProjectionSchemaV1,
		Kind:           taskworkspace.ProjectionFactCleanupDebt,
		Operation:      taskworkspace.ProjectionOperationCleanup,
		Result:         taskworkspace.ProjectionResultCommitted,
		LifecycleState: taskworkspace.ProjectionStateCleanupResolved,
		ResourceClass:  taskworkspace.ProjectionResourceCleanupDebt,
		AdapterClass:   taskworkspace.ProjectionAdapterDeterministic,
	}
	if err := projector.Project(context.Background(), fabricated); err != nil {
		t.Fatalf("project fabricated operational observation: %v", err)
	}
	inspected, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, DebtID: debt.DebtID,
	})
	if err != nil {
		t.Fatalf("inspect authoritative Cleanup Debt: %v", err)
	}
	if inspected.State != taskworkspace.CleanupDebtOpen || inspected.Resolution != "" {
		t.Fatalf("telemetry changed authoritative Cleanup Debt: %#v", inspected)
	}
}

func TestAuthoritativeCleanupDebtSurvivesLiveTelemetryOutage(t *testing.T) {
	telemetry := &telemetryDouble{failure: errors.New("logs metrics traces and collector unavailable")}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-live-debt-projection-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-live-debt-projection-1"),
	)
	if err != nil {
		t.Fatalf("telemetry outage rolled back Cleanup Debt creation: %v", err)
	}
	if debt.State != taskworkspace.CleanupDebtOpen {
		t.Fatalf("Cleanup Debt was not committed: %#v", debt)
	}
	found := false
	for _, signals := range telemetry.signals {
		for _, log := range signals.Logs {
			if log.FactKind == taskworkspace.ProjectionFactCleanupDebt &&
				log.State == taskworkspace.ProjectionStateCleanupOpen {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("live Cleanup Debt projection missing: %#v", telemetry.signals)
	}
}

func TestCleanupDebtProjectionRevisionAdvancesAcrossClaimAndRetry(t *testing.T) {
	telemetry := &telemetryDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-cleanup-revision-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	debt, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-revision-1"),
	)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	claimed, err := lifecycle.ClaimCleanupDebt(
		context.Background(), claimCleanupDebtRequest(debt, debt.RetryGeneration, "claim-cleanup-revision-1"),
	)
	if err != nil {
		t.Fatalf("claim Cleanup Debt: %v", err)
	}
	if _, err := lifecycle.ReconcileCleanupDebt(
		context.Background(), reconcileCleanupDebtRequest(claimed, "reconcile-cleanup-revision-1"),
	); err != nil {
		t.Fatalf("reconcile Cleanup Debt: %v", err)
	}

	states := make(map[taskworkspace.ProjectionLifecycleState]bool)
	for _, signals := range telemetry.signals {
		for _, log := range signals.Logs {
			if log.FactKind == taskworkspace.ProjectionFactCleanupDebt {
				states[log.State] = true
			}
		}
	}
	for _, want := range []taskworkspace.ProjectionLifecycleState{
		taskworkspace.ProjectionStateCleanupOpen,
		taskworkspace.ProjectionStateCleanupClaimed,
		taskworkspace.ProjectionStateCleanupRetry,
	} {
		if !states[want] {
			t.Fatalf("Cleanup Debt projection omitted %q transition: %#v", want, telemetry.signals)
		}
	}
}

func TestCommitProjectsLifecycleOperationIntegrityAndRetentionFactsLive(t *testing.T) {
	telemetry := &telemetryDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Projection = taskworkspace.NewDeterministicProjection(telemetry)
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-live-commit-facts-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-live-commit-facts-1")
	validation := acceptedValidationEvidence(confirmed, view, manifest)
	telemetry.calls = 0
	telemetry.signals = nil
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, validation, "commit-live-facts-1",
	)); err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	families := make(map[taskworkspace.ProjectionFactKind]bool)
	for _, signals := range telemetry.signals {
		for _, log := range signals.Logs {
			families[log.FactKind] = true
		}
	}
	for _, want := range []taskworkspace.ProjectionFactKind{
		taskworkspace.ProjectionFactLifecycle,
		taskworkspace.ProjectionFactOperation,
		taskworkspace.ProjectionFactIntegrity,
		taskworkspace.ProjectionFactRetention,
	} {
		if !families[want] {
			t.Fatalf("live commit projection omitted %q: %#v", want, telemetry.signals)
		}
	}
}

func administratorDiagnosticsRequest(
	debt taskworkspace.CleanupDebt,
	authority taskworkspace.PlatformAdministratorAuthority,
	operationID string,
) taskworkspace.QueryAdministratorDiagnosticsRequest {
	request := taskworkspace.QueryAdministratorDiagnosticsRequest{
		PolicyDomainID:         debt.PolicyDomainID,
		TaskID:                 debt.TaskID,
		DebtID:                 debt.DebtID,
		Subject:                taskworkspace.DiagnosticSubjectCleanupDebt,
		Reason:                 taskworkspace.DiagnosticReasonCleanupReconciliation,
		AdministratorAuthority: authority,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func stringsContainsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func projectionSignalIdentities(
	signals []taskworkspace.OperationalSignals,
) map[taskworkspace.ProjectionSignalIdentity]struct{} {
	identities := make(map[taskworkspace.ProjectionSignalIdentity]struct{}, len(signals))
	for _, signal := range signals {
		identities[signal.Identity] = struct{}{}
	}
	return identities
}

type telemetryDouble struct {
	calls   int
	failure error
	signals []taskworkspace.OperationalSignals
}

type projectionCaptureDouble struct {
	rebuilds [][]taskworkspace.ProjectionEnvelope
}

func (*projectionCaptureDouble) Project(context.Context, taskworkspace.ProjectionEnvelope) error {
	return nil
}

func (d *projectionCaptureDouble) Rebuild(
	_ context.Context,
	_ taskworkspace.ProjectionSchemaRevision,
	facts []taskworkspace.ProjectionEnvelope,
) error {
	d.rebuilds = append(d.rebuilds, append([]taskworkspace.ProjectionEnvelope(nil), facts...))
	return nil
}

func (d *telemetryDouble) Emit(_ context.Context, signals taskworkspace.OperationalSignals) error {
	d.calls++
	d.signals = append(d.signals, signals)
	return d.failure
}

type auditDeliveryDouble struct {
	calls   int
	failure error
	facts   []taskworkspace.AuditDeliveryFact
}

type blockingAuditDeliveryDouble struct {
	entered chan struct{}
	release chan struct{}
	failure error
}

func newBlockingAuditDeliveryDouble() *blockingAuditDeliveryDouble {
	return &blockingAuditDeliveryDouble{entered: make(chan struct{}), release: make(chan struct{})}
}

func (d *blockingAuditDeliveryDouble) Deliver(context.Context, taskworkspace.AuditDeliveryFact) error {
	close(d.entered)
	<-d.release
	return d.failure
}

func (d *auditDeliveryDouble) Deliver(_ context.Context, fact taskworkspace.AuditDeliveryFact) error {
	d.calls++
	d.facts = append(d.facts, fact)
	return d.failure
}

type diagnosticsDouble struct {
	denied error
	status taskworkspace.DiagnosticsSourceStatus
}

type capturingCleanupAuditDouble struct {
	calls   int
	failure error
	intents []taskworkspace.CleanupAuditIntent
	facts   []taskworkspace.CleanupAuditEvidence
}

func (d *capturingCleanupAuditDouble) RecordRequired(
	_ context.Context,
	transaction taskworkspace.CleanupAuditTransaction,
) error {
	d.calls++
	intent := transaction.Intent()
	d.intents = append(d.intents, intent)
	if d.failure != nil {
		return d.failure
	}
	evidence := taskworkspace.CleanupAuditEvidence{
		ID:     taskworkspace.CleanupAuditEvidenceID("audit-" + intent.Operation.ID),
		Action: intent.Action, DebtID: intent.DebtID,
		AdministratorID:     intent.AdministratorAuthority.ID,
		AuthorityGeneration: intent.AdministratorAuthority.Generation,
		Resolution:          intent.Resolution, ClosedReason: intent.ClosedReason,
		Duration: intent.Duration, DecisionEvidenceRoot: intent.DecisionEvidenceRoot,
		ResolutionGeneration: intent.ResolutionGeneration,
		OperationID:          intent.Operation.ID, RecordedAt: 100,
	}
	evidence.Digest = evidence.CanonicalDigest()
	if err := transaction.Commit(evidence); err != nil {
		return err
	}
	d.facts = append(d.facts, evidence)
	return nil
}

func projectionEnvelope(
	factID taskworkspace.ProjectionFactID,
	operation taskworkspace.ProjectionOperation,
) taskworkspace.ProjectionEnvelope {
	return taskworkspace.ProjectionEnvelope{
		FactID: factID, FactRevision: 1, SchemaRevision: taskworkspace.ProjectionSchemaV1,
		Kind: taskworkspace.ProjectionFactOperation, Operation: operation,
		Result: taskworkspace.ProjectionResultCommitted, LifecycleState: taskworkspace.ProjectionStateCommitted,
		ResourceClass: taskworkspace.ProjectionResourceTaskWorkspace,
		AdapterClass:  taskworkspace.ProjectionAdapterDeterministic,
	}
}

type blockingTelemetryDouble struct {
	mu           sync.Mutex
	calls        int
	firstEntered chan struct{}
	releaseFirst chan struct{}
}

type staleFailureTelemetryDouble struct {
	mu           sync.Mutex
	calls        int
	firstEntered chan struct{}
	releaseFirst chan struct{}
}

func newStaleFailureTelemetryDouble() *staleFailureTelemetryDouble {
	return &staleFailureTelemetryDouble{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (d *staleFailureTelemetryDouble) Emit(context.Context, taskworkspace.OperationalSignals) error {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()
	if call == 1 {
		close(d.firstEntered)
		<-d.releaseFirst
		return errors.New("interrupted telemetry attempt failed after rebuild")
	}
	return nil
}

func (d *staleFailureTelemetryDouble) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func newBlockingTelemetryDouble() *blockingTelemetryDouble {
	return &blockingTelemetryDouble{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (d *blockingTelemetryDouble) Emit(context.Context, taskworkspace.OperationalSignals) error {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()
	if call == 1 {
		close(d.firstEntered)
		<-d.releaseFirst
	}
	return nil
}

func (d *diagnosticsDouble) Authorize(
	_ context.Context,
	_ taskworkspace.AdministratorDiagnosticIntent,
) error {
	return d.denied
}

func (d *diagnosticsDouble) SourceStatus(context.Context) taskworkspace.DiagnosticsSourceStatus {
	return d.status
}
