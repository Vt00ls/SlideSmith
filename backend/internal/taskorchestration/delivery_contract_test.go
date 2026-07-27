package taskorchestration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestDispatcherClaimsOnlyCommittedOutboxRecordsWithinItsBatchBound(t *testing.T) {
	now := time.Date(2026, time.July, 27, 16, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "delivery-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "delivery-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "delivery-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "delivery-start", "delivery-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "delivery-work", "delivery-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "delivery-work-available"),
	))
	if err != nil {
		t.Fatalf("commit authoritative outbox record: %v", err)
	}
	cancelHeader := intentHeader(t, "delivery-cancel", "delivery-task", now.Add(2*time.Second))
	cancelHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	cancel, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
		cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
	))
	if err != nil || len(cancel.EnactmentRefs) != 1 {
		t.Fatalf("commit second authoritative outbox record: count=%d err=%v", len(cancel.EnactmentRefs), err)
	}

	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now:              func() time.Time { return now.Add(3 * time.Second) },
		MaxBatchSize:     1,
		LeaseDuration:    time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	}, unusedOwnedTransport{})
	if err != nil {
		t.Fatalf("create outbox dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker,
		Limit:     20,
	})
	if err != nil {
		t.Fatalf("claim committed outbox: %v", err)
	}
	if len(batch.Claims) != 1 {
		t.Fatalf("claim count = %d, want configured bound 1", len(batch.Claims))
	}
	claim := batch.Claims[0]
	wantOperationID := work.EnactmentRefs[0].OperationID
	if claim.OperationID != wantOperationID || claim.Request.OperationID != wantOperationID {
		t.Fatal("dispatcher did not preserve the committed OperationID")
	}
	if claim.Request.PayloadDigest != work.EnactmentRefs[0].PayloadDigest ||
		claim.Request.ActivityGeneration != work.EnactmentRefs[0].ActivityGeneration ||
		claim.Request.CausationID != work.EnactmentRefs[0].CausationID ||
		claim.Request.DecisionID != work.DecisionID ||
		claim.Request.Prerequisites.TaskRevision != work.AcceptedTaskRevision ||
		claim.Request.SafetyEpoch != work.TaskProjection.SafetyEpoch ||
		claim.Request.FenceKind != work.EnactmentRefs[0].Fence.EnactmentFenceKind() {
		t.Fatal("dispatcher did not preserve the immutable enactment binding")
	}
}

func TestDispatcherHeartbeatExtendsLeaseAndExpiryRecoversTheOriginalOperation(t *testing.T) {
	now := time.Date(2026, time.July, 27, 16, 10, 0, 0, time.UTC)
	current := now
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "lease-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	workerA := taskorchestration.NewWorkerAuthority(
		authorityID(t, "lease-worker-a"), taskorchestration.AuthorizationGeneration(1),
	)
	workerB := taskorchestration.NewWorkerAuthority(
		authorityID(t, "lease-worker-b"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "lease-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "lease-start", "lease-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start lease Task: %v", err)
	}
	workHeader := intentHeader(t, "lease-work", "lease-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, workerA, operationID(t, "lease-work-available"),
	))
	if err != nil {
		t.Fatalf("commit lease outbox record: %v", err)
	}
	transport, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{workerA, workerB},
	})
	if err != nil {
		t.Fatalf("create lease transport: %v", err)
	}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{workerA, workerB},
	}, transport)
	if err != nil {
		t.Fatalf("create lease dispatcher: %v", err)
	}
	firstBatch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: workerA, Limit: 1,
	})
	if err != nil || len(firstBatch.Claims) != 1 {
		t.Fatalf("claim first lease: count=%d err=%v", len(firstBatch.Claims), err)
	}
	first := firstBatch.Claims[0]
	current = current.Add(30 * time.Second)
	renewed, err := dispatcher.Heartbeat(context.Background(), taskorchestration.DeliveryHeartbeatRequest{
		Authority: workerA, OperationID: first.OperationID, LeaseFence: first.LeaseFence,
	})
	if err != nil {
		t.Fatalf("heartbeat active lease: %v", err)
	}
	if !renewed.LeaseExpiresAt.Equal(current.Add(time.Minute)) || renewed.LeaseFence != first.LeaseFence {
		t.Fatal("heartbeat did not extend the same fenced lease")
	}
	current = current.Add(59 * time.Second)
	blocked, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: workerB, Limit: 1,
	})
	if err != nil || len(blocked.Claims) != 0 {
		t.Fatalf("claim before renewed lease expiry: count=%d err=%v", len(blocked.Claims), err)
	}
	current = current.Add(2 * time.Second)
	recovered, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: workerB, Limit: 1,
	})
	if err != nil || len(recovered.Claims) != 1 {
		t.Fatalf("recover expired claim: count=%d err=%v", len(recovered.Claims), err)
	}
	if recovered.Claims[0].OperationID != work.EnactmentRefs[0].OperationID ||
		recovered.Claims[0].LeaseFence <= first.LeaseFence {
		t.Fatal("claim recovery did not retain the OperationID with a higher lease fence")
	}
	_, err = dispatcher.Deliver(context.Background(), first)
	var deliveryError *taskorchestration.DeliveryError
	if !errors.As(err, &deliveryError) || deliveryError.Code() != taskorchestration.DeliveryClaimLost {
		t.Fatalf("stale claimant delivery = %T, want typed claim loss", err)
	}
	delivered, err := dispatcher.Deliver(context.Background(), recovered.Claims[0])
	if err != nil || delivered.Disposition != taskorchestration.DeliveryAccepted ||
		delivered.OperationID != work.EnactmentRefs[0].OperationID {
		t.Fatalf("deliver recovered claim: result=%+v err=%v", delivered, err)
	}
}

