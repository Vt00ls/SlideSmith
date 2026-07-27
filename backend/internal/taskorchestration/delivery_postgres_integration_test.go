package taskorchestration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

type blockingOwnedTransport struct {
	owned       *taskorchestration.DeterministicOwnedTransport
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	mu          sync.Mutex
	count       uint32
}

func newBlockingOwnedTransport(
	owned *taskorchestration.DeterministicOwnedTransport,
	capacity int,
) *blockingOwnedTransport {
	return &blockingOwnedTransport{
		owned: owned, entered: make(chan struct{}, capacity), release: make(chan struct{}),
	}
}

func (transport *blockingOwnedTransport) Deliver(
	ctx context.Context,
	request taskorchestration.OwnedTransportRequest,
) (taskorchestration.OwnedTransportResponse, error) {
	transport.mu.Lock()
	transport.count++
	transport.mu.Unlock()
	select {
	case transport.entered <- struct{}{}:
	case <-ctx.Done():
		return taskorchestration.OwnedTransportResponse{}, ctx.Err()
	}
	select {
	case <-transport.release:
	case <-ctx.Done():
		return taskorchestration.OwnedTransportResponse{}, ctx.Err()
	}
	return transport.owned.Deliver(ctx, request)
}

func (transport *blockingOwnedTransport) Inspect(
	ctx context.Context,
	request taskorchestration.OwnedTransportInspection,
) (taskorchestration.OwnedTransportResponse, error) {
	return transport.owned.Inspect(ctx, request)
}

func (transport *blockingOwnedTransport) releaseAll() {
	transport.releaseOnce.Do(func() { close(transport.release) })
}

func (transport *blockingOwnedTransport) deliveries() uint32 {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.count
}

func TestPostgresDispatcherDeliversCommittedOutboxAndRetainsDispositionAcrossRestart(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-delivery-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-delivery-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-delivery-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-delivery-start", "postgres-delivery-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start PostgreSQL delivery Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-delivery-work", "postgres-delivery-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-delivery-work-available"),
	))
	if err != nil {
		t.Fatalf("commit PostgreSQL outbox record: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID: taskID(t, "postgres-delivery-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	before, err := adapter.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query before PostgreSQL delivery: %v", err)
	}
	transport, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion:       taskorchestration.OwnedTransportV1,
		Authorities:            []taskorchestration.WorkerAuthority{worker},
		Now:                    func() time.Time { return now },
		PrerequisiteRetryDelay: time.Minute,
		PrerequisitesSatisfied: acceptOwnedTransportPrerequisites,
	})
	if err != nil {
		t.Fatalf("create PostgreSQL owned transport: %v", err)
	}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create PostgreSQL dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 10,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim PostgreSQL outbox: count=%d err=%v", len(batch.Claims), err)
	}
	if batch.Claims[0].OperationID != work.EnactmentRefs[0].OperationID {
		t.Fatal("PostgreSQL dispatcher changed the committed OperationID")
	}
	delivered, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
	if err != nil || delivered.Disposition != taskorchestration.DeliveryAccepted {
		t.Fatalf("deliver PostgreSQL outbox: result=%+v err=%v", delivered, err)
	}

	restartedAdapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Hour) },
	})
	restartedDispatcher, err := restartedAdapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(time.Hour) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport.Restart())
	if err != nil {
		t.Fatalf("restart PostgreSQL dispatcher: %v", err)
	}
	view, err := restartedDispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || !view.Terminal || view.Disposition != taskorchestration.DeliveryAccepted ||
		view.ResultDigest != delivered.ResultDigest || view.DeliveryCount != 1 {
		t.Fatalf("inspect PostgreSQL delivery after restart: view=%+v err=%v", view, err)
	}
	after, err := restartedAdapter.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query after PostgreSQL delivery: %v", err)
	}
	if before.TaskRevision != after.TaskRevision || before.DecisionCount != after.DecisionCount ||
		before.EnactmentCount != after.EnactmentCount || before.PhaseRunCount != after.PhaseRunCount ||
		before.RuntimeRunCount != after.RuntimeRunCount {
		t.Fatal("PostgreSQL delivery changed Task authority")
	}
}

