#!/usr/bin/env python3
"""Deterministic SVG bundle inspector for SlideSmith SPEC-06.

The inspector never modifies authored SVG pages. It parses the live bundle,
enforces page/filename/canvas/static-security contracts, and atomically writes
the canonical SVG inventory consumed by later contract stages.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import sys
import unicodedata
import xml.etree.ElementTree as ET
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from typing import Any
from urllib.parse import urlsplit


SVG_NS = "http://www.w3.org/2000/svg"
INVENTORY_SCHEMA = "slidesmith.svg_inventory.v1"
MANIFEST_SCHEMA = "slidesmith.resources_manifest.v1"
RESOURCE_USAGE_SCHEMA = "slidesmith.svg_resource_usage.v1"
CHART_USAGE_SCHEMA = "slidesmith.chart_usage.v1"
NOTES_INVENTORY_SCHEMA = "slidesmith.notes_inventory.v1"
CANVAS_TOLERANCE = 1e-6
CANVASES: dict[str, tuple[float, float]] = {
    "ppt169": (1280.0, 720.0),
    "ppt43": (1024.0, 768.0),
}
FILENAME_PATTERN = re.compile(r"^(?P<page>[0-9]{2})_(?P<slug>[^/\\]+)\.svg$")
PAGE_ID_PATTERN = re.compile(r"^P(?P<page>[0-9]{2})$")
ELEMENT_ID_PATTERN = re.compile(r"^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$")
CSS_URL_PATTERN = re.compile(r"url\(\s*(['\"]?)(.*?)\1\s*\)", re.IGNORECASE)
BANNED_ELEMENTS = {"script", "foreignobject", "iframe", "object", "embed"}
BANNED_SCHEMES = {"http", "https", "file", "javascript", "data"}
FALLBACK_USAGES = {"diagram", "shape", "text", "placeholder", "omit_optional"}
CHART_VERIFICATION_MODES = {
    "direct-calc",
    "decomposable-calc",
    "partial-calc",
    "formula-verify",
    "manual-verify",
    "not-data-driven",
}
NOTES_HEADING_PATTERN = re.compile(r"^##\s+(P[0-9]{2})\s*\|\s*(.+?)\s*$")
SPEC_PAGE_LINE_PATTERN = re.compile(r"(?im)^\s*(?:#{1,6}\s*)?(?:[-*]\s*)?(P[0-9]{2})\b")
NOTES_PLACEHOLDERS = ("lorem ipsum", "todo", "tbd", "speaker notes here")


class ContractError(RuntimeError):
    """Blocking SVG bundle contract failure."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ContractError("json_invalid", f"cannot read {path.name}: {exc}") from exc
    if not isinstance(value, dict):
        raise ContractError("json_invalid", f"{path.name} root must be an object")
    return value


def atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        tmp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        os.chmod(tmp, 0o644)
        tmp.replace(path)
    finally:
        if tmp.exists():
            tmp.unlink()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(chunk)
    except OSError as exc:
        raise ContractError("file_unreadable", f"cannot hash {path.name}: {exc}") from exc
    return digest.hexdigest()


def project_file(project: Path, path: Path | str, *, nonempty: bool = True) -> Path:
    candidate = Path(path)
    if not candidate.is_absolute():
        candidate = project / candidate
    try:
        relative = candidate.relative_to(project)
    except ValueError as exc:
        raise ContractError("path_escape", f"path is outside the project: {path}") from exc
    current = project
    for part in relative.parts:
        current = current / part
        if current.is_symlink():
            raise ContractError("symlink_forbidden", f"symlink path is forbidden: {relative.as_posix()}")
    try:
        resolved = candidate.resolve(strict=True)
    except OSError as exc:
        raise ContractError("file_missing", f"missing project file: {relative.as_posix()}") from exc
    try:
        resolved.relative_to(project)
    except ValueError as exc:
        raise ContractError("path_escape", f"path resolves outside the project: {relative.as_posix()}") from exc
    if not resolved.is_file() or resolved.is_symlink():
        raise ContractError("file_invalid", f"not a regular file: {relative.as_posix()}")
    if nonempty and resolved.stat().st_size <= 0:
        raise ContractError("file_empty", f"empty project file: {relative.as_posix()}")
    return resolved


def project_relative_argument(path: Path | str, field: str) -> Path:
    raw = str(path)
    candidate = PurePosixPath(raw)
    if (
        not raw
        or "\\" in raw
        or candidate.is_absolute()
        or raw != candidate.as_posix()
        or any(part in {"", ".", ".."} for part in candidate.parts)
    ):
        raise ContractError("path_invalid", f"{field} must be a canonical project-relative POSIX path")
    return Path(candidate.as_posix())


def local_name(value: str) -> str:
    return value.rsplit("}", 1)[-1] if "}" in value else value


