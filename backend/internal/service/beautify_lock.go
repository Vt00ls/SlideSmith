package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type BeautifyLock struct {
	Schema                  string                 `json:"schema"`
	TaskID                  string                 `json:"task_id"`
	Revision                int                    `json:"revision"`
	SourcePPTXSHA256        string                 `json:"source_pptx_sha256"`
	InputsSHA256            string                 `json:"inputs_sha256"`
	InventorySHA256         string                 `json:"inventory_sha256"`
	InventoryContractSHA256 string                 `json:"inventory_contract_sha256"`
	PlanSHA256              string                 `json:"plan_sha256"`
	PlanContractSHA256      string                 `json:"plan_contract_sha256"`
	ConfirmationSHA256      string                 `json:"confirmation_sha256"`
	IdentitySHA256          string                 `json:"identity_sha256"`
	SlideCount              int                    `json:"slide_count"`
	SlideOrder              []int                  `json:"slide_order"`
	Canvas                  BeautifyCanvas         `json:"canvas"`
	Identity                BeautifyPlanIdentity   `json:"identity"`
	Slides                  []BeautifyFrozenSlide  `json:"slides"`
	Ignored                 []BeautifyLockDecision `json:"ignored"`
	Unsupported             []BeautifyLockDecision `json:"unsupported"`
	AcceptedRisks           []string               `json:"accepted_risks"`
	LockedAt                string                 `json:"locked_at"`
}

type BeautifyFrozenSlide struct {
	SourceSlide int                      `json:"source_slide"`
	OutputPage  int                      `json:"output_page"`
	SHA256      string                   `json:"content_data_sha256"`
	TextBlocks  []BeautifyInventoryText  `json:"text_blocks"`
	Tables      []BeautifyInventoryTable `json:"tables"`
	Charts      []BeautifyInventoryChart `json:"charts"`
	Images      []BeautifyInventoryImage `json:"images"`
}

type BeautifyLockDecision struct {
	SlideIndex int    `json:"slide_index"`
	ID         string `json:"id"`
	Reason     string `json:"reason"`
}

func BuildBeautifyLock(projectPath, expectedTaskID, expectedPlanSHA string) (*BeautifyLock, error) {
	if !beautifySHA256Pattern.MatchString(expectedPlanSHA) {
		return nil, fmt.Errorf("beautify lock expected plan SHA-256 is invalid")
	}
	lockPath := filepath.Join(projectPath, ".slidesmith", "beautify_lock.json")
	if _, err := os.Lstat(lockPath); err == nil {
		existing, validateErr := ValidateBeautifyLock(projectPath, expectedTaskID)
		if validateErr != nil {
			return nil, fmt.Errorf("existing beautify lock is invalid and immutable: %w", validateErr)
		}
		if existing.PlanSHA256 != expectedPlanSHA {
			return nil, fmt.Errorf("beautify lock is immutable at plan revision %d", existing.Revision)
		}
		return existing, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	inputs, err := ValidateBeautifyInputsContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	inventoryContract, err := validateExistingBeautifyInventoryContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	planContract, err := validateExistingBeautifyPlanContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	if planContract.PlanStatus != "confirmed" {
		return nil, fmt.Errorf("beautify lock requires a confirmed plan")
	}
	if planContract.PlanSHA256 != expectedPlanSHA {
		return nil, fmt.Errorf("beautify lock plan changed before confirmation commit")
	}
	var inventory BeautifyInventoryDocument
	if err := beautifyReadJSON(projectPath, "analysis/beautify_inventory.json", &inventory); err != nil {
		return nil, err
	}
	var plan BeautifyPlanDocument
	if err := beautifyReadJSON(projectPath, "analysis/beautify_plan.json", &plan); err != nil {
		return nil, err
	}
	planContractSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_plan.json"))
	if err != nil {
		return nil, err
	}
	lock := &BeautifyLock{
		Schema: beautifyLockSchema, TaskID: expectedTaskID, Revision: plan.Revision,
		SourcePPTXSHA256: inputs.SourcePPTX.SHA256, InputsSHA256: planContract.InputsSHA256,
		InventorySHA256:         inventoryContract.InventorySHA256,
		InventoryContractSHA256: planContract.InventoryContractSHA256,
		PlanSHA256:              planContract.PlanSHA256, PlanContractSHA256: planContractSHA,
		ConfirmationSHA256: planContract.ConfirmationSHA256, IdentitySHA256: inputs.Identity.SHA256,
		SlideCount: inputs.SlideCount, Canvas: inputs.Canvas, Identity: plan.Identity,
		AcceptedRisks: append([]string(nil), planContract.AcceptedRisks...),
		LockedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	pageHashByIndex := map[int]string{}
	for _, page := range inventoryContract.Pages {
		pageHashByIndex[page.SlideIndex] = page.SHA256
	}
	for index, slide := range inventory.Slides {
		planSlide := plan.Slides[index]
		lock.SlideOrder = append(lock.SlideOrder, slide.SlideIndex)
		lock.Slides = append(lock.Slides, BeautifyFrozenSlide{
			SourceSlide: slide.SlideIndex, OutputPage: planSlide.OutputPage,
			SHA256: pageHashByIndex[slide.SlideIndex], TextBlocks: slide.TextBlocks,
			Tables: slide.Tables, Charts: slide.Charts, Images: slide.Images,
		})
		for _, item := range planSlide.Ignored {
			lock.Ignored = append(lock.Ignored, BeautifyLockDecision{SlideIndex: slide.SlideIndex, ID: item.ID, Reason: item.Reason})
		}
		for _, item := range planSlide.Unsupported {
			lock.Unsupported = append(lock.Unsupported, BeautifyLockDecision{SlideIndex: slide.SlideIndex, ID: item.ID, Reason: item.Reason})
		}
	}
	if err := validateBeautifyLockDocument(lock); err != nil {
		return nil, err
	}
	if err := writeBeautifyLockExclusive(lockPath, lock); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, validateErr := ValidateBeautifyLock(projectPath, expectedTaskID)
			if validateErr == nil && existing.PlanSHA256 == expectedPlanSHA {
				return existing, nil
			}
		}
		return nil, err
	}
	return ValidateBeautifyLock(projectPath, expectedTaskID)
}

