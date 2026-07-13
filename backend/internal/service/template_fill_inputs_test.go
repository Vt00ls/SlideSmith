package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTemplateFillCaseFoldUsesPinnedUnicode15Corpus(t *testing.T) {
	if len(templateFillCaseFoldMappings) != templateFillCaseFoldMappingCount {
		t.Fatalf("case-fold mappings = %d, want %d", len(templateFillCaseFoldMappings), templateFillCaseFoldMappingCount)
	}
	if digest := sha256.Sum256(templateFillCaseFoldAsset); strings.ToLower(templateFillCaseFoldAssetSHA256) != strings.ToLower(fmt.Sprintf("%x", digest)) {
		t.Fatalf("case-fold asset SHA-256 = %x, want %s", digest, templateFillCaseFoldAssetSHA256)
	}
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", ".."))
	pythonAsset, err := os.ReadFile(filepath.Join(repoRoot, "runtime", "ppt-master-agent", "scripts", "unicode_casefold_15_0.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(templateFillCaseFoldAsset, pythonAsset) {
		t.Fatal("Go and Python pinned case-fold assets differ")
	}

	tests := map[string]string{
		"I": "i",
		"İ": "i\u0307",
		"ẞ": "ss",
		"ﬃ": "ffi",
		"Σ": "σ",
		"ς": "σ",
		"Ɤ": "Ɤ",
	}
	for input, want := range tests {
		if got := templateFillCaseFold(input); got != want {
			t.Fatalf("templateFillCaseFold(%q) = %q, want %q", input, got, want)
		}
	}
	if templateFillCaseFold("Ɤ") == templateFillCaseFold("ɤ") {
		t.Fatal("Unicode 16 U+A7CB mapping leaked into pinned Unicode 15 folding")
	}

	hash := sha256.New()
	var encoded [4]byte
	for source := rune(0); source <= utf8.MaxRune; source++ {
		if source >= 0xD800 && source <= 0xDFFF {
			continue
		}
		binary.BigEndian.PutUint32(encoded[:], uint32(source))
		_, _ = hash.Write(encoded[:])
		folded := templateFillCaseFold(string(source))
		runeCount := utf8.RuneCountInString(folded)
		if runeCount > 255 {
			t.Fatalf("folded rune count = %d for U+%04X", runeCount, source)
		}
		_, _ = hash.Write([]byte{byte(runeCount)})
		for _, target := range folded {
			binary.BigEndian.PutUint32(encoded[:], uint32(target))
			_, _ = hash.Write(encoded[:])
		}
	}
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != "e7b7267656504e1e9625b731d88c5fe9f669a0f4b07038e76caaf8296ce1769b" {
		t.Fatalf("Unicode 15 full-scalar fold corpus SHA-256 = %s", got)
	}
}

func TestDiscoverTemplateFillInputsFindsSingleDeckAndContent(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand_template.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand_template.md"), "# Template readback\n")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# New content\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand_template.slide_library.json"), `{"slides":[]}`+"\n")

	inputs, err := discoverTemplateFillInputs(projectPath)
	if err != nil {
		t.Fatalf("discoverTemplateFillInputs() error = %v", err)
	}
	canonicalProjectPath := mustCanonicalTemplateFillTestPath(t, projectPath)
	want := TemplateFillInputs{
		ProjectPath:    canonicalProjectPath,
		SourcePPTX:     filepath.Join(canonicalProjectPath, "sources", "brand_template.pptx"),
		SlideLibrary:   filepath.Join(canonicalProjectPath, "analysis", "brand_template.slide_library.json"),
		FillPlan:       filepath.Join(canonicalProjectPath, "analysis", "fill_plan.json"),
		CheckReport:    filepath.Join(canonicalProjectPath, "analysis", "check_report.json"),
		ValidateReport: filepath.Join(canonicalProjectPath, "validation", "validate_report.json"),
		Readback:       filepath.Join(canonicalProjectPath, "validation", "readback.md"),
		ExportBase:     filepath.Join(canonicalProjectPath, "exports", filepath.Base(canonicalProjectPath)+"_template_fill.pptx"),
		ContentSources: []string{filepath.Join(canonicalProjectPath, "sources", "content.md")},
	}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("inputs = %#v, want %#v", inputs, want)
	}
}

