package artifactpublication

// Publication bridge over real PostgreSQL (child SPEC #109 / C05-06). The
// same bridge contract that runs over the deterministic in-memory authority
// must hold when the C05 owned transport wraps the production-shaped
// PostgresAuthority: the committed canonical request is delivered through
// the owned transport, response loss returns typed
// reconciliation-required and reconciles by the ORIGINAL OperationID, and
// restart/response-loss replay never creates a second operation or Artifact
// Version.

import (
	"context"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

// TestOwnedTransportBridgePostgresDeliversThroughOwnedTransportToC05 drives
// the full committed-outbox -> owned transport -> real PostgreSQL chain and
// proves the activation evidence binds the exact OperationID, Phase Run
// generation/fence, activity generation, safety epoch, ArtifactVersionID and
// manifest digest.
func TestOwnedTransportBridgePostgresDeliversThroughOwnedTransportToC05(t *testing.T) {
	f := newPostgresFixture(t)
	harness := ownedTransportPostgresHarness(t, f)
	client := harness.Client()
	operationID := "pg-bridge-c05-1"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
	binding := bridgeBindingFromIntent(t, mustPrepareIntent(t, intent))

	now := time.Unix(int64(f.now), 0).UTC()
	port := &ownedTransportBridgePort{f: f.fixture, client: client}
	adapter := taskorchestration.NewPublicationBridgeAdapter(port)
	bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("commit: %v", err)
	}
	claimed := bridge.Claim(1)
	delivery, err := bridge.Deliver(context.Background(), claimed[0])
	if err != nil || delivery.Outcome != taskorchestration.PublicationDelivered {
		t.Fatalf("deliver through owned transport to PostgreSQL = %#v, err = %v", delivery, err)
	}
	if delivery.OperationID != binding.OperationID || delivery.Digest != binding.CanonicalDigest() {
		t.Fatalf("postgres bridge delivery changed identity/digest: %#v", delivery)
	}

	view, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: PublicationOperationID(operationID),
	})
	if err != nil || view.State != OperationPrepared {
		t.Fatalf("postgres C05 operation after bridge delivery = %#v, err = %v", view, err)
	}

	if _, err := client.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
		t.Fatalf("verify through transport: %v", err)
	}
	activated, err := client.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || activated.ActivationEvidence == nil {
		t.Fatalf("activate through transport = %#v, err = %v", activated, err)
	}
	evidence := activated.ActivationEvidence
	if evidence.OperationID != PublicationOperationID(operationID) ||
		evidence.PhaseRunID != f.phaseRunID ||
		evidence.ActivityGeneration != f.generation ||
		evidence.Generation != f.generation || evidence.Fence != f.fence ||
		evidence.SafetyEpoch != f.safetyEpoch ||
		evidence.ArtifactVersionID != view.ArtifactVersionID ||
		evidence.ManifestDigest != view.ManifestDigest {
		t.Fatalf("postgres activation evidence did not bind the exact facts: %#v", evidence)
	}
}

// TestOwnedTransportBridgePostgresRestartResponseLossReplay proves response
// loss through the bridge over real PostgreSQL reconciles by the ORIGINAL
// OperationID after an authority+adapter restart, without a second operation
// or Artifact Version.
func TestOwnedTransportBridgePostgresRestartResponseLossReplay(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-bridge-restart-1"
	bindings := NewOwnedTransportBindingStore()
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
	binding := bridgeBindingFromIntent(t, mustPrepareIntent(t, intent))
	now := time.Unix(int64(f.now), 0).UTC()

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
	port := &ownedTransportBridgePort{f: f.fixture, client: first.Client()}
	adapter := taskorchestration.NewPublicationBridgeAdapter(port)
	bridge := taskorchestration.NewPublicationBridge(adapter, func() time.Time { return now })
	if err := bridge.Commit(binding); err != nil {
		t.Fatalf("commit: %v", err)
	}
	claimed := bridge.Claim(1)
	first.FailNext(OwnedTransportResponseLoss)
	delivery, err := bridge.Deliver(context.Background(), claimed[0])
	if err != nil || delivery.Outcome != taskorchestration.PublicationReconciliationRequired {
		t.Fatalf("postgres response-lost delivery = %#v, err = %v", delivery, err)
	}

	// Restart: fresh authority over the same schema, fresh adapter and
	// bridge; reconcile the ORIGINAL OperationID.
	f.rebuildAuthority(t)
	restarted := NewOwnedTransportHarness(config())
	restartPort := &ownedTransportBridgePort{f: f.fixture, client: restarted.Client()}
	restartAdapter := taskorchestration.NewPublicationBridgeAdapter(restartPort)
	restartBridge := taskorchestration.NewPublicationBridge(restartAdapter, func() time.Time { return now })
	if err := restartBridge.Commit(binding); err != nil {
		t.Fatalf("re-commit after restart: %v", err)
	}
	reconciled, err := restartBridge.Reconcile(context.Background(), binding.OperationID)
	if err != nil || reconciled.OperationID != binding.OperationID ||
		reconciled.Outcome != taskorchestration.PublicationDelivered {
		t.Fatalf("post-restart reconcile = %#v, err = %v", reconciled, err)
	}
	view, err := restarted.Client().Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: PublicationOperationID(operationID),
	})
	if err != nil || view.State != OperationPrepared {
		t.Fatalf("post-restart operation = %#v, err = %v", view, err)
	}
	// No version was created by the ambiguous delivery.
	history, err := restarted.Client().Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err == nil {
		if len(history.History) != 0 {
			t.Fatalf("post-restart history = %#v", history)
		}
	} else if !isCode(err, ErrorNotFound) {
		t.Fatalf("post-restart history query: %v", err)
	}
}
