package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/gorm"
)

func TestStagePreparedProjectRejectsNonDirectoryRoots(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, string)
	}{
		{
			name: "regular file",
			setup: func(t *testing.T, sourceProject, _ string) {
				mustWriteFile(t, sourceProject, "not a directory")
			},
		},
		{
			name: "symlink directory",
			setup: func(t *testing.T, sourceProject, canonicalProject string) {
				if err := os.MkdirAll(filepath.Dir(sourceProject), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(canonicalProject, sourceProject); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
		{
			name:  "missing",
			setup: func(*testing.T, string, string) {},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, task, canonicalProject, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, nil)
			sessionWorkspace := filepath.Join(t.TempDir(), "workspace")
			sourceProject := filepath.Join(sessionWorkspace, "projects", filepath.Base(canonicalProject))
			test.setup(t, sourceProject, canonicalProject)

			staged, err := service.stagePreparedProject(context.Background(), task, sessionWorkspace, workspacePath)
			if staged != nil {
				staged.cleanup()
			}
			if err == nil {
				t.Fatal("stagePreparedProject() error = nil, want real-directory rejection")
			}
		})
	}
}

func TestStagePreparedProjectRejectsSymlinkMember(t *testing.T) {
	service, _, task, canonicalProject, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, nil)
	sessionWorkspace := filepath.Join(t.TempDir(), "workspace")
	if err := copyDir(context.Background(), workspacePath, sessionWorkspace); err != nil {
		t.Fatal(err)
	}
	sessionProject := filepath.Join(sessionWorkspace, "projects", filepath.Base(canonicalProject))
	outsidePath := filepath.Join(t.TempDir(), "outside-secret.txt")
	mustWriteFile(t, outsidePath, "outside secret")
	if err := os.Symlink(outsidePath, filepath.Join(sessionProject, "sources", "leak.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	staged, err := service.stagePreparedProject(context.Background(), task, sessionWorkspace, workspacePath)
	if staged != nil {
		staged.cleanup()
	}
	if err == nil {
		t.Fatal("stagePreparedProject() error = nil, want symlink-member rejection")
	}
	if _, statErr := os.Stat(filepath.Join(canonicalProject, "sources", "leak.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink member reached canonical project: %v", statErr)
	}
}

func TestStagePreparedProjectRejectsSymlinkedProjectsAncestorOutsideWorkspace(t *testing.T) {
	service, _, task, canonicalProject, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, nil)
	sessionWorkspace := filepath.Join(t.TempDir(), "workspace")
	outsideProjects := filepath.Join(t.TempDir(), "outside-projects")
	externalProject := filepath.Join(outsideProjects, filepath.Base(canonicalProject))
	mustWriteFile(t, filepath.Join(externalProject, "external-secret.txt"), "external secret")
	if err := os.MkdirAll(sessionWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideProjects, filepath.Join(sessionWorkspace, "projects")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	staged, err := service.stagePreparedProject(context.Background(), task, sessionWorkspace, workspacePath)
	if staged != nil {
		defer func() { _ = staged.cleanup() }()
	}
	if err == nil {
		if _, statErr := os.Stat(filepath.Join(staged.projectPath, "external-secret.txt")); statErr == nil {
			t.Fatal("external project bytes reached promotion staging through symlinked projects ancestor")
		}
		t.Fatal("stagePreparedProject() error = nil, want outside-workspace ancestor rejection")
	}
	if _, statErr := os.Stat(filepath.Join(canonicalProject, "external-secret.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("external project bytes reached canonical project: %v", statErr)
	}
}

func TestPromoteStagedProjectRechecksRealDirectoryRoot(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, string)
	}{
		{
			name: "regular file",
			setup: func(t *testing.T, stagedProject, _ string) {
				if err := os.RemoveAll(stagedProject); err != nil {
					t.Fatal(err)
				}
				mustWriteFile(t, stagedProject, "not a directory")
			},
		},
		{
			name: "symlink directory",
			setup: func(t *testing.T, stagedProject, canonicalProject string) {
				if err := os.RemoveAll(stagedProject); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(canonicalProject, stagedProject); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
		{
			name: "missing",
			setup: func(t *testing.T, stagedProject, _ string) {
				if err := os.RemoveAll(stagedProject); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, canonicalProject, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, nil)
			claimedAt := time.Now().UTC()
			claimed, err := repo.ClaimTaskExecution(context.Background(), task.ID, task.Status, "promotion-owner", claimedAt, claimedAt.Add(-time.Hour))
			if err != nil || !claimed {
				t.Fatalf("claim promotion owner = %v, %v", claimed, err)
			}
			task.ExecutionClaimToken = "promotion-owner"
			task.ExecutionClaimedAt = &claimedAt
			sentinelPath := filepath.Join(canonicalProject, "canonical-sentinel.txt")
			mustWriteFile(t, sentinelPath, "canonical")

			sessionWorkspace := filepath.Join(t.TempDir(), "workspace")
			if err := copyDir(context.Background(), workspacePath, sessionWorkspace); err != nil {
				t.Fatal(err)
			}
			staged, err := service.stagePreparedProject(context.Background(), task, sessionWorkspace, workspacePath)
			if err != nil {
				t.Fatal(err)
			}
			defer staged.cleanup()
			test.setup(t, staged.projectPath, canonicalProject)

			if _, err := service.promoteStagedProject(context.Background(), task, staged); err == nil {
				t.Fatal("promoteStagedProject() error = nil, want staged-root rejection")
			}
			info, err := os.Lstat(canonicalProject)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("canonical project is not a real directory after rejection: info=%v error=%v", info, err)
			}
			raw, err := os.ReadFile(sentinelPath)
			if err != nil || string(raw) != "canonical" {
				t.Fatalf("canonical sentinel changed after rejection: %q, %v", raw, err)
			}
		})
	}
}

