package taskorchestration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestExternalAuditAndTelemetryProjectionDeliveryIsAsynchronousAndRebuildable(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	sink := &failingDecisionProjectionSink{}
	auditFaults := &taskorchestration.DiagnosticAuditFaultController{}
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: sink,
			Telemetry:     sink,
		},
	)
	if err != nil {
		t.Fatalf("create decision projection adapter: %v", err)
	}
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, ProjectionDelivery: projector,
		DiagnosticAuditFaults: auditFaults,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "projection-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := adapter.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		intentHeader(t, "projection-start", "projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit with failed external projections: %v", err)
	}
	if decision.AcceptedTaskRevision != 1 || sink.auditCount() != 0 || sink.telemetryCount() != 0 {
		t.Fatalf("projection delivery ran in protected Decide path: decision=%+v", decision)
	}
	if _, err := adapter.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		intentHeader(t, "projection-other-start", "projection-other-task", now), owner,
	)); err != nil {
		t.Fatalf("commit other Task used to prove scoped backlog: %v", err)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "projection-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	inspection := taskorchestration.NewProjectionDeliveryInspectionRequest(
		administrator, taskID(t, "projection-task"), 1,
	)
	auditFaults.FailNext()
	_, err = adapter.InspectDecisionProjectionBacklog(context.Background(), inspection)
	requireSharedDecisionError(t, err, taskorchestration.ErrorDependencyUnavailable)
	beforeDelivery, err := adapter.InspectDecisionProjectionBacklog(context.Background(), inspection)
	if err != nil || beforeDelivery.Pending != 1 || beforeDelivery.Delivered != 0 ||
		beforeDelivery.SourceFactCount != 1 || len(beforeDelivery.Evidence) != 1 ||
		beforeDelivery.Evidence[0].AttemptCount != 0 ||
		beforeDelivery.AccessAuditFactRef.AuditFactID.String() == "" ||
		beforeDelivery.AccessAuditFactRef.Outcome != taskorchestration.DiagnosticAuditAccepted {
		t.Fatalf("inspect initial projection backlog: backlog=%+v err=%v", beforeDelivery, err)
	}
	failed, err := adapter.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			administrator, taskID(t, "projection-task"), 100,
		),
	)
	if err != nil || failed.Pending != 2 || failed.Delivered != 0 ||
		failed.SourceFactCount != 2 || len(failed.Evidence) != 2 ||
		failed.Evidence[0].AttemptCount != 1 || sink.auditCount() != 2 || sink.telemetryCount() != 1 {
		t.Fatalf("failed projection delivery was not retained: backlog=%+v err=%v", failed, err)
	}
	if sink.lastAudit().CanonicalDigest == (taskorchestration.ProjectionDigest{}) ||
		sink.lastAudit().CanonicalDigest != taskorchestration.ExternalAuditProjectionDigest(sink.lastAudit()) {
		t.Fatalf("external audit projection lacks canonical digest: %+v", sink.lastAudit())
	}
	view, err := adapter.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "projection-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || view.TaskRevision != 1 || view.DecisionCount != 1 {
		t.Fatalf("projection failure rolled back protected Task: view=%+v err=%v", view, err)
	}
	sink.setHealthy()
	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Second) }, ProjectionDelivery: projector,
		DiagnosticAuditFaults: auditFaults,
	})
	rebuilt, err := restarted.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			administrator, taskID(t, "projection-task"), 100,
		),
	)
	if err != nil || rebuilt.Pending != 0 || rebuilt.Delivered != 3 ||
		rebuilt.SourceFactCount != 3 || len(rebuilt.Evidence) != 3 ||
		rebuilt.Evidence[0].AttemptCount != 2 || sink.auditCount() != 5 || sink.telemetryCount() != 2 {
		t.Fatalf("rebuild projection delivery after restart: backlog=%+v err=%v", rebuilt, err)
	}
	again, err := restarted.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			administrator, taskID(t, "projection-task"), 100,
		),
	)
	if err != nil || again.Pending != 0 || again.Delivered != 4 ||
		again.SourceFactCount != 4 || sink.auditCount() != 6 || sink.telemetryCount() != 2 ||
		sink.auditFactCount(decision.MandatoryAuditFactRef.AuditFactID) != 2 {
		t.Fatalf("delivered projection was not idempotent: backlog=%+v err=%v", again, err)
	}
}