def namespace(value: str) -> str:
    if value.startswith("{") and "}" in value:
        return value[1 : value.index("}")]
    return ""


def parse_number(value: str, field: str, *, allow_px: bool = False) -> float:
    raw = value.strip()
    if allow_px and raw.lower().endswith("px"):
        raw = raw[:-2].strip()
    if not raw or raw.endswith("%"):
        raise ContractError("canvas_invalid", f"{field} must be an absolute number")
    try:
        number = float(raw)
    except ValueError as exc:
        raise ContractError("canvas_invalid", f"{field} is not numeric: {value!r}") from exc
    if not math.isfinite(number):
        raise ContractError("canvas_invalid", f"{field} must be finite")
    return number


def parse_view_box(value: str) -> list[float]:
    parts = [part for part in re.split(r"[\s,]+", value.strip()) if part]
    if len(parts) != 4:
        raise ContractError("canvas_invalid", "viewBox must contain four numbers")
    numbers = [parse_number(part, "viewBox") for part in parts]
    if abs(numbers[0]) > CANVAS_TOLERANCE or abs(numbers[1]) > CANVAS_TOLERANCE:
        raise ContractError("canvas_invalid", "viewBox origin must be 0 0")
    if numbers[2] <= 0 or numbers[3] <= 0:
        raise ContractError("canvas_invalid", "viewBox width and height must be positive")
    return numbers


def nearly_equal(left: float, right: float) -> bool:
    return abs(left - right) <= CANVAS_TOLERANCE


def validate_slug(slug: str, filename: str) -> None:
    if not slug or slug in {".", ".."} or slug != slug.strip():
        raise ContractError("filename_invalid", f"unsafe SVG slug: {filename}")
    if unicodedata.normalize("NFC", slug) != slug:
        raise ContractError("filename_invalid", f"SVG slug must use NFC normalization: {filename}")
    for character in slug:
        category = unicodedata.category(character)
        if character not in {"-", "_"} and not category.startswith(("L", "N")):
            raise ContractError("filename_invalid", f"SVG slug contains a forbidden character: {filename}")


def confirmed_canvas(project: Path) -> tuple[str, float, float, int]:
    confirmation = load_json(project_file(project, "confirm_ui/result.json"))
    canvas = str(confirmation.get("canvas") or "").strip().lower()
    if canvas not in CANVASES:
        raise ContractError("canvas_unknown", f"unsupported confirmed canvas: {canvas!r}")
    try:
        page_count = int(confirmation.get("page_count"))
    except (TypeError, ValueError) as exc:
        raise ContractError("page_count_invalid", "confirmed page_count must be an integer") from exc
    if page_count < 1 or page_count > 99:
        raise ContractError("page_count_invalid", f"confirmed page_count is outside 1..99: {page_count}")
    width, height = CANVASES[canvas]

    lock_text = project_file(project, "spec_lock.md").read_text(encoding="utf-8")
    canvas_match = re.search(
        r"(?im)^\s*(?:[-*]\s*)?(?:canvas|canvas_format)\s*[:=]\s*([a-zA-Z0-9_-]+)\s*$",
        lock_text,
    )
    if canvas_match and canvas_match.group(1).strip().lower() != canvas:
        raise ContractError("canvas_mismatch", "spec_lock canvas differs from confirmation")
    view_box_match = re.search(r"(?im)^\s*(?:[-*]\s*)?viewbox\s*[:=]\s*([^\r\n]+)$", lock_text)
    if view_box_match:
        locked = parse_view_box(view_box_match.group(1))
        if not nearly_equal(locked[2], width) or not nearly_equal(locked[3], height):
            raise ContractError("canvas_mismatch", "spec_lock viewBox differs from confirmation")
    return canvas, width, height, page_count


def validate_spec_page_mapping(project: Path, expected_pages: int) -> None:
    wanted = [f"P{page:02d}" for page in range(1, expected_pages + 1)]
    design_text = project_file(project, "design_spec.md").read_text(encoding="utf-8")
    design_ids = list(dict.fromkeys(SPEC_PAGE_LINE_PATTERN.findall(design_text)))
    if design_ids != wanted:
        raise ContractError("page_mapping_invalid", f"design spec page IDs are {design_ids}, expected {wanted}")
    lock_text = project_file(project, "spec_lock.md").read_text(encoding="utf-8")
    lock_ids = list(dict.fromkeys(SPEC_PAGE_LINE_PATTERN.findall(lock_text)))
    if lock_ids and lock_ids != wanted:
        raise ContractError("page_mapping_invalid", f"spec lock page IDs are {lock_ids}, expected {wanted}")


