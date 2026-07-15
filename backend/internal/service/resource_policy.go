package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const resourcePolicySchema = "slidesmith.resource_policy.v1"

type resourcePolicySnapshot struct {
	Schema                   string            `json:"schema"`
	TaskID                   string            `json:"task_id"`
	Route                    string            `json:"route"`
	RunnerProfile            string            `json:"runner_profile"`
	RunnerProfileLockedAt    string            `json:"runner_profile_locked_at"`
	PhaseRunID               string            `json:"phase_run_id"`
	ConfirmationSHA256       string            `json:"confirmation_sha256"`
	ConfirmationImageSources []string          `json:"confirmation_image_sources"`
	IconLibrary              string            `json:"icon_library"`
	FormulaPolicy            string            `json:"formula_policy"`
	ImageAIPath              string            `json:"image_ai_path"`
	PhaseEnabled             bool              `json:"phase_enabled"`
	NetworkEnabled           bool              `json:"network_enabled"`
	WebImageEnabled          bool              `json:"web_image_enabled"`
	AIImageEnabled           bool              `json:"ai_image_enabled"`
	FormulaNetworkEnabled    bool              `json:"formula_network_enabled"`
	AllowedAIPaths           []string          `json:"allowed_ai_paths"`
	AllowedWebProviders      []string          `json:"allowed_web_providers"`
	AllowedAIProviders       []string          `json:"allowed_ai_providers"`
	MaxFiles                 int               `json:"max_files"`
	MaxTotalBytes            int64             `json:"max_total_bytes"`
	MaxSingleBytes           int64             `json:"max_single_bytes"`
	TimeoutSeconds           int64             `json:"timeout_seconds"`
	FallbackRules            map[string]string `json:"fallback_rules"`
	CreatedAt                string            `json:"created_at"`
	PolicySHA256             string            `json:"policy_sha256"`
}

func (s *TaskService) writeResourcePolicySnapshot(task *model.Task, projectPath, phaseRunID string) (*resourcePolicySnapshot, error) {
	if task == nil || !s.useFullPPTMaster(task) || !isFullSVGRoute(task.Route) {
		return nil, fmt.Errorf("resource policy requires a locked full-ppt-master full SVG route task")
	}
	if task.RunnerProfileLockedAt == nil {
		return nil, fmt.Errorf("resource policy requires runner profile lock time")
	}
	confirmationPath := filepath.Join(projectPath, "confirm_ui", "result.json")
	confirmationSHA, err := sha256File(confirmationPath)
	if err != nil {
		return nil, fmt.Errorf("hash resource confirmation: %w", err)
	}
	confirmation := readJSONMap(confirmationPath)
	sourcesMap := confirmationImageSources(confirmation)
	sources := make([]string, 0, len(sourcesMap))
	for source := range sourcesMap {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	maxFiles := s.agentCfg.ResourceMaxFiles
	if maxFiles <= 0 {
		maxFiles = 100
	}
	maxTotalBytes := s.agentCfg.ResourceMaxTotalBytes
	if maxTotalBytes <= 0 {
		maxTotalBytes = 524288000
	}
	maxSingleBytes := s.agentCfg.ResourceMaxSingleBytes
	if maxSingleBytes <= 0 || maxSingleBytes > maxTotalBytes {
		maxSingleBytes = 52428800
		if maxSingleBytes > maxTotalBytes {
			maxSingleBytes = maxTotalBytes
		}
	}
	timeout := s.agentCfg.ResourceTimeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	policy := &resourcePolicySnapshot{
		Schema:                   resourcePolicySchema,
		TaskID:                   task.ID,
		Route:                    task.Route,
		RunnerProfile:            task.RunnerProfile,
		RunnerProfileLockedAt:    task.RunnerProfileLockedAt.UTC().Format(time.RFC3339Nano),
		PhaseRunID:               phaseRunID,
		ConfirmationSHA256:       confirmationSHA,
		ConfirmationImageSources: sources,
		IconLibrary:              strings.ToLower(strings.TrimSpace(valueString(confirmation, "icons", "none"))),
		FormulaPolicy:            strings.ToLower(strings.TrimSpace(valueString(confirmation, "formula_policy", "none"))),
		ImageAIPath:              strings.ToLower(strings.TrimSpace(valueString(confirmation, "image_ai_path", "api"))),
		PhaseEnabled:             s.agentCfg.ResourcePhaseEnabled,
		NetworkEnabled:           s.agentCfg.ResourceNetworkEnabled,
		WebImageEnabled:          s.agentCfg.ResourceWebEnabled,
		AIImageEnabled:           s.agentCfg.ResourceAIEnabled,
		FormulaNetworkEnabled:    s.agentCfg.ResourceFormulaNetwork,
		AllowedAIPaths:           splitResourceConfigList(s.agentCfg.ResourceAIPaths),
		AllowedWebProviders:      splitResourceConfigList(s.agentCfg.ResourceWebProviders),
		AllowedAIProviders:       splitResourceConfigList(s.agentCfg.ResourceAIProviders),
		MaxFiles:                 maxFiles,
		MaxTotalBytes:            maxTotalBytes,
		MaxSingleBytes:           maxSingleBytes,
		TimeoutSeconds:           int64(timeout / time.Second),
		FallbackRules: map[string]string{
			"policy_denied":   "use only the fallback explicitly approved by resource_plan.json",
			"provider_failed": "never change web to ai or ai to web",
			"manual":          "manual_path_not_supported",
			"host_native":     "host_native_path_not_supported",
		},
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	digest, err := resourcePolicyDigest(policy)
	if err != nil {
		return nil, err
	}
	policy.PolicySHA256 = digest
	if err := writeJSONAtomic(filepath.Join(projectPath, ".slidesmith", "resource_policy.json"), policy); err != nil {
		return nil, err
	}
	return policy, nil
}

func loadResourcePolicy(projectPath string) (*resourcePolicySnapshot, error) {
	path := filepath.Join(projectPath, ".slidesmith", "resource_policy.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var policy resourcePolicySnapshot
	if err := json.Unmarshal(raw, &policy); err != nil {
		return nil, err
	}
	if policy.Schema != resourcePolicySchema {
		return nil, fmt.Errorf("resource policy schema = %q", policy.Schema)
	}
	digest, err := resourcePolicyDigest(&policy)
	if err != nil {
		return nil, err
	}
	if policy.PolicySHA256 != digest {
		return nil, fmt.Errorf("resource policy digest mismatch")
	}
	return &policy, nil
}

func resourcePolicyDigest(policy *resourcePolicySnapshot) (string, error) {
	copyPolicy := *policy
	copyPolicy.PolicySHA256 = ""
	raw, err := json.Marshal(copyPolicy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func splitResourceConfigList(value string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".slidesmith-resource-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
