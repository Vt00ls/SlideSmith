package runtimeexecution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

// ReconcilingResult encodes the result of one reconciliation attempt.
type ReconcilingResult uint8

const (
	ReconcilingResultStable            ReconcilingResult = iota + 1 // runtime is no longer reconciling
	ReconcilingResultStillReconciling                               // still waiting for an external observation
	ReconcilingResultTerminalRejected                               // permanently rejected (zero-lease proved)
	ReconcilingResultTerminalLost                                   // unrecoverable (possible process effect, can't reconcile)
	ReconcilingResultTerminalTimedOut                               // deadline expired, no lease
	ReconcilingResultIntegrityIncident                              // different digest conflict with retained binding
)

// ReconciliationObservation is a portable snapshot of what the reconciliation
// adapter observed about the original operation.
type ReconciliationObservation struct {
	RuntimeRunID         RuntimeRunID
	OperationID          OperationID
	ObservedDigest       Digest
	ObservedState        RuntimeState
	ObservedOutcome      RuntimeOutcome
	HasLeaseEvidence     bool
	HasProcessEvidence   bool
	HasExternalAck       bool
	ExternalOperationID  string
	ObservedAt           time.Time
	DiagnosticEvidenceID EvidenceID
}

// ReconciliationStateMachine owns the Inspect→Replay→Reconcile loop for a
// single Runtime Run that has entered the Reconciling state. It is the
// authoritative authority for reconciliation transitions.
type ReconciliationStateMachine struct {
	store    ReconciliationStore
	clock    func() time.Time
	observer ReconciliationObserver
}

// ReconciliationStore is the persistence contract for reconciliation state.
type ReconciliationStore interface {
	// LoadReconciliationObligation returns the durable reconciliation record
	// for a runtime run, or nil if none exists.
	LoadReconciliationObligation(ctx context.Context, runtimeRunID RuntimeRunID) (*ReconciliationObligation, error)

	// RecordReconciliationObservation persists an observation and advances
	// the obligation state.
	RecordReconciliationObservation(ctx context.Context, obligation ReconciliationObligation, observation ReconciliationObservation) error

	// ResolveReconciliation marks an obligation as resolved and persists the
	// final disposition.
	ResolveReconciliation(ctx context.Context, obligation ReconciliationObligation, result ReconcilingResult) error

	// LoadRuntimeSnapshot loads the current runtime snapshot for Inspect.
	// The store is responsible for resolving the correct authority scope.
	LoadRuntimeSnapshot(ctx context.Context, runtimeRunID RuntimeRunID) (RuntimeSnapshot, error)

	// ReplayExactCommand replays an exact previously-accepted command and
	// returns the original decision and current snapshot.
	ReplayExactCommand(ctx context.Context, command RuntimeCommand) (RuntimeDecision, error)

	// LoadRuntimeSnapshotForInspect loads a runtime snapshot using the
	// provided authority scope (workspace + authority).
	LoadRuntimeSnapshotForInspect(ctx context.Context, ref RuntimeRunRef) (RuntimeSnapshot, error)
}

// ReconciliationObserver is an external adapter that inspects the original
// operation (e.g., queries the worker, checks the lease, or polls a queue).
type ReconciliationObserver interface {
	// ObserveOriginalOperation inspects the original operation and returns
	// what it found. The observer must never create a new operation, grant,
	// or generation.
	ObserveOriginalOperation(ctx context.Context, ref ReconciliationRef) (ReconciliationObservation, error)
}

// ReconciliationRef identifies the original operation for observation.
type ReconciliationRef struct {
	RuntimeRunID            RuntimeRunID
	OperationID             OperationID
	StartDigest             Digest
	LeaseAcquireOperationID OperationID
	LeaseAcquireDigest      Digest
	HasLease                bool
	SandboxLeaseID          SandboxLeaseID
	WorkerAuthorityID       WorkerAuthorityID
	ExecutionNodeID         ExecutionNodeID
}

