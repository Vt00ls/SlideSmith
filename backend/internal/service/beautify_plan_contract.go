package service

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type BeautifyPlanDocument struct {
	Schema             string               `json:"schema"`
	TaskID             string               `json:"task_id"`
	Status             string               `json:"status"`
	Revision           int                  `json:"revision"`
	SourcePPTXSHA256   string               `json:"source_pptx_sha256"`
	InventorySHA256    string               `json:"inventory_sha256"`
	ConfirmationSHA256 string               `json:"confirmation_sha256"`
	SlideCount         int                  `json:"slide_count"`
	Identity           BeautifyPlanIdentity `json:"identity"`
	Slides             []BeautifyPlanSlide  `json:"slides"`
	GlobalIgnored      []BeautifyContentRef `json:"global_ignored"`
	AcceptedRisks      []string             `json:"accepted_risks"`
	CreatedAt          string               `json:"created_at"`
}

type BeautifyPlanIdentity struct {
	Source             string `json:"source"`
	CanvasOverride     bool   `json:"canvas_override"`
	PaletteOverride    bool   `json:"palette_override"`
	TypographyOverride bool   `json:"typography_override"`
}

type BeautifyPlanSlide struct {
	SourceSlide    int                  `json:"source_slide"`
	OutputPage     int                  `json:"output_page"`
	PageRole       string               `json:"page_role"`
	PageRhythm     string               `json:"page_rhythm"`
	LayoutStrategy string               `json:"layout_strategy"`
	TextBlockIDs   []string             `json:"text_block_ids"`
	ImageIDs       []string             `json:"image_ids"`
	TableIDs       []string             `json:"table_ids"`
	ChartIDs       []string             `json:"chart_ids"`
	Ignored        []BeautifyContentRef `json:"ignored"`
	Unsupported    []BeautifyContentRef `json:"unsupported"`
	Risks          []string             `json:"risks"`
}

type BeautifyPlanContract struct {
	Schema                  string   `json:"schema"`
	TaskID                  string   `json:"task_id"`
	PlanSHA256              string   `json:"plan_sha256"`
	PlanStatus              string   `json:"plan_status"`
	PlanRevision            int      `json:"plan_revision"`
	InputsSHA256            string   `json:"inputs_sha256"`
	InventorySHA256         string   `json:"inventory_sha256"`
	InventoryContractSHA256 string   `json:"inventory_contract_sha256"`
	RiskReportSHA256        string   `json:"risk_report_sha256"`
	ConfirmationSHA256      string   `json:"confirmation_sha256"`
	SourcePPTXSHA256        string   `json:"source_pptx_sha256"`
	SlideCount              int      `json:"slide_count"`
	AcceptedRisks           []string `json:"accepted_risks"`
	CheckedAt               string   `json:"checked_at"`
}

