package service

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const resourcesManifestSchema = "slidesmith.resources_manifest.v1"

type resourcesManifest struct {
	Schema             string                  `json:"schema"`
	TaskID             string                  `json:"task_id"`
	Route              string                  `json:"route"`
	RunnerProfile      string                  `json:"runner_profile"`
	ProjectPath        string                  `json:"project_path"`
	ResourcePlanSHA256 string                  `json:"resource_plan_sha256"`
	PolicySHA256       string                  `json:"policy_sha256"`
	SpecSHA256         string                  `json:"spec_sha256"`
	SpecLockSHA256     string                  `json:"spec_lock_sha256"`
	PhaseRunID         string                  `json:"phase_run_id"`
	Resources          []resourceManifestItem  `json:"resources"`
	Summary            resourceManifestSummary `json:"summary"`
	CreatedAt          string                  `json:"created_at"`
	CompletedAt        string                  `json:"completed_at"`
}

type resourceManifestItem struct {
	ID          string                   `json:"id"`
	Page        int                      `json:"page"`
	Type        string                   `json:"type"`
	Purpose     string                   `json:"purpose"`
	Required    bool                     `json:"required"`
	AcquireVia  string                   `json:"acquire_via"`
	Provider    string                   `json:"provider"`
	Status      string                   `json:"status"`
	Attempt     int                      `json:"attempt"`
	Input       map[string]any           `json:"input"`
	Output      *resourceManifestOutput  `json:"output"`
	Provenance  map[string]any           `json:"provenance"`
	Fallback    resourceManifestFallback `json:"fallback"`
	Publishable bool                     `json:"publishable"`
	Error       map[string]any           `json:"error"`
	CacheKey    string                   `json:"cache_key"`
	CacheReused bool                     `json:"cache_reused,omitempty"`
}

