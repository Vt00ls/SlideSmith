#!/usr/bin/env python3
"""Structured SVG/chart quality gate for SlideSmith SPEC-07.

The upstream PPT Master checker is called through its Python API. Human stdout
is intentionally discarded and never participates in the contract.
"""

from __future__ import annotations

import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import math
import re
import sys
import xml.etree.ElementTree as ET
from pathlib import Path, PurePosixPath
from typing import Any

from quality_schema import QualityError, atomic_json, finding, report_ref, sha256_file, summarize, utc_now


SVG_REPORT_SCHEMA = "slidesmith.svg_quality_report.v1"
CHART_REPORT_SCHEMA = "slidesmith.chart_verify_report.v1"
SUMMARY_SCHEMA = "slidesmith.quality_summary.v1"
CONTRACT_SCHEMA = "slidesmith.quality_check_contract.v1"
UPSTREAM_HASHES = {
    "svg_inventory_sha256": "analysis/svg_inventory.json",
    "svg_resource_usage_sha256": "analysis/svg_resource_usage.json",
    "chart_usage_sha256": "analysis/chart_usage.json",
    "notes_sha256": "notes/total.md",
    "notes_inventory_sha256": "analysis/notes_inventory.json",
    "resources_manifest_sha256": ".slidesmith/resources_manifest.json",
    "design_spec_sha256": "design_spec.md",
    "spec_lock_sha256": "spec_lock.md",
}
PLACEHOLDER_RE = re.compile(
    r"(?:lorem\s+ipsum|\bTODO\b|\bTBD\b|X{4,}|placeholder|speaker\s+notes\s+here|this\s+(?:page|slide)\s+layout|待补充|占位(?:符|文本)?)",
    re.IGNORECASE,
)
NUMBER_RE = re.compile(r"^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:px)?$")


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise QualityError("quality.input_invalid", f"cannot read {path.name}: {exc}") from exc
    if not isinstance(value, dict):
        raise QualityError("quality.input_invalid", f"{path.name} must contain an object")
    return value


def project_path(project: Path, relative: str) -> Path:
    posix = PurePosixPath(relative)
    if not relative or posix.is_absolute() or posix.as_posix() != relative or any(part in {"", ".", ".."} for part in posix.parts):
        raise QualityError("quality.path_invalid", f"invalid project-relative path: {relative!r}")
    candidate = project.joinpath(*posix.parts)
    current = project
    for part in posix.parts:
        current /= part
        if current.is_symlink():
            raise QualityError("quality.symlink_forbidden", f"symlink is forbidden: {relative}")
    try:
        resolved = candidate.resolve(strict=True)
        resolved.relative_to(project)
    except (OSError, ValueError) as exc:
        raise QualityError("quality.path_invalid", f"path is missing or escapes project: {relative}") from exc
    if not resolved.is_file() or resolved.stat().st_size <= 0:
        raise QualityError("quality.input_invalid", f"required file is empty or invalid: {relative}")
    return resolved


def aggregate_sha(paths: list[tuple[str, Path]]) -> str:
    digest = hashlib.sha256()
    for relative, path in paths:
        digest.update(relative.encode("utf-8") + b"\x00" + sha256_file(path).encode("ascii") + b"\n")
    return digest.hexdigest()


