package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadTemplateFillJSONObjectWithSHA256HashesValidatedBytes(t *testing.T) {
	projectPath := t.TempDir()
	raw := []byte("{\n  \"schema\": \"example.v1\",\n  \"value\": 7\n}\n")
	path := filepath.Join(projectPath, "report.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	object, digest, err := readTemplateFillJSONObjectWithSHA256(path, "test report")
	if err != nil {
		t.Fatal(err)
	}
	if object["schema"] != "example.v1" {
		t.Fatalf("object = %#v", object)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if digest != wantDigest {
		t.Fatalf("digest = %q, want %q", digest, wantDigest)
	}
}

func TestValidateTemplateFillPlanContractWritesContract(t *testing.T) {
	projectPath := templateFillContractProject(t)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	for _, directory := range []string{"svg_output", "svg_final"} {
		if err := os.MkdirAll(filepath.Join(projectPath, directory), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
	}

	contract, err := validateTemplateFillPlanContract(projectPath)
	if err != nil {
		t.Fatalf("validateTemplateFillPlanContract() error = %v", err)
	}
	canonicalProjectPath := canonicalTemplateFillContractPath(t, projectPath)
	wantFields := map[string]any{
		"phase":                string(PhaseTemplateFillPlan),
		"project_path":         canonicalProjectPath,
		"source_pptx":          filepath.Join(canonicalProjectPath, "sources", "brand.pptx"),
		"slide_library":        filepath.Join(canonicalProjectPath, "analysis", "brand.slide_library.json"),
		"fill_plan":            filepath.Join(canonicalProjectPath, "analysis", "fill_plan.json"),
		"plan_status":          "draft",
		"planned_slide_count":  1,
		"content_source_count": 1,
	}
	for field, want := range wantFields {
		if got := contract[field]; got != want {
			t.Fatalf("contract[%q] = %#v, want %#v", field, got, want)
		}
	}
	if _, ok := contract["planned_slide_count"].(int); !ok {
		t.Fatalf("planned_slide_count type = %T, want int", contract["planned_slide_count"])
	}
	requireTemplateFillCheckedAt(t, contract)

	contractPath := filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_plan.json")
	requireFileExists(t, contractPath)
	report := readTemplateFillContractReport(t, contractPath)
	if report["phase"] != string(PhaseTemplateFillPlan) {
		t.Fatalf("report phase = %#v, want %q", report["phase"], PhaseTemplateFillPlan)
	}
	if report["project_path"] != canonicalProjectPath {
		t.Fatalf("report project_path = %#v, want %q", report["project_path"], canonicalProjectPath)
	}
}

func TestValidateTemplateFillPlanContractReportsMissingAndCorruptJSON(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, projectPath string)
		want    string
	}{
		{
			name: "missing fill plan",
			arrange: func(t *testing.T, projectPath string) {
				t.Helper()
			},
			want: "read template fill plan",
		},
		{
			name: "corrupt fill plan",
			arrange: func(t *testing.T, projectPath string) {
				t.Helper()
				mustWriteFileNoTest(projectPath, filepath.Join("analysis", "fill_plan.json"), "{\n")
			},
			want: "parse template fill plan",
		},
		{
			name: "corrupt slide library",
			arrange: func(t *testing.T, projectPath string) {
				t.Helper()
				mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
				mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), "{\n")
			},
			want: "parse template fill slide library",
		},
		{
			name: "slide library trailing JSON",
			arrange: func(t *testing.T, projectPath string) {
				t.Helper()
				mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
				mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{"slides":[{"slide_index":1}]}`+"\n{}\n")
			},
			want: "multiple JSON values",
		},
		{
			name: "slide library non-object JSON",
			arrange: func(t *testing.T, projectPath string) {
				t.Helper()
				mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
				mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), "[]\n")
			},
			want: "JSON object",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			test.arrange(t, projectPath)
			_, err := validateTemplateFillPlanContract(projectPath)
			requireTemplateFillContractError(t, err, test.want)
		})
	}
}

func TestValidateTemplateFillPlanContractRejectsInvalidShapeStatusSlidesAndLayout(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(plan map[string]any)
		want   string
	}{
		{
			name: "schema",
			mutate: func(plan map[string]any) {
				plan["schema"] = "template_fill_pptx_plan.v0"
			},
			want: "schema",
		},
		{
			name: "status type",
			mutate: func(plan map[string]any) {
				plan["status"] = 7
			},
			want: "status",
		},
		{
			name: "status value",
			mutate: func(plan map[string]any) {
				plan["status"] = "approved"
			},
			want: "status",
		},
		{
			name: "slides missing",
			mutate: func(plan map[string]any) {
				delete(plan, "slides")
			},
			want: "slides",
		},
		{
			name: "slides wrong type",
			mutate: func(plan map[string]any) {
				plan["slides"] = "slide"
			},
			want: "slides",
		},
		{
			name: "slides empty",
			mutate: func(plan map[string]any) {
				plan["slides"] = []any{}
			},
			want: "slides",
		},
		{
			name: "slide wrong type",
			mutate: func(plan map[string]any) {
				plan["slides"] = []any{"slide"}
			},
			want: "slides[0]",
		},
		{
			name: "source slide zero",
			mutate: func(plan map[string]any) {
				templateFillContractFirstSlide(plan)["source_slide"] = 0
			},
			want: "source_slide",
		},
		{
			name: "source slide fractional",
			mutate: func(plan map[string]any) {
				templateFillContractFirstSlide(plan)["source_slide"] = 1.5
			},
			want: "source_slide",
		},
		{
			name: "source slide string",
			mutate: func(plan map[string]any) {
				templateFillContractFirstSlide(plan)["source_slide"] = "1"
			},
			want: "source_slide",
		},
		{
			name: "purpose empty",
			mutate: func(plan map[string]any) {
				templateFillContractFirstSlide(plan)["purpose"] = "  "
			},
			want: "purpose",
		},
		{
			name: "layout missing",
			mutate: func(plan map[string]any) {
				delete(templateFillContractFirstSlide(plan), "layout_rationale")
			},
			want: "layout_rationale",
		},
		{
			name: "layout wrong type",
			mutate: func(plan map[string]any) {
				templateFillContractFirstSlide(plan)["layout_rationale"] = "content"
			},
			want: "layout_rationale",
		},
		{
			name: "layout pattern blank",
			mutate: func(plan map[string]any) {
				templateFillContractFirstLayout(plan)["layout_pattern"] = ""
			},
			want: "layout_pattern",
		},
		{
			name: "why fit missing",
			mutate: func(plan map[string]any) {
				delete(templateFillContractFirstLayout(plan), "why_fit")
			},
			want: "why_fit",
		},
		{
			name: "risk wrong type",
			mutate: func(plan map[string]any) {
				templateFillContractFirstLayout(plan)["risk"] = true
			},
			want: "risk",
		},
		{
			name: "replacements wrong type",
			mutate: func(plan map[string]any) {
				templateFillContractFirstSlide(plan)["replacements"] = "none"
			},
			want: "replacements",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			plan := templateFillContractPlan("draft", 1)
			test.mutate(plan)
			mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "fill_plan.json"), plan)

			_, err := validateTemplateFillPlanContract(projectPath)
			requireTemplateFillContractError(t, err, test.want)
		})
	}

	t.Run("root must be object", func(t *testing.T) {
		projectPath := templateFillContractProject(t)
		mustWriteFileNoTest(projectPath, filepath.Join("analysis", "fill_plan.json"), "[]\n")
		_, err := validateTemplateFillPlanContract(projectPath)
		requireTemplateFillContractError(t, err, "JSON object")
	})
}

func TestValidateTemplateFillPlanContractRejectsInvalidSourcePath(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "absolute", source: "/tmp/brand.pptx"},
		{name: "outside", source: "../brand.pptx"},
		{name: "noncanonical alias", source: "sources/../sources/brand.pptx"},
		{name: "wrong source", source: "sources/other.pptx"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			plan := templateFillContractPlan("draft", 1)
			plan["source_pptx"] = test.source
			mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "fill_plan.json"), plan)
			_, err := validateTemplateFillPlanContract(projectPath)
			requireTemplateFillContractError(t, err, "source_pptx")
		})
	}

	t.Run("symlink alias", func(t *testing.T) {
		projectPath := templateFillContractProject(t)
		if err := os.Symlink(filepath.Join(projectPath, "sources"), filepath.Join(projectPath, "source_alias")); err != nil {
			t.Fatalf("symlink source alias: %v", err)
		}
		plan := templateFillContractPlan("draft", 1)
		plan["source_pptx"] = "source_alias/brand.pptx"
		mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "fill_plan.json"), plan)
		_, err := validateTemplateFillPlanContract(projectPath)
		requireTemplateFillContractError(t, err, "source_pptx")
	})
}

func TestValidateTemplateFillPlanContractRequiresExistingSourceSlide(t *testing.T) {
	projectPath := templateFillContractProject(t)
	plan := templateFillContractPlan("draft", 1)
	templateFillContractFirstSlide(plan)["source_slide"] = 99
	mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "fill_plan.json"), plan)

	_, err := validateTemplateFillPlanContract(projectPath)
	requireTemplateFillContractError(t, err, "source_slide 99")
}

func TestValidateTemplateFillPlanContractRejectsMainRouteOutputs(t *testing.T) {
	tests := []string{
		"design_spec.md",
		"spec_lock.md",
		filepath.Join("svg_output", "01.svg"),
		filepath.Join("svg_final", "nested", "01.SVG"),
	}
	for _, relativePath := range tests {
		t.Run(filepath.ToSlash(relativePath), func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
			mustWriteFileNoTest(projectPath, relativePath, "generated by wrong workflow\n")
			_, err := validateTemplateFillPlanContract(projectPath)
			requireTemplateFillContractError(t, err, filepath.Base(relativePath))
		})
	}
}

func TestValidateTemplateFillPlanContractAllowsDirectoryNamedSVG(t *testing.T) {
	projectPath := templateFillContractProject(t)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	if err := os.MkdirAll(filepath.Join(projectPath, "svg_output", "assets.svg"), 0o755); err != nil {
		t.Fatalf("mkdir svg-suffixed directory: %v", err)
	}

	if _, err := validateTemplateFillPlanContract(projectPath); err != nil {
		t.Fatalf("validateTemplateFillPlanContract() error = %v", err)
	}
}

func TestTemplateFillExpectedSlideCountReadsValidatedPlan(t *testing.T) {
	projectPath := templateFillContractProject(t)
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", 2)

	count, err := templateFillExpectedSlideCount(projectPath)
	if err != nil {
		t.Fatalf("templateFillExpectedSlideCount() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("templateFillExpectedSlideCount() = %d, want 2", count)
	}
}

func TestValidateTemplateFillCheckContractWritesContract(t *testing.T) {
	projectPath := templateFillContractProject(t)
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "check_report.json"), `{
  "schema": "template_fill_pptx_check.v1",
  "summary": {"ok": 2, "warn": 1, "error": 0},
  "results": []
}`+"\n")

	contract, err := validateTemplateFillCheckContract(projectPath, true)
	if err != nil {
		t.Fatalf("validateTemplateFillCheckContract() error = %v", err)
	}
	canonicalProjectPath := canonicalTemplateFillContractPath(t, projectPath)
	if contract["phase"] != string(PhaseTemplateFillCheck) {
		t.Fatalf("phase = %#v, want %q", contract["phase"], PhaseTemplateFillCheck)
	}
	if contract["project_path"] != canonicalProjectPath {
		t.Fatalf("project_path = %#v, want %q", contract["project_path"], canonicalProjectPath)
	}
	if contract["check_report"] != filepath.Join(canonicalProjectPath, "analysis", "check_report.json") {
		t.Fatalf("check_report = %#v", contract["check_report"])
	}
	if got := templateFillContractSummaryCount(t, contract, "warn"); got != 1 {
		t.Fatalf("summary.warn = %d, want 1", got)
	}
	if contract["plan_status"] != "confirmed" {
		t.Fatalf("plan_status = %#v, want confirmed", contract["plan_status"])
	}
	for _, field := range []string{"plan_sha256", "check_report_sha256"} {
		value, ok := contract[field].(string)
		if !ok || len(value) != 64 {
			t.Fatalf("%s = %#v, want SHA-256 digest", field, contract[field])
		}
	}
	requireTemplateFillCheckedAt(t, contract)
	requireFileExists(t, filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json"))
}

func TestValidateTemplateFillCheckContractReportsMissingAndCorruptJSON(t *testing.T) {
	validReport := `{
  "schema": "template_fill_pptx_check.v1",
  "summary": {"ok": 1, "warn": 0, "error": 0},
  "results": []
}`
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "missing", want: "read template fill check report"},
		{name: "corrupt", content: "{\n", want: "parse template fill check report"},
		{name: "trailing JSON", content: validReport + "\n{}\n", want: "multiple JSON values"},
		{name: "non-object JSON", content: "[]\n", want: "JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
			if test.content != "" {
				mustWriteFileNoTest(projectPath, filepath.Join("analysis", "check_report.json"), test.content)
			}
			_, err := validateTemplateFillCheckContract(projectPath, false)
			requireTemplateFillContractError(t, err, test.want)
		})
	}
}

func TestValidateTemplateFillCheckContractValidatesSummaryAndErrorGate(t *testing.T) {
	tests := []struct {
		name            string
		schema          string
		summary         any
		requireNoErrors bool
		want            string
	}{
		{name: "schema", schema: "template_fill_pptx_check.v0", summary: map[string]any{"ok": 1, "warn": 0, "error": 0}, want: "schema"},
		{name: "summary object", schema: "template_fill_pptx_check.v1", summary: "ok", want: "summary"},
		{name: "missing ok", schema: "template_fill_pptx_check.v1", summary: map[string]any{"warn": 0, "error": 0}, want: "summary.ok"},
		{name: "warn string", schema: "template_fill_pptx_check.v1", summary: map[string]any{"ok": 1, "warn": "0", "error": 0}, want: "summary.warn"},
		{name: "negative error", schema: "template_fill_pptx_check.v1", summary: map[string]any{"ok": 1, "warn": 0, "error": -1}, want: "summary.error"},
		{name: "fractional ok", schema: "template_fill_pptx_check.v1", summary: map[string]any{"ok": 1.5, "warn": 0, "error": 0}, want: "summary.ok"},
		{name: "blocking error", schema: "template_fill_pptx_check.v1", summary: map[string]any{"ok": 0, "warn": 0, "error": 1}, requireNoErrors: true, want: "summary.error = 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
			report := map[string]any{"schema": test.schema, "summary": test.summary, "results": []any{}}
			mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "check_report.json"), report)
			_, err := validateTemplateFillCheckContract(projectPath, test.requireNoErrors)
			requireTemplateFillContractError(t, err, test.want)
		})
	}

	t.Run("errors allowed at plan gate", func(t *testing.T) {
		projectPath := templateFillContractProject(t)
		mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
		report := map[string]any{
			"schema":  "template_fill_pptx_check.v1",
			"summary": map[string]any{"ok": 0, "warn": 0, "error": 1},
			"results": []any{},
		}
		mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "check_report.json"), report)
		if _, err := validateTemplateFillCheckContract(projectPath, false); err != nil {
			t.Fatalf("validateTemplateFillCheckContract(requireNoErrors=false) error = %v", err)
		}
	})
}

func TestValidateTemplateFillApplyContractUsesLatestPPTXAndWritesContract(t *testing.T) {
	projectPath := templateFillContractProject(t)
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", 2)
	for _, directory := range []string{"svg_output", "svg_final"} {
		if err := os.MkdirAll(filepath.Join(projectPath, directory), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
	}
	oldExport := filepath.Join(projectPath, "exports", "old.pptx")
	latestExport := filepath.Join(projectPath, "exports", "latest.pptx")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "old.pptx"), 1)
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "latest.pptx"), 2)
	oldTime := time.Now().Add(-time.Hour)
	latestTime := time.Now()
	if err := os.Chtimes(oldExport, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old export: %v", err)
	}
	if err := os.Chtimes(latestExport, latestTime, latestTime); err != nil {
		t.Fatalf("chtimes latest export: %v", err)
	}

	contract, err := validateTemplateFillApplyContract(projectPath)
	if err != nil {
		t.Fatalf("validateTemplateFillApplyContract() error = %v", err)
	}
	if contract["phase"] != string(PhaseTemplateFillApply) {
		t.Fatalf("phase = %#v, want %q", contract["phase"], PhaseTemplateFillApply)
	}
	wantLatestExport := filepath.Join(canonicalTemplateFillContractPath(t, projectPath), "exports", "latest.pptx")
	if contract["export"] != wantLatestExport {
		t.Fatalf("export = %#v, want %q", contract["export"], wantLatestExport)
	}
	if contract["planned_slide_count"] != 2 || contract["slide_count"] != 2 {
		t.Fatalf("slide counts = planned %#v, actual %#v", contract["planned_slide_count"], contract["slide_count"])
	}
	requireTemplateFillCheckedAt(t, contract)
	requireFileExists(t, filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_apply.json"))
}

func TestValidateTemplateFillApplyContractRequiresConfirmedPlanAndMatchingPPTX(t *testing.T) {
	t.Run("confirmed plan", func(t *testing.T) {
		projectPath := templateFillContractProject(t)
		mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
		mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 1)
		_, err := validateTemplateFillApplyContract(projectPath)
		requireTemplateFillContractError(t, err, `status = "draft", expected "confirmed"`)
	})

	t.Run("export exists", func(t *testing.T) {
		projectPath := templateFillContractProject(t)
		mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
		_, err := validateTemplateFillApplyContract(projectPath)
		requireTemplateFillContractError(t, err, "exports/*.pptx")
	})

	t.Run("slide count matches", func(t *testing.T) {
		projectPath := templateFillContractProject(t)
		mustWriteTemplateFillPlan(t, projectPath, "confirmed", 2)
		mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 1)
		_, err := validateTemplateFillApplyContract(projectPath)
		requireTemplateFillContractError(t, err, "has 1 slides, expected 2")
	})
}

func TestValidateTemplateFillApplyContractRejectsSVGFiles(t *testing.T) {
	for _, relativePath := range []string{
		filepath.Join("svg_output", "01.svg"),
		filepath.Join("svg_final", "nested", "01.SVG"),
	} {
		t.Run(filepath.ToSlash(relativePath), func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
			mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 1)
			mustWriteFileNoTest(projectPath, relativePath, "<svg/>\n")
			_, err := validateTemplateFillApplyContract(projectPath)
			requireTemplateFillContractError(t, err, filepath.Base(relativePath))
		})
	}
}

func TestValidateTemplateFillValidateContractWritesContract(t *testing.T) {
	projectPath := templateFillContractProject(t)
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "readback.md"), "## Slide 1\n")
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "validate_report.json"), `{
  "schema": "template_fill_pptx_validate.v1",
  "summary": {"ok": 2, "warn": 1, "error": 0},
  "results": []
}`+"\n")

	contract, err := validateTemplateFillValidateContract(projectPath)
	if err != nil {
		t.Fatalf("validateTemplateFillValidateContract() error = %v", err)
	}
	canonicalProjectPath := canonicalTemplateFillContractPath(t, projectPath)
	if contract["phase"] != string(PhaseTemplateFillValidate) {
		t.Fatalf("phase = %#v, want %q", contract["phase"], PhaseTemplateFillValidate)
	}
	if contract["validate_report"] != filepath.Join(canonicalProjectPath, "validation", "validate_report.json") {
		t.Fatalf("validate_report = %#v", contract["validate_report"])
	}
	if contract["readback"] != filepath.Join(canonicalProjectPath, "validation", "readback.md") {
		t.Fatalf("readback = %#v", contract["readback"])
	}
	if got := templateFillContractSummaryCount(t, contract, "error"); got != 0 {
		t.Fatalf("summary.error = %d, want 0", got)
	}
	requireTemplateFillCheckedAt(t, contract)
	requireFileExists(t, filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_validate.json"))
}

func TestValidateTemplateFillValidateContractReportsMissingAndCorruptJSON(t *testing.T) {
	validReport := `{
  "schema": "template_fill_pptx_validate.v1",
  "summary": {"ok": 1, "warn": 0, "error": 0},
  "results": []
}`
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "missing", want: "read template fill validate report"},
		{name: "corrupt", content: "{\n", want: "parse template fill validate report"},
		{name: "trailing JSON", content: validReport + "\n{}\n", want: "multiple JSON values"},
		{name: "non-object JSON", content: "[]\n", want: "JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			mustWriteFileNoTest(projectPath, filepath.Join("validation", "readback.md"), "## Slide 1\n")
			if test.content != "" {
				mustWriteFileNoTest(projectPath, filepath.Join("validation", "validate_report.json"), test.content)
			}
			_, err := validateTemplateFillValidateContract(projectPath)
			requireTemplateFillContractError(t, err, test.want)
		})
	}
}

func TestValidateTemplateFillValidateContractRequiresValidReportReadbackAndNoErrors(t *testing.T) {
	tests := []struct {
		name      string
		report    any
		readback  *string
		wantError string
	}{
		{
			name:      "schema",
			report:    map[string]any{"schema": "template_fill_pptx_validate.v0", "summary": map[string]any{"ok": 1, "warn": 0, "error": 0}},
			readback:  templateFillContractString("## Slide 1\n"),
			wantError: "schema",
		},
		{
			name:      "summary",
			report:    map[string]any{"schema": "template_fill_pptx_validate.v1", "summary": "ok"},
			readback:  templateFillContractString("## Slide 1\n"),
			wantError: "summary",
		},
		{
			name:      "error missing",
			report:    map[string]any{"schema": "template_fill_pptx_validate.v1", "summary": map[string]any{"ok": 1, "warn": 0}},
			readback:  templateFillContractString("## Slide 1\n"),
			wantError: "summary.error",
		},
		{
			name:      "errors block",
			report:    map[string]any{"schema": "template_fill_pptx_validate.v1", "summary": map[string]any{"ok": 0, "warn": 0, "error": 2}},
			readback:  templateFillContractString("## Slide 1\n"),
			wantError: "summary.error = 2",
		},
		{
			name:      "readback missing",
			report:    map[string]any{"schema": "template_fill_pptx_validate.v1", "summary": map[string]any{"ok": 1, "warn": 0, "error": 0}},
			wantError: "required file not found",
		},
		{
			name:      "readback empty",
			report:    map[string]any{"schema": "template_fill_pptx_validate.v1", "summary": map[string]any{"ok": 1, "warn": 0, "error": 0}},
			readback:  templateFillContractString(""),
			wantError: "required file is empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("validation", "validate_report.json"), test.report)
			if test.readback != nil {
				mustWriteFileNoTest(projectPath, filepath.Join("validation", "readback.md"), *test.readback)
			}
			_, err := validateTemplateFillValidateContract(projectPath)
			requireTemplateFillContractError(t, err, test.wantError)
		})
	}
}

func TestValidateTemplateFillValidateContractRequiresStrictSummaryCounts(t *testing.T) {
	invalidValues := []struct {
		name   string
		mutate func(summary map[string]any, field string)
	}{
		{
			name: "missing",
			mutate: func(summary map[string]any, field string) {
				delete(summary, field)
			},
		},
		{
			name: "string",
			mutate: func(summary map[string]any, field string) {
				summary[field] = "0"
			},
		},
		{
			name: "fractional",
			mutate: func(summary map[string]any, field string) {
				summary[field] = 0.5
			},
		},
		{
			name: "negative",
			mutate: func(summary map[string]any, field string) {
				summary[field] = -1
			},
		},
	}
	for _, field := range []string{"ok", "warn", "error"} {
		for _, invalid := range invalidValues {
			t.Run(field+"/"+invalid.name, func(t *testing.T) {
				projectPath := templateFillContractProject(t)
				summary := map[string]any{"ok": 1, "warn": 0, "error": 0}
				invalid.mutate(summary, field)
				report := map[string]any{
					"schema":  "template_fill_pptx_validate.v1",
					"summary": summary,
					"results": []any{},
				}
				mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("validation", "validate_report.json"), report)
				mustWriteFileNoTest(projectPath, filepath.Join("validation", "readback.md"), "## Slide 1\n")

				_, err := validateTemplateFillValidateContract(projectPath)
				requireTemplateFillContractError(t, err, "summary."+field)
				requireTemplateFillPathNotExists(t, filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_validate.json"))
			})
		}
	}
}

func TestValidateTemplateFillCheckAndValidateContractsRejectMainRouteOutputs(t *testing.T) {
	tests := []struct {
		name         string
		relativePath string
		prepare      func(t *testing.T, projectPath string)
		validate     func(projectPath string) error
	}{
		{
			name:         "check design spec",
			relativePath: "design_spec.md",
			prepare:      prepareTemplateFillCheckContractReport,
			validate: func(projectPath string) error {
				_, err := validateTemplateFillCheckContract(projectPath, false)
				return err
			},
		},
		{
			name:         "check SVG",
			relativePath: filepath.Join("svg_output", "01.svg"),
			prepare:      prepareTemplateFillCheckContractReport,
			validate: func(projectPath string) error {
				_, err := validateTemplateFillCheckContract(projectPath, false)
				return err
			},
		},
		{
			name:         "validate spec lock",
			relativePath: "spec_lock.md",
			prepare:      prepareTemplateFillValidateContractReport,
			validate: func(projectPath string) error {
				_, err := validateTemplateFillValidateContract(projectPath)
				return err
			},
		},
		{
			name:         "validate SVG",
			relativePath: filepath.Join("svg_final", "01.svg"),
			prepare:      prepareTemplateFillValidateContractReport,
			validate: func(projectPath string) error {
				_, err := validateTemplateFillValidateContract(projectPath)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			test.prepare(t, projectPath)
			mustWriteFileNoTest(projectPath, test.relativePath, "generated by wrong workflow\n")
			err := test.validate(projectPath)
			requireTemplateFillContractError(t, err, filepath.Base(test.relativePath))
		})
	}
}

func TestValidateTemplateFillContractsRejectUnsafeReportPaths(t *testing.T) {
	for _, contractCase := range templateFillContractValidationCases() {
		t.Run(contractCase.name+"/contracts symlink", func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			contractCase.prepare(t, projectPath)
			outsidePath := t.TempDir()
			if err := os.MkdirAll(filepath.Join(projectPath, ".slidesmith"), 0o755); err != nil {
				t.Fatalf("mkdir .slidesmith: %v", err)
			}
			if err := os.Symlink(outsidePath, filepath.Join(projectPath, ".slidesmith", "contracts")); err != nil {
				t.Fatalf("symlink contracts directory: %v", err)
			}

			err := contractCase.validate(projectPath)
			requireTemplateFillContractError(t, err, "contract report")
			requireTemplateFillPathNotExists(t, filepath.Join(outsidePath, string(contractCase.phase)+".json"))
		})

		t.Run(contractCase.name+"/destination symlink", func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			contractCase.prepare(t, projectPath)
			contractsPath := filepath.Join(projectPath, ".slidesmith", "contracts")
			if err := os.MkdirAll(contractsPath, 0o755); err != nil {
				t.Fatalf("mkdir contracts: %v", err)
			}
			outsidePath := t.TempDir()
			outsideReport := filepath.Join(outsidePath, "outside.json")
			mustWriteFileNoTest(outsidePath, "outside.json", "sentinel\n")
			reportPath := filepath.Join(contractsPath, string(contractCase.phase)+".json")
			if err := os.Symlink(outsideReport, reportPath); err != nil {
				t.Fatalf("symlink contract report: %v", err)
			}

			err := contractCase.validate(projectPath)
			requireTemplateFillContractError(t, err, "contract report")
			raw, readErr := os.ReadFile(outsideReport)
			if readErr != nil {
				t.Fatalf("read outside report: %v", readErr)
			}
			if string(raw) != "sentinel\n" {
				t.Fatalf("outside report was overwritten through destination symlink: %q", raw)
			}
		})
	}
}

func TestValidateTemplateFillContractsDoNotWriteSuccessReportOnValidationFailure(t *testing.T) {
	tests := []struct {
		name     string
		phase    PipelinePhase
		arrange  func(t *testing.T, projectPath string)
		validate func(projectPath string) error
	}{
		{
			name:  "plan",
			phase: PhaseTemplateFillPlan,
			arrange: func(t *testing.T, projectPath string) {
				plan := templateFillContractPlan("draft", 1)
				plan["schema"] = "template_fill_pptx_plan.v0"
				mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "fill_plan.json"), plan)
			},
			validate: func(projectPath string) error {
				_, err := validateTemplateFillPlanContract(projectPath)
				return err
			},
		},
		{
			name:  "check",
			phase: PhaseTemplateFillCheck,
			arrange: func(t *testing.T, projectPath string) {
				mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
				report := map[string]any{
					"schema":  "template_fill_pptx_check.v1",
					"summary": map[string]any{"ok": 1, "warn": "0", "error": 0},
					"results": []any{},
				}
				mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "check_report.json"), report)
			},
			validate: func(projectPath string) error {
				_, err := validateTemplateFillCheckContract(projectPath, false)
				return err
			},
		},
		{
			name:  "apply",
			phase: PhaseTemplateFillApply,
			arrange: func(t *testing.T, projectPath string) {
				mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
				mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 1)
			},
			validate: func(projectPath string) error {
				_, err := validateTemplateFillApplyContract(projectPath)
				return err
			},
		},
		{
			name:  "validate",
			phase: PhaseTemplateFillValidate,
			arrange: func(t *testing.T, projectPath string) {
				report := map[string]any{
					"schema":  "template_fill_pptx_validate.v1",
					"summary": map[string]any{"ok": 0, "warn": 0, "error": 1},
					"results": []any{},
				}
				mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("validation", "validate_report.json"), report)
				mustWriteFileNoTest(projectPath, filepath.Join("validation", "readback.md"), "## Slide 1\n")
			},
			validate: func(projectPath string) error {
				_, err := validateTemplateFillValidateContract(projectPath)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := templateFillContractProject(t)
			test.arrange(t, projectPath)
			if err := test.validate(projectPath); err == nil {
				t.Fatalf("validation error = nil")
			}
			requireTemplateFillPathNotExists(t, filepath.Join(projectPath, ".slidesmith", "contracts", string(test.phase)+".json"))
		})
	}
}

type templateFillContractValidationCase struct {
	name     string
	phase    PipelinePhase
	prepare  func(t *testing.T, projectPath string)
	validate func(projectPath string) error
}

func templateFillContractValidationCases() []templateFillContractValidationCase {
	return []templateFillContractValidationCase{
		{
			name:  "plan",
			phase: PhaseTemplateFillPlan,
			prepare: func(t *testing.T, projectPath string) {
				mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
			},
			validate: func(projectPath string) error {
				_, err := validateTemplateFillPlanContract(projectPath)
				return err
			},
		},
		{
			name:    "check",
			phase:   PhaseTemplateFillCheck,
			prepare: prepareTemplateFillCheckContractReport,
			validate: func(projectPath string) error {
				_, err := validateTemplateFillCheckContract(projectPath, false)
				return err
			},
		},
		{
			name:  "apply",
			phase: PhaseTemplateFillApply,
			prepare: func(t *testing.T, projectPath string) {
				mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
				mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 1)
			},
			validate: func(projectPath string) error {
				_, err := validateTemplateFillApplyContract(projectPath)
				return err
			},
		},
		{
			name:    "validate",
			phase:   PhaseTemplateFillValidate,
			prepare: prepareTemplateFillValidateContractReport,
			validate: func(projectPath string) error {
				_, err := validateTemplateFillValidateContract(projectPath)
				return err
			},
		},
	}
}

func prepareTemplateFillCheckContractReport(t *testing.T, projectPath string) {
	t.Helper()
	fillPlanPath := filepath.Join(projectPath, "analysis", "fill_plan.json")
	if _, err := os.Lstat(fillPlanPath); os.IsNotExist(err) {
		mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	} else if err != nil {
		t.Fatalf("inspect template fill plan: %v", err)
	}
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "check_report.json"), `{
  "schema": "template_fill_pptx_check.v1",
  "summary": {"ok": 1, "warn": 0, "error": 0},
  "results": []
}`+"\n")
}

func prepareTemplateFillValidateContractReport(t *testing.T, projectPath string) {
	t.Helper()
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "readback.md"), "## Slide 1\n")
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "validate_report.json"), `{
  "schema": "template_fill_pptx_validate.v1",
  "summary": {"ok": 1, "warn": 0, "error": 0},
  "results": []
}`+"\n")
}

func templateFillContractProject(t *testing.T) string {
	t.Helper()
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), `{
  "slides": [
    {"slide_index": 1},
    {"slide_index": 2}
  ]
}`+"\n")
	return projectPath
}

func mustWriteTemplateFillPlan(t *testing.T, projectPath, status string, slideCount int) {
	t.Helper()
	mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "fill_plan.json"), templateFillContractPlan(status, slideCount))
}

func templateFillContractPlan(status string, slideCount int) map[string]any {
	slides := make([]any, 0, slideCount)
	for index := 1; index <= slideCount; index++ {
		slides = append(slides, map[string]any{
			"source_slide": index,
			"purpose":      fmt.Sprintf("slide-%d", index),
			"layout_rationale": map[string]any{
				"layout_pattern": "content",
				"why_fit":        "matches the target message",
				"risk":           "keep copy concise",
			},
			"replacements": []any{},
			"table_edits":  []any{},
			"chart_edits":  []any{},
		})
	}
	return map[string]any{
		"schema":            "template_fill_pptx_plan.v1",
		"status":            status,
		"source_pptx":       "sources/brand.pptx",
		"accepted_warnings": []any{},
		"slides":            slides,
	}
}

func templateFillContractFirstSlide(plan map[string]any) map[string]any {
	return plan["slides"].([]any)[0].(map[string]any)
}

func templateFillContractFirstLayout(plan map[string]any) map[string]any {
	return templateFillContractFirstSlide(plan)["layout_rationale"].(map[string]any)
}

func mustWriteTemplateFillContractJSON(t *testing.T, root, relativePath string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", relativePath, err)
	}
	mustWriteFileNoTest(root, relativePath, string(raw)+"\n")
}

func canonicalTemplateFillContractPath(t *testing.T, path string) string {
	t.Helper()
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs %s: %v", path, err)
	}
	canonicalPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return filepath.Clean(canonicalPath)
}

func requireFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", path)
	}
}

func requireTemplateFillPathNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			t.Fatalf("lstat %s: %v", path, err)
		}
		t.Fatalf("path exists unexpectedly: %s", path)
	}
}

func readTemplateFillContractReport(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read contract %s: %v", path, err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("parse contract %s: %v", path, err)
	}
	return report
}

func requireTemplateFillCheckedAt(t *testing.T, contract map[string]any) {
	t.Helper()
	checkedAt, ok := contract["checked_at"].(string)
	if !ok || checkedAt == "" {
		t.Fatalf("checked_at = %#v, want timestamp string", contract["checked_at"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, checkedAt)
	if err != nil {
		t.Fatalf("checked_at = %q, want RFC3339Nano: %v", checkedAt, err)
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("checked_at = %q, want UTC", checkedAt)
	}
}

func requireTemplateFillContractError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want containing %q", err, want)
	}
}

func templateFillContractSummaryCount(t *testing.T, contract map[string]any, field string) int {
	t.Helper()
	summary, ok := contract["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary = %#v, want map[string]any", contract["summary"])
	}
	switch value := summary[field].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			t.Fatalf("summary.%s = %q: %v", field, value, err)
		}
		return int(parsed)
	default:
		t.Fatalf("summary.%s = %#v, want number", field, summary[field])
		return 0
	}
}

func templateFillContractString(value string) *string {
	return &value
}