func TestPostgresConcurrentDeliverUsesOneTransportSendPerClaim(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 3, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-concurrent-deliver-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-concurrent-deliver-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-concurrent-deliver-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-concurrent-deliver-start", "postgres-concurrent-deliver-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start PostgreSQL concurrent-deliver Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-concurrent-deliver-work", "postgres-concurrent-deliver-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-concurrent-deliver-work-available"),
	))
	if err != nil {
		t.Fatalf("commit PostgreSQL concurrent-deliver outbox: %v", err)
	}
	owned, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion:       taskorchestration.OwnedTransportV1,
		Authorities:            []taskorchestration.WorkerAuthority{worker},
		Now:                    func() time.Time { return now.Add(2 * time.Second) },
		PrerequisiteRetryDelay: time.Minute,
		PrerequisitesSatisfied: acceptOwnedTransportPrerequisites,
	})
	if err != nil {
		t.Fatalf("create concurrent-deliver owned transport: %v", err)
	}
	transport := newBlockingOwnedTransport(owned, 32)
	defer transport.releaseAll()
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create PostgreSQL concurrent-deliver dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim PostgreSQL concurrent-deliver operation: count=%d err=%v", len(batch.Claims), err)
	}

	const concurrency = 32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	startDeliveries := make(chan struct{})
	results := make(chan error, concurrency)
	for range concurrency {
		go func() {
			<-startDeliveries
			_, deliverErr := dispatcher.Deliver(ctx, batch.Claims[0])
			results <- deliverErr
		}()
	}
	close(startDeliveries)
	select {
	case <-transport.entered:
	case <-ctx.Done():
		t.Fatal("no concurrent delivery reached the owned transport")
	}
	completed := 0
	duplicateSend := false
	for completed < concurrency-1 && !duplicateSend {
		select {
		case <-transport.entered:
			duplicateSend = true
		case <-results:
			completed++
		case <-ctx.Done():
			transport.releaseAll()
			t.Fatal("concurrent delivery attempts did not settle")
		}
	}
	transport.releaseAll()
	for completed < concurrency {
		select {
		case <-results:
			completed++
		case <-ctx.Done():
			t.Fatal("concurrent delivery results did not settle")
		}
	}
	if duplicateSend || transport.deliveries() != 1 {
		t.Fatalf("same PostgreSQL claim reached transport %d times", transport.deliveries())
	}
	view, err := dispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil {
		t.Fatalf("inspect concurrent PostgreSQL delivery: %v", err)
	}
	if view.Disposition == taskorchestration.DeliveryReconciliationRequired {
		reconciled, reconcileErr := dispatcher.Reconcile(context.Background(), taskorchestration.DeliveryReconcileRequest{
			Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
		})
		if reconcileErr != nil || reconciled.Disposition != taskorchestration.DeliveryAccepted {
			t.Fatalf("reconcile concurrent PostgreSQL delivery: result=%+v err=%v", reconciled, reconcileErr)
		}
	} else if !view.Terminal || view.Disposition != taskorchestration.DeliveryAccepted {
		t.Fatalf("concurrent PostgreSQL delivery view = %+v", view)
	}
}