type resourceManifestOutput struct {
	Path     string `json:"path"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	HasAlpha bool   `json:"has_alpha"`
}

type resourceManifestFallback struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

type resourceManifestSummary struct {
	Total          int   `json:"total"`
	Ready          int   `json:"ready"`
	Degraded       int   `json:"degraded"`
	Failed         int   `json:"failed"`
	Pending        int   `json:"pending"`
	RequiredFailed int   `json:"required_failed"`
	Bytes          int64 `json:"bytes"`
}

type TaskResourceView struct {
	TaskID       string                  `json:"task_id"`
	PhaseStatus  string                  `json:"phase_status"`
	Summary      resourceManifestSummary `json:"summary"`
	Resources    []TaskResourceItemView  `json:"resources"`
	ManifestHash string                  `json:"manifest_sha256"`
}

type TaskResourceItemView struct {
	ID          string                   `json:"id"`
	Page        int                      `json:"page"`
	Type        string                   `json:"type"`
	Purpose     string                   `json:"purpose"`
	Required    bool                     `json:"required"`
	AcquireVia  string                   `json:"acquire_via"`
	Provider    string                   `json:"provider"`
	Status      string                   `json:"status"`
	Fallback    resourceManifestFallback `json:"fallback"`
	Publishable bool                     `json:"publishable"`
	ArtifactID  string                   `json:"artifact_id,omitempty"`
	MimeType    string                   `json:"mime_type,omitempty"`
	Size        int64                    `json:"size,omitempty"`
	Width       int                      `json:"width,omitempty"`
	Height      int                      `json:"height,omitempty"`
	ErrorCode   string                   `json:"error_code,omitempty"`
	Error       string                   `json:"error,omitempty"`
}

func validateResourceManifestContract(projectPath string, task *model.Task, phaseRunID string) (map[string]any, error) {
	return validateResourceManifestContractWithBindings(projectPath, task, phaseRunID, "", "")
}

func validateResourceManifestContractWithBindings(projectPath string, task *model.Task, phaseRunID, expectedPlanSHA, expectedPolicySHA string) (map[string]any, error) {
	manifestPath := filepath.Join(projectPath, ".slidesmith", "resources_manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read resources manifest: %w", err)
	}
	var manifest resourcesManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode resources manifest: %w", err)
	}
	if manifest.Schema != resourcesManifestSchema {
		return nil, fmt.Errorf("resources manifest schema = %q", manifest.Schema)
	}
	if task == nil || manifest.TaskID != task.ID || manifest.Route != model.TaskRouteMain || manifest.RunnerProfile != model.RunnerProfileFullPPTMaster || manifest.RunnerProfile != task.RunnerProfile {
		return nil, fmt.Errorf("resources manifest task/route/profile binding mismatch")
	}
	if phaseRunID == "" || manifest.PhaseRunID != phaseRunID {
		return nil, fmt.Errorf("resources manifest phase_run_id = %q, expected %q", manifest.PhaseRunID, phaseRunID)
	}
	if manifest.ProjectPath != filepath.ToSlash(filepath.Join("projects", filepath.Base(projectPath))) {
		return nil, fmt.Errorf("resources manifest project_path binding mismatch")
	}
	plan, _, err := validateResourcePlanContract(projectPath, task.ID)
	if err != nil {
		return nil, err
	}
	planSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
	if err != nil {
		return nil, err
	}
	policy, err := loadResourcePolicy(projectPath)
	if err != nil {
		return nil, err
	}
	if expectedPlanSHA != "" && planSHA != expectedPlanSHA {
		return nil, fmt.Errorf("resource plan changed during resource phase")
	}
	if expectedPolicySHA != "" && policy.PolicySHA256 != expectedPolicySHA {
		return nil, fmt.Errorf("resource policy changed during resource phase")
	}
	if manifest.ResourcePlanSHA256 != planSHA || manifest.PolicySHA256 != policy.PolicySHA256 || manifest.SpecSHA256 != plan.SpecSHA256 || manifest.SpecLockSHA256 != plan.SpecLockSHA256 {
		return nil, fmt.Errorf("resources manifest upstream hash binding mismatch")
	}
	if policy.TaskID != task.ID || policy.PhaseRunID != phaseRunID || policy.RunnerProfile != task.RunnerProfile {
		return nil, fmt.Errorf("resource policy task/profile/phase binding mismatch")
	}
	requirements := make(map[string]resourceRequirement, len(plan.Requirements))
	for _, requirement := range plan.Requirements {
		requirements[requirement.ID] = requirement
	}
	seen := make(map[string]bool, len(manifest.Resources))
	seenOutputPaths := make(map[string]string, len(manifest.Resources))
	computed := resourceManifestSummary{Total: len(manifest.Resources)}
	files := 0
	for _, item := range manifest.Resources {
		requirement, ok := requirements[item.ID]
		if !ok {
			return nil, fmt.Errorf("resources manifest contains undeclared resource %q", item.ID)
		}
		if seen[item.ID] {
			return nil, fmt.Errorf("resources manifest contains duplicate resource %q", item.ID)
		}
		seen[item.ID] = true
		if item.Page != requirement.Page || item.Type != requirement.Type || item.Purpose != requirement.Purpose || item.AcquireVia != requirement.AcquireVia || item.Required != requirement.Required || item.Fallback.Type != requirement.Fallback {
			return nil, fmt.Errorf("resource %s does not match resource plan", item.ID)
		}
		if item.Attempt < 1 || item.CacheKey == "" {
			return nil, fmt.Errorf("resource %s lacks attempt/cache binding", item.ID)
		}
		switch item.Status {
		case "ready":
			computed.Ready++
			if item.Output == nil {
				return nil, fmt.Errorf("ready resource %s has no output", item.ID)
			}
			if len(item.Error) != 0 {
				return nil, fmt.Errorf("ready resource %s retains an error", item.ID)
			}
			if err := validateReadyResourcePolicy(item, policy); err != nil {
				return nil, err
			}
			if err := validateResourceOutput(projectPath, item.Output, policy.MaxSingleBytes); err != nil {
				return nil, fmt.Errorf("resource %s: %w", item.ID, err)
			}
			outputKey := strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(item.Output.Path))))
			if previousID := seenOutputPaths[outputKey]; previousID != "" {
				return nil, fmt.Errorf("resources %s and %s share output path %q", previousID, item.ID, item.Output.Path)
			}
			seenOutputPaths[outputKey] = item.ID
			files++
			computed.Bytes += item.Output.Size
		case "degraded":
			computed.Degraded++
			if !allowedResourceFallback[item.Fallback.Type] || item.Fallback.Type == "" || item.Fallback.Type == "omit_optional" || item.Fallback.Reason == "" {
				return nil, fmt.Errorf("degraded resource %s has invalid fallback", item.ID)
			}
			if requirement.Fallback == "" || item.Fallback.Type != requirement.Fallback {
				return nil, fmt.Errorf("degraded resource %s uses fallback %q not approved by resource plan", item.ID, item.Fallback.Type)
			}
			if item.Output != nil {
				return nil, fmt.Errorf("degraded resource %s must consume fallback, not output", item.ID)
			}
			if len(item.Error) != 0 {
				return nil, fmt.Errorf("degraded resource %s retains an error", item.ID)
			}
		case "skipped":
			if item.Required || item.Fallback.Type != "omit_optional" || requirement.Fallback != "omit_optional" || item.Fallback.Reason == "" {
				return nil, fmt.Errorf("resource %s cannot be skipped", item.ID)
			}
		case "failed":
			computed.Failed++
			if item.Required {
				computed.RequiredFailed++
			}
			return nil, fmt.Errorf("resource %s remains failed", item.ID)
		case "planned", "pending", "running", "stale":
			computed.Pending++
			return nil, fmt.Errorf("resource %s remains non-terminal (%s)", item.ID, item.Status)
		default:
			return nil, fmt.Errorf("resource %s has invalid status %q", item.ID, item.Status)
		}
	}
	if len(seen) != len(requirements) {
		return nil, fmt.Errorf("resources manifest has %d items, resource plan requires %d", len(seen), len(requirements))
	}
	if files > policy.MaxFiles || computed.Bytes > policy.MaxTotalBytes {
		return nil, fmt.Errorf("resource manifest exceeds policy limits")
	}
	if computed.RequiredFailed != 0 || computed.Pending != 0 || computed.Failed != 0 {
		return nil, fmt.Errorf("resource manifest contains blocking resources")
	}
	if manifest.Summary != computed {
		return nil, fmt.Errorf("resources manifest summary does not match live items")
	}
	if err := requireNonEmptyFile(filepath.Join(projectPath, "analysis", "resource_requirements.json")); err != nil {
		return nil, err
	}
	if err := requireNonEmptyFile(filepath.Join(projectPath, "analysis", "image_analysis.csv")); err != nil {
		return nil, err
	}
	manifestSHA, err := sha256File(manifestPath)
	if err != nil {
		return nil, err
	}
	contract := map[string]any{
		"phase":                     string(PhaseImageAcquire),
		"project_path":              projectPath,
		"phase_run_id":              phaseRunID,
		"resource_plan_sha256":      planSHA,
		"resource_policy_sha256":    policy.PolicySHA256,
		"resources_manifest":        manifestPath,
		"resources_manifest_sha256": manifestSHA,
		"resource_summary":          manifest.Summary,
		"checked_at":                time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeContractReport(projectPath, string(PhaseImageAcquire), contract); err != nil {
		return nil, err
	}
	return contract, nil
}

func validateReadyResourcePolicy(item resourceManifestItem, policy *resourcePolicySnapshot) error {
	contains := func(values []string, value string) bool {
		for _, candidate := range values {
			if candidate == value {
				return true
			}
		}
		return false
	}
	switch item.AcquireVia {
	case "web":
		if !policy.NetworkEnabled || !policy.WebImageEnabled || item.Provider == "" || !contains(policy.AllowedWebProviders, item.Provider) {
			return fmt.Errorf("ready resource %s violates Web resource policy", item.ID)
		}
		license, _ := item.Provenance["license"].(string)
		if strings.TrimSpace(license) == "" {
			return fmt.Errorf("ready Web resource %s has no license metadata", item.ID)
		}
	case "ai":
		if !policy.NetworkEnabled || !policy.AIImageEnabled || policy.ImageAIPath != "api" || !contains(policy.AllowedAIPaths, "api") || item.Provider == "" || !contains(policy.AllowedAIProviders, item.Provider) {
			return fmt.Errorf("ready resource %s violates AI resource policy", item.ID)
		}
	case "formula":
		if !policy.NetworkEnabled || !policy.FormulaNetworkEnabled {
			return fmt.Errorf("ready resource %s violates formula resource policy", item.ID)
		}
	}
	return nil
}

func validateResourceOutput(projectPath string, output *resourceManifestOutput, maxSingleBytes int64) error {
	if output == nil || output.Path == "" || filepath.IsAbs(output.Path) {
		return fmt.Errorf("resource output path is empty or absolute")
	}
	if strings.Contains(output.Path, "://") || strings.HasPrefix(strings.ToLower(output.Path), "file:") {
		return fmt.Errorf("resource output path uses a forbidden URI")
	}
	clean := filepath.Clean(filepath.FromSlash(output.Path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("resource output path escapes project")
	}
	projectRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return err
	}
	candidate := filepath.Join(projectRoot, clean)
	info, resolved, err := inspectContainedPath(projectRoot, candidate)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return fmt.Errorf("resource output is not a non-empty regular file")
	}
	if output.Size != info.Size() || output.Size > maxSingleBytes {
		return fmt.Errorf("resource output size mismatch or policy overflow")
	}
	actualSHA, err := sha256File(resolved)
	if err != nil {
		return err
	}
	if output.SHA256 == "" || output.SHA256 != actualSHA {
		return fmt.Errorf("resource output SHA-256 mismatch")
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	actualMIME := detectResourceMIME(resolved, raw)
	if output.MimeType != actualMIME {
		return fmt.Errorf("resource output MIME = %q, detected %q", output.MimeType, actualMIME)
	}
	if actualMIME == "image/svg+xml" {
		if err := validateResourceSVG(raw); err != nil {
			return err
		}
	} else if strings.HasPrefix(actualMIME, "image/") {
		if output.Width <= 0 || output.Height <= 0 {
			return fmt.Errorf("resource output dimensions are missing")
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("resource output image cannot be decoded: %w", err)
		}
		if cfg.Width != output.Width || cfg.Height != output.Height {
			return fmt.Errorf("resource output dimensions mismatch")
		}
	}
	return nil
}

func detectResourceMIME(path string, raw []byte) string {
	lower := strings.ToLower(filepath.Ext(path))
	if lower == ".svg" || bytes.Contains(bytes.ToLower(raw[:minInt(len(raw), 512)]), []byte("<svg")) {
		return "image/svg+xml"
	}
	if lower == ".json" {
		return "application/json"
	}
	if lower == ".csv" {
		return "text/csv"
	}
	return strings.Split(http.DetectContentType(raw[:minInt(len(raw), 512)]), ";")[0]
}

func validateResourceSVG(raw []byte) error {
	lower := strings.ToLower(string(raw))
	if strings.Contains(lower, "<script") || strings.Contains(lower, "javascript:") || strings.Contains(lower, `href="http://`) || strings.Contains(lower, `href="https://`) || strings.Contains(lower, "href='http://") || strings.Contains(lower, "href='https://") {
		return fmt.Errorf("resource SVG contains script or external reference")
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		if _, err := decoder.Token(); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("resource SVG is not parseable: %w", err)
		}
	}
	return nil
}

func validateExistingResourceContract(projectPath string, task *model.Task, workspace *TaskWorkspace) (map[string]any, error) {
	path := filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseImageAcquire)+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var contract map[string]any
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil, err
	}
	if profile, _ := contract["runner_profile"].(string); task == nil || profile != task.RunnerProfile {
		return nil, fmt.Errorf("resource contract runner profile does not match task lock")
	}
	checks := map[string]string{
		"resources_manifest_sha256": filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"),
		"resource_plan_sha256":      filepath.Join(projectPath, ".slidesmith", "resource_plan.json"),
	}
	if workspace != nil {
		checks["runtime_manifest_sha256"] = filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json")
		if expected, _ := contract["skill_lock_sha256"].(string); expected != "" {
			checks["skill_lock_sha256"] = filepath.Join(workspace.HostDir, ".slidesmith", "skill_lock.json")
		}
	}
	for field, checkPath := range checks {
		expected, _ := contract[field].(string)
		if expected == "" {
			return nil, fmt.Errorf("resource contract missing %s", field)
		}
		actual, err := sha256File(checkPath)
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, fmt.Errorf("resource contract stale: %s changed", checkPath)
		}
	}
	return contract, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
