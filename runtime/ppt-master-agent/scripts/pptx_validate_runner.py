#!/usr/bin/env python3
"""Post-export PPTX package/read-back/render gate for SlideSmith SPEC-07."""

from __future__ import annotations

import argparse
import json
import os
import re
import signal
import shutil
import subprocess
import sys
import tempfile
import unicodedata
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path, PurePosixPath
from typing import Any
from urllib.parse import unquote, urlsplit

from PIL import Image, ImageChops, ImageDraw

from quality_schema import QualityError, atomic_json, finding, sha256_file, summarize, utc_now


REPORT_SCHEMA = "slidesmith.pptx_validate_report.v1"
TEXT_SCHEMA = "slidesmith.pptx_text_inventory.v1"
CONTRACT_SCHEMA = "slidesmith.pptx_validate_contract.v1"
REL_NS = "http://schemas.openxmlformats.org/package/2006/relationships"
DOC_REL_NS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
P_NS = "http://schemas.openxmlformats.org/presentationml/2006/main"
A_NS = "http://schemas.openxmlformats.org/drawingml/2006/main"
MAX_ZIP_ENTRIES = 5000
MAX_UNCOMPRESSED = 512 * 1024 * 1024
MAX_COMPRESSION_RATIO = 200


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise QualityError("pptx.input_invalid", f"cannot read {path.name}: {exc}", stage="pptx_validate") from exc
    if not isinstance(value, dict):
        raise QualityError("pptx.input_invalid", f"{path.name} root must be an object", stage="pptx_validate")
    return value


def safe_relative(value: str) -> str:
    posix = PurePosixPath(value)
    if not value or posix.is_absolute() or posix.as_posix() != value or any(part in {"", ".", ".."} for part in posix.parts):
        raise QualityError("pptx.path_invalid", f"invalid project path {value!r}", stage="pptx_validate")
    return value


def project_file(project: Path, relative: str) -> Path:
    relative = safe_relative(relative)
    candidate = project.joinpath(*PurePosixPath(relative).parts)
    current = project
    for part in PurePosixPath(relative).parts:
        current /= part
        if current.is_symlink():
            raise QualityError("pptx.symlink_forbidden", f"symlink is forbidden: {relative}", stage="pptx_validate")
    try:
        resolved = candidate.resolve(strict=True)
        resolved.relative_to(project)
    except (OSError, ValueError) as exc:
        raise QualityError("pptx.path_invalid", f"path missing or outside project: {relative}", stage="pptx_validate") from exc
    if not resolved.is_file() or resolved.stat().st_size <= 0:
        raise QualityError("pptx.input_invalid", f"file is empty or invalid: {relative}", stage="pptx_validate")
    return resolved


def normalize_text(value: str) -> str:
    value = unicodedata.normalize("NFKC", value or "")
    value = value.replace("\r\n", "\n").replace("\r", "\n").replace("\v", "\n")
    value = value.replace("\u00a0", " ").replace("\u2022", "•")
    return " ".join(value.split())


def fidelity_match_key(value: str) -> str:
    """Ignore layout-only whitespace while retaining report/readback text."""
    return re.sub(r"\s+", "", normalize_text(value))


def package_target(source_part: str, target: str) -> str:
    decoded = unquote(target).split("#", 1)[0]
    if "\\" in decoded or decoded.startswith("/"):
        raise QualityError("pptx.relationship_escape", f"relationship target escapes package: {target}", stage="pptx_validate")
    base = PurePosixPath(source_part).parent if source_part else PurePosixPath("")
    stack: list[str] = []
    for part in (base / decoded).parts:
        if part in {"", "."}:
            continue
        if part == "..":
            if not stack:
                raise QualityError("pptx.relationship_escape", f"relationship target escapes package: {target}", stage="pptx_validate")
            stack.pop()
        else:
            stack.append(part)
    if not stack:
        raise QualityError("pptx.relationship_escape", f"relationship target is empty: {target}", stage="pptx_validate")
    return "/".join(stack)


def rel_source(rels_name: str) -> str:
    path = PurePosixPath(rels_name)
    if rels_name == "_rels/.rels":
        return ""
    if len(path.parts) < 3 or path.parts[-2] != "_rels" or not path.name.endswith(".rels"):
        raise QualityError("pptx.relationship_invalid", f"invalid relationship part: {rels_name}", stage="pptx_validate")
    source_name = path.name.removesuffix(".rels")
    return (PurePosixPath(*path.parts[:-2]) / source_name).as_posix()


