package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type sourcePrepareContractDocument struct {
	Schema                  string                          `json:"schema"`
	Phase                   string                          `json:"phase"`
	Route                   string                          `json:"route"`
	ProjectPath             string                          `json:"project_path"`
	SourceCount             int                             `json:"source_count"`
	NormalizedMarkdownCount int                             `json:"normalized_markdown_count"`
	ConversionProfileCount  int                             `json:"conversion_profile_count"`
	PPTXDeckCount           int                             `json:"pptx_deck_count"`
	HasSourceProfile        bool                            `json:"has_source_profile"`
	SourceProfile           string                          `json:"source_profile"`
	Sources                 []sourcePrepareContractArtifact `json:"sources"`
	Analysis                []sourcePrepareContractArtifact `json:"analysis"`
	Warnings                []string                        `json:"warnings"`
	CheckedAt               string                          `json:"checked_at"`
}

type sourcePrepareContractArtifact struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

func TestValidateSourcePrepareContractCompletePPTXAnalysis(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "project")
	files := map[string]string{
		filepath.Join("sources", "deck.conversion_profile.json"):     `{}`,
		filepath.Join("sources", "deck.md"):                          "# Deck\n",
		filepath.Join("sources", "deck.pptx"):                        "pptx",
		filepath.Join("sources", "explicit.docx"):                    "docx",
		filepath.Join("sources", "explicit.md"):                      "# Explicit Markdown\n",
		filepath.Join("sources", "notes.md"):                         "# Notes\n",
		filepath.Join("sources", "notes.txt"):                        "Notes\n",
		filepath.Join("sources", "nested", "ignored.md"):             "# Nested\n",
		filepath.Join("analysis", "deck.identity.json"):              `{}`,
		filepath.Join("analysis", "deck.slide_library.json"):         `{}`,
		filepath.Join("analysis", "source_profile.json"):             `{}`,
		filepath.Join("analysis", "ignored.json"):                    `{}`,
		filepath.Join("analysis", "nested", "ignored.identity.json"): `{}`,
	}
	for rel, content := range files {
		mustWriteFileNoTest(projectPath, rel, content)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeProjectPath, err := filepath.Rel(workingDirectory, projectPath)
	if err != nil {
		t.Fatal(err)
	}

	contract, err := validateSourcePrepareContract(relativeProjectPath, model.TaskRouteMain)
	if err != nil {
		t.Fatalf("validateSourcePrepareContract() error = %v", err)
	}
	doc := decodeSourcePrepareContract(t, contract)
	wantAbsolutePath, err := filepath.Abs(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "slidesmith.source_prepare_contract.v1" || doc.Phase != string(PhaseSourcePrepare) || doc.Route != model.TaskRouteMain {
		t.Fatalf("unexpected contract identity: %#v", doc)
	}
	if doc.ProjectPath != wantAbsolutePath {
		t.Fatalf("project_path = %q, want %q", doc.ProjectPath, wantAbsolutePath)
	}
	if doc.SourceCount != 4 || doc.NormalizedMarkdownCount != 4 || doc.ConversionProfileCount != 1 || doc.PPTXDeckCount != 1 {
		t.Fatalf("unexpected counts: source=%d normalized=%d profiles=%d decks=%d", doc.SourceCount, doc.NormalizedMarkdownCount, doc.ConversionProfileCount, doc.PPTXDeckCount)
	}
	if !doc.HasSourceProfile || doc.SourceProfile != "analysis/source_profile.json" {
		t.Fatalf("unexpected source profile fields: has=%v path=%q", doc.HasSourceProfile, doc.SourceProfile)
	}
	wantSources := []sourcePrepareContractArtifact{
		{Path: "sources/deck.conversion_profile.json", Kind: model.ArtifactKindSourceConversionProfile, Size: 2},
		{Path: "sources/deck.md", Kind: model.ArtifactKindSourceMarkdown, Size: int64(len("# Deck\n"))},
		{Path: "sources/deck.pptx", Kind: model.ArtifactKindSource, Size: 4},
		{Path: "sources/explicit.docx", Kind: model.ArtifactKindSource, Size: 4},
		{Path: "sources/explicit.md", Kind: model.ArtifactKindSourceMarkdown, Size: int64(len("# Explicit Markdown\n"))},
		{Path: "sources/notes.md", Kind: model.ArtifactKindSourceMarkdown, Size: int64(len("# Notes\n"))},
		{Path: "sources/notes.txt", Kind: model.ArtifactKindSourceMarkdown, Size: int64(len("Notes\n"))},
	}
	if !reflect.DeepEqual(doc.Sources, wantSources) {
		t.Fatalf("sources = %#v, want %#v", doc.Sources, wantSources)
	}
	wantAnalysis := []sourcePrepareContractArtifact{
		{Path: "analysis/deck.identity.json", Kind: model.ArtifactKindPPTXIdentity, Size: 2},
		{Path: "analysis/deck.slide_library.json", Kind: model.ArtifactKindPPTXSlideLibrary, Size: 2},
		{Path: "analysis/source_profile.json", Kind: model.ArtifactKindSourceProfile, Size: 2},
	}
	if !reflect.DeepEqual(doc.Analysis, wantAnalysis) {
		t.Fatalf("analysis = %#v, want %#v", doc.Analysis, wantAnalysis)
	}
	if len(doc.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want empty", doc.Warnings)
	}
	checkedAt, err := time.Parse(time.RFC3339Nano, doc.CheckedAt)
	if err != nil {
		t.Fatalf("checked_at = %q, want RFC3339Nano: %v", doc.CheckedAt, err)
	}
	if checkedAt.Location() != time.UTC || !strings.HasSuffix(doc.CheckedAt, "Z") {
		t.Fatalf("checked_at = %q, want UTC", doc.CheckedAt)
	}

	canonicalPath := filepath.Join(projectPath, ".slidesmith", "contracts", "source_prepare.json")
	compatibilityPath := filepath.Join(projectPath, ".slidesmith", "source_prepare_contract.json")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical contract: %v", err)
	}
	compatibility, err := os.ReadFile(compatibilityPath)
	if err != nil {
		t.Fatalf("read compatibility contract: %v", err)
	}
	if !reflect.DeepEqual(canonical, compatibility) {
		t.Fatalf("contract paths differ:\ncanonical:\n%s\ncompatibility:\n%s", canonical, compatibility)
	}
	var written sourcePrepareContractDocument
	if err := json.Unmarshal(canonical, &written); err != nil {
		t.Fatalf("canonical contract is invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(written, doc) {
		t.Fatalf("written contract = %#v, returned = %#v", written, doc)
	}
}

func TestValidateSourcePrepareContractReturnsCanonicalDecodedMap(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")

	contract, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
	if err != nil {
		t.Fatalf("validateSourcePrepareContract() error = %v", err)
	}
	for _, field := range []string{
		"source_count",
		"normalized_markdown_count",
		"conversion_profile_count",
		"pptx_deck_count",
	} {
		if _, ok := contract[field].(float64); !ok {
			t.Fatalf("contract[%q] type = %T, want float64 from canonical JSON decode", field, contract[field])
		}
	}
	for _, field := range []string{"sources", "analysis", "warnings"} {
		if _, ok := contract[field].([]any); !ok {
			t.Fatalf("contract[%q] type = %T, want []any from canonical JSON decode", field, contract[field])
		}
	}
	sources := contract["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources length = %d, want 1", len(sources))
	}
	if _, ok := sources[0].(map[string]any); !ok {
		t.Fatalf("sources[0] type = %T, want map[string]any", sources[0])
	}

	canonicalRaw, err := os.ReadFile(filepath.Join(projectPath, ".slidesmith", "contracts", "source_prepare.json"))
	if err != nil {
		t.Fatal(err)
	}
	var canonical map[string]any
	if err := json.Unmarshal(canonicalRaw, &canonical); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(contract, canonical) {
		t.Fatalf("returned contract differs from canonical JSON decode:\nreturned: %#v\ncanonical: %#v", contract, canonical)
	}
}

