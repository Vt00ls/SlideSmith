package taskworkspace

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"time"
)

// ErrLocalFilesystemUnavailable is deliberately content- and path-free. The
// adapter never returns an operating-system error across the lifecycle seam.
var ErrLocalFilesystemUnavailable = errors.New("local development storage is unavailable")

type LocalContentSource interface {
	OpenContent(context.Context, Digest) (io.ReadCloser, error)
}

type LocalContentSourceFunc func(context.Context, Digest) (io.ReadCloser, error)

func (f LocalContentSourceFunc) OpenContent(ctx context.Context, digest Digest) (io.ReadCloser, error) {
	return f(ctx, digest)
}

type LocalFilesystemCapacity struct {
	AvailableBytes  uint64
	AvailableInodes uint64
}

type LocalFilesystemCapacityProbe func() (LocalFilesystemCapacity, error)

type LocalFilesystemFaultPoint string

const (
	LocalFaultBeforeWrite                  LocalFilesystemFaultPoint = "before_write"
	LocalFaultAfterWrite                   LocalFilesystemFaultPoint = "after_write"
	LocalFaultBeforeFileSync               LocalFilesystemFaultPoint = "before_file_sync"
	LocalFaultAfterFileSync                LocalFilesystemFaultPoint = "after_file_sync"
	LocalFaultBeforeDirectorySync          LocalFilesystemFaultPoint = "before_directory_sync"
	LocalFaultAfterDirectorySync           LocalFilesystemFaultPoint = "after_directory_sync"
	LocalFaultBeforePrivatePlacement       LocalFilesystemFaultPoint = "before_private_placement"
	LocalFaultBeforePromotion              LocalFilesystemFaultPoint = "before_promotion"
	LocalFaultAfterPromotion               LocalFilesystemFaultPoint = "after_promotion"
	LocalFaultBeforeReadback               LocalFilesystemFaultPoint = "before_readback"
	LocalFaultAfterReadback                LocalFilesystemFaultPoint = "after_readback"
	LocalFaultBeforeCapacityReserve        LocalFilesystemFaultPoint = "before_capacity_reservation"
	LocalFaultAfterMaterializationSkeleton LocalFilesystemFaultPoint = "after_materialization_skeleton"
	LocalFaultBeforeCleanup                LocalFilesystemFaultPoint = "before_cleanup"
	LocalFaultAfterCleanupVerify           LocalFilesystemFaultPoint = "after_cleanup_verification"
	LocalFaultBeforeCleanupDelete          LocalFilesystemFaultPoint = "before_cleanup_delete"
	LocalFaultAfterCleanupPrepare          LocalFilesystemFaultPoint = "after_cleanup_prepare"
	LocalFaultBeforeCleanupEntryRemove     LocalFilesystemFaultPoint = "before_cleanup_entry_remove"
	LocalFaultAfterCleanup                 LocalFilesystemFaultPoint = "after_cleanup"
)

// LocalFilesystemFaultEvent contains only opaque authority identities. It is
// safe to use in deterministic tests without making a local path evidence.
type LocalFilesystemFaultEvent struct {
	Point       LocalFilesystemFaultPoint
	OperationID OperationID
	SubjectID   string
	Ordinal     int
}

type LocalFilesystemConfig struct {
	Root            string
	Lifecycle       InMemoryConfig
	ContentSource   LocalContentSource
	Capacity        LocalFilesystemCapacityProbe
	Random          io.Reader
	FilesystemFault func(LocalFilesystemFaultEvent) error
}

