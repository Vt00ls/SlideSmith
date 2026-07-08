package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
)

var ErrInvalidTemplateAssetPath = errors.New("invalid template asset path")

type TemplateCatalogService struct {
	repo              *repository.Repository
	pptMasterSkillDir string
}

type TemplateCatalogItem struct {
	ID               string                `json:"id"`
	Kind             string                `json:"kind"`
	Name             string                `json:"name"`
	DisplayName      string                `json:"display_name"`
	Version          string                `json:"version,omitempty"`
	Status           string                `json:"status,omitempty"`
	Summary          string                `json:"summary,omitempty"`
	Canvas           string                `json:"canvas,omitempty"`
	DefaultPageCount int                   `json:"default_page_count,omitempty"`
	PageTypes        []string              `json:"page_types,omitempty"`
	PrimaryColor     string                `json:"primary_color,omitempty"`
	PreviewAssets    []TemplatePreview     `json:"preview_assets,omitempty"`
	TemplatePath     string                `json:"template_path,omitempty"`
	DesignSpecPath   string                `json:"design_spec_path,omitempty"`
	Checksum         string                `json:"checksum,omitempty"`
	Compatibility    TemplateCompatibility `json:"compatibility,omitempty"`
}

type TemplatePreview struct {
	Name string `json:"name"`
	Path string `json:"path"`
	URL  string `json:"url"`
}

type TemplateCompatibility map[string][]string

type TemplateLock struct {
	SchemaVersion    int                   `json:"schema_version"`
	TemplateID       string                `json:"template_id"`
	Kind             string                `json:"kind"`
	Name             string                `json:"name"`
	DisplayName      string                `json:"display_name"`
	Version          string                `json:"version"`
	SourcePath       string                `json:"source_path"`
	Checksum         string                `json:"checksum"`
	Canvas           string                `json:"canvas,omitempty"`
	DefaultPageCount int                   `json:"default_page_count,omitempty"`
	PageTypes        []string              `json:"page_types,omitempty"`
	PrimaryColor     string                `json:"primary_color,omitempty"`
	PreviewAssets    []TemplatePreview     `json:"preview_assets,omitempty"`
	Compatibility    TemplateCompatibility `json:"compatibility,omitempty"`
	LockedAt         string                `json:"locked_at"`
}

type templateIndexEntry struct {
	Summary      string   `json:"summary"`
	Canvas       string   `json:"canvas_format"`
	PageCount    int      `json:"page_count"`
	PageTypes    []string `json:"page_types"`
	PrimaryColor string   `json:"primary_color"`
}

type templateCatalogSource struct {
	kind      string
	groupPath string
	indexFile string
}

func NewTemplateCatalogService(pptMasterSkillDir string) *TemplateCatalogService {
	return NewTemplateCatalogServiceWithRepository(nil, pptMasterSkillDir)
}

func NewTemplateCatalogServiceWithRepository(repo *repository.Repository, pptMasterSkillDir string) *TemplateCatalogService {
	return &TemplateCatalogService{
		repo:              repo,
		pptMasterSkillDir: strings.TrimSpace(pptMasterSkillDir),
	}
}

func (s *TemplateCatalogService) SyncFromDisk(ctx context.Context) (int, error) {
	if s.repo == nil {
		return 0, nil
	}
	root, err := s.templatesRoot()
	if err != nil {
		return 0, err
	}
	items, err := s.loadTemplatesFromRoot(ctx, root)
	if err != nil {
		return 0, err
	}
	entries := make([]model.TemplateRegistryEntry, 0, len(items))
	activeIDs := make([]string, 0, len(items))
	for _, item := range items {
		checksum, err := sha256Dir(item.TemplatePath)
		if err != nil {
			return 0, fmt.Errorf("checksum template %s: %w", item.ID, err)
		}
		pageTypesJSON, err := marshalTemplateCatalogJSON(item.PageTypes, "[]")
		if err != nil {
			return 0, err
		}
		previewAssetsJSON, err := marshalTemplateCatalogJSON(item.PreviewAssets, "[]")
		if err != nil {
			return 0, err
		}
		compatibilityJSON, err := marshalTemplateCatalogJSON(defaultTemplateCompatibility(item), "{}")
		if err != nil {
			return 0, err
		}
		entries = append(entries, model.TemplateRegistryEntry{
			ID:                item.ID,
			Kind:              item.Kind,
			Name:              item.Name,
			DisplayName:       item.DisplayName,
			Version:           "workspace",
			Status:            model.TemplateStatusActive,
			Summary:           item.Summary,
			Canvas:            item.Canvas,
			DefaultPageCount:  item.DefaultPageCount,
			PageTypesJSON:     pageTypesJSON,
			PrimaryColor:      item.PrimaryColor,
			PreviewAssetsJSON: previewAssetsJSON,
			TemplatePath:      item.TemplatePath,
			DesignSpecPath:    item.DesignSpecPath,
			Checksum:          "sha256:" + checksum,
			CompatibilityJSON: compatibilityJSON,
		})
		activeIDs = append(activeIDs, item.ID)
	}
	if err := s.repo.UpsertTemplateRegistryEntries(ctx, entries); err != nil {
		return 0, err
	}
	if err := s.repo.DisableTemplateRegistryEntriesMissingFromDisk(ctx, root, activeIDs); err != nil {
		return 0, err
	}
	return len(entries), nil
}