func TestDiscoverTemplateFillInputsUsesUnicodeFullCaseFoldForGeneratedReadback(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "straße.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "STRASSE.md"), "# Generated template readback\n")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# New content\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "straße.slide_library.json"), `{"slides":[]}`+"\n")

	inputs, err := discoverTemplateFillInputs(projectPath)
	if err != nil {
		t.Fatalf("discoverTemplateFillInputs() error = %v", err)
	}
	if len(inputs.ContentSources) != 1 || filepath.Base(inputs.ContentSources[0]) != "content.md" {
		t.Fatalf("content sources = %#v, want only exact business content", inputs.ContentSources)
	}
}

func TestDiscoverTemplateFillInputsSortsReadableContentCaseInsensitively(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "template.PPTX"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "zeta.TSV"), "zeta\n")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "alpha.markdown"), "# Alpha\n")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "middle.TEXT"), "Middle\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "template.slide_library.json"), `{"slides":[]}`+"\n")

	inputs, err := discoverTemplateFillInputs(projectPath)
	if err != nil {
		t.Fatalf("discoverTemplateFillInputs() error = %v", err)
	}
	canonicalProjectPath := mustCanonicalTemplateFillTestPath(t, projectPath)
	want := []string{
		filepath.Join(canonicalProjectPath, "sources", "alpha.markdown"),
		filepath.Join(canonicalProjectPath, "sources", "middle.TEXT"),
		filepath.Join(canonicalProjectPath, "sources", "zeta.TSV"),
	}
	if !reflect.DeepEqual(inputs.ContentSources, want) {
		t.Fatalf("ContentSources = %#v, want %#v", inputs.ContentSources, want)
	}
}

func TestDiscoverTemplateFillInputsPreservesExplicitSameStemMarkdown(t *testing.T) {
	workspacePath := t.TempDir()
	projectPath := filepath.Join(workspacePath, "projects", "brand_project")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.md"), "# Uploaded business content\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[]}`+"\n")
	mustWriteFileNoTest(workspacePath, filepath.Join(".slidesmith", "source_inputs.json"), `{
  "schema": "slidesmith.source_inputs.v1",
  "files": [
    {"name": "brand.pptx", "upload_path": "uploads/task-1/brand.pptx", "extension": "pptx"},
    {"name": "brand.md", "upload_path": "uploads/task-1/brand.md", "extension": "md"}
  ]
}`+"\n")

	inputs, err := discoverTemplateFillInputs(projectPath)
	if err != nil {
		t.Fatalf("discoverTemplateFillInputs() error = %v", err)
	}
	canonicalProjectPath := mustCanonicalTemplateFillTestPath(t, projectPath)
	want := []string{filepath.Join(canonicalProjectPath, "sources", "brand.md")}
	if !reflect.DeepEqual(inputs.ContentSources, want) {
		t.Fatalf("ContentSources = %#v, want explicitly uploaded same-stem Markdown %#v", inputs.ContentSources, want)
	}
}

func TestTemplateFillProvenanceKeepsExactCaseAuthorizationAndFingerprintsGeneratedReadback(t *testing.T) {
	workspacePath := t.TempDir()
	if !supportsCaseSensitiveTemplateFillTestPaths(t, workspacePath) {
		t.Skip("filesystem does not support distinct case-sensitive filenames")
	}
	projectPath := filepath.Join(workspacePath, "projects", "brand_project")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "Brand.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.md"), "# Explicit upload\n")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "Brand.md"), "# Generated readback\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "Brand.slide_library.json"), `{"slides":[]}`+"\n")
	mustWriteFileNoTest(workspacePath, filepath.Join(".slidesmith", "source_inputs.json"), `{
  "schema": "slidesmith.source_inputs.v1",
  "files": [
    {"name": "Brand.pptx", "upload_path": "uploads/task-1/Brand.pptx"},
    {"name": "brand.md", "upload_path": "uploads/task-1/brand.md"}
  ]
}`+"\n")

	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !provenance.hasBoundSource("sources/brand.md") {
		t.Fatal("exact uploaded sources/brand.md was not authorized")
	}
	if provenance.hasBoundSource("sources/Brand.md") {
		t.Fatal("generated sources/Brand.md was authorized by a case-folded claim")
	}
	for _, relativePath := range []string{"sources/Brand.pptx", "sources/brand.md", "sources/Brand.md"} {
		if _, ok := provenance.sources[relativePath]; !ok {
			t.Fatalf("provenance omitted exact fingerprint %s: %#v", relativePath, provenance.sources)
		}
	}

	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		t.Fatal(err)
	}
	wantContent := filepath.Join(mustCanonicalTemplateFillTestPath(t, projectPath), "sources", "brand.md")
	if !reflect.DeepEqual(inputs.ContentSources, []string{wantContent}) {
		t.Fatalf("ContentSources = %#v, want only exact upload %s", inputs.ContentSources, wantContent)
	}

	candidateProject := filepath.Join(t.TempDir(), "candidate")
	if err := copyProjectDirectoryStrict(context.Background(), projectPath, candidateProject); err != nil {
		t.Fatal(err)
	}
	mustWriteFileNoTest(candidateProject, filepath.Join("sources", "Brand.md"), "# Mutated generated readback\n")
	if err := provenance.validateCandidate(candidateProject); err == nil || !strings.Contains(err.Error(), "sources/Brand.md") {
		t.Fatalf("validateCandidate() error = %v, want exact generated readback mutation fence", err)
	}
}

