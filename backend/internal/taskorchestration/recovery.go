package taskorchestration

import (
	"time"
)

// RecoveryPointID identifies a durable joint Recovery Point.
type RecoveryPointID struct{ value string }

// RecoveryPointStatus tracks the lifecycle of a Recovery Point from creation
// through promotion.
type RecoveryPointStatus uint8

const (
	RecoveryPointCandidate RecoveryPointStatus = iota + 1
	RecoveryPointFinalized
	RecoveryPointRestoring
	RecoveryPointRestored
	RecoveryPointPromoted
	RecoveryPointAbandoned
)

// RecoveryPointSnapshot is the authoritative signed manifest root for a
// joint PostgreSQL + Durable Object Recovery Point.
type RecoveryPointSnapshot struct {
	RecoveryPointID   RecoveryPointID
	Status            RecoveryPointStatus
	TargetTime        time.Time
	ProtectedThrough  time.Time
	PriorPointID      RecoveryPointID
	ManifestDigest    EvidenceDigest
	Generation        RecoveryGeneration
	Fence             RecoveryFence
	SafetyEpoch       SafetyEpoch
	CreatedAt         time.Time
	FinalizedAt       time.Time
	RestoreStartedAt  time.Time
	PromotedAt        time.Time
}

// RestoreDecision records the authoritative outcome of a restore attempt. It
// binds the selected Recovery Point, the new environment generation, and
// the post-restore fence and safety epoch.
type RestoreDecision struct {
	RecoveryPointID      RecoveryPointID
	NewGeneration        RecoveryGeneration
	NewFence             RecoveryFence
	NewSafetyEpoch       SafetyEpoch
	PreviousGeneration   RecoveryGeneration
	PreviousFence        RecoveryFence
	PreviousSafetyEpoch  SafetyEpoch
	OperationalMode      OperationalMode
	FencedRuntimeRuns    []RuntimeRunID
	RejectedRuntimeRuns  []RuntimeRunID
	LostRuntimeRuns      []RuntimeRunID
	DecidedAt            time.Time
}

// RestoreBinding identifies the authority and scope of a restore attempt.
type RestoreBinding struct {
	RecoveryPointID RecoveryPointID
	Authority       RecoveryAuthority
	TargetMode      OperationalMode
}

// valid checks the binding.
func (binding RestoreBinding) valid() bool {
	return validOpaqueID(binding.RecoveryPointID.value) &&
		binding.Authority.value.valid() &&
		binding.Authority.value.kind == AuthorityRecovery &&
		operationalModeName(binding.TargetMode) != ""
}

// RecoveryState holds the durable operational mode and fence facts. It is the
// authoritative singleton row behind the task_orchestration_recovery_state
// table.
type RecoveryState struct {
	authority               authorityValue
	generation              RecoveryGeneration
	fence                   RecoveryFence
	safetyEpoch             SafetyEpoch
	activityGenerationFence ActivityGeneration
	mode                    OperationalMode
	restoreHistory          []RestoreDecision
}

// isReadOnly reports whether mutations are fenced.
func (state RecoveryState) isReadOnly() bool {
	return state.mode == OperationalReadOnly
}

// validRecoveryTransition checks that the recovery state transition is legal.
func validRecoveryTransition(from OperationalMode, to OperationalMode) bool {
	switch from {
	case OperationalReadOnly:
		return to == OperationalFullReady
	case OperationalFullReady:
		return to == OperationalReadOnly
	default:
		return false
	}
}
