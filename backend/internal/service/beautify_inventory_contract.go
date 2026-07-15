package service

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type BeautifyInventoryDocument struct {
	Schema           string                   `json:"schema"`
	TaskID           string                   `json:"task_id"`
	SourcePPTXSHA256 string                   `json:"source_pptx_sha256"`
	SlideCount       int                      `json:"slide_count"`
	Slides           []BeautifyInventorySlide `json:"slides"`
}

type BeautifyInventorySlide struct {
	SlideIndex        int                      `json:"slide_index"`
	PageType          string                   `json:"page_type,omitempty"`
	Hidden            bool                     `json:"hidden,omitempty"`
	TextBlocks        []BeautifyInventoryText  `json:"text_blocks"`
	Tables            []BeautifyInventoryTable `json:"tables"`
	Charts            []BeautifyInventoryChart `json:"charts"`
	Images            []BeautifyInventoryImage `json:"images"`
	Ignored           []BeautifyContentRef     `json:"ignored"`
	NeedsConfirmation []BeautifyContentRef     `json:"needs_confirmation"`
}

type BeautifyInventoryText struct {
	ID         string   `json:"id"`
	Role       string   `json:"role,omitempty"`
	Text       string   `json:"text"`
	Paragraphs []string `json:"paragraphs,omitempty"`
}

type BeautifyInventoryTable struct {
	ID       string     `json:"id"`
	RowCount int        `json:"row_count"`
	ColCount int        `json:"column_count"`
	Cells    [][]string `json:"cells"`
}

type BeautifyInventoryChartSeries struct {
	Name   string `json:"name"`
	Values []any  `json:"values"`
}

type BeautifyInventoryChart struct {
	ID         string                         `json:"id"`
	Type       string                         `json:"type"`
	Categories []string                       `json:"categories"`
	Series     []BeautifyInventoryChartSeries `json:"series"`
}

type BeautifyInventoryImage struct {
	ID               string `json:"id"`
	Filename         string `json:"filename,omitempty"`
	SourceOccurrence string `json:"source_occurrence"`
	SourcePath       string `json:"source_path,omitempty"`
	SHA256           string `json:"sha256"`
	Size             int64  `json:"size"`
	Required         bool   `json:"required"`
}

type BeautifyRiskReport struct {
	Schema          string                `json:"schema"`
	TaskID          string                `json:"task_id"`
	InputsSHA256    string                `json:"inputs_sha256"`
	InventorySHA256 string                `json:"inventory_sha256"`
	Risks           []BeautifyRiskFinding `json:"risks"`
	CreatedAt       string                `json:"created_at,omitempty"`
}

type BeautifyRiskFinding struct {
	ID                string   `json:"id"`
	SlideIndex        int      `json:"slide_index"`
	Rule              string   `json:"rule"`
	Severity          string   `json:"severity"`
	ItemIDs           []string `json:"item_ids"`
	NeedsConfirmation bool     `json:"needs_confirmation"`
	Message           string   `json:"message"`
}

type BeautifyInventoryPageContract struct {
	SlideIndex int      `json:"slide_index"`
	SHA256     string   `json:"content_data_sha256"`
	TextIDs    []string `json:"text_ids"`
	TableIDs   []string `json:"table_ids"`
	ChartIDs   []string `json:"chart_ids"`
	ImageIDs   []string `json:"image_ids"`
}

type BeautifyInventoryContract struct {
	Schema           string                          `json:"schema"`
	TaskID           string                          `json:"task_id"`
	InputsSHA256     string                          `json:"inputs_sha256"`
	InventorySHA256  string                          `json:"inventory_sha256"`
	RiskReportSHA256 string                          `json:"risk_report_sha256"`
	SourcePPTXSHA256 string                          `json:"source_pptx_sha256"`
	SlideCount       int                             `json:"slide_count"`
	Pages            []BeautifyInventoryPageContract `json:"pages"`
	RiskIDs          []string                        `json:"risk_ids"`
	CheckedAt        string                          `json:"checked_at"`
}

