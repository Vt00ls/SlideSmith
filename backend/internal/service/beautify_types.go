package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	beautifyInputsSchema            = "slidesmith.beautify_inputs.v1"
	beautifyInventorySchema         = "beautify_inventory.v1"
	beautifyInventoryContractSchema = "slidesmith.beautify_inventory_contract.v1"
	beautifyRiskReportSchema        = "slidesmith.beautify_risk_report.v1"
	beautifyPlanSchema              = "slidesmith.beautify_plan.v1"
	beautifyPlanContractSchema      = "slidesmith.beautify_plan_contract.v1"
	beautifyLockSchema              = "slidesmith.beautify_lock.v1"
	beautifyFidelityReportSchema    = "slidesmith.beautify_fidelity_report.v1"
)

var beautifySHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type BeautifyFileRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type BeautifyCanvas struct {
	Width       float64 `json:"width,omitempty"`
	Height      float64 `json:"height,omitempty"`
	Unit        string  `json:"unit,omitempty"`
	AspectRatio float64 `json:"aspect_ratio,omitempty"`
}

type BeautifyContentRef struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

func beautifyFileRef(projectPath, relativePath string) (BeautifyFileRef, error) {
	path, err := containedProjectContractPath(projectPath, relativePath)
	if err != nil {
		return BeautifyFileRef{}, err
	}
	info, _, err := inspectContainedPath(projectPath, path)
	if err != nil {
		return BeautifyFileRef{}, fmt.Errorf("unsafe beautify project-relative file %q (symlink, path escape, or missing file)", relativePath)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return BeautifyFileRef{}, fmt.Errorf("beautify input is not a non-empty regular file: %s", filepath.Base(path))
	}
	digest, err := sha256File(path)
	if err != nil {
		return BeautifyFileRef{}, fmt.Errorf("hash beautify project-relative file %q", relativePath)
	}
	return BeautifyFileRef{Path: relativePath, SHA256: digest, Size: info.Size()}, nil
}

func validateBeautifyFileRef(projectPath string, ref BeautifyFileRef, label string) error {
	if ref.Path == "" || ref.Size <= 0 || !beautifySHA256Pattern.MatchString(ref.SHA256) {
		return fmt.Errorf("%s file binding is incomplete", label)
	}
	live, err := beautifyFileRef(projectPath, ref.Path)
	if err != nil {
		return fmt.Errorf("%s file binding: %w", label, err)
	}
	if live.SHA256 != ref.SHA256 || live.Size != ref.Size {
		return fmt.Errorf("%s file binding is stale", label)
	}
	return nil
}

func beautifyJSONSHA256(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func beautifyReadJSON(projectPath, relativePath string, target any) error {
	path, err := containedProjectContractPath(projectPath, relativePath)
	if err != nil {
		return err
	}
	if err := requireContainedContractFile(projectPath, path); err != nil {
		return fmt.Errorf("unsafe or missing beautify contract file %q", relativePath)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read beautify contract file %q", relativePath)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(relativePath), err)
	}
	return nil
}

func beautifyRequireUniqueIDs(ids []string, label string) error {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("%s contains an empty id", label)
		}
		if seen[id] {
			return fmt.Errorf("%s contains duplicate id %q", label, id)
		}
		seen[id] = true
	}
	return nil
}

func beautifySortedUnique(values []string) ([]string, error) {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("empty identifier")
		}
		if seen[value] {
			return nil, fmt.Errorf("duplicate identifier %q", value)
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}