def bundle_files(project: Path, expected_pages: int) -> list[tuple[int, str, Path]]:
    svg_dir = project / "svg_output"
    if not svg_dir.is_dir() or svg_dir.is_symlink():
        raise ContractError("bundle_missing", "svg_output must be a directory")
    files: list[tuple[int, str, Path]] = []
    casefold_names: set[str] = set()
    for entry in sorted(svg_dir.iterdir(), key=lambda item: item.name.casefold()):
        if entry.is_symlink() or not entry.is_file():
            raise ContractError("bundle_entry_invalid", f"unexpected bundle entry: {entry.name}")
        match = FILENAME_PATTERN.fullmatch(entry.name)
        if not match:
            raise ContractError("filename_invalid", f"SVG filename must match <NN>_<slug>.svg: {entry.name}")
        slug = match.group("slug")
        validate_slug(slug, entry.name)
        folded = unicodedata.normalize("NFC", entry.name).casefold()
        if folded in casefold_names:
            raise ContractError("filename_collision", f"casefold filename collision: {entry.name}")
        casefold_names.add(folded)
        files.append((int(match.group("page")), slug, entry))
    if len(files) != expected_pages:
        raise ContractError("page_count_mismatch", f"bundle has {len(files)} pages, expected {expected_pages}")
    files.sort(key=lambda item: item[0])
    actual_pages = [item[0] for item in files]
    wanted_pages = list(range(1, expected_pages + 1))
    if actual_pages != wanted_pages:
        raise ContractError("page_sequence_invalid", f"bundle page prefixes are {actual_pages}, expected {wanted_pages}")
    return files


def validate_local_reference(project: Path, svg_path: Path, reference: str, field: str) -> str | None:
    value = reference.strip().strip("'\"")
    if not value or value.startswith("#"):
        return None
    lowered = value.lower()
    parsed = urlsplit(value)
    if parsed.scheme.lower() in BANNED_SCHEMES or lowered.startswith("//"):
        raise ContractError("external_uri", f"{field} contains a forbidden URI in {svg_path.name}")
    path_part = parsed.path
    if not path_part:
        return None
    if Path(path_part).is_absolute():
        raise ContractError("path_escape", f"absolute SVG reference in {svg_path.name}")
    candidate = svg_path.parent / path_part
    resolved = project_file(project, candidate, nonempty=True)
    return resolved.relative_to(project).as_posix()


def validate_attribute_value(project: Path, svg_path: Path, name: str, value: str) -> None:
    lowered = value.strip().lower()
    if any(lowered.startswith(f"{scheme}:") for scheme in BANNED_SCHEMES):
        raise ContractError("external_uri", f"attribute {name} contains a forbidden URI in {svg_path.name}")
    for match in CSS_URL_PATTERN.finditer(value):
        validate_local_reference(project, svg_path, match.group(2), f"{name} url()")
    if local_name(name).lower() in {"href", "src"}:
        validate_local_reference(project, svg_path, value, local_name(name))


