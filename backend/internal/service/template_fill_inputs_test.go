package service

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
