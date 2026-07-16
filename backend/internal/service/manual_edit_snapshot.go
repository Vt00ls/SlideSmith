package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
)

type ManualEditPageSnapshot struct {
	TaskID               string                      `json:"task_id"`
	SessionID            string                      `json:"session_id"`
	Revision             int64                       `json:"revision"`
	PageID               string                      `json:"page_id"`
	BaseSVGsha256        string                      `json:"base_svg_sha256"`
	EditorSnapshotSHA256 string                      `json:"editor_snapshot_sha256"`
	Canvas               ManualEditSnapshotCanvas    `json:"canvas"`
	SVG                  string                      `json:"svg"`
	Elements             []ManualEditSnapshotElement `json:"elements"`
	Warnings             []string                    `json:"warnings"`
}

type ManualEditSnapshotCanvas struct {
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	ViewBox string  `json:"view_box"`
}

type ManualEditSnapshotElement struct {
	ElementID          string            `json:"element_id"`
	SourceID           string            `json:"source_id,omitempty"`
	Tag                string            `json:"tag"`
	ElementFingerprint string            `json:"element_fingerprint"`
	Text               string            `json:"text,omitempty"`
	Attributes         map[string]string `json:"attributes"`
}

type safeXMLNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Contents []safeXMLContent
	Parent   *safeXMLNode
	EditorID string
	SourceID string
}

type safeXMLContent struct {
	Text  string
	Child *safeXMLNode
}

func (s *TaskService) GetEditSessionPage(ctx context.Context, taskID, sessionID, pageID string) (*ManualEditPageSnapshot, error) {
	session, err := s.repo.GetEditSession(ctx, taskID, sessionID)
	if err != nil {
		return nil, err
	}
	base, err := s.loadEditBaseInventory(ctx, taskID, session.BasePublishVersion)
	if err != nil {
		return nil, err
	}
	var inventoryPage *svgInventoryPage
	for index := range base.Inventory.Pages {
		if base.Inventory.Pages[index].PageID == pageID {
			inventoryPage = &base.Inventory.Pages[index]
			break
		}
	}
	if inventoryPage == nil {
		return nil, repository.ErrNotFound
	}
	artifact, ok := base.Authored[inventoryPage.Path]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if _, err := validateStoredArtifact(s.storage, artifact); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.storage.Path(artifact.ObjectKey))
	if err != nil {
		return nil, err
	}
	if len(raw) > 8*1024*1024 {
		return nil, fmt.Errorf("authored SVG exceeds preview size limit")
	}
	resources := make(map[string]model.Artifact, len(base.Artifacts))
	for _, candidate := range base.Artifacts {
		resources[versionArtifactRelativePath(taskID, session.BasePublishVersion, candidate.ObjectKey)] = candidate
	}
	snapshotSVG, elements, warnings, err := buildSafeSVGSnapshot(raw, inventoryPage.Path, resources, s.storage)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(snapshotSVG)
	viewBox := fmt.Sprintf("0 0 %g %g", inventoryPage.Width, inventoryPage.Height)
	if len(inventoryPage.ViewBox) == 4 {
		viewBox = fmt.Sprintf("%g %g %g %g", inventoryPage.ViewBox[0], inventoryPage.ViewBox[1], inventoryPage.ViewBox[2], inventoryPage.ViewBox[3])
	}
	return &ManualEditPageSnapshot{
		TaskID: taskID, SessionID: session.ID, Revision: session.Revision, PageID: pageID,
		BaseSVGsha256: inventoryPage.SHA256, EditorSnapshotSHA256: hex.EncodeToString(sum[:]),
		Canvas: ManualEditSnapshotCanvas{Width: inventoryPage.Width, Height: inventoryPage.Height, ViewBox: viewBox},
		SVG:    string(snapshotSVG), Elements: elements, Warnings: warnings,
	}, nil
}

func buildSafeSVGSnapshot(raw []byte, pagePath string, resources map[string]model.Artifact, storage StorageService) ([]byte, []ManualEditSnapshotElement, []string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = true
	root, err := decodeSafeXMLNode(decoder, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse authored SVG: %w", err)
	}
	if root == nil || strings.ToLower(root.Name.Local) != "svg" {
		return nil, nil, nil, fmt.Errorf("authored preview root must be svg")
	}
	seenIDs := map[string]bool{}
	counter := 0
	warnings := []string{}
	if err := sanitizeSnapshotNode(root, true, seenIDs, &counter, filepath.ToSlash(filepath.Dir(pagePath)), resources, storage, &warnings); err != nil {
		return nil, nil, nil, err
	}
	elements := []ManualEditSnapshotElement{}
	collectSnapshotElements(root, &elements)
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	if err := encodeSafeXMLNode(encoder, root); err != nil {
		return nil, nil, nil, err
	}
	if err := encoder.Flush(); err != nil {
		return nil, nil, nil, err
	}
	return output.Bytes(), elements, warnings, nil
}

