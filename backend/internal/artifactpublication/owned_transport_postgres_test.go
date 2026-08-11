package artifactpublication

// Owned publication transport over real PostgreSQL (child SPEC #109 /
// C05-06). The identical transport contract that runs over the
// deterministic in-memory authority must hold when the receiving harness
// wraps the production-shaped PostgresAuthority: strict envelope binding,
// at-least-once duplicate exact delivery, same-OperationID/different-payload
// integrity conflict, response loss -> reconciliation-required with inspect/
// replay of the ORIGINAL OperationID, restart/response-loss replay with the
// persisted binding, and no second publication operation or Artifact
// Version.

import (
	"context"
	"testing"
)

// ownedTransportPostgresHarness wraps the real PostgreSQL authority in the
// deterministic transport harness with the fixture's controlled clock.
func ownedTransportPostgresHarness(t *testing.T, f *postgresFixture) *OwnedTransportHarness {
	t.Helper()
	return NewOwnedTransportHarness(OwnedTransportHarnessConfig{
		Core: f.authority,
		MachineAuthority: OwnedTransportMachineAuthority{
			ID: "task-orchestration-machine-1", Generation: 7, ExpiresAt: f.now + 10_000,
		},
		AuthenticationKey: []byte("owned-publication-transport-postgres-key"),
		Authorize: func(scope OwnedTransportAuthorityScope) bool {
			return scope.PolicyDomainID == f.policyDomain && scope.TaskID == f.taskID
		},
		Now: func() Instant { return f.now }, RequestLifetime: 50,
	})
}

// TestOwnedTransportPostgresFullPublicationLifecycle drives the full
// prepare -> verify -> activate -> query lifecycle through the owned
// transport against real PostgreSQL and proves the activation evidence
// binds the exact OperationID, generation/fence, safety epoch,
// ArtifactVersionID and manifest digest.
func TestOwnedTransportPostgresFullPublicationLifecycle(t *testing.T) {
	f := newPostgresFixture(t)
	harness := ownedTransportPostgresHarness(t, f)
	client := harness.Client()
	operationID := "pg-transport-lifecycle-1"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	prepare, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil || prepare.State != OperationPrepared || prepare.ArtifactVersionID == "" {
		t.Fatalf("postgres prepare through transport = %#v, err = %v", prepare, err)
	}
	verify, err := client.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil || verify.State != OperationVerified ||
		verify.ArtifactVersionID != prepare.ArtifactVersionID {
		t.Fatalf("postgres verify through transport = %#v, err = %v", verify, err)
	}
	activated, err := client.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || activated.ActivationEvidence == nil {
		t.Fatalf("postgres activate through transport = %#v, err = %v", activated, err)
	}
	evidence := activated.ActivationEvidence
	if evidence.OperationID != PublicationOperationID(operationID) ||
		evidence.ArtifactVersionID != prepare.ArtifactVersionID ||
		evidence.ManifestDigest != prepare.ManifestDigest ||
		evidence.Generation != f.generation || evidence.Fence != f.fence ||
		evidence.SafetyEpoch != f.safetyEpoch ||
		evidence.StreamRevision != 1 || evidence.CurrentHead != prepare.ArtifactVersionID {
		t.Fatalf("postgres activation evidence did not bind exact facts: %#v", evidence)
	}

	view, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: prepare.ArtifactVersionID,
	})
	if err != nil || view.ArtifactVersionID != prepare.ArtifactVersionID || len(view.Members) != 1 {
		t.Fatalf("postgres exact version query through transport = %#v, err = %v", view, err)
	}

	// Duplicate exact delivery of the activation returns the same decision
	// and never creates a second version.
	harness.FailNext(OwnedTransportDuplicateDelivery)
	duplicated, err := client.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || !assertOwnedTransportSameDecision(duplicated, activated) {
		t.Fatalf("postgres duplicate-delivered activation = %#v, err = %v", duplicated, err)
	}
	history, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil || len(history.History) != 1 || history.StreamRevision != 1 {
		t.Fatalf("postgres duplicate delivery must not create a second version: %#v, err = %v", history, err)
	}
}

// TestOwnedTransportPostgresRestartResponseLossReplay proves that a
// response-lost activation over real PostgreSQL can be inspected and
// exact-replayed through the ORIGINAL OperationID after an authority and
// adapter restart, without creating a second operation or Artifact Version
// and without re-allocating identity.
func TestOwnedTransportPostgresRestartResponseLossReplay(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-transport-restart-1"
	bindings := NewOwnedTransportBindingStore()

	config := func() OwnedTransportHarnessConfig {
		return OwnedTransportHarnessConfig{
			Core: f.authority,
			MachineAuthority: OwnedTransportMachineAuthority{
				ID: "task-orchestration-machine-1", Generation: 7, ExpiresAt: f.now + 10_000,
			},
			AuthenticationKey: []byte("owned-publication-transport-postgres-key"),
			Authorize: func(scope OwnedTransportAuthorityScope) bool {
				return scope.PolicyDomainID == f.policyDomain && scope.TaskID == f.taskID
			},
			Now: func() Instant { return f.now }, RequestLifetime: 50,
			BindingStore: bindings,
		}
	}
	first := NewOwnedTransportHarness(config())
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	if _, err := first.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))); err != nil {
		t.Fatalf("prepare through transport: %v", err)
	}
	if _, err := first.Client().Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
		t.Fatalf("verify through transport: %v", err)
	}

	first.FailNext(OwnedTransportResponseLoss)
	_, err := first.Client().Mutate(context.Background(), f.activateIntent(operationID))
	assertOwnedTransportErrorCode(t, err, ErrorReconciliationRequired)

	// The operation committed durably; inspect it through the same harness.
	inspection, err := first.Client().Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: PublicationOperationID(operationID),
	})
	if err != nil || inspection.State != OperationActivated || inspection.ActivationEvidence == nil {
		t.Fatalf("inspect response-lost activation = %#v, err = %v", inspection, err)
	}
	versionID := inspection.ArtifactVersionID
	if versionID == "" {
		t.Fatal("response-lost activation must keep the committed ArtifactVersionID")
	}

	// Restart: a fresh authority over the same schema and a fresh adapter
	// over the same binding store. Exact replay returns the same decision
	// with the same candidate identity and evidence; the stream still has
	// exactly one version.
	f.rebuildAuthority(t)
	restarted := NewOwnedTransportHarness(config())
	replayed, err := restarted.Client().Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || replayed.State != OperationActivated ||
		replayed.ArtifactVersionID != versionID {
		t.Fatalf("post-restart replay = %#v, err = %v", replayed, err)
	}
	if replayed.ActivationEvidence == nil ||
		replayed.ActivationEvidence.OperationID != PublicationOperationID(operationID) ||
		replayed.ActivationEvidence.ArtifactVersionID != versionID {
		t.Fatalf("post-restart evidence did not preserve the original binding: %#v", replayed)
	}
	history, err := restarted.Client().Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil || len(history.History) != 1 || history.StreamRevision != 1 ||
		history.CurrentHead != versionID {
		t.Fatalf("post-restart history = %#v, err = %v", history, err)
	}
}
