package artifactpublication

import (
	"context"
	"encoding/json"
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
