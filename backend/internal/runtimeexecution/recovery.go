package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

// RecoveryPointRestore owns the authoritative restore state machine that
// advances generation, fences old work, and reclassifies pre-restore runtime
// runs. It never restores live sandbox, Runtime View, session, node DB, queue
// projection, or secret/cache state.
type RecoveryPointRestore struct {
	store    RecoveryStore
	clock    func() time.Time
}

// RecoveryStore is the persistence contract for recovery operations.
type RecoveryStore interface {
	// LoadRecoveryState returns the current recovery state.
	LoadRecoveryState(ctx context.Context) (*RecoveryState, error)

	// AdvanceRecoveryGeneration persists new generation, fence, and safety
	// epoch after a restore.
	AdvanceRecoveryGeneration(ctx context.Context, decision RestoreDecision) error

	// ListReconcilingRuntimes returns all runtimes in Reconciling state
	// that predate the current recovery fence.
	ListReconcilingRuntimes(ctx context.Context, beforeFence RuntimeFence) ([]RuntimeRunID, error)

	// ListActiveLeaseRuntimes returns all runtimes with active leases
	// that predate the current recovery fence.
	ListActiveLeaseRuntimes(ctx context.Context, beforeFence RuntimeFence) ([]RuntimeRunID, error)

	// ClassifyRuntimeAfterRestore atomically evaluates and transitions a
	// single runtime run based on post-restore evidence.
	ClassifyRuntimeAfterRestore(ctx context.Context, runtimeRunID RuntimeRunID, decision RestoreDecision) (*RestoreRuntimeClassification, error)

	// FenceRuntime permanently fences a runtime run, marking it as Lost.
	FenceRuntime(ctx context.Context, runtimeRunID RuntimeRunID, fence RuntimeFence) error
}

// RecoveryState is the singleton recovery authority record.
type RecoveryState struct {
	Generation    RecoveryGeneration
	Fence         RecoveryFence
	SafetyEpoch   SafetyEpoch
	Mode          OperationalMode
	RestoredAt    time.Time
	RecoveryPointID string
}

// RecoveryGeneration identifies a restore generation.
type RecoveryGeneration uint64

// RecoveryFence is a monotonic fence that invalidates all pre-restore work.
type RecoveryFence uint64

// SafetyEpoch advances on every restore to invalidate old release/catalog
// bindings.
type SafetyEpoch uint64

// OperationalMode describes whether the platform accepts mutations.
type OperationalMode uint8

const (
	OperationalReadOnly  OperationalMode = iota + 1
	OperationalFullReady
)

// RestoreDecision records the authoritative outcome of a restore.
type RestoreDecision struct {
	RecoveryPointID           string
	NewGeneration             RecoveryGeneration
	NewFence                  RecoveryFence
	NewSafetyEpoch            SafetyEpoch
	PreviousGeneration        RecoveryGeneration
	PreviousFence             RecoveryFence
	PreviousSafetyEpoch       SafetyEpoch
	Mode                      OperationalMode
	FencedRuntimeRuns         []RuntimeRunID
	RejectedRuntimeRuns       []RuntimeRunID
	LostRuntimeRuns           []RuntimeRunID
	DecidedAt                 time.Time
}

// RestoreRuntimeClassification is the result of classifying one runtime run
// after a restore.
type RestoreRuntimeClassification struct {
	RuntimeRunID         RuntimeRunID
	Classification       RestoreClassification
	PreRestoreState      RuntimeState
	PostRestoreState     RuntimeState
	PostRestoreOutcome   RuntimeOutcome
	NoLeaseDisposition   bool
	CapacityReleased     bool
}

// RestoreClassification categorises what happens to a runtime run after restore.
type RestoreClassification uint8

const (
	RestoreClassificationNone          RestoreClassification = iota
	RestoreClassificationZeroLeaseRejected                          // no lease committed → Rejected
	RestoreClassificationAmbiguousReconcile                         // lease transaction ambiguous → reconcile
	RestoreClassificationPossibleEffectLost                         // possible process effect → Lost
	RestoreClassificationAlreadyTerminal                            // already terminal, no change
	RestoreClassificationFenced                                     // fenced by recovery
)

// NewRecoveryPointRestore creates a new recovery point restore state machine.
func NewRecoveryPointRestore(
	store RecoveryStore,
	clock func() time.Time,
) *RecoveryPointRestore {
	if clock == nil {
		clock = time.Now
	}
	return &RecoveryPointRestore{
		store: store,
		clock: clock,
	}
}

// Restore executes the complete restore decision: advance generations,
// classify all reconciling and active-lease runtimes, and fence old work.
func (recovery *RecoveryPointRestore) Restore(
	ctx context.Context,
	decision RestoreDecision,
) ([]RestoreRuntimeClassification, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, newError(ErrorDependencyUnavailable)
	}
	if !validRestoreDecision(decision) {
		return nil, newError(ErrorInvalidRequest)
	}

	// Step 1: advance the recovery generation, fence, and safety epoch.
	if err := recovery.store.AdvanceRecoveryGeneration(ctx, decision); err != nil {
		return nil, err
	}

	// Step 2: classify all pre-fence runtimes.
	classifications, err := recovery.classifyRuntimes(ctx, decision)
	if err != nil {
		return nil, err
	}

	return classifications, nil
}