func ValidateBeautifyPlanContract(projectPath, expectedTaskID string) (*BeautifyPlanContract, error) {
	inputs, err := ValidateBeautifyInputsContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	inventoryContract, err := validateExistingBeautifyInventoryContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	var inventory BeautifyInventoryDocument
	if err := beautifyReadJSON(projectPath, "analysis/beautify_inventory.json", &inventory); err != nil {
		return nil, err
	}
	var risks BeautifyRiskReport
	if err := beautifyReadJSON(projectPath, "analysis/beautify_risk_report.json", &risks); err != nil {
		return nil, err
	}
	var plan BeautifyPlanDocument
	if err := beautifyReadJSON(projectPath, "analysis/beautify_plan.json", &plan); err != nil {
		return nil, err
	}
	confirmationSHA, err := sha256File(filepath.Join(projectPath, "confirm_ui", "result.json"))
	if err != nil {
		return nil, fmt.Errorf("hash beautify confirmation: %w", err)
	}
	if plan.Schema != beautifyPlanSchema || plan.TaskID != expectedTaskID || plan.Revision < 1 || (plan.Status != "draft" && plan.Status != "confirmed") {
		return nil, fmt.Errorf("beautify plan schema/task/status/revision is invalid")
	}
	if plan.SourcePPTXSHA256 != inputs.SourcePPTX.SHA256 || plan.InventorySHA256 != inventoryContract.InventorySHA256 || plan.ConfirmationSHA256 != confirmationSHA {
		return nil, fmt.Errorf("beautify plan upstream hash binding mismatch")
	}
	if plan.SlideCount != inputs.SlideCount || len(plan.Slides) != inputs.SlideCount {
		return nil, fmt.Errorf("beautify plan slide count mismatch")
	}
	if strings.TrimSpace(plan.Identity.Source) == "" {
		return nil, fmt.Errorf("beautify plan identity source is empty")
	}
	acceptedRisks, err := validateBeautifyPlanAccounting(&plan, &inventory, &risks)
	if err != nil {
		return nil, err
	}
	planSHA, err := sha256File(filepath.Join(projectPath, "analysis", "beautify_plan.json"))
	if err != nil {
		return nil, err
	}
	inputsSHA, _ := sha256File(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"))
	inventoryContractSHA, _ := sha256File(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inventory.json"))
	contract := &BeautifyPlanContract{
		Schema: beautifyPlanContractSchema, TaskID: expectedTaskID, PlanSHA256: planSHA,
		PlanStatus: plan.Status, PlanRevision: plan.Revision, InputsSHA256: inputsSHA,
		InventorySHA256: inventoryContract.InventorySHA256, InventoryContractSHA256: inventoryContractSHA,
		RiskReportSHA256: inventoryContract.RiskReportSHA256, ConfirmationSHA256: confirmationSHA,
		SourcePPTXSHA256: inputs.SourcePPTX.SHA256, SlideCount: inputs.SlideCount,
		AcceptedRisks: acceptedRisks, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSONAtomic(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_plan.json"), contract); err != nil {
		return nil, err
	}
	return contract, nil
}

func validateExistingBeautifyPlanContract(projectPath, expectedTaskID string) (*BeautifyPlanContract, error) {
	var contract BeautifyPlanContract
	if err := beautifyReadJSON(projectPath, ".slidesmith/contracts/beautify_plan.json", &contract); err != nil {
		return nil, err
	}
	if contract.Schema != beautifyPlanContractSchema || contract.TaskID != expectedTaskID || contract.PlanRevision < 1 || contract.SlideCount <= 0 {
		return nil, fmt.Errorf("beautify plan contract schema/task/revision is invalid")
	}
	checks := map[string]string{
		contract.PlanSHA256:              filepath.Join(projectPath, "analysis", "beautify_plan.json"),
		contract.InputsSHA256:            filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"),
		contract.InventorySHA256:         filepath.Join(projectPath, "analysis", "beautify_inventory.json"),
		contract.InventoryContractSHA256: filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inventory.json"),
		contract.RiskReportSHA256:        filepath.Join(projectPath, "analysis", "beautify_risk_report.json"),
		contract.ConfirmationSHA256:      filepath.Join(projectPath, "confirm_ui", "result.json"),
	}
	for expected, path := range checks {
		actual, err := sha256File(path)
		if err != nil || !beautifySHA256Pattern.MatchString(expected) || actual != expected {
			return nil, fmt.Errorf("beautify plan contract is stale for %s", filepath.Base(path))
		}
	}
	return &contract, nil
}

func validateBeautifyPlanAccounting(plan *BeautifyPlanDocument, inventory *BeautifyInventoryDocument, risks *BeautifyRiskReport) ([]string, error) {
	inventoryByID := map[string]struct {
		kind string
		page int
	}{}
	semanticByID := map[string]struct {
		disposition string
		page        int
	}{}
	for _, slide := range inventory.Slides {
		for _, item := range slide.TextBlocks {
			inventoryByID[item.ID] = struct {
				kind string
				page int
			}{"text", slide.SlideIndex}
		}
		for _, item := range slide.Tables {
			inventoryByID[item.ID] = struct {
				kind string
				page int
			}{"table", slide.SlideIndex}
		}
		for _, item := range slide.Charts {
			inventoryByID[item.ID] = struct {
				kind string
				page int
			}{"chart", slide.SlideIndex}
		}
		for _, item := range slide.Images {
			inventoryByID[item.ID] = struct {
				kind string
				page int
			}{"image", slide.SlideIndex}
		}
		for _, item := range slide.Ignored {
			semanticByID[item.ID] = struct {
				disposition string
				page        int
			}{"ignored", slide.SlideIndex}
		}
		for _, item := range slide.NeedsConfirmation {
			semanticByID[item.ID] = struct {
				disposition string
				page        int
			}{"unsupported", slide.SlideIndex}
		}
	}
	accounted := map[string]string{}
	semanticAccounted := map[string]bool{}
	knownRisks := map[string]BeautifyRiskFinding{}
	plannedRisks := map[string]bool{}
	for _, risk := range risks.Risks {
		knownRisks[risk.ID] = risk
	}
	for index, slide := range plan.Slides {
		page := index + 1
		if slide.SourceSlide != page || slide.OutputPage != page {
			return nil, fmt.Errorf("beautify plan page %d is not a 1:1 source/output mapping", page)
		}
		if strings.TrimSpace(slide.LayoutStrategy) == "" {
			return nil, fmt.Errorf("beautify plan page %d layout_strategy is empty", page)
		}
		planned := map[string][]string{"text": slide.TextBlockIDs, "table": slide.TableIDs, "chart": slide.ChartIDs, "image": slide.ImageIDs}
		for kind, ids := range planned {
			if err := beautifyRequireUniqueIDs(ids, fmt.Sprintf("beautify plan page %d %s", page, kind)); err != nil {
				return nil, err
			}
			for _, id := range ids {
				item, ok := inventoryByID[id]
				if !ok || item.kind != kind || item.page != page {
					return nil, fmt.Errorf("beautify plan page %d %s id %q has invalid inventory binding", page, kind, id)
				}
				if previous := accounted[id]; previous != "" {
					return nil, fmt.Errorf("beautify plan item %q is accounted more than once", id)
				}
				accounted[id] = "planned"
			}
		}
		for disposition, decisions := range map[string][]BeautifyContentRef{"ignored": slide.Ignored, "unsupported": slide.Unsupported} {
			for _, decision := range decisions {
				item, ok := inventoryByID[decision.ID]
				if strings.TrimSpace(decision.Reason) == "" {
					return nil, fmt.Errorf("beautify plan page %d has invalid %s item %q", page, disposition, decision.ID)
				}
				if !ok {
					semantic, semanticOK := semanticByID[decision.ID]
					if !semanticOK || semantic.page != page || (semantic.disposition == "ignored" && disposition != "ignored") {
						return nil, fmt.Errorf("beautify plan page %d has invalid %s item %q", page, disposition, decision.ID)
					}
					if semanticAccounted[decision.ID] {
						return nil, fmt.Errorf("beautify plan semantic item %q is accounted more than once", decision.ID)
					}
					semanticAccounted[decision.ID] = true
					continue
				}
				if item.page != page {
					return nil, fmt.Errorf("beautify plan page %d has invalid %s item %q", page, disposition, decision.ID)
				}
				if item.kind != "image" {
					return nil, fmt.Errorf("beautify plan %s cannot discard frozen %s item %q", disposition, item.kind, decision.ID)
				}
				if previous := accounted[decision.ID]; previous != "" {
					return nil, fmt.Errorf("beautify plan item %q is accounted more than once", decision.ID)
				}
				accounted[decision.ID] = disposition
			}
		}
		if err := beautifyRequireUniqueIDs(slide.Risks, fmt.Sprintf("beautify plan page %d risks", page)); err != nil {
			return nil, err
		}
		for _, riskID := range slide.Risks {
			if risk, ok := knownRisks[riskID]; !ok || risk.SlideIndex != page {
				return nil, fmt.Errorf("beautify plan page %d references invalid risk %q", page, riskID)
			}
			plannedRisks[riskID] = true
			if semantic, ok := semanticByID[riskID]; ok && semantic.disposition == "unsupported" {
				semanticAccounted[riskID] = true
			}
		}
	}
	for id, item := range inventoryByID {
		if accounted[id] == "" {
			return nil, fmt.Errorf("beautify plan does not account for %s item %q on page %d", item.kind, id, item.page)
		}
	}
	for id, item := range semanticByID {
		if !semanticAccounted[id] {
			return nil, fmt.Errorf("beautify plan does not account for %s semantic item %q on page %d", item.disposition, id, item.page)
		}
	}
	for _, item := range plan.GlobalIgnored {
		if item.ID == "" || item.Reason == "" {
			return nil, fmt.Errorf("beautify plan has invalid global ignored item")
		}
		if _, frozen := inventoryByID[item.ID]; frozen {
			return nil, fmt.Errorf("beautify plan global ignored cannot discard frozen item %q", item.ID)
		}
	}
	for riskID := range knownRisks {
		if !plannedRisks[riskID] {
			return nil, fmt.Errorf("beautify plan does not account for risk %q", riskID)
		}
	}
	accepted, err := beautifySortedUnique(plan.AcceptedRisks)
	if err != nil && len(plan.AcceptedRisks) > 0 {
		return nil, fmt.Errorf("beautify plan accepted risks: %w", err)
	}
	acceptedSet := map[string]bool{}
	for _, id := range accepted {
		if _, ok := knownRisks[id]; !ok {
			return nil, fmt.Errorf("beautify plan accepts unknown risk %q", id)
		}
		acceptedSet[id] = true
	}
	if plan.Status == "confirmed" {
		for _, risk := range risks.Risks {
			if risk.NeedsConfirmation && !acceptedSet[risk.ID] {
				return nil, fmt.Errorf("confirmed beautify plan has unaccepted risk %q", risk.ID)
			}
		}
	}
	return accepted, nil
}

func CheckBeautifyCAS(actualSHA string, actualRevision int, expectedSHA string, expectedRevision int) error {
	if !beautifySHA256Pattern.MatchString(actualSHA) || !beautifySHA256Pattern.MatchString(expectedSHA) {
		return fmt.Errorf("beautify CAS requires canonical SHA-256 values")
	}
	if actualRevision < 1 || expectedRevision < 1 {
		return fmt.Errorf("beautify CAS requires positive revisions")
	}
	if actualSHA != expectedSHA || actualRevision != expectedRevision {
		return fmt.Errorf("beautify plan changed concurrently: current revision/hash %d/%s", actualRevision, actualSHA)
	}
	return nil
}

func beautifySortedKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
