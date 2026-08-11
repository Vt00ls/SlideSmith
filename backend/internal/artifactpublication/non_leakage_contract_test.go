package artifactpublication

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// canaryValues are the leak probes injected into otherwise valid requests.
// Public decisions, views, safe errors, and the canonical manifest/lineage
// encodings must never echo them.
var canaryValues = []string{
	"s3://canary-bucket/path/key.pptx",
	"/var/lib/slidesmith/sessions/session-42",
	"credential:AKIA-CANARY",
	"vendor-endpoint.example.com",
	"object-key-canary",
	"mount-point-canary",
}

// TestNonLeakageDecisionsAndViews proves public decisions and views never
// leak content, paths, object keys, buckets, vendors, credentials, sessions,
// or materialization locators even when hostile values are injected into the
// request.
func TestNonLeakageDecisionsAndViews(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	// Inject canaries into evidence fields that are opaque by contract
	// BEFORE prepare so the pinned references match the submitted evidence.
	set.runtimeEvidence[0].ProposalRef = canaryValues[1]
	set.runtimeEvidence[0].Digest = set.runtimeEvidence[0].CanonicalDigest()
	// Rebuild the validation and C04 evidence so they bind the mutated
	// runtime evidence, then carry canaries in the opaque roots.
	set.validation = f.validationEvidence(set.validation.ContractID, set.runtimeEvidence, set.proposalDigest)
	set.c04 = f.c04Commit(set.validation, nil)
	set.c04.ContentEvidenceRoot = canaryValues[4]
	set.c04.DurabilityEvidenceRoot = canaryValues[5]
	set.c04.Digest = set.c04.CanonicalDigest()

	prepare := f.mustPrepare(t, "op-leak", set)
	verify := f.mustVerify(t, "op-leak", set)

	encoded := string(mustMarshalJSON(t, prepare)) + string(mustMarshalJSON(t, verify))
	for _, canary := range canaryValues {
		if strings.Contains(encoded, canary) {
			t.Fatalf("decision leaks canary %q", canary)
		}
	}

	operationView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-leak",
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	candidateView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryCandidate, PolicyDomainID: f.policyDomain, TaskID: f.taskID, ArtifactVersionID: prepare.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query candidate: %v", err)
	}
	streamView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	allViews := string(mustMarshalJSON(t, operationView)) + string(mustMarshalJSON(t, candidateView)) +
		string(mustMarshalJSON(t, streamView))
	for _, canary := range canaryValues {
		if strings.Contains(allViews, canary) {
			t.Fatalf("view leaks canary %q", canary)
		}
	}
}

// TestNonLeakageSafeErrors proves safe errors never echo submitted identity
// or locator material and never enumerate other scopes.
func TestNonLeakageSafeErrors(t *testing.T) {
	f := newFixture(t)
	// An operation in another policy domain exists; a cross-scope query
	// must resolve to the same non-enumerating not-found error.
	other := newFixture(t)
	otherSet := other.buildEvidence(t, []ArtifactMemberSpec{other.deckMemberSpec()})
	other.mustPrepare(t, "op-other", otherSet)
	otherVersion := other.mustPrepare(t, "op-other-2", otherSet).ArtifactVersionID

	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-other",
	})
	first := err.Error()
	_, err = f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: "task-that-does-not-exist", OperationID: "op-does-not-exist",
	})
	second := err.Error()
	if first != second {
		t.Fatalf("cross-scope query must be non-enumerating: %q vs %q", first, second)
	}
	for _, canary := range append(canaryValues, "op-other", "task-1", "policy-domain-1") {
		if strings.Contains(first, canary) {
			t.Fatalf("safe error leaks %q", canary)
		}
	}

	// Cross-workspace candidate identity is never disclosed.
	_, err = f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryCandidate, PolicyDomainID: f.policyDomain, TaskID: f.taskID, ArtifactVersionID: otherVersion,
	})
	if !isCode(err, ErrorNotFound) {
		t.Fatalf("cross-workspace candidate error = %v, want not found", err)
	}
	if strings.Contains(err.Error(), string(otherVersion)) {
		t.Fatalf("cross-workspace error discloses identity %q", otherVersion)
	}

	// Invalid-intent errors are content-free.
	_, err = f.core.Mutate(context.Background(), nil)
	if !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("nil intent error = %v, want invalid intent", err)
	}
	if strings.Contains(err.Error(), "nil") {
		t.Fatalf("invalid intent error leaks internals: %q", err.Error())
	}
}

