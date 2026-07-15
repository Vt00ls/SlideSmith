#!/usr/bin/env python3
"""Deterministic inventory and source-fidelity contracts for SPEC-08 Beautify."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unicodedata
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path, PurePosixPath
from typing import Any

from quality_schema import QualityError, atomic_json, finding, sha256_file, summarize, utc_now


INPUTS_SCHEMA = "slidesmith.beautify_inputs.v1"
INVENTORY_SCHEMA = "beautify_inventory.v1"
INVENTORY_CONTRACT_SCHEMA = "slidesmith.beautify_inventory_contract.v1"
RISK_SCHEMA = "slidesmith.beautify_risk_report.v1"
LOCK_SCHEMA = "slidesmith.beautify_lock.v1"
SVG_FIDELITY_SCHEMA = "slidesmith.beautify_svg_fidelity.v1"
FIDELITY_SCHEMA = "slidesmith.beautify_fidelity_report.v1"
PRESENTATION_SUFFIXES = {".pptx", ".pptm", ".ppsx", ".ppsm", ".potx", ".potm"}
MAX_ZIP_ENTRIES = 5000
MAX_UNCOMPRESSED = 512 * 1024 * 1024
MAX_COMPRESSION_RATIO = 200
REL_NS = "http://schemas.openxmlformats.org/package/2006/relationships"
P_NS = "http://schemas.openxmlformats.org/presentationml/2006/main"
DOC_REL_NS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
TRUE_VALUES = {"1", "true", "yes", "on"}
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")


def load_json(path: Path, *, root: type = dict, rule: str = "beautify.input_invalid") -> Any:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise QualityError(rule, f"cannot read {path.name}: {exc}", stage="beautify") from exc
    if not isinstance(value, root):
        raise QualityError(rule, f"{path.name} root must be {root.__name__}", stage="beautify")
    return value


def canonical_sha(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def normalize_text(value: Any) -> str:
    text = unicodedata.normalize("NFKC", str(value or ""))
    text = text.replace("\r\n", "\n").replace("\r", "\n").replace("\v", "\n")
    text = text.replace("\u00a0", " ").replace("\u2022", "•")
    return " ".join(text.split())


def fidelity_key(value: Any) -> str:
    return re.sub(r"\s+", "", normalize_text(value))


def _relative(project: Path, path: Path) -> str:
    return path.relative_to(project).as_posix()


def _regular_project_file(project: Path, relative: str, *, rule: str, nonempty: bool = True) -> Path:
    posix = PurePosixPath(relative)
    if not relative or posix.is_absolute() or posix.as_posix() != relative or any(part in {"", ".", ".."} for part in posix.parts):
        raise QualityError(rule, f"unsafe project path: {relative!r}", stage="beautify")
    current = project
    for part in posix.parts:
        current /= part
        if current.is_symlink():
            raise QualityError(rule, f"symlink is forbidden: {relative}", stage="beautify")
    try:
        resolved = current.resolve(strict=True)
        resolved.relative_to(project)
    except (OSError, ValueError) as exc:
        raise QualityError(rule, f"path missing or outside project: {relative}", stage="beautify") from exc
    if not resolved.is_file() or (nonempty and resolved.stat().st_size <= 0):
        raise QualityError(rule, f"regular non-empty file required: {relative}", stage="beautify")
    return resolved


def _project_root(project: Path) -> Path:
    requested = Path(os.path.abspath(project.expanduser()))
    if requested.is_symlink():
        raise QualityError("beautify_inventory.inputs", "project root must not be a symlink", stage="beautify_inventory")
    try:
        resolved = requested.resolve(strict=True)
    except OSError as exc:
        raise QualityError("beautify_inventory.inputs", "project root is missing", stage="beautify_inventory") from exc
    if not resolved.is_dir():
        raise QualityError("beautify_inventory.inputs", "project root must be a directory", stage="beautify_inventory")
    return resolved


def _file_ref(project: Path, path: Path) -> dict[str, Any]:
    return {"path": _relative(project, path), "sha256": sha256_file(path), "size": path.stat().st_size}


def _safe_xml(package: zipfile.ZipFile, name: str) -> ET.Element:
    try:
        raw = package.read(name)
    except KeyError as exc:
        raise QualityError("beautify_inventory.inputs", f"missing PPTX part: {name}", stage="beautify_inventory") from exc
    if re.search(br"<!\s*(?:DOCTYPE|ENTITY)\b", raw, re.IGNORECASE):
        raise QualityError("beautify_inventory.inputs", f"XML entity is forbidden: {name}", stage="beautify_inventory")
    try:
        return ET.fromstring(raw)
    except ET.ParseError as exc:
        raise QualityError("beautify_inventory.inputs", f"invalid XML part: {name}", stage="beautify_inventory") from exc


def _resolve_package_target(source: str, target: str) -> str:
    if not target or target.startswith("/") or "\\" in target or ":" in target.split("/", 1)[0]:
        raise QualityError("beautify_inventory.inputs", "unsafe PPTX relationship target", stage="beautify_inventory")
    stack: list[str] = []
    for part in (PurePosixPath(source).parent / target).parts:
        if part in {"", "."}:
            continue
        if part == "..":
            if not stack:
                raise QualityError("beautify_inventory.inputs", "PPTX relationship escapes package", stage="beautify_inventory")
            stack.pop()
        else:
            stack.append(part)
    return "/".join(stack)


def inspect_source_package(source: Path) -> dict[str, Any]:
    try:
        package = zipfile.ZipFile(source)
    except (OSError, zipfile.BadZipFile) as exc:
        raise QualityError("beautify_inventory.inputs", "source is not a valid PPTX ZIP package", stage="beautify_inventory") from exc
    with package:
        infos = package.infolist()
        names = [item.filename for item in infos]
        if len(infos) > MAX_ZIP_ENTRIES or len(names) != len(set(names)):
            raise QualityError("beautify_inventory.inputs", "source PPTX has too many or duplicate entries", stage="beautify_inventory")
        if any(name.startswith("/") or "\\" in name or ".." in PurePosixPath(name).parts for name in names):
            raise QualityError("beautify_inventory.inputs", "source PPTX has an unsafe ZIP entry", stage="beautify_inventory")
        if sum(item.file_size for item in infos) > MAX_UNCOMPRESSED:
            raise QualityError("beautify_inventory.inputs", "source PPTX exceeds the uncompressed size limit", stage="beautify_inventory")
        if any(item.file_size > 1024 * 1024 and item.compress_size > 0 and item.file_size / item.compress_size > MAX_COMPRESSION_RATIO for item in infos):
            raise QualityError("beautify_inventory.inputs", "source PPTX exceeds the compression-ratio limit", stage="beautify_inventory")
        required = {"[Content_Types].xml", "_rels/.rels", "ppt/presentation.xml", "ppt/_rels/presentation.xml.rels"}
        if required - set(names):
            raise QualityError("beautify_inventory.inputs", "source PPTX is missing required package parts", stage="beautify_inventory")
        for name in names:
            if name.lower().endswith((".xml", ".rels")):
                _safe_xml(package, name)

        rels = _safe_xml(package, "ppt/_rels/presentation.xml.rels")
        by_id: dict[str, str] = {}
        for rel in rels.findall(f"{{{REL_NS}}}Relationship"):
            if str(rel.get("TargetMode") or "").lower() == "external":
                raise QualityError("beautify_inventory.inputs", "external PPTX relationships are forbidden", stage="beautify_inventory")
            rel_id = str(rel.get("Id") or "")
            target = _resolve_package_target("ppt/presentation.xml", str(rel.get("Target") or ""))
            if target not in names:
                raise QualityError("beautify_inventory.inputs", "source PPTX relationship target is missing", stage="beautify_inventory")
            by_id[rel_id] = target
        presentation = _safe_xml(package, "ppt/presentation.xml")
        slide_parts: list[str] = []
        for slide_id in presentation.findall(f".//{{{P_NS}}}sldId"):
            part = by_id.get(str(slide_id.get(f"{{{DOC_REL_NS}}}id") or ""), "")
            if not part.startswith("ppt/slides/slide"):
                raise QualityError("beautify_inventory.inputs", "source PPTX slide order is invalid", stage="beautify_inventory")
            slide_parts.append(part)
        if not slide_parts or len(slide_parts) != len(set(slide_parts)):
            raise QualityError("beautify_inventory.inputs", "source PPTX has empty or duplicate slide order", stage="beautify_inventory")

        risks: list[dict[str, Any]] = []
        for index, part in enumerate(slide_parts, start=1):
            root = _safe_xml(package, part)
            xml = ET.tostring(root, encoding="unicode").lower()
            relation_name = str(PurePosixPath(part).parent / "_rels" / f"{PurePosixPath(part).name}.rels")
            relation_types: list[str] = []
            related_visible_text: list[str] = []
            if relation_name in names:
                rel_root = _safe_xml(package, relation_name)
                for rel in rel_root.findall(f"{{{REL_NS}}}Relationship"):
                    if str(rel.get("TargetMode") or "").lower() == "external":
                        raise QualityError("beautify_inventory.inputs", "external slide relationship is forbidden", stage="beautify_inventory")
                    target = _resolve_package_target(part, str(rel.get("Target") or ""))
                    if target not in names:
                        raise QualityError("beautify_inventory.inputs", "slide relationship target is missing", stage="beautify_inventory")
                    relation_type = str(rel.get("Type") or "").rsplit("/", 1)[-1].lower()
                    relation_types.append(relation_type)
                    if relation_type == "diagramdata":
                        related_root = _safe_xml(package, target)
                        for node in related_root.iter():
                            if node.tag.rsplit("}", 1)[-1].lower() == "t" and normalize_text(node.text):
                                related_visible_text.append(str(node.text))
            unsupported = sorted({
                value for value in ("oleobject", "video", "audio", "diagramdata", "diagramlayout", "diagramquickstyle", "diagramcolors")
                if value in relation_types
            })
            ignored = sorted({value for value in ("notesslide", "comments", "commentauthors") if value in relation_types})
            hidden = str(root.get("show") or "").lower() in {"0", "false"}
            local_names = [node.tag.rsplit("}", 1)[-1].lower() for node in root.iter()]
            transition = "transition" in local_names
            animation = "timing" in local_names
            merged_cells = sum(
                node.tag.rsplit("}", 1)[-1].lower() in {"hmerge", "vmerge"}
                or (
                    str(node.get("gridSpan") or "1").isdigit()
                    and int(str(node.get("gridSpan") or "1")) > 1
                )
                for node in root.iter()
            )
            hidden_shapes = sum(1 for node in root.iter() if str(node.get("hidden") or "").lower() in TRUE_VALUES)
            risks.append({
                "source_slide": index,
                "part": part,
                "hidden": hidden,
                "hidden_shapes": hidden_shapes,
                "transition": transition,
                "animation": animation,
                "merged_cells": merged_cells,
                "unsupported": unsupported,
                "ignored": ignored,
                "related_visible_text": related_visible_text,
                "relationship_types": sorted(set(relation_types)),
            })
        return {"slide_count": len(slide_parts), "slides": risks}


def discover_inputs(project: Path, task_id: str, runner_profile: str) -> tuple[dict[str, Any], dict[str, Any]]:
    project = _project_root(project)
    sources = project / "sources"
    if not sources.is_dir() or sources.is_symlink():
        raise QualityError("beautify_inventory.inputs", "beautify requires a regular sources directory", stage="beautify_inventory")
    presentations = sorted((entry for entry in sources.iterdir() if entry.suffix.lower() in PRESENTATION_SUFFIXES), key=lambda item: item.name.casefold())
    if not presentations:
        raise QualityError("beautify_inventory.inputs", "beautify requires exactly one presentation source, found 0", stage="beautify_inventory")
    if len(presentations) != 1:
        raise QualityError("beautify_inventory.multiple_pptx", f"beautify requires exactly one presentation source, found {len(presentations)}", stage="beautify_inventory")
    source = presentations[0]
    if source.suffix.lower() != ".pptx":
        raise QualityError("beautify_inventory.unsupported_source_type", "beautify v1 accepts only .pptx source decks", stage="beautify_inventory")
    source = _regular_project_file(project, _relative(project, source), rule="beautify_inventory.inputs")
    stem = source.stem
    markdown = _regular_project_file(project, f"sources/{stem}.md", rule="beautify_inventory.inputs")
    identity = _regular_project_file(project, f"analysis/{stem}.identity.json", rule="beautify_inventory.missing_identity")
    library = _regular_project_file(project, f"analysis/{stem}.slide_library.json", rule="beautify_inventory.missing_library")
    profile = _regular_project_file(project, "analysis/source_profile.json", rule="beautify_inventory.inputs")
    package = inspect_source_package(source)
    identity_value = load_json(identity, rule="beautify_inventory.missing_identity")
    library_value = load_json(library, rule="beautify_inventory.missing_library")
    profile_value = load_json(profile, rule="beautify_inventory.inputs")
    if library_value.get("schema") != "template_fill_pptx_library.v1":
        raise QualityError("beautify_inventory.missing_library", "unsupported slide library schema", stage="beautify_inventory")
    if profile_value.get("schema") != "pptx_intake_index.v1" or not isinstance(profile_value.get("decks"), list):
        raise QualityError("beautify_inventory.inputs", "source profile schema is invalid", stage="beautify_inventory")
    profile_decks = [item for item in profile_value["decks"] if isinstance(item, dict)]
    if len(profile_decks) != 1 or profile_decks[0].get("stem") != stem:
        raise QualityError("beautify_inventory.inputs", "source profile must contain exactly the discovered source deck", stage="beautify_inventory")
    slide_count = int(library_value.get("slide_count") or 0)
    identity_count = int(identity_value.get("slide_count") or 0)
    if slide_count <= 0 or slide_count != identity_count or slide_count != package["slide_count"]:
        raise QualityError("beautify_inventory.inputs", "source PPTX, identity, and slide-library counts differ", stage="beautify_inventory")
    images_path = project / "images" / "image_manifest.json"
    images: list[Any] = []
    image_ref: dict[str, Any] | None = None
    if images_path.exists() or images_path.is_symlink():
        images_path = _regular_project_file(project, "images/image_manifest.json", rule="beautify_inventory.inputs")
        images = load_json(images_path, root=list, rule="beautify_inventory.inputs")
        image_ref = _file_ref(project, images_path) | {"count": len(images)}
    warnings: list[str] = []
    if not images:
        warnings.append("source deck has no extracted image manifest")
    raw_canvas = identity_value.get("canvas") or library_value.get("canvas_px") or {}
    canvas_width = raw_canvas.get("width") or raw_canvas.get("width_px") or 0
    canvas_height = raw_canvas.get("height") or raw_canvas.get("height_px") or 0
    contract = {
        "schema": INPUTS_SCHEMA,
        "phase": "beautify_inventory",
        "task_id": task_id,
        "route": "beautify",
        "runner_profile": runner_profile,
        "source_pptx": _file_ref(project, source),
        "source_markdown": _file_ref(project, markdown),
        "identity": _file_ref(project, identity),
        "slide_library": _file_ref(project, library),
        "source_profile": _file_ref(project, profile),
        "source_slide_count": slide_count,
        "source_canvas": {
            "width": canvas_width,
            "height": canvas_height,
            "unit": "px",
            "aspect_ratio": (float(canvas_width) / float(canvas_height)) if canvas_width and canvas_height else 0,
        },
        "image_manifest": image_ref,
        "image_count": len(images),
        "warnings": warnings,
        "checked_at": utc_now(),
    }
    return contract, {"project": project, "source": source, "library": library, "images": images_path if image_ref else None, "package": package}


def resolve_inventory_script(explicit: str = "") -> Path:
    candidates: list[Path] = []
    if explicit:
        candidates.append(Path(explicit))
    for name in ("SLIDESMITH_PPT_MASTER_SKILL_DIR", "PPT_MASTER_SKILL_DIR"):
        value = os.environ.get(name, "").strip()
        if value:
            candidates.append(Path(value) / "scripts" / "beautify_inventory.py")
    candidates.extend([
        Path.cwd() / "skills/ppt-master/scripts/beautify_inventory.py",
        Path(__file__).resolve().parents[1] / "skills/ppt-master/scripts/beautify_inventory.py",
    ])
    for candidate in candidates:
        if candidate.is_file():
            return candidate.resolve()
    raise QualityError("beautify_inventory.runner_unavailable", "beautify_inventory.py is unavailable", stage="beautify_inventory")


def _validate_image_entries(project: Path, images: list[Any], slide_count: int) -> None:
    seen: set[str] = set()
    for index, raw in enumerate(images):
        if not isinstance(raw, dict):
            raise QualityError("beautify_inventory.inputs", f"image manifest entry {index} is invalid", stage="beautify_inventory")
        filename = raw.get("filename")
        if not isinstance(filename, str) or PurePosixPath(filename).name != filename or filename in {"", ".", ".."}:
            raise QualityError("beautify_inventory.inputs", f"image manifest filename {index} is unsafe", stage="beautify_inventory")
        _regular_project_file(project, f"images/{filename}", rule="beautify_inventory.inputs")
        if filename in seen:
            raise QualityError("beautify_inventory.inputs", f"duplicate image filename: {filename}", stage="beautify_inventory")
        seen.add(filename)
        occurrences = raw.get("occurrences", [])
        if not isinstance(occurrences, list):
            raise QualityError("beautify_inventory.inputs", f"image occurrences for {filename} must be an array", stage="beautify_inventory")
        for occurrence in occurrences:
            if not isinstance(occurrence, dict) or not isinstance(occurrence.get("slide_index"), int) or not 1 <= occurrence["slide_index"] <= slide_count:
                raise QualityError("beautify_inventory.inputs", f"image occurrence for {filename} has invalid slide_index", stage="beautify_inventory")


def _canonical_inventory(raw: dict[str, Any], inputs: dict[str, Any], package: dict[str, Any], project: Path) -> dict[str, Any]:
    slides: list[dict[str, Any]] = []
    package_by_slide = {item["source_slide"]: item for item in package["slides"]}
    for raw_slide in raw.get("slides", []):
        if not isinstance(raw_slide, dict):
            raise QualityError("beautify_inventory.contract", "inventory slide is invalid", stage="beautify_inventory")
        slide_index = int(raw_slide.get("slide_index") or 0)
        text_blocks: list[dict[str, Any]] = []
        for index, item in enumerate(raw_slide.get("text_blocks", []), start=1):
            if not isinstance(item, dict) or not normalize_text(item.get("text")):
                continue
            text = str(item.get("text") or "")
            text_blocks.append({
                "id": str(item.get("id") or item.get("slot_id") or f"p{slide_index:02d}.text.{index:02d}"),
                "role": str(item.get("role") or "body"),
                "text": text,
                "paragraphs": [line for line in text.replace("\r\n", "\n").replace("\r", "\n").split("\n") if normalize_text(line)],
            })
        existing_text = {fidelity_key(item["text"]) for item in text_blocks}
        for index, text in enumerate(package_by_slide.get(slide_index, {}).get("related_visible_text", []), start=1):
            if fidelity_key(text) in existing_text:
                continue
            text_blocks.append({
                "id": f"p{slide_index:02d}.smartart.text.{index:02d}",
                "role": "diagram_text",
                "text": text,
                "paragraphs": [text],
            })
            existing_text.add(fidelity_key(text))
        tables: list[dict[str, Any]] = []
        for index, item in enumerate(raw_slide.get("tables", []), start=1):
            if not isinstance(item, dict):
                continue
            cells = item.get("cells") or []
            column_count = int(item.get("column_count") or (max((len(row) for row in cells if isinstance(row, list)), default=0)))
            normalized_cells = [
                [str(cell or "") for cell in row] + [""] * max(0, column_count - len(row))
                for row in cells
                if isinstance(row, list)
            ]
            tables.append({
                "id": str(item.get("id") or item.get("table_id") or f"p{slide_index:02d}.table.{index:02d}"),
                "row_count": int(item.get("row_count") or len(normalized_cells)),
                "column_count": column_count,
                "cells": normalized_cells,
            })
        charts: list[dict[str, Any]] = []
        for index, item in enumerate(raw_slide.get("charts", []), start=1):
            if not isinstance(item, dict):
                continue
            charts.append({
                "id": str(item.get("id") or item.get("chart_id") or f"p{slide_index:02d}.chart.{index:02d}"),
                "type": str(item.get("type") or item.get("chart_type") or ""),
                "categories": item.get("categories") or [],
                "series": item.get("series") or [],
            })
        images: list[dict[str, Any]] = []
        for index, item in enumerate(raw_slide.get("images", []), start=1):
            if not isinstance(item, dict):
                continue
            filename = str(item.get("filename") or "")
            shape_name = str(item.get("shape_name") or filename or index)
            source_path = f"images/{filename}" if filename else ""
            if not source_path:
                raise QualityError("beautify_inventory.contract", "source image filename is missing", stage="beautify_inventory")
            source_file = _regular_project_file(project, source_path, rule="beautify_inventory.contract")
            images.append({
                "id": str(item.get("id") or f"p{slide_index:02d}.image.{index:02d}"),
                "filename": filename,
                "source_occurrence": str(item.get("source_occurrence") or f"P{slide_index:02d}:{shape_name}"),
                "source_path": source_path,
                "sha256": sha256_file(source_file),
                "size": source_file.stat().st_size,
                "required": bool(item.get("required", True)),
            })
        slides.append({
            "slide_index": slide_index,
            "page_type": str(raw_slide.get("page_type") or "content_candidate"),
            "text_blocks": text_blocks, "tables": tables, "charts": charts, "images": images,
            "ignored": [], "needs_confirmation": [],
        })
    return {
        "schema": INVENTORY_SCHEMA,
        "task_id": inputs["task_id"],
        "source_pptx_sha256": inputs["source_pptx"]["sha256"],
        "slide_count": inputs["source_slide_count"],
        "slides": slides,
    }


def _validate_inventory(value: dict[str, Any], slide_count: int, project: Path) -> None:
    if value.get("schema") != INVENTORY_SCHEMA or int(value.get("slide_count") or 0) != slide_count:
        raise QualityError("beautify_inventory.contract", "beautify inventory schema/count is invalid", stage="beautify_inventory")
    slides = value.get("slides")
    if not isinstance(slides, list) or [item.get("slide_index") if isinstance(item, dict) else None for item in slides] != list(range(1, slide_count + 1)):
        raise QualityError("beautify_inventory.contract", "inventory slide indexes must be continuous and unique", stage="beautify_inventory")
    all_ids: set[str] = set()
    for slide in slides:
        for key in ("text_blocks", "tables", "charts", "images", "ignored", "needs_confirmation"):
            if not isinstance(slide.get(key), list):
                raise QualityError("beautify_inventory.contract", f"inventory {key} must be an array", stage="beautify_inventory")
        for text in slide["text_blocks"]:
            text_id = str(text.get("id") or "") if isinstance(text, dict) else ""
            if not text_id or text_id in all_ids or not normalize_text(text.get("text")):
                raise QualityError("beautify_inventory.contract", "text IDs/content must be non-empty and globally unique", stage="beautify_inventory")
            all_ids.add(text_id)
        for table in slide["tables"]:
            table_id = str(table.get("id") or "") if isinstance(table, dict) else ""
            rows = table.get("cells") if isinstance(table, dict) else None
            row_count = int(table.get("row_count") or 0) if isinstance(table, dict) else 0
            column_count = int(table.get("column_count") or 0) if isinstance(table, dict) else 0
            if not table_id or table_id in all_ids or row_count <= 0 or column_count <= 0 or not isinstance(rows, list) or len(rows) != row_count or any(not isinstance(row, list) or len(row) != column_count for row in rows):
                raise QualityError("beautify_inventory.contract", "table IDs/dimensions must be stable and globally unique", stage="beautify_inventory")
            all_ids.add(table_id)
        for chart in slide["charts"]:
            chart_id = str(chart.get("id") or "") if isinstance(chart, dict) else ""
            categories = chart.get("categories") if isinstance(chart, dict) else None
            series = chart.get("series") if isinstance(chart, dict) else None
            if not chart_id or chart_id in all_ids or not chart.get("type") or not isinstance(categories, list) or not isinstance(series, list) or not series:
                raise QualityError("beautify_inventory.contract", "chart IDs/type/data must be stable and globally unique", stage="beautify_inventory")
            names: set[str] = set()
            for item in series:
                name = str(item.get("name") or "") if isinstance(item, dict) else ""
                values = item.get("values") if isinstance(item, dict) else None
                if not name or name in names or not isinstance(values, list) or len(values) != len(categories):
                    raise QualityError("beautify_inventory.contract", "chart series/category dimensions are invalid", stage="beautify_inventory")
                for number in values:
                    if isinstance(number, bool) or not isinstance(number, (str, int, float)) or isinstance(number, float) and not math.isfinite(number):
                        raise QualityError("beautify_inventory.contract", "chart data type is unstable", stage="beautify_inventory")
                names.add(name)
            all_ids.add(chart_id)
        for image in slide["images"]:
            image_id = str(image.get("id") or "") if isinstance(image, dict) else ""
            source_occurrence = str(image.get("source_occurrence") or "") if isinstance(image, dict) else ""
            source_path = str(image.get("source_path") or "") if isinstance(image, dict) else ""
            source_sha = str(image.get("sha256") or "") if isinstance(image, dict) else ""
            source_size = int(image.get("size") or 0) if isinstance(image, dict) else 0
            if not image_id or image_id in all_ids or not source_occurrence or not source_path or not SHA256_RE.fullmatch(source_sha) or source_size <= 0:
                raise QualityError("beautify_inventory.contract", "image IDs/occurrences must be non-empty and globally unique", stage="beautify_inventory")
            source_file = _regular_project_file(project, source_path, rule="beautify_inventory.contract")
            if sha256_file(source_file) != source_sha or source_file.stat().st_size != source_size:
                raise QualityError("beautify_inventory.contract", f"source image binding is stale: {image_id}", stage="beautify_inventory")
            all_ids.add(image_id)


def _risk_enrichment(inventory: dict[str, Any], package: dict[str, Any], inputs_sha: str) -> dict[str, Any]:
    package_by_slide = {item["source_slide"]: item for item in package["slides"]}
    pages: list[dict[str, Any]] = []
    for slide in inventory["slides"]:
        index = int(slide["slide_index"])
        raw = package_by_slide[index]
        text_chars = sum(len(normalize_text(item.get("text"))) for item in slide["text_blocks"] if isinstance(item, dict))
        counts = {
            "text_blocks": len(slide["text_blocks"]), "text_characters": text_chars,
            "tables": len(slide["tables"]), "charts": len(slide["charts"]), "images": len(slide["images"]),
            "unsupported_objects": len(raw["unsupported"]),
        }
        density = "overcrowded" if counts["text_blocks"] >= 16 or text_chars >= 900 else "near_empty" if counts["text_blocks"] <= 1 and text_chars <= 40 else "normal"
        ignored: list[dict[str, Any]] = []
        needs: list[dict[str, Any]] = []
        risks: list[dict[str, Any]] = []
        for kind in raw["ignored"]:
            risk_id = f"P{index:02d}.{kind}"
            ignored.append({"id": risk_id, "reason": "not frozen visible slide content"})
            risks.append({"id": risk_id, "slide_index": index, "rule": f"beautify.{kind}", "severity": "info", "item_ids": [], "needs_confirmation": False, "message": "Object is outside frozen visible slide content"})
        if raw["transition"]:
            ignored.append({"id": f"P{index:02d}.transition", "reason": "v1 does not preserve source transitions"})
            risks.append({"id": f"P{index:02d}.transition", "slide_index": index, "rule": "beautify.transition", "severity": "info", "item_ids": [], "needs_confirmation": False, "message": "Source transition will not be preserved"})
        if raw["animation"]:
            ignored.append({"id": f"P{index:02d}.animation", "reason": "v1 does not preserve source animation"})
            risks.append({"id": f"P{index:02d}.animation", "slide_index": index, "rule": "beautify.animation", "severity": "info", "item_ids": [], "needs_confirmation": False, "message": "Source animation will not be preserved"})
        for kind in raw["unsupported"]:
            risk_id = f"P{index:02d}.{kind}"
            needs.append({"id": risk_id, "reason": "native semantics are unsupported in v1"})
            risks.append({"id": risk_id, "slide_index": index, "rule": f"beautify.{kind}_unsupported", "severity": "warning", "item_ids": [], "needs_confirmation": True, "message": "Native object semantics are unsupported in v1"})
        if raw["hidden"]:
            needs.append({"id": f"P{index:02d}.hidden", "reason": "page/content is preserved but hidden flag is not"})
            risks.append({"id": f"P{index:02d}.hidden", "slide_index": index, "rule": "beautify.hidden_slide", "severity": "warning", "item_ids": [], "needs_confirmation": True, "message": "Hidden slide state will not be preserved"})
        if raw["hidden_shapes"]:
            needs.append({"id": f"P{index:02d}.hidden_shapes", "reason": "hidden shapes require explicit disposition"})
            risks.append({"id": f"P{index:02d}.hidden_shapes", "slide_index": index, "rule": "beautify.hidden_shapes", "severity": "warning", "item_ids": [], "needs_confirmation": True, "message": "Hidden source shapes require explicit disposition"})
        if raw["merged_cells"]:
            table_ids = [item["id"] for item in slide["tables"]]
            needs.append({"id": f"P{index:02d}.merged_table", "reason": "merge semantics are best effort"})
            risks.append({"id": f"P{index:02d}.merged_table", "slide_index": index, "rule": "beautify.merged_table", "severity": "warning", "item_ids": table_ids, "needs_confirmation": True, "message": "Merged table semantics are best effort"})
        for chart in slide["charts"]:
            if isinstance(chart, dict) and chart.get("type") == "comboChart":
                risk_id = f"P{index:02d}.{chart.get('id')}.combo"
                needs.append({"id": risk_id, "reason": "multi-plot chart capture is best effort"})
                risks.append({"id": risk_id, "slide_index": index, "rule": "beautify.complex_chart", "severity": "warning", "item_ids": [chart["id"]], "needs_confirmation": True, "message": "Multi-plot chart capture is best effort"})
        if density != "normal":
            needs.append({"id": f"P{index:02d}.density", "reason": "page density requires explicit planning"})
            risks.append({"id": f"P{index:02d}.density", "slide_index": index, "rule": f"beautify.{density}", "severity": "warning", "item_ids": [], "needs_confirmation": True, "message": "Page density requires explicit planning"})
        slide["ignored"] = ignored
        slide["needs_confirmation"] = needs
        content = {
            "text": [{"id": item.get("id"), "text": item.get("text")} for item in slide["text_blocks"]],
            "tables": [{"id": item.get("id"), "cells": item.get("cells")} for item in slide["tables"]],
            "charts": [{"id": item.get("id"), "type": item.get("type"), "categories": item.get("categories"), "series": item.get("series")} for item in slide["charts"]],
            "images": [{
                "id": item.get("id"), "source_occurrence": item.get("source_occurrence"),
                "source_path": item.get("source_path"), "sha256": item.get("sha256"), "size": item.get("size"),
            } for item in slide["images"]],
        }
        pages.append({
            "source_slide": index, "page_id": f"P{index:02d}", "counts": counts, "density": density,
            "hidden": raw["hidden"], "ignored": ignored, "needs_confirmation": needs,
            "content_sha256": canonical_sha(content), "risks": risks,
        })
    all_needs = [item for page in pages for item in page["needs_confirmation"]]
    all_risks = [item for page in pages for item in page["risks"]]
    return {
        "schema": RISK_SCHEMA,
        "task_id": inventory["task_id"],
        "inputs_sha256": inputs_sha,
        "inventory_sha256": "",
        "risks": all_risks,
        "pages": pages,
        "summary": {
            "slides": len(pages), "ignored": sum(len(page["ignored"]) for page in pages),
            "needs_confirmation": len(all_needs), "unsupported": sum(page["counts"]["unsupported_objects"] for page in pages),
            "overcrowded": sum(page["density"] == "overcrowded" for page in pages),
            "near_empty": sum(page["density"] == "near_empty" for page in pages),
        },
        "created_at": utc_now(),
    }


def run_inventory(
    project: Path,
    task_id: str,
    runner_profile: str,
    phase_run_id: str,
    inventory_script: str = "",
    skill_root: str = "",
) -> dict[str, Any]:
    project = _project_root(project)
    for relative in (
        ".slidesmith/contracts/beautify_inputs.json",
        ".slidesmith/contracts/beautify_inventory.json",
        "analysis/beautify_inventory.json",
        "analysis/beautify_risk_report.json",
    ):
        owned = project.joinpath(*PurePosixPath(relative).parts)
        if owned.exists() or owned.is_symlink():
            if owned.is_dir() and not owned.is_symlink():
                raise QualityError("beautify_inventory.output_invalid", f"owned output path is a directory: {relative}", stage="beautify_inventory")
            owned.unlink()
    inputs, discovered = discover_inputs(project, task_id, runner_profile)
    project = discovered["project"]
    if discovered["images"] is not None:
        image_values = load_json(discovered["images"], root=list, rule="beautify_inventory.inputs")
        _validate_image_entries(project, image_values, inputs["source_slide_count"])
    if not inventory_script and skill_root:
        inventory_script = str(Path(skill_root) / "scripts" / "beautify_inventory.py")
    script = resolve_inventory_script(inventory_script)
    temp_root = project / ".slidesmith" / "tmp"
    temp_root.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix=f"beautify-inventory-{phase_run_id}-", dir=temp_root))
    try:
        output = stage / "beautify_inventory.json"
        command = [sys.executable, str(script), str(discovered["library"]), "-o", str(output)]
        if discovered["images"] is not None:
            command.extend(["--images", str(discovered["images"])])
        completed = subprocess.run(command, cwd=str(project), text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180)
        if completed.returncode != 0 or not output.is_file():
            raise QualityError("beautify_inventory.runner_failed", "beautify_inventory.py failed", stage="beautify_inventory")
        raw_inventory = load_json(output, rule="beautify_inventory.contract")
        if raw_inventory.get("schema") != INVENTORY_SCHEMA:
            raise QualityError("beautify_inventory.contract", "upstream beautify inventory schema is invalid", stage="beautify_inventory")
        inventory = _canonical_inventory(raw_inventory, inputs, discovered["package"], project)
        _validate_inventory(inventory, inputs["source_slide_count"], project)
        inputs["phase_run_id"] = phase_run_id
        atomic_json(project / ".slidesmith/contracts/beautify_inputs.json", inputs)
        inputs_sha = sha256_file(project / ".slidesmith/contracts/beautify_inputs.json")
        risk = _risk_enrichment(inventory, discovered["package"], inputs_sha)
        atomic_json(project / "analysis/beautify_inventory.json", inventory)
        risk["inventory_sha256"] = sha256_file(project / "analysis/beautify_inventory.json")
        atomic_json(project / "analysis/beautify_risk_report.json", risk)
        contract = {
            "schema": INVENTORY_CONTRACT_SCHEMA,
            "phase": "beautify_inventory",
            "task_id": task_id,
            "route": "beautify",
            "runner_profile": runner_profile,
            "phase_run_id": phase_run_id,
            "inputs_sha256": inputs_sha,
            "inventory_sha256": sha256_file(project / "analysis/beautify_inventory.json"),
            "risk_report_sha256": sha256_file(project / "analysis/beautify_risk_report.json"),
            "source_pptx_sha256": inputs["source_pptx"]["sha256"],
            "slide_count": inputs["source_slide_count"],
            "pages": [
                {
                    "slide_index": page["source_slide"], "content_data_sha256": page["content_sha256"],
                    "text_ids": sorted(item["id"] for item in inventory["slides"][page["source_slide"] - 1]["text_blocks"]),
                    "table_ids": sorted(item["id"] for item in inventory["slides"][page["source_slide"] - 1]["tables"]),
                    "chart_ids": sorted(item["id"] for item in inventory["slides"][page["source_slide"] - 1]["charts"]),
                    "image_ids": sorted(item["id"] for item in inventory["slides"][page["source_slide"] - 1]["images"]),
                }
                for page in risk["pages"]
            ],
            "risk_ids": sorted(item["id"] for item in risk["risks"]),
            "summary": risk["summary"],
            "checked_at": utc_now(),
        }
        atomic_json(project / ".slidesmith/contracts/beautify_inventory.json", contract)
        return contract
    finally:
        shutil.rmtree(stage, ignore_errors=True)


def _lock_pages(lock: dict[str, Any]) -> list[dict[str, Any]]:
    pages = lock.get("pages")
    if not isinstance(pages, list):
        pages = lock.get("slides")
    if not isinstance(pages, list) or not pages:
        raise QualityError("beautify_fidelity.lock_invalid", "beautify lock has no frozen pages", stage="pptx_validate")
    result: list[dict[str, Any]] = []
    for index, raw in enumerate(pages, start=1):
        if not isinstance(raw, dict):
            raise QualityError("beautify_fidelity.lock_invalid", "beautify lock page is invalid", stage="pptx_validate")
        source_slide = int(raw.get("source_slide") or raw.get("slide_index") or index)
        output_page = int(raw.get("output_page") or index)
        if source_slide != index or output_page != index:
            raise QualityError("beautify_fidelity.page_order", "beautify lock page mapping must be strict 1:1", stage="pptx_validate")
        result.append(raw)
    return result


def _text_units(page: dict[str, Any]) -> list[dict[str, str]]:
    raw_units = page.get("text_units")
    if not isinstance(raw_units, list):
        raw_units = page.get("text_blocks")
    if not isinstance(raw_units, list):
        raw_units = []
    units: list[dict[str, str]] = []
    for index, raw in enumerate(raw_units, start=1):
        if isinstance(raw, str):
            units.append({"id": f"text-{index}", "text": raw})
        elif isinstance(raw, dict):
            units.append({"id": str(raw.get("id") or raw.get("slot_id") or f"text-{index}"), "text": str(raw.get("text") or "")})
    return [unit for unit in units if fidelity_key(unit["text"])]


def _item_id(raw: dict[str, Any], kind: str, index: int) -> str:
    keys = {"table": ("table_id", "id"), "chart": ("chart_id", "id"), "image": ("occurrence_id", "id", "resource_id")}[kind]
    return next((str(raw[key]) for key in keys if raw.get(key)), f"{kind}-{index}")


def _content_payload(raw: dict[str, Any], kind: str) -> Any:
    if kind == "table":
        return {"cells": raw.get("cells") or raw.get("grid") or [], "row_count": raw.get("row_count"), "column_count": raw.get("column_count")}
    if kind == "chart":
        return {"categories": raw.get("categories") or [], "series": raw.get("series") or []}
    return {
        "id": raw.get("id") or raw.get("occurrence_id"),
        "filename": raw.get("filename"),
        "source_occurrence": raw.get("source_occurrence"),
        "source_path": raw.get("source_path"),
        "sha256": raw.get("sha256"),
        "size": raw.get("size"),
        "required": bool(raw.get("required", True)),
    }


def _resource_output_is_live(project: Path, resource: dict[str, Any]) -> bool:
    output = resource.get("output")
    if not isinstance(output, dict) or not output.get("path") or not output.get("sha256"):
        return False
    try:
        path = _regular_project_file(project, str(output["path"]), rule="beautify_fidelity.svg_receipt_invalid")
    except QualityError:
        return False
    return sha256_file(path) == output["sha256"]


def _receipt_index(project: Path, lock_sha: str, pages: list[dict[str, Any]]) -> dict[tuple[str, str, str], dict[str, Any]]:
    result: dict[tuple[str, str, str], dict[str, Any]] = {}
    manifest_path = project / ".slidesmith/resources_manifest.json"
    resource_usage_path = project / "analysis/svg_resource_usage.json"
    chart_usage_path = project / "analysis/chart_usage.json"
    manifest: dict[str, Any] = {}
    resources: dict[str, dict[str, Any]] = {}
    if manifest_path.is_file():
        manifest = load_json(manifest_path, rule="beautify_fidelity.svg_receipt_invalid")
        resources = {
            str(item.get("id") or ""): item
            for item in manifest.get("resources", [])
            if isinstance(item, dict) and item.get("id")
        }
    if resource_usage_path.is_file() and resources:
        usage = load_json(resource_usage_path, rule="beautify_fidelity.svg_receipt_invalid")
        bindings_by_page = {
            str(page.get("page_id") or ""): {
                str(item.get("resource_id") or "")
                for item in page.get("resources", [])
                if isinstance(item, dict)
            }
            for page in usage.get("pages", [])
            if isinstance(page, dict)
        }
        for page_index, page in enumerate(pages, start=1):
            page_id = f"P{page_index:02d}"
            for item_index, image in enumerate(page.get("images", []), start=1):
                if not isinstance(image, dict):
                    continue
                item_id = _item_id(image, "image", item_index)
                source_path = str(image.get("source_path") or "")
                matches = [
                    resource_id for resource_id, resource in resources.items()
                    if resource.get("type") == "image"
                    and int(resource.get("page") or 0) == page_index
                    and str((resource.get("input") or {}).get("source_reference") or "") == source_path
                    and resource_id in bindings_by_page.get(page_id, set())
                    and resource.get("status") == "ready"
                    and _resource_output_is_live(project, resource)
                ]
                if len(matches) == 1:
                    result[(page_id, "image", item_id)] = {
                        "content_sha256": canonical_sha(_content_payload(image, "image")),
                        "decision": "pass",
                        "resource_id": matches[0],
                    }
    if chart_usage_path.is_file() and resources:
        usage = load_json(chart_usage_path, rule="beautify_fidelity.svg_receipt_invalid")
        charts_by_key = {
            (str(item.get("page_id") or ""), str(item.get("chart_id") or "")): item
            for item in usage.get("charts", [])
            if isinstance(item, dict)
        }
        for page_index, page in enumerate(pages, start=1):
            page_id = f"P{page_index:02d}"
            for item_index, chart in enumerate(page.get("charts", []), start=1):
                if not isinstance(chart, dict):
                    continue
                item_id = _item_id(chart, "chart", item_index)
                usage_item = charts_by_key.get((page_id, item_id))
                resource = resources.get(str((usage_item or {}).get("data_resource_id") or ""))
                output = (resource or {}).get("output") if isinstance(resource, dict) else None
                if not isinstance(output, dict) or not output.get("path") or not output.get("sha256"):
                    continue
                try:
                    data_path = _regular_project_file(project, str(output["path"]), rule="beautify_fidelity.svg_receipt_invalid")
                    if sha256_file(data_path) != output["sha256"] or sha256_file(data_path) != usage_item.get("data_sha256"):
                        continue
                    payload = load_json(data_path, rule="beautify_fidelity.svg_receipt_invalid")
                except QualityError:
                    continue
                data = payload.get("data") if isinstance(payload.get("data"), dict) else payload
                actual = {"categories": data.get("categories") or [], "series": data.get("series") or []}
                expected = _content_payload(chart, "chart")
                if canonical_sha(actual) == canonical_sha(expected):
                    result[(page_id, "chart", item_id)] = {"content_sha256": canonical_sha(expected), "decision": "pass"}
    path = project / "analysis/beautify_svg_fidelity.json"
    if not path.exists():
        return result
    path = _regular_project_file(project, "analysis/beautify_svg_fidelity.json", rule="beautify_fidelity.svg_receipt_invalid")
    value = load_json(path, rule="beautify_fidelity.svg_receipt_invalid")
    if value.get("schema") != SVG_FIDELITY_SCHEMA or value.get("beautify_lock_sha256") != lock_sha:
        raise QualityError("beautify_fidelity.svg_receipt_stale", "SVG fidelity receipt is stale", stage="pptx_validate")
    for page in value.get("pages", []):
        if not isinstance(page, dict):
            continue
        page_id = str(page.get("page_id") or "")
        for kind in ("tables", "charts", "images"):
            singular = kind[:-1]
            for raw in page.get(kind) or []:
                if isinstance(raw, dict):
                    result[(page_id, singular, _item_id(raw, singular, 0))] = raw
    return result


def _pptx_canvas(path: Path) -> dict[str, float]:
    try:
        with zipfile.ZipFile(path) as package:
            root = _safe_xml(package, "ppt/presentation.xml")
    except (OSError, zipfile.BadZipFile, QualityError):
        return {}
    size = root.find(f"{{{P_NS}}}sldSz")
    if size is None:
        return {}
    try:
        width = int(str(size.get("cx") or "0")) * 96 / 914400
        height = int(str(size.get("cy") or "0")) * 96 / 914400
    except ValueError:
        return {}
    return {"width": width, "height": height}


def _confirmation_fonts(value: Any) -> set[str]:
    fonts: set[str] = set()
    if isinstance(value, dict):
        for key, child in value.items():
            if key in {"cjk", "latin"} and isinstance(child, str) and child.strip():
                fonts.add(child.strip())
            else:
                fonts.update(_confirmation_fonts(child))
    elif isinstance(value, list):
        for child in value:
            fonts.update(_confirmation_fonts(child))
    return fonts


def build_fidelity_report(
    project: Path,
    pptx_runs: list[list[str]],
    output_pptx: Path,
    phase_run_id: str,
    *,
    pptx_fonts: list[str] | None = None,
) -> tuple[dict[str, Any], list[dict[str, Any]], dict[str, Any]]:
    project = _project_root(project)
    lock_path = _regular_project_file(project, ".slidesmith/beautify_lock.json", rule="beautify_fidelity.lock_invalid")
    inputs_path = _regular_project_file(project, ".slidesmith/contracts/beautify_inputs.json", rule="beautify_fidelity.inputs_invalid")
    inventory_path = _regular_project_file(project, "analysis/beautify_inventory.json", rule="beautify_fidelity.inventory_invalid")
    plan_path = _regular_project_file(project, "analysis/beautify_plan.json", rule="beautify_fidelity.plan_invalid")
    lock = load_json(lock_path, rule="beautify_fidelity.lock_invalid")
    inputs = load_json(inputs_path, rule="beautify_fidelity.inputs_invalid")
    if lock.get("schema") != LOCK_SCHEMA or inputs.get("schema") != INPUTS_SCHEMA:
        raise QualityError("beautify_fidelity.lock_invalid", "beautify lock/input schema is invalid", stage="pptx_validate")
    source_ref = inputs.get("source_pptx")
    if not isinstance(source_ref, dict):
        raise QualityError("beautify_fidelity.inputs_invalid", "beautify inputs have no source PPTX", stage="pptx_validate")
    source = _regular_project_file(project, str(source_ref.get("path") or ""), rule="beautify_fidelity.inputs_invalid")
    if sha256_file(source) != source_ref.get("sha256") or source.stat().st_size != int(source_ref.get("size") or 0):
        raise QualityError("beautify_fidelity.source_stale", "source PPTX changed after beautify inventory", stage="pptx_validate")
    if lock.get("source_pptx_sha256") and lock.get("source_pptx_sha256") != source_ref.get("sha256"):
        raise QualityError("beautify_fidelity.lock_stale", "beautify lock source PPTX hash is stale", stage="pptx_validate")
    inventory_sha = sha256_file(inventory_path)
    inputs_sha = sha256_file(inputs_path)
    plan_sha = sha256_file(plan_path)
    for aliases, actual in (
        (("inputs_sha256", "beautify_inputs_sha256"), inputs_sha),
        (("inventory_sha256", "beautify_inventory_sha256"), inventory_sha),
        (("plan_sha256", "beautify_plan_sha256"), plan_sha),
    ):
        expected = next((lock.get(field) for field in aliases if lock.get(field)), None)
        if expected and expected != actual:
            raise QualityError("beautify_fidelity.lock_stale", f"beautify lock {aliases[0]} is stale", stage="pptx_validate")
    pages = _lock_pages(lock)
    ignored_decisions = lock.get("ignored") or []
    unsupported_decisions = lock.get("unsupported") or []
    for page_index, page in enumerate(pages, start=1):
        for image_index, image in enumerate(page.get("images", []), start=1):
            if not isinstance(image, dict):
                continue
            image_id = _item_id(image, "image", image_index)
            source_path = str(image.get("source_path") or "")
            source_sha = str(image.get("sha256") or "")
            source_size = int(image.get("size") or 0)
            if not source_path or not SHA256_RE.fullmatch(source_sha) or source_size <= 0:
                raise QualityError("beautify_fidelity.source_image_stale", f"source image binding is incomplete: {image_id}", stage="pptx_validate")
            source_image = _regular_project_file(project, source_path, rule="beautify_fidelity.source_image_stale")
            if sha256_file(source_image) != source_sha or source_image.stat().st_size != source_size:
                raise QualityError("beautify_fidelity.source_image_stale", f"source image changed after lock: {image_id}", stage="pptx_validate")
    lock_sha = sha256_file(lock_path)
    receipts = _receipt_index(project, lock_sha, pages)
    findings: list[dict[str, Any]] = []
    page_reports: list[dict[str, Any]] = []
    for index, page in enumerate(pages, start=1):
        page_id = f"P{index:02d}"
        aggregate = normalize_text(" ".join(pptx_runs[index - 1] if index <= len(pptx_runs) else []))
        aggregate_key = fidelity_key(aggregate)
        cursor = 0
        missing: list[str] = []
        matched = 0
        units = _text_units(page)
        for unit in units:
            key = fidelity_key(unit["text"])
            position = aggregate_key.find(key, cursor)
            if position < 0:
                missing.append(unit["id"])
                findings.append(finding(
                    rule="beautify.text_missing_or_reordered", severity="error", stage="pptx_post_export",
                    message="Frozen source text is missing, changed, or reordered on its output page", page_id=page_id,
                    artifact=f"ppt/slides/slide{index}.xml", element_ids=[unit["id"]], evidence={"text": unit["text"][:120]},
                    owner_phase="svg_execute", retry_phase="svg_execute",
                ))
            else:
                cursor = position + len(key)
                matched += 1
        known_text = [fidelity_key(unit["text"]) for unit in units]
        for table in page.get("tables", []):
            if isinstance(table, dict):
                known_text.extend(
                    fidelity_key(cell)
                    for row in (table.get("cells") or [])
                    if isinstance(row, list)
                    for cell in row
                    if fidelity_key(cell)
                )
        has_chart = False
        for chart in page.get("charts", []):
            if not isinstance(chart, dict):
                continue
            has_chart = True
            known_text.extend(fidelity_key(item) for item in chart.get("categories", []) if fidelity_key(item))
            for series in chart.get("series", []):
                if not isinstance(series, dict):
                    continue
                if fidelity_key(series.get("name")):
                    known_text.append(fidelity_key(series["name"]))
                known_text.extend(fidelity_key(item) for item in series.get("values", []) if fidelity_key(item))
        unexpected: list[str] = []
        for run in (pptx_runs[index - 1] if index <= len(pptx_runs) else []):
            run_key = fidelity_key(run)
            if not run_key:
                continue
            if any(run_key in expected or expected in run_key for expected in known_text):
                continue
            if has_chart and re.fullmatch(r"[-+−]?\d+(?:[.,]\d+)?%?", run_key):
                continue
            unexpected.append(normalize_text(run)[:120])
        if unexpected:
            findings.append(finding(
                rule="beautify.text_added_or_changed", severity="error", stage="pptx_post_export",
                message="Output page contains visible text not present in the frozen source contract", page_id=page_id,
                artifact=f"ppt/slides/slide{index}.xml", evidence={"text": unexpected},
                owner_phase="svg_execute", retry_phase="svg_execute",
            ))
        item_reports: dict[str, Any] = {}
        for plural, singular in (("tables", "table"), ("charts", "chart"), ("images", "image")):
            expected_items = page.get(plural, [])
            if not isinstance(expected_items, list):
                expected_items = []
            if singular == "image":
                ignored_ids = {
                    str(item.get("id") or "")
                    for item in ignored_decisions + unsupported_decisions
                    if isinstance(item, dict) and int(item.get("slide_index") or 0) == index
                }
                expected_items = [
                    item for item in expected_items
                    if isinstance(item, dict) and bool(item.get("required", True)) and _item_id(item, "image", 0) not in ignored_ids
                ]
            matches = 0
            mismatches: list[str] = []
            for item_index, raw in enumerate(expected_items, start=1):
                if not isinstance(raw, dict):
                    continue
                item_id = _item_id(raw, singular, item_index)
                expected_sha = canonical_sha(_content_payload(raw, singular))
                receipt = receipts.get((page_id, singular, item_id))
                if receipt is None and singular == "table":
                    table_cursor = 0
                    table_ok = True
                    table_tokens = [
                        fidelity_key(cell)
                        for row in (raw.get("cells") or [])
                        if isinstance(row, list)
                        for cell in row
                        if fidelity_key(cell)
                    ]
                    for token in table_tokens:
                        position = aggregate_key.find(token, table_cursor)
                        if position < 0:
                            table_ok = False
                            break
                        table_cursor = position + len(token)
                    if table_tokens and table_ok:
                        receipt = {"content_sha256": expected_sha, "decision": "pass", "evidence": "pptx_table_cell_text"}
                actual_sha = str((receipt or {}).get("content_sha256") or "")
                if receipt and actual_sha == expected_sha and str(receipt.get("decision") or "pass") in {"pass", "pass_with_warnings"}:
                    matches += 1
                    continue
                mismatches.append(item_id)
                findings.append(finding(
                    rule=f"beautify.{singular}_fidelity", severity="error", stage="pptx_post_export",
                    message=f"Frozen source {singular} is missing or changed in the SVG/export chain", page_id=page_id,
                    artifact="analysis/beautify_svg_fidelity.json", element_ids=[item_id], evidence={"expected_sha256": expected_sha, "actual_sha256": actual_sha},
                    owner_phase="svg_execute", retry_phase="svg_execute",
                ))
            if singular == "image":
                item_reports[plural] = {
                    "required": len(expected_items), "used": matches, "missing": mismatches,
                    "source_bindings": [
                        {"id": _item_id(item, "image", item_index), "sha256": item.get("sha256"), "size": item.get("size")}
                        for item_index, item in enumerate(expected_items, start=1)
                        if isinstance(item, dict)
                    ],
                }
            else:
                item_reports[plural] = {"expected": len(expected_items), "matched": matches, "mismatches": mismatches}
        text_report = {"expected": len(units), "matched": matched, "missing": missing, "changed": unexpected, "reordered": list(missing)}
        page_decision = "fail" if missing or unexpected or item_reports["tables"]["mismatches"] or item_reports["charts"]["mismatches"] or item_reports["images"]["missing"] else "pass"
        page_reports.append({"source_slide": index, "output_page": index, "text": text_report, **item_reports, "decision": page_decision})
    if len(pptx_runs) != len(pages):
        findings.append(finding(
            rule="beautify.page_count", severity="error", stage="pptx_post_export", message="Source and output page counts differ",
            evidence={"source": len(pages), "output": len(pptx_runs)}, owner_phase="finalize_export", retry_phase="finalize_export",
        ))
    confirmed_identity = lock.get("identity") if isinstance(lock.get("identity"), dict) else {}
    confirmation: dict[str, Any] = {}
    confirmation_path = project / "confirm_ui/result.json"
    if confirmation_path.is_file():
        confirmation = load_json(confirmation_path, rule="beautify_fidelity.lock_stale")
        if lock.get("confirmation_sha256") and sha256_file(confirmation_path) != lock.get("confirmation_sha256"):
            raise QualityError("beautify_fidelity.lock_stale", "beautify confirmation changed after lock", stage="pptx_validate")
    expected_fonts = _confirmation_fonts(confirmation.get("typography"))
    actual_fonts = set(pptx_fonts or [])
    substitutions = sorted(expected_fonts - actual_fonts) if actual_fonts else []
    if substitutions:
        findings.append(finding(
            rule="beautify.font_substitution", severity="warning", stage="pptx_post_export", message="Confirmed source fonts are absent from output font declarations",
            evidence={"fonts": substitutions}, owner_phase="finalize_export", retry_phase="finalize_export",
        ))
    expected_canvas = lock.get("canvas") if isinstance(lock.get("canvas"), dict) else {}
    output_canvas = _pptx_canvas(output_pptx)
    if expected_canvas and output_canvas:
        expected_width = float(expected_canvas.get("width") or expected_canvas.get("width_px") or 0)
        expected_height = float(expected_canvas.get("height") or expected_canvas.get("height_px") or 0)
        if expected_width > 0 and expected_height > 0 and (
            abs(output_canvas["width"] - expected_width) > 1.0 or abs(output_canvas["height"] - expected_height) > 1.0
        ):
            findings.append(finding(
                rule="beautify.canvas_mismatch", severity="error", stage="pptx_post_export",
                message="Output PPTX canvas differs from the immutable Beautify lock",
                evidence={"expected": [expected_width, expected_height], "actual": [round(output_canvas["width"], 2), round(output_canvas["height"], 2)]},
                owner_phase="finalize_export", retry_phase="finalize_export",
            ))
    overrides = [
        name for name, field in (("canvas", "canvas_override"), ("palette", "palette_override"), ("typography", "typography_override"))
        if bool(confirmed_identity.get(field))
    ]
    summary = summarize(findings)
    report = {
        "schema": FIDELITY_SCHEMA,
        "task_id": str(inputs.get("task_id") or ""),
        "phase_run_id": phase_run_id,
        "source_pptx_sha256": source_ref["sha256"],
        "output_pptx_sha256": sha256_file(output_pptx),
        "beautify_lock_sha256": lock_sha,
        "source_slide_count": len(pages),
        "output_slide_count": len(pptx_runs),
        "pages": page_reports,
        "identity": {
            "selected_source": confirmed_identity.get("source") or confirmed_identity.get("selected_source") or "source",
            "overrides": overrides or confirmed_identity.get("overrides", []),
            "font_substitutions": substitutions,
        },
        "ignored": ignored_decisions, "unsupported": unsupported_decisions,
        "findings": findings, "summary": summary, "decision": summary["decision"], "checked_at": utc_now(),
    }
    bindings = {
        "beautify_inputs_sha256": inputs_sha, "beautify_inventory_sha256": inventory_sha,
        "beautify_plan_sha256": plan_sha, "beautify_lock_sha256": lock_sha,
        "source_pptx_sha256": source_ref["sha256"], "output_pptx_sha256": report["output_pptx_sha256"],
        "source_slide_count": len(pages), "output_slide_count": len(pptx_runs),
    }
    return report, findings, bindings


def main() -> int:
    parser = argparse.ArgumentParser(description="SlideSmith SPEC-08 Beautify runtime contracts")
    sub = parser.add_subparsers(dest="command", required=True)
    inventory = sub.add_parser("inventory")
    inventory.add_argument("project")
    inventory.add_argument("--task-id", required=True)
    inventory.add_argument("--runner-profile", default="full-ppt-master")
    inventory.add_argument("--phase-run-id", required=True)
    inventory.add_argument("--inventory-script", default="")
    inventory.add_argument("--skill-root", default="")
    args = parser.parse_args()
    try:
        contract = run_inventory(
            Path(args.project), args.task_id, args.runner_profile, args.phase_run_id,
            args.inventory_script, args.skill_root,
        )
    except QualityError as exc:
        print(json.dumps({"status": "failed", "rule": exc.rule, "message": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 0
    except Exception as exc:
        print(json.dumps({"status": "failed", "rule": "beautify.runner_exception", "message": type(exc).__name__}, ensure_ascii=False), file=sys.stderr)
        return 0
    print(json.dumps({"status": "passed", "contract": contract}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