func TestTemplateFillProvenanceValidateCandidateRejectsChangedSourceInventory(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "Brand.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")

	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		mutate     func(t *testing.T, candidateProject string)
		wantSource string
	}{
		{
			name: "extra exact-case source",
			mutate: func(t *testing.T, candidateProject string) {
				mustWriteFileNoTest(candidateProject, filepath.Join("sources", "Extra.md"), "# Extra\n")
			},
			wantSource: "sources/Extra.md",
		},
		{
			name: "missing exact-case source",
			mutate: func(t *testing.T, candidateProject string) {
				t.Helper()
				if err := os.Remove(filepath.Join(candidateProject, "sources", "content.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantSource: "sources/content.md",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateProject := filepath.Join(t.TempDir(), "candidate")
			if err := copyProjectDirectoryStrict(context.Background(), projectPath, candidateProject); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, candidateProject)

			if err := provenance.validateCandidate(candidateProject); err == nil || !strings.Contains(err.Error(), test.wantSource) {
				t.Fatalf("validateCandidate() error = %v, want changed inventory rejection naming %s", err, test.wantSource)
			}
		})
	}
}

func TestTemplateFillProvenanceAcceptsImageCompanionDirectory(t *testing.T) {
	projectPath := newTemplateFillCompanionDirectoryProject(t)
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		t.Fatalf("snapshotTemplateFillSourceProvenance() error = %v", err)
	}
	if empty, ok := provenance.sources["sources/brand_files/empty"]; !ok || empty.nodeType != templateFillSourceNodeDirectory {
		t.Fatalf("provenance omitted empty companion directory: %#v", provenance.sources)
	}
	candidateProject := filepath.Join(t.TempDir(), "candidate")
	if err := copyDir(context.Background(), projectPath, candidateProject); err != nil {
		t.Fatal(err)
	}
	if err := provenance.validateCandidate(candidateProject); err != nil {
		t.Fatalf("validateCandidate() error = %v", err)
	}
	inputs, err := discoverTemplateFillInputsWithProvenance(candidateProject, provenance)
	if err != nil {
		t.Fatalf("discoverTemplateFillInputsWithProvenance() error = %v", err)
	}
	if filepath.Base(inputs.SourcePPTX) != "brand.pptx" || len(inputs.ContentSources) != 1 || filepath.Base(inputs.ContentSources[0]) != "content.md" {
		t.Fatalf("discovered companion-directory inputs = %#v", inputs)
	}
}

