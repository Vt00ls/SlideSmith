package artifactpublication

// Child SPEC #111 (C05-07) mandatory audit contract: every protected
// publication decision (prepare, verify, activate, reject, cancel,
// reconcile, record residue assembly, release residue, resolve cleanup
// debt) commits one canonical, content-free, versioned mandatory audit fact
// that is correlatable through the exact operation identity and request
// digest. The audit fact is retained with the authoritative state (it
// survives restart), is verified through the protected projection backlog
// surface, and never carries content, member names, paths, object keys,
// buckets, mounts, vendor URLs, credentials, sessions, or cross-Workspace
// identity canaries.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// auditFactsFor returns the retained canonical audit facts of one exact
// operation in submission order.
func auditFactsFor(f *fixture, operationID string) []PublicationAuditFact {
	facts := make([]PublicationAuditFact, 0)
	for _, fact := range f.persistence.auditFacts {
		if fact.OperationID == PublicationOperationID(operationID) {
			facts = append(facts, fact)
		}
	}
	return facts
}

// TestMandatoryAuditFactsForEveryProtectedDecision proves every protected
// mutation kind commits one canonical audit fact binding the exact
// operation identity, request digest, closed action, typed authority and
// committed stream facts.
func TestMandatoryAuditFactsForEveryProtectedDecision(t *testing.T) {
	// activate: prepare + verify + activate on a fresh stream
	activateFixture := newFixture(t)
	activateFixture.prepareAndVerify(t, "op-audit-activate")
	activateFixture.mustActivate(t, "op-audit-activate")
	activationFacts := auditFactsFor(activateFixture, "op-audit-activate")
	if len(activationFacts) != 3 {
		t.Fatalf("activation operation audit facts = %d, want 3 (prepare/verify/activate)", len(activationFacts))
	}
	for index, wantAction := range []PublicationIntentKind{
		IntentPreparePublication, IntentVerifyPublication, IntentActivatePublication,
	} {
		fact := activationFacts[index]
		if fact.Action != wantAction || fact.OperationID != "op-audit-activate" ||
			fact.RequestDigest == "" || fact.AuthorityKind != AuthorityTaskOrchestration ||
			fact.State == "" || !validPublicationAuditFact(fact) {
			t.Fatalf("unexpected audit fact %d: %#v", index, fact)
		}
	}
	if activationFacts[2].State != OperationActivated || activationFacts[2].StreamRevision != 1 {
		t.Fatalf("activation audit fact must bind committed stream facts: %#v", activationFacts[2])
	}

	// reject: prepare + verify + reject on a fresh stream
	rejectFixture := newFixture(t)
	rejectSet := rejectFixture.buildEvidence(t, []ArtifactMemberSpec{rejectFixture.deckMemberSpec()})
	rejectFixture.mustPrepare(t, "op-audit-reject", rejectSet)
	rejectFixture.mustVerify(t, "op-audit-reject", rejectSet)
	if _, err := rejectFixture.core.Mutate(context.Background(), rejectFixture.rejectIntent("op-audit-reject", RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	rejectFacts := auditFactsFor(rejectFixture, "op-audit-reject")
	if len(rejectFacts) != 3 || rejectFacts[2].Action != IntentRejectPublication ||
		rejectFacts[2].State != OperationRejected || !validPublicationAuditFact(rejectFacts[2]) {
		t.Fatalf("unexpected reject audit facts: %#v", rejectFacts)
	}

	// cancel: prepare + cancel on a fresh stream
	cancelFixture := newFixture(t)
	cancelSet := cancelFixture.buildEvidence(t, []ArtifactMemberSpec{cancelFixture.deckMemberSpec()})
	cancelFixture.mustPrepare(t, "op-audit-cancel", cancelSet)
	if _, err := cancelFixture.core.Mutate(context.Background(), cancelFixture.cancelIntent("op-audit-cancel", CancelTaskOrchestration)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancelFacts := auditFactsFor(cancelFixture, "op-audit-cancel")
	if len(cancelFacts) != 2 || cancelFacts[1].Action != IntentCancelPublication ||
		cancelFacts[1].State != OperationCancelled || !validPublicationAuditFact(cancelFacts[1]) {
		t.Fatalf("unexpected cancel audit facts: %#v", cancelFacts)
	}

	// reconcile (inspect): prepare + reconcile on a fresh stream
	reconcileFixture := newFixture(t)
	reconcileSet := reconcileFixture.buildEvidence(t, []ArtifactMemberSpec{reconcileFixture.deckMemberSpec()})
	reconcileFixture.mustPrepare(t, "op-audit-reconcile", reconcileSet)
	if _, err := reconcileFixture.core.Mutate(context.Background(), reconcileFixture.reconcileIntent("op-audit-reconcile", ReconcileInspect)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	reconcileFacts := auditFactsFor(reconcileFixture, "op-audit-reconcile")
	if len(reconcileFacts) != 2 || reconcileFacts[1].Action != IntentReconcilePublication ||
		reconcileFacts[1].State != OperationPrepared || !validPublicationAuditFact(reconcileFacts[1]) {
		t.Fatalf("unexpected reconcile audit facts: %#v", reconcileFacts)
	}

	// protected cleanup resolution: record assembly, release, resolve debt
	assemblyFixture := newFixture(t)
	assemblyOp := "op-audit-assembly"
	set := assemblyFixture.buildEvidence(t, []ArtifactMemberSpec{assemblyFixture.deckMemberSpec()})
	assemblyFixture.mustPrepare(t, assemblyOp, set)
	assemblyFixture.mustVerify(t, assemblyOp, set)
	if _, err := assemblyFixture.core.Mutate(context.Background(), assemblyFixture.rejectIntent(assemblyOp, RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject assembly op: %v", err)
	}
	assemblyFixture.mustRecordAssembly(t, assemblyOp, assemblyFixture.assemblyReference())
	// Release without a release port stays reconciliation-required but is
	// still an audited protected decision.
	if _, err := assemblyFixture.core.Mutate(context.Background(), assemblyFixture.releaseResidueIntent(assemblyOp)); err == nil {
		t.Fatal("release without release port must fail closed as reconciliation-required")
	}
	assemblyFacts := auditFactsFor(assemblyFixture, assemblyOp)
	actions := make([]PublicationIntentKind, 0, len(assemblyFacts))
	for _, fact := range assemblyFacts {
		actions = append(actions, fact.Action)
		if !validPublicationAuditFact(fact) {
			t.Fatalf("invalid assembly audit fact: %#v", fact)
		}
	}
	joined := strings.Join(actionsToStrings(actions), ",")
	if !strings.Contains(joined, string(IntentRecordResidueAssembly)) ||
		!strings.Contains(joined, string(IntentReleaseResidue)) {
		t.Fatalf("protected cleanup decisions missing audit facts: %v", joined)
	}
}

func actionsToStrings(actions []PublicationIntentKind) []string {
	out := make([]string, len(actions))
	for index, action := range actions {
		out[index] = string(action)
	}
	return out
}

// mustRecordAssembly records the C05-owned assembly obligation and returns
// the debt identity (fixture helper for in-memory tests).
func (f *fixture) mustRecordAssembly(t *testing.T, operationID string, assembly AssemblyReference) CleanupDebtID {
	t.Helper()
	decision, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly))
	if err != nil {
		t.Fatalf("record assembly: %v", err)
	}
	if decision.CleanupDebtID == "" {
		t.Fatal("record assembly returned no debt id")
	}
	return decision.CleanupDebtID
}

// TestMandatoryAuditFactsCorrelateExactOperation proves the retained audit
// facts of an operation are correlatable end to end: the same opaque
// operation identity and policy facts appear in the operation record, the
// activation evidence, and every audit fact; the prepare fact binds the
// operation's canonical request digest.
func TestMandatoryAuditFactsCorrelateExactOperation(t *testing.T) {
	f := newFixture(t)
	operationID := "op-audit-correlate"
	set, prepare, _ := f.prepareAndVerify(t, operationID)
	f.mustActivate(t, operationID)

	facts := auditFactsFor(f, operationID)
	if len(facts) != 3 {
		t.Fatalf("audit facts = %d, want 3", len(facts))
	}
	for _, fact := range facts {
		if fact.OperationID != PublicationOperationID(operationID) ||
			fact.PolicyDomainID != f.policyDomain || fact.TaskID != f.taskID {
			t.Fatalf("audit fact not correlated to the operation: %#v", fact)
		}
	}
	// The prepare fact binds the operation's canonical request digest; every
	// later fact binds the exact intent's own canonical digest while
	// sharing the operation identity (retries never change the operation).
	if facts[0].RequestDigest != prepare.Operation.RequestDigest {
		t.Fatalf("prepare audit fact must bind the operation request digest: %#v", facts[0])
	}
	if facts[1].RequestDigest == facts[0].RequestDigest {
		t.Fatal("verify audit fact must bind its own intent digest")
	}
	if facts[2].VersionID != prepare.ArtifactVersionID ||
		facts[2].ManifestDigest != prepare.ManifestDigest {
		t.Fatalf("activation audit fact must bind the committed version: %#v", facts[2])
	}
	_ = set
}

// TestMandatoryAuditFactsSurviveRestart proves the canonical audit facts
// are authoritative and retained: a fresh authority over the same
// persistence resumes them unchanged (child SPEC #111 acceptance #1).
func TestMandatoryAuditFactsSurviveRestart(t *testing.T) {
	f := newFixture(t)
	f.prepareAndVerify(t, "op-audit-restart")
	f.mustActivate(t, "op-audit-restart")
	before := auditFactsFor(f, "op-audit-restart")
	if len(before) != 3 {
		t.Fatalf("audit facts before restart = %d, want 3", len(before))
	}
	f.rebuild()
	after := auditFactsFor(f, "op-audit-restart")
	if len(after) != len(before) {
		t.Fatalf("audit facts after restart = %d, want %d", len(after), len(before))
	}
	for index := range before {
		if before[index].AuditFactID != after[index].AuditFactID ||
			before[index].CanonicalDigest != after[index].CanonicalDigest ||
			before[index].Action != after[index].Action {
			t.Fatalf("audit fact %d changed across restart: %#v vs %#v", index, before[index], after[index])
		}
	}
}

// TestMandatoryAuditFactsContentFree proves the canonical audit fact is
// content-free even when hostile canaries are injected into the submitted
// evidence: no content, member name, path, object key, bucket, mount,
// vendor URL, credential, session, or locator ever reaches the audit fact.
func TestMandatoryAuditFactsContentFree(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	// Inject canaries into the opaque evidence fields BEFORE prepare so the
	// pinned references match the submitted evidence.
	set.runtimeEvidence[0].ProposalRef = canaryValues[1]
	set.runtimeEvidence[0].Digest = set.runtimeEvidence[0].CanonicalDigest()
	set.validation = f.validationEvidence(set.validation.ContractID, set.runtimeEvidence, set.proposalDigest)
	set.c04 = f.c04Commit(set.validation, nil)
	set.c04.ContentEvidenceRoot = canaryValues[4]
	set.c04.DurabilityEvidenceRoot = canaryValues[5]
	set.c04.Digest = set.c04.CanonicalDigest()
	f.mustPrepare(t, "op-audit-leak", set)
	f.mustVerify(t, "op-audit-leak", set)

	// Reject so the residue path and protected cleanup audit run too.
	if _, err := f.core.Mutate(context.Background(), f.rejectIntent("op-audit-leak", RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	f.mustRecordAssembly(t, "op-audit-leak", f.assemblyReference())

	for _, fact := range auditFactsFor(f, "op-audit-leak") {
		encoded, err := json.Marshal(fact)
		if err != nil {
			t.Fatalf("marshal audit fact: %v", err)
		}
		for _, canary := range canaryValues {
			if strings.Contains(string(encoded), canary) {
				t.Fatalf("audit fact leaks canary %q: %s", canary, encoded)
			}
		}
	}
}

// TestMandatoryAuditFailureRollsBackProtectedDecision (PostgreSQL) proves a
// mandatory audit canonicalization/persistence failure rolls back the whole
// protected decision all-or-none. The in-memory authority never has an
// audit persistence failure; the real PostgreSQL adapter is the fail-closed
// gate (also covered by TestPostgresActivationAllOrNoneOnFault).
func TestMandatoryAuditFailureRollsBackProtectedDecision(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-audit-rollback"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, operationID, set)
	f.mustVerify(t, operationID, set)

	// Inject the audit fault exactly before the activation audit row: the
	// whole activation transaction must roll back (no version, no head, no
	// outbox, no audit row).
	f.authority.faults = func(event PostgresFaultEvent) error {
		if event.Point == PostgresFaultBeforeMandatoryAudit &&
			event.IntentKind == IntentActivatePublication {
			return &Error{Code: ErrorIntegrityFailure}
		}
		return nil
	}
	if _, err := f.core.Mutate(context.Background(), f.activateIntent(operationID)); !isCode(err, ErrorIntegrityFailure) {
		t.Fatalf("activate under audit fault = %v, want integrity failure", err)
	}
	if got := f.countRows(t, "publication_activated"); got != 0 {
		t.Fatalf("activation must roll back under audit failure, activated rows = %d", got)
	}
	if got := f.countRows(t, "publication_audit"); got != 2 {
		t.Fatalf("audit rows = %d, want exactly the 2 committed prepare+verify facts", got)
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 0 || stream.CurrentHead != "" {
		t.Fatalf("audit failure must not advance the stream: %#v", stream)
	}
}
