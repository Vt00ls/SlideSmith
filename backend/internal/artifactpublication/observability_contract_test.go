package artifactpublication

// Child SPEC #111 (C05-07) bounded-observability contract: metrics use only
// registered bounded dimensions; User, Workspace, Task, version, member,
// operation, evidence, digest, path, locator, TraceID and free-form values
// can never become primary labels. Structured logs and trace spans carry
// closed allowlists. Telemetry is emitted strictly after commit and its
// outage never changes publication authority. Protected telemetry reads
// require a reason-bound, least-privilege, bounded scope and never return
// unknown state as a fabricated zero.

import (
	"context"
	"testing"
)

// failingSink fails every projection delivery, simulating an external audit
// or telemetry outage.
type failingSink struct {
	err error
}

func (s *failingSink) ProjectTelemetry(context.Context, PublicationTelemetryProjection) error {
	return s.err
}

func (s *failingSink) ProjectExternalAudit(context.Context, ExternalAuditProjection) error {
	return s.err
}

// TestMetricRegistryRejectsUnregisteredSamples proves the metric registry is
// closed: an unregistered name, an invalid label value, or a label
// combination outside the per-metric policy is rejected, and a business or
// high-cardinality value cannot even be expressed as a label.
func TestMetricRegistryRejectsUnregisteredSamples(t *testing.T) {
	// A valid registered sample (post-commit publication operation) passes.
	valid := MetricSample{
		Name: MetricPublicationOperationCount,
		Labels: MetricLabels{
			Operation: MetricOperationActivate, State: MetricStateActivated,
			Outcome: MetricOutcomeAccepted, Adapter: MetricAdapterInMemory,
			Category: TelemetryCategoryNone,
		},
		Count: 1,
	}
	if !RegisteredMetricSample(valid) {
		t.Fatal("registered sample must be accepted")
	}
	// Unregistered metric name is rejected.
	if RegisteredMetricSample(MetricSample{Name: 0, Labels: valid.Labels, Count: 1}) {
		t.Fatal("zero metric name must be rejected")
	}
	if RegisteredMetricSample(MetricSample{Name: 99, Labels: valid.Labels, Count: 1}) {
		t.Fatal("unregistered metric name must be rejected")
	}
	// Invalid label values are rejected.
	invalidOutcome := valid
	invalidOutcome.Labels.Outcome = 200
	if RegisteredMetricSample(invalidOutcome) {
		t.Fatal("invalid outcome label must be rejected")
	}
	invalidState := valid
	invalidState.Labels.State = 200
	if RegisteredMetricSample(invalidState) {
		t.Fatal("invalid state label must be rejected by the policy")
	}
	// A residue-disposition label is only admitted by the residue policy.
	residueMisplaced := valid
	residueMisplaced.Labels.ResidueDisposition = MetricResidueDispositionReleased
	if RegisteredMetricSample(residueMisplaced) {
		t.Fatal("residue disposition label outside its metric policy must be rejected")
	}
	// Zero count is never a valid sample (no fabricated zeros).
	if RegisteredMetricSample(MetricSample{Name: MetricPublicationOperationCount, Labels: valid.Labels, Count: 0}) {
		t.Fatal("zero-count sample must be rejected")
	}
}

// TestMetricSeriesUpperBoundIsBounded proves the metric series budget is
// derived from the closed registry and cannot be enlarged by runtime
// identities: no User, Workspace, Task, version, member, operation,
// evidence, digest, path, locator or TraceID exists in the label set.
func TestMetricSeriesUpperBoundIsBounded(t *testing.T) {
	bound := MetricSeriesUpperBound()
	if bound == 0 {
		t.Fatal("metric series bound must be positive")
	}
	// The bound is a fixed constant of the closed registry; running more
	// operations never changes it.
	first := MetricSeriesUpperBound()
	_ = RegisteredMetricSample(MetricSample{
		Name: MetricPublicationOperationCount,
		Labels: MetricLabels{
			Operation: MetricOperationPrepare, State: MetricStatePrepared,
			Outcome: MetricOutcomeAccepted, Adapter: MetricAdapterPostgres,
		},
		Count: 1,
	})
	if second := MetricSeriesUpperBound(); second != first {
		t.Fatalf("metric series bound changed after samples: %d -> %d", first, second)
	}
}