func TestTemplateFillProvenanceRejectsNestedCompanionDirectoryMutation(t *testing.T) {
	tests := []struct {
		name       string
		wantSource string
		mutate     func(*testing.T, string)
	}{
		{
			name:       "nested add",
			wantSource: "sources/brand_files/nested/added.png",
			mutate: func(t *testing.T, candidateProject string) {
				mustWriteFileNoTest(candidateProject, filepath.Join("sources", "brand_files", "nested", "added.png"), "added")
			},
		},
		{
			name:       "nested remove",
			wantSource: "sources/brand_files/nested/preview.png",
			mutate: func(t *testing.T, candidateProject string) {
				if err := os.Remove(filepath.Join(candidateProject, "sources", "brand_files", "nested", "preview.png")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "nested content",
			wantSource: "sources/brand_files/nested/preview.png",
			mutate: func(t *testing.T, candidateProject string) {
				mustWriteFileNoTest(candidateProject, filepath.Join("sources", "brand_files", "nested", "preview.png"), "changed-preview")
			},
		},
		{
			name:       "empty directory remove",
			wantSource: "sources/brand_files/empty",
			mutate: func(t *testing.T, candidateProject string) {
				if err := os.Remove(filepath.Join(candidateProject, "sources", "brand_files", "empty")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "empty directory add",
			wantSource: "sources/brand_files/added-empty",
			mutate: func(t *testing.T, candidateProject string) {
				if err := os.Mkdir(filepath.Join(candidateProject, "sources", "brand_files", "added-empty"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "file becomes directory",
			wantSource: "sources/brand_files/hero.png",
			mutate: func(t *testing.T, candidateProject string) {
				path := filepath.Join(candidateProject, "sources", "brand_files", "hero.png")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "directory becomes file",
			wantSource: "sources/brand_files/nested",
			mutate: func(t *testing.T, candidateProject string) {
				path := filepath.Join(candidateProject, "sources", "brand_files", "nested")
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				mustWriteFileNoTest(candidateProject, filepath.Join("sources", "brand_files", "nested"), "not-a-directory")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := newTemplateFillCompanionDirectoryProject(t)
			provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
			if err != nil {
				t.Fatalf("snapshotTemplateFillSourceProvenance() error = %v", err)
			}
			candidateProject := filepath.Join(t.TempDir(), "candidate")
			if err := copyDir(context.Background(), projectPath, candidateProject); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, candidateProject)
			if err := provenance.validateCandidate(candidateProject); err == nil || !strings.Contains(err.Error(), test.wantSource) {
				t.Fatalf("validateCandidate() error = %v, want nested inventory rejection naming %s", err, test.wantSource)
			}
		})
	}
}

func TestTemplateFillProvenanceRevalidationRejectsNestedCompanionMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "add",
			mutate: func(t *testing.T, projectPath string) {
				mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand_files", "nested", "added.png"), "added")
			},
		},
		{
			name: "remove",
			mutate: func(t *testing.T, projectPath string) {
				if err := os.Remove(filepath.Join(projectPath, "sources", "brand_files", "nested", "preview.png")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "content",
			mutate: func(t *testing.T, projectPath string) {
				mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand_files", "nested", "preview.png"), "changed-preview")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := newTemplateFillCompanionDirectoryProject(t)
			provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, projectPath)
			if err := provenance.revalidateAuthoritative(); err == nil {
				t.Fatal("revalidateAuthoritative() error = nil, want nested companion mutation rejection")
			}
		})
	}
}

func TestTemplateFillProvenanceRejectsNestedCompanionSymlink(t *testing.T) {
	projectPath := newTemplateFillCompanionDirectoryProject(t)
	symlinkPath := filepath.Join(projectPath, "sources", "brand_files", "nested", "linked.png")
	if err := os.Symlink(filepath.Join(projectPath, "sources", "brand_files", "hero.png"), symlinkPath); err != nil {
		t.Fatal(err)
	}
	_, err := snapshotTemplateFillSourceProvenance(projectPath)
	wantSource := "sources/brand_files/nested/linked.png"
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") || !strings.Contains(filepath.ToSlash(err.Error()), wantSource) {
		t.Fatalf("snapshotTemplateFillSourceProvenance() error = %v, want nested symlink rejection naming %s", err, wantSource)
	}
}

func TestTemplateFillProvenanceRejectsNestedCandidateCompanionSymlink(t *testing.T) {
	projectPath := newTemplateFillCompanionDirectoryProject(t)
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	candidateProject := filepath.Join(t.TempDir(), "candidate")
	if err := copyDir(context.Background(), projectPath, candidateProject); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(candidateProject, "sources", "brand_files", "nested", "linked.png")
	if err := os.Symlink(filepath.Join(candidateProject, "sources", "brand_files", "hero.png"), symlinkPath); err != nil {
		t.Fatal(err)
	}
	err = provenance.validateCandidate(candidateProject)
	wantSource := "sources/brand_files/nested/linked.png"
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") || !strings.Contains(filepath.ToSlash(err.Error()), wantSource) {
		t.Fatalf("validateCandidate() error = %v, want nested candidate symlink rejection naming %s", err, wantSource)
	}
}

func newTemplateFillCompanionDirectoryProject(t *testing.T) string {
	t.Helper()
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand_files", "hero.png"), "hero-image")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand_files", "nested", "preview.png"), "preview-image")
	if err := os.MkdirAll(filepath.Join(projectPath, "sources", "brand_files", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[]}`+"\n")
	return projectPath
}

func TestTemplateFillProvenanceRejectsAmbiguousManifestClaims(t *testing.T) {
	tests := []struct {
		name      string
		filesJSON string
		want      string
	}{
		{
			name: "duplicate exact source across entries",
			filesJSON: `[
        {"name":"brand.md","upload_path":"uploads/task-1/brand.md"},
        {"name":"brand.md","upload_path":"uploads/task-1/brand.md"}
      ]`,
			want: "duplicate",
		},
		{
			name: "case fold collision",
			filesJSON: `[
        {"name":"brand.md","upload_path":"uploads/task-1/brand.md"},
        {"name":"Brand.md","upload_path":"uploads/task-1/Brand.md"}
      ]`,
			want: "case-fold",
		},
		{
			name: "unicode case fold collision",
			filesJSON: `[
        {"name":"Σ.md","upload_path":"uploads/task-1/Σ.md"},
        {"name":"ς.md","upload_path":"uploads/task-1/ς.md"}
      ]`,
			want: "case-fold",
		},
		{
			name: "unicode full case fold expansion collision",
			filesJSON: `[
        {"name":"straße.md","upload_path":"uploads/task-1/straße.md"},
        {"name":"STRASSE.md","upload_path":"uploads/task-1/STRASSE.md"}
      ]`,
			want: "case-fold",
		},
		{
			name:      "one entry authorizes different basenames",
			filesJSON: `[{"name":"brand.md","upload_path":"uploads/task-1/other.md"}]`,
			want:      "ambiguous",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspacePath := t.TempDir()
			projectPath := filepath.Join(workspacePath, "projects", "brand_project")
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.md"), "# Content\n")
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "other.md"), "# Other\n")
			mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[]}`+"\n")
			mustWriteFileNoTest(workspacePath, filepath.Join(".slidesmith", "source_inputs.json"), `{
  "schema":"slidesmith.source_inputs.v1",
  "files":`+test.filesJSON+`
}`+"\n")

			_, err := snapshotTemplateFillSourceProvenance(projectPath)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("snapshotTemplateFillSourceProvenance() error = %v, want %q rejection", err, test.want)
			}
		})
	}
}