def inspect_svg(
    project: Path,
    svg_path: Path,
    page: int,
    expected_width: float,
    expected_height: float,
) -> dict[str, Any]:
    raw = project_file(project, svg_path).read_bytes()
    if re.search(br"<!\s*(?:DOCTYPE|ENTITY)\b", raw, re.IGNORECASE):
        raise ContractError("doctype_forbidden", f"DOCTYPE/entity is forbidden: {svg_path.name}")
    try:
        root = ET.fromstring(raw)
    except ET.ParseError as exc:
        raise ContractError("xml_invalid", f"cannot parse {svg_path.name}: {exc}") from exc
    if local_name(root.tag).lower() != "svg" or namespace(root.tag) != SVG_NS:
        raise ContractError("root_invalid", f"root must be svg in the SVG namespace: {svg_path.name}")

    page_id = f"P{page:02d}"
    if root.attrib.get("data-page-id") != page_id or root.attrib.get("data-spec-page-id") != page_id:
        raise ContractError("page_mapping_invalid", f"root page metadata must equal {page_id}: {svg_path.name}")
    if "viewBox" not in root.attrib or "width" not in root.attrib or "height" not in root.attrib:
        raise ContractError("canvas_invalid", f"root canvas attributes are incomplete: {svg_path.name}")
    view_box = parse_view_box(root.attrib["viewBox"])
    width = parse_number(root.attrib["width"], "width", allow_px=True)
    height = parse_number(root.attrib["height"], "height", allow_px=True)
    if width <= 0 or height <= 0:
        raise ContractError("canvas_invalid", f"width/height must be positive: {svg_path.name}")
    if not all(
        (
            nearly_equal(width, expected_width),
            nearly_equal(height, expected_height),
            nearly_equal(view_box[2], expected_width),
            nearly_equal(view_box[3], expected_height),
            nearly_equal(width / height, view_box[2] / view_box[3]),
        )
    ):
        raise ContractError("canvas_mismatch", f"SVG canvas differs from confirmed canvas: {svg_path.name}")

    element_ids: set[str] = set()
    resource_ids: set[str] = set()
    counts = {"elements": 0, "texts": 0, "images": 0, "uses": 0, "charts": 0, "formulas": 0}
    for element in root.iter():
        counts["elements"] += 1
        tag = local_name(element.tag).lower()
        if tag in BANNED_ELEMENTS:
            raise ContractError("element_forbidden", f"forbidden <{tag}> in {svg_path.name}")
        if tag == "text":
            counts["texts"] += 1
        elif tag == "image":
            counts["images"] += 1
        elif tag == "use":
            counts["uses"] += 1
        if element.attrib.get("data-chart-id"):
            counts["charts"] += 1
        if element.attrib.get("data-formula-id"):
            counts["formulas"] += 1
        resource_id = element.attrib.get("data-resource-id", "").strip()
        if resource_id:
            resource_ids.add(resource_id)
        if "id" in element.attrib:
            element_id = element.attrib["id"].strip()
            if not element_id or not ELEMENT_ID_PATTERN.fullmatch(element_id):
                raise ContractError("element_id_invalid", f"invalid element ID in {svg_path.name}")
            if element_id in element_ids:
                raise ContractError("element_id_duplicate", f"duplicate element ID {element_id!r} in {svg_path.name}")
            element_ids.add(element_id)
        for attribute_name, attribute_value in element.attrib.items():
            if local_name(attribute_name).lower().startswith("on"):
                raise ContractError("event_handler_forbidden", f"event handler in {svg_path.name}")
            validate_attribute_value(project, svg_path, attribute_name, attribute_value)

    return {
        "page_id": page_id,
        "spec_page_id": page_id,
        "page": page,
        "path": svg_path.relative_to(project).as_posix(),
        "sha256": sha256_file(svg_path),
        "width": width,
        "height": height,
        "view_box": view_box,
        "element_count": counts["elements"],
        "text_count": counts["texts"],
        "image_count": counts["images"],
        "use_count": counts["uses"],
        "chart_count": counts["charts"],
        "formula_count": counts["formulas"],
        "resource_ids": sorted(resource_ids),
        "element_ids": sorted(element_ids),
        "warnings": [],
    }


def document_indexes(project: Path, page: dict[str, Any]) -> tuple[ET.Element, dict[str, ET.Element]]:
    svg_path = project_file(project, str(page["path"]))
    try:
        root = ET.fromstring(svg_path.read_bytes())
    except ET.ParseError as exc:  # The page was already parsed; treat mutation as stale.
        raise ContractError("bundle_stale", f"SVG changed during inspection: {page['path']}") from exc
    elements: dict[str, ET.Element] = {}
    for element in root.iter():
        element_id = element.attrib.get("id", "").strip()
        if element_id:
            elements[element_id] = element
    return root, elements


def manifest_resources(manifest: dict[str, Any]) -> dict[str, dict[str, Any]]:
    resources: dict[str, dict[str, Any]] = {}
    for raw in manifest.get("resources") or []:
        if not isinstance(raw, dict):
            raise ContractError("manifest_invalid", "manifest resource must be an object")
        resource_id = str(raw.get("id") or "").strip()
        if not resource_id or resource_id in resources:
            raise ContractError("manifest_invalid", "manifest resource IDs must be non-empty and unique")
        resources[resource_id] = raw
    return resources


def page_sidecar_map(value: dict[str, Any], expected_pages: list[dict[str, Any]], schema: str) -> dict[str, dict[str, Any]]:
    if value.get("schema") != schema:
        raise ContractError("sidecar_invalid", f"sidecar schema must be {schema}")
    rows = value.get("pages")
    if not isinstance(rows, list):
        raise ContractError("sidecar_invalid", f"{schema} pages must be an array")
    by_id: dict[str, dict[str, Any]] = {}
    for raw in rows:
        if not isinstance(raw, dict):
            raise ContractError("sidecar_invalid", f"{schema} page must be an object")
        page_id = str(raw.get("page_id") or "").strip()
        if not PAGE_ID_PATTERN.fullmatch(page_id) or page_id in by_id:
            raise ContractError("sidecar_invalid", f"{schema} has an invalid or duplicate page_id")
        by_id[page_id] = raw
    wanted = [str(page["page_id"]) for page in expected_pages]
    if sorted(by_id) != sorted(wanted):
        raise ContractError("sidecar_invalid", f"{schema} page IDs do not match the SVG bundle")
    return by_id


def element_local_references(project: Path, svg_path: Path, owner: ET.Element) -> set[str]:
    references: set[str] = set()
    for element in owner.iter():
        for name, value in element.attrib.items():
            if local_name(name).lower() in {"href", "src"}:
                relative = validate_local_reference(project, svg_path, value, local_name(name))
                if relative:
                    references.add(relative)
            for match in CSS_URL_PATTERN.finditer(value):
                relative = validate_local_reference(project, svg_path, match.group(2), f"{name} url()")
                if relative:
                    references.add(relative)
    return references