type claimedPromotionFixture struct {
	service          *TaskService
	repo             interface{ DB() *gorm.DB }
	task             *model.Task
	canonicalProject string
	sessionWorkspace string
	staged           *stagedProjectPromotion
}

func newClaimedPromotionFixture(t *testing.T) claimedPromotionFixture {
	t.Helper()
	service, repo, task, canonicalProject, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, nil)
	claimedAt := time.Now().UTC()
	claimed, err := repo.ClaimTaskExecution(context.Background(), task.ID, task.Status, "promotion-owner", claimedAt, claimedAt.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("claim promotion owner = %v, %v", claimed, err)
	}
	task.ExecutionClaimToken = "promotion-owner"
	task.ExecutionClaimedAt = &claimedAt
	mustWriteFile(t, filepath.Join(canonicalProject, "promotion-sentinel.txt"), "canonical")
	sessionWorkspace := filepath.Join(t.TempDir(), "workspace")
	if err := copyDir(context.Background(), workspacePath, sessionWorkspace); err != nil {
		t.Fatal(err)
	}
	sessionProject := filepath.Join(sessionWorkspace, "projects", filepath.Base(canonicalProject))
	mustWriteFile(t, filepath.Join(sessionProject, "promotion-sentinel.txt"), "session")
	staged, err := service.stagePreparedProject(context.Background(), task, sessionWorkspace, workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	return claimedPromotionFixture{
		service:          service,
		repo:             repo,
		task:             task,
		canonicalProject: canonicalProject,
		sessionWorkspace: sessionWorkspace,
		staged:           staged,
	}
}

