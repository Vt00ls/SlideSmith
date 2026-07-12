//go:build darwin || linux

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"golang.org/x/sys/unix"
)

func TestStagePreparedProjectRejectsFIFOMember(t *testing.T) {
	service, _, task, canonicalProject, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, nil)
	sessionWorkspace := filepath.Join(t.TempDir(), "workspace")
	if err := copyDir(context.Background(), workspacePath, sessionWorkspace); err != nil {
		t.Fatal(err)
	}
	sessionProject := filepath.Join(sessionWorkspace, "projects", filepath.Base(canonicalProject))
	fifoPath := filepath.Join(sessionProject, "sources", "blocked-input.fifo")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	staged, err := service.stagePreparedProject(context.Background(), task, sessionWorkspace, workspacePath)
	if staged != nil {
		staged.cleanup()
	}
	if err == nil {
		t.Fatal("stagePreparedProject() error = nil, want FIFO-member rejection")
	}
}

func TestCopyProjectDirectoryStrictRejectsRacedFileSymlinkWithSameTargetInode(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "source")
	targetRoot := filepath.Join(t.TempDir(), "target")
	sourceFile := filepath.Join(sourceRoot, "payload.txt")
	mustWriteFile(t, sourceFile, "trusted payload")
	outsideLink := filepath.Join(t.TempDir(), "outside-hardlink.txt")
	if err := os.Link(sourceFile, outsideLink); err != nil {
		t.Skipf("hard link unavailable: %v", err)
	}

	err := copyProjectDirectoryStrictWithHook(context.Background(), sourceRoot, targetRoot, func(relativePath string) error {
		if relativePath != "payload.txt" {
			return nil
		}
		if err := os.Rename(sourceFile, sourceFile+".moved"); err != nil {
			return err
		}
		return os.Symlink(outsideLink, sourceFile)
	})
	if err == nil {
		t.Fatal("copyProjectDirectoryStrictWithHook() error = nil, want raced symlink rejection")
	}
	if _, statErr := os.Stat(filepath.Join(targetRoot, "payload.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("raced symlink bytes reached staging: %v", statErr)
	}
}

func TestCopyProjectDirectoryStrictRejectsRacedIntermediateDirectorySymlink(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "source")
	targetRoot := filepath.Join(t.TempDir(), "target")
	sourceDir := filepath.Join(sourceRoot, "content")
	mustWriteFile(t, filepath.Join(sourceDir, "payload.txt"), "trusted payload")
	outsideDir := t.TempDir()
	mustWriteFile(t, filepath.Join(outsideDir, "payload.txt"), "outside payload")

	err := copyProjectDirectoryStrictWithHook(context.Background(), sourceRoot, targetRoot, func(relativePath string) error {
		if relativePath != "content" {
			return nil
		}
		if err := os.Rename(sourceDir, sourceDir+".moved"); err != nil {
			return err
		}
		return os.Symlink(outsideDir, sourceDir)
	})
	if err == nil {
		t.Fatal("copyProjectDirectoryStrictWithHook() error = nil, want intermediate symlink rejection")
	}
	if _, statErr := os.Stat(filepath.Join(targetRoot, "content", "payload.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("intermediate symlink bytes reached staging: %v", statErr)
	}
}