def validate_resource_usage(
    project: Path,
    pages: list[dict[str, Any]],
    manifest: dict[str, Any],
    manifest_sha: str,
    usage_path: Path | str,
) -> dict[str, int]:
    usage = load_json(project_file(project, usage_path))
    if usage.get("resources_manifest_sha256") != manifest_sha:
        raise ContractError("resource_usage_invalid", "resource usage manifest hash is stale")
    try:
        rows = page_sidecar_map(usage, pages, RESOURCE_USAGE_SCHEMA)
    except ContractError as exc:
        raise ContractError("resource_usage_invalid", exc.message) from exc
    resources = manifest_resources(manifest)
    validated_bindings: set[tuple[str, str]] = set()
    used_resources: dict[str, int] = {}
    ready_manifest_paths = {
        str(output.get("path"))
        for resource in resources.values()
        for output in [resource.get("output")]
        if resource.get("status") == "ready" and isinstance(output, dict) and output.get("path")
    }
    for page in pages:
        page_id = str(page["page_id"])
        row = rows[page_id]
        if row.get("svg") != page["path"] or row.get("svg_sha256") != page["sha256"]:
            raise ContractError("resource_usage_invalid", f"resource usage SVG binding is stale for {page_id}")
        bindings = row.get("resources")
        if not isinstance(bindings, list):
            raise ContractError("resource_usage_invalid", f"resource usage entries must be an array for {page_id}")
        root, elements = document_indexes(project, page)
        svg_path = project_file(project, str(page["path"]))
        actual_resource_ids = {
            str(element.attrib.get("data-resource-id") or "").strip()
            for element in root.iter()
            if str(element.attrib.get("data-resource-id") or "").strip()
        }
        visible_sidecar_ids: set[str] = set()
        for raw in bindings:
            if not isinstance(raw, dict):
                raise ContractError("resource_usage_invalid", f"resource binding must be an object for {page_id}")
            resource_id = str(raw.get("resource_id") or "").strip()
            resource = resources.get(resource_id)
            if resource is None:
                raise ContractError("resource_usage_invalid", f"unknown resource {resource_id!r} on {page_id}")
            status = str(resource.get("status") or "").strip()
            usage_type = str(raw.get("usage") or "").strip()
            element_id = str(raw.get("element_id") or "").strip()
            fallback = str(raw.get("fallback") or "").strip()
            if status == "ready":
                if not element_id or element_id not in elements:
                    raise ContractError("resource_usage_invalid", f"resource {resource_id} owner does not exist on {page_id}")
                owner = elements[element_id]
                if owner.attrib.get("data-resource-id") != resource_id:
                    raise ContractError("resource_usage_invalid", f"resource {resource_id} is not bound to its owner on {page_id}")
                output = resource.get("output")
                if not isinstance(output, dict) or not output.get("path") or not output.get("sha256"):
                    raise ContractError("resource_usage_invalid", f"ready resource {resource_id} has no output contract")
                output_path = project_file(project, str(output["path"]))
                if sha256_file(output_path) != output["sha256"]:
                    raise ContractError("resource_usage_invalid", f"ready resource {resource_id} hash is stale")
                sidecar_href = validate_local_reference(project, svg_path, str(raw.get("href") or ""), "sidecar href")
                if sidecar_href != str(output["path"]):
                    raise ContractError("resource_usage_invalid", f"resource {resource_id} sidecar href does not match manifest")
                if str(output["path"]) not in element_local_references(project, svg_path, owner):
                    raise ContractError("resource_usage_invalid", f"resource {resource_id} href is not under its owner")
                if not usage_type:
                    raise ContractError("resource_usage_invalid", f"ready resource {resource_id} usage is empty")
                visible_sidecar_ids.add(resource_id)
            elif status == "degraded":
                manifest_fallback = resource.get("fallback") or {}
                fallback_type = str(manifest_fallback.get("type") or "") if isinstance(manifest_fallback, dict) else ""
                if fallback_type not in FALLBACK_USAGES or fallback != fallback_type or usage_type != fallback_type:
                    raise ContractError("resource_usage_invalid", f"resource {resource_id} fallback binding is invalid")
                if fallback_type == "omit_optional":
                    if bool(resource.get("required")) or element_id:
                        raise ContractError("resource_usage_invalid", f"omit_optional binding is invalid for {resource_id}")
                else:
                    if not element_id or element_id not in elements:
                        raise ContractError("resource_usage_invalid", f"fallback owner for {resource_id} does not exist")
                    if elements[element_id].attrib.get("data-resource-id") != resource_id:
                        raise ContractError("resource_usage_invalid", f"fallback {resource_id} is not bound to its owner")
                    visible_sidecar_ids.add(resource_id)
            else:
                raise ContractError("resource_usage_invalid", f"resource {resource_id} has non-consumable status {status!r}")
            validated_bindings.add((page_id, resource_id))
            used_resources[resource_id] = used_resources.get(resource_id, 0) + 1
            allow_multiple = bool(resource.get("allow_multiple_usage")) or bool(
                isinstance(resource.get("input"), dict) and resource["input"].get("allow_multiple_usage")
            )
            if used_resources[resource_id] > 1 and not allow_multiple:
                raise ContractError("resource_usage_invalid", f"resource {resource_id} is used multiple times without approval")
        if actual_resource_ids != visible_sidecar_ids:
            raise ContractError("resource_usage_invalid", f"SVG and sidecar resource IDs differ on {page_id}")
        unregistered_references = element_local_references(project, svg_path, root) - ready_manifest_paths
        if unregistered_references:
            raise ContractError(
                "resource_usage_invalid",
                f"SVG has local references not registered as ready manifest outputs on {page_id}",
            )
    for resource_id, resource in resources.items():
        if resource.get("type") in {"chart_data", "chart_template"}:
            continue
        if bool(resource.get("required")) and resource.get("status") in {"ready", "degraded"} and not used_resources.get(resource_id):
            raise ContractError("resource_usage_invalid", f"required resource {resource_id} is not used")
    return {"bindings": len(validated_bindings), "resources": len(used_resources)}