// NewLocalFilesystem constructs the local-development adapter. Platform
// callers still receive only Lifecycle; the root and all physical entries stay
// behind this constructor boundary.
func NewLocalFilesystem(config LocalFilesystemConfig) (Lifecycle, error) {
	if config.Root == "" || config.ContentSource == nil {
		return nil, ErrLocalFilesystemUnavailable
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if err := os.MkdirAll(config.Root, 0o700); err != nil {
		return nil, ErrLocalFilesystemUnavailable
	}
	info, err := os.Lstat(config.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrLocalFilesystemUnavailable
	}
	root, err := os.OpenRoot(config.Root)
	if err != nil {
		return nil, ErrLocalFilesystemUnavailable
	}
	now := func() Instant { return Instant(time.Now().UnixNano()) }
	if config.Lifecycle.Now != nil {
		now = config.Lifecycle.Now
	}
	store, err := newLocalFilesystemStore(localFilesystemStoreConfig{
		root:        root,
		source:      config.ContentSource,
		capacity:    config.Capacity,
		random:      config.Random,
		fault:       config.FilesystemFault,
		now:         now,
		authorityID: config.Lifecycle.DurabilityAuthorityID,
		adapterID:   "local-development-adapter-v1",
	})
	if err != nil {
		_ = root.Close()
		return nil, ErrLocalFilesystemUnavailable
	}
	config.Lifecycle.DurableObject = store
	config.Lifecycle.CheckpointReclamation = store
	config.Lifecycle.Cleanup = store
	return &localFilesystemLifecycle{
		Lifecycle: NewInMemory(config.Lifecycle),
		store:     store,
	}, nil
}

type localFilesystemLifecycle struct {
	Lifecycle
	store *localFilesystemStore
}

type localWorkspaceAuthorityReader interface {
	currentLocalWorkspaceAuthority(
		PolicyDomainID,
		TaskID,
		TaskWorkspaceID,
	) (Generation, Fence, bool)
}

type localResidueAuthority struct {
	policyDomainID  PolicyDomainID
	taskID          TaskID
	taskWorkspaceID TaskWorkspaceID
	generation      Generation
	fence           Fence
	operationID     OperationID
}

func (l *localFilesystemLifecycle) Materialize(
	ctx context.Context,
	request MaterializeRequest,
) (MaterializeResult, error) {
	result, err := l.Lifecycle.Materialize(ctx, request)
	outcomeErr := l.finishMaterialization(ctx, localResidueAuthority{
		policyDomainID: request.PolicyDomainID, taskID: request.TaskID,
		taskWorkspaceID: request.TaskWorkspaceID, generation: request.Generation,
		fence: request.Fence, operationID: request.Operation.ID,
	}, localMaterializationAuthority{
		OperationID: request.Operation.ID, MaterializationID: result.MaterializationID,
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, Generation: request.Generation,
		Fence: request.Fence, PhysicalRequired: request.CheckpointID != "",
	}, err)
	if outcomeErr != nil {
		return result, outcomeErr
	}
	return result, nil
}

func (l *localFilesystemLifecycle) RestoreTaskWorkspace(
	ctx context.Context,
	request RestoreTaskWorkspaceRequest,
) (RestoreTaskWorkspaceResult, error) {
	result, err := l.Lifecycle.RestoreTaskWorkspace(ctx, request)
	outcomeErr := l.finishMaterialization(ctx, localResidueAuthority{
		policyDomainID: request.Intent.PolicyDomainID, taskID: request.Intent.TaskID,
		taskWorkspaceID: request.Intent.TaskWorkspaceID, generation: request.Intent.Generation,
		fence: request.Intent.Fence, operationID: request.Operation.ID,
	}, localMaterializationAuthority{
		OperationID: request.Operation.ID, MaterializationID: result.MaterializationID,
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		TaskWorkspaceID: result.TaskWorkspaceID, Generation: result.Generation,
		Fence: result.Fence, PhysicalRequired: result.CheckpointID != "",
	}, err)
	if outcomeErr != nil {
		return result, outcomeErr
	}
	return result, nil
}

func (l *localFilesystemLifecycle) ReconstructTaskWorkspace(
	ctx context.Context,
	request ReconstructTaskWorkspaceRequest,
) (ReconstructTaskWorkspaceResult, error) {
	result, err := l.Lifecycle.ReconstructTaskWorkspace(ctx, request)
	outcomeErr := l.finishMaterialization(ctx, localResidueAuthority{
		policyDomainID: request.Intent.PolicyDomainID, taskID: request.Intent.TaskID,
		taskWorkspaceID: request.Intent.TaskWorkspaceID, generation: request.Intent.Generation,
		fence: request.Intent.Fence, operationID: request.Operation.ID,
	}, localMaterializationAuthority{
		OperationID: request.Operation.ID, MaterializationID: result.MaterializationID,
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		TaskWorkspaceID: result.TaskWorkspaceID, Generation: result.Generation,
		Fence: result.Fence,
	}, err)
	if outcomeErr != nil {
		return result, outcomeErr
	}
	return result, nil
}

func (l *localFilesystemLifecycle) finishMaterialization(
	ctx context.Context,
	residueAuthority localResidueAuthority,
	materializationAuthority localMaterializationAuthority,
	lifecycleErr error,
) error {
	if lifecycleErr != nil {
		if debtErr := l.createFailureResidueDebts(
			ctx, residueAuthority.policyDomainID, residueAuthority.taskID,
			residueAuthority.taskWorkspaceID, residueAuthority.generation,
			residueAuthority.fence, residueAuthority.operationID,
		); debtErr != nil {
			return &Error{Code: ErrorReconciliationRequired}
		}
		return lifecycleErr
	}
	if err := l.store.bindMaterialization(materializationAuthority); err != nil {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return nil
}

func (l *localFilesystemLifecycle) CommitRuntimeView(
	ctx context.Context,
	request CommitRuntimeViewRequest,
) (CommitRuntimeViewResult, error) {
	result, err := l.Lifecycle.CommitRuntimeView(ctx, request)
	if err != nil {
		if debtErr := l.createFailureResidueDebts(ctx, request.PolicyDomainID, request.TaskID,
			request.TaskWorkspaceID, request.Generation, request.Fence, request.Operation.ID); debtErr != nil {
			return CommitRuntimeViewResult{}, &Error{Code: ErrorReconciliationRequired}
		}
		return result, err
	}
	if err := l.store.activateCheckpoint(request.Operation.ID, result.CheckpointEvidence); err != nil {
		_ = l.createFailureResidueDebts(ctx, request.PolicyDomainID, request.TaskID,
			request.TaskWorkspaceID, request.Generation, request.Fence, request.Operation.ID)
		return CommitRuntimeViewResult{}, &Error{Code: ErrorReconciliationRequired}
	}
	return result, nil
}

func (l *localFilesystemLifecycle) ExpireMaterialization(
	ctx context.Context,
	request ExpireMaterializationRequest,
) (ExpireMaterializationResult, error) {
	result, err := l.Lifecycle.ExpireMaterialization(ctx, request)
	if err != nil {
		return result, err
	}
	residue, cleanupErr := l.store.expireMaterialization(request)
	if cleanupErr != nil {
		if residue == nil {
			return ExpireMaterializationResult{}, &Error{Code: ErrorReconciliationRequired}
		}
		if debtErr := l.createResidueDebt(ctx, request.PolicyDomainID, request.TaskID, request.TaskWorkspaceID,
			request.Generation, request.Fence, request.Operation.ID, *residue,
			CleanupTaskWorkspaceMaterialization); debtErr != nil {
			return ExpireMaterializationResult{}, &Error{Code: ErrorReconciliationRequired}
		}
	}
	return result, nil
}

func (l *localFilesystemLifecycle) createFailureResidueDebts(
	ctx context.Context,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	taskWorkspaceID TaskWorkspaceID,
	generation Generation,
	fence Fence,
	operationID OperationID,
) error {
	var failed bool
	for _, residue := range l.store.failureResidues(operationID) {
		if err := l.createResidueDebt(ctx, policyDomainID, taskID, taskWorkspaceID, generation, fence,
			operationID, residue, CleanupWorkspaceResidue); err != nil {
			failed = true
		}
	}
	if failed {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return nil
}

func (l *localFilesystemLifecycle) createResidueDebt(
	ctx context.Context,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	taskWorkspaceID TaskWorkspaceID,
	generation Generation,
	fence Fence,
	causeOperationID OperationID,
	residue localFilesystemResidue,
	resourceClass CleanupResourceClass,
) error {
	if authority, ok := l.Lifecycle.(localWorkspaceAuthorityReader); ok {
		currentGeneration, currentFence, current := authority.currentLocalWorkspaceAuthority(
			policyDomainID, taskID, taskWorkspaceID,
		)
		if !current {
			return &Error{Code: ErrorReconciliationRequired}
		}
		generation = currentGeneration
		fence = currentFence
	}
	operation := Operation{ID: OperationID("cleanup-" + string(causeOperationID) + "-" + residue.id)}
	request := CreateCleanupObligationRequest{
		PolicyDomainID:     policyDomainID,
		TaskID:             taskID,
		TaskWorkspaceID:    taskWorkspaceID,
		Owner:              CleanupOwnerC04,
		ResourceClass:      resourceClass,
		ResourceID:         CleanupResourceID(residue.id),
		ResourceGeneration: CleanupResourceGeneration(residue.generation),
		Generation:         generation,
		Fence:              fence,
		Capacity:           residue.capacity,
		EligibilityEvidenceRoot: canonicalDigest(struct {
			ResourceID string
			Generation string
		}{residue.id, residue.generation}),
		Operation: operation,
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	if _, err := l.Lifecycle.CreateCleanupObligation(ctx, request); err != nil {
		return &Error{Code: ErrorReconciliationRequired}
	}
	if err := l.store.markCleanupDebtRegistered(residue.id, residue.generation); err != nil {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return nil
}