func TestDispatcherDeliversTheCommittedEnvelopeWithoutMutatingTaskAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 27, 16, 20, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "deliver-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "deliver-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "deliver-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "deliver-start", "deliver-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start delivery Task: %v", err)
	}
	workHeader := intentHeader(t, "deliver-work", "deliver-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "deliver-work-available"),
	))
	if err != nil {
		t.Fatalf("commit delivery outbox record: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID: taskID(t, "deliver-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	before, err := harness.Queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query before delivery: %v", err)
	}
	resultDigest := deliveryResultDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	transport := &acceptingOwnedTransport{resultDigest: resultDigest}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create delivery dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim delivery: count=%d err=%v", len(batch.Claims), err)
	}
	delivered, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
	if err != nil {
		t.Fatalf("deliver committed operation: %v", err)
	}
	if delivered.OperationID != work.EnactmentRefs[0].OperationID ||
		delivered.Disposition != taskorchestration.DeliveryAccepted ||
		delivered.ResultDigest != resultDigest {
		t.Fatalf("delivery result did not retain accepted operation: %+v", delivered)
	}
	received := transport.lastRequest()
	if received.OperationID != work.EnactmentRefs[0].OperationID ||
		received.PayloadDigest != work.EnactmentRefs[0].PayloadDigest ||
		received.Version != taskorchestration.OwnedTransportV1 {
		t.Fatal("owned transport did not receive the committed versioned envelope")
	}
	inspected, err := dispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil {
		t.Fatalf("inspect terminal delivery: %v", err)
	}
	if inspected.Disposition != taskorchestration.DeliveryAccepted ||
		inspected.ResultDigest != resultDigest || !inspected.Terminal {
		t.Fatalf("terminal delivery inspection = %+v", inspected)
	}
	after, err := harness.Queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query after delivery: %v", err)
	}
	if before.TaskRevision != after.TaskRevision || before.DecisionCount != after.DecisionCount ||
		before.EnactmentCount != after.EnactmentCount || before.Status != after.Status ||
		before.PhaseRuns[0].Outcome != after.PhaseRuns[0].Outcome {
		t.Fatal("delivery changed Task, Decision, outbox, or Phase authority")
	}
}