func TestValidateSourcePrepareContractDoesNotPartiallyWriteContracts(t *testing.T) {
	tests := []struct {
		name              string
		canonicalContents string
	}{
		{name: "canonical absent"},
		{name: "canonical unchanged", canonicalContents: "existing canonical\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := t.TempDir()
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")
			canonicalRel := filepath.Join(".slidesmith", "contracts", "source_prepare.json")
			canonicalPath := filepath.Join(projectPath, canonicalRel)
			if test.canonicalContents != "" {
				mustWriteFileNoTest(projectPath, canonicalRel, test.canonicalContents)
			}
			compatibilityPath := filepath.Join(projectPath, ".slidesmith", "source_prepare_contract.json")
			if err := os.MkdirAll(compatibilityPath, 0o755); err != nil {
				t.Fatal(err)
			}

			_, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
			if err == nil {
				t.Fatal("validateSourcePrepareContract() error = nil, want non-regular compatibility target failure")
			}
			if test.canonicalContents == "" {
				if _, statErr := os.Stat(canonicalPath); !os.IsNotExist(statErr) {
					t.Fatalf("canonical contract was partially created, stat error = %v", statErr)
				}
				return
			}
			canonicalRaw, readErr := os.ReadFile(canonicalPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(canonicalRaw) != test.canonicalContents {
				t.Fatalf("canonical contract was partially replaced: got %q, want %q", canonicalRaw, test.canonicalContents)
			}
		})
	}
}