def read_xml(package: zipfile.ZipFile, name: str) -> ET.Element:
    try:
        raw = package.read(name)
        if b"<!DOCTYPE" in raw.upper() or b"<!ENTITY" in raw.upper():
            raise QualityError("pptx.xml_entity_forbidden", f"XML entities are forbidden: {name}", stage="pptx_validate")
        return ET.fromstring(raw)
    except KeyError as exc:
        raise QualityError("pptx.part_missing", f"missing package part: {name}", stage="pptx_validate") from exc
    except ET.ParseError as exc:
        raise QualityError("pptx.xml_invalid", f"invalid XML part: {name}", stage="pptx_validate") from exc


def validate_package(pptx: Path) -> tuple[list[str], list[list[str]], list[str]]:
    try:
        package = zipfile.ZipFile(pptx)
    except (OSError, zipfile.BadZipFile) as exc:
        raise QualityError("pptx.package_invalid", "PPTX is not a valid ZIP package", stage="pptx_validate") from exc
    with package:
        infos = package.infolist()
        names = [info.filename for info in infos]
        if len(infos) > MAX_ZIP_ENTRIES or len(names) != len(set(names)):
            raise QualityError("pptx.package_invalid", "PPTX has too many or duplicate ZIP entries", stage="pptx_validate")
        if any(name.startswith("/") or "\\" in name or ".." in PurePosixPath(name).parts for name in names):
            raise QualityError("pptx.package_escape", "PPTX contains an unsafe ZIP entry", stage="pptx_validate")
        if sum(info.file_size for info in infos) > MAX_UNCOMPRESSED:
            raise QualityError("pptx.zip_bomb", "PPTX uncompressed size exceeds limit", stage="pptx_validate")
        for info in infos:
            if info.file_size > 1024 * 1024 and info.compress_size > 0 and info.file_size / info.compress_size > MAX_COMPRESSION_RATIO:
                raise QualityError("pptx.zip_bomb", "PPTX compression ratio exceeds limit", stage="pptx_validate")
        required = {"[Content_Types].xml", "_rels/.rels", "ppt/presentation.xml", "ppt/_rels/presentation.xml.rels"}
        missing = required - set(names)
        if missing:
            raise QualityError("pptx.part_missing", f"missing required package parts: {sorted(missing)}", stage="pptx_validate")
        xml_names = [name for name in names if name.lower().endswith((".xml", ".rels"))]
        for name in xml_names:
            read_xml(package, name)
        relationships: dict[str, dict[str, str]] = {}
        fonts: set[str] = set()
        for name in xml_names:
            root = read_xml(package, name)
            for typeface in root.iter(f"{{{A_NS}}}latin"):
                if typeface.get("typeface"):
                    fonts.add(str(typeface.get("typeface")))
            if not name.endswith(".rels"):
                continue
            source = rel_source(name)
            by_id: dict[str, str] = {}
            for rel in root.findall(f"{{{REL_NS}}}Relationship"):
                rel_id = str(rel.get("Id") or "")
                target = str(rel.get("Target") or "")
                if not rel_id or not target:
                    raise QualityError("pptx.relationship_invalid", f"invalid relationship in {name}", stage="pptx_validate")
                if str(rel.get("TargetMode") or "").lower() == "external" or urlsplit(target).scheme:
                    raise QualityError("pptx.external_relationship", f"external relationship is forbidden in {name}", stage="pptx_validate")
                resolved = package_target(source, target)
                if resolved not in names:
                    raise QualityError("pptx.relationship_missing", f"relationship target is missing: {resolved}", stage="pptx_validate")
                by_id[rel_id] = resolved
            relationships[source] = by_id
        presentation = read_xml(package, "ppt/presentation.xml")
        presentation_rels = relationships.get("ppt/presentation.xml", {})
        slide_parts: list[str] = []
        for slide_id in presentation.findall(f".//{{{P_NS}}}sldId"):
            rel_id = str(slide_id.get(f"{{{DOC_REL_NS}}}id") or "")
            part = presentation_rels.get(rel_id, "")
            if not part.startswith("ppt/slides/slide") or part not in names:
                raise QualityError("pptx.slide_order_invalid", f"presentation slide relationship is invalid: {rel_id}", stage="pptx_validate")
            slide_parts.append(part)
        if not slide_parts or len(slide_parts) != len(set(slide_parts)):
            raise QualityError("pptx.slide_order_invalid", "PPTX slide order is empty or duplicated", stage="pptx_validate")
        page_text: list[list[str]] = []
        for part in slide_parts:
            root = read_xml(package, part)
            paragraphs: list[str] = []
            for paragraph in root.iter(f"{{{A_NS}}}p"):
                value = "".join(str(node.text or "") for node in paragraph.iter(f"{{{A_NS}}}t"))
                if normalize_text(value):
                    paragraphs.append(value)
            page_text.append(paragraphs)
        return slide_parts, page_text, sorted(fonts)