func TestPostgresClaimSupersedesPendingWorkBehindCommittedCancellationFence(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 5, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-supersede-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-supersede-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-supersede-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-supersede-start", "postgres-supersede-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start PostgreSQL supersession Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-supersede-work", "postgres-supersede-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-supersede-work-available"),
	))
	if err != nil {
		t.Fatalf("commit PostgreSQL pending enactment: %v", err)
	}
	cancelHeader := intentHeader(t, "postgres-supersede-cancel", "postgres-supersede-task", now.Add(2*time.Second))
	cancelHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	cancel, err := adapter.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
		cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
	))
	if err != nil || len(cancel.EnactmentRefs) != 1 {
		t.Fatalf("commit PostgreSQL cancellation fence: count=%d err=%v", len(cancel.EnactmentRefs), err)
	}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(3 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, unusedOwnedTransport{})
	if err != nil {
		t.Fatalf("create PostgreSQL supersession dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 2,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim PostgreSQL cancellation enactment: count=%d err=%v", len(batch.Claims), err)
	}
	if batch.Claims[0].OperationID != cancel.EnactmentRefs[0].OperationID {
		t.Fatal("PostgreSQL dispatcher claimed stale work before its cancellation fence")
	}
	stale, err := dispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || !stale.Terminal || stale.Disposition != taskorchestration.DeliverySuperseded {
		t.Fatalf("inspect PostgreSQL superseded work: view=%+v err=%v", stale, err)
	}
}

func TestPostgresClaimResponseLossRecoversOriginalOperationAfterLeaseExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 10, 0, 0, time.UTC)
	current := now.Add(2 * time.Second)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-claim-loss-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-claim-loss-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-claim-loss-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-claim-loss-start", "postgres-claim-loss-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start PostgreSQL claim-loss Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-claim-loss-work", "postgres-claim-loss-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-claim-loss-work-available"),
	))
	if err != nil {
		t.Fatalf("commit PostgreSQL claim-loss outbox: %v", err)
	}
	faults := &taskorchestration.DeliveryFaultController{}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
		Faults:           faults,
	}, unusedOwnedTransport{})
	if err != nil {
		t.Fatalf("create fault-injected PostgreSQL dispatcher: %v", err)
	}
	if err := faults.FailNextAt(taskorchestration.DeliveryFaultAfterClaimCommit); err != nil {
		t.Fatalf("inject post-claim crash: %v", err)
	}
	_, err = dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	var deliveryError *taskorchestration.DeliveryError
	if !errors.As(err, &deliveryError) || deliveryError.Code() != taskorchestration.DeliveryUnavailable {
		t.Fatalf("post-claim crash = %T, want safe unavailable error", err)
	}
	current = current.Add(time.Minute + time.Nanosecond)
	restartedAdapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return current },
	})
	restartedDispatcher, err := restartedAdapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	}, unusedOwnedTransport{})
	if err != nil {
		t.Fatalf("restart claim-loss dispatcher: %v", err)
	}
	recovered, err := restartedDispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(recovered.Claims) != 1 {
		t.Fatalf("recover lost PostgreSQL claim: count=%d err=%v", len(recovered.Claims), err)
	}
	if recovered.Claims[0].OperationID != work.EnactmentRefs[0].OperationID ||
		recovered.Claims[0].LeaseFence <= 1 {
		t.Fatal("claim-loss recovery changed the OperationID or reused the stale lease fence")
	}
}

