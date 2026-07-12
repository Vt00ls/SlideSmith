package service

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func (s *TaskService) templateFillPlanPrompt(task *model.Task, inputs TemplateFillInputs) string {
	projectRel := s.projectRel(task, inputs.ProjectPath)
	workspacePath := func(hostPath string) string {
		relativePath, err := filepath.Rel(inputs.ProjectPath, hostPath)
		if err != nil || relativePath == "." || filepath.IsAbs(relativePath) || strings.HasPrefix(relativePath, "..") {
			return projectRel
		}
		return filepath.ToSlash(filepath.Join(projectRel, relativePath))
	}
	contentSources := make([]string, 0, len(inputs.ContentSources))
	for _, contentSource := range inputs.ContentSources {
		contentSources = append(contentSources, "- "+workspacePath(contentSource))
	}

	return fmt.Sprintf(`You are building analysis/fill_plan.json for the Template Fill PPTX workflow.

Task:
- SlideSmith task ID: %s
- Project directory: %s
- Source PPTX: %s
- Slide library: %s
- Content sources:
%s
- Output fill plan: %s

Hard rules:
- Do not run pptx_to_svg.py, pptx_template_import.py, finalize_svg.py, or svg_to_pptx.py.
- Read the slide library JSON before selecting pages.
- Use the target story order, not the source deck order.
- Reuse, reorder, or omit source slides when layout fit requires it.
- Every planned slide must include layout_rationale.layout_pattern, why_fit, and risk.
- All factual content must come from the provided content source files.
- Write only analysis/fill_plan.json.
- Keep top-level status as "draft".
- Do not create PPTX exports.

Write the plan to %s and stop after verifying that it is valid JSON. Do not write a check report; the service runs the checker separately.
`, task.ID, projectRel, workspacePath(inputs.SourcePPTX), workspacePath(inputs.SlideLibrary), strings.Join(contentSources, "\n"), workspacePath(inputs.FillPlan), workspacePath(inputs.FillPlan))
}