func (s *TemplateCatalogService) ListTemplates(ctx context.Context) ([]TemplateCatalogItem, error) {
	if s.repo != nil {
		entries, err := s.repo.ListTemplateRegistryEntries(ctx, nil)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return templateItemsFromRegistryEntries(entries, true)
		}
	}
	return s.listTemplatesFromDisk(ctx)
}

func (s *TemplateCatalogService) listTemplatesFromDisk(ctx context.Context) ([]TemplateCatalogItem, error) {
	root, err := s.templatesRoot()
	if err != nil {
		return nil, err
	}
	return s.loadTemplatesFromRoot(ctx, root)
}

func (s *TemplateCatalogService) loadTemplatesFromRoot(ctx context.Context, root string) ([]TemplateCatalogItem, error) {
	sources := []templateCatalogSource{
		{kind: "layout", groupPath: "layouts", indexFile: "layouts_index.json"},
		{kind: "deck", groupPath: "decks", indexFile: "decks_index.json"},
		{kind: "brand", groupPath: "brands", indexFile: "brands_index.json"},
	}
	items := make([]TemplateCatalogItem, 0)
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		loaded, err := s.loadIndex(ctx, root, source)
		if err != nil {
			return nil, err
		}
		items = append(items, loaded...)
	}
	sortTemplateItems(items)
	return items, nil
}

func (s *TemplateCatalogService) GetTemplate(ctx context.Context, id string) (TemplateCatalogItem, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return TemplateCatalogItem{}, repository.ErrNotFound
	}
	if s.repo != nil {
		entry, err := s.repo.GetTemplateRegistryEntry(ctx, id)
		if err == nil {
			if !templateStatusVisible(entry.Status) {
				return TemplateCatalogItem{}, repository.ErrNotFound
			}
			return templateItemFromRegistryEntry(entry)
		}
		if !errors.Is(err, repository.ErrNotFound) {
			return TemplateCatalogItem{}, err
		}
		entries, err := s.repo.ListTemplateRegistryEntries(ctx, nil)
		if err != nil {
			return TemplateCatalogItem{}, err
		}
		if len(entries) > 0 {
			return TemplateCatalogItem{}, repository.ErrNotFound
		}
	}
	return s.getTemplateFromDisk(ctx, id)
}

