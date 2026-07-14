package service

import (
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestSVGInspectorFailureMapsStructuredCodesToSafeSubphases(t *testing.T) {
	tests := map[string]string{
		"xml_invalid":            "svg_execute.xml",
		"canvas_mismatch":        "svg_execute.canvas",
		"page_mapping_invalid":   "svg_execute.page_mapping",
		"resource_usage_invalid": "svg_execute.resource_usage",
		"chart_usage_invalid":    "svg_execute.chart_usage",
		"notes_invalid":          "svg_execute.notes",
		"external_uri":           "svg_execute.security",
		"unknown_code":           "svg_execute.bundle",
	}
	for code, want := range tests {
		run := &model.TaskRuntimeRun{StderrTail: "runtime prefix\n" + `{"schema":"slidesmith.svg_bundle_inspection_error.v1","code":"` + code + `","message":"safe"}`}
		phase, gotCode := svgInspectorFailure(run)
		if phase != want || gotCode != code {
			t.Fatalf("svgInspectorFailure(%s) = %q, %q; want %q, %q", code, phase, gotCode, want, code)
		}
	}
	phase, code := svgInspectorFailure(&model.TaskRuntimeRun{StderrTail: "not json"})
	if phase != "svg_execute.bundle" || code != "" {
		t.Fatalf("unstructured failure = %q, %q", phase, code)
	}
}