// classifyRuntimes evaluates every runtime run that needs reclassification
// after a restore.
func (recovery *RecoveryPointRestore) classifyRuntimes(
	ctx context.Context,
	decision RestoreDecision,
) ([]RestoreRuntimeClassification, error) {
	var classifications []RestoreRuntimeClassification

	// Classify reconciling runtimes first.
	reconciling, err := recovery.store.ListReconcilingRuntimes(ctx, RuntimeFence(decision.PreviousFence))
	if err != nil {
		return nil, err
	}
	for _, runID := range reconciling {
		classification, err := recovery.store.ClassifyRuntimeAfterRestore(ctx, runID, decision)
		if err != nil {
			return nil, err
		}
		if classification != nil {
			classifications = append(classifications, *classification)
		}
	}

	// Then classify active-lease runtimes.
	activeLeases, err := recovery.store.ListActiveLeaseRuntimes(ctx, RuntimeFence(decision.PreviousFence))
	if err != nil {
		return nil, err
	}
	for _, runID := range activeLeases {
		classification, err := recovery.store.ClassifyRuntimeAfterRestore(ctx, runID, decision)
		if err != nil {
			return nil, err
		}
		if classification != nil {
			classifications = append(classifications, *classification)
		}
	}

	return classifications, nil
}

// IsReadOnly returns true when the recovery state denies new mutations.
func (recovery *RecoveryPointRestore) IsReadOnly(ctx context.Context) (bool, error) {
	state, err := recovery.store.LoadRecoveryState(ctx)
	if err != nil {
		return true, err
	}
	if state == nil {
		return false, nil
	}
	return state.Mode == OperationalReadOnly, nil
}

// RejectNewStart returns an error if the platform is in read-only mode and
// should reject new start requests.
func (recovery *RecoveryPointRestore) RejectNewStart(ctx context.Context) error {
	readOnly, err := recovery.IsReadOnly(ctx)
	if err != nil {
		return err
	}
	if readOnly {
		return newError(ErrorDependencyUnavailable)
	}
	return nil
}

// ClassifyPostRestore categorises a single runtime run based on its
// pre-restore state and the restore decision.
func ClassifyPostRestore(
	state RuntimeState,
	outcome RuntimeOutcome,
	leaseStatus LeaseAcquireStatus,
	leaseDisposition LeaseDisposition,
	hasProcessEvidence bool,
	hasNoLeaseDisposition bool,
	preRestoreFence RuntimeFence,
	newFence RuntimeFence,
) RestoreClassification {
	// Already terminal: no change needed beyond the fence advance.
	if state == RuntimeTerminal {
		return RestoreClassificationAlreadyTerminal
	}

	// Fence is already ahead of pre-restore state.
	if preRestoreFence >= newFence {
		return RestoreClassificationFenced
	}

	// Zero lease proved: no lease ever committed → Rejected + NoLeasePhysicalDisposition.
	if hasNoLeaseDisposition &&
		leaseStatus != LeaseGranted &&
		leaseDisposition != LeaseActive {
		return RestoreClassificationZeroLeaseRejected
	}

	// Lease transaction was in progress or ambiguous.
	if leaseStatus == LeaseAcquirePending ||
		leaseStatus == LeaseAcquireReconciliationRequired {
		return RestoreClassificationAmbiguousReconcile
	}

	// Lease was granted and process may have run.
	if leaseStatus == LeaseGranted || leaseDisposition == LeaseActive {
		if hasProcessEvidence {
			// Process may have run; can't prove zero effect → Lost.
			return RestoreClassificationPossibleEffectLost
		}
		// Lease exists but no process evidence yet → reconcile the lease.
		return RestoreClassificationAmbiguousReconcile
	}

	// Any other non-terminal state with no lease gets fenced.
	return RestoreClassificationFenced
}

// validRestoreDecision checks that a RestoreDecision has all required fields.
func validRestoreDecision(decision RestoreDecision) bool {
	return decision.RecoveryPointID != "" &&
		decision.NewGeneration > 0 &&
		decision.NewFence > 0 &&
		decision.NewGeneration > decision.PreviousGeneration &&
		decision.NewFence > decision.PreviousFence &&
		(decision.Mode == OperationalReadOnly || decision.Mode == OperationalFullReady) &&
		!decision.DecidedAt.IsZero()
}

// validOperationalMode checks if a mode is valid.
func validOperationalMode(mode OperationalMode) bool {
	return mode == OperationalReadOnly || mode == OperationalFullReady
}

// operationalModeName returns a display name for the mode.
func operationalModeName(mode OperationalMode) string {
	switch mode {
	case OperationalReadOnly:
		return "read_only"
	case OperationalFullReady:
		return "full_ready"
	default:
		return ""
	}
}

// canonicalRecoveryDigest produces a canonical digest for a recovery decision.
func canonicalRecoveryDigest(decision RestoreDecision) Digest {
	payload := fmt.Sprintf("slidesmith.recovery-decision/v1\n%s\n%d\n%d\n%d\n%d\n%d\n%d\n%s\n%s",
		decision.RecoveryPointID,
		decision.NewGeneration,
		decision.NewFence,
		decision.NewSafetyEpoch,
		decision.PreviousGeneration,
		decision.PreviousFence,
		decision.PreviousSafetyEpoch,
		operationalModeName(decision.Mode),
		decision.DecidedAt.UTC().Format(time.RFC3339Nano),
	)
	return Digest(sha256.Sum256([]byte(payload)))
}

// EnsureRecoveryInterface checks compile-time interface satisfaction.
var _ interface {
	Restore(ctx context.Context, decision RestoreDecision) ([]RestoreRuntimeClassification, error)
	IsReadOnly(ctx context.Context) (bool, error)
	RejectNewStart(ctx context.Context) error
} = (*RecoveryPointRestore)(nil)
