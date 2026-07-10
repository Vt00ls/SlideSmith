package service

import (
	"path/filepath"
	"strings"
)

type SourceKind string

const (
	SourceKindMarkdown     SourceKind = "markdown"
	SourceKindText         SourceKind = "text"
	SourceKindTableText    SourceKind = "table_text"
	SourceKindPDF          SourceKind = "pdf"
	SourceKindDocument     SourceKind = "document"
	SourceKindExcel        SourceKind = "excel"
	SourceKindExcelLegacy  SourceKind = "excel_legacy"
	SourceKindPresentation SourceKind = "presentation"
	SourceKindUnsupported  SourceKind = "unsupported"
)

type SourceKindInfo struct {
	Kind         SourceKind `json:"source_kind"`
	Extension    string     `json:"extension"`
	Supported    bool       `json:"supported"`
	Message      string     `json:"message,omitempty"`
	Markdown     bool       `json:"markdown"`
	PPTXAnalysis bool       `json:"pptx_analysis"`
}

func DetectSourceKind(filename string) SourceKindInfo {
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(filename))), ".")
	info := SourceKindInfo{
		Kind:      SourceKindUnsupported,
		Extension: extension,
	}

	switch extension {
	case "md", "markdown":
		info.Kind = SourceKindMarkdown
		info.Supported = true
		info.Markdown = true
	case "txt", "text":
		info.Kind = SourceKindText
		info.Supported = true
		info.Markdown = true
	case "csv", "tsv":
		info.Kind = SourceKindTableText
		info.Supported = true
		info.Markdown = true
	case "pdf":
		info.Kind = SourceKindPDF
		info.Supported = true
		info.Markdown = true
	case "docx", "doc", "odt", "rtf", "epub", "html", "htm", "tex", "latex", "rst", "org", "ipynb", "typ":
		info.Kind = SourceKindDocument
		info.Supported = true
		info.Markdown = true
	case "xlsx", "xlsm":
		info.Kind = SourceKindExcel
		info.Supported = true
		info.Markdown = true
	case "xls":
		info.Kind = SourceKindExcelLegacy
		info.Supported = true
		info.Message = "legacy .xls is archived only; resave as .xlsx for automatic Markdown conversion"
	case "pptx", "pptm", "ppsx", "ppsm", "potx", "potm":
		info.Kind = SourceKindPresentation
		info.Supported = true
		info.Markdown = true
		info.PPTXAnalysis = true
	case "ppt":
		info.Message = "legacy .ppt is not supported; resave as .pptx before upload"
	case "png", "jpg", "jpeg", "gif", "bmp", "webp", "svg", "tif", "tiff", "heic", "heif", "avif", "ico":
		info.Message = "image files are not supported as source input; upload a supported document or presentation instead"
	case "zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "tbz2", "xz", "txz", "zst":
		info.Message = "archive files are not supported as source input; extract and upload a supported file instead"
	case "":
		info.Message = "source file must have a supported extension"
	default:
		info.Message = "unsupported source file extension ." + extension
	}

	return info
}

func SourceArtifactMetadata(info SourceKindInfo) map[string]any {
	metadata := map[string]any{
		"schema":      "slidesmith.source_artifact_metadata.v1",
		"source_kind": info.Kind,
		"extension":   info.Extension,
		"supported":   info.Supported,
		"intake": map[string]any{
			"markdown":      info.Markdown,
			"pptx_analysis": info.PPTXAnalysis,
		},
	}
	if info.Message != "" {
		metadata["message"] = info.Message
	}
	return metadata
}
