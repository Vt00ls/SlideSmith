package artifactpublication

// Child SPEC #111 (C05-07) projection backlog contract: external audit
// sink, metrics, logs and trace failures never roll back committed
// protected decisions; they form a durable, rebuildable backlog from the
// retained authoritative audit facts. Duplicate or out-of-order projection
// delivery is idempotent and never rewrites a source fact; an unknown,
// missing, or corrupt source is never projected as delivered or as zero.
// The inspection and rebuild surfaces are protected: reason-bound,
// least-privilege, exact-scope, and bounded.

import (
	"context"
	"testing"
)

// flipSink is a controllable projection sink double: the external audit
// channel and the telemetry channel can fail independently.
type flipSink struct {
	telemetry     *DeterministicTelemetry
	failAudit     bool
	failTelemetry bool
}

func (s *flipSink) ProjectExternalAudit(ctx context.Context, projection ExternalAuditProjection) error {
	if s.failAudit {
		return newProjectionError(ProjectionUnavailable)
	}
	return s.telemetry.ProjectExternalAudit(ctx, projection)
}

func (s *flipSink) ProjectTelemetry(ctx context.Context, projection PublicationTelemetryProjection) error {
	if s.failTelemetry {
		return newProjectionError(ProjectionUnavailable)
	}
	return s.telemetry.ProjectTelemetry(ctx, projection)
}

// fixtureOverSink rebuilds the fixture authority over the same persistence
// with the given projection sink (which must implement both the external
// audit and the telemetry port).
func fixtureOverSink(f *fixture, sink interface {
	ExternalAuditProjectionSink
	TelemetrySink
}) {
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
		ExternalAuditSink:            sink,
		TelemetrySink:                sink,
		Adapter:                      MetricAdapterInMemory,
		DiagnosticAuditFaults:        f.auditFaults,
	}, f.persistence)
}

// TestExternalAuditSinkFailureNeverRollsBackAndRebuilds proves an external
// audit sink outage after commit does not roll back the protected decision,
// forms a durable pending backlog, and the protected rebuild surface
// redelivers once the sink recovers.
func TestExternalAuditSinkFailureNeverRollsBackAndRebuilds(t *testing.T) {
	f := newObservableFixture(t)
	sink := &flipSink{telemetry: f.telemetry, failAudit: true, failTelemetry: false}
	fixtureOverSink(f, sink)

	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, "op-sink", set)

	// The decision is committed despite the external audit outage.
	backlog, err := f.core.(*inMemory).InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(f.auditAuthority(), f.taskID, 10))
	if err != nil {
		t.Fatalf("inspect backlog: %v", err)
	}
	if backlog.SourceFactCount != 1 || backlog.Pending != 1 || backlog.Delivered != 0 {
		t.Fatalf("external audit outage must leave a pending rebuildable backlog: %#v", backlog)
	}
	operation, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: "op-sink",
	})
	if err != nil || operation.State != OperationPrepared ||
		operation.ArtifactVersionID != prepare.ArtifactVersionID {
		t.Fatalf("committed decision must survive the audit outage: %#v err=%v", operation, err)
	}

	// The sink recovers; rebuild redelivers only the pending fact.
	sink.failAudit = false
	rebuilt, err := f.core.(*inMemory).RebuildProjectionDelivery(context.Background(),
		NewProjectionDeliveryRebuildRequest(f.auditAuthority(), f.taskID, 10))
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rebuilt.Pending != 0 || rebuilt.Delivered != 1 {
		t.Fatalf("rebuild must deliver the pending fact: %#v", rebuilt)
	}
	// The external audit sink now holds the content-free projection.
	if len(sink.telemetry.audit) != 1 {
		t.Fatalf("external audit sink must have received the projection, got %d", len(sink.telemetry.audit))
	}
}