func TestPostgresCrashAfterSendThenCancellationStillRequiresInspection(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 20, 0, 0, time.UTC)
	current := now.Add(2 * time.Second)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-send-crash-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-send-crash-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-send-crash-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-send-crash-start", "postgres-send-crash-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start PostgreSQL send-crash Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-send-crash-work", "postgres-send-crash-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-send-crash-work-available"),
	))
	if err != nil {
		t.Fatalf("commit PostgreSQL send-crash outbox: %v", err)
	}
	transport, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion:       taskorchestration.OwnedTransportV1,
		Authorities:            []taskorchestration.WorkerAuthority{worker},
		Now:                    func() time.Time { return current },
		PrerequisiteRetryDelay: time.Minute,
		PrerequisitesSatisfied: acceptOwnedTransportPrerequisites,
	})
	if err != nil {
		t.Fatalf("create send-crash transport: %v", err)
	}
	faults := &taskorchestration.DeliveryFaultController{}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
		Faults:           faults,
	}, transport)
	if err != nil {
		t.Fatalf("create send-crash dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim send-crash delivery: count=%d err=%v", len(batch.Claims), err)
	}
	if err := faults.FailNextAt(taskorchestration.DeliveryFaultAfterSend); err != nil {
		t.Fatalf("inject post-send crash: %v", err)
	}
	_, err = dispatcher.Deliver(context.Background(), batch.Claims[0])
	var deliveryError *taskorchestration.DeliveryError
	if !errors.As(err, &deliveryError) || deliveryError.Code() != taskorchestration.DeliveryUnavailable {
		t.Fatalf("post-send crash = %T, want safe unavailable error", err)
	}
	cancelHeader := intentHeader(t, "postgres-send-crash-cancel", "postgres-send-crash-task", now.Add(3*time.Second))
	cancelHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	if _, err := adapter.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
		cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
	)); err != nil {
		t.Fatalf("commit PostgreSQL cancellation after ambiguous send: %v", err)
	}
	continued, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
	if err != nil || continued.Disposition != taskorchestration.DeliveryReconciliationRequired ||
		continued.DeliveryCount != 1 {
		t.Fatalf("continue PostgreSQL send-started claim after cancellation: result=%+v err=%v", continued, err)
	}
	current = current.Add(time.Minute + time.Nanosecond)
	restartedAdapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return current },
	})
	restartedDispatcher, err := restartedAdapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	}, transport.Restart())
	if err != nil {
		t.Fatalf("restart post-send dispatcher: %v", err)
	}
	retry, err := restartedDispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil {
		t.Fatalf("claim PostgreSQL work after ambiguous send and cancellation: %v", err)
	}
	for _, claim := range retry.Claims {
		if claim.OperationID == work.EnactmentRefs[0].OperationID {
			t.Fatal("ambiguous PostgreSQL operation was blindly redelivered after cancellation")
		}
	}
	ambiguous, err := restartedDispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || ambiguous.Disposition != taskorchestration.DeliveryReconciliationRequired ||
		ambiguous.Terminal {
		t.Fatalf("post-send crash inspection = %+v err=%v", ambiguous, err)
	}
	reconciled, err := restartedDispatcher.Reconcile(context.Background(), taskorchestration.DeliveryReconcileRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || reconciled.Disposition != taskorchestration.DeliveryAccepted ||
		reconciled.DeliveryCount != 1 {
		t.Fatalf("reconcile post-send crash: result=%+v err=%v", reconciled, err)
	}
}

func TestPostgresCancellationAfterReclaimSupersedesOperationWhenInspectionProvedUnknown(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 25, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-inspected-unknown-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-inspected-unknown-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-inspected-unknown-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-inspected-unknown-start", "postgres-inspected-unknown-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start PostgreSQL inspected-unknown Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-inspected-unknown-work", "postgres-inspected-unknown-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-inspected-unknown-work-available"),
	))
	if err != nil {
		t.Fatalf("commit PostgreSQL inspected-unknown outbox: %v", err)
	}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, fixedOutcomeTransport{response: taskorchestration.OwnedTransportResponse{
		Version: taskorchestration.OwnedTransportV1,
		Outcome: taskorchestration.OwnedTransportUnknown,
	}})
	if err != nil {
		t.Fatalf("create PostgreSQL inspected-unknown dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim PostgreSQL inspected-unknown operation: count=%d err=%v", len(batch.Claims), err)
	}
	ambiguous, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
	if err != nil || ambiguous.Disposition != taskorchestration.DeliveryReconciliationRequired {
		t.Fatalf("record PostgreSQL unknown send outcome: result=%+v err=%v", ambiguous, err)
	}
	unknown, err := dispatcher.Reconcile(context.Background(), taskorchestration.DeliveryReconcileRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || unknown.Disposition != taskorchestration.DeliveryPending ||
		unknown.DeliveryCount != 1 {
		t.Fatalf("reconcile PostgreSQL authoritative unknown: result=%+v err=%v", unknown, err)
	}
	retry, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(retry.Claims) != 1 ||
		retry.Claims[0].OperationID != work.EnactmentRefs[0].OperationID {
		t.Fatalf("reclaim PostgreSQL operation after authoritative unknown: batch=%+v err=%v", retry, err)
	}
	cancelHeader := intentHeader(t, "postgres-inspected-unknown-cancel", "postgres-inspected-unknown-task", now.Add(3*time.Second))
	cancelHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	if _, err := adapter.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
		cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
	)); err != nil {
		t.Fatalf("commit PostgreSQL cancellation after inspected-unknown reclaim: %v", err)
	}
	stale, err := dispatcher.Deliver(context.Background(), retry.Claims[0])
	if err != nil || stale.OperationID != work.EnactmentRefs[0].OperationID ||
		stale.Disposition != taskorchestration.DeliverySuperseded {
		t.Fatalf("deliver reclaimed PostgreSQL operation after cancellation: result=%+v err=%v", stale, err)
	}
	view, err := dispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || !view.Terminal || view.Disposition != taskorchestration.DeliverySuperseded {
		t.Fatalf("inspect PostgreSQL superseded unknown operation: view=%+v err=%v", view, err)
	}
}

