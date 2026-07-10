package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDetectSourceKindCoversSupportedExtensionFamilies(t *testing.T) {
	tests := []struct {
		extension    string
		wantKind     SourceKind
		wantMarkdown bool
		wantPPTX     bool
	}{
		{extension: "md", wantKind: SourceKindMarkdown, wantMarkdown: true},
		{extension: "markdown", wantKind: SourceKindMarkdown, wantMarkdown: true},
		{extension: "txt", wantKind: SourceKindText, wantMarkdown: true},
		{extension: "text", wantKind: SourceKindText, wantMarkdown: true},
		{extension: "csv", wantKind: SourceKindTableText, wantMarkdown: true},
		{extension: "tsv", wantKind: SourceKindTableText, wantMarkdown: true},
		{extension: "pdf", wantKind: SourceKindPDF, wantMarkdown: true},
		{extension: "docx", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "doc", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "odt", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "rtf", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "epub", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "html", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "htm", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "tex", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "latex", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "rst", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "org", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "ipynb", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "typ", wantKind: SourceKindDocument, wantMarkdown: true},
		{extension: "xlsx", wantKind: SourceKindExcel, wantMarkdown: true},
		{extension: "xlsm", wantKind: SourceKindExcel, wantMarkdown: true},
		{extension: "pptx", wantKind: SourceKindPresentation, wantMarkdown: true, wantPPTX: true},
		{extension: "pptm", wantKind: SourceKindPresentation, wantMarkdown: true, wantPPTX: true},
		{extension: "ppsx", wantKind: SourceKindPresentation, wantMarkdown: true, wantPPTX: true},
		{extension: "ppsm", wantKind: SourceKindPresentation, wantMarkdown: true, wantPPTX: true},
		{extension: "potx", wantKind: SourceKindPresentation, wantMarkdown: true, wantPPTX: true},
		{extension: "potm", wantKind: SourceKindPresentation, wantMarkdown: true, wantPPTX: true},
	}

	for _, test := range tests {
		t.Run(test.extension, func(t *testing.T) {
			info := DetectSourceKind("source." + test.extension)
			if info.Kind != test.wantKind {
				t.Fatalf("Kind = %q, want %q", info.Kind, test.wantKind)
			}
			if info.Extension != test.extension {
				t.Fatalf("Extension = %q, want %q", info.Extension, test.extension)
			}
			if !info.Supported {
				t.Fatalf("Supported = false, want true: %#v", info)
			}
			if info.Markdown != test.wantMarkdown {
				t.Fatalf("Markdown = %v, want %v", info.Markdown, test.wantMarkdown)
			}
			if info.PPTXAnalysis != test.wantPPTX {
				t.Fatalf("PPTXAnalysis = %v, want %v", info.PPTXAnalysis, test.wantPPTX)
			}
			if info.Message != "" {
				t.Fatalf("Message = %q, want empty", info.Message)
			}
		})
	}
}

func TestDetectSourceKindIsCaseInsensitive(t *testing.T) {
	info := DetectSourceKind("Quarterly.Report.PPTX")
	if info.Kind != SourceKindPresentation || info.Extension != "pptx" || !info.Supported || !info.Markdown || !info.PPTXAnalysis {
		t.Fatalf("DetectSourceKind() = %#v, want supported presentation with normalized extension", info)
	}
}

func TestDetectSourceKindHandlesLegacyOfficeFormats(t *testing.T) {
	xls := DetectSourceKind("ledger.xls")
	if xls.Kind != SourceKindExcelLegacy || !xls.Supported || xls.Markdown || xls.PPTXAnalysis {
		t.Fatalf("legacy XLS = %#v, want archive-only supported input", xls)
	}
	const wantXLSMessage = "legacy .xls is archived only; resave as .xlsx for automatic Markdown conversion"
	if xls.Message != wantXLSMessage {
		t.Fatalf("legacy XLS message = %q, want %q", xls.Message, wantXLSMessage)
	}

	ppt := DetectSourceKind("slides.ppt")
	if ppt.Kind != SourceKindUnsupported || ppt.Supported || ppt.Markdown || ppt.PPTXAnalysis {
		t.Fatalf("legacy PPT = %#v, want unsupported", ppt)
	}
	if !strings.Contains(strings.ToLower(ppt.Message), ".pptx") {
		t.Fatalf("legacy PPT message = %q, want resave-as-pptx guidance", ppt.Message)
	}
}

func TestDetectSourceKindRejectsImagesArchivesAndUnknownExtensions(t *testing.T) {
	for _, filename := range []string{
		"photo.PNG",
		"scan.jpeg",
		"assets.zip",
		"backup.tar",
		"source.unknown",
		"README",
		"source.",
		"",
	} {
		t.Run(filename, func(t *testing.T) {
			info := DetectSourceKind(filename)
			if info.Kind != SourceKindUnsupported || info.Supported || info.Markdown || info.PPTXAnalysis {
				t.Fatalf("DetectSourceKind(%q) = %#v, want unsupported", filename, info)
			}
			if strings.TrimSpace(info.Message) == "" {
				t.Fatalf("DetectSourceKind(%q) returned an empty user-facing message", filename)
			}
		})
	}
}

func TestDetectSourceKindBuildsArtifactMetadata(t *testing.T) {
	metadata := SourceArtifactMetadata(DetectSourceKind("deck.PPTX"))
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded struct {
		Schema     string     `json:"schema"`
		SourceKind SourceKind `json:"source_kind"`
		Extension  string     `json:"extension"`
		Supported  bool       `json:"supported"`
		Message    *string    `json:"message"`
		Intake     struct {
			Markdown     bool `json:"markdown"`
			PPTXAnalysis bool `json:"pptx_analysis"`
		} `json:"intake"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.Schema != "slidesmith.source_artifact_metadata.v1" || decoded.SourceKind != SourceKindPresentation || decoded.Extension != "pptx" || !decoded.Supported {
		t.Fatalf("metadata identity = %#v", decoded)
	}
	if !decoded.Intake.Markdown || !decoded.Intake.PPTXAnalysis {
		t.Fatalf("metadata intake = %#v, want markdown and pptx analysis", decoded.Intake)
	}
	if decoded.Message != nil {
		t.Fatalf("metadata message = %q, want omitted", *decoded.Message)
	}

	legacy := SourceArtifactMetadata(DetectSourceKind("ledger.xls"))
	if legacy["message"] != "legacy .xls is archived only; resave as .xlsx for automatic Markdown conversion" {
		t.Fatalf("legacy metadata message = %#v", legacy["message"])
	}
}