def chart_data_dimensions(payload: dict[str, Any]) -> tuple[list[str] | None, list[str] | None]:
    data = payload.get("data")
    if not isinstance(data, dict):
        return None, None
    categories = data.get("categories") if isinstance(data.get("categories"), list) else data.get("labels")
    series = data.get("series")
    normalized_categories = [str(item) for item in categories] if isinstance(categories, list) else None
    normalized_series: list[str] | None = None
    if isinstance(series, list):
        normalized_series = [str(item.get("name") if isinstance(item, dict) else item) for item in series]
    return normalized_categories, normalized_series


def validate_chart_usage(
    project: Path,
    pages: list[dict[str, Any]],
    manifest: dict[str, Any],
    manifest_sha: str,
    chart_path: Path | str,
) -> dict[str, int]:
    sidecar = load_json(project_file(project, chart_path))
    if sidecar.get("schema") != CHART_USAGE_SCHEMA or sidecar.get("resources_manifest_sha256") != manifest_sha:
        raise ContractError("chart_usage_invalid", "chart usage schema or manifest hash is invalid")
    charts = sidecar.get("charts")
    if not isinstance(charts, list):
        raise ContractError("chart_usage_invalid", "chart usage charts must be an array")
    page_map = {str(page["page_id"]): page for page in pages}
    resources = manifest_resources(manifest)
    seen_chart_ids: set[str] = set()
    used_chart_resource_ids: set[str] = set()
    actual_chart_ids: set[str] = set()
    actual_chart_pages: dict[str, str] = {}
    for page in pages:
        root, _elements = document_indexes(project, page)
        for element in root.iter():
            actual_chart_id = str(element.attrib.get("data-chart-id") or "").strip()
            if not actual_chart_id:
                continue
            if actual_chart_id in actual_chart_pages:
                raise ContractError("chart_usage_invalid", f"duplicate chart ID {actual_chart_id!r} in SVG bundle")
            actual_chart_ids.add(actual_chart_id)
            actual_chart_pages[actual_chart_id] = str(page["page_id"])
    for raw in charts:
        if not isinstance(raw, dict):
            raise ContractError("chart_usage_invalid", "chart usage entry must be an object")
        chart_id = str(raw.get("chart_id") or "").strip()
        page_id = str(raw.get("page_id") or "").strip()
        element_id = str(raw.get("element_id") or "").strip()
        if not chart_id or chart_id in seen_chart_ids or page_id not in page_map:
            raise ContractError("chart_usage_invalid", "chart ID/page binding is invalid")
        seen_chart_ids.add(chart_id)
        page = page_map[page_id]
        if raw.get("svg") != page["path"]:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} SVG path is stale")
        _root, elements = document_indexes(project, page)
        owner = elements.get(element_id)
        data_id = str(raw.get("data_resource_id") or "").strip()
        template_id = str(raw.get("template_resource_id") or "").strip()
        if owner is None or owner.attrib.get("data-chart-id") != chart_id:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} owner does not exist")
        if actual_chart_pages.get(chart_id) != page_id:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} page binding is invalid")
        if owner.attrib.get("data-chart-data-resource-id") != data_id:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} data owner binding is invalid")
        if owner.attrib.get("data-chart-template-resource-id") != template_id:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} template owner binding is invalid")
        data_resource = resources.get(data_id)
        template_resource = resources.get(template_id)
        if not data_resource or data_resource.get("status") != "ready" or data_resource.get("type") != "chart_data":
            raise ContractError("chart_usage_invalid", f"chart {chart_id} data resource is not ready chart_data")
        if not template_resource or template_resource.get("status") != "ready" or template_resource.get("type") != "chart_template":
            raise ContractError("chart_usage_invalid", f"chart {chart_id} template resource is not ready chart_template")
        used_chart_resource_ids.update({data_id, template_id})
        chart_type = str(raw.get("chart_type") or "").strip()
        expected_chart_type = str(data_resource.get("chart_type") or "").strip()
        if not expected_chart_type and isinstance(data_resource.get("input"), dict):
            expected_chart_type = str(data_resource["input"].get("chart_type") or "").strip()
        if not chart_type or (expected_chart_type and chart_type != expected_chart_type):
            raise ContractError("chart_usage_invalid", f"chart {chart_id} chart type is invalid")
        data_output = data_resource.get("output") or {}
        data_file = project_file(project, str(data_output.get("path") or ""))
        data_sha = sha256_file(data_file)
        if data_sha != data_output.get("sha256") or data_sha != raw.get("data_sha256"):
            raise ContractError("chart_usage_invalid", f"chart {chart_id} data hash is stale")
        mode = str(raw.get("verification_mode") or "")
        if mode not in CHART_VERIFICATION_MODES:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} verification mode is invalid")
        plot = raw.get("plot_area")
        if not isinstance(plot, list) or len(plot) != 4:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} plot_area is invalid")
        try:
            x1, y1, x2, y2 = [float(item) for item in plot]
        except (TypeError, ValueError) as exc:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} plot_area is invalid") from exc
        if not all(math.isfinite(item) for item in (x1, y1, x2, y2)) or not (
            0 <= x1 < x2 <= float(page["width"]) and 0 <= y1 < y2 <= float(page["height"])
        ):
            raise ContractError("chart_usage_invalid", f"chart {chart_id} plot_area is outside the canvas")
        citation = raw.get("source_citation")
        if not isinstance(citation, dict):
            raise ContractError("chart_usage_invalid", f"chart {chart_id} source citation is missing")
        source_file = str(citation.get("file") or citation.get("source_file") or "")
        source_section = str(citation.get("section") or "").strip()
        if not source_file.startswith("sources/") or not source_section:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} citation is outside sources")
        project_file(project, source_file)
        payload = load_json(data_file)
        expected_categories, expected_series = chart_data_dimensions(payload)
        categories = raw.get("categories")
        series = raw.get("series")
        if not isinstance(categories, list) or not categories or not isinstance(series, list) or not series:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} series/categories must be arrays")
        if expected_categories is not None and [str(item) for item in categories] != expected_categories:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} categories differ from chart data")
        if expected_series is not None and [str(item) for item in series] != expected_series:
            raise ContractError("chart_usage_invalid", f"chart {chart_id} series differ from chart data")
    if actual_chart_ids != seen_chart_ids:
        raise ContractError("chart_usage_invalid", "SVG and chart sidecar IDs differ")
    for resource_id, resource in resources.items():
        if (
            resource.get("type") in {"chart_data", "chart_template"}
            and bool(resource.get("required"))
            and resource.get("status") == "ready"
            and resource_id not in used_chart_resource_ids
        ):
            raise ContractError("chart_usage_invalid", f"required chart resource {resource_id} is not used")
    return {"charts": len(charts)}