// ReconciliationObligation is the durable record of a runtime run that must
// be reconciled before it can become terminal.
type ReconciliationObligation struct {
	RuntimeRunID           RuntimeRunID
	OperationID            OperationID
	DecisionID             RuntimeDecisionID
	StartDigest            Digest // canonical request digest of the original start
	AuthorityKind          AuthorityKind
	AuthorityID            AuthorityID
	AuthorityGeneration    AuthorizationGeneration
	RuntimeRevision        RuntimeRevision
	OperationGeneration    OperationGeneration
	RuntimeFence           RuntimeFence
	Reason                 ReconciliationReason
	Status                 ReconciliationStatus
	Result                 ReconcilingResult
	FirstRecordedAt        time.Time
	LastRecordedAt         time.Time
	ObservationCount       uint64
	Unresolved             bool
	NextRetryAt            time.Time
	SafeFailureCount       uint64
	StaleEvidenceCount     uint64
	EvidenceRootID         EvidenceID
	EvidenceRootDigest     Digest
	DiagnosticObservations []ReconciliationObservation
}

// NewReconciliationStateMachine creates a new state machine.
func NewReconciliationStateMachine(
	store ReconciliationStore,
	clock func() time.Time,
	observer ReconciliationObserver,
) *ReconciliationStateMachine {
	if clock == nil {
		clock = time.Now
	}
	return &ReconciliationStateMachine{
		store:    store,
		clock:    clock,
		observer: observer,
	}
}

// Inspect loads the current runtime snapshot and reconciliation obligation
// without executing any transition. The ref carries the authority scope needed
// for authorization.
func (machine *ReconciliationStateMachine) Inspect(
	ctx context.Context,
	ref RuntimeRunRef,
) (RuntimeSnapshot, *ReconciliationObligation, error) {
	if ctx == nil || ctx.Err() != nil {
		return RuntimeSnapshot{}, nil, newError(ErrorDependencyUnavailable)
	}
	snapshot, err := machine.store.LoadRuntimeSnapshotForInspect(ctx, ref)
	if err != nil {
		return RuntimeSnapshot{}, nil, err
	}
	obligation, err := machine.store.LoadReconciliationObligation(ctx, ref.RuntimeRunID)
	if err != nil {
		return RuntimeSnapshot{}, nil, err
	}
	return snapshot, obligation, nil
}

// Replay attempts exact replay of a previously accepted command. If the
// command was already accepted with the same digest, it returns the original
// decision. If the digest differs, it returns an integrity conflict.
func (machine *ReconciliationStateMachine) Replay(
	ctx context.Context,
	command RuntimeCommand,
) (RuntimeDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	return machine.store.ReplayExactCommand(ctx, command)
}