func supportsCaseSensitiveTemplateFillTestPaths(t *testing.T, root string) bool {
	t.Helper()
	probeDir := filepath.Join(root, "case-sensitive-probe")
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(probeDir, "Probe")
	lower := filepath.Join(probeDir, "probe")
	if err := os.WriteFile(upper, []byte("upper"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lower, []byte("lower"), 0o644); err != nil {
		return false
	}
	upperRaw, upperErr := os.ReadFile(upper)
	lowerRaw, lowerErr := os.ReadFile(lower)
	return upperErr == nil && lowerErr == nil && string(upperRaw) == "upper" && string(lowerRaw) == "lower"
}

func TestDiscoverTemplateFillInputsCanonicalizesIntermediateProjectSymlink(t *testing.T) {
	realWorkspacePath := t.TempDir()
	realProjectPath := filepath.Join(realWorkspacePath, "projects", "brand_project")
	mustWriteFileNoTest(realProjectPath, filepath.Join("sources", "brand.pptx"), "pptx")
	mustWriteFileNoTest(realProjectPath, filepath.Join("sources", "content.md"), "# Content\n")
	mustWriteFileNoTest(realProjectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[]}`+"\n")

	aliasRoot := t.TempDir()
	aliasWorkspacePath := filepath.Join(aliasRoot, "workspace-alias")
	if err := os.Symlink(realWorkspacePath, aliasWorkspacePath); err != nil {
		t.Fatal(err)
	}
	aliasedProjectPath := filepath.Join(aliasWorkspacePath, "projects", "brand_project")

	inputs, err := discoverTemplateFillInputs(aliasedProjectPath)
	if err != nil {
		t.Fatalf("discoverTemplateFillInputs() error = %v", err)
	}
	canonicalProjectPath := mustCanonicalTemplateFillTestPath(t, realProjectPath)
	want := TemplateFillInputs{
		ProjectPath:    canonicalProjectPath,
		SourcePPTX:     filepath.Join(canonicalProjectPath, "sources", "brand.pptx"),
		SlideLibrary:   filepath.Join(canonicalProjectPath, "analysis", "brand.slide_library.json"),
		FillPlan:       filepath.Join(canonicalProjectPath, "analysis", "fill_plan.json"),
		CheckReport:    filepath.Join(canonicalProjectPath, "analysis", "check_report.json"),
		ValidateReport: filepath.Join(canonicalProjectPath, "validation", "validate_report.json"),
		Readback:       filepath.Join(canonicalProjectPath, "validation", "readback.md"),
		ExportBase:     filepath.Join(canonicalProjectPath, "exports", "brand_project_template_fill.pptx"),
		ContentSources: []string{filepath.Join(canonicalProjectPath, "sources", "content.md")},
	}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("inputs = %#v, want canonical paths %#v", inputs, want)
	}
}

func TestDiscoverTemplateFillInputsRejectsSymlinkedProvenanceDirectory(t *testing.T) {
	workspacePath := t.TempDir()
	projectPath := filepath.Join(workspacePath, "projects", "brand_project")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.md"), "# Uploaded business content\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[]}`+"\n")
	externalMetadata := t.TempDir()
	mustWriteFileNoTest(externalMetadata, "source_inputs.json", `{
  "schema": "slidesmith.source_inputs.v1",
  "files": [{"name": "brand.md", "upload_path": "uploads/task-1/brand.md", "extension": "md"}]
}`+"\n")
	if err := os.Symlink(externalMetadata, filepath.Join(workspacePath, ".slidesmith")); err != nil {
		t.Fatal(err)
	}

	_, err := discoverTemplateFillInputs(projectPath)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("error = %v, want symlinked provenance directory rejection", err)
	}
}