func TestProtectedDiagnosticAccessAuditDeliveryIsRebuildable(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 15, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	sink := &failingDecisionProjectionSink{healthy: true}
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: sink,
			Telemetry:     sink,
		},
	)
	if err != nil {
		t.Fatalf("create diagnostic audit projection adapter: %v", err)
	}
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, ProjectionDelivery: projector,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "diagnostic-projection-owner"),
		taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := adapter.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		intentHeader(t, "diagnostic-projection-start", "diagnostic-projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit diagnostic projection Task: %v", err)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "diagnostic-projection-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonIntegrity,
	)
	view, err := adapter.Diagnose(
		context.Background(),
		taskorchestration.NewDecisionDiagnosticQuery(
			administrator, taskID(t, "diagnostic-projection-task"), decision.DecisionID,
		),
	)
	if err != nil {
		t.Fatalf("record protected diagnostic access audit: %v", err)
	}
	request := taskorchestration.NewProjectionDeliveryRebuildRequest(
		administrator, taskID(t, "diagnostic-projection-task"), 10,
	)
	if _, err := adapter.RebuildDecisionProjectionDelivery(context.Background(), request); err != nil {
		t.Fatalf("rebuild diagnostic access audit delivery: %v", err)
	}
	projected, found := sink.auditFor(view.AccessAuditFactRef.AuditFactID)
	if !found || projected.FactKind != taskorchestration.ExternalAuditDiagnosticAccessFact ||
		projected.CanonicalDigest != view.AccessAuditFactRef.CanonicalDigest ||
		taskorchestration.ExternalAuditProjectionDigest(projected) != projected.CanonicalDigest ||
		sink.auditFactCount(view.AccessAuditFactRef.AuditFactID) != 1 {
		t.Fatalf("diagnostic access audit was not projected canonically: projected=%+v found=%t", projected, found)
	}
	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Second) }, ProjectionDelivery: projector,
	})
	if _, err := restarted.RebuildDecisionProjectionDelivery(context.Background(), request); err != nil {
		t.Fatalf("rebuild diagnostic access audit delivery after restart: %v", err)
	}
	if sink.auditFactCount(view.AccessAuditFactRef.AuditFactID) != 1 {
		t.Fatalf("delivered diagnostic access audit was projected more than once")
	}
}