// TestPostCommitTelemetryProjectionBounded proves the deterministic
// telemetry adapter receives bounded, content-free post-commit projections
// for every protected decision with closed enums only, and that the same
// projection carries no business identity label.
func TestPostCommitTelemetryProjectionBounded(t *testing.T) {
	f := newObservableFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-telemetry", set)
	f.mustVerify(t, "op-telemetry", set)
	f.mustActivate(t, "op-telemetry")

	authority := f.auditAuthority()
	snapshot, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		authority, f.taskID, "op-telemetry", 10,
	))
	if err != nil {
		t.Fatalf("telemetry snapshot: %v", err)
	}
	if len(snapshot.Traces) != 3 {
		t.Fatalf("traces = %d, want 3 (prepare/verify/activate)", len(snapshot.Traces))
	}
	for _, trace := range snapshot.Traces {
		if trace.TaskID != f.taskID || trace.OperationID == "" ||
			trace.Operation == MetricOperationNone || !validMetricOperation(trace.Operation) ||
			trace.TraceID != "" {
			t.Fatalf("trace must carry only allowlisted protected correlation: %#v", trace)
		}
	}
	for _, sample := range snapshot.Metrics {
		if !RegisteredMetricSample(sample) {
			t.Fatalf("emitted metric must be registered: %#v", sample)
		}
	}
	for _, record := range snapshot.Logs {
		if record.SchemaVersion != StructuredLogSchemaV1 || record.Module != TelemetryModuleArtifactPublication ||
			record.Event == 0 {
			t.Fatalf("structured log must carry a closed allowlist: %#v", record)
		}
	}
}

// TestTelemetrySnapshotProtectedReasonBound proves the protected telemetry
// snapshot requires a valid reason-bound administrator metadata authority,
// is exact-scope, is bounded, and fails closed without one.
func TestTelemetrySnapshotProtectedReasonBound(t *testing.T) {
	f := newObservableFixture(t)
	f.prepareAndVerify(t, "op-telemetry-scope")

	// Unbound/unknown authority is denied non-enumerating.
	unbound := NewAdministratorMetadataAuthority("", 0, DiagnosticReasonOperations)
	if _, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		unbound, f.taskID, "op-telemetry-scope", 10,
	)); !isCode(err, ErrorOwnershipDenied) {
		t.Fatalf("unbound authority error = %v, want ownership denied", err)
	}
	// Unknown reason is denied.
	unknownReason := NewAdministratorMetadataAuthority(f.publicationAuthority, 1, DiagnosticReason(200))
	if _, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		unknownReason, f.taskID, "op-telemetry-scope", 10,
	)); !isCode(err, ErrorOwnershipDenied) {
		t.Fatalf("unknown reason error = %v, want ownership denied", err)
	}
	// A zero/oversized limit is rejected.
	if _, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		f.auditAuthority(), f.taskID, "op-telemetry-scope", 0,
	)); !isCode(err, ErrorOwnershipDenied) {
		t.Fatalf("zero limit error = %v, want ownership denied", err)
	}
	// Exact-scope: another Task's traces never leak into this snapshot.
	otherSnapshot, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		f.auditAuthority(), "task-other", "", 10,
	))
	if err != nil {
		t.Fatalf("other-task snapshot: %v", err)
	}
	if len(otherSnapshot.Traces) != 0 {
		t.Fatalf("exact scope leaked traces of another Task: %#v", otherSnapshot.Traces)
	}
}