func ValidateBeautifyLock(projectPath, expectedTaskID string) (*BeautifyLock, error) {
	var lock BeautifyLock
	if err := beautifyReadJSON(projectPath, ".slidesmith/beautify_lock.json", &lock); err != nil {
		return nil, err
	}
	if lock.TaskID != expectedTaskID {
		return nil, fmt.Errorf("beautify lock task_id = %q, expected %q", lock.TaskID, expectedTaskID)
	}
	if err := validateBeautifyLockDocument(&lock); err != nil {
		return nil, err
	}
	checks := map[string]string{
		lock.SourcePPTXSHA256:        filepath.Join(projectPath, lockSourcePPTXPath(projectPath)),
		lock.InputsSHA256:            filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"),
		lock.InventorySHA256:         filepath.Join(projectPath, "analysis", "beautify_inventory.json"),
		lock.InventoryContractSHA256: filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inventory.json"),
		lock.PlanSHA256:              filepath.Join(projectPath, "analysis", "beautify_plan.json"),
		lock.PlanContractSHA256:      filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_plan.json"),
		lock.ConfirmationSHA256:      filepath.Join(projectPath, "confirm_ui", "result.json"),
	}
	for expected, path := range checks {
		actual, err := sha256File(path)
		if err != nil || !beautifySHA256Pattern.MatchString(expected) || actual != expected {
			return nil, fmt.Errorf("beautify lock is stale for %s", filepath.Base(path))
		}
	}
	inputs, err := ValidateBeautifyInputsContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	if lock.SourcePPTXSHA256 != inputs.SourcePPTX.SHA256 || lock.IdentitySHA256 != inputs.Identity.SHA256 || lock.SlideCount != inputs.SlideCount {
		return nil, fmt.Errorf("beautify lock source identity binding is stale")
	}
	for _, slide := range lock.Slides {
		for _, image := range slide.Images {
			if err := validateBeautifyFileRef(projectPath, BeautifyFileRef{Path: image.SourcePath, SHA256: image.SHA256, Size: image.Size}, "beautify lock image "+image.ID); err != nil {
				return nil, err
			}
		}
	}
	return &lock, nil
}

func validateBeautifyLockDocument(lock *BeautifyLock) error {
	if lock == nil || lock.Schema != beautifyLockSchema || lock.TaskID == "" || lock.Revision < 1 || lock.SlideCount <= 0 || len(lock.Slides) != lock.SlideCount || len(lock.SlideOrder) != lock.SlideCount {
		return fmt.Errorf("beautify lock schema/task/revision/page binding is invalid")
	}
	for index, slide := range lock.Slides {
		page := index + 1
		if lock.SlideOrder[index] != page || slide.SourceSlide != page || slide.OutputPage != page || !beautifySHA256Pattern.MatchString(slide.SHA256) {
			return fmt.Errorf("beautify lock slide %d mapping/hash is invalid", page)
		}
		for _, image := range slide.Images {
			if image.ID == "" || image.SourcePath == "" || !beautifySHA256Pattern.MatchString(image.SHA256) || image.Size <= 0 {
				return fmt.Errorf("beautify lock slide %d image binding is invalid", page)
			}
		}
	}
	if _, err := beautifySortedUnique(lock.AcceptedRisks); err != nil && len(lock.AcceptedRisks) > 0 {
		return fmt.Errorf("beautify lock accepted risks: %w", err)
	}
	for _, decisions := range [][]BeautifyLockDecision{lock.Ignored, lock.Unsupported} {
		for _, item := range decisions {
			if item.SlideIndex < 1 || item.SlideIndex > lock.SlideCount || item.ID == "" || item.Reason == "" {
				return fmt.Errorf("beautify lock contains invalid disposition")
			}
		}
	}
	return nil
}

func lockSourcePPTXPath(projectPath string) string {
	var inputs BeautifyInputsContract
	if beautifyReadJSON(projectPath, ".slidesmith/contracts/beautify_inputs.json", &inputs) == nil {
		return filepath.FromSlash(inputs.SourcePPTX.Path)
	}
	return filepath.Join("sources", "missing.pptx")
}

func writeBeautifyLockExclusive(path string, lock *BeautifyLock) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		closed = true
		_ = os.Remove(path)
		return err
	}
	closed = true
	return nil
}

func sortBeautifyLockDecisions(items []BeautifyLockDecision) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].SlideIndex != items[j].SlideIndex {
			return items[i].SlideIndex < items[j].SlideIndex
		}
		return items[i].ID < items[j].ID
	})
}