func TestProjectionDeliverySuccessIsMonotonicAcrossConcurrentRebuilds(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 20, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	sink := newInterleavingProjectionSink()
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: sink,
			Telemetry:     sink,
		},
	)
	if err != nil {
		t.Fatalf("create concurrent projection adapter: %v", err)
	}
	older := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, ProjectionDelivery: projector,
	})
	newer := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Second) }, ProjectionDelivery: projector,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "concurrent-projection-owner"),
		taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := older.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		intentHeader(t, "concurrent-projection-start", "concurrent-projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit concurrent projection Task: %v", err)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "concurrent-projection-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	rebuild := taskorchestration.NewProjectionDeliveryRebuildRequest(
		administrator, taskID(t, "concurrent-projection-task"), 10,
	)
	olderDone := make(chan error, 1)
	go func() {
		_, rebuildErr := older.RebuildDecisionProjectionDelivery(context.Background(), rebuild)
		olderDone <- rebuildErr
	}()
	<-sink.firstAuditStarted
	if _, err := newer.RebuildDecisionProjectionDelivery(context.Background(), rebuild); err != nil {
		t.Fatalf("newer concurrent projection attempt: %v", err)
	}
	close(sink.releaseFirstAudit)
	if err := <-olderDone; err != nil {
		t.Fatalf("older concurrent projection attempt: %v", err)
	}
	before, err := older.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "concurrent-projection-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query Task after concurrent projection attempts: %v", err)
	}
	backlog, err := newer.InspectDecisionProjectionBacklog(
		context.Background(),
		taskorchestration.NewProjectionDeliveryInspectionRequest(
			administrator, taskID(t, "concurrent-projection-task"), 10,
		),
	)
	if err != nil {
		t.Fatalf("inspect concurrent projection evidence: %v", err)
	}
	var evidence taskorchestration.ProjectionDeliveryEvidence
	for _, candidate := range backlog.Evidence {
		if candidate.AuditFactID == decision.MandatoryAuditFactRef.AuditFactID {
			evidence = candidate
			break
		}
	}
	if !evidence.ExternalAuditDelivered || !evidence.TelemetryDelivered ||
		evidence.FirstExternalAuditDeliveredAt.IsZero() ||
		evidence.LastExternalAuditDeliveredAt.IsZero() ||
		!evidence.FirstExternalAuditDeliveredAt.Equal(now.Add(time.Second)) ||
		!evidence.LastExternalAuditDeliveredAt.Equal(now.Add(time.Second)) {
		t.Fatalf("older failed attempt regressed successful delivery evidence: %+v", evidence)
	}
	after, err := newer.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "concurrent-projection-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || after.TaskRevision != before.TaskRevision ||
		after.DecisionCount != before.DecisionCount || after.LatestDecisionID != before.LatestDecisionID {
		t.Fatalf("concurrent projection delivery changed Task state: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestBoundedTelemetryUsesTypedLabelsAndAllowlistedLogsAndTraces(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 30, 0, 0, time.UTC)
	telemetry := taskorchestration.NewDeterministicTelemetry()
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: taskorchestration.ExternalAuditProjectionSinkFunc(func(
				context.Context,
				taskorchestration.ExternalAuditProjection,
			) error {
				return nil
			}),
			Telemetry: telemetry,
		},
	)
	if err != nil {
		t.Fatalf("create bounded telemetry projector: %v", err)
	}
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatalf("create telemetry decision harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "telemetry-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "telemetry-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "telemetry-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(),
		taskorchestration.NewStartPinnedTaskIntent(
			intentHeader(t, "telemetry-start", "telemetry-task", now), owner, pinned,
		))
	if err != nil {
		t.Fatalf("start telemetry Task: %v", err)
	}
	workHeader := intentHeader(t, "telemetry-work", "telemetry-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(),
		taskorchestration.NewMakeWorkAvailableIntent(
			workHeader, worker, operationID(t, "telemetry-work-available"),
		))
	if err != nil {
		t.Fatalf("commit telemetry work: %v", err)
	}
	if err := projector.ObserveCommittedDecision(context.Background(), work); err != nil {
		t.Fatalf("project committed Decision: %v", err)
	}
	if err := telemetry.ProjectReconciliation(
		context.Background(),
		taskorchestration.ReconciliationTelemetryProjection{
			SchemaVersion: taskorchestration.ProjectionSchemaV1,
			DecisionID:    work.DecisionID, OperationID: work.EnactmentRefs[0].OperationID,
			TaskRevision: work.AcceptedTaskRevision,
			Outcome:      taskorchestration.TelemetryReconciliationRequired,
			Category:     taskorchestration.TelemetryCategoryDependency,
			ObservedAt:   now.Add(2 * time.Second),
		},
	); err != nil {
		t.Fatalf("project reconciliation health: %v", err)
	}
	snapshot := telemetry.Snapshot()
	if len(snapshot.Metrics) != 3 || len(snapshot.Logs) != 3 || len(snapshot.Traces) != 3 {
		t.Fatalf("bounded telemetry snapshot omitted signals: %+v", snapshot)
	}
	if taskorchestration.MetricSeriesUpperBound() == 0 ||
		taskorchestration.MetricSeriesUpperBound() > 512 {
		t.Fatalf("metric series bound is absent or unexpectedly unbounded: %d",
			taskorchestration.MetricSeriesUpperBound())
	}
	for _, metric := range snapshot.Metrics {
		if !taskorchestration.RegisteredMetricSample(metric) {
			t.Fatalf("metric escaped the closed registry: %+v", metric)
		}
		text := fmt.Sprintf("%+v", metric.Labels)
		for _, forbidden := range []string{
			work.DecisionID.String(), work.EnactmentRefs[0].OperationID.String(),
			"telemetry-task", "telemetry-work-available",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("metric labels contain high-cardinality identity %q: %+v", forbidden, metric)
			}
		}
	}
	for _, log := range snapshot.Logs {
		if log.SchemaVersion != taskorchestration.StructuredLogSchemaV1 ||
			(log.Severity != taskorchestration.StructuredLogInfo &&
				log.Severity != taskorchestration.StructuredLogWarning) {
			t.Fatalf("ordinary structured log lacks registered schema or safe severity: %+v", log)
		}
		text := fmt.Sprintf("%+v", log)
		if strings.Contains(text, work.DecisionID.String()) ||
			strings.Contains(text, work.EnactmentRefs[0].OperationID.String()) {
			t.Fatalf("ordinary structured log contains protected business identity: %+v", log)
		}
	}
	if snapshot.Traces[0].DecisionID != work.DecisionID ||
		snapshot.Traces[1].OperationID != work.EnactmentRefs[0].OperationID {
		t.Fatalf("allowlisted protected trace correlation is missing: %+v", snapshot.Traces)
	}
}

