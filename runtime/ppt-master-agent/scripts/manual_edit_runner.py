#!/usr/bin/env python3
"""Deterministic, network-free structured SVG editor for SlideSmith SPEC-09."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import tempfile
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any

SVG_NS = "http://www.w3.org/2000/svg"
ET.register_namespace("", SVG_NS)

ALLOWED_OPERATIONS = {
    "set_text", "translate", "set_fill", "set_stroke", "set_opacity",
    "set_font_size", "set_font_family", "set_font_weight", "set_text_anchor",
}
COLOR_RE = re.compile(r"^#[0-9a-fA-F]{3}(?:[0-9a-fA-F]{3})?(?:[0-9a-fA-F]{2})?$")
FONT_RE = re.compile(r"^[\w\s,'\"-]{1,160}$", re.UNICODE)
NUMBER_RE = re.compile(r"^\s*(-?(?:\d+(?:\.\d*)?|\.\d+))(.*)$")
TRANSLATE_RE = re.compile(r"^\s*(?:translate\(\s*-?[\d.]+(?:[ ,]+-?[\d.]+)?\s*\)\s*)*$")


class EditError(RuntimeError):
    pass


def local_name(tag: str) -> str:
    return tag.split("}", 1)[1] if "}" in tag else tag


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def normalize_text(value: str) -> str:
    return " ".join(value.split())


def assign_editor_ids(root: ET.Element) -> tuple[dict[str, ET.Element], dict[ET.Element, str]]:
    seen: set[str] = set()
    by_id: dict[str, ET.Element] = {}
    source_ids: dict[ET.Element, str] = {}
    for element in root.iter():
        source_id = (element.get("id") or "").strip()
        if source_id.startswith("_edit_"):
            element.attrib.pop("id", None)
            source_id = ""
        if source_id:
            if source_id in seen:
                raise EditError(f"duplicate SVG id {source_id!r}")
            seen.add(source_id)
            source_ids[element] = source_id
    counter = 0
    for element in root.iter():
        if element is root:
            continue
        editor_id = source_ids.get(element, "")
        if not editor_id:
            editor_id = f"_edit_{counter}"
            counter += 1
            element.set("id", editor_id)
        by_id[editor_id] = element
    return by_id, source_ids


def parent_map(root: ET.Element) -> dict[ET.Element, ET.Element]:
    return {child: parent for parent in root.iter() for child in parent}


def fingerprint(element: ET.Element, parents: dict[ET.Element, ET.Element], source_ids: dict[ET.Element, str]) -> str:
    pairs = []
    for key, value in element.attrib.items():
        name = local_name(key).lower()
        if name.startswith("data-editor-") or name == "href" or name.endswith(":href"):
            continue
        # Synthetic editor IDs are transport-only selectors. The Go preview
        # fingerprint is computed before those IDs are exposed to the client,
        # while authored source IDs remain part of the immutable identity.
        if name == "id" and element not in source_ids:
            continue
        pairs.append({"name": name, "value": value})
    pairs.sort(key=lambda item: (item["name"], item["value"]))
    parent = parents.get(element)
    parent_signature = ""
    if parent is not None:
        parent_id = source_ids.get(parent, parent.get("id") or "")
        parent_signature = f"{local_name(parent.tag).lower()}#{parent_id}"
    payload = {
        "attrs": pairs,
        "children": [local_name(child.tag).lower() for child in list(element)],
        "parent": parent_signature,
        "tag": local_name(element.tag).lower(),
        "text": normalize_text("".join(element.itertext())),
    }
    raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode("utf-8")
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def finite_number(value: Any, field: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise EditError(f"{field} must be a number")
    number = float(value)
    if not math.isfinite(number):
        raise EditError(f"{field} must be finite")
    return number


def format_number(value: float) -> str:
    text = f"{value:.6f}".rstrip("0").rstrip(".")
    return text or "0"


def add_to_numeric_attr(element: ET.Element, attr: str, delta: float) -> bool:
    current = element.get(attr)
    if current is None:
        return False
    match = NUMBER_RE.match(current)
    if not match:
        raise EditError(f"attribute {attr} is not a simple numeric value")
    element.set(attr, format_number(float(match.group(1)) + delta) + match.group(2))
    return True


def apply_translate(element: ET.Element, dx: float, dy: float) -> None:
    tag = local_name(element.tag).lower()
    changed = False
    if tag in {"text", "tspan", "rect", "image", "use"}:
        changed = add_to_numeric_attr(element, "x", dx) | add_to_numeric_attr(element, "y", dy)
    elif tag in {"circle", "ellipse"}:
        changed = add_to_numeric_attr(element, "cx", dx) | add_to_numeric_attr(element, "cy", dy)
    elif tag == "line":
        changed = add_to_numeric_attr(element, "x1", dx) | add_to_numeric_attr(element, "y1", dy)
        changed = add_to_numeric_attr(element, "x2", dx) | add_to_numeric_attr(element, "y2", dy) | changed
    if changed:
        return
    transform = (element.get("transform") or "").strip()
    if transform and not TRANSLATE_RE.fullmatch(transform):
        raise EditError("complex transform cannot be safely translated")
    suffix = f"translate({format_number(dx)} {format_number(dy)})"
    element.set("transform", f"{transform} {suffix}".strip())


def operation_value(operation: dict[str, Any], field: str) -> Any:
    values = operation.get("value")
    if not isinstance(values, dict) or field not in values:
        raise EditError(f"operation value {field!r} is required")
    return values[field]


def apply_operation(element: ET.Element, operation: dict[str, Any]) -> None:
    operation_type = operation.get("type")
    if operation_type not in ALLOWED_OPERATIONS:
        raise EditError(f"unsupported operation {operation_type!r}")
    tag = local_name(element.tag).lower()
    allowed_tags = {
        "set_text": {"text", "tspan"},
        "translate": {"g", "text", "tspan", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "use", "image"},
        "set_fill": {"text", "tspan", "rect", "circle", "ellipse", "polygon", "path"},
        "set_stroke": {"text", "tspan", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path"},
        "set_opacity": {"g", "text", "tspan", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "use", "image"},
        "set_font_size": {"text", "tspan"}, "set_font_family": {"text", "tspan"},
        "set_font_weight": {"text", "tspan"}, "set_text_anchor": {"text", "tspan"},
    }
    if tag not in allowed_tags[operation_type]:
        raise EditError(f"operation {operation_type} does not support <{tag}>")
    if operation_type == "set_text":
        if tag not in {"text", "tspan"}:
            raise EditError("set_text target must be text or tspan")
        if any(local_name(child.tag).lower() == "tspan" for child in element):
            raise EditError("set_text target has tspan children")
        value = operation_value(operation, "text")
        if not isinstance(value, str) or len(value.encode("utf-8")) > 5000:
            raise EditError("text value is invalid")
        element.text = value
        return
    if operation_type == "translate":
        apply_translate(element, finite_number(operation_value(operation, "dx"), "dx"), finite_number(operation_value(operation, "dy"), "dy"))
        return
    attr = operation_type.removeprefix("set_").replace("_", "-")
    field = operation_type.removeprefix("set_")
    value = operation_value(operation, field)
    if operation_type in {"set_fill", "set_stroke"}:
        if not isinstance(value, str) or not (COLOR_RE.fullmatch(value) or value.lower() in {"none", "black", "white", "red", "green", "blue", "gray", "grey", "transparent"}):
            raise EditError("color value is invalid")
    elif operation_type == "set_opacity":
        value = finite_number(value, field)
        if value < 0 or value > 1:
            raise EditError("opacity is outside 0..1")
        value = format_number(value)
    elif operation_type == "set_font_size":
        value = finite_number(value, field)
        if value <= 0:
            raise EditError("font size must be positive")
        value = format_number(value)
    elif operation_type == "set_font_family":
        if not isinstance(value, str) or not FONT_RE.fullmatch(value):
            raise EditError("font family is invalid")
    elif operation_type == "set_font_weight":
        if isinstance(value, (int, float)):
            value = finite_number(value, field)
            if value < 100 or value > 900 or value % 100:
                raise EditError("font weight is invalid")
            value = format_number(value)
        elif value not in {"normal", "bold", "lighter", "bolder", "100", "200", "300", "400", "500", "600", "700", "800", "900"}:
            raise EditError("font weight is invalid")
    elif operation_type == "set_text_anchor" and value not in {"start", "middle", "end"}:
        raise EditError("text anchor is invalid")
    element.set(attr, str(value))


def atomic_write_xml(tree: ET.ElementTree, path: Path) -> None:
    fd, temporary = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp", dir=path.parent)
    os.close(fd)
    try:
        tree.write(temporary, encoding="utf-8", xml_declaration=True)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    raw = json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    path.write_text(raw, encoding="utf-8")


def run(args: argparse.Namespace) -> dict[str, Any]:
    project = args.project.resolve()
    if not project.is_dir():
        raise EditError("project path is not a directory")
    patch_path = (project / args.patch).resolve()
    if project not in patch_path.parents or not patch_path.is_file():
        raise EditError("patch path is not a contained file")
    patch = json.loads(patch_path.read_text(encoding="utf-8"))
    if patch.get("schema") != "slidesmith.manual_edit_draft.v1" or patch.get("task_id") != args.task_id or patch.get("edit_session_id") != args.session_id:
        raise EditError("patch identity binding mismatch")
    patch_sha = hashlib.sha256(patch_path.read_bytes()).hexdigest()
    operations_report: list[dict[str, Any]] = []
    page_hashes: list[dict[str, Any]] = []
    requested = 0
    for page in patch.get("pages", []):
        page_id = page.get("page_id", "")
        candidates = sorted((project / "svg_output").glob(f"{int(page_id[1:]):02d}_*.svg")) if re.fullmatch(r"P\d{2}", page_id) else []
        if len(candidates) != 1:
            raise EditError(f"page {page_id!r} does not bind to exactly one authored SVG")
        svg_path = candidates[0]
        before_sha = sha256_file(svg_path)
        if before_sha != page.get("base_svg_sha256"):
            raise EditError(f"page {page_id} base SVG hash mismatch")
        tree = ET.parse(svg_path)
        root = tree.getroot()
        by_id, source_ids = assign_editor_ids(root)
        parents = parent_map(root)
        base_fingerprints = {editor_id: fingerprint(element, parents, source_ids) for editor_id, element in by_id.items()}
        for operation in page.get("operations", []):
            requested += 1
            target = operation.get("target") or {}
            element_id = target.get("element_id", "")
            element = by_id.get(element_id)
            if element is None:
                raise EditError(f"operation {operation.get('operation_id')} target was not found")
            if local_name(element.tag).lower() != target.get("tag"):
                raise EditError(f"operation {operation.get('operation_id')} target tag mismatch")
            before_fingerprint = fingerprint(element, parents, source_ids)
            if base_fingerprints[element_id] != target.get("element_fingerprint"):
                raise EditError(f"operation {operation.get('operation_id')} target fingerprint mismatch")
            before_state = ET.tostring(element, encoding="utf-8")
            apply_operation(element, operation)
            after_state = ET.tostring(element, encoding="utf-8")
            if before_state == after_state:
                raise EditError(f"operation {operation.get('operation_id')} is a no-op")
            after_fingerprint = fingerprint(element, parents, source_ids)
            operations_report.append({
                "operation_id": operation.get("operation_id"), "page_id": page_id,
                "element_id": element_id, "type": operation.get("type"),
                "base_fingerprint": base_fingerprints[element_id], "before_fingerprint": before_fingerprint, "after_fingerprint": after_fingerprint,
                "status": "applied",
            })
        for element, source_id in source_ids.items():
            if source_id:
                element.set("id", source_id)
        for element in root.iter():
            if (element.get("id") or "").startswith("_edit_") and element not in source_ids:
                element.attrib.pop("id", None)
        atomic_write_xml(tree, svg_path)
        after_sha = sha256_file(svg_path)
        page_hashes.append({"page_id": page_id, "before_sha256": before_sha, "after_sha256": after_sha})
    report = {
        "schema": "slidesmith.manual_edit_apply_report.v1", "task_id": args.task_id,
        "edit_session_id": args.session_id, "frozen_patch_sha256": patch_sha,
        "summary": {"requested": requested, "applied": len(operations_report), "rejected": 0},
        "operations": operations_report, "page_hashes": page_hashes, "passed": requested == len(operations_report),
    }
    write_json(project / args.report, report)
    annotation_report = {
        "schema": "slidesmith.annotation_apply_report.v1", "task_id": args.task_id,
        "edit_session_id": args.session_id, "requested": len(patch.get("annotations", [])),
        "applied": 0, "skipped": not patch.get("annotations"), "passed": not patch.get("annotations"),
    }
    write_json(project / "analysis/annotation_apply_report.json", annotation_report)
    return report


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("project", type=Path)
    parser.add_argument("--patch", default="analysis/manual_edit_patch.json")
    parser.add_argument("--task-id", required=True)
    parser.add_argument("--session-id", required=True)
    parser.add_argument("--report", default="analysis/manual_edit_apply_report.json")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report = run(args)
    except (EditError, OSError, ValueError, ET.ParseError, json.JSONDecodeError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
        return 2
    print(json.dumps({"ok": True, "report": report}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