func TestDiscoverTemplateFillInputsRejectsMultiplePPTXDeterministically(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "z.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "a.PPTX"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")

	_, err := discoverTemplateFillInputs(projectPath)
	if err == nil {
		t.Fatal("discoverTemplateFillInputs() error = nil, want multiple presentation failure")
	}
	message := err.Error()
	for _, want := range []string{"exactly one source PPTX", "found 2", "sources/a.PPTX", "sources/z.pptx"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error = %q, want substring %q", message, want)
		}
	}
	if strings.Index(message, "sources/a.PPTX") > strings.Index(message, "sources/z.pptx") {
		t.Fatalf("error filenames are not sorted: %q", message)
	}
}

func TestDiscoverTemplateFillInputsRejectsOtherPresentationFormats(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		count string
	}{
		{name: "pptm", files: []string{"template.PPTM"}, count: "found 1"},
		{name: "ppsx", files: []string{"template.ppsx"}, count: "found 1"},
		{name: "ppsm", files: []string{"template.PPSM"}, count: "found 1"},
		{name: "potx", files: []string{"template.potx"}, count: "found 1"},
		{name: "potm", files: []string{"template.POTM"}, count: "found 1"},
		{name: "pptx beside non pptx presentation", files: []string{"a.pptx", "b.potm"}, count: "found 2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := t.TempDir()
			for _, name := range test.files {
				mustWriteFileNoTest(projectPath, filepath.Join("sources", name), "presentation")
			}
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")

			_, err := discoverTemplateFillInputs(projectPath)
			if err == nil {
				t.Fatal("discoverTemplateFillInputs() error = nil, want presentation format failure")
			}
			message := err.Error()
			if !strings.Contains(message, "exactly one source PPTX") || !strings.Contains(message, test.count) {
				t.Fatalf("error = %q, want PPTX requirement and %q", message, test.count)
			}
			for _, name := range test.files {
				if !strings.Contains(message, filepath.ToSlash(filepath.Join("sources", name))) {
					t.Fatalf("error = %q, want filename %q", message, name)
				}
			}
		})
	}
}