func TestValidateSourcePrepareContractRejectsMissingPPTXSourceProfile(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "deck.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "deck.md"), "# Deck\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "deck.identity.json"), `{}`)
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "deck.slide_library.json"), `{}`)

	_, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
	if err == nil {
		t.Fatal("validateSourcePrepareContract() error = nil, want missing source profile")
	}
	if !strings.Contains(err.Error(), "analysis/source_profile.json") {
		t.Fatalf("error = %q, want source profile path", err)
	}
}

func TestValidateSourcePrepareContractRejectsMissingPerStemPPTXAnalysis(t *testing.T) {
	tests := []struct {
		name       string
		presentRel string
		wantPath   string
	}{
		{name: "identity", presentRel: filepath.Join("analysis", "deck.slide_library.json"), wantPath: "analysis/deck.identity.json"},
		{name: "slide library", presentRel: filepath.Join("analysis", "deck.identity.json"), wantPath: "analysis/deck.slide_library.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := t.TempDir()
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "deck.pptx"), "pptx")
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "deck.md"), "# Deck\n")
			mustWriteFileNoTest(projectPath, filepath.Join("analysis", "source_profile.json"), `{}`)
			mustWriteFileNoTest(projectPath, test.presentRel, `{}`)
			mustWriteFileNoTest(projectPath, filepath.Join("analysis", "other.identity.json"), `{}`)
			mustWriteFileNoTest(projectPath, filepath.Join("analysis", "other.slide_library.json"), `{}`)

			_, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
			if err == nil {
				t.Fatalf("validateSourcePrepareContract() error = nil, want missing %s", test.wantPath)
			}
			if !strings.Contains(err.Error(), test.wantPath) {
				t.Fatalf("error = %q, want exact deck analysis path %q", err, test.wantPath)
			}
		})
	}
}

func TestValidateSourcePrepareContractRejectsCaseFoldedDuplicateDeckStems(t *testing.T) {
	tests := []struct {
		name       string
		secondDeck string
		secondStem string
	}{
		{name: "same spelling different extensions", secondDeck: "deck.pptm", secondStem: "deck"},
		{name: "case folded collision", secondDeck: "DECK.pptm", secondStem: "DECK"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := t.TempDir()
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "deck.pptx"), "pptx")
			mustWriteFileNoTest(projectPath, filepath.Join("sources", test.secondDeck), "pptm")
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "deck.md"), "# Deck\n")
			mustWriteFileNoTest(projectPath, filepath.Join("analysis", "source_profile.json"), `{}`)
			mustWriteFileNoTest(projectPath, filepath.Join("analysis", "deck.identity.json"), `{}`)
			mustWriteFileNoTest(projectPath, filepath.Join("analysis", "deck.slide_library.json"), `{}`)
			if test.secondStem != "deck" {
				mustWriteFileNoTest(projectPath, filepath.Join("analysis", test.secondStem+".identity.json"), `{}`)
				mustWriteFileNoTest(projectPath, filepath.Join("analysis", test.secondStem+".slide_library.json"), `{}`)
			}

			_, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
			if err == nil {
				t.Fatal("validateSourcePrepareContract() error = nil, want deck stem collision")
			}
			if !strings.Contains(strings.ToLower(err.Error()), "deck stem collision") {
				t.Fatalf("error = %q, want clear deck stem collision", err)
			}
		})
	}
}

func TestValidateSourcePrepareContractRejectsPinnedUnicodeFoldDeckCollision(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "straße.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "STRASSE.pptm"), "pptm")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")

	_, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
	if err == nil {
		t.Fatal("validateSourcePrepareContract() error = nil, want Unicode full-fold deck stem collision")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "deck stem collision") {
		t.Fatalf("error = %q, want clear deck stem collision", err)
	}
}

