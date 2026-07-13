//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTemplateFillProvenanceRejectsNestedCompanionSpecialNode(t *testing.T) {
	projectPath := newTemplateFillCompanionDirectoryProject(t)
	fifoPath := filepath.Join(projectPath, "sources", "brand_files", "nested", "source.pipe")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := snapshotTemplateFillSourceProvenance(projectPath)
	wantSource := "sources/brand_files/nested/source.pipe"
	if err == nil || !strings.Contains(filepath.ToSlash(err.Error()), wantSource) {
		t.Fatalf("snapshotTemplateFillSourceProvenance() error = %v, want nested special-node rejection naming %s", err, wantSource)
	}
}

func TestTemplateFillProvenanceRejectsNestedCandidateCompanionSpecialNode(t *testing.T) {
	projectPath := newTemplateFillCompanionDirectoryProject(t)
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	candidateProject := filepath.Join(t.TempDir(), "candidate")
	if err := copyDir(context.Background(), projectPath, candidateProject); err != nil {
		t.Fatal(err)
	}
	fifoPath := filepath.Join(candidateProject, "sources", "brand_files", "nested", "source.pipe")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	err = provenance.validateCandidate(candidateProject)
	wantSource := "sources/brand_files/nested/source.pipe"
	if err == nil || !strings.Contains(filepath.ToSlash(err.Error()), wantSource) {
		t.Fatalf("validateCandidate() error = %v, want nested candidate special-node rejection naming %s", err, wantSource)
	}
}