type failingDecisionProjectionSink struct {
	mu         sync.Mutex
	audits     uint64
	telemetry  uint64
	healthy    bool
	audit      taskorchestration.ExternalAuditProjection
	auditsSeen []taskorchestration.ExternalAuditProjection
}

type interleavingProjectionSink struct {
	mu                sync.Mutex
	auditCalls        uint64
	firstAuditStarted chan struct{}
	releaseFirstAudit chan struct{}
}

func newInterleavingProjectionSink() *interleavingProjectionSink {
	return &interleavingProjectionSink{
		firstAuditStarted: make(chan struct{}),
		releaseFirstAudit: make(chan struct{}),
	}
}

func (sink *interleavingProjectionSink) ProjectExternalAudit(
	_ context.Context,
	_ taskorchestration.ExternalAuditProjection,
) error {
	sink.mu.Lock()
	sink.auditCalls++
	call := sink.auditCalls
	sink.mu.Unlock()
	if call == 1 {
		close(sink.firstAuditStarted)
		<-sink.releaseFirstAudit
		return errors.New("older controlled projection failure")
	}
	return nil
}

func (sink *interleavingProjectionSink) ProjectTelemetry(
	_ context.Context,
	_ taskorchestration.DecisionTelemetryProjection,
) error {
	return nil
}

func (sink *failingDecisionProjectionSink) ProjectExternalAudit(
	_ context.Context,
	projection taskorchestration.ExternalAuditProjection,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.audits++
	sink.audit = projection
	sink.auditsSeen = append(sink.auditsSeen, projection)
	if sink.healthy {
		return nil
	}
	return errors.New("credential-canary at /private/session")
}

func (sink *failingDecisionProjectionSink) ProjectTelemetry(
	_ context.Context,
	_ taskorchestration.DecisionTelemetryProjection,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.telemetry++
	if sink.healthy {
		return nil
	}
	return errors.New("content-canary at object-locator")
}

func (sink *failingDecisionProjectionSink) setHealthy() {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.healthy = true
}

func (sink *failingDecisionProjectionSink) lastAudit() taskorchestration.ExternalAuditProjection {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.audit
}

func (sink *failingDecisionProjectionSink) auditCount() uint64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.audits
}

func (sink *failingDecisionProjectionSink) auditFor(
	auditFactID taskorchestration.AuditFactID,
) (taskorchestration.ExternalAuditProjection, bool) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, projection := range sink.auditsSeen {
		if projection.AuditFactID == auditFactID {
			return projection, true
		}
	}
	return taskorchestration.ExternalAuditProjection{}, false
}

func (sink *failingDecisionProjectionSink) auditFactCount(
	auditFactID taskorchestration.AuditFactID,
) uint64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var count uint64
	for _, projection := range sink.auditsSeen {
		if projection.AuditFactID == auditFactID {
			count++
		}
	}
	return count
}

func (sink *failingDecisionProjectionSink) telemetryCount() uint64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.telemetry
}