def notes_sections(raw: str) -> list[dict[str, Any]]:
    sections: list[dict[str, Any]] = []
    current: dict[str, Any] | None = None
    body: list[str] = []
    for line in raw.splitlines():
        match = NOTES_HEADING_PATTERN.fullmatch(line)
        if match:
            if current is not None:
                current["body"] = "\n".join(body).strip()
                sections.append(current)
            current = {"page_id": match.group(1), "heading": match.group(2).strip()}
            body = []
        elif current is not None:
            body.append(line)
    if current is not None:
        current["body"] = "\n".join(body).strip()
        sections.append(current)
    return sections


def validate_notes(project: Path, pages: list[dict[str, Any]], notes_path: Path | str) -> dict[str, Any]:
    path = project_file(project, notes_path)
    raw = path.read_text(encoding="utf-8")
    sections = notes_sections(raw)
    wanted = [str(page["page_id"]) for page in pages]
    actual = [str(section["page_id"]) for section in sections]
    if actual != wanted or len(set(actual)) != len(actual):
        raise ContractError("notes_invalid", f"notes sections are {actual}, expected {wanted}")
    if not any(str(section.get("body") or "").strip() for section in sections):
        raise ContractError("notes_invalid", "all notes sections are empty")
    inventory_pages = []
    for section in sections:
        body = str(section.get("body") or "")
        lowered = body.casefold()
        if any(placeholder in lowered for placeholder in NOTES_PLACEHOLDERS):
            raise ContractError("notes_invalid", f"placeholder notes found for {section['page_id']}")
        inventory_pages.append(
            {
                "page_id": section["page_id"],
                "heading": section["heading"],
                "word_count": len(re.findall(r"\b\w+\b", body, re.UNICODE)),
                "char_count": len(body),
                "empty": not bool(body.strip()),
            }
        )
    return {
        "schema": NOTES_INVENTORY_SCHEMA,
        "notes_sha256": sha256_file(path),
        "page_count": len(pages),
        "pages": inventory_pages,
    }