func TestValidateSourcePrepareContractAcceptsReadableMainSources(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{name: "markdown", file: "content.md"},
		{name: "text extension", file: "content.text"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := t.TempDir()
			mustWriteFileNoTest(projectPath, filepath.Join("sources", test.file), "Readable\n")

			contract, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
			if err != nil {
				t.Fatalf("validateSourcePrepareContract() error = %v", err)
			}
			doc := decodeSourcePrepareContract(t, contract)
			if doc.SourceCount != 1 || doc.NormalizedMarkdownCount != 1 {
				t.Fatalf("unexpected counts: source=%d normalized=%d", doc.SourceCount, doc.NormalizedMarkdownCount)
			}
			if len(doc.Sources) != 1 || doc.Sources[0].Kind != model.ArtifactKindSourceMarkdown {
				t.Fatalf("sources = %#v, want one source_markdown", doc.Sources)
			}
		})
	}
}

func TestValidateSourcePrepareContractHandlesLegacyXLS(t *testing.T) {
	t.Run("main rejects archive only input", func(t *testing.T) {
		projectPath := t.TempDir()
		mustWriteFileNoTest(projectPath, filepath.Join("sources", "ledger.xls"), "xls")

		_, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
		if err == nil {
			t.Fatal("validateSourcePrepareContract() error = nil, want readable source failure")
		}
		if !strings.Contains(err.Error(), "readable") {
			t.Fatalf("error = %q, want readable source failure", err)
		}
	})

	t.Run("non main reports warning", func(t *testing.T) {
		projectPath := t.TempDir()
		mustWriteFileNoTest(projectPath, filepath.Join("sources", "ledger.xls"), "xls")

		contract, err := validateSourcePrepareContract(projectPath, model.TaskRouteBeautify)
		if err != nil {
			t.Fatalf("validateSourcePrepareContract() error = %v", err)
		}
		doc := decodeSourcePrepareContract(t, contract)
		wantWarnings := []string{"sources/ledger.xls: legacy .xls archived only; no Markdown conversion"}
		if !reflect.DeepEqual(doc.Warnings, wantWarnings) {
			t.Fatalf("warnings = %#v, want %#v", doc.Warnings, wantWarnings)
		}
		if doc.SourceCount != 1 || doc.NormalizedMarkdownCount != 0 {
			t.Fatalf("unexpected counts: source=%d normalized=%d", doc.SourceCount, doc.NormalizedMarkdownCount)
		}
	})
}