func TestOwnedTransportReplaysExactDuplicateAndRejectsOperationRebinding(t *testing.T) {
	now := time.Date(2026, time.July, 27, 16, 30, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "transport-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "transport-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "transport-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "transport-start", "transport-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start transport Task: %v", err)
	}
	workHeader := intentHeader(t, "transport-work", "transport-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "transport-work-available"),
	)); err != nil {
		t.Fatalf("commit transport outbox record: %v", err)
	}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, unusedOwnedTransport{})
	if err != nil {
		t.Fatalf("create envelope source dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim owned transport envelope: count=%d err=%v", len(batch.Claims), err)
	}
	request := batch.Claims[0].Request
	transport, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	})
	if err != nil {
		t.Fatalf("create deterministic owned transport: %v", err)
	}
	first, err := transport.Deliver(context.Background(), request)
	if err != nil || first.Outcome != taskorchestration.OwnedTransportAccepted ||
		first.ResultDigest == (taskorchestration.DeliveryResultDigest{}) {
		t.Fatalf("accept original envelope: response=%+v err=%v", first, err)
	}
	duplicate, err := transport.Deliver(context.Background(), request)
	if err != nil {
		t.Fatalf("deliver exact duplicate: %v", err)
	}
	if duplicate.Outcome != first.Outcome || duplicate.ResultDigest != first.ResultDigest ||
		!duplicate.Duplicate {
		t.Fatalf("exact duplicate did not return original acceptance: %+v", duplicate)
	}
	rebound := request
	rebound.PayloadDigest = enactmentPayloadDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	conflict, err := transport.Deliver(context.Background(), rebound)
	if err != nil {
		t.Fatalf("deliver rebound OperationID: %v", err)
	}
	if conflict.Outcome != taskorchestration.OwnedTransportIntegrityConflict ||
		conflict.ResultDigest != (taskorchestration.DeliveryResultDigest{}) {
		t.Fatalf("rebound OperationID result = %+v, want typed integrity conflict", conflict)
	}
	inspected, err := transport.Inspect(context.Background(), taskorchestration.OwnedTransportInspection{
		Version: taskorchestration.OwnedTransportV1, Authority: worker, OperationID: request.OperationID,
	})
	if err != nil {
		t.Fatalf("inspect accepted duplicate: %v", err)
	}
	if inspected.Outcome != first.Outcome || inspected.ResultDigest != first.ResultDigest {
		t.Fatal("integrity conflict overwrote the original transport acceptance")
	}
}

func TestDispatcherReconcilesAcceptanceAfterAcknowledgementLoss(t *testing.T) {
	now := time.Date(2026, time.July, 27, 16, 40, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "reconcile-delivery-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "reconcile-delivery-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "reconcile-delivery-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "reconcile-delivery-start", "reconcile-delivery-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start reconciliation Task: %v", err)
	}
	workHeader := intentHeader(t, "reconcile-delivery-work", "reconcile-delivery-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "reconcile-delivery-work-available"),
	))
	if err != nil {
		t.Fatalf("commit reconciliation outbox record: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID: taskID(t, "reconcile-delivery-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	before, err := harness.Queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query before ambiguous delivery: %v", err)
	}
	owned, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	})
	if err != nil {
		t.Fatalf("create owned transport: %v", err)
	}
	transport := &acknowledgementLosingTransport{owned: owned}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create reconciliation dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim ambiguous delivery: count=%d err=%v", len(batch.Claims), err)
	}
	ambiguous, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
	if err != nil {
		t.Fatalf("ambiguous transport response escaped as failure: %v", err)
	}
	if ambiguous.OperationID != work.EnactmentRefs[0].OperationID ||
		ambiguous.Disposition != taskorchestration.DeliveryReconciliationRequired {
		t.Fatalf("acknowledgement loss disposition = %+v", ambiguous)
	}
	for _, canary := range []string{"/private/session", "credential-canary"} {
		if strings.Contains(fmt.Sprintf("%+v", ambiguous), canary) {
			t.Fatalf("delivery result leaked transport-private detail %q", canary)
		}
	}
	view, err := dispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: ambiguous.OperationID,
	})
	if err != nil || view.Terminal ||
		view.Disposition != taskorchestration.DeliveryReconciliationRequired {
		t.Fatalf("ambiguous inspection = %+v err=%v", view, err)
	}
	reconciled, err := dispatcher.Reconcile(context.Background(), taskorchestration.DeliveryReconcileRequest{
		Authority: worker, OperationID: ambiguous.OperationID,
	})
	if err != nil {
		t.Fatalf("reconcile downstream acceptance: %v", err)
	}
	if reconciled.Disposition != taskorchestration.DeliveryAccepted ||
		reconciled.OperationID != ambiguous.OperationID ||
		reconciled.ResultDigest == (taskorchestration.DeliveryResultDigest{}) ||
		reconciled.DeliveryCount != 1 {
		t.Fatalf("reconciled result = %+v", reconciled)
	}
	after, err := harness.Queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query after delivery reconciliation: %v", err)
	}
	if before.TaskRevision != after.TaskRevision || before.DecisionCount != after.DecisionCount ||
		before.EnactmentCount != after.EnactmentCount || before.PhaseRunCount != after.PhaseRunCount ||
		before.RuntimeRunCount != after.RuntimeRunCount {
		t.Fatal("delivery reconciliation created Task authority or a business attempt")
	}
}