def svg_text_inventory(project: Path) -> tuple[list[dict[str, Any]], str]:
    inventory_path = project_file(project, "analysis/svg_inventory.json")
    inventory = load_json(inventory_path)
    pages = inventory.get("pages")
    if not isinstance(pages, list) or not pages:
        raise QualityError("pptx.svg_inventory_invalid", "SVG inventory has no pages", stage="pptx_validate")
    result: list[dict[str, Any]] = []
    for page in pages:
        if not isinstance(page, dict):
            raise QualityError("pptx.svg_inventory_invalid", "SVG inventory page is invalid", stage="pptx_validate")
        relative = str(page.get("path") or "")
        path = project_file(project, relative)
        if str(page.get("sha256") or "") != sha256_file(path):
            raise QualityError("pptx.svg_inventory_stale", f"SVG changed after quality gate: {relative}", stage="pptx_validate")
        try:
            root = ET.parse(path).getroot()
        except ET.ParseError as exc:
            raise QualityError("pptx.svg_invalid", f"cannot parse {relative}", stage="pptx_validate") from exc
        units: list[dict[str, Any]] = []
        for element in root.iter():
            if element.tag.rsplit("}", 1)[-1].lower() != "text":
                continue
            text = normalize_text("".join(element.itertext()))
            if not text:
                continue
            role = str(element.get("data-role") or "body").strip().lower()
            units.append(
                {
                    "element_id": str(element.get("id") or ""),
                    "text": text,
                    "normalized": text,
                    "role": role,
                    "required_fidelity": "warning" if len(text) <= 1 or role in {"page-number", "decorative"} else "error",
                }
            )
        result.append({"page_id": str(page.get("page_id") or ""), "svg": relative, "units": units, "aggregate": normalize_text(" ".join(unit["text"] for unit in units))})
    return result, sha256_file(inventory_path)


def compare_text(svg_pages: list[dict[str, Any]], pptx_text: list[list[str]]) -> tuple[list[dict[str, Any]], list[dict[str, Any]], float]:
    findings: list[dict[str, Any]] = []
    pages: list[dict[str, Any]] = []
    total = 0
    matched = 0
    for index, svg_page in enumerate(svg_pages):
        page_id = str(svg_page["page_id"])
        runs = pptx_text[index] if index < len(pptx_text) else []
        aggregate = normalize_text(" ".join(runs))
        aggregate_key = fidelity_match_key(aggregate)
        page_total = 0
        page_matched = 0
        missing: list[dict[str, Any]] = []
        for unit in svg_page["units"]:
            page_total += 1
            total += 1
            value = str(unit["normalized"])
            value_key = fidelity_match_key(value)
            if value_key and value_key in aggregate_key:
                page_matched += 1
                matched += 1
                continue
            missing.append(unit)
            severity = str(unit["required_fidelity"])
            findings.append(
                finding(
                    rule="pptx.text_missing",
                    severity=severity,
                    stage="pptx_post_export",
                    message="Visible SVG text is missing from the corresponding PPTX page",
                    page_id=page_id,
                    artifact=f"ppt/slides/slide{index + 1}.xml",
                    element_ids=[str(unit.get("element_id") or "")],
                    evidence={"role": unit.get("role"), "text": value[:120]},
                    owner_phase="finalize_export",
                    retry_phase="finalize_export",
                )
            )
        pages.append(
            {
                "page_id": page_id,
                "pptx_runs": runs,
                "pptx_aggregate": aggregate,
                "required_units": page_total,
                "matched_units": page_matched,
                "coverage": 1.0 if page_total == 0 else page_matched / page_total,
                "missing": missing,
            }
        )
    return pages, findings, 1.0 if total == 0 else matched / total