func decodeSafeXMLNode(decoder *xml.Decoder, parent *safeXMLNode) (*safeXMLNode, error) {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			node := &safeXMLNode{Name: typed.Name, Attrs: append([]xml.Attr(nil), typed.Attr...), Parent: parent}
			for {
				next, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				switch inner := next.(type) {
				case xml.StartElement:
					child, err := decodeSafeXMLStarted(decoder, node, inner)
					if err != nil {
						return nil, err
					}
					node.Contents = append(node.Contents, safeXMLContent{Child: child})
				case xml.CharData:
					node.Contents = append(node.Contents, safeXMLContent{Text: string(inner)})
				case xml.EndElement:
					if inner.Name != node.Name {
						return nil, fmt.Errorf("mismatched closing element %s", inner.Name.Local)
					}
					return node, nil
				case xml.Directive:
					return nil, fmt.Errorf("XML directives are forbidden")
				case xml.ProcInst:
					return nil, fmt.Errorf("XML processing instructions are forbidden")
				}
			}
		case xml.Directive:
			return nil, fmt.Errorf("XML directives are forbidden")
		case xml.ProcInst:
			if strings.ToLower(typed.Target) != "xml" {
				return nil, fmt.Errorf("XML processing instructions are forbidden")
			}
		}
	}
}

func decodeSafeXMLStarted(decoder *xml.Decoder, parent *safeXMLNode, start xml.StartElement) (*safeXMLNode, error) {
	node := &safeXMLNode{Name: start.Name, Attrs: append([]xml.Attr(nil), start.Attr...), Parent: parent}
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			child, err := decodeSafeXMLStarted(decoder, node, typed)
			if err != nil {
				return nil, err
			}
			node.Contents = append(node.Contents, safeXMLContent{Child: child})
		case xml.CharData:
			node.Contents = append(node.Contents, safeXMLContent{Text: string(typed)})
		case xml.EndElement:
			if typed.Name != node.Name {
				return nil, fmt.Errorf("mismatched closing element %s", typed.Name.Local)
			}
			return node, nil
		case xml.Directive, xml.ProcInst:
			return nil, fmt.Errorf("XML directives and processing instructions are forbidden")
		}
	}
}

func sanitizeSnapshotNode(node *safeXMLNode, root bool, seen map[string]bool, counter *int, pageDir string, resources map[string]model.Artifact, storage StorageService, warnings *[]string) error {
	tag := strings.ToLower(node.Name.Local)
	switch tag {
	case "script", "foreignobject", "iframe", "object", "embed":
		return fmt.Errorf("unsafe SVG element <%s>", tag)
	}
	cleanAttrs := make([]xml.Attr, 0, len(node.Attrs)+3)
	for _, attr := range node.Attrs {
		name := strings.ToLower(attr.Name.Local)
		if strings.HasPrefix(name, "on") {
			return fmt.Errorf("unsafe SVG event attribute %s", attr.Name.Local)
		}
		if name == "style" && strings.Contains(strings.ToLower(attr.Value), "url(") {
			return fmt.Errorf("unsafe SVG style URL")
		}
		if name == "id" {
			value := strings.TrimSpace(attr.Value)
			if value == "" || seen[value] {
				return fmt.Errorf("duplicate or empty SVG id %q", value)
			}
			seen[value] = true
			if strings.HasPrefix(value, "_edit_") {
				continue
			}
			node.SourceID, node.EditorID = value, value
		}
		if name == "href" {
			value := strings.TrimSpace(attr.Value)
			lower := strings.ToLower(value)
			switch {
			case strings.HasPrefix(value, "#"), strings.HasPrefix(lower, "data:image/"):
			case strings.Contains(lower, "://"), strings.HasPrefix(lower, "javascript:"), strings.HasPrefix(lower, "file:"), strings.HasPrefix(value, "/"):
				return fmt.Errorf("external SVG href is forbidden")
			default:
				inlined, err := inlineSnapshotResource(pageDir, value, resources, storage)
				if err != nil {
					return err
				}
				attr.Value = inlined
				*warnings = append(*warnings, "local image resource was inlined for preview")
			}
		}
		if strings.HasPrefix(name, "data-editor-") {
			continue
		}
		cleanAttrs = append(cleanAttrs, attr)
	}
	node.Attrs = cleanAttrs
	if !root && node.EditorID == "" {
		node.EditorID = fmt.Sprintf("_edit_%d", *counter)
		*counter++
	}
	for _, content := range node.Contents {
		if content.Child != nil {
			if err := sanitizeSnapshotNode(content.Child, false, seen, counter, pageDir, resources, storage, warnings); err != nil {
				return err
			}
		}
	}
	if !root {
		fingerprint := snapshotElementFingerprint(node)
		node.Attrs = append(node.Attrs,
			xml.Attr{Name: xml.Name{Local: "data-editor-id"}, Value: node.EditorID},
			xml.Attr{Name: xml.Name{Local: "data-editor-fingerprint"}, Value: fingerprint},
		)
		if snapshotSelectable(tag) {
			node.Attrs = append(node.Attrs, xml.Attr{Name: xml.Name{Local: "data-editor-selectable"}, Value: "true"})
		}
	}
	return nil
}