def verify_upstream(project: Path) -> tuple[dict[str, Any], dict[str, Any], list[dict[str, Any]], str]:
    contract_path = project_path(project, ".slidesmith/contracts/svg_execute.json")
    contract = load_json(contract_path)
    for field, relative in UPSTREAM_HASHES.items():
        actual = sha256_file(project_path(project, relative))
        if contract.get(field) != actual:
            raise QualityError("quality.upstream_contract_stale", f"svg_execute contract hash changed: {field}")
    inventory = load_json(project_path(project, "analysis/svg_inventory.json"))
    if inventory.get("schema") != "slidesmith.svg_inventory.v1":
        raise QualityError("quality.upstream_contract_invalid", "SVG inventory schema is invalid")
    pages = inventory.get("pages")
    if not isinstance(pages, list) or not pages:
        raise QualityError("quality.upstream_contract_invalid", "SVG inventory pages are missing")
    svg_paths: list[tuple[str, Path]] = []
    for page in pages:
        if not isinstance(page, dict):
            raise QualityError("quality.upstream_contract_invalid", "SVG inventory page is invalid")
        relative = str(page.get("path") or "")
        path = project_path(project, relative)
        actual = sha256_file(path)
        if page.get("sha256") != actual:
            raise QualityError("quality.upstream_contract_stale", f"live SVG changed: {relative}")
        svg_paths.append((relative.removeprefix("svg_output/"), path))
    output_sha = aggregate_sha(svg_paths)
    if contract.get("svg_output_sha256") != output_sha:
        raise QualityError("quality.upstream_contract_stale", "SVG aggregate hash changed")
    exports = project / "exports"
    if exports.exists() and any(path.is_file() for path in exports.glob("*.pptx")):
        raise QualityError("quality.stale_export", "quality_check refuses pre-existing PPTX exports")
    return contract, inventory, pages, output_sha


def checker_path(project: Path, explicit: str) -> Path:
    candidates = []
    if explicit:
        candidates.append(Path(explicit))
    candidates.extend(
        [
            Path.cwd() / "skills/ppt-master/scripts/svg_quality_checker.py",
            project.parent.parent / "skills/ppt-master/scripts/svg_quality_checker.py",
        ]
    )
    for candidate in candidates:
        if candidate.is_file() and not candidate.is_symlink():
            return candidate.resolve()
    raise QualityError("quality.checker_unavailable", "svg_quality_checker.py is unavailable")


def checker_rule(message: str) -> str:
    lower = message.casefold()
    mappings = (
        ("mask", "svg.forbidden_mask"),
        ("foreignobject", "svg.forbidden_foreign_object"),
        ("viewbox", "svg.canvas_invalid"),
        ("well-formed", "svg.xml_invalid"),
        ("font", "svg.font_invalid"),
        ("image", "svg.image_reference_invalid"),
        ("animation", "svg.animation_invalid"),
        ("pattern", "svg.pattern_invalid"),
    )
    for token, rule in mappings:
        if token in lower:
            return rule
    return "svg.checker_error"


def run_checker(project: Path, path: Path, pages: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], str]:
    module_name = f"slidesmith_svg_checker_{sha256_file(path)[:12]}"
    spec = importlib.util.spec_from_file_location(module_name, path)
    if spec is None or spec.loader is None:
        raise QualityError("quality.checker_unavailable", "cannot load checker module")
    module = importlib.util.module_from_spec(spec)
    checker_dir = str(path.parent)
    inserted = checker_dir not in sys.path
    if inserted:
        sys.path.insert(0, checker_dir)
    try:
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            spec.loader.exec_module(module)
            checker = module.SVGQualityChecker()
            results = checker.check_directory(str(project))
    except Exception as exc:  # upstream isolation boundary
        raise QualityError("quality.checker_exception", f"SVG checker raised {type(exc).__name__}") from exc
    finally:
        if inserted and checker_dir in sys.path:
            sys.path.remove(checker_dir)
    if not isinstance(results, list) or len(results) != len(pages):
        raise QualityError("quality.checker_invalid_result", "SVG checker returned an invalid page result set")
    by_name = {Path(str(page.get("path") or "")).name: page for page in pages}
    findings: list[dict[str, Any]] = []
    for result in results:
        if not isinstance(result, dict):
            raise QualityError("quality.checker_invalid_result", "SVG checker page result is invalid")
        filename = Path(str(result.get("file") or result.get("path") or "")).name
        page = by_name.get(filename, {})
        page_id = str(page.get("page_id") or "")
        artifact = str(page.get("path") or f"svg_output/{filename}")
        for severity, key in (("error", "errors"), ("warning", "warnings")):
            values = result.get(key) or []
            if not isinstance(values, list):
                raise QualityError("quality.checker_invalid_result", f"checker {key} is not an array")
            for raw in values:
                message = str(raw)
                findings.append(
                    finding(
                        rule=checker_rule(message) if severity == "error" else "svg.checker_warning",
                        severity=severity,
                        stage="svg_pre_export",
                        message=message,
                        page_id=page_id,
                        artifact=artifact,
                        evidence={"source": "svg_quality_checker"},
                    )
                )
    return findings, sha256_file(path)