func TestDispatcherHonorsTransportBackpressureWithoutCreatingAnotherOperation(t *testing.T) {
	now := time.Date(2026, time.July, 27, 16, 50, 0, 0, time.UTC)
	current := now.Add(2 * time.Second)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "backpressure-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "backpressure-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "backpressure-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "backpressure-start", "backpressure-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start backpressure Task: %v", err)
	}
	workHeader := intentHeader(t, "backpressure-work", "backpressure-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "backpressure-work-available"),
	))
	if err != nil {
		t.Fatalf("commit backpressure outbox record: %v", err)
	}
	retryAt := current.Add(2 * time.Minute)
	transport := fixedOutcomeTransport{response: taskorchestration.OwnedTransportResponse{
		Version: taskorchestration.OwnedTransportV1,
		Outcome: taskorchestration.OwnedTransportBackpressured,
		RetryAt: retryAt,
	}}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create backpressure dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim backpressure delivery: count=%d err=%v", len(batch.Claims), err)
	}
	firstClaim := batch.Claims[0]
	deferred, err := dispatcher.Deliver(context.Background(), firstClaim)
	if err != nil {
		t.Fatalf("record transport backpressure: %v", err)
	}
	if deferred.OperationID != work.EnactmentRefs[0].OperationID ||
		deferred.Disposition != taskorchestration.DeliveryBackpressured ||
		!deferred.RetryAt.Equal(retryAt) {
		t.Fatalf("backpressure result = %+v", deferred)
	}
	current = retryAt.Add(-time.Nanosecond)
	tooEarly, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(tooEarly.Claims) != 0 {
		t.Fatalf("claim before transport retry-at: count=%d err=%v", len(tooEarly.Claims), err)
	}
	current = retryAt
	retry, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(retry.Claims) != 1 {
		t.Fatalf("claim at transport retry-at: count=%d err=%v", len(retry.Claims), err)
	}
	if retry.Claims[0].OperationID != firstClaim.OperationID ||
		retry.Claims[0].LeaseFence <= firstClaim.LeaseFence {
		t.Fatal("backpressure retry did not reuse the operation with a new lease fence")
	}
}

func TestOutOfOrderCancellationFenceSupersedesStalePendingEnactment(t *testing.T) {
	now := time.Date(2026, time.July, 27, 17, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "supersede-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "supersede-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "supersede-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "supersede-start", "supersede-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start supersession Task: %v", err)
	}
	workHeader := intentHeader(t, "supersede-work", "supersede-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "supersede-work-available"),
	))
	if err != nil {
		t.Fatalf("commit pending enactment: %v", err)
	}
	cancelHeader := intentHeader(t, "supersede-cancel", "supersede-task", now.Add(2*time.Second))
	cancelHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	cancel, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
		cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
	))
	if err != nil {
		t.Fatalf("commit cancellation fence: %v", err)
	}
	if len(cancel.EnactmentRefs) != 1 {
		t.Fatalf("cancellation enactment count = %d, want 1", len(cancel.EnactmentRefs))
	}
	query := taskorchestration.TaskQuery{
		TaskID: taskID(t, "supersede-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	acceptedHistory, err := harness.Queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query accepted cancellation history: %v", err)
	}
	transport, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	})
	if err != nil {
		t.Fatalf("create supersession transport: %v", err)
	}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(3 * time.Second) }, MaxBatchSize: 2,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create supersession dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 2,
	})
	if err != nil || len(batch.Claims) != 2 {
		t.Fatalf("claim pending and cancellation enactments: count=%d err=%v", len(batch.Claims), err)
	}
	claims := make(map[taskorchestration.OperationID]taskorchestration.DeliveryClaim, len(batch.Claims))
	for _, claim := range batch.Claims {
		claims[claim.OperationID] = claim
	}
	newer, err := dispatcher.Deliver(context.Background(), claims[cancel.EnactmentRefs[0].OperationID])
	if err != nil || newer.Disposition != taskorchestration.DeliveryAccepted {
		t.Fatalf("deliver cancellation fence first: result=%+v err=%v", newer, err)
	}
	stale, err := dispatcher.Deliver(context.Background(), claims[work.EnactmentRefs[0].OperationID])
	if err != nil {
		t.Fatalf("deliver stale pending enactment: %v", err)
	}
	if stale.OperationID != work.EnactmentRefs[0].OperationID ||
		stale.Disposition != taskorchestration.DeliverySuperseded {
		t.Fatalf("stale enactment disposition = %+v", stale)
	}
	staleView, err := dispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: stale.OperationID,
	})
	if err != nil || !staleView.Terminal ||
		staleView.Disposition != taskorchestration.DeliverySuperseded {
		t.Fatalf("inspect superseded enactment: view=%+v err=%v", staleView, err)
	}
	afterDelivery, err := harness.Queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query after stale delivery: %v", err)
	}
	if acceptedHistory.TaskRevision != afterDelivery.TaskRevision ||
		acceptedHistory.DecisionCount != afterDelivery.DecisionCount ||
		acceptedHistory.CancellationState != afterDelivery.CancellationState ||
		acceptedHistory.PhaseRuns[0].Outcome != afterDelivery.PhaseRuns[0].Outcome {
		t.Fatal("stale delivery rewrote accepted cancellation or Phase history")
	}
}

