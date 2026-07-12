package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type stagedProjectPromotion struct {
	noOp                bool
	promotionRoot       string
	claimRoot           string
	attemptRoot         string
	projectPath         string
	targetPath          string
	recoveryPath        string
	retainRecovery      bool
	exchangeDirectories func(string, string) error
	removeAll           func(string) error
	removeDir           func(string) error
}

func (s *TaskService) stagePreparedProject(
	ctx context.Context,
	task *model.Task,
	runtimeWorkspacePath string,
	targetWorkspaceDir string,
) (*stagedProjectPromotion, error) {
	if task == nil {
		return nil, fmt.Errorf("stage runtime project: task is nil")
	}
	if task.RuntimeProject == "" || runtimeWorkspacePath == "" || targetWorkspaceDir == "" {
		return nil, fmt.Errorf("stage runtime project: runtime project and workspace paths are required")
	}

	resolvedRuntimeWorkspacePath, err := resolveRuntimeWorkspacePath(runtimeWorkspacePath)
	if err != nil {
		return nil, fmt.Errorf("stage runtime project: %w", err)
	}
	sourceProjectsDir := filepath.Join(resolvedRuntimeWorkspacePath, "projects")
	projectsInfo, resolvedSourceProjectsDir, err := inspectContainedPath(resolvedRuntimeWorkspacePath, sourceProjectsDir)
	if err != nil {
		return nil, fmt.Errorf("inspect runtime projects path: %w", err)
	}
	if !projectsInfo.IsDir() {
		return nil, fmt.Errorf("runtime projects path is not a directory: %s", sourceProjectsDir)
	}
	sourceProjectsDir = resolvedSourceProjectsDir
	matches, err := filepath.Glob(filepath.Join(sourceProjectsDir, task.RuntimeProject+"_ppt169_*"))
	if err != nil {
		return nil, err
	}
	direct := filepath.Join(sourceProjectsDir, task.RuntimeProject)
	if _, err := os.Stat(direct); err == nil {
		matches = append(matches, direct)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("prepared project not found for %s in %s", task.RuntimeProject, sourceProjectsDir)
	}
	selectedSourceProject := newestPath(matches)
	sourceInfo, sourceProject, err := inspectContainedPath(resolvedRuntimeWorkspacePath, selectedSourceProject)
	if err != nil {
		return nil, fmt.Errorf("inspect prepared project %s: %w", selectedSourceProject, err)
	}
	if !sourceInfo.IsDir() {
		return nil, fmt.Errorf("runtime project source must be a directory: %s", selectedSourceProject)
	}
	return s.stageProjectPromotion(ctx, task, sourceProject, targetWorkspaceDir)
}

func (s *TaskService) stageProjectPromotion(
	ctx context.Context,
	task *model.Task,
	sourceProject string,
	targetWorkspaceDir string,
) (*stagedProjectPromotion, error) {
	if err := requireRealProjectDirectory(sourceProject, "runtime project source"); err != nil {
		return nil, err
	}
	targetProject := filepath.Join(targetWorkspaceDir, "projects", filepath.Base(sourceProject))
	if sameFilesystemPath(sourceProject, targetProject) {
		if task.ExecutionClaimToken == "" {
			return &stagedProjectPromotion{
				noOp:        true,
				projectPath: sourceProject,
				targetPath:  targetProject,
			}, nil
		}
	}

	claimSegment := sanitizePathSegment(task.ExecutionClaimToken)
	if claimSegment == "" {
		claimSegment = "unclaimed"
	}
	promotionRoot := filepath.Join(targetWorkspaceDir, ".slidesmith", "project-promotions")
	claimRoot := filepath.Join(promotionRoot, claimSegment)
	attemptRoot := filepath.Join(claimRoot, uuid.NewString())
	stagedProjectPath := filepath.Join(attemptRoot, "project")
	staged := &stagedProjectPromotion{
		promotionRoot:       promotionRoot,
		claimRoot:           claimRoot,
		attemptRoot:         attemptRoot,
		projectPath:         stagedProjectPath,
		targetPath:          targetProject,
		exchangeDirectories: atomicExchangeDirectories,
		removeAll:           os.RemoveAll,
		removeDir:           os.Remove,
	}
	if err := os.MkdirAll(attemptRoot, 0o755); err != nil {
		return nil, errors.Join(err, staged.cleanup())
	}
	if err := copyProjectDirectoryStrict(ctx, sourceProject, stagedProjectPath); err != nil {
		return nil, errors.Join(err, staged.cleanup())
	}
	if err := requireRealProjectDirectory(stagedProjectPath, "staged runtime project"); err != nil {
		return nil, errors.Join(err, staged.cleanup())
	}
	return staged, nil
}