func ValidateBeautifyInventoryContract(projectPath, expectedTaskID string) (*BeautifyInventoryContract, error) {
	inputs, err := ValidateBeautifyInputsContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, fmt.Errorf("beautify inventory inputs: %w", err)
	}
	var inventory BeautifyInventoryDocument
	if err := beautifyReadJSON(projectPath, "analysis/beautify_inventory.json", &inventory); err != nil {
		return nil, err
	}
	if inventory.Schema != beautifyInventorySchema || inventory.TaskID != expectedTaskID || inventory.SourcePPTXSHA256 != inputs.SourcePPTX.SHA256 {
		return nil, fmt.Errorf("beautify inventory schema/task/source binding mismatch")
	}
	if inventory.SlideCount != inputs.SlideCount || len(inventory.Slides) != inputs.SlideCount {
		return nil, fmt.Errorf("beautify inventory slide count = %d/%d, expected %d", inventory.SlideCount, len(inventory.Slides), inputs.SlideCount)
	}
	inputsSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"))
	if err != nil {
		return nil, err
	}
	inventorySHA, err := sha256File(filepath.Join(projectPath, "analysis", "beautify_inventory.json"))
	if err != nil {
		return nil, err
	}
	pages, allIDs, err := validateBeautifyInventorySlides(projectPath, inventory.Slides)
	if err != nil {
		return nil, err
	}
	var risks BeautifyRiskReport
	if err := beautifyReadJSON(projectPath, "analysis/beautify_risk_report.json", &risks); err != nil {
		return nil, err
	}
	if risks.Schema != beautifyRiskReportSchema || risks.TaskID != expectedTaskID || risks.InputsSHA256 != inputsSHA || risks.InventorySHA256 != inventorySHA {
		return nil, fmt.Errorf("beautify risk report upstream binding mismatch")
	}
	riskIDs, err := validateBeautifyRisks(risks.Risks, inputs.SlideCount, allIDs)
	if err != nil {
		return nil, err
	}
	riskSHA, err := sha256File(filepath.Join(projectPath, "analysis", "beautify_risk_report.json"))
	if err != nil {
		return nil, err
	}
	contract := &BeautifyInventoryContract{
		Schema: beautifyInventoryContractSchema, TaskID: expectedTaskID,
		InputsSHA256: inputsSHA, InventorySHA256: inventorySHA, RiskReportSHA256: riskSHA,
		SourcePPTXSHA256: inputs.SourcePPTX.SHA256, SlideCount: inputs.SlideCount,
		Pages: pages, RiskIDs: riskIDs, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSONAtomic(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inventory.json"), contract); err != nil {
		return nil, err
	}
	return contract, nil
}

func validateExistingBeautifyInventoryContract(projectPath, expectedTaskID string) (*BeautifyInventoryContract, error) {
	var contract BeautifyInventoryContract
	if err := beautifyReadJSON(projectPath, ".slidesmith/contracts/beautify_inventory.json", &contract); err != nil {
		return nil, err
	}
	if contract.Schema != beautifyInventoryContractSchema || contract.TaskID != expectedTaskID || contract.SlideCount <= 0 || len(contract.Pages) != contract.SlideCount {
		return nil, fmt.Errorf("beautify inventory contract schema/task/page binding is invalid")
	}
	checks := map[string]string{
		contract.InputsSHA256:     filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"),
		contract.InventorySHA256:  filepath.Join(projectPath, "analysis", "beautify_inventory.json"),
		contract.RiskReportSHA256: filepath.Join(projectPath, "analysis", "beautify_risk_report.json"),
	}
	for expected, path := range checks {
		actual, err := sha256File(path)
		if err != nil || !beautifySHA256Pattern.MatchString(expected) || actual != expected {
			return nil, fmt.Errorf("beautify inventory contract is stale for %s", filepath.Base(path))
		}
	}
	var inventory BeautifyInventoryDocument
	if err := beautifyReadJSON(projectPath, "analysis/beautify_inventory.json", &inventory); err != nil {
		return nil, err
	}
	if err := validateBeautifyInventoryImageFiles(projectPath, inventory.Slides); err != nil {
		return nil, err
	}
	return &contract, nil
}