func TestRecoveryFenceSupersedesPreRecoveryPendingEnactmentBeforeRemoteIO(t *testing.T) {
	now := time.Date(2026, time.July, 27, 17, 10, 0, 0, time.UTC)
	recoveryAuthority := taskorchestration.NewRecoveryAuthority(
		authorityID(t, "delivery-recovery-authority"), taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Recovery: taskorchestration.HarnessRecoveryFixture{
			Authority: recoveryAuthority, Generation: 1, Fence: 1, SafetyEpoch: 1,
			Mode: taskorchestration.OperationalFullReady,
		},
	})
	if err != nil {
		t.Fatalf("create recovery harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "delivery-recovery-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "delivery-recovery-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "delivery-recovery-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "delivery-recovery-start", "delivery-recovery-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start recovery Task: %v", err)
	}
	workHeader := intentHeader(t, "delivery-recovery-work", "delivery-recovery-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "delivery-recovery-work-available"),
	))
	if err != nil {
		t.Fatalf("commit pre-recovery outbox record: %v", err)
	}
	fenceHeader := intentHeader(t, "delivery-recovery-fence", "delivery-recovery-task", now.Add(2*time.Second))
	fenceHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	fenced, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewApplyOperationalFenceIntent(
		fenceHeader, recoveryAuthority, taskorchestration.OperationalFenceBinding{
			Generation: 2, Fence: 2, SafetyEpoch: 2, Mode: taskorchestration.OperationalReadOnly,
		},
	))
	if err != nil {
		t.Fatalf("commit recovery fence: %v", err)
	}
	transport := &countingOwnedTransport{}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(3 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create recovery dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil {
		t.Fatalf("claim after recovery fence: %v", err)
	}
	if len(batch.Claims) != 0 {
		t.Fatal("dispatcher claimed a pre-recovery stale enactment")
	}
	view, err := dispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || !view.Terminal || view.Disposition != taskorchestration.DeliverySuperseded {
		t.Fatalf("pre-recovery enactment inspection = %+v err=%v", view, err)
	}
	if transport.deliveries() != 0 {
		t.Fatal("recovery supersession performed remote I/O")
	}
	queryView, err := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: taskID(t, "delivery-recovery-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query after recovery supersession: %v", err)
	}
	if queryView.TaskRevision != fenced.AcceptedTaskRevision ||
		queryView.LatestDecisionID != fenced.DecisionID ||
		queryView.OperationalMode != taskorchestration.OperationalReadOnly {
		t.Fatal("delivery supersession rewrote the accepted recovery decision")
	}
}

func TestDispatcherTerminatesPoisonDeliveryWithoutRedrivingIt(t *testing.T) {
	now := time.Date(2026, time.July, 27, 17, 20, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "poison-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "poison-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "poison-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "poison-start", "poison-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start poison Task: %v", err)
	}
	workHeader := intentHeader(t, "poison-work", "poison-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "poison-work-available"),
	))
	if err != nil {
		t.Fatalf("commit poison outbox record: %v", err)
	}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, fixedOutcomeTransport{response: taskorchestration.OwnedTransportResponse{
		Version: taskorchestration.OwnedTransportV1,
		Outcome: taskorchestration.OwnedTransportPoisoned,
	}})
	if err != nil {
		t.Fatalf("create poison dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim poison delivery: count=%d err=%v", len(batch.Claims), err)
	}
	poisoned, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
	if err != nil {
		t.Fatalf("record poison delivery: %v", err)
	}
	if poisoned.OperationID != work.EnactmentRefs[0].OperationID ||
		poisoned.Disposition != taskorchestration.DeliveryPoisoned {
		t.Fatalf("poison disposition = %+v", poisoned)
	}
	restarted := harness.Restart()
	restartedDispatcher, err := restarted.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(3 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, unusedOwnedTransport{})
	if err != nil {
		t.Fatalf("restart poison dispatcher: %v", err)
	}
	retry, err := restartedDispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(retry.Claims) != 0 {
		t.Fatalf("claim terminal poison after restart: count=%d err=%v", len(retry.Claims), err)
	}
	view, err := restartedDispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || !view.Terminal || view.Disposition != taskorchestration.DeliveryPoisoned {
		t.Fatalf("inspect poison after restart: view=%+v err=%v", view, err)
	}
}