func TestPostgresDispositionResponseLossRetainsTerminalAcceptance(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 30, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-disposition-loss-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-disposition-loss-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-disposition-loss-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-disposition-loss-start", "postgres-disposition-loss-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start disposition-loss Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-disposition-loss-work", "postgres-disposition-loss-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-disposition-loss-work-available"),
	))
	if err != nil {
		t.Fatalf("commit disposition-loss outbox: %v", err)
	}
	transport, err := taskorchestration.NewDeterministicOwnedTransport(taskorchestration.OwnedTransportConfig{
		SupportedVersion:       taskorchestration.OwnedTransportV1,
		Authorities:            []taskorchestration.WorkerAuthority{worker},
		Now:                    func() time.Time { return now },
		PrerequisiteRetryDelay: time.Minute,
		PrerequisitesSatisfied: acceptOwnedTransportPrerequisites,
	})
	if err != nil {
		t.Fatalf("create disposition-loss transport: %v", err)
	}
	faults := &taskorchestration.DeliveryFaultController{}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker}, Faults: faults,
	}, transport)
	if err != nil {
		t.Fatalf("create disposition-loss dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim disposition-loss delivery: count=%d err=%v", len(batch.Claims), err)
	}
	if err := faults.FailNextAt(taskorchestration.DeliveryFaultAfterDispositionCommit); err != nil {
		t.Fatalf("inject disposition response loss: %v", err)
	}
	_, err = dispatcher.Deliver(context.Background(), batch.Claims[0])
	var deliveryError *taskorchestration.DeliveryError
	if !errors.As(err, &deliveryError) || deliveryError.Code() != taskorchestration.DeliveryUnavailable {
		t.Fatalf("disposition response loss = %T, want safe unavailable error", err)
	}
	restartedAdapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Hour) },
	})
	restartedDispatcher, err := restartedAdapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(time.Hour) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport.Restart())
	if err != nil {
		t.Fatalf("restart disposition-loss dispatcher: %v", err)
	}
	view, err := restartedDispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || !view.Terminal || view.Disposition != taskorchestration.DeliveryAccepted ||
		view.ResultDigest == (taskorchestration.DeliveryResultDigest{}) || view.DeliveryCount != 1 {
		t.Fatalf("inspect committed disposition after response loss: view=%+v err=%v", view, err)
	}
	retry, err := restartedDispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(retry.Claims) != 0 {
		t.Fatalf("terminal disposition was redelivered: count=%d err=%v", len(retry.Claims), err)
	}
}