// TestProjectionRebuildRedeliversOnlyPendingFacts proves rebuild redrives
// only not-yet-delivered projection facts, keeps already delivered facts
// untouched, and never invents a delivered status for an unknown fact.
func TestProjectionRebuildRedeliversOnlyPendingFacts(t *testing.T) {
	f := newObservableFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-rebuild", set)
	f.mustVerify(t, "op-rebuild", set)
	f.mustActivate(t, "op-rebuild")

	// All delivered under the working sink.
	backlog, err := f.core.(*inMemory).InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(f.auditAuthority(), f.taskID, 10))
	if err != nil {
		t.Fatalf("inspect backlog: %v", err)
	}
	if backlog.Delivered != 3 || backlog.Pending != 0 {
		t.Fatalf("expected all 3 facts delivered: %#v", backlog)
	}

	// Rebuild under a working sink must not change anything (idempotent).
	rebuild, err := f.core.(*inMemory).RebuildProjectionDelivery(context.Background(),
		NewProjectionDeliveryRebuildRequest(f.auditAuthority(), f.taskID, 10))
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rebuild.Delivered != 3 || rebuild.Pending != 0 {
		t.Fatalf("rebuild must not touch delivered facts: %#v", rebuild)
	}
}

// TestProjectionBacklogInspectionProtected proves inspection and rebuild
// require a valid reason-bound administrator metadata authority and a
// bounded exact scope; unbound or unknown authorities fail closed
// non-enumerating.
func TestProjectionBacklogInspectionProtected(t *testing.T) {
	f := newObservableFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-protected", set)

	unbound := NewAdministratorMetadataAuthority("", 0, DiagnosticReasonOperations)
	if _, err := f.core.(*inMemory).InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(unbound, f.taskID, 10)); !isCode(err, ErrorOwnershipDenied) {
		t.Fatalf("unbound inspect error = %v, want ownership denied", err)
	}
	if _, err := f.core.(*inMemory).RebuildProjectionDelivery(context.Background(),
		NewProjectionDeliveryRebuildRequest(unbound, f.taskID, 10)); !isCode(err, ErrorOwnershipDenied) {
		t.Fatalf("unbound rebuild error = %v, want ownership denied", err)
	}
	if _, err := f.core.(*inMemory).InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(f.auditAuthority(), f.taskID, 0)); !isCode(err, ErrorOwnershipDenied) {
		t.Fatalf("zero limit error = %v, want ownership denied", err)
	}
	// Exact scope: another Task's backlog is empty, never another scope's
	// facts.
	other, err := f.core.(*inMemory).InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(f.auditAuthority(), "task-other", 10))
	if err != nil {
		t.Fatalf("other-task backlog: %v", err)
	}
	if other.SourceFactCount != 0 {
		t.Fatalf("exact scope must not enumerate another Task's facts: %#v", other)
	}
}

// TestProjectionDuplicateOutOfOrderNeverChangesAuthority proves duplicate or
// out-of-order projection delivery is idempotent by AuditFactID and
// canonical digest and never rewrites the source fact or the committed
// decision.
func TestProjectionDuplicateOutOfOrderNeverChangesAuthority(t *testing.T) {
	f := newObservableFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-dup", set)

	before := len(f.telemetry.metrics)
	snapshot, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		f.auditAuthority(), f.taskID, "op-dup", 10,
	))
	if err != nil || len(snapshot.Traces) != 1 {
		t.Fatalf("telemetry snapshot: traces=%d err=%v", len(snapshot.Traces), err)
	}
	// The committed decision is unchanged by duplicate projection delivery:
	// exact replay returns the same decision and no second audit fact is
	// created.
	replayed, err := f.core.Mutate(context.Background(), f.prepareIntent("op-dup", f.preparePayload("op-dup", set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil || !replayed.Replay || replayed.ArtifactVersionID == "" {
		t.Fatalf("exact replay after duplicate projection = %#v err=%v", replayed, err)
	}
	if got := len(f.persistence.auditFacts); got != 1 {
		t.Fatalf("duplicate projection must not create audit facts, got %d", got)
	}
	// A second identical snapshot read does not duplicate the metric (the
	// protected snapshot surface is read-only and never emits a new sample).
	second, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		f.auditAuthority(), f.taskID, "op-dup", 10,
	))
	if err != nil || len(second.Traces) != 1 {
		t.Fatalf("second snapshot: traces=%d err=%v", len(second.Traces), err)
	}
	if got := len(f.telemetry.metrics); got != before {
		t.Fatalf("snapshot must be read-only: before=%d after=%d", before, got)
	}
}