func TestDispatcherDefersOutOfOrderPrerequisiteAndReplaysTheSameOperation(t *testing.T) {
	now := time.Date(2026, time.July, 27, 17, 30, 0, 0, time.UTC)
	current := now.Add(2 * time.Second)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "deferred-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "deferred-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "deferred-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "deferred-start", "deferred-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start deferred Task: %v", err)
	}
	workHeader := intentHeader(t, "deferred-work", "deferred-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "deferred-work-available"),
	))
	if err != nil {
		t.Fatalf("commit deferred outbox record: %v", err)
	}
	retryAt := current.Add(time.Minute)
	resultDigest := deliveryResultDigest(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	transport := &sequenceOwnedTransport{responses: []taskorchestration.OwnedTransportResponse{
		{
			Version:        taskorchestration.OwnedTransportV1,
			Outcome:        taskorchestration.OwnedTransportDeferred,
			DeferralReason: taskorchestration.OwnedTransportPrerequisiteDeferred,
			RetryAt:        retryAt,
		},
		{
			Version:      taskorchestration.OwnedTransportV1,
			Outcome:      taskorchestration.OwnedTransportAccepted,
			ResultDigest: resultDigest,
		},
	}}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create deferred dispatcher: %v", err)
	}
	firstBatch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(firstBatch.Claims) != 1 {
		t.Fatalf("claim out-of-order delivery: count=%d err=%v", len(firstBatch.Claims), err)
	}
	first, err := dispatcher.Deliver(context.Background(), firstBatch.Claims[0])
	if err != nil {
		t.Fatalf("defer out-of-order delivery: %v", err)
	}
	if first.OperationID != work.EnactmentRefs[0].OperationID ||
		first.Disposition != taskorchestration.DeliveryDeferred ||
		first.DeferralReason != taskorchestration.OwnedTransportPrerequisiteDeferred ||
		!first.RetryAt.Equal(retryAt) {
		t.Fatalf("out-of-order deferral = %+v", first)
	}
	current = retryAt
	retryBatch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(retryBatch.Claims) != 1 {
		t.Fatalf("reclaim deferred operation: count=%d err=%v", len(retryBatch.Claims), err)
	}
	accepted, err := dispatcher.Deliver(context.Background(), retryBatch.Claims[0])
	if err != nil {
		t.Fatalf("accept deferred operation after prerequisite: %v", err)
	}
	if accepted.OperationID != first.OperationID || accepted.Disposition != taskorchestration.DeliveryAccepted ||
		accepted.ResultDigest != resultDigest || accepted.DeliveryCount != 2 {
		t.Fatalf("deferred replay result = %+v", accepted)
	}
}

func TestOwnedTransportWireIsVersionedStrictAndSafe(t *testing.T) {
	now := time.Date(2026, time.July, 27, 17, 40, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "wire-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "wire-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "wire-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "wire-start", "wire-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start wire Task: %v", err)
	}
	workHeader := intentHeader(t, "wire-work", "wire-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "wire-work-available"),
	)); err != nil {
		t.Fatalf("commit wire outbox record: %v", err)
	}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, unusedOwnedTransport{})
	if err != nil {
		t.Fatalf("create wire dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim wire envelope: count=%d err=%v", len(batch.Claims), err)
	}
	request := batch.Claims[0].Request
	wire, err := taskorchestration.EncodeOwnedTransportRequest(request)
	if err != nil {
		t.Fatalf("encode owned transport request: %v", err)
	}
	if !bytes.Contains(wire, []byte(`"schema_version":"1.0"`)) ||
		!bytes.Contains(wire, []byte(`"operation_id":"`+request.OperationID.String()+`"`)) ||
		bytes.Contains(wire, []byte("content")) || bytes.Contains(wire, []byte("path")) ||
		bytes.Contains(wire, []byte("session")) || bytes.Contains(wire, []byte("credential")) {
		t.Fatalf("unsafe or incomplete request wire: %s", wire)
	}
	decoded, err := taskorchestration.DecodeOwnedTransportRequest(wire)
	if err != nil {
		t.Fatalf("decode owned transport request: %v", err)
	}
	if !reflect.DeepEqual(decoded, request) {
		t.Fatal("request wire round trip changed the envelope")
	}
	unknownField := bytes.Replace(wire, []byte("}"), []byte(`,"credential":"credential-canary"}`), 1)
	_, err = taskorchestration.DecodeOwnedTransportRequest(unknownField)
	var wireError *taskorchestration.OwnedTransportWireError
	if !errors.As(err, &wireError) ||
		wireError.Code() != taskorchestration.OwnedTransportWireInvalidEnvelope {
		t.Fatalf("unknown request field = %T, want typed invalid envelope", err)
	}
	if strings.Contains(err.Error(), "credential-canary") {
		t.Fatal("wire error leaked the rejected credential field")
	}
	transport, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	})
	if err != nil {
		t.Fatalf("create version-negotiating transport: %v", err)
	}
	unsupported := request
	unsupported.Version = taskorchestration.OwnedTransportVersion(2 << 16)
	negotiated, err := transport.Deliver(context.Background(), unsupported)
	if err != nil || negotiated.Outcome != taskorchestration.OwnedTransportUnsupportedVersion ||
		negotiated.Version != taskorchestration.OwnedTransportV1 {
		t.Fatalf("unsupported version negotiation = %+v err=%v", negotiated, err)
	}
	response := taskorchestration.OwnedTransportResponse{
		Version: taskorchestration.OwnedTransportV1, OperationID: request.OperationID,
		Outcome:      taskorchestration.OwnedTransportAccepted,
		ResultDigest: deliveryResultDigest(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
	}
	responseWire, err := taskorchestration.EncodeOwnedTransportResponse(response)
	if err != nil {
		t.Fatalf("encode owned transport response: %v", err)
	}
	decodedResponse, err := taskorchestration.DecodeOwnedTransportResponse(responseWire)
	if err != nil || decodedResponse != response {
		t.Fatalf("response wire round trip = %+v err=%v", decodedResponse, err)
	}
}

