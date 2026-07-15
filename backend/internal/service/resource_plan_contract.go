package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const resourcePlanSchema = "slidesmith.resource_plan.v1"

var resourceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,95}$`)

type resourcePlan struct {
	Schema             string                `json:"schema"`
	TaskID             string                `json:"task_id"`
	PageCount          int                   `json:"page_count"`
	SpecSHA256         string                `json:"spec_sha256"`
	SpecLockSHA256     string                `json:"spec_lock_sha256"`
	ConfirmationSHA256 string                `json:"confirmation_sha256"`
	Requirements       []resourceRequirement `json:"requirements"`
}

type resourceRequirement struct {
	ID              string `json:"id"`
	Page            int    `json:"page"`
	Type            string `json:"type"`
	Purpose         string `json:"purpose"`
	Required        bool   `json:"required"`
	AcquireVia      string `json:"acquire_via"`
	Fallback        string `json:"fallback"`
	OutputName      string `json:"output_name"`
	Placement       any    `json:"placement,omitempty"`
	PromptOrQuery   string `json:"prompt_or_query,omitempty"`
	SourceReference string `json:"source_reference,omitempty"`
	ParentID        string `json:"parent_id,omitempty"`
	Expression      string `json:"expression,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Data            any    `json:"data,omitempty"`
	Citation        any    `json:"citation,omitempty"`
	Parameters      any    `json:"parameters,omitempty"`
	Publishable     *bool  `json:"publishable,omitempty"`
}

