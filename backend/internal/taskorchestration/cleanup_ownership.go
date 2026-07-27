package taskorchestration

import (
	"context"
	"sync"
)

type CleanupOwnerModule uint8

const (
	CleanupOwnerTaskWorkspaceLifecycle CleanupOwnerModule = iota + 1
	CleanupOwnerRuntimeExecution
	CleanupOwnerDurableObject
	CleanupOwnerArtifactPublication
	CleanupOwnerReleaseManagement
	CleanupOwnerCatalogPublication
	CleanupOwnerBackupRecovery
)

func (owner CleanupOwnerModule) String() string {
	switch owner {
	case CleanupOwnerTaskWorkspaceLifecycle:
		return "task_workspace_lifecycle"
	case CleanupOwnerRuntimeExecution:
		return "runtime_execution"
	case CleanupOwnerDurableObject:
		return "durable_object"
	case CleanupOwnerArtifactPublication:
		return "artifact_publication"
	case CleanupOwnerReleaseManagement:
		return "release_management"
	case CleanupOwnerCatalogPublication:
		return "catalog_publication"
	case CleanupOwnerBackupRecovery:
		return "backup_recovery"
	default:
		return ""
	}
}

type CleanupResourceClass uint8

const (
	CleanupTaskWorkspaceRuntimeView CleanupResourceClass = iota + 1
	CleanupTaskWorkspaceMaterialization
	CleanupTaskWorkspaceCheckpoint
	CleanupRuntimeProcess
	CleanupRuntimeSandbox
	CleanupRuntimeLease
	CleanupRuntimeContainment
	CleanupDurableObjectStaging
	CleanupDurableObjectPhysicalGeneration
	CleanupDurableObjectCache
	CleanupDurableObjectQuarantine
	CleanupDurableObjectReclamation
	CleanupArtifactPublicationStaging
	CleanupReleasePackageMaterialization
	CleanupCatalogPackageMaterialization
	CleanupBackupCopyStaging
)

type CleanupOwnership struct {
	ResourceClass CleanupResourceClass
	Owner         CleanupOwnerModule
}

var cleanupOwnershipMatrix = [...]CleanupOwnership{
	{CleanupTaskWorkspaceRuntimeView, CleanupOwnerTaskWorkspaceLifecycle},
	{CleanupTaskWorkspaceMaterialization, CleanupOwnerTaskWorkspaceLifecycle},
	{CleanupTaskWorkspaceCheckpoint, CleanupOwnerTaskWorkspaceLifecycle},
	{CleanupRuntimeProcess, CleanupOwnerRuntimeExecution},
	{CleanupRuntimeSandbox, CleanupOwnerRuntimeExecution},
	{CleanupRuntimeLease, CleanupOwnerRuntimeExecution},
	{CleanupRuntimeContainment, CleanupOwnerRuntimeExecution},
	{CleanupDurableObjectStaging, CleanupOwnerDurableObject},
	{CleanupDurableObjectPhysicalGeneration, CleanupOwnerDurableObject},
	{CleanupDurableObjectCache, CleanupOwnerDurableObject},
	{CleanupDurableObjectQuarantine, CleanupOwnerDurableObject},
	{CleanupDurableObjectReclamation, CleanupOwnerDurableObject},
	{CleanupArtifactPublicationStaging, CleanupOwnerArtifactPublication},
	{CleanupReleasePackageMaterialization, CleanupOwnerReleaseManagement},
	{CleanupCatalogPackageMaterialization, CleanupOwnerCatalogPublication},
	{CleanupBackupCopyStaging, CleanupOwnerBackupRecovery},
}

// CleanupOwnershipMatrix returns the closed one-owner-per-resource contract.
// Task Orchestration is intentionally not a physical resource owner.
func CleanupOwnershipMatrix() []CleanupOwnership {
	return append([]CleanupOwnership(nil), cleanupOwnershipMatrix[:]...)
}

func cleanupOwner(resourceClass CleanupResourceClass) (CleanupOwnerModule, bool) {
	for _, ownership := range cleanupOwnershipMatrix {
		if ownership.ResourceClass == resourceClass {
			return ownership.Owner, true
		}
	}
	return 0, false
}

type CleanupDebtID struct{ value string }

func NewCleanupDebtID(value string) (CleanupDebtID, error) {
	if !validOpaqueID(value) {
		return CleanupDebtID{}, invalidIntentError()
	}
	return CleanupDebtID{value: value}, nil
}

func (id CleanupDebtID) String() string { return id.value }