def run_command(argv: list[str], timeout: int) -> subprocess.CompletedProcess[str]:
    if not argv or Path(argv[0]).name not in {"soffice", "libreoffice", "pdfinfo", "pdftoppm"}:
        raise QualityError("pptx.render_tool_forbidden", "render executable is not allowlisted", stage="pptx_validate")
    try:
        process = subprocess.Popen(argv, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired as exc:
            os.killpg(process.pid, signal.SIGKILL)
            process.communicate()
            raise QualityError("pptx.render_timeout", f"render command timed out: {Path(argv[0]).name}", stage="pptx_validate") from exc
        return subprocess.CompletedProcess(argv, process.returncode, stdout, stderr)
    except OSError as exc:
        raise QualityError("pptx.render_tool_failure", f"render command failed: {Path(argv[0]).name}", stage="pptx_validate") from exc


def blank_image(path: Path) -> tuple[bool, float]:
    with Image.open(path) as image:
        rgb = image.convert("RGB")
        if rgb.width < 64 or rgb.height < 64:
            return True, 1.0
        thumbnail = rgb.copy()
        thumbnail.thumbnail((320, 180))
        colors = thumbnail.getcolors(maxcolors=thumbnail.width * thumbnail.height)
        if not colors:
            return False, 0.0
        dominant = max(count for count, _ in colors) / (thumbnail.width * thumbnail.height)
        extrema = ImageChops.difference(thumbnail, Image.new("RGB", thumbnail.size, thumbnail.getpixel((0, 0)))).getbbox()
        return dominant >= 0.9995 or extrema is None, dominant


def contact_sheet(slides: list[Path], output: Path, columns: int = 4) -> None:
    if not slides:
        raise QualityError("pptx.render_missing", "no rendered slides for contact sheet", stage="pptx_validate")
    thumbs: list[Image.Image] = []
    for slide in slides:
        with Image.open(slide) as image:
            thumb = image.convert("RGB")
            thumb.thumbnail((320, 180))
            canvas = Image.new("RGB", (340, 215), "white")
            canvas.paste(thumb, ((340 - thumb.width) // 2, 10))
            thumbs.append(canvas)
    rows = (len(thumbs) + columns - 1) // columns
    sheet = Image.new("RGB", (columns * 340, rows * 215), "#e5e7eb")
    draw = ImageDraw.Draw(sheet)
    for index, thumb in enumerate(thumbs):
        x, y = (index % columns) * 340, (index // columns) * 215
        sheet.paste(thumb, (x, y))
        draw.text((x + 8, y + 195), f"P{index + 1:02d}", fill="#111827")
    output.parent.mkdir(parents=True, exist_ok=True)
    sheet.save(output, "PNG")


def render_pptx(pptx: Path, output: Path, phase_run_id: str, expected_pages: int, *, soffice: str, pdfinfo: str, pdftoppm: str, timeout: int) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    output.mkdir(parents=True, exist_ok=True)
    profile = output.parent / f"lo-profile-{re.sub(r'[^A-Za-z0-9_.-]', '_', phase_run_id)}"
    profile.mkdir(parents=True, exist_ok=True)
    result = run_command(
        [soffice, "--headless", f"-env:UserInstallation={profile.resolve().as_uri()}", "--convert-to", "pdf", "--outdir", str(output), str(pptx)],
        timeout,
    )
    if result.returncode != 0:
        raise QualityError("pptx.render_failed", f"LibreOffice exited {result.returncode}", stage="pptx_validate")
    generated = output / f"{pptx.stem}.pdf"
    if not generated.is_file() or generated.stat().st_size <= 0:
        raise QualityError("pptx.render_missing", "LibreOffice did not create a fresh PDF", stage="pptx_validate")
    canonical_pdf = output / "output.pdf"
    if generated != canonical_pdf:
        generated.replace(canonical_pdf)
    info = run_command([pdfinfo, str(canonical_pdf)], timeout)
    if info.returncode != 0:
        raise QualityError("pptx.render_failed", "pdfinfo failed", stage="pptx_validate")
    match = re.search(r"(?im)^Pages:\s+(\d+)\s*$", info.stdout)
    if not match or int(match.group(1)) != expected_pages:
        raise QualityError("pptx.render_page_count", "rendered PDF page count differs from PPTX", stage="pptx_validate")
    raster = run_command([pdftoppm, "-png", "-r", "150", str(canonical_pdf), str(output / "slide")], timeout)
    if raster.returncode != 0:
        raise QualityError("pptx.render_failed", "pdftoppm failed", stage="pptx_validate")
    produced = sorted(output.glob("slide-*.png"), key=lambda path: int(re.search(r"(\d+)$", path.stem).group(1)))
    if len(produced) != expected_pages:
        raise QualityError("pptx.render_page_count", f"rendered PNG count is {len(produced)}, expected {expected_pages}", stage="pptx_validate")
    slides: list[Path] = []
    findings: list[dict[str, Any]] = []
    slide_reports: list[dict[str, Any]] = []
    expected_size: tuple[int, int] | None = None
    for index, path in enumerate(produced, start=1):
        target = output / f"slide-{index:02d}.png"
        if path != target:
            path.replace(target)
        with Image.open(target) as image:
            size = image.size
        if expected_size is None:
            expected_size = size
        elif size != expected_size:
            findings.append(finding(rule="pptx.render_size_mismatch", severity="error", stage="pptx_post_export", message="Rendered slide dimensions differ", page_id=f"P{index:02d}", artifact=f"validation/render/{target.name}", owner_phase="pptx_validate"))
        is_blank, dominant = blank_image(target)
        if is_blank:
            findings.append(finding(rule="pptx.render_blank", severity="error", stage="pptx_post_export", message="Rendered slide is blank or all-background", page_id=f"P{index:02d}", artifact=f"validation/render/{target.name}", evidence={"dominant_ratio": round(dominant, 6)}, owner_phase="finalize_export", retry_phase="finalize_export"))
        slides.append(target)
        slide_reports.append({"page_id": f"P{index:02d}", "path": f"validation/render/{target.name}", "sha256": sha256_file(target), "width": size[0], "height": size[1], "blank": is_blank, "dominant_ratio": dominant})
    contact = output / "contact_sheet.png"
    contact_sheet(slides, contact)
    shutil.rmtree(profile, ignore_errors=True)
    return {
        "pdf": "validation/render/output.pdf",
        "pdf_sha256": sha256_file(canonical_pdf),
        "page_count": expected_pages,
        "slides": slide_reports,
        "contact_sheet": "validation/render/contact_sheet.png",
        "contact_sheet_sha256": sha256_file(contact),
    }, findings


def fake_render(output: Path, expected_pages: int) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    output.mkdir(parents=True, exist_ok=True)
    (output / "output.pdf").write_bytes(b"%PDF-test\n")
    slides = []
    paths = []
    for index in range(1, expected_pages + 1):
        path = output / f"slide-{index:02d}.png"
        image = Image.new("RGB", (640, 360), "white")
        draw = ImageDraw.Draw(image)
        draw.rectangle((20, 20, 620, 340), fill="#2563eb")
        draw.text((40, 40), f"P{index:02d}", fill="white")
        image.save(path)
        paths.append(path)
        slides.append({"page_id": f"P{index:02d}", "path": f"validation/render/{path.name}", "sha256": sha256_file(path), "width": 640, "height": 360, "blank": False, "dominant_ratio": 0.9})
    contact_sheet(paths, output / "contact_sheet.png")
    return {"pdf": "validation/render/output.pdf", "pdf_sha256": sha256_file(output / "output.pdf"), "page_count": expected_pages, "slides": slides, "contact_sheet": "validation/render/contact_sheet.png", "contact_sheet_sha256": sha256_file(output / "contact_sheet.png")}, []


def promote_validation(project: Path, stage: Path) -> None:
    validation = project / "validation"
    validation.mkdir(parents=True, exist_ok=True)
    for name in ("pptx_readback.md", "pptx_text_inventory.json", "pptx_validate_report.json", "beautify_fidelity_report.json"):
        staged = stage / name
        if not staged.exists():
            continue
        target = validation / name
        if target.exists() or target.is_symlink():
            target.unlink()
        staged.replace(target)
    target_render = validation / "render"
    if target_render.exists() or target_render.is_symlink():
        if target_render.is_symlink():
            target_render.unlink()
        else:
            shutil.rmtree(target_render)
    (stage / "render").replace(target_render)


def run(project: Path, export_contract_relative: str, phase_run_id: str, *, render: bool = True, soffice: str = "soffice", pdfinfo: str = "pdfinfo", pdftoppm: str = "pdftoppm", timeout: int = 180) -> dict[str, Any]:
    project = project.resolve(strict=True)
    export_contract_path = project_file(project, safe_relative(export_contract_relative))
    export_contract = load_json(export_contract_path)
    canonical = export_contract.get("canonical_pptx")
    if not isinstance(canonical, dict):
        raise QualityError("pptx.export_contract_invalid", "export contract has no canonical PPTX", stage="pptx_validate")
    pptx_relative = str(canonical.get("path") or "")
    pptx = project_file(project, pptx_relative)
    pptx_sha = sha256_file(pptx)
    if canonical.get("sha256") != pptx_sha or int(canonical.get("size") or 0) != pptx.stat().st_size:
        raise QualityError("pptx.export_contract_stale", "canonical PPTX hash/size changed", stage="pptx_validate")
    quality_summary_path = project_file(project, "validation/quality_summary.json")
    quality_summary = load_json(quality_summary_path)
    if quality_summary.get("decision") not in {"pass", "pass_with_warnings"}:
        raise QualityError("pptx.quality_gate_invalid", "upstream quality summary is not publishable", stage="pptx_validate")
    slide_parts, pptx_runs, fonts = validate_package(pptx)
    expected_pages = int(export_contract.get("expected_pages") or 0)
    if expected_pages <= 0 or len(slide_parts) != expected_pages or int(canonical.get("slide_count") or 0) != expected_pages:
        raise QualityError("pptx.slide_count_mismatch", "PPTX slide count differs from export contract", stage="pptx_validate")
    svg_pages, svg_inventory_sha = svg_text_inventory(project)
    if len(svg_pages) != expected_pages:
        raise QualityError("pptx.slide_count_mismatch", "SVG/PPTX page count differs", stage="pptx_validate")
    fidelity_pages, text_findings, deck_coverage = compare_text(svg_pages, pptx_runs)
    temp_root = project / ".slidesmith" / "tmp"
    temp_root.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix=f"pptx-validate-{phase_run_id}-", dir=temp_root))
    try:
        render_report, render_findings = render_pptx(pptx, stage / "render", phase_run_id, expected_pages, soffice=soffice, pdfinfo=pdfinfo, pdftoppm=pdftoppm, timeout=timeout) if render else fake_render(stage / "render", expected_pages)
        beautify_report: dict[str, Any] | None = None
        beautify_bindings: dict[str, Any] = {}
        beautify_findings: list[dict[str, Any]] = []
        beautify_lock = project / ".slidesmith" / "beautify_lock.json"
        if beautify_lock.exists() or beautify_lock.is_symlink():
            import beautify_runner

            beautify_report, beautify_findings, beautify_bindings = beautify_runner.build_fidelity_report(
                project,
                pptx_runs,
                pptx,
                phase_run_id,
                pptx_fonts=fonts,
            )
            atomic_json(stage / "beautify_fidelity_report.json", beautify_report)
        findings = text_findings + render_findings + beautify_findings
        summary = summarize(findings)
        inventory = {
            "schema": TEXT_SCHEMA,
            "task_id": str(quality_summary.get("task_id") or export_contract.get("task_id") or ""),
            "phase_run_id": phase_run_id,
            "svg_pages": svg_pages,
            "pptx_pages": fidelity_pages,
            "deck_coverage": deck_coverage,
            "generated_at": utc_now(),
        }
        atomic_json(stage / "pptx_text_inventory.json", inventory)
        readback = "\n\n".join(f"## P{index:02d}\n\n{normalize_text(' '.join(runs))}" for index, runs in enumerate(pptx_runs, start=1)) + "\n"
        (stage / "pptx_readback.md").write_text(readback, encoding="utf-8")
        report = {
            "schema": REPORT_SCHEMA,
            "task_id": inventory["task_id"],
            "phase_run_id": phase_run_id,
            "pptx": {"path": pptx_relative, "sha256": pptx_sha, "size": pptx.stat().st_size, "slide_count": expected_pages},
            "package": {"slide_parts": slide_parts, "fonts": fonts, "notes_policy": str(export_contract.get("notes_policy") or "")},
            "render": render_report,
            "text_fidelity": {"pages": fidelity_pages, "deck_coverage": deck_coverage},
            "findings": findings,
            "summary": summary,
            "decision": summary["decision"],
            "checked_at": utc_now(),
        }
        if beautify_report is not None:
            report["beautify_fidelity"] = {
                "path": "validation/beautify_fidelity_report.json",
                "sha256": sha256_file(stage / "beautify_fidelity_report.json"),
                "decision": beautify_report["decision"],
                "summary": beautify_report["summary"],
            }
        atomic_json(stage / "pptx_validate_report.json", report)
        promote_validation(project, stage)
        contract = {
            "schema": CONTRACT_SCHEMA,
            "phase": "pptx_validate",
            "task_id": inventory["task_id"],
            "phase_run_id": phase_run_id,
            "export_contract_sha256": sha256_file(export_contract_path),
            "quality_summary_sha256": sha256_file(quality_summary_path),
            "svg_inventory_sha256": svg_inventory_sha,
            "svg_output_sha256": str(quality_summary.get("svg_output_sha256") or ""),
            "canonical_pptx": {"path": pptx_relative, "sha256": pptx_sha, "size": pptx.stat().st_size},
            "pptx_readback_sha256": sha256_file(project / "validation/pptx_readback.md"),
            "pptx_text_inventory_sha256": sha256_file(project / "validation/pptx_text_inventory.json"),
            "pptx_validate_report_sha256": sha256_file(project / "validation/pptx_validate_report.json"),
            "render": render_report,
            "tools": {"soffice": soffice, "pdfinfo": pdfinfo, "pdftoppm": pdftoppm, "pillow": Image.__version__ if hasattr(Image, "__version__") else "unknown"},
            "summary": summary,
            "decision": summary["decision"],
            "checked_at": utc_now(),
        }
        if beautify_report is not None:
            contract.update(beautify_bindings)
            contract["beautify_fidelity_report_sha256"] = sha256_file(project / "validation/beautify_fidelity_report.json")
            contract["beautify_fidelity_decision"] = beautify_report["decision"]
            contract["beautify_fidelity_summary"] = beautify_report["summary"]
        atomic_json(project / ".slidesmith/contracts/pptx_validate.json", contract)
        return contract
    finally:
        shutil.rmtree(stage, ignore_errors=True)


def write_failure_report(project: Path, phase_run_id: str, rule: str, message: str) -> None:
    try:
        project = project.resolve(strict=True)
        issue = finding(rule=rule, severity="blocking", stage="pptx_post_export", message=message, owner_phase="pptx_validate", retry_phase="pptx_validate")
        summary = summarize([issue])
        atomic_json(project / "validation/pptx_validate_report.json", {
            "schema": REPORT_SCHEMA, "task_id": "", "phase_run_id": phase_run_id,
            "pptx": {}, "render": {}, "text_fidelity": {"pages": [], "deck_coverage": 0},
            "findings": [issue], "summary": summary, "decision": "fail", "checked_at": utc_now(),
            "diagnostic_only": True,
        })
    except Exception:
        return


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("project")
    parser.add_argument("--export-contract", default=".slidesmith/contracts/finalize_export.json")
    parser.add_argument("--phase-run-id", required=True)
    parser.add_argument("--soffice", default="soffice")
    parser.add_argument("--pdfinfo", default="pdfinfo")
    parser.add_argument("--pdftoppm", default="pdftoppm")
    parser.add_argument("--timeout", type=int, default=180)
    args = parser.parse_args()
    try:
        contract = run(Path(args.project), args.export_contract, args.phase_run_id, soffice=args.soffice, pdfinfo=args.pdfinfo, pdftoppm=args.pdftoppm, timeout=args.timeout)
    except QualityError as exc:
        write_failure_report(Path(args.project), args.phase_run_id, exc.rule, str(exc))
        print(json.dumps({"status": "failed", "rule": exc.rule, "message": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 0
    except Exception as exc:
        write_failure_report(Path(args.project), args.phase_run_id, "pptx.runner_exception", type(exc).__name__)
        print(json.dumps({"status": "failed", "rule": "pptx.runner_exception", "message": type(exc).__name__}, ensure_ascii=False), file=sys.stderr)
        return 0
    print(json.dumps({"status": "passed", "decision": contract["decision"]}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