func (s *TemplateCatalogService) getTemplateFromDisk(ctx context.Context, id string) (TemplateCatalogItem, error) {
	items, err := s.listTemplatesFromDisk(ctx)
	if err != nil {
		return TemplateCatalogItem{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return TemplateCatalogItem{}, repository.ErrNotFound
}

func (s *TemplateCatalogService) BuildTemplateLock(ctx context.Context, id string) (TemplateLock, error) {
	item, err := s.GetTemplate(ctx, strings.TrimSpace(id))
	if err != nil {
		return TemplateLock{}, err
	}
	if normalizeTemplateStatus(item.Status) == model.TemplateStatusDisabled {
		return TemplateLock{}, repository.ErrNotFound
	}
	version := strings.TrimSpace(item.Version)
	if version == "" {
		version = "workspace"
	}
	checksum := strings.TrimSpace(item.Checksum)
	if checksum == "" {
		rawChecksum, err := sha256Dir(item.TemplatePath)
		if err != nil {
			return TemplateLock{}, err
		}
		checksum = "sha256:" + rawChecksum
	}
	return TemplateLock{
		SchemaVersion:    1,
		TemplateID:       item.ID,
		Kind:             item.Kind,
		Name:             item.Name,
		DisplayName:      item.DisplayName,
		Version:          version,
		SourcePath:       item.TemplatePath,
		Checksum:         checksum,
		Canvas:           item.Canvas,
		DefaultPageCount: item.DefaultPageCount,
		PageTypes:        item.PageTypes,
		PrimaryColor:     item.PrimaryColor,
		PreviewAssets:    item.PreviewAssets,
		Compatibility:    item.Compatibility,
		LockedAt:         time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func (s *TemplateCatalogService) TemplateAssetPath(ctx context.Context, id, assetPath string) (string, string, error) {
	item, err := s.GetTemplate(ctx, id)
	if err != nil {
		return "", "", err
	}
	cleanPath, err := cleanTemplateAssetPath(assetPath)
	if err != nil {
		return "", "", err
	}
	fullPath := filepath.Join(item.TemplatePath, cleanPath)
	info, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		return "", "", repository.ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return "", "", repository.ErrNotFound
	}
	templateRoot, err := filepath.EvalSymlinks(item.TemplatePath)
	if err != nil {
		return "", "", err
	}
	assetRealPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return "", "", err
	}
	if !pathInsideDir(templateRoot, assetRealPath) {
		return "", "", ErrInvalidTemplateAssetPath
	}
	contentType := mime.TypeByExtension(filepath.Ext(assetRealPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return assetRealPath, contentType, nil
}

func (s *TemplateCatalogService) templatesRoot() (string, error) {
	root := strings.TrimSpace(s.pptMasterSkillDir)
	if root == "" {
		return "", fmt.Errorf("SLIDESMITH_PPT_MASTER_SKILL_DIR is not configured")
	}
	if filepath.Base(root) != "templates" {
		root = filepath.Join(root, "templates")
	}
	absRoot, err := filepath.Abs(root)
	if err == nil {
		root = absRoot
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("ppt-master templates root %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("ppt-master templates root is not a directory: %s", root)
	}
	return root, nil
}

func (s *TemplateCatalogService) loadIndex(ctx context.Context, root string, source templateCatalogSource) ([]TemplateCatalogItem, error) {
	indexPath := filepath.Join(root, source.groupPath, source.indexFile)
	raw, err := os.ReadFile(indexPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]templateIndexEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse template index %s: %w", indexPath, err)
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]TemplateCatalogItem, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := entries[name]
		templateDir := filepath.Join(root, source.groupPath, name)
		info, err := os.Stat(templateDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		id := source.kind + ":" + name
		previews, err := templatePreviews(templateDir, id)
		if err != nil {
			return nil, err
		}
		item := TemplateCatalogItem{
			ID:               id,
			Kind:             source.kind,
			Name:             name,
			DisplayName:      name,
			Summary:          entry.Summary,
			Canvas:           entry.Canvas,
			DefaultPageCount: entry.PageCount,
			PageTypes:        entry.PageTypes,
			PrimaryColor:     entry.PrimaryColor,
			PreviewAssets:    previews,
			TemplatePath:     templateDir,
		}
		designSpecPath := filepath.Join(templateDir, "design_spec.md")
		if _, err := os.Stat(designSpecPath); err == nil {
			item.DesignSpecPath = designSpecPath
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func templateItemsFromRegistryEntries(entries []model.TemplateRegistryEntry, visibleOnly bool) ([]TemplateCatalogItem, error) {
	items := make([]TemplateCatalogItem, 0, len(entries))
	for _, entry := range entries {
		if visibleOnly && !templateStatusVisible(entry.Status) {
			continue
		}
		item, err := templateItemFromRegistryEntry(entry)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sortTemplateItems(items)
	return items, nil
}

func templateItemFromRegistryEntry(entry model.TemplateRegistryEntry) (TemplateCatalogItem, error) {
	var pageTypes []string
	if err := unmarshalTemplateCatalogJSON(entry.PageTypesJSON, &pageTypes, "[]"); err != nil {
		return TemplateCatalogItem{}, fmt.Errorf("parse page types for template %s: %w", entry.ID, err)
	}
	var previews []TemplatePreview
	if err := unmarshalTemplateCatalogJSON(entry.PreviewAssetsJSON, &previews, "[]"); err != nil {
		return TemplateCatalogItem{}, fmt.Errorf("parse preview assets for template %s: %w", entry.ID, err)
	}
	compatibility := TemplateCompatibility{}
	if err := unmarshalTemplateCatalogJSON(entry.CompatibilityJSON, &compatibility, "{}"); err != nil {
		return TemplateCatalogItem{}, fmt.Errorf("parse compatibility for template %s: %w", entry.ID, err)
	}
	if len(compatibility) == 0 {
		compatibility = nil
	}
	return TemplateCatalogItem{
		ID:               entry.ID,
		Kind:             entry.Kind,
		Name:             entry.Name,
		DisplayName:      entry.DisplayName,
		Version:          entry.Version,
		Status:           normalizeTemplateStatus(entry.Status),
		Summary:          entry.Summary,
		Canvas:           entry.Canvas,
		DefaultPageCount: entry.DefaultPageCount,
		PageTypes:        pageTypes,
		PrimaryColor:     entry.PrimaryColor,
		PreviewAssets:    previews,
		TemplatePath:     entry.TemplatePath,
		DesignSpecPath:   entry.DesignSpecPath,
		Checksum:         entry.Checksum,
		Compatibility:    compatibility,
	}, nil
}

func marshalTemplateCatalogJSON(value any, fallback string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	encoded := string(raw)
	if encoded == "" || encoded == "null" {
		return fallback, nil
	}
	return encoded, nil
}

func unmarshalTemplateCatalogJSON(raw string, target any, fallback string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = fallback
	}
	return json.Unmarshal([]byte(raw), target)
}

func defaultTemplateCompatibility(item TemplateCatalogItem) TemplateCompatibility {
	compatibility := TemplateCompatibility{
		"runner_profiles": []string{"real-lite", "full-ppt-master"},
	}
	if item.Canvas != "" {
		compatibility["canvas"] = []string{item.Canvas}
	}
	return compatibility
}

func templatePreviews(templateDir, templateID string) ([]TemplatePreview, error) {
	entries, err := os.ReadDir(templateDir)
	if err != nil {
		return nil, err
	}
	previews := make([]TemplatePreview, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isPreviewAsset(name) {
			continue
		}
		previews = append(previews, TemplatePreview{
			Name: previewName(name),
			Path: filepath.ToSlash(name),
			URL:  templateAssetURL(templateID, name),
		})
	}
	sort.Slice(previews, func(i, j int) bool {
		return previews[i].Path < previews[j].Path
	})
	return previews, nil
}

func isPreviewAsset(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".svg", ".png", ".jpg", ".jpeg", ".webp":
		return true
	default:
		return false
	}
}

func previewName(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	parts := strings.SplitN(base, "_", 2)
	if len(parts) == 2 && allDigits(parts[0]) {
		base = parts[1]
	}
	return base
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func templateAssetURL(templateID, assetPath string) string {
	segments := strings.Split(filepath.ToSlash(assetPath), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/api/templates/" + url.PathEscape(templateID) + "/assets/" + strings.Join(segments, "/")
}

func cleanTemplateAssetPath(assetPath string) (string, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(assetPath, "/"))
	if trimmed == "" {
		return "", ErrInvalidTemplateAssetPath
	}
	if filepath.IsAbs(trimmed) {
		return "", ErrInvalidTemplateAssetPath
	}
	cleaned := filepath.Clean(filepath.FromSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", ErrInvalidTemplateAssetPath
	}
	return cleaned, nil
}

func pathInsideDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func sortTemplateItems(items []TemplateCatalogItem) {
	sort.Slice(items, func(i, j int) bool {
		leftRank := templateKindRank(items[i].Kind)
		rightRank := templateKindRank(items[j].Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return items[i].DisplayName < items[j].DisplayName
	})
}

func normalizeTemplateStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return model.TemplateStatusActive
	}
	return status
}

func templateStatusVisible(status string) bool {
	switch normalizeTemplateStatus(status) {
	case model.TemplateStatusActive, model.TemplateStatusDeprecated:
		return true
	default:
		return false
	}
}

func templateKindRank(kind string) int {
	switch kind {
	case "layout":
		return 0
	case "deck":
		return 1
	case "brand":
		return 2
	default:
		return 99
	}
}

func sha256Dir(root string) (string, error) {
	hash := sha256.New()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		hash.Write([]byte(filepath.ToSlash(rel)))
		hash.Write([]byte{0})
		hash.Write(raw)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
