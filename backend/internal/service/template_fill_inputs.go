package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type TemplateFillInputs struct {
	ProjectPath    string   `json:"project_path"`
	SourcePPTX     string   `json:"source_pptx"`
	SlideLibrary   string   `json:"slide_library"`
	FillPlan       string   `json:"fill_plan"`
	CheckReport    string   `json:"check_report"`
	ValidateReport string   `json:"validate_report"`
	Readback       string   `json:"readback"`
	ExportBase     string   `json:"export_base"`
	ContentSources []string `json:"content_sources"`
}

type templateFillPresentationInput struct {
	name         string
	relativePath string
	absolutePath string
	extension    string
}

type templateFillManifestPath struct {
	permittedRoot string
	path          string
}

func discoverTemplateFillInputs(projectPath string) (TemplateFillInputs, error) {
	projectPath, err := resolveTemplateFillProjectPath(projectPath)
	if err != nil {
		return TemplateFillInputs{}, err
	}

	sourcesPath := filepath.Join(projectPath, "sources")
	sourcesInfo, resolvedSourcesPath, err := inspectContainedPath(projectPath, sourcesPath)
	if err != nil {
		return TemplateFillInputs{}, fmt.Errorf("inspect template fill sources directory: %w", err)
	}
	if !sourcesInfo.IsDir() {
		return TemplateFillInputs{}, fmt.Errorf("template fill requires sources directory: %s", filepath.ToSlash(filepath.Join("sources")))
	}
	entries, err := os.ReadDir(resolvedSourcesPath)
	if err != nil {
		return TemplateFillInputs{}, fmt.Errorf("read template fill sources directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	presentations := make([]templateFillPresentationInput, 0)
	for _, entry := range entries {
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if _, ok := pptxDeckExtensions[extension]; !ok {
			continue
		}
		relativePath := filepath.ToSlash(filepath.Join("sources", entry.Name()))
		absolutePath := filepath.Join(sourcesPath, entry.Name())
		info, _, err := inspectContainedPath(projectPath, absolutePath)
		if err != nil {
			return TemplateFillInputs{}, fmt.Errorf("template fill presentation input must be regular and non-symlinked: %s: %w", relativePath, err)
		}
		if !info.Mode().IsRegular() {
			return TemplateFillInputs{}, fmt.Errorf("template fill presentation input must be a regular file: %s", relativePath)
		}
		presentations = append(presentations, templateFillPresentationInput{
			name:         entry.Name(),
			relativePath: relativePath,
			absolutePath: absolutePath,
			extension:    extension,
		})
	}
	sort.Slice(presentations, func(i, j int) bool {
		return presentations[i].relativePath < presentations[j].relativePath
	})
	if len(presentations) != 1 || presentations[0].extension != ".pptx" {
		paths := make([]string, 0, len(presentations))
		for _, presentation := range presentations {
			paths = append(paths, presentation.relativePath)
		}
		message := fmt.Sprintf("template fill requires exactly one source PPTX, found %d presentation files", len(presentations))
		if len(paths) > 0 {
			message += ": " + strings.Join(paths, ", ")
		}
		return TemplateFillInputs{}, fmt.Errorf("%s", message)
	}

	presentation := presentations[0]
	stem := strings.TrimSuffix(presentation.name, filepath.Ext(presentation.name))
	slideLibraryRelative := filepath.ToSlash(filepath.Join("analysis", stem+".slide_library.json"))
	slideLibraryPath := filepath.Join(projectPath, filepath.FromSlash(slideLibraryRelative))
	slideLibraryInfo, _, err := inspectContainedPath(projectPath, slideLibraryPath)
	if err != nil {
		return TemplateFillInputs{}, fmt.Errorf("template fill requires slide library: %s: %w", slideLibraryRelative, err)
	}
	if !slideLibraryInfo.Mode().IsRegular() || slideLibraryInfo.Size() == 0 {
		return TemplateFillInputs{}, fmt.Errorf("template fill requires slide library: %s must be a non-empty regular file", slideLibraryRelative)
	}

	explicitSameStemMarkdown, err := templateFillHasExplicitSameStemMarkdown(projectPath, stem)
	if err != nil {
		return TemplateFillInputs{}, err
	}
	contentSources := make([]string, 0)
	for _, entry := range entries {
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if !isReadableSourceExtension(extension) {
			continue
		}
		entryStem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if extension == ".md" && strings.EqualFold(entryStem, stem) && !explicitSameStemMarkdown {
			continue
		}
		relativePath := filepath.ToSlash(filepath.Join("sources", entry.Name()))
		absolutePath := filepath.Join(sourcesPath, entry.Name())
		info, _, err := inspectContainedPath(projectPath, absolutePath)
		if err != nil {
			return TemplateFillInputs{}, fmt.Errorf("template fill content source must be regular and non-symlinked: %s: %w", relativePath, err)
		}
		if !info.Mode().IsRegular() {
			return TemplateFillInputs{}, fmt.Errorf("template fill content source must be a regular file: %s", relativePath)
		}
		contentSources = append(contentSources, absolutePath)
	}
	sort.Strings(contentSources)
	if len(contentSources) == 0 {
		return TemplateFillInputs{}, fmt.Errorf("template fill requires content source beside template PPTX")
	}

	inputs := TemplateFillInputs{
		ProjectPath:    projectPath,
		SourcePPTX:     presentation.absolutePath,
		SlideLibrary:   slideLibraryPath,
		FillPlan:       filepath.Join(projectPath, "analysis", "fill_plan.json"),
		CheckReport:    filepath.Join(projectPath, "analysis", "check_report.json"),
		ValidateReport: filepath.Join(projectPath, "validation", "validate_report.json"),
		Readback:       filepath.Join(projectPath, "validation", "readback.md"),
		ExportBase:     filepath.Join(projectPath, "exports", filepath.Base(projectPath)+"_template_fill.pptx"),
		ContentSources: contentSources,
	}
	for _, outputPath := range []string{
		inputs.FillPlan,
		inputs.CheckReport,
		inputs.ValidateReport,
		inputs.Readback,
		inputs.ExportBase,
	} {
		if err := validateTemplateFillOutputPath(projectPath, outputPath); err != nil {
			return TemplateFillInputs{}, err
		}
	}
	return inputs, nil
}

func validateTemplateFillOutputPath(projectPath, outputPath string) error {
	relativePath, err := filepath.Rel(projectPath, outputPath)
	if err != nil || filepath.IsAbs(relativePath) || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return fmt.Errorf("template fill output path is outside project: %s", outputPath)
	}
	relativePath = filepath.ToSlash(relativePath)

	parentInfo, _, err := inspectContainedPath(projectPath, filepath.Dir(outputPath))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("template fill output path must be non-symlinked: %s: %w", relativePath, err)
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("template fill output parent must be a directory: %s", relativePath)
	}

	info, _, err := inspectContainedPath(projectPath, outputPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("template fill output path must be non-symlinked: %s: %w", relativePath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("template fill output path must be a regular file: %s", relativePath)
	}
	return nil
}

func resolveTemplateFillProjectPath(projectPath string) (string, error) {
	absolutePath, err := filepath.Abs(projectPath)
	if err != nil {
		return "", fmt.Errorf("resolve template fill project path: %w", err)
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("inspect template fill project path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("template fill project path must be non-symlinked: %s", absolutePath)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("template fill project path must be a directory: %s", absolutePath)
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", fmt.Errorf("resolve template fill project path: %w", err)
	}
	return filepath.Clean(resolvedPath), nil
}

func templateFillHasExplicitSameStemMarkdown(projectPath, stem string) (bool, error) {
	manifestPaths := []templateFillManifestPath{{
		permittedRoot: projectPath,
		path:          filepath.Join(projectPath, ".slidesmith", "source_inputs.json"),
	}}
	projectsPath := filepath.Dir(projectPath)
	if filepath.Base(projectsPath) == "projects" {
		workspacePath := filepath.Dir(projectsPath)
		manifestPaths = append([]templateFillManifestPath{{
			permittedRoot: workspacePath,
			path:          filepath.Join(workspacePath, ".slidesmith", "source_inputs.json"),
		}}, manifestPaths...)
	}

	for _, manifestPath := range manifestPaths {
		info, resolvedPath, err := inspectContainedPath(manifestPath.permittedRoot, manifestPath.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect template fill source inputs manifest: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("template fill source inputs manifest must be a regular non-symlinked file: %s", manifestPath.path)
		}
		raw, err := os.ReadFile(resolvedPath)
		if err != nil {
			return false, fmt.Errorf("read template fill source inputs manifest: %w", err)
		}
		var manifest sourceInputsManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return false, fmt.Errorf("parse template fill source inputs manifest: %w", err)
		}
		if manifest.Schema != "slidesmith.source_inputs.v1" {
			return false, fmt.Errorf("unsupported template fill source inputs manifest schema: %q", manifest.Schema)
		}
		for _, file := range manifest.Files {
			if templateFillManifestNameMatchesMarkdownStem(file.Name, stem) ||
				templateFillManifestNameMatchesMarkdownStem(filepath.Base(filepath.FromSlash(file.UploadPath)), stem) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}

func templateFillManifestNameMatchesMarkdownStem(name, stem string) bool {
	name = strings.TrimSpace(name)
	extension := strings.ToLower(filepath.Ext(name))
	if extension != ".md" {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), stem)
}