def local_name(tag: str) -> str:
    return tag.rsplit("}", 1)[-1].lower()


def number(value: str | None) -> float | None:
    raw = str(value or "").strip().lower()
    if not NUMBER_RE.fullmatch(raw):
        return None
    try:
        result = float(raw.removesuffix("px"))
    except ValueError:
        return None
    return result if math.isfinite(result) else None


def simple_bbox(element: ET.Element) -> tuple[float, float, float, float] | None:
    tag = local_name(element.tag)
    if "transform" in element.attrib:
        return None
    if tag in {"rect", "image", "svg"}:
        values = [number(element.get(key)) for key in ("x", "y", "width", "height")]
        if values[2] is None or values[3] is None:
            return None
        x, y, width, height = values[0] or 0.0, values[1] or 0.0, values[2], values[3]
        return x, y, x + width, y + height
    if tag == "circle":
        cx, cy, radius = number(element.get("cx")), number(element.get("cy")), number(element.get("r"))
        if None not in (cx, cy, radius):
            return cx - radius, cy - radius, cx + radius, cy + radius
    if tag == "ellipse":
        cx, cy, rx, ry = (number(element.get(key)) for key in ("cx", "cy", "rx", "ry"))
        if None not in (cx, cy, rx, ry):
            return cx - rx, cy - ry, cx + rx, cy + ry
    if tag == "line":
        values = [number(element.get(key)) for key in ("x1", "y1", "x2", "y2")]
        if None not in values:
            return min(values[0], values[2]), min(values[1], values[3]), max(values[0], values[2]), max(values[1], values[3])
    if tag == "text":
        x, y = number(element.get("x")), number(element.get("y"))
        if x is not None and y is not None:
            return x, y, x, y
    raw_bbox = str(element.get("data-bbox") or "")
    parts = [number(item) for item in re.split(r"[ ,]+", raw_bbox.strip()) if item]
    if len(parts) == 4 and None not in parts:
        return parts[0], parts[1], parts[0] + parts[2], parts[1] + parts[3]
    return None