func bindGeneratedResourcePlanHashes(projectPath, expectedTaskID string) error {
	planPath := filepath.Join(projectPath, ".slidesmith", "resource_plan.json")
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read resource plan for hash binding: %w", err)
	}
	var envelope struct {
		Schema    string `json:"schema"`
		TaskID    string `json:"task_id"`
		PageCount int    `json:"page_count"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode resource plan for hash binding: %w", err)
	}
	if envelope.Schema != resourcePlanSchema {
		return fmt.Errorf("resource plan schema = %q, expected %q", envelope.Schema, resourcePlanSchema)
	}
	if expectedTaskID == "" || envelope.TaskID != expectedTaskID {
		return fmt.Errorf("resource plan task_id = %q, expected %q", envelope.TaskID, expectedTaskID)
	}
	expectedPages := confirmedPageCount(projectPath)
	if envelope.PageCount != expectedPages {
		return fmt.Errorf("resource plan page_count = %d, expected %d", envelope.PageCount, expectedPages)
	}

	bindings := map[string]string{
		"spec_sha256":         filepath.Join(projectPath, "design_spec.md"),
		"spec_lock_sha256":    filepath.Join(projectPath, "spec_lock.md"),
		"confirmation_sha256": filepath.Join(projectPath, "confirm_ui", "result.json"),
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decode resource plan object for hash binding: %w", err)
	}
	for field, path := range bindings {
		sha, err := sha256File(path)
		if err != nil {
			return fmt.Errorf("hash resource plan binding %s: %w", field, err)
		}
		value[field] = sha
	}
	if err := writeJSONPretty(planPath, value); err != nil {
		return fmt.Errorf("write resource plan hash bindings: %w", err)
	}
	return nil
}

var allowedResourceTypes = map[string]bool{
	"image": true, "illustration_sheet": true, "illustration_slice": true,
	"icon": true, "formula": true, "chart_template": true, "chart_data": true,
	"template_asset": true, "placeholder": true,
}

var allowedAcquireVia = map[string]bool{
	"user": true, "template": true, "icon": true, "formula": true,
	"chart_template": true, "source": true, "web": true, "ai": true,
	"slice": true, "placeholder": true,
}

var allowedResourceFallback = map[string]bool{
	"": true, "diagram": true, "shape": true, "text": true,
	"placeholder": true, "omit_optional": true,
}

func validateResourcePlanContract(projectPath, expectedTaskID string) (*resourcePlan, map[string]any, error) {
	planPath := filepath.Join(projectPath, ".slidesmith", "resource_plan.json")
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read resource plan: %w", err)
	}
	var plan resourcePlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, nil, fmt.Errorf("decode resource plan: %w", err)
	}
	if plan.Schema != resourcePlanSchema {
		return nil, nil, fmt.Errorf("resource plan schema = %q, expected %q", plan.Schema, resourcePlanSchema)
	}
	if expectedTaskID != "" && plan.TaskID != expectedTaskID {
		return nil, nil, fmt.Errorf("resource plan task_id = %q, expected %q", plan.TaskID, expectedTaskID)
	}
	if plan.TaskID == "" {
		return nil, nil, fmt.Errorf("resource plan task_id is empty")
	}
	expectedPages := confirmedPageCount(projectPath)
	if plan.PageCount != expectedPages {
		return nil, nil, fmt.Errorf("resource plan page_count = %d, expected %d", plan.PageCount, expectedPages)
	}
	designPath := filepath.Join(projectPath, "design_spec.md")
	lockPath := filepath.Join(projectPath, "spec_lock.md")
	confirmationPath := filepath.Join(projectPath, "confirm_ui", "result.json")
	designSHA, err := sha256File(designPath)
	if err != nil {
		return nil, nil, err
	}
	lockSHA, err := sha256File(lockPath)
	if err != nil {
		return nil, nil, err
	}
	confirmationSHA, err := sha256File(confirmationPath)
	if err != nil {
		return nil, nil, err
	}
	if plan.SpecSHA256 != designSHA || plan.SpecLockSHA256 != lockSHA || plan.ConfirmationSHA256 != confirmationSHA {
		return nil, nil, fmt.Errorf("resource plan hash binding mismatch")
	}
	designRaw, err := os.ReadFile(designPath)
	if err != nil {
		return nil, nil, err
	}
	lockRaw, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, nil, err
	}
	confirmation := readJSONMap(confirmationPath)
	allowedSources := confirmationImageSources(confirmation)
	iconLibrary := strings.ToLower(strings.TrimSpace(valueString(confirmation, "icons", "none")))
	formulaPolicy := strings.ToLower(strings.TrimSpace(valueString(confirmation, "formula_policy", "none")))
	seen := make(map[string]bool, len(plan.Requirements))
	seenOutputNames := make(map[string]string, len(plan.Requirements))
	parentCandidates := make(map[string]resourceRequirement, len(plan.Requirements))
	for _, item := range plan.Requirements {
		if resourceIDPattern.MatchString(item.ID) {
			parentCandidates[item.ID] = item
		}
	}
	for index := range plan.Requirements {
		item := &plan.Requirements[index]
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		item.AcquireVia = strings.ToLower(strings.TrimSpace(item.AcquireVia))
		item.Fallback = strings.ToLower(strings.TrimSpace(item.Fallback))
		if !resourceIDPattern.MatchString(item.ID) {
			return nil, nil, fmt.Errorf("resource plan item %d has invalid id %q", index, item.ID)
		}
		if seen[item.ID] {
			return nil, nil, fmt.Errorf("resource plan has duplicate id %q", item.ID)
		}
		seen[item.ID] = true
		if item.Page < 1 || item.Page > plan.PageCount {
			return nil, nil, fmt.Errorf("resource %s page %d is outside 1..%d", item.ID, item.Page, plan.PageCount)
		}
		if strings.TrimSpace(item.Purpose) == "" {
			return nil, nil, fmt.Errorf("resource %s purpose is empty", item.ID)
		}
		if !allowedResourceTypes[item.Type] {
			return nil, nil, fmt.Errorf("resource %s has unsupported type %q in spec_generate; read locked layout shells from template_lock/template_resolution, or use type %q with acquire_via %q for a concrete template file", item.ID, item.Type, "template_asset", "template")
		}
		if !allowedAcquireVia[item.AcquireVia] {
			return nil, nil, fmt.Errorf("resource %s has unsupported acquire_via %q", item.ID, item.AcquireVia)
		}
		if !allowedResourceFallback[item.Fallback] {
			return nil, nil, fmt.Errorf("resource %s has unsupported fallback %q", item.ID, item.Fallback)
		}
		if item.Required && item.Fallback == "omit_optional" {
			return nil, nil, fmt.Errorf("required resource %s cannot use omit_optional fallback", item.ID)
		}
		if !resourceAcquireMatchesType(item.Type, item.AcquireVia) {
			return nil, nil, fmt.Errorf("resource %s type %q cannot use acquire_via %q", item.ID, item.Type, item.AcquireVia)
		}
		if !confirmationAllowsAcquireVia(allowedSources, item.AcquireVia) {
			return nil, nil, fmt.Errorf("resource %s acquire_via %q is not allowed by confirmation", item.ID, item.AcquireVia)
		}
		if item.OutputName != "" && !isSafeResourceBasename(item.OutputName) {
			return nil, nil, fmt.Errorf("resource %s output_name %q is not a safe basename", item.ID, item.OutputName)
		}
		if item.OutputName != "" {
			outputKey := strings.ToLower(item.OutputName)
			if previousID := seenOutputNames[outputKey]; previousID != "" {
				return nil, nil, fmt.Errorf("resources %s and %s share output_name %q", previousID, item.ID, item.OutputName)
			}
			seenOutputNames[outputKey] = item.ID
		}
		if item.AcquireVia == "slice" {
			parent, ok := parentCandidates[item.ParentID]
			if item.ParentID == "" || !ok || item.ParentID == item.ID || strings.ToLower(strings.TrimSpace(parent.Type)) != "illustration_sheet" {
				return nil, nil, fmt.Errorf("resource %s has invalid slice parent %q", item.ID, item.ParentID)
			}
		}
		if item.Type == "formula" && strings.TrimSpace(item.Expression) == "" && strings.TrimSpace(item.PromptOrQuery) == "" {
			return nil, nil, fmt.Errorf("formula resource %s has no expression", item.ID)
		}
		if item.Type == "formula" && formulaPolicy != "mixed" && formulaPolicy != "render-all" {
			return nil, nil, fmt.Errorf("formula resource %s is forbidden by confirmation formula_policy %q", item.ID, formulaPolicy)
		}
		if item.Type == "icon" {
			iconName := strings.ToLower(strings.TrimSpace(item.SourceReference))
			if iconName == "" {
				iconName = strings.ToLower(strings.TrimSpace(item.PromptOrQuery))
			}
			if iconLibrary == "" || iconLibrary == "none" || !strings.HasPrefix(iconName, iconLibrary+"/") {
				return nil, nil, fmt.Errorf("icon resource %s does not match confirmed icon library %q", item.ID, iconLibrary)
			}
		}
		if item.Type == "chart_data" && !strings.HasPrefix(filepath.ToSlash(strings.TrimSpace(item.SourceReference)), "sources/") {
			return nil, nil, fmt.Errorf("chart_data resource %s must reference project sources", item.ID)
		}
		if item.Type == "chart_data" && !resourceCitationPresent(item.Citation) {
			return nil, nil, fmt.Errorf("chart_data resource %s has no source citation", item.ID)
		}
		if !markdownResourceDeclarationMatches(string(designRaw), *item) || !markdownResourceDeclarationMatches(string(lockRaw), *item) {
			return nil, nil, fmt.Errorf("resource %s is not consistently declared in design_spec.md and spec_lock.md", item.ID)
		}
	}
	planSHA := sha256.Sum256(raw)
	ids := make([]string, 0, len(plan.Requirements))
	for _, item := range plan.Requirements {
		ids = append(ids, item.ID)
	}
	contract := map[string]any{
		"schema":                plan.Schema,
		"resource_plan":         planPath,
		"resource_plan_sha256":  hex.EncodeToString(planSHA[:]),
		"resource_count":        len(plan.Requirements),
		"resource_ids":          ids,
		"confirmation_sha256":   confirmationSHA,
		"design_spec_sha256":    designSHA,
		"spec_lock_sha256":      lockSHA,
		"resource_plan_task_id": plan.TaskID,
	}
	return &plan, contract, nil
}

func resourceCitationPresent(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]any:
		return len(typed) > 0
	default:
		return false
	}
}

func markdownResourceDeclarationMatches(markdown string, item resourceRequirement) bool {
	lines := strings.Split(markdown, "\n")
	for index, line := range lines {
		fields := strings.FieldsFunc(line, func(r rune) bool {
			return r == '|' || r == ',' || r == ':' || r == ';' || r == '=' || r == ' ' || r == '\t' || r == '`'
		})
		containsID := false
		for _, field := range fields {
			if field == item.ID {
				containsID = true
				break
			}
		}
		if !containsID {
			continue
		}
		start := index - 5
		if start < 0 {
			start = 0
		}
		end := index + 6
		if end > len(lines) {
			end = len(lines)
		}
		window := strings.ToLower(strings.Join(lines[start:end], "\n"))
		if markdownPageReferenceMatches(window, item.Page) && strings.Contains(window, strings.ToLower(strings.TrimSpace(item.Purpose))) {
			return true
		}
	}
	return false
}