// CleanupEvidenceReference is the only Cleanup Debt datum retained by Task
// Orchestration. Physical identity, cleanup state, retry state, locations,
// paths, credentials, and resource mechanics remain in the owning module.
type CleanupEvidenceReference struct {
	DebtID        CleanupDebtID
	Owner         CleanupOwnerModule
	ResourceClass CleanupResourceClass
	EvidenceID    EvidenceID
	OperationID   OperationID
	Category      TelemetryCategory
}

func NewCleanupEvidenceReference(
	debtID CleanupDebtID,
	owner CleanupOwnerModule,
	resourceClass CleanupResourceClass,
	evidenceID EvidenceID,
	operationID OperationID,
	category TelemetryCategory,
) (CleanupEvidenceReference, error) {
	reference := CleanupEvidenceReference{
		DebtID: debtID, Owner: owner, ResourceClass: resourceClass,
		EvidenceID: evidenceID, OperationID: operationID, Category: category,
	}
	if !validCleanupEvidenceReference(reference) {
		if expected, exists := cleanupOwner(resourceClass); exists && expected != owner {
			return CleanupEvidenceReference{}, newError(ErrorIntegrityConflict)
		}
		return CleanupEvidenceReference{}, invalidIntentError()
	}
	return reference, nil
}

func validCleanupEvidenceReference(reference CleanupEvidenceReference) bool {
	owner, exists := cleanupOwner(reference.ResourceClass)
	return exists && owner == reference.Owner && validOpaqueID(reference.DebtID.value) &&
		validOpaqueID(reference.EvidenceID.value) && validOpaqueID(reference.OperationID.value) &&
		reference.Category >= TelemetryCategoryNone &&
		reference.Category <= TelemetryCategoryUnknown
}

type CleanupEvidenceAuthority struct {
	id         AuthorityID
	generation AuthorizationGeneration
	owner      CleanupOwnerModule
}

func NewCleanupEvidenceAuthority(
	id AuthorityID,
	generation AuthorizationGeneration,
	owner CleanupOwnerModule,
) CleanupEvidenceAuthority {
	return CleanupEvidenceAuthority{id: id, generation: generation, owner: owner}
}

func (authority CleanupEvidenceAuthority) valid() bool {
	return validOpaqueID(authority.id.value) && authority.generation > 0 &&
		authority.owner.String() != ""
}

type CleanupEvidenceQuery struct {
	authority AdministratorMetadataAuthority
	debtID    CleanupDebtID
}

func NewCleanupEvidenceQuery(
	authority AdministratorMetadataAuthority,
	debtID CleanupDebtID,
) CleanupEvidenceQuery {
	return CleanupEvidenceQuery{authority: authority, debtID: debtID}
}

type CleanupEvidenceIndex interface {
	Record(context.Context, CleanupEvidenceAuthority, CleanupEvidenceReference) error
	Query(context.Context, CleanupEvidenceQuery) (CleanupEvidenceReference, error)
}

type DeterministicCleanupEvidenceIndex struct {
	mu         sync.Mutex
	references map[CleanupDebtID]CleanupEvidenceReference
}

func NewDeterministicCleanupEvidenceIndex() *DeterministicCleanupEvidenceIndex {
	return &DeterministicCleanupEvidenceIndex{
		references: make(map[CleanupDebtID]CleanupEvidenceReference),
	}
}

func (index *DeterministicCleanupEvidenceIndex) Record(
	ctx context.Context,
	authority CleanupEvidenceAuthority,
	reference CleanupEvidenceReference,
) error {
	if index == nil || ctx == nil || ctx.Err() != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if !authority.valid() || authority.owner != reference.Owner {
		return newError(ErrorAuthorizationDenied)
	}
	if !validCleanupEvidenceReference(reference) {
		return newError(ErrorIntegrityConflict)
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	if existing, exists := index.references[reference.DebtID]; exists {
		if existing == reference {
			return nil
		}
		return newError(ErrorIntegrityConflict)
	}
	index.references[reference.DebtID] = reference
	return nil
}

func (index *DeterministicCleanupEvidenceIndex) Query(
	ctx context.Context,
	query CleanupEvidenceQuery,
) (CleanupEvidenceReference, error) {
	if index == nil || ctx == nil || ctx.Err() != nil {
		return CleanupEvidenceReference{}, newError(ErrorDependencyUnavailable)
	}
	if !query.authority.valid() || !validOpaqueID(query.debtID.value) {
		return CleanupEvidenceReference{}, newError(ErrorAuthorizationDenied)
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	reference, exists := index.references[query.debtID]
	if !exists {
		return CleanupEvidenceReference{}, newError(ErrorAuthorizationDenied)
	}
	return reference, nil
}
