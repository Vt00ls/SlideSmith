package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path"
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
	projectLocal  bool
}

func discoverTemplateFillInputs(projectPath string) (TemplateFillInputs, error) {
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return TemplateFillInputs{}, err
	}
	return discoverTemplateFillInputsWithProvenance(projectPath, provenance)
}

type templateFillProvenanceSource struct {
	relativePath string
	size         int64
	sha256       string
}

type templateFillSourceProvenance struct {
	canonicalProject     string
	manifestPath         string
	manifestSHA256       string
	manifestProjectLocal bool
	projectLocalManifest bool
	sources              map[string]templateFillProvenanceSource
	authorizedSources    map[string]struct{}
}

func discoverTemplateFillInputsWithProvenance(projectPath string, provenance templateFillSourceProvenance) (TemplateFillInputs, error) {
	projectPath, err := resolveTemplateFillProjectPath(projectPath)
	if err != nil {
		return TemplateFillInputs{}, err
	}
	if err := provenance.validateCandidate(projectPath); err != nil {
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

	contentSources := make([]string, 0)
	for _, entry := range entries {
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if !isReadableSourceExtension(extension) {
			continue
		}
		entryStem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		relativePath := filepath.ToSlash(filepath.Join("sources", entry.Name()))
		if extension == ".md" && strings.EqualFold(entryStem, stem) && !provenance.hasBoundSource(relativePath) {
			continue
		}
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

func snapshotTemplateFillSourceProvenance(projectPath string) (templateFillSourceProvenance, error) {
	projectPath, err := resolveTemplateFillProjectPath(projectPath)
	if err != nil {
		return templateFillSourceProvenance{}, err
	}
	provenance := templateFillSourceProvenance{
		canonicalProject:  projectPath,
		sources:           map[string]templateFillProvenanceSource{},
		authorizedSources: map[string]struct{}{},
	}
	manifestPaths := templateFillSourceManifestPaths(projectPath)
	for _, manifestPath := range manifestPaths {
		if !manifestPath.projectLocal {
			continue
		}
		info, _, err := inspectContainedPath(manifestPath.permittedRoot, manifestPath.path)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return templateFillSourceProvenance{}, fmt.Errorf("inspect project-local template fill source inputs manifest: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return templateFillSourceProvenance{}, fmt.Errorf("project-local template fill source inputs manifest must be a regular non-symlinked file: %s", manifestPath.path)
		}
		provenance.projectLocalManifest = true
		break
	}
	for _, manifestPath := range manifestPaths {
		info, resolvedPath, err := inspectContainedPath(manifestPath.permittedRoot, manifestPath.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return templateFillSourceProvenance{}, fmt.Errorf("inspect template fill source inputs manifest: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return templateFillSourceProvenance{}, fmt.Errorf("template fill source inputs manifest must be a regular non-symlinked file: %s", manifestPath.path)
		}
		raw, err := os.ReadFile(resolvedPath)
		if err != nil {
			return templateFillSourceProvenance{}, fmt.Errorf("read template fill source inputs manifest: %w", err)
		}
		var manifest sourceInputsManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return templateFillSourceProvenance{}, fmt.Errorf("parse template fill source inputs manifest: %w", err)
		}
		if manifest.Schema != "slidesmith.source_inputs.v1" {
			return templateFillSourceProvenance{}, fmt.Errorf("unsupported template fill source inputs manifest schema: %q", manifest.Schema)
		}
		claimedNames := make(map[string]struct{}, len(manifest.Files))
		for index, file := range manifest.Files {
			name, claimed, err := templateFillManifestSourceName(file, index)
			if err != nil {
				return templateFillSourceProvenance{}, err
			}
			if !claimed {
				continue
			}
			if _, duplicate := claimedNames[name]; duplicate {
				return templateFillSourceProvenance{}, fmt.Errorf("template fill source inputs manifest has duplicate claim for %q", name)
			}
			for existing := range claimedNames {
				if strings.EqualFold(existing, name) {
					return templateFillSourceProvenance{}, fmt.Errorf("template fill source inputs manifest has case-fold-colliding claims %q and %q", existing, name)
				}
			}
			claimedNames[name] = struct{}{}
		}
		provenance.manifestPath = resolvedPath
		provenance.manifestSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
		provenance.manifestProjectLocal = manifestPath.projectLocal
		if err := provenance.bindCanonicalSources(projectPath, claimedNames); err != nil {
			return templateFillSourceProvenance{}, err
		}
		return provenance, nil
	}
	if err := provenance.bindCanonicalSources(projectPath, nil); err != nil {
		return templateFillSourceProvenance{}, err
	}
	return provenance, nil
}

func templateFillSourceManifestPaths(projectPath string) []templateFillManifestPath {
	manifestPaths := []templateFillManifestPath{{
		permittedRoot: projectPath,
		path:          filepath.Join(projectPath, ".slidesmith", "source_inputs.json"),
		projectLocal:  true,
	}}
	projectsPath := filepath.Dir(projectPath)
	if filepath.Base(projectsPath) == "projects" {
		workspacePath := filepath.Dir(projectsPath)
		manifestPaths = append([]templateFillManifestPath{{
			permittedRoot: workspacePath,
			path:          filepath.Join(workspacePath, ".slidesmith", "source_inputs.json"),
		}}, manifestPaths...)
	}
	return manifestPaths
}

func templateFillManifestSourceName(file sourceInputsManifestFile, index int) (string, bool, error) {
	names := make([]string, 0, 2)
	if name := strings.TrimSpace(file.Name); name != "" {
		normalized := strings.ReplaceAll(name, "\\", "/")
		if normalized != path.Base(normalized) || normalized == "." || normalized == ".." {
			return "", false, fmt.Errorf("template fill source inputs manifest files[%d].name must be a filename", index)
		}
		names = append(names, normalized)
	}
	if uploadPath := strings.TrimSpace(file.UploadPath); uploadPath != "" {
		normalized := strings.ReplaceAll(uploadPath, "\\", "/")
		cleaned := path.Clean(normalized)
		if path.IsAbs(normalized) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return "", false, fmt.Errorf("template fill source inputs manifest files[%d].upload_path is outside workspace", index)
		}
		name := path.Base(cleaned)
		if name != "." && name != ".." && name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", false, nil
	}
	if len(names) == 2 && names[0] != names[1] {
		return "", false, fmt.Errorf(
			"template fill source inputs manifest files[%d] ambiguously authorizes %q and %q",
			index,
			names[0],
			names[1],
		)
	}
	return names[0], true, nil
}

func (provenance *templateFillSourceProvenance) bindCanonicalSources(projectPath string, claimedNames map[string]struct{}) error {
	sourcesPath := filepath.Join(projectPath, "sources")
	info, resolvedSourcesPath, err := inspectContainedPath(projectPath, sourcesPath)
	if err != nil {
		return fmt.Errorf("inspect template fill provenance sources directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("template fill provenance sources path must be a directory: %s", sourcesPath)
	}
	entries, err := os.ReadDir(resolvedSourcesPath)
	if err != nil {
		return fmt.Errorf("read template fill provenance sources directory: %w", err)
	}
	for _, entry := range entries {
		relativePath := filepath.ToSlash(filepath.Join("sources", entry.Name()))
		fingerprint, err := templateFillFingerprintSource(projectPath, relativePath)
		if err != nil {
			return err
		}
		provenance.sources[relativePath] = fingerprint
		if _, authorized := claimedNames[entry.Name()]; authorized {
			provenance.authorizedSources[relativePath] = struct{}{}
		}
	}
	return nil
}

func templateFillFingerprintSource(projectPath, relativePath string) (templateFillProvenanceSource, error) {
	absolutePath := filepath.Join(projectPath, filepath.FromSlash(relativePath))
	info, resolvedPath, err := inspectContainedPath(projectPath, absolutePath)
	if err != nil {
		return templateFillProvenanceSource{}, fmt.Errorf("inspect template fill provenance source %s: %w", relativePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return templateFillProvenanceSource{}, fmt.Errorf("template fill provenance source must be a regular non-symlinked file: %s", relativePath)
	}
	digest, err := sha256File(resolvedPath)
	if err != nil {
		return templateFillProvenanceSource{}, fmt.Errorf("hash template fill provenance source %s: %w", relativePath, err)
	}
	return templateFillProvenanceSource{relativePath: relativePath, size: info.Size(), sha256: digest}, nil
}

func (provenance templateFillSourceProvenance) hasBoundSource(relativePath string) bool {
	_, ok := provenance.authorizedSources[filepath.ToSlash(relativePath)]
	return ok
}

func (provenance templateFillSourceProvenance) validateCandidate(projectPath string) error {
	if provenance.canonicalProject == "" {
		return fmt.Errorf("template fill source provenance snapshot is required")
	}
	projectManifestPath := filepath.Join(projectPath, ".slidesmith", "source_inputs.json")
	info, resolvedManifestPath, err := inspectContainedPath(projectPath, projectManifestPath)
	switch {
	case provenance.manifestProjectLocal:
		if err != nil {
			return fmt.Errorf("inspect candidate template fill source inputs manifest: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("candidate template fill source inputs manifest must be a regular non-symlinked file: %s", projectManifestPath)
		}
		raw, err := os.ReadFile(resolvedManifestPath)
		if err != nil {
			return fmt.Errorf("read candidate template fill source inputs manifest: %w", err)
		}
		if digest := fmt.Sprintf("%x", sha256.Sum256(raw)); digest != provenance.manifestSHA256 {
			return fmt.Errorf("candidate template fill source inputs manifest changed from authoritative provenance")
		}
	case err == nil:
		return fmt.Errorf("candidate template fill source inputs manifest is not authoritative: %s", projectManifestPath)
	case !os.IsNotExist(err):
		return fmt.Errorf("inspect candidate template fill source inputs manifest: %w", err)
	}
	candidate := templateFillSourceProvenance{sources: map[string]templateFillProvenanceSource{}}
	if err := candidate.bindCanonicalSources(projectPath, nil); err != nil {
		return fmt.Errorf("inspect template fill candidate source inventory: %w", err)
	}
	for relativePath := range provenance.sources {
		if _, ok := candidate.sources[relativePath]; !ok {
			return fmt.Errorf("template fill candidate source inventory is missing authoritative source: %s", relativePath)
		}
	}
	for relativePath := range candidate.sources {
		if _, ok := provenance.sources[relativePath]; !ok {
			return fmt.Errorf("template fill candidate source inventory has unexpected source: %s", relativePath)
		}
	}
	for relativePath, expected := range provenance.sources {
		actual := candidate.sources[relativePath]
		if actual.size != expected.size || actual.sha256 != expected.sha256 {
			return fmt.Errorf("template fill candidate source changed from authoritative provenance: %s", relativePath)
		}
	}
	return nil
}

func (provenance templateFillSourceProvenance) revalidateAuthoritative() error {
	current, err := snapshotTemplateFillSourceProvenance(provenance.canonicalProject)
	if err != nil {
		return err
	}
	if current.manifestPath != provenance.manifestPath ||
		current.manifestSHA256 != provenance.manifestSHA256 ||
		current.manifestProjectLocal != provenance.manifestProjectLocal ||
		current.projectLocalManifest != provenance.projectLocalManifest ||
		len(current.sources) != len(provenance.sources) ||
		len(current.authorizedSources) != len(provenance.authorizedSources) {
		return fmt.Errorf("template fill source provenance changed during operation")
	}
	for key, expected := range provenance.sources {
		actual, ok := current.sources[key]
		if !ok || actual.relativePath != expected.relativePath || actual.size != expected.size || actual.sha256 != expected.sha256 {
			return fmt.Errorf("template fill source provenance changed during operation: %s", expected.relativePath)
		}
	}
	for relativePath := range provenance.authorizedSources {
		if _, ok := current.authorizedSources[relativePath]; !ok {
			return fmt.Errorf("template fill source authorization changed during operation: %s", relativePath)
		}
	}
	return nil
}