// Reconcile drives one reconciliation loop iteration. It observes the
// original operation through the adapter and applies the result.
func (machine *ReconciliationStateMachine) Reconcile(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
) (ReconcilingResult, RuntimeSnapshot, error) {
	if ctx == nil || ctx.Err() != nil {
		return ReconcilingResultStillReconciling, RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}

	snapshot, err := machine.store.LoadRuntimeSnapshot(ctx, runtimeRunID)
	if err != nil {
		return ReconcilingResultStillReconciling, RuntimeSnapshot{}, err
	}
	obligation, err := machine.store.LoadReconciliationObligation(ctx, runtimeRunID)
	if err != nil {
		return ReconcilingResultStillReconciling, RuntimeSnapshot{}, err
	}
	if snapshot.State != RuntimeReconciling || obligation == nil || !obligation.Unresolved {
		return ReconcilingResultStable, snapshot, nil
	}

	now := machine.clock()
	if !obligation.NextRetryAt.IsZero() && now.Before(obligation.NextRetryAt) {
		return ReconcilingResultStillReconciling, snapshot, nil
	}

	// Zero-lease evidence check: if PG proves no lease or process effect
	// ever committed, we can finalize as Rejected + NoLeasePhysicalDisposition.
	// This applies to all pre-lease ambiguity reasons (transport timeout,
	// callback loss, poll interruption, queue claim loss, worker response
	// loss, lease transaction ambiguous).
	if isPreLeaseAmbiguityReason(obligation.Reason) {
		// If there is no lease committed AND no process evidence, it's a
		// zero-lease run that should terminate as Rejected.
		if snapshot.Lease.AcquireStatus != LeaseGranted &&
			snapshot.Lease.Disposition != LeaseActive &&
			snapshot.Capacity.NoLease == NoLeaseDispositionRecorded {
			result := ReconcilingResultTerminalRejected
			if err := machine.store.ResolveReconciliation(ctx, *obligation, result); err != nil {
				return ReconcilingResultStillReconciling, RuntimeSnapshot{}, err
			}
			// Re-read snapshot after resolution
			snapshot, err = machine.store.LoadRuntimeSnapshot(ctx, runtimeRunID)
			return result, snapshot, err
		}
	}

	// If no observer is configured, we can't make progress.
	if machine.observer == nil {
		// Increment safe failure count and back off.
		obligation.SafeFailureCount++
		obligation.LastRecordedAt = now
		obligation.NextRetryAt = now.Add(reconciliationBackoff(obligation.SafeFailureCount))
		if err := machine.store.RecordReconciliationObservation(ctx, *obligation, ReconciliationObservation{
			RuntimeRunID: runtimeRunID, OperationID: obligation.OperationID,
			ObservedAt: now,
		}); err != nil {
			return ReconcilingResultStillReconciling, RuntimeSnapshot{}, err
		}
		return ReconcilingResultStillReconciling, snapshot, nil
	}

	// Build the observation reference from the current snapshot.
	ref := ReconciliationRef{
		RuntimeRunID:            runtimeRunID,
		OperationID:             obligation.OperationID,
		LeaseAcquireOperationID: snapshot.Lease.AcquireOperationID,
		LeaseAcquireDigest:      snapshot.Lease.AcquireDigest,
		HasLease:                snapshot.Lease.AcquireStatus == LeaseGranted && snapshot.Lease.Disposition == LeaseActive,
		SandboxLeaseID:          snapshot.Lease.LeaseID,
		WorkerAuthorityID:       snapshot.Lease.WorkerAuthorityID,
		ExecutionNodeID:         snapshot.Operation.ExecutionNodeID,
	}

	observation, obsErr := machine.observer.ObserveOriginalOperation(ctx, ref)
	if obsErr != nil {
		// Observer unavailable - back off and retry
		obligation.SafeFailureCount++
		obligation.LastRecordedAt = now
		obligation.NextRetryAt = now.Add(reconciliationBackoff(obligation.SafeFailureCount))
		if err := machine.store.RecordReconciliationObservation(ctx, *obligation, ReconciliationObservation{
			RuntimeRunID: runtimeRunID, OperationID: obligation.OperationID,
			ObservedAt: now,
		}); err != nil {
			return ReconcilingResultStillReconciling, RuntimeSnapshot{}, err
		}
		return ReconcilingResultStillReconciling, snapshot, nil
	}

	// Record the observation
	obligation.ObservationCount++
	obligation.LastRecordedAt = now
	obligation.DiagnosticObservations = append(obligation.DiagnosticObservations, observation)
	if len(obligation.DiagnosticObservations) > maxDiagnosticObservations {
		obligation.DiagnosticObservations = obligation.DiagnosticObservations[len(obligation.DiagnosticObservations)-maxDiagnosticObservations:]
	}

	if err := machine.store.RecordReconciliationObservation(ctx, *obligation, observation); err != nil {
		return ReconcilingResultStillReconciling, RuntimeSnapshot{}, err
	}

	// Determine outcome from observation
	result := machine.evaluateObservation(obligation, snapshot, observation)

	if result == ReconcilingResultStillReconciling {
		// Schedule next attempt
		obligation.NextRetryAt = now.Add(reconciliationBackoff(obligation.SafeFailureCount + 1))
		if err := machine.store.RecordReconciliationObservation(ctx, *obligation, observation); err != nil {
			return ReconcilingResultStillReconciling, RuntimeSnapshot{}, err
		}
		return ReconcilingResultStillReconciling, snapshot, nil
	}

	// Resolve terminal
	if err := machine.store.ResolveReconciliation(ctx, *obligation, result); err != nil {
		return ReconcilingResultStillReconciling, RuntimeSnapshot{}, err
	}

	snapshot, err = machine.store.LoadRuntimeSnapshot(ctx, runtimeRunID)
	return result, snapshot, err
}