func TestPostgresCrashBeforeSendReclaimsWithoutReconciliation(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 40, 0, 0, time.UTC)
	current := now.Add(2 * time.Second)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-before-send-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-before-send-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-before-send-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-before-send-start", "postgres-before-send-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start before-send Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-before-send-work", "postgres-before-send-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-before-send-work-available"),
	))
	if err != nil {
		t.Fatalf("commit before-send outbox: %v", err)
	}
	transport := &countingOwnedTransport{}
	faults := &taskorchestration.DeliveryFaultController{}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker}, Faults: faults,
	}, transport)
	if err != nil {
		t.Fatalf("create before-send dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim before-send delivery: count=%d err=%v", len(batch.Claims), err)
	}
	if err := faults.FailNextAt(taskorchestration.DeliveryFaultBeforeSend); err != nil {
		t.Fatalf("inject before-send crash: %v", err)
	}
	_, err = dispatcher.Deliver(context.Background(), batch.Claims[0])
	var deliveryError *taskorchestration.DeliveryError
	if !errors.As(err, &deliveryError) || deliveryError.Code() != taskorchestration.DeliveryUnavailable {
		t.Fatalf("before-send crash = %T, want safe unavailable error", err)
	}
	if transport.deliveries() != 0 {
		t.Fatal("before-send crash called remote transport")
	}
	current = current.Add(time.Minute + time.Nanosecond)
	restartedAdapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return current },
	})
	restartedDispatcher, err := restartedAdapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return current }, MaxBatchSize: 1, LeaseDuration: time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("restart before-send dispatcher: %v", err)
	}
	reclaimed, err := restartedDispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(reclaimed.Claims) != 1 {
		t.Fatalf("reclaim unsent operation: count=%d err=%v", len(reclaimed.Claims), err)
	}
	if reclaimed.Claims[0].OperationID != work.EnactmentRefs[0].OperationID ||
		reclaimed.Claims[0].LeaseFence <= batch.Claims[0].LeaseFence {
		t.Fatal("before-send recovery changed the OperationID or lease fence")
	}
}

func TestPostgresConcurrentDispatchersProduceOneLeaseWinner(t *testing.T) {
	now := time.Date(2026, time.July, 27, 18, 50, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-claim-race-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	workers := []taskorchestration.WorkerAuthority{
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "postgres-claim-race-worker-a"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "postgres-claim-race-worker-b"), taskorchestration.AuthorizationGeneration(1),
		),
	}
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-claim-race-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-claim-race-start", "postgres-claim-race-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start claim-race Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-claim-race-work", "postgres-claim-race-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, workers[0], operationID(t, "postgres-claim-race-work-available"),
	))
	if err != nil {
		t.Fatalf("commit claim-race outbox: %v", err)
	}
	dispatchers := make([]taskorchestration.OutboxDispatcher, len(workers))
	for index, worker := range workers {
		dispatchers[index], err = adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
			Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
			LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
			Authorities: []taskorchestration.WorkerAuthority{worker},
		}, unusedOwnedTransport{})
		if err != nil {
			t.Fatalf("create concurrent dispatcher %d: %v", index, err)
		}
	}
	type claimOutcome struct {
		batch taskorchestration.DeliveryClaimBatch
		err   error
	}
	outcomes := make([]claimOutcome, len(dispatchers))
	ready := make(chan struct{})
	var wait sync.WaitGroup
	for index := range dispatchers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-ready
			outcomes[index].batch, outcomes[index].err = dispatchers[index].Claim(
				context.Background(), taskorchestration.DeliveryClaimRequest{
					Authority: workers[index], Limit: 1,
				},
			)
		}(index)
	}
	close(ready)
	wait.Wait()
	winners := 0
	for _, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent claim failed: %v", outcome.err)
		}
		if len(outcome.batch.Claims) > 1 {
			t.Fatal("concurrent dispatcher exceeded the batch bound")
		}
		if len(outcome.batch.Claims) == 1 {
			winners++
			if outcome.batch.Claims[0].OperationID != work.EnactmentRefs[0].OperationID {
				t.Fatal("concurrent claim winner changed the OperationID")
			}
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent lease winners = %d, want exactly 1", winners)
	}
}