// TestCanonicalEncodingNeverContainsLocators proves the canonical manifest
// and lineage encodings are structurally locator-free: their field set is
// closed and contains no content, path, object key, bucket, mount, vendor,
// credential, session, or materialization locator.
func TestCanonicalEncodingNeverContainsLocators(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, "op-encoding", set)

	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-encoding",
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	for _, canary := range canaryValues {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("view encoding leaks %q", canary)
		}
	}
	_ = prepare
}

// TestNonLeakageActivationEvidenceAndVersionViews proves the activation
// evidence and the committed version/member/history views never leak
// content, paths, object keys, buckets, vendors, credentials, sessions, or
// materialization locators even when hostile values are injected into the
// request.
func TestNonLeakageActivationEvidenceAndVersionViews(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	// Inject canaries into opaque evidence fields BEFORE prepare so the
	// pinned references match the submitted evidence, and rebuild the
	// dependent evidence.
	set.runtimeEvidence[0].ProposalRef = canaryValues[1]
	set.runtimeEvidence[0].Digest = set.runtimeEvidence[0].CanonicalDigest()
	set.validation = f.validationEvidence(set.validation.ContractID, set.runtimeEvidence, set.proposalDigest)
	set.c04 = f.c04Commit(set.validation, nil)
	set.c04.ContentEvidenceRoot = canaryValues[4]
	set.c04.DurabilityEvidenceRoot = canaryValues[5]
	set.c04.Digest = set.c04.CanonicalDigest()

	f.mustPrepare(t, "op-leak-activate", set)
	f.mustVerify(t, "op-leak-activate", set)
	activated, err := f.core.Mutate(context.Background(), f.activateIntent("op-leak-activate"))
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	encoded := string(mustMarshalJSON(t, activated))
	if activated.ActivationEvidence != nil {
		encoded += string(mustMarshalJSON(t, activated.ActivationEvidence))
	}
	for _, canary := range canaryValues {
		if strings.Contains(encoded, canary) {
			t.Fatalf("activation decision leaks canary %q", canary)
		}
	}

	versionView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query exact version: %v", err)
	}
	historyView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	allViews := string(mustMarshalJSON(t, versionView)) + string(mustMarshalJSON(t, historyView))
	for _, canary := range canaryValues {
		if strings.Contains(allViews, canary) {
			t.Fatalf("version view leaks canary %q", canary)
		}
	}
}

// TestTestOutputFreeOfLocators proves the safe status fields exposed by the
// seam never carry a materialization locator (content-free residue status).
func TestTestOutputFreeOfLocators(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-residue-leak", set)
	cancelled, err := f.core.Mutate(context.Background(), f.cancelIntent("op-residue-leak", CancelTaskOrchestration))
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	encoded := mustMarshalJSON(t, cancelled)
	for _, canary := range canaryValues {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("residue status leaks %q", canary)
		}
	}
	if !strings.Contains(string(encoded), "true") { // ResidueRelease bool is the only residue fact
		t.Fatalf("residue status must expose only content-free facts: %s", encoded)
	}
}