func TestDispatcherAndOwnedTransportRestartReconcileTheOriginalAcceptance(t *testing.T) {
	now := time.Date(2026, time.July, 27, 17, 50, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "restart-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "restart-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "restart-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "restart-start", "restart-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start restart Task: %v", err)
	}
	workHeader := intentHeader(t, "restart-work", "restart-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "restart-work-available"),
	))
	if err != nil {
		t.Fatalf("commit restart outbox record: %v", err)
	}
	owned, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	})
	if err != nil {
		t.Fatalf("create restart transport: %v", err)
	}
	dispatcher, err := harness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, &acknowledgementLosingTransport{owned: owned})
	if err != nil {
		t.Fatalf("create restart dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim restart delivery: count=%d err=%v", len(batch.Claims), err)
	}
	ambiguous, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
	if err != nil || ambiguous.Disposition != taskorchestration.DeliveryReconciliationRequired {
		t.Fatalf("record restart ambiguity: result=%+v err=%v", ambiguous, err)
	}

	restartedHarness := harness.Restart()
	restartedTransport := owned.Restart()
	restartedDispatcher, err := restartedHarness.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(time.Hour) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, restartedTransport)
	if err != nil {
		t.Fatalf("restart delivery adapters: %v", err)
	}
	reconciled, err := restartedDispatcher.Reconcile(context.Background(), taskorchestration.DeliveryReconcileRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil {
		t.Fatalf("reconcile after process restart: %v", err)
	}
	if reconciled.OperationID != work.EnactmentRefs[0].OperationID ||
		reconciled.Disposition != taskorchestration.DeliveryAccepted ||
		reconciled.DeliveryCount != 1 ||
		reconciled.ResultDigest == (taskorchestration.DeliveryResultDigest{}) {
		t.Fatalf("restart reconciliation = %+v", reconciled)
	}
}

type unusedOwnedTransport struct{}

func (unusedOwnedTransport) Deliver(
	context.Context,
	taskorchestration.OwnedTransportRequest,
) (taskorchestration.OwnedTransportResponse, error) {
	return taskorchestration.OwnedTransportResponse{}, nil
}

type acceptingOwnedTransport struct {
	mu           sync.Mutex
	request      taskorchestration.OwnedTransportRequest
	resultDigest taskorchestration.DeliveryResultDigest
}

type acknowledgementLosingTransport struct {
	owned *taskorchestration.DeterministicOwnedTransport
}

type fixedOutcomeTransport struct {
	response taskorchestration.OwnedTransportResponse
}

type sequenceOwnedTransport struct {
	mu        sync.Mutex
	responses []taskorchestration.OwnedTransportResponse
	next      int
}

func (transport *sequenceOwnedTransport) Deliver(
	_ context.Context,
	request taskorchestration.OwnedTransportRequest,
) (taskorchestration.OwnedTransportResponse, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.next >= len(transport.responses) {
		return taskorchestration.OwnedTransportResponse{}, errors.New("transport sequence exhausted")
	}
	response := transport.responses[transport.next]
	transport.next++
	response.OperationID = request.OperationID
	return response, nil
}