def inspect_bundle(
    project_path: Path,
    resources_manifest: Path | str = ".slidesmith/resources_manifest.json",
    resource_usage: Path | str = "analysis/svg_resource_usage.json",
    chart_usage: Path | str = "analysis/chart_usage.json",
    notes: Path | str = "notes/total.md",
) -> dict[str, Any]:
    try:
        project = project_path.resolve(strict=True)
    except OSError as exc:
        raise ContractError("project_missing", f"project does not exist: {project_path}") from exc
    if not project.is_dir() or project.is_symlink():
        raise ContractError("project_invalid", "project must be a regular directory")

    resources_manifest = project_relative_argument(resources_manifest, "resources manifest")
    resource_usage = project_relative_argument(resource_usage, "resource usage")
    chart_usage = project_relative_argument(chart_usage, "chart usage")
    notes = project_relative_argument(notes, "notes")

    manifest_path = project_file(project, resources_manifest)
    manifest = load_json(manifest_path)
    if manifest.get("schema") != MANIFEST_SCHEMA:
        raise ContractError("manifest_invalid", "resources manifest schema mismatch")
    task_id = str(manifest.get("task_id") or "").strip()
    runner_profile = str(manifest.get("runner_profile") or "").strip()
    if not task_id or runner_profile != "full-ppt-master":
        raise ContractError("manifest_invalid", "resources manifest task/profile binding is invalid")

    canvas, expected_width, expected_height, expected_pages = confirmed_canvas(project)
    validate_spec_page_mapping(project, expected_pages)
    pages = [
        inspect_svg(project, svg_path, page, expected_width, expected_height)
        for page, _slug, svg_path in bundle_files(project, expected_pages)
    ]
    manifest_sha = sha256_file(manifest_path)
    resource_summary = validate_resource_usage(project, pages, manifest, manifest_sha, resource_usage)
    chart_summary = validate_chart_usage(project, pages, manifest, manifest_sha, chart_usage)
    notes_inventory = validate_notes(project, pages, notes)
    summary = {
        "pages": len(pages),
        "elements": sum(int(page["element_count"]) for page in pages),
        "texts": sum(int(page["text_count"]) for page in pages),
        "images": sum(int(page["image_count"]) for page in pages),
        "charts": sum(int(page["chart_count"]) for page in pages),
        "formulas": sum(int(page["formula_count"]) for page in pages),
    }
    inventory = {
        "schema": INVENTORY_SCHEMA,
        "task_id": task_id,
        "runner_profile": runner_profile,
        "spec_sha256": sha256_file(project_file(project, "design_spec.md")),
        "spec_lock_sha256": sha256_file(project_file(project, "spec_lock.md")),
        "resources_manifest_sha256": manifest_sha,
        "canvas": canvas,
        "page_count": expected_pages,
        "pages": pages,
        "summary": summary,
        "resource_summary": resource_summary,
        "chart_summary": chart_summary,
        "notes_sha256": notes_inventory["notes_sha256"],
        "generated_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
    }
    atomic_json(project / "analysis" / "notes_inventory.json", notes_inventory)
    atomic_json(project / "analysis" / "svg_inventory.json", inventory)
    return inventory


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Inspect a SlideSmith SVG bundle")
    parser.add_argument("project_path")
    parser.add_argument(
        "--resources-manifest",
        default=".slidesmith/resources_manifest.json",
        help="project-relative canonical resources manifest",
    )
    parser.add_argument("--resource-usage", default="analysis/svg_resource_usage.json")
    parser.add_argument("--chart-usage", default="analysis/chart_usage.json")
    parser.add_argument("--notes", default="notes/total.md")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        inventory = inspect_bundle(
            Path(args.project_path),
            Path(args.resources_manifest),
            Path(args.resource_usage),
            Path(args.chart_usage),
            Path(args.notes),
        )
    except ContractError as exc:
        print(
            json.dumps(
                {
                    "schema": "slidesmith.svg_bundle_inspection_error.v1",
                    "code": exc.code,
                    "message": exc.message,
                },
                ensure_ascii=False,
            ),
            file=sys.stderr,
        )
        return 1
    print(json.dumps({"schema": inventory["schema"], "summary": inventory["summary"]}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