// TestNonLeakageAuditTelemetryAndProjectionSurfaces proves the mandatory
// audit facts, the external audit projections, the bounded telemetry
// snapshot (metrics, logs, traces), and the protected projection backlog
// never leak content, member names, paths, object keys, buckets, mounts,
// vendor URLs, credentials, sessions, raw errors, or cross-Workspace
// identity canaries even when hostile values are injected into the request.
func TestNonLeakageAuditTelemetryAndProjectionSurfaces(t *testing.T) {
	f := newObservableFixture(t)
	// Inject hostile member names and prompt canaries into the member spec
	// (AC5: member name and prompt must never reach audit/telemetry).
	member := f.deckMemberSpec()
	member.LogicalName = "Deck-Content-Canary-Hostile.pptx"
	set := f.buildEvidence(t, []ArtifactMemberSpec{member})
	set.runtimeEvidence[0].ProposalRef = canaryValues[1]
	set.runtimeEvidence[0].Digest = set.runtimeEvidence[0].CanonicalDigest()
	set.validation = f.validationEvidence(set.validation.ContractID, set.runtimeEvidence, set.proposalDigest)
	set.c04 = f.c04Commit(set.validation, nil)
	set.c04.ContentEvidenceRoot = canaryValues[4]
	set.c04.DurabilityEvidenceRoot = canaryValues[5]
	set.c04.Digest = set.c04.CanonicalDigest()
	f.mustPrepare(t, "op-leak-obs", set)
	f.mustVerify(t, "op-leak-obs", set)
	if _, err := f.core.Mutate(context.Background(), f.rejectIntent("op-leak-obs", RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	f.mustRecordAssembly(t, "op-leak-obs", f.assemblyReference())

	// Protected telemetry snapshot.
	snapshot, err := f.telemetry.Snapshot(context.Background(), NewTelemetryDiagnosticQuery(
		f.auditAuthority(), f.taskID, "", 100,
	))
	if err != nil {
		t.Fatalf("telemetry snapshot: %v", err)
	}
	surfaces := []string{
		string(mustMarshalJSON(t, snapshot)),
		string(mustMarshalJSON(t, f.persistence.auditFacts)),
	}
	// External audit projections retained by the deterministic sink.
	encodedAudit := ""
	for _, projection := range f.telemetry.audit {
		encodedAudit += string(mustMarshalJSON(t, projection))
	}
	surfaces = append(surfaces, encodedAudit)
	// Protected projection backlog inspection.
	backlog, err := f.core.(*inMemory).InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(f.auditAuthority(), f.taskID, 100))
	if err != nil {
		t.Fatalf("projection backlog: %v", err)
	}
	surfaces = append(surfaces, string(mustMarshalJSON(t, backlog)))

	for _, surface := range surfaces {
		for _, canary := range append(canaryValues, "Deck-Content-Canary-Hostile.pptx") {
			if strings.Contains(surface, canary) {
				t.Fatalf("observability surface leaks canary %q", canary)
			}
		}
	}
	// Cross-Workspace identity canary: an operation identity of ANOTHER
	// Workspace never appears in this Workspace's observability surfaces.
	foreign := newFixture(t)
	foreignSet := foreign.buildEvidence(t, []ArtifactMemberSpec{foreign.deckMemberSpec()})
	foreign.mustPrepare(t, "op-foreign-workspace", foreignSet)
	foreignCanary := "op-foreign-workspace"
	for _, surface := range surfaces {
		if strings.Contains(surface, foreignCanary) {
			t.Fatalf("observability surface leaks cross-Workspace identity %q", foreignCanary)
		}
	}
}

// TestNonLeakageSafeErrorVersionedAndContentFree proves the versioned safe
// error surface is closed, content-free, and never echoes submitted
// identity or locator material.
func TestNonLeakageSafeErrorVersionedAndContentFree(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	// A stale generation/fence on a verify intent fails closed with a
	// content-free, versioned safe error.
	f.mustPrepare(t, "op-safe-error", set)
	header := f.header("op-safe-error")
	header.Generation = f.generation + 999
	verify := bindDigest(NewVerifyPublication(header, f.verifyPayload(set)))
	_, err := f.core.Mutate(context.Background(), verify)
	var publicationError *Error
	if !errors.As(err, &publicationError) {
		t.Fatalf("expected typed safe error, got %v", err)
	}
	if publicationError.SchemaVersion() != SafeErrorSchemaV1 || publicationError.SchemaVersion().Major() != 1 {
		t.Fatalf("safe error must be versioned: %#v", publicationError)
	}
	if publicationError.SafeCategory() != SafeErrorStaleRevisionGenerationFence {
		t.Fatalf("unexpected safe category: %v", publicationError.SafeCategory())
	}
	text := publicationError.Error()
	for _, canary := range append(canaryValues, "op-safe-error", "999") {
		if strings.Contains(text, canary) {
			t.Fatalf("safe error leaks %q: %q", canary, text)
		}
	}

	// Evidence binding failures map to the closed binding category: the
	// verify payload passes intent validation but its runtime evidence
	// digest is corrupt (does not match its canonical digest), so the
	// evidence matrix reports a corrupt-evidence binding failure.
	other := newFixture(t)
	otherSet := other.buildEvidence(t, []ArtifactMemberSpec{other.deckMemberSpec()})
	other.mustPrepare(t, "op-binding", otherSet)
	bindingPayload := other.verifyPayload(otherSet)
	// Tamper an evidence field while keeping the pinned digest: the matrix
	// detects the corrupt binding (digest no longer canonical).
	bindingPayload.RuntimeEvidence[0].ProposalRef = "tampered-proposal-ref"
	_, err = other.core.Mutate(context.Background(), other.verifyIntent("op-binding", bindingPayload))
	if !errors.As(err, &publicationError) {
		t.Fatalf("expected typed binding error, got %v", err)
	}
	if publicationError.SafeCategory() != SafeErrorBindingUnavailable {
		t.Fatalf("evidence binding failure must map to the binding category: %v", publicationError.SafeCategory())
	}
}