func (transport *sequenceOwnedTransport) Inspect(
	_ context.Context,
	inspection taskorchestration.OwnedTransportInspection,
) (taskorchestration.OwnedTransportResponse, error) {
	return taskorchestration.OwnedTransportResponse{
		Version: inspection.Version, OperationID: inspection.OperationID,
		Outcome: taskorchestration.OwnedTransportUnknown,
	}, nil
}

type countingOwnedTransport struct {
	mu    sync.Mutex
	count uint32
}

func (transport *countingOwnedTransport) Deliver(
	_ context.Context,
	request taskorchestration.OwnedTransportRequest,
) (taskorchestration.OwnedTransportResponse, error) {
	transport.mu.Lock()
	transport.count++
	transport.mu.Unlock()
	return taskorchestration.OwnedTransportResponse{
		Version: request.Version, OperationID: request.OperationID,
		Outcome: taskorchestration.OwnedTransportAccepted,
		ResultDigest: deliveryResultDigestValue(
			"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		),
	}, nil
}

func (transport *countingOwnedTransport) Inspect(
	context.Context,
	taskorchestration.OwnedTransportInspection,
) (taskorchestration.OwnedTransportResponse, error) {
	return taskorchestration.OwnedTransportResponse{}, nil
}

func (transport *countingOwnedTransport) deliveries() uint32 {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.count
}

func (transport fixedOutcomeTransport) Deliver(
	_ context.Context,
	request taskorchestration.OwnedTransportRequest,
) (taskorchestration.OwnedTransportResponse, error) {
	response := transport.response
	response.OperationID = request.OperationID
	return response, nil
}

func (transport fixedOutcomeTransport) Inspect(
	_ context.Context,
	inspection taskorchestration.OwnedTransportInspection,
) (taskorchestration.OwnedTransportResponse, error) {
	response := transport.response
	response.OperationID = inspection.OperationID
	return response, nil
}

func (transport *acknowledgementLosingTransport) Deliver(
	ctx context.Context,
	request taskorchestration.OwnedTransportRequest,
) (taskorchestration.OwnedTransportResponse, error) {
	if _, err := transport.owned.Deliver(ctx, request); err != nil {
		return taskorchestration.OwnedTransportResponse{}, err
	}
	return taskorchestration.OwnedTransportResponse{}, errors.New(
		"timeout after acceptance at /private/session credential-canary",
	)
}

func (transport *acknowledgementLosingTransport) Inspect(
	ctx context.Context,
	inspection taskorchestration.OwnedTransportInspection,
) (taskorchestration.OwnedTransportResponse, error) {
	return transport.owned.Inspect(ctx, inspection)
}

func (transport *acceptingOwnedTransport) Deliver(
	_ context.Context,
	request taskorchestration.OwnedTransportRequest,
) (taskorchestration.OwnedTransportResponse, error) {
	transport.mu.Lock()
	transport.request = request
	transport.mu.Unlock()
	return taskorchestration.OwnedTransportResponse{
		Version: request.Version, OperationID: request.OperationID,
		Outcome: taskorchestration.OwnedTransportAccepted, ResultDigest: transport.resultDigest,
	}, nil
}

func (transport *acceptingOwnedTransport) Inspect(
	context.Context,
	taskorchestration.OwnedTransportInspection,
) (taskorchestration.OwnedTransportResponse, error) {
	return taskorchestration.OwnedTransportResponse{}, nil
}

func (transport *acceptingOwnedTransport) lastRequest() taskorchestration.OwnedTransportRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.request
}

func deliveryResultDigest(t *testing.T, value string) taskorchestration.DeliveryResultDigest {
	t.Helper()
	digest, err := taskorchestration.ParseDeliveryResultDigest(value)
	if err != nil {
		t.Fatalf("parse delivery result digest: %v", err)
	}
	return digest
}

func enactmentPayloadDigest(t *testing.T, value string) taskorchestration.EnactmentPayloadDigest {
	t.Helper()
	digest, err := taskorchestration.ParseEnactmentPayloadDigest(value)
	if err != nil {
		t.Fatalf("parse enactment payload digest: %v", err)
	}
	return digest
}

func deliveryResultDigestValue(value string) taskorchestration.DeliveryResultDigest {
	digest, _ := taskorchestration.ParseDeliveryResultDigest(value)
	return digest
}

func (unusedOwnedTransport) Inspect(
	context.Context,
	taskorchestration.OwnedTransportInspection,
) (taskorchestration.OwnedTransportResponse, error) {
	return taskorchestration.OwnedTransportResponse{}, nil
}
