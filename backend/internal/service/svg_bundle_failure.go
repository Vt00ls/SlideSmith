package service

import (
	"encoding/json"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func svgInspectorFailure(run *model.TaskRuntimeRun) (string, string) {
	code := svgInspectorErrorCode(run)
	suffix := "bundle"
	switch code {
	case "xml_invalid", "root_invalid":
		suffix = "xml"
	case "canvas_invalid", "canvas_unknown", "canvas_mismatch":
		suffix = "canvas"
	case "filename_invalid", "filename_collision", "page_count_invalid", "page_count_mismatch", "page_sequence_invalid", "page_mapping_invalid":
		suffix = "page_mapping"
	case "resource_usage_invalid", "manifest_invalid", "sidecar_invalid":
		suffix = "resource_usage"
	case "chart_usage_invalid":
		suffix = "chart_usage"
	case "notes_invalid":
		suffix = "notes"
	case "doctype_forbidden", "element_forbidden", "event_handler_forbidden", "external_uri", "path_escape", "path_invalid", "symlink_forbidden", "element_id_invalid", "element_id_duplicate":
		suffix = "security"
	}
	return string(PhaseSVGExecute) + "." + suffix, code
}

func svgInspectorErrorCode(run *model.TaskRuntimeRun) string {
	if run == nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(run.StderrTail), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if start := strings.IndexByte(line, '{'); start >= 0 {
			line = line[start:]
		}
		var failure struct {
			Schema string `json:"schema"`
			Code   string `json:"code"`
		}
		if json.Unmarshal([]byte(line), &failure) == nil && failure.Schema == "slidesmith.svg_bundle_inspection_error.v1" {
			return strings.TrimSpace(failure.Code)
		}
	}
	return ""
}