func TestSyncRuntimeProjectDoesNotFailAfterExchangeWhenEventWritesFail(t *testing.T) {
	fixture := newClaimedPromotionFixture(t)
	if err := fixture.staged.cleanup(); err != nil {
		t.Fatal(err)
	}
	forcedErr := errors.New("forced post-exchange event failure")
	callbackName := "test:fail-post-exchange-event"
	if err := fixture.repo.DB().Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.TaskEvent{}).TableName() {
			tx.AddError(forcedErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer fixture.repo.DB().Callback().Create().Remove(callbackName)

	workspace := fixture.service.resolveTaskWorkspace(fixture.task)
	if _, err := fixture.service.syncRuntimeProject(context.Background(), fixture.task, workspace, fixture.sessionWorkspace); err != nil {
		t.Fatalf("syncRuntimeProject() error after successful exchange = %v", err)
	}
	requirePromotionSentinel(t, fixture.canonicalProject, "session")
}

func TestPromoteStagedProjectDoesNotExchangeWhenClaimRenewalFails(t *testing.T) {
	fixture := newClaimedPromotionFixture(t)
	defer func() { _ = fixture.staged.cleanup() }()
	forcedErr := errors.New("forced claim renewal failure")
	callbackName := "test:fail-promotion-claim-renewal"
	if err := fixture.repo.DB().Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.Task{}).TableName() {
			tx.AddError(forcedErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer fixture.repo.DB().Callback().Update().Remove(callbackName)
	exchangeCalled := false
	fixture.staged.exchangeDirectories = func(string, string) error {
		exchangeCalled = true
		return nil
	}

	if _, err := fixture.service.promoteStagedProject(context.Background(), fixture.task, fixture.staged); !errors.Is(err, forcedErr) {
		t.Fatalf("promoteStagedProject() error = %v, want forced renewal failure", err)
	}
	if exchangeCalled {
		t.Fatal("directory exchange ran after failed claim renewal")
	}
	requirePromotionSentinel(t, fixture.canonicalProject, "canonical")
}

func TestPromoteStagedProjectExchangeFailureLeavesCanonicalUntouched(t *testing.T) {
	fixture := newClaimedPromotionFixture(t)
	defer func() { _ = fixture.staged.cleanup() }()
	forcedErr := errors.New("forced directory exchange failure")
	fixture.staged.exchangeDirectories = func(string, string) error { return forcedErr }

	if _, err := fixture.service.promoteStagedProject(context.Background(), fixture.task, fixture.staged); !errors.Is(err, forcedErr) {
		t.Fatalf("promoteStagedProject() error = %v, want forced exchange failure", err)
	}
	requirePromotionSentinel(t, fixture.canonicalProject, "canonical")
	requirePromotionSentinel(t, fixture.staged.projectPath, "session")
}

func TestPromotionCleanupFailureIsVisibleAndPreservesOldCanonicalRecoveryTree(t *testing.T) {
	fixture := newClaimedPromotionFixture(t)
	if _, err := fixture.service.promoteStagedProject(context.Background(), fixture.task, fixture.staged); err != nil {
		t.Fatal(err)
	}
	requirePromotionSentinel(t, fixture.canonicalProject, "session")
	requirePromotionSentinel(t, fixture.staged.projectPath, "canonical")

	forcedErr := errors.New("forced promotion cleanup failure")
	realRemoveAll := fixture.staged.removeAll
	fixture.staged.removeAll = func(path string) error {
		if path == fixture.staged.attemptRoot {
			return forcedErr
		}
		return realRemoveAll(path)
	}
	if err := fixture.staged.cleanup(); !errors.Is(err, forcedErr) {
		t.Fatalf("cleanup() error = %v, want forced cleanup failure", err)
	}
	requirePromotionSentinel(t, fixture.staged.projectPath, "canonical")
	fixture.staged.removeAll = realRemoveAll
	if err := fixture.staged.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestPromotionCleanupFailureAfterExchangeFailurePreservesBothTrees(t *testing.T) {
	fixture := newClaimedPromotionFixture(t)
	forcedExchangeErr := errors.New("forced directory exchange failure")
	fixture.staged.exchangeDirectories = func(string, string) error { return forcedExchangeErr }
	if _, err := fixture.service.promoteStagedProject(context.Background(), fixture.task, fixture.staged); !errors.Is(err, forcedExchangeErr) {
		t.Fatalf("promoteStagedProject() error = %v, want forced exchange failure", err)
	}

	forcedCleanupErr := errors.New("forced promotion cleanup failure")
	realRemoveAll := fixture.staged.removeAll
	fixture.staged.removeAll = func(path string) error {
		if path == fixture.staged.attemptRoot {
			return forcedCleanupErr
		}
		return realRemoveAll(path)
	}
	if err := fixture.staged.cleanup(); !errors.Is(err, forcedCleanupErr) {
		t.Fatalf("cleanup() error = %v, want forced cleanup failure", err)
	}
	requirePromotionSentinel(t, fixture.canonicalProject, "canonical")
	requirePromotionSentinel(t, fixture.staged.projectPath, "session")
	fixture.staged.removeAll = realRemoveAll
	if err := fixture.staged.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestPromotionEmptyParentPruneFailureDoesNotReportRecoverylessCleanupError(t *testing.T) {
	fixture := newClaimedPromotionFixture(t)
	if _, err := fixture.service.promoteStagedProject(context.Background(), fixture.task, fixture.staged); err != nil {
		t.Fatal(err)
	}
	requirePromotionSentinel(t, fixture.canonicalProject, "session")
	requirePromotionSentinel(t, fixture.staged.projectPath, "canonical")

	forcedErr := errors.New("forced empty parent prune failure")
	realRemoveDir := fixture.staged.removeDir
	fixture.staged.removeDir = func(path string) error {
		if path == fixture.staged.claimRoot {
			return forcedErr
		}
		return realRemoveDir(path)
	}
	t.Cleanup(func() {
		fixture.staged.removeDir = realRemoveDir
		_ = fixture.staged.cleanup()
	})

	if err := fixture.staged.cleanup(); err != nil {
		t.Fatalf("cleanup() error after old-tree deletion = %v, want best-effort empty-parent pruning", err)
	}
	requirePromotionSentinel(t, fixture.canonicalProject, "session")
	if _, err := os.Lstat(fixture.staged.projectPath); !os.IsNotExist(err) {
		t.Fatalf("old canonical was not discarded by successful attempt cleanup: %v", err)
	}
}

func TestValidatedPromotionRevalidatesCanonicalBeforeDiscardingRecovery(t *testing.T) {
	tests := []struct {
		name          string
		validationErr error
	}{
		{name: "canonical contract read failure", validationErr: errors.New("forced canonical contract read failure")},
		{name: "canonical contract write failure", validationErr: errors.New("forced canonical contract write failure")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaimedPromotionFixture(t)
			if err := fixture.staged.cleanup(); err != nil {
				t.Fatal(err)
			}
			workspace := fixture.service.resolveTaskWorkspace(fixture.task)
			validatedPaths := make([]string, 0, 2)

			targetPath, err := fixture.service.syncRuntimeProjectValidated(
				context.Background(),
				fixture.task,
				workspace,
				fixture.sessionWorkspace,
				func(projectPath string) error {
					validatedPaths = append(validatedPaths, projectPath)
					if projectPath == fixture.canonicalProject {
						return test.validationErr
					}
					return nil
				},
			)
			if !errors.Is(err, test.validationErr) {
				t.Fatalf("syncRuntimeProjectValidated() error = %v, want %v", err, test.validationErr)
			}
			if targetPath != fixture.canonicalProject {
				t.Fatalf("syncRuntimeProjectValidated() target = %q, want %q", targetPath, fixture.canonicalProject)
			}
			if len(validatedPaths) != 2 || validatedPaths[0] == fixture.canonicalProject || validatedPaths[1] != fixture.canonicalProject {
				t.Fatalf("validated paths = %#v, want staged path followed by canonical path", validatedPaths)
			}
			requirePromotionSentinel(t, fixture.canonicalProject, "session")

			recoveryPattern := filepath.Join(
				workspace.HostDir,
				".slidesmith",
				"project-promotions",
				sanitizePathSegment(fixture.task.ExecutionClaimToken),
				"*",
				"project",
			)
			recoveryProjects, globErr := filepath.Glob(recoveryPattern)
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(recoveryProjects) != 1 {
				t.Fatalf("recovery projects = %#v, want one retained old canonical", recoveryProjects)
			}
			requirePromotionSentinel(t, recoveryProjects[0], "canonical")
			t.Cleanup(func() {
				_ = os.RemoveAll(filepath.Join(workspace.HostDir, ".slidesmith", "project-promotions"))
			})
		})
	}
}

func requirePromotionSentinel(t *testing.T, projectPath, want string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(projectPath, "promotion-sentinel.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("promotion sentinel at %s = %q, want %q", projectPath, raw, want)
	}
}