func inlineSnapshotResource(pageDir, href string, resources map[string]model.Artifact, storage StorageService) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(filepath.Join(pageDir, filepath.FromSlash(href))))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("SVG image href escapes project")
	}
	artifact, ok := resources[clean]
	if !ok {
		return "", fmt.Errorf("SVG image href %q is not a version artifact", href)
	}
	if _, err := validateStoredArtifact(storage, artifact); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(storage.Path(artifact.ObjectKey))
	if err != nil {
		return "", err
	}
	if len(raw) > 10*1024*1024 {
		return "", fmt.Errorf("SVG image resource is too large")
	}
	mimeType := artifact.MimeType
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(artifact.Name))
	}
	if !strings.HasPrefix(mimeType, "image/") || mimeType == "image/svg+xml" {
		return "", fmt.Errorf("SVG image resource MIME type is forbidden")
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

func snapshotSelectable(tag string) bool {
	switch tag {
	case "g", "text", "tspan", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "use", "image":
		return true
	}
	return false
}

func snapshotElementFingerprint(node *safeXMLNode) string {
	type pair struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	pairs := []pair{}
	for _, attr := range node.Attrs {
		name := strings.ToLower(attr.Name.Local)
		if strings.HasPrefix(name, "data-editor-") || name == "href" {
			continue
		}
		pairs = append(pairs, pair{Name: name, Value: attr.Value})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Name == pairs[j].Name {
			return pairs[i].Value < pairs[j].Value
		}
		return pairs[i].Name < pairs[j].Name
	})
	children := []string{}
	for _, content := range node.Contents {
		if content.Child != nil {
			children = append(children, strings.ToLower(content.Child.Name.Local))
		}
	}
	payload := map[string]any{
		"tag": strings.ToLower(node.Name.Local), "attrs": pairs, "text": normalizeSnapshotText(nodeText(node)),
		"parent": snapshotParentSignature(node.Parent), "children": children,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func snapshotParentSignature(node *safeXMLNode) string {
	if node == nil {
		return ""
	}
	id := node.SourceID
	if id == "" {
		id = node.EditorID
	}
	return strings.ToLower(node.Name.Local) + "#" + id
}

func nodeText(node *safeXMLNode) string {
	var builder strings.Builder
	for _, content := range node.Contents {
		if content.Child != nil {
			builder.WriteString(nodeText(content.Child))
		} else {
			builder.WriteString(content.Text)
		}
	}
	return builder.String()
}

func normalizeSnapshotText(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}

func collectSnapshotElements(node *safeXMLNode, result *[]ManualEditSnapshotElement) {
	if node.EditorID != "" && snapshotSelectable(strings.ToLower(node.Name.Local)) {
		attrs := map[string]string{}
		for _, attr := range node.Attrs {
			name := strings.ToLower(attr.Name.Local)
			if strings.HasPrefix(name, "data-editor-") || name == "id" || name == "href" {
				continue
			}
			switch name {
			case "x", "y", "transform", "fill", "stroke", "opacity", "font-size", "font-family", "font-weight", "text-anchor":
				attrs[name] = attr.Value
			}
		}
		*result = append(*result, ManualEditSnapshotElement{
			ElementID: node.EditorID, SourceID: node.SourceID, Tag: strings.ToLower(node.Name.Local),
			ElementFingerprint: snapshotElementFingerprint(node), Text: normalizeSnapshotText(nodeText(node)), Attributes: attrs,
		})
	}
	for _, content := range node.Contents {
		if content.Child != nil {
			collectSnapshotElements(content.Child, result)
		}
	}
}

func encodeSafeXMLNode(encoder *xml.Encoder, node *safeXMLNode) error {
	start := xml.StartElement{Name: node.Name, Attr: node.Attrs}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	for _, content := range node.Contents {
		if content.Child != nil {
			if err := encodeSafeXMLNode(encoder, content.Child); err != nil {
				return err
			}
		} else if content.Text != "" {
			if err := encoder.EncodeToken(xml.CharData([]byte(content.Text))); err != nil {
				return err
			}
		}
	}
	return encoder.EncodeToken(start.End())
}