func TestDiscoverTemplateFillInputsRejectsMissingReadableContent(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.md"), "# Generated template readback\n")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "archived.xls"), "xls")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[]}`+"\n")

	_, err := discoverTemplateFillInputs(projectPath)
	if err == nil || !strings.Contains(err.Error(), "requires content source") {
		t.Fatalf("error = %v, want readable content source failure", err)
	}
}

func TestDiscoverTemplateFillInputsRejectsMissingOrEmptySlideLibrary(t *testing.T) {
	for _, test := range []struct {
		name       string
		writeEmpty bool
	}{
		{name: "missing"},
		{name: "empty", writeEmpty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectPath := t.TempDir()
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")
			if test.writeEmpty {
				mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), "")
			}

			_, err := discoverTemplateFillInputs(projectPath)
			if err == nil || !strings.Contains(err.Error(), "requires slide library") || !strings.Contains(err.Error(), "analysis/brand.slide_library.json") {
				t.Fatalf("error = %v, want slide library failure with relative path", err)
			}
		})
	}
}

func TestDiscoverTemplateFillInputsRejectsSymlinkedInputPaths(t *testing.T) {
	t.Run("sources directory", func(t *testing.T) {
		projectPath := t.TempDir()
		externalSources := filepath.Join(t.TempDir(), "sources")
		mustWriteFileNoTest(externalSources, "brand.pptx", "pptx")
		mustWriteFileNoTest(externalSources, "content.md", "# Content\n")
		if err := os.Symlink(externalSources, filepath.Join(projectPath, "sources")); err != nil {
			t.Fatal(err)
		}

		_, err := discoverTemplateFillInputs(projectPath)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
			t.Fatalf("error = %v, want symlinked sources rejection", err)
		}
	})

	t.Run("slide library", func(t *testing.T) {
		projectPath := t.TempDir()
		mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
		mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")
		mustWriteFileNoTest(projectPath, filepath.Join("analysis", "actual.json"), `{"slides":[]}`+"\n")
		if err := os.Symlink("actual.json", filepath.Join(projectPath, "analysis", "brand.slide_library.json")); err != nil {
			t.Fatal(err)
		}

		_, err := discoverTemplateFillInputs(projectPath)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
			t.Fatalf("error = %v, want symlinked slide library rejection", err)
		}
	})

	t.Run("content source", func(t *testing.T) {
		projectPath := t.TempDir()
		mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
		mustWriteFileNoTest(projectPath, filepath.Join("sources", "actual.md"), "# Content\n")
		if err := os.Symlink("actual.md", filepath.Join(projectPath, "sources", "content.md")); err != nil {
			t.Fatal(err)
		}
		mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[]}`+"\n")

		_, err := discoverTemplateFillInputs(projectPath)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
			t.Fatalf("error = %v, want symlinked content rejection", err)
		}
	})
}

func TestDiscoverTemplateFillInputsRejectsSymlinkedOutputPaths(t *testing.T) {
	for _, test := range []struct {
		name       string
		symlinkRel string
		targetFile bool
	}{
		{name: "exports directory", symlinkRel: "exports"},
		{name: "validation directory", symlinkRel: "validation"},
		{name: "fill plan file", symlinkRel: filepath.Join("analysis", "fill_plan.json"), targetFile: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectPath := t.TempDir()
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")
			mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[]}`+"\n")

			targetPath := t.TempDir()
			if test.targetFile {
				targetPath = filepath.Join(targetPath, "outside.json")
				if err := os.WriteFile(targetPath, []byte(`{}`+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(targetPath, filepath.Join(projectPath, test.symlinkRel)); err != nil {
				t.Fatal(err)
			}

			_, err := discoverTemplateFillInputs(projectPath)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") || !strings.Contains(err.Error(), filepath.ToSlash(test.symlinkRel)) {
				t.Fatalf("error = %v, want symlinked output rejection for %s", err, test.symlinkRel)
			}
		})
	}
}

func mustCanonicalTemplateFillTestPath(t *testing.T, path string) string {
	t.Helper()
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v", path, err)
	}
	return canonicalPath
}