// evaluateObservation decides the reconciling result from an observation.
func (machine *ReconciliationStateMachine) evaluateObservation(
	obligation *ReconciliationObligation,
	snapshot RuntimeSnapshot,
	observation ReconciliationObservation,
) ReconcilingResult {
	// Integrity check: if the observed digest doesn't match the original
	// canonical start digest, it's an integrity incident. A zero digest
	// means the observer didn't report a digest (e.g., it only observed
	// state), which is not treated as a mismatch.
	if observation.ObservedDigest != (Digest{}) &&
		obligation.StartDigest != (Digest{}) &&
		!CompareReconciliationDigests(observation.ObservedDigest, obligation.StartDigest) {
		return ReconcilingResultIntegrityIncident
	}

	// If the operation has an external ack, we observe the existing external
	// operation outcome.
	if observation.HasExternalAck {
		switch observation.ObservedOutcome {
		case RuntimeSucceeded, RuntimeFailed:
			return ReconcilingResultStable
		case RuntimeCancelled:
			return ReconcilingResultStable
		case RuntimeLost:
			return ReconcilingResultTerminalLost
		default:
			// External ack exists but outcome is unknown - keep reconciling
			return ReconcilingResultStillReconciling
		}
	}

	// No external ack means the worker disappeared before ack. If there is
	// evidence of a lease and possible process effect, we can replay.
	if observation.HasLeaseEvidence {
		if observation.HasProcessEvidence {
			// Possible process effect exists - if we can't confirm the outcome,
			// this may become Lost
			snapshotLeaseActive := snapshot.Lease.AcquireStatus == LeaseGranted &&
				snapshot.Lease.Disposition == LeaseActive

			if snapshotLeaseActive && observation.ObservedState == RuntimeRunning {
				// Lease active and process running - keep reconciling
				return ReconcilingResultStillReconciling
			}

			if observation.ObservedState == RuntimeTerminal {
				return ReconcilingResultStable
			}

			// Can't determine outcome with possible process effect
			// This will eventually become Lost after enough attempts
			if obligation.SafeFailureCount >= maxReconciliationAttempts {
				return ReconcilingResultTerminalLost
			}
			return ReconcilingResultStillReconciling
		}
	}

	// Stale observation: no lease, no process, no external ack
	if observation.ObservedState == RuntimeTerminal {
		return ReconcilingResultStable
	}

	return ReconcilingResultStillReconciling
}

// reconciliationBackoff returns an exponential backoff duration based on the
// failure count, capped at a maximum.
func reconciliationBackoff(failureCount uint64) time.Duration {
	const (
		base    = 500 * time.Millisecond
		maxWait = 30 * time.Second
	)
	wait := base
	for i := uint64(0); i < failureCount && wait < maxWait; i++ {
		wait *= 2
	}
	if wait > maxWait {
		return maxWait
	}
	return wait
}

const (
	maxDiagnosticObservations = 8
	maxReconciliationAttempts = 10
)

// isPreLeaseAmbiguityReason returns true when the reconciliation reason
// indicates a pre-lease ambiguity where zero-lease proof may apply.
func isPreLeaseAmbiguityReason(reason ReconciliationReason) bool {
	// ReconciliationTransportAmbiguous is the primary pre-lease ambiguity
	// reason. Pre-lease terminal and lease-commit ambiguity reasons also
	// qualify for zero-lease evaluation.
	return reason == ReconciliationTransportAmbiguous ||
		reason == ReconciliationProjectionDelivery
}

// validReconciliationRef checks that a ReconciliationRef has all required fields.
func validReconciliationRef(ref ReconciliationRef) bool {
	return validOpaqueID(ref.RuntimeRunID.String()) &&
		validOpaqueID(ref.OperationID.String())
}