func TestPostgresDecisionAndClaimNeverPerformRemoteIO(t *testing.T) {
	now := time.Date(2026, time.July, 27, 19, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-remote-boundary-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-remote-boundary-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	transport := &countingOwnedTransport{}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create remote-boundary dispatcher: %v", err)
	}
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-remote-boundary-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-remote-boundary-start", "postgres-remote-boundary-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start remote-boundary Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-remote-boundary-work", "postgres-remote-boundary-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	if _, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-remote-boundary-work-available"),
	)); err != nil {
		t.Fatalf("commit remote-boundary outbox: %v", err)
	}
	if transport.deliveries() != 0 {
		t.Fatal("Decision transaction performed remote transport I/O")
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim remote-boundary outbox: count=%d err=%v", len(batch.Claims), err)
	}
	if transport.deliveries() != 0 {
		t.Fatal("claim transaction performed remote transport I/O")
	}
	if _, err := dispatcher.Deliver(context.Background(), batch.Claims[0]); err != nil {
		t.Fatalf("perform explicit remote delivery: %v", err)
	}
	if transport.deliveries() != 1 {
		t.Fatal("explicit Deliver did not own the single remote I/O")
	}
}

func TestPostgresTimeoutDuringSendPersistsReconciliationRequired(t *testing.T) {
	now := time.Date(2026, time.July, 27, 19, 10, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-timeout-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-timeout-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-timeout-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-timeout-start", "postgres-timeout-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start timeout Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-timeout-work", "postgres-timeout-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-timeout-work-available"),
	))
	if err != nil {
		t.Fatalf("commit timeout outbox: %v", err)
	}
	transport := &contextTimeoutTransport{entered: make(chan struct{})}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now: func() time.Time { return now.Add(2 * time.Second) }, MaxBatchSize: 1,
		LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities: []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create timeout dispatcher: %v", err)
	}
	batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: worker, Limit: 1,
	})
	if err != nil || len(batch.Claims) != 1 {
		t.Fatalf("claim timeout delivery: count=%d err=%v", len(batch.Claims), err)
	}
	deliveryContext, cancelDelivery := context.WithCancel(context.Background())
	type deliveryOutcome struct {
		result taskorchestration.DeliveryResult
		err    error
	}
	completed := make(chan deliveryOutcome, 1)
	go func() {
		result, deliverErr := dispatcher.Deliver(deliveryContext, batch.Claims[0])
		completed <- deliveryOutcome{result: result, err: deliverErr}
	}()
	<-transport.entered
	cancelDelivery()
	outcome := <-completed
	if outcome.err != nil || outcome.result.OperationID != work.EnactmentRefs[0].OperationID ||
		outcome.result.Disposition != taskorchestration.DeliveryReconciliationRequired {
		t.Fatalf("timeout delivery outcome = %+v err=%v", outcome.result, outcome.err)
	}
	view, err := dispatcher.Inspect(context.Background(), taskorchestration.DeliveryInspectionRequest{
		Authority: worker, OperationID: work.EnactmentRefs[0].OperationID,
	})
	if err != nil || view.Terminal ||
		view.Disposition != taskorchestration.DeliveryReconciliationRequired {
		t.Fatalf("inspect timeout delivery: view=%+v err=%v", view, err)
	}
}

type contextTimeoutTransport struct {
	entered chan struct{}
}

func (transport *contextTimeoutTransport) Deliver(
	ctx context.Context,
	_ taskorchestration.OwnedTransportRequest,
) (taskorchestration.OwnedTransportResponse, error) {
	close(transport.entered)
	<-ctx.Done()
	return taskorchestration.OwnedTransportResponse{}, ctx.Err()
}

func (transport *contextTimeoutTransport) Inspect(
	context.Context,
	taskorchestration.OwnedTransportInspection,
) (taskorchestration.OwnedTransportResponse, error) {
	return taskorchestration.OwnedTransportResponse{}, errors.New("timeout inspection unavailable")
}