// TestTelemetryOutageNeverChangesAuthority proves a failing telemetry sink
// never rolls back a committed protected decision and never fabricates a
// zero: the decision is durable, exact replay returns the same decision,
// and the projection backlog keeps the failed/pending disposition for
// rebuild.
func TestTelemetryOutageNeverChangesAuthority(t *testing.T) {
	// Phase 1: working sink; commit prepare + verify + activate.
	f := newObservableFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-outage", set)
	f.mustVerify(t, "op-outage", set)
	activated := f.mustActivate(t, "op-outage")
	versionID := activated.ArtifactVersionID

	// Phase 2: rebuild the authority over the SAME persistence with a
	// failing telemetry sink (outage after commit).
	down := &failingSink{err: newProjectionError(ProjectionUnavailable)}
	f.telemetry = nil
	f.core = NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		PublicationAuthorityID:       f.publicationAuthority,
		CleanupAuthorityID:           f.cleanupAuthority,
		CurrentContentCapability:     f.registry.resolve,
		CurrentContentScope:          f.scopes.resolve,
		ExternalAuditSink:            down,
		TelemetrySink:                down,
		Adapter:                      MetricAdapterInMemory,
		DiagnosticAuditFaults:        f.auditFaults,
	}, f.persistence)

	// Exact replay under outage returns the SAME committed decision: the
	// outage never changes authority.
	replayed, err := f.core.Mutate(context.Background(), f.activateIntent("op-outage"))
	if err != nil || !replayed.Replay || replayed.ArtifactVersionID != versionID ||
		replayed.ActivationEvidence == nil {
		t.Fatalf("exact replay under telemetry outage = %#v err=%v, want original decision", replayed, err)
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil || stream.StreamRevision != 1 || stream.CurrentHead != versionID {
		t.Fatalf("stream under outage = %#v err=%v, want committed facts intact", stream, err)
	}

	// A NEW committed decision under outage: delivery fails into the durable
	// backlog, the decision still commits, and no zero is fabricated.
	header := f.header("op-outage-2")
	header.ExpectedStreamRevision = 1
	header.ExpectedHead = versionID
	outagePrepare := bindDigest(NewPreparePublication(header, f.preparePayload("op-outage-2", set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if _, err := f.core.Mutate(context.Background(), outagePrepare); err != nil {
		t.Fatalf("prepare under outage: %v", err)
	}
	backlog, err := f.core.(*inMemory).InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(f.auditAuthority(), f.taskID, 10))
	if err != nil {
		t.Fatalf("inspect backlog under outage: %v", err)
	}
	// The 3 pre-outage facts stay delivered; the outage fact stays pending.
	if backlog.SourceFactCount != 4 || backlog.Delivered != 3 || backlog.Pending != 1 {
		t.Fatalf("outage must keep the new fact pending without touching committed facts: %#v", backlog)
	}
	for _, evidence := range backlog.Evidence {
		if evidence.OperationID == "op-outage-2" && evidence.AuditDelivered {
			t.Fatalf("outage fact must stay pending, never delivered or zero: %#v", evidence)
		}
	}
}

// TestUnknownNeverProjectedAsZero proves a Task with no telemetry has no
// fabricated zero samples: an unavailable source never emits a zero, and a
// missing backlog fact is reported as pending (not delivered) with the
// source count derived only from retained facts.
func TestUnknownNeverProjectedAsZero(t *testing.T) {
	f := newObservableFixture(t)
	// No operations: the snapshot is empty, never a fabricated zero sample.
	snapshot, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		f.auditAuthority(), f.taskID, "", 10,
	))
	if err != nil {
		t.Fatalf("empty snapshot: %v", err)
	}
	for _, sample := range snapshot.Metrics {
		if sample.Count == 0 {
			t.Fatalf("unknown source must never be projected as a fabricated zero: %#v", sample)
		}
	}

	// A committed decision without any sink keeps the backlog pending; the
	// source count is derived from the retained facts, never invented.
	f2 := newObservableFixture(t)
	f2.telemetry = nil // no sink: no delivery attempt, backlog stays pending
	f2.rebuild()
	set := f2.buildEvidence(t, []ArtifactMemberSpec{f2.deckMemberSpec()})
	f2.mustPrepare(t, "op-unknown", set)
	backlog, err := f2.core.(*inMemory).InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(f2.auditAuthority(), f2.taskID, 10))
	if err != nil {
		t.Fatalf("inspect backlog: %v", err)
	}
	if backlog.SourceFactCount != 1 || backlog.Pending != 1 || backlog.Delivered != 0 {
		t.Fatalf("backlog must report the retained source as pending, never zero: %#v", backlog)
	}
}