func markdownPageReferenceMatches(markdown string, page int) bool {
	pageText := regexp.QuoteMeta(strconv.Itoa(page))
	patterns := []string{
		`(^|[^0-9])` + pageText + `([^0-9]|$)`,
		`(^|[^a-z0-9])p0*` + pageText + `([^0-9]|$)`,
		`(^|[^a-z0-9])(page|slide)[ _:#-]*0*` + pageText + `([^0-9]|$)`,
	}
	for _, pattern := range patterns {
		if regexp.MustCompile(pattern).MatchString(markdown) {
			return true
		}
	}
	return false
}

func resourceAcquireMatchesType(resourceType, acquireVia string) bool {
	switch resourceType {
	case "icon":
		return acquireVia == "icon"
	case "formula":
		return acquireVia == "formula"
	case "chart_template":
		return acquireVia == "chart_template"
	case "chart_data":
		return acquireVia == "source"
	case "template_asset":
		return acquireVia == "template"
	case "illustration_slice":
		return acquireVia == "slice"
	case "placeholder":
		return acquireVia == "placeholder"
	case "illustration_sheet":
		return acquireVia == "ai"
	case "image":
		return map[string]bool{"user": true, "source": true, "template": true, "web": true, "ai": true, "placeholder": true}[acquireVia]
	default:
		return false
	}
}

func isSafeResourceBasename(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`) && !strings.ContainsRune(value, 0)
}

func confirmationImageSources(confirmation map[string]any) map[string]bool {
	out := map[string]bool{}
	var add func(any)
	add = func(value any) {
		switch typed := value.(type) {
		case string:
			for _, part := range strings.FieldsFunc(strings.ToLower(typed), func(r rune) bool {
				return r == ',' || r == ';' || r == '|' || r == '/' || r == ' ' || r == '[' || r == ']' || r == '"'
			}) {
				if part != "" {
					out[part] = true
				}
			}
		case []any:
			for _, item := range typed {
				add(item)
			}
		case []string:
			for _, item := range typed {
				add(item)
			}
		}
	}
	add(confirmation["image_usage"])
	return out
}

func confirmationAllowsAcquireVia(allowed map[string]bool, acquireVia string) bool {
	switch acquireVia {
	case "web":
		return allowed["web"]
	case "ai":
		return allowed["ai"]
	case "user":
		return allowed["provided"] || allowed["user"] || allowed["source"]
	case "source":
		return true
	default:
		return true
	}
}