func (s *TaskService) mutateCanonicalProjectClaimed(
	ctx context.Context,
	task *model.Task,
	projectPath string,
	mutate func(string) error,
	validate func(string) error,
) (targetProject string, err error) {
	if task == nil || task.ExecutionClaimToken == "" {
		return "", fmt.Errorf("mutate canonical runtime project: claimed task is required")
	}
	workspace := s.resolveTaskWorkspace(task)
	if workspace == nil || !sameFilesystemPath(projectPath, filepath.Join(workspace.HostDir, "projects", filepath.Base(projectPath))) {
		return "", fmt.Errorf("mutate canonical runtime project: project %s is not canonical", projectPath)
	}
	staged, err := s.stageProjectPromotion(ctx, task, projectPath, workspace.HostDir)
	if err != nil {
		return "", err
	}
	defer func() {
		if staged.retainRecovery {
			return
		}
		err = errors.Join(err, staged.cleanup())
	}()
	if staged.noOp {
		return "", fmt.Errorf("mutate canonical runtime project: claimed mutation cannot be a no-op")
	}
	if mutate != nil {
		if err := mutate(staged.projectPath); err != nil {
			return "", err
		}
	}
	if validate != nil {
		if err := validate(staged.projectPath); err != nil {
			return "", err
		}
	}
	if s.beforeCanonicalMutationPromotion != nil {
		if err := s.beforeCanonicalMutationPromotion(staged.projectPath); err != nil {
			return "", err
		}
	}
	return s.promoteStagedProjectValidated(ctx, task, staged, validate)
}

func requireRealProjectDirectory(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", label, path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a real directory: %s", label, path)
	}
	return nil
}

func (staged *stagedProjectPromotion) cleanup() error {
	if staged == nil {
		return nil
	}
	if staged.noOp {
		return nil
	}
	removeAll := staged.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	removeDir := staged.removeDir
	if removeDir == nil {
		removeDir = os.Remove
	}
	if err := removeAll(staged.attemptRoot); err != nil {
		return fmt.Errorf("remove runtime project promotion attempt %s: %w", staged.attemptRoot, err)
	}
	// Removing the attempt is the recovery-critical cleanup boundary. Once it
	// succeeds, any exchanged old canonical has been deliberately discarded;
	// empty ancestor pruning must not turn that success into an error with no
	// recovery tree left to inspect.
	for _, path := range []string{staged.claimRoot, staged.promotionRoot} {
		_ = removeDir(path)
	}
	return nil
}

func (s *TaskService) promoteStagedProject(
	ctx context.Context,
	task *model.Task,
	staged *stagedProjectPromotion,
) (string, error) {
	return s.promoteStagedProjectValidated(ctx, task, staged, nil)
}

func (s *TaskService) promoteStagedProjectValidated(
	ctx context.Context,
	task *model.Task,
	staged *stagedProjectPromotion,
	validateCanonical func(string) error,
) (string, error) {
	if task == nil || staged == nil {
		return "", fmt.Errorf("promote runtime project: task and staging are required")
	}
	if err := requireRealProjectDirectory(staged.projectPath, "staged runtime project"); err != nil {
		return "", err
	}
	lockPath := filepath.Join(filepath.Dir(staged.promotionRoot), "project-promotions.lock")
	unlock, err := acquireProjectPromotionLock(ctx, lockPath)
	if err != nil {
		return "", err
	}
	defer unlock()

	if err := os.MkdirAll(filepath.Dir(staged.targetPath), 0o755); err != nil {
		return "", err
	}
	matched, err := s.repo.RenewTaskExecutionClaim(ctx, task.ID, task.Status, task.ExecutionClaimToken)
	if err != nil {
		return "", err
	}
	if !matched {
		return "", errTaskStateChanged
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := requireRealProjectDirectory(staged.projectPath, "staged runtime project"); err != nil {
		return "", err
	}
	info, statErr := os.Lstat(staged.targetPath)
	switch {
	case os.IsNotExist(statErr):
		if err := os.Rename(staged.projectPath, staged.targetPath); err != nil {
			return "", fmt.Errorf("promote initial runtime project: %w", err)
		}
	case statErr != nil:
		return "", fmt.Errorf("inspect canonical runtime project: %w", statErr)
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("canonical runtime project must be a real directory: %s", staged.targetPath)
	default:
		exchange := staged.exchangeDirectories
		if exchange == nil {
			exchange = atomicExchangeDirectories
		}
		if err := exchange(staged.projectPath, staged.targetPath); err != nil {
			return "", fmt.Errorf("atomically exchange runtime project: %w", err)
		}
		staged.recoveryPath = staged.projectPath
	}
	if validateCanonical != nil {
		if err := validateCanonical(staged.targetPath); err != nil {
			if staged.recoveryPath != "" {
				// The caller's deferred cleanup observes this fence and leaves the
				// exchanged old canonical at recoveryPath for manual recovery.
				staged.retainRecovery = true
				return staged.targetPath, fmt.Errorf(
					"revalidate promoted runtime project (old canonical retained at %s): %w",
					staged.recoveryPath,
					err,
				)
			}
			return staged.targetPath, fmt.Errorf("revalidate promoted runtime project: %w", err)
		}
	}
	return staged.targetPath, nil
}