def deterministic_findings(project: Path, pages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    findings: list[dict[str, Any]] = []
    for page in pages:
        page_id, relative = str(page.get("page_id") or ""), str(page.get("path") or "")
        path = project_path(project, relative)
        try:
            root = ET.parse(path).getroot()
        except ET.ParseError as exc:
            findings.append(finding(rule="svg.xml_invalid", severity="error", stage="svg_pre_export", message=str(exc), page_id=page_id, artifact=relative))
            continue
        view_box = [number(item) for item in re.split(r"[ ,]+", str(root.get("viewBox") or "").strip()) if item]
        if len(view_box) != 4 or None in view_box:
            continue
        x0, y0, width, height = view_box
        visible = 0
        text_count = 0
        for element in root.iter():
            tag = local_name(element.tag)
            if tag in {"svg", "defs", "style", "metadata", "title", "desc", "clippath"}:
                continue
            style = str(element.get("style") or "").replace(" ", "").lower()
            if element.get("display") == "none" or "display:none" in style or element.get("visibility") == "hidden":
                continue
            if tag == "text":
                text_count += 1
                content = " ".join("".join(element.itertext()).split())
                match = PLACEHOLDER_RE.search(content)
                if match:
                    findings.append(
                        finding(
                            rule="svg.placeholder_text",
                            severity="error",
                            stage="svg_pre_export",
                            message="Visible placeholder text remains in the slide",
                            page_id=page_id,
                            artifact=relative,
                            element_ids=[element.get("id") or ""],
                            evidence={"match": match.group(0)},
                        )
                    )
            bbox = simple_bbox(element)
            if bbox is not None:
                left, top, right, bottom = bbox
                full_background = tag == "rect" and left <= x0 and top <= y0 and right >= x0 + width and bottom >= y0 + height
                if not full_background:
                    visible += 1
                if left < x0 - 0.5 or top < y0 - 0.5 or right > x0 + width + 0.5 or bottom > y0 + height + 0.5:
                    findings.append(
                        finding(
                            rule="svg.element_out_of_bounds",
                            severity="error",
                            stage="svg_pre_export",
                            message="Visible element extends beyond the SVG canvas",
                            page_id=page_id,
                            artifact=relative,
                            element_ids=[element.get("id") or ""],
                            evidence={"bbox": [round(value, 2) for value in bbox]},
                        )
                    )
            elif tag in {"path", "polygon", "polyline", "use", "text", "image"}:
                visible += 1
        if visible == 0 and text_count == 0:
            findings.append(
                finding(
                    rule="svg.blank_page",
                    severity="error",
                    stage="svg_pre_export",
                    message="SVG page contains no visible non-background content",
                    page_id=page_id,
                    artifact=relative,
                )
            )
    return findings


def chart_findings(project: Path, pages: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    usage = load_json(project_path(project, "analysis/chart_usage.json"))
    charts = usage.get("charts")
    if not isinstance(charts, list):
        raise QualityError("chart.usage_invalid", "chart_usage charts must be an array")
    page_hashes = {str(page.get("page_id")): (str(page.get("path")), str(page.get("sha256"))) for page in pages}
    findings: list[dict[str, Any]] = []
    receipts: list[dict[str, Any]] = []
    for raw in charts:
        if not isinstance(raw, dict):
            raise QualityError("chart.usage_invalid", "chart usage entry is invalid")
        chart_id = str(raw.get("chart_id") or "")
        page_id = str(raw.get("page_id") or "")
        mode = str(raw.get("verification_mode") or "")
        svg_path, svg_sha = page_hashes.get(page_id, ("", ""))
        comparisons = raw.get("comparisons") or []
        if not isinstance(comparisons, list):
            comparisons = []
        normalized: list[dict[str, Any]] = []
        failed = False
        for comparison in comparisons:
            if not isinstance(comparison, dict):
                continue
            try:
                expected = float(comparison.get("expected"))
                actual = float(comparison.get("actual"))
                tolerance = float(comparison.get("tolerance", 1.0))
            except (TypeError, ValueError):
                continue
            delta = abs(actual - expected)
            passed = math.isfinite(delta) and math.isfinite(tolerance) and tolerance >= 0 and delta <= tolerance
            failed = failed or not passed
            normalized.append(
                {
                    "element_id": str(comparison.get("element_id") or ""),
                    "attribute": str(comparison.get("attribute") or ""),
                    "expected": expected,
                    "actual": actual,
                    "tolerance": tolerance,
                    "delta": delta,
                    "passed": passed,
                }
            )
        decision = "pass"
        if mode == "manual-verify":
            decision = "pass_with_warnings"
            findings.append(
                finding(
                    rule="chart.manual_review_required",
                    severity="warning",
                    stage="svg_pre_export",
                    message="Chart verification mode requires manual review",
                    page_id=page_id,
                    artifact=svg_path,
                    element_ids=[str(raw.get("element_id") or "")],
                    evidence={"chart_id": chart_id, "mode": mode},
                )
            )
        elif mode == "not-data-driven":
            decision = "pass"
        elif not normalized:
            severity = "error" if mode in {"direct-calc", "formula-verify"} else "warning"
            decision = "fail" if severity == "error" else "pass_with_warnings"
            findings.append(
                finding(
                    rule="chart.verification_incomplete",
                    severity=severity,
                    stage="svg_pre_export",
                    message="Chart usage does not contain deterministic coordinate comparisons",
                    page_id=page_id,
                    artifact=svg_path,
                    element_ids=[str(raw.get("element_id") or "")],
                    evidence={"chart_id": chart_id, "mode": mode},
                )
            )
        elif failed:
            decision = "fail"
            findings.append(
                finding(
                    rule="chart.geometry_mismatch",
                    severity="error",
                    stage="svg_pre_export",
                    message="Chart geometry differs from its declared data coordinates",
                    page_id=page_id,
                    artifact=svg_path,
                    element_ids=[item["element_id"] for item in normalized if item["element_id"]],
                    evidence={"chart_id": chart_id, "failed": sum(1 for item in normalized if not item["passed"])},
                )
            )
        receipts.append(
            {
                "chart_id": chart_id,
                "page_id": page_id,
                "mode": mode,
                "data_sha256": str(raw.get("data_sha256") or ""),
                "svg_sha256": svg_sha,
                "plot_area": raw.get("plot_area"),
                "calculator": {"command": mode, "version": "slidesmith.chart-receipt.v1"},
                "comparisons": normalized,
                "decision": decision,
            }
        )
    return findings, receipts


def run(project: Path, phase_run_id: str, explicit_checker: str = "") -> dict[str, Any]:
    project = project.resolve(strict=True)
    upstream, inventory, pages, svg_output_sha = verify_upstream(project)
    path = checker_path(project, explicit_checker)
    checker_findings, checker_sha = run_checker(project, path, pages)
    findings = checker_findings + deterministic_findings(project, pages)
    chart_issues, receipts = chart_findings(project, pages)
    findings.extend(chart_issues)
    checked_at = utc_now()
    svg_summary = summarize(findings)
    svg_report = {
        "schema": SVG_REPORT_SCHEMA,
        "task_id": str(inventory.get("task_id") or upstream.get("task_id") or ""),
        "phase_run_id": phase_run_id,
        "svg_execute_contract_sha256": sha256_file(project_path(project, ".slidesmith/contracts/svg_execute.json")),
        "svg_output_sha256": svg_output_sha,
        "findings": findings,
        "summary": svg_summary,
        "checker": {"path": "skills/ppt-master/scripts/svg_quality_checker.py", "sha256": checker_sha, "adapter": "python_api"},
        "checked_at": checked_at,
    }
    chart_only = [item for item in findings if str(item.get("rule") or "").startswith("chart.")]
    chart_report = {
        "schema": CHART_REPORT_SCHEMA,
        "task_id": svg_report["task_id"],
        "phase_run_id": phase_run_id,
        "chart_usage_sha256": sha256_file(project_path(project, "analysis/chart_usage.json")),
        "receipts": receipts,
        "findings": chart_only,
        "summary": summarize(chart_only),
        "checked_at": checked_at,
    }
    validation = project / "validation"
    atomic_json(validation / "svg_quality_report.json", svg_report)
    atomic_json(validation / "chart_verify_report.json", chart_report)
    summary = {
        "schema": SUMMARY_SCHEMA,
        "stage": "svg_pre_export",
        "task_id": svg_report["task_id"],
        "phase_run_id": phase_run_id,
        "svg_output_sha256": svg_output_sha,
        "reports": [
            report_ref(project, "validation/svg_quality_report.json") | {"kind": "svg"},
            report_ref(project, "validation/chart_verify_report.json") | {"kind": "chart"},
        ],
        "summary": svg_summary,
        "decision": svg_summary["decision"],
        "checked_at": checked_at,
    }
    atomic_json(validation / "quality_summary.json", summary)
    pointer = {
        "schema": "slidesmith.quality_report_pointer.v1",
        "canonical_path": "validation/quality_summary.json",
        "canonical_sha256": sha256_file(validation / "quality_summary.json"),
        "decision": summary["decision"],
        "summary": summary["summary"],
    }
    atomic_json(project / ".slidesmith/quality_report.json", pointer)
    contract = {
        "schema": CONTRACT_SCHEMA,
        "phase": "quality_check",
        "task_id": svg_report["task_id"],
        "phase_run_id": phase_run_id,
        "svg_execute_contract_sha256": svg_report["svg_execute_contract_sha256"],
        "svg_output_sha256": svg_output_sha,
        "quality_summary_sha256": sha256_file(validation / "quality_summary.json"),
        "svg_quality_report_sha256": sha256_file(validation / "svg_quality_report.json"),
        "chart_verify_report_sha256": sha256_file(validation / "chart_verify_report.json"),
        "checker_sha256": checker_sha,
        "decision": summary["decision"],
        "summary": summary["summary"],
        "checked_at": checked_at,
    }
    for field, relative in UPSTREAM_HASHES.items():
        contract[field] = sha256_file(project_path(project, relative))
    atomic_json(project / ".slidesmith/contracts/quality_check.json", contract)
    return contract


def write_failure_reports(project: Path, phase_run_id: str, rule: str, message: str) -> None:
    try:
        project = project.resolve(strict=True)
        inventory = load_json(project / "analysis/svg_inventory.json") if (project / "analysis/svg_inventory.json").is_file() else {}
        task_id = str(inventory.get("task_id") or "")
        issue = finding(rule=rule, severity="blocking", stage="svg_pre_export", message=message, owner_phase="quality_check", retry_phase="quality_check")
        summary = summarize([issue])
        validation = project / "validation"
        svg_report = {"schema": SVG_REPORT_SCHEMA, "task_id": task_id, "phase_run_id": phase_run_id, "findings": [issue], "summary": summary, "checked_at": utc_now()}
        chart_report = {"schema": CHART_REPORT_SCHEMA, "task_id": task_id, "phase_run_id": phase_run_id, "receipts": [], "findings": [], "summary": summarize([]), "checked_at": utc_now()}
        atomic_json(validation / "svg_quality_report.json", svg_report)
        atomic_json(validation / "chart_verify_report.json", chart_report)
        quality_summary = {
            "schema": SUMMARY_SCHEMA, "stage": "svg_pre_export", "task_id": task_id, "phase_run_id": phase_run_id,
            "svg_output_sha256": "", "reports": [
                report_ref(project, "validation/svg_quality_report.json") | {"kind": "svg"},
                report_ref(project, "validation/chart_verify_report.json") | {"kind": "chart"},
            ],
            "summary": summary, "decision": "fail", "checked_at": utc_now(),
        }
        atomic_json(validation / "quality_summary.json", quality_summary)
        atomic_json(project / ".slidesmith/quality_report.json", {
            "schema": "slidesmith.quality_report_pointer.v1", "canonical_path": "validation/quality_summary.json",
            "canonical_sha256": sha256_file(validation / "quality_summary.json"), "decision": "fail", "summary": summary,
        })
    except Exception:
        return


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("svg",))
    parser.add_argument("project")
    parser.add_argument("--phase-run-id", required=True)
    parser.add_argument("--checker", default="")
    args = parser.parse_args()
    project = Path(args.project)
    try:
        contract = run(project, args.phase_run_id, args.checker)
    except QualityError as exc:
        write_failure_reports(project, args.phase_run_id, exc.rule, str(exc))
        print(json.dumps({"status": "failed", "rule": exc.rule, "message": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 0
    except Exception as exc:
        write_failure_reports(project, args.phase_run_id, "quality.runner_exception", type(exc).__name__)
        print(json.dumps({"status": "failed", "rule": "quality.runner_exception", "message": type(exc).__name__}, ensure_ascii=False), file=sys.stderr)
        return 0
    print(json.dumps({"status": "passed", "decision": contract["decision"]}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