// ReconciliationDecision encapsulates the result of a reconciliation action.
type ReconciliationDecision struct {
	Result               ReconcilingResult
	RuntimeSnapshot      RuntimeSnapshot
	Obligation           *ReconciliationObligation
	NoLeaseDisposition   bool
	PhysicalReleaseReady bool
	StaleEvidence        bool
	IntegrityIncident    bool
}

// ReconcileCommand is the top-level entry point for the reconciling loop.
// It drives Inspect→Replay→Reconcile for a runtime run in Reconciling state
// and returns the unified decision. This is the method wired into Execute
// when a runtime is in Reconciling state.
func (machine *ReconciliationStateMachine) ReconcileCommand(
	ctx context.Context,
	ref RuntimeRunRef,
) (*ReconciliationDecision, error) {
	snapshot, obligation, err := machine.Inspect(ctx, ref)
	if err != nil {
		return nil, err
	}
	if snapshot.State != RuntimeReconciling || obligation == nil || !obligation.Unresolved {
		return &ReconciliationDecision{
			Result:          ReconcilingResultStable,
			RuntimeSnapshot: snapshot,
			Obligation:      obligation,
		}, nil
	}

	result, finalSnapshot, err := machine.Reconcile(ctx, ref.RuntimeRunID)
	if err != nil {
		return nil, err
	}

	obligation, _ = machine.store.LoadReconciliationObligation(ctx, ref.RuntimeRunID)

	return &ReconciliationDecision{
		Result:               result,
		RuntimeSnapshot:      finalSnapshot,
		Obligation:           obligation,
		NoLeaseDisposition:   result == ReconcilingResultTerminalRejected,
		PhysicalReleaseReady: result == ReconcilingResultStable && snapshot.Lease.Disposition == LeaseReleased,
		StaleEvidence:        result == ReconcilingResultIntegrityIncident,
	}, nil
}

// EnsureReconciliationInterface checks compile-time interface satisfaction.
var _ interface {
	Inspect(ctx context.Context, ref RuntimeRunRef) (RuntimeSnapshot, *ReconciliationObligation, error)
	Replay(ctx context.Context, command RuntimeCommand) (RuntimeDecision, error)
	Reconcile(ctx context.Context, runtimeRunID RuntimeRunID) (ReconcilingResult, RuntimeSnapshot, error)
} = (*ReconciliationStateMachine)(nil)

// canonicalReconciliationDigest produces a stable digest for a reconciliation
// obligation, binding runtime run, reason, and version.
func canonicalReconciliationDigest(runtimeRunID RuntimeRunID, reason ReconciliationReason, revision RuntimeRevision) Digest {
	payload := fmt.Sprintf("slidesmith.reconciliation-obligation/v1\n%s\n%d\n%d", runtimeRunID.String(), reason, revision)
	return Digest(sha256.Sum256([]byte(payload)))
}

// IsZeroLeaseProved returns true when snapshot evidence proves no lease or
// process effect committed.
func IsZeroLeaseProved(snapshot RuntimeSnapshot) bool {
	return snapshot.Lease.AcquireStatus != LeaseGranted &&
		snapshot.Lease.Disposition != LeaseActive &&
		snapshot.Capacity.NoLease == NoLeaseDispositionRecorded
}

// IsPossibleProcessEffect returns true when there is evidence that a lease
// was granted and a worker may have started processing.
func IsPossibleProcessEffect(snapshot RuntimeSnapshot) bool {
	return snapshot.Lease.AcquireStatus == LeaseGranted &&
		snapshot.Lease.Disposition == LeaseActive &&
		snapshot.Worker.Status != WorkerOperationNone
}

// CompareReconciliationDigests checks whether two digests are identical for
// exact repair matching.
func CompareReconciliationDigests(a, b Digest) bool {
	return bytes.Equal(a[:], b[:])
}

// IsStaleReconciliationEvidence returns true when the observation timestamp
// is before the current runtime fence was established, meaning the evidence
// predates the fence and is stale.
func IsStaleReconciliationEvidence(observedAt time.Time, runtimeFence RuntimeFence, fenceEstablishedAt time.Time) bool {
	return observedAt.Before(fenceEstablishedAt)
}
