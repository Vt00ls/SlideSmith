package runtimeexecution

import (
	"context"
	"time"
)

// OperationalDiagnostics is the read-only protected diagnostics seam for
// Runtime Execution. It is deliberately separate from the public Execute and
// Inspect surface and from the RuntimeMaintenance mutation seam. Every query
// is reason-bound, requires a Platform Administrator authority, references an
// exact identity (non-enumerating), and returns a bounded content-free view.
// It exposes no path, locator, credential, content, or mutation-capable
// repository.
type OperationalDiagnostics interface {
	Diagnose(context.Context, OperationalDiagnosticQuery) (OperationalDiagnosticView, error)
}

type DiagnosticReason uint8

const (
	DiagnosticReasonCleanupHealth DiagnosticReason = iota + 1
	DiagnosticReasonNodeHealth
	DiagnosticReasonCapacityInvestigation
)

type DiagnosticLookup uint8

const (
	DiagnosticLookupCleanupDebt DiagnosticLookup = iota + 1
	DiagnosticLookupExecutionNode
	DiagnosticLookupRuntimeLease
)

// OperationalDiagnosticQuery binds one exact reference. A query that names no
// exact DebtID, ExecutionNodeID, or RuntimeRunID matching its Lookup is
// rejected, so the seam can never enumerate the whole population.
type OperationalDiagnosticQuery struct {
	SchemaVersion       SchemaVersion
	Reason              DiagnosticReason
	Authority           RuntimeAuthority
	Lookup              DiagnosticLookup
	DebtID              string
	PersonalWorkspaceID PersonalWorkspaceID
	ExecutionNodeID     ExecutionNodeID
	RuntimeRunID        RuntimeRunID
	Bounded             bool
}

type OperationalDiagnosticView struct {
	Lookup  DiagnosticLookup
	Debt    *CleanupDebtDiagnosticView
	Node    *NodeDiagnosticView
	Runtime *RuntimeDiagnosticView
}

// CleanupDebtDiagnosticView is content-free: opaque identities, enums, and
// digest-backed facts only. It never contains a path, locator, credential,
// content, or free-form error text.
type CleanupDebtDiagnosticView struct {
	DebtID           string
	DebtRevision     uint64
	OwnerModule      string
	ResourceClass    cleanupResourceClass
	Status           cleanupDebtStatus
	Unresolved       bool
	RetryDisposition cleanupRetryDisposition
	LastError        cleanupFailureCategory
	EstimateState    cleanupEstimateState
	Blockers         cleanupBlockerClass
	ResolutionClass  cleanupResolutionClass
	ExceptionUntil   time.Time
	AttemptCount     uint64
}

type NodeDiagnosticView struct {
	ExecutionNodeID ExecutionNodeID
	Generation      NodeGeneration
	Readiness       NodeReadiness
	Occupancy       NodeOccupancy
	Quarantined     bool
	Containment     ContainmentStatus
	Reset           ResetStatus
}

type RuntimeDiagnosticView struct {
	RuntimeRunID     RuntimeRunID
	RuntimeRevision  RuntimeRevision
	State            RuntimeState
	Outcome          RuntimeOutcome
	LeaseDisposition LeaseDisposition
	Physical         PhysicalCapacityDisposition
	Cleanup          LeaseCleanupStatus
	Quarantined      bool
	Containment      ContainmentStatus
	Reset            ResetStatus
}

func validOperationalDiagnosticQuery(query OperationalDiagnosticQuery) bool {
	if query.SchemaVersion.Major() != SchemaV1.Major() ||
		query.Reason < DiagnosticReasonCleanupHealth || query.Reason > DiagnosticReasonCapacityInvestigation ||
		!validAuthority(query.Authority) || query.Authority.kind != AuthorityAdministrator || !query.Bounded {
		return false
	}
	switch query.Lookup {
	case DiagnosticLookupCleanupDebt:
		return validOpaqueID(query.DebtID) && validOpaqueID(query.PersonalWorkspaceID.String()) &&
			validOpaqueID(query.RuntimeRunID.String()) && query.ExecutionNodeID == (ExecutionNodeID{})
	case DiagnosticLookupExecutionNode:
		return validOpaqueID(query.ExecutionNodeID.String()) && query.DebtID == "" &&
			query.RuntimeRunID == (RuntimeRunID{})
	case DiagnosticLookupRuntimeLease:
		return validOpaqueID(query.RuntimeRunID.String()) && validOpaqueID(query.PersonalWorkspaceID.String()) &&
			query.DebtID == "" && query.ExecutionNodeID == (ExecutionNodeID{})
	default:
		return false
	}
}

var _ OperationalDiagnostics = (*invariantEngine)(nil)