func validateBeautifyInventorySlides(projectPath string, slides []BeautifyInventorySlide) ([]BeautifyInventoryPageContract, map[string]bool, error) {
	allIDs := map[string]bool{}
	pages := make([]BeautifyInventoryPageContract, 0, len(slides))
	for index, slide := range slides {
		if slide.SlideIndex != index+1 {
			return nil, nil, fmt.Errorf("beautify inventory slide index = %d at position %d", slide.SlideIndex, index+1)
		}
		page := BeautifyInventoryPageContract{SlideIndex: slide.SlideIndex}
		for _, item := range slide.TextBlocks {
			if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Text) == "" {
				return nil, nil, fmt.Errorf("beautify inventory slide %d has invalid text block", slide.SlideIndex)
			}
			page.TextIDs = append(page.TextIDs, item.ID)
		}
		for _, item := range slide.Tables {
			if err := validateBeautifyInventoryTable(slide.SlideIndex, item); err != nil {
				return nil, nil, err
			}
			page.TableIDs = append(page.TableIDs, item.ID)
		}
		for _, item := range slide.Charts {
			if err := validateBeautifyInventoryChart(slide.SlideIndex, item); err != nil {
				return nil, nil, err
			}
			page.ChartIDs = append(page.ChartIDs, item.ID)
		}
		for _, item := range slide.Images {
			if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.SourceOccurrence) == "" || strings.TrimSpace(item.SourcePath) == "" {
				return nil, nil, fmt.Errorf("beautify inventory slide %d has invalid image", slide.SlideIndex)
			}
			if err := validateBeautifyFileRef(projectPath, BeautifyFileRef{Path: item.SourcePath, SHA256: item.SHA256, Size: item.Size}, "beautify inventory image "+item.ID); err != nil {
				return nil, nil, err
			}
			page.ImageIDs = append(page.ImageIDs, item.ID)
		}
		for label, ids := range map[string][]string{"text": page.TextIDs, "table": page.TableIDs, "chart": page.ChartIDs, "image": page.ImageIDs} {
			if err := beautifyRequireUniqueIDs(ids, fmt.Sprintf("slide %d %s ids", slide.SlideIndex, label)); err != nil {
				return nil, nil, err
			}
			for _, id := range ids {
				if allIDs[id] {
					return nil, nil, fmt.Errorf("beautify inventory id %q is not globally unique", id)
				}
				allIDs[id] = true
			}
		}
		for _, decision := range append(append([]BeautifyContentRef{}, slide.Ignored...), slide.NeedsConfirmation...) {
			if strings.TrimSpace(decision.ID) == "" || strings.TrimSpace(decision.Reason) == "" {
				return nil, nil, fmt.Errorf("beautify inventory slide %d has invalid ignored/confirmation decision", slide.SlideIndex)
			}
		}
		page.TextIDs, _ = beautifySortedUnique(page.TextIDs)
		page.TableIDs, _ = beautifySortedUnique(page.TableIDs)
		page.ChartIDs, _ = beautifySortedUnique(page.ChartIDs)
		page.ImageIDs, _ = beautifySortedUnique(page.ImageIDs)
		page.SHA256, _ = beautifyJSONSHA256(struct {
			Slide BeautifyInventorySlide `json:"slide"`
		}{Slide: slide})
		pages = append(pages, page)
	}
	return pages, allIDs, nil
}

func validateBeautifyInventoryImageFiles(projectPath string, slides []BeautifyInventorySlide) error {
	for _, slide := range slides {
		for _, image := range slide.Images {
			if err := validateBeautifyFileRef(projectPath, BeautifyFileRef{Path: image.SourcePath, SHA256: image.SHA256, Size: image.Size}, "beautify inventory image "+image.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBeautifyInventoryTable(slide int, table BeautifyInventoryTable) error {
	if table.ID == "" || table.RowCount <= 0 || table.ColCount <= 0 || len(table.Cells) != table.RowCount {
		return fmt.Errorf("beautify inventory slide %d has invalid table %q dimensions", slide, table.ID)
	}
	for _, row := range table.Cells {
		if len(row) != table.ColCount {
			return fmt.Errorf("beautify inventory table %s cell grid is ragged", table.ID)
		}
	}
	return nil
}

func validateBeautifyInventoryChart(slide int, chart BeautifyInventoryChart) error {
	if chart.ID == "" || chart.Type == "" || len(chart.Series) == 0 {
		return fmt.Errorf("beautify inventory slide %d has invalid chart %q", slide, chart.ID)
	}
	seriesNames := make([]string, 0, len(chart.Series))
	for _, series := range chart.Series {
		seriesNames = append(seriesNames, series.Name)
		if len(series.Values) != len(chart.Categories) {
			return fmt.Errorf("beautify inventory chart %s series %q value count mismatch", chart.ID, series.Name)
		}
		for _, value := range series.Values {
			switch typed := value.(type) {
			case string:
			case float64:
				if math.IsNaN(typed) || math.IsInf(typed, 0) {
					return fmt.Errorf("beautify inventory chart %s has non-finite data", chart.ID)
				}
			default:
				return fmt.Errorf("beautify inventory chart %s has unstable data type", chart.ID)
			}
		}
	}
	return beautifyRequireUniqueIDs(seriesNames, "chart "+chart.ID+" series")
}

func validateBeautifyRisks(risks []BeautifyRiskFinding, slideCount int, itemIDs map[string]bool) ([]string, error) {
	ids := make([]string, 0, len(risks))
	for _, risk := range risks {
		if risk.ID == "" || risk.Rule == "" || risk.Message == "" || risk.SlideIndex < 1 || risk.SlideIndex > slideCount {
			return nil, fmt.Errorf("beautify risk report contains invalid risk %q", risk.ID)
		}
		if risk.Severity != "info" && risk.Severity != "warning" && risk.Severity != "error" {
			return nil, fmt.Errorf("beautify risk %s has invalid severity %q", risk.ID, risk.Severity)
		}
		for _, itemID := range risk.ItemIDs {
			if !itemIDs[itemID] {
				return nil, fmt.Errorf("beautify risk %s references unknown item %q", risk.ID, itemID)
			}
		}
		ids = append(ids, risk.ID)
	}
	if err := beautifyRequireUniqueIDs(ids, "beautify risk ids"); err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

func beautifyFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