func TestValidateSourcePrepareContractRequiresRegularSourceFiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(string)
	}{
		{name: "missing sources directory", setup: func(string) {}},
		{name: "empty sources directory", setup: func(projectPath string) {
			if err := os.MkdirAll(filepath.Join(projectPath, "sources"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "nested source only", setup: func(projectPath string) {
			mustWriteFileNoTest(projectPath, filepath.Join("sources", "nested", "content.md"), "# Nested\n")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := t.TempDir()
			test.setup(projectPath)

			_, err := validateSourcePrepareContract(projectPath, model.TaskRouteMain)
			if err == nil {
				t.Fatal("validateSourcePrepareContract() error = nil, want sources validation failure")
			}
			if !strings.Contains(err.Error(), "sources") {
				t.Fatalf("error = %q, want sources validation failure", err)
			}
		})
	}
}

func TestProcessPrepareAddsSourceContractToPhaseOutput(t *testing.T) {
	service, repo, task, _ := templateResolvePrepareService(t)

	if err := service.processPrepare(context.Background(), task); err != nil {
		t.Fatalf("processPrepare() error = %v", err)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase != string(PhaseSourcePrepare) {
			continue
		}
		if phaseRun.Status != PhaseRunStatusSucceeded {
			t.Fatalf("source_prepare status = %q, want succeeded", phaseRun.Status)
		}
		var output struct {
			SourceContract map[string]any `json:"source_contract"`
		}
		if err := json.Unmarshal([]byte(phaseRun.OutputJSON), &output); err != nil {
			t.Fatalf("invalid source_prepare output: %v", err)
		}
		if output.SourceContract == nil {
			t.Fatalf("source_prepare output missing source_contract: %s", phaseRun.OutputJSON)
		}
		doc := decodeSourcePrepareContract(t, output.SourceContract)
		if doc.SourceCount != 1 || doc.NormalizedMarkdownCount != 1 {
			t.Fatalf("unexpected phase contract counts: source=%d normalized=%d", doc.SourceCount, doc.NormalizedMarkdownCount)
		}
		return
	}
	t.Fatal("source_prepare phase run not found")
}

type sameStemMixedSourcePrepareAgent struct{}

func (sameStemMixedSourcePrepareAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (sameStemMixedSourcePrepareAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	sessionWorkspace, err := distinctTestAgentWorkspace(ctx, req)
	if err != nil {
		return nil, err
	}
	project := filepath.Join(sessionWorkspace, "projects", "task_template_ppt169_20260708")
	mustWriteFileNoTest(project, filepath.Join("sources", "foo.pdf"), "pdf")
	mustWriteFileNoTest(project, filepath.Join("sources", "foo.txt"), "text")
	mustWriteFileNoTest(project, filepath.Join("sources", "foo.md"), "# PDF conversion\n")
	mustWriteFileNoTest(project, filepath.Join("sources", "foo_2.md"), "# Text conversion\n")
	exitCode := 0
	return &AgentRunResult{
		RunID:         "run-same-stem-mixed",
		SessionID:     "session-same-stem-mixed",
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: sessionWorkspace,
	}, nil
}

func TestProcessPrepareUsesExactManifestSourceCountForSameStemMixedSources(t *testing.T) {
	service, repo, task, _ := templateResolvePrepareService(t)
	ctx := context.Background()
	if err := repo.DB().Where("task_id = ?", task.ID).Delete(&model.Artifact{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, source := range []struct {
		name    string
		content string
	}{
		{name: "foo.pdf", content: "pdf upload"},
		{name: "foo.txt", content: "text upload"},
	} {
		objectKey := filepath.ToSlash(filepath.Join("tasks", task.ID, "source", source.name))
		mustWriteFile(t, service.storage.Path(objectKey), source.content)
		if err := repo.CreateArtifact(ctx, &model.Artifact{
			TaskID:    task.ID,
			Kind:      model.ArtifactKindSource,
			Name:      source.name,
			ObjectKey: objectKey,
			Storage:   "local",
		}); err != nil {
			t.Fatal(err)
		}
	}
	service.agent = sameStemMixedSourcePrepareAgent{}

	if err := service.processPrepare(ctx, task); err != nil {
		t.Fatalf("processPrepare() error = %v", err)
	}
	phaseRuns, err := repo.ListPhaseRuns(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase != string(PhaseSourcePrepare) {
			continue
		}
		var output struct {
			SourceContract map[string]any `json:"source_contract"`
		}
		if err := json.Unmarshal([]byte(phaseRun.OutputJSON), &output); err != nil {
			t.Fatal(err)
		}
		doc := decodeSourcePrepareContract(t, output.SourceContract)
		if doc.SourceCount != 2 {
			t.Fatalf("source_count = %d, want exact manifest input count 2", doc.SourceCount)
		}
		return
	}
	t.Fatal("source_prepare phase run not found")
}

type invalidSourcePrepareAgent struct{}

func (invalidSourcePrepareAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (invalidSourcePrepareAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	sessionWorkspace, err := distinctTestAgentWorkspace(ctx, req)
	if err != nil {
		return nil, err
	}
	project := filepath.Join(sessionWorkspace, "projects", "task_template_ppt169_20260708")
	mustWriteFileNoTest(project, filepath.Join("sources", "ledger.xls"), "xls")
	exitCode := 0
	return &AgentRunResult{
		RunID:         "run-invalid-prepare",
		SessionID:     "session-invalid-prepare",
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: sessionWorkspace,
	}, nil
}

func TestProcessPrepareFailsSourceContractWithMetadata(t *testing.T) {
	service, repo, task, workspaceRoot := templateResolvePrepareService(t)
	service.agent = invalidSourcePrepareAgent{}

	err := service.processPrepare(context.Background(), task)
	if err == nil {
		t.Fatal("processPrepare() error = nil, want source contract failure")
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed {
		t.Fatalf("status = %q, want failed", updated.Status)
	}
	if updated.FailurePhase != "source_prepare.contract" {
		t.Fatalf("failure phase = %q, want source_prepare.contract", updated.FailurePhase)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(updated.FailureMetadata), &metadata); err != nil {
		t.Fatalf("invalid failure metadata: %v", err)
	}
	wantProjectPath := filepath.Join(workspaceRoot, task.RuntimeProject, "projects", "task_template_ppt169_20260708")
	for key, want := range map[string]string{
		"workspace_path": filepath.Join(workspaceRoot, task.RuntimeProject),
		"project_path":   wantProjectPath,
		"route":          model.TaskRouteMain,
	} {
		if metadata[key] != want {
			t.Fatalf("metadata[%q] = %#v, want %q; metadata=%#v", key, metadata[key], want, metadata)
		}
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase == string(PhaseSourcePrepare) {
			if phaseRun.Status != PhaseRunStatusFailed {
				t.Fatalf("source_prepare status = %q, want failed", phaseRun.Status)
			}
			return
		}
	}
	t.Fatal("source_prepare phase run not found")
}

func decodeSourcePrepareContract(t *testing.T, contract map[string]any) sourcePrepareContractDocument {
	t.Helper()
	raw, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	var doc sourcePrepareContractDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}
