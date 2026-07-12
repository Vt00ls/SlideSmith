#!/usr/bin/env python3
"""Runtime runner for SlideSmith + agent-compose + PPT Master."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET
import zipfile
from datetime import datetime, timezone
from pathlib import Path
import re
from typing import Any


WORKSPACE = Path(os.environ.get("WORKSPACE", Path.cwd())).resolve()


def resolve_skill_dir() -> Path:
    candidates: list[Path] = [WORKSPACE / "skills" / "ppt-master"]
    for env_name in ("SLIDESMITH_PPT_MASTER_SKILL_DIR", "PPT_MASTER_SKILL_DIR"):
        value = os.environ.get(env_name, "").strip()
        if value:
            candidates.append(Path(value))
    root = os.environ.get("PPT_MASTER_ROOT", "").strip()
    if root:
        candidates.append(Path(root) / "skills" / "ppt-master")
    candidates.extend([
        Path("/opt/ppt-master/skills/ppt-master"),
        Path("/Users/vt/Dev_space/ppt-master/skills/ppt-master"),
    ])
    for candidate in candidates:
        candidate = candidate.resolve()
        if (candidate / "SKILL.md").exists():
            return candidate
    raise FileNotFoundError("ppt-master skill not found; expected /workspace/skills/ppt-master/SKILL.md")


SKILL_DIR = resolve_skill_dir()
PPT_MASTER_ROOT = SKILL_DIR.parent.parent
SCRIPTS_DIR = SKILL_DIR / "scripts"
STATE_DIR = WORKSPACE / ".slidesmith"
EVENTS_PATH = STATE_DIR / "events.ndjson"
STATUS_PATH = STATE_DIR / "status.json"
ARTIFACTS_PATH = STATE_DIR / "artifacts.json"
TEXT_SOURCE_SUFFIXES = {".md", ".markdown", ".txt", ".text", ".csv", ".tsv", ".json", ".jsonl", ".yaml", ".yml"}
PRESENTATION_SOURCE_SUFFIXES = {".pptx", ".pptm", ".ppsx", ".ppsm", ".potx", ".potm"}
TEMPLATE_FILL_CONTENT_SOURCE_SUFFIXES = {".md", ".markdown", ".txt", ".text", ".csv", ".tsv"}

PRESENTATION_MAIN_CONTENT_TYPE = "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"
MACRO_PRESENTATION_MAIN_CONTENT_TYPE = "application/vnd.ms-powerpoint.presentation.macroEnabled.main+xml"
OOXML_CONTENT_TYPES_NAMESPACE = "http://schemas.openxmlformats.org/package/2006/content-types"
PRESENTATIONML_NAMESPACE = "http://schemas.openxmlformats.org/presentationml/2006/main"
PRESERVED_PRESENTATION_CONTENT_TYPES = {
    ".pptx": (PRESENTATION_MAIN_CONTENT_TYPE, "presentation"),
    ".pptm": (MACRO_PRESENTATION_MAIN_CONTENT_TYPE, "macro presentation"),
}
STAGED_PRESENTATION_CONTENT_TYPES = {
    ".ppsx": (
        "application/vnd.openxmlformats-officedocument.presentationml.slideshow.main+xml",
        PRESENTATION_MAIN_CONTENT_TYPE,
        "slideshow",
    ),
    ".ppsm": (
        "application/vnd.ms-powerpoint.slideshow.macroEnabled.main+xml",
        MACRO_PRESENTATION_MAIN_CONTENT_TYPE,
        "macro slideshow",
    ),
    ".potx": (
        "application/vnd.openxmlformats-officedocument.presentationml.template.main+xml",
        PRESENTATION_MAIN_CONTENT_TYPE,
        "template",
    ),
    ".potm": (
        "application/vnd.ms-powerpoint.template.macroEnabled.main+xml",
        MACRO_PRESENTATION_MAIN_CONTENT_TYPE,
        "macro template",
    ),
}


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def ensure_state_dir() -> None:
    STATE_DIR.mkdir(parents=True, exist_ok=True)


def write_json(path: Path, data: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def emit_event(event_type: str, title: str, content: str = "", status: str = "info", payload: dict[str, Any] | None = None) -> None:
    ensure_state_dir()
    event = {
        "created_at": utc_now(),
        "type": event_type,
        "title": title,
        "content": content,
        "status": status,
        "payload": payload or {},
    }
    with EVENTS_PATH.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(event, ensure_ascii=False) + "\n")
    print(f"[{status}] {title}: {content}", flush=True)


def set_status(stage: str, status: str = "running", **extra: Any) -> None:
    ensure_state_dir()
    payload = {
        "stage": stage,
        "status": status,
        "updated_at": utc_now(),
        **extra,
    }
    write_json(STATUS_PATH, payload)
    emit_event("status", stage, status, status, extra)


def run_command(args: list[str], cwd: Path | None = None, check: bool = True, timeout: int | None = None) -> subprocess.CompletedProcess[str]:
    emit_event("command", "Running command", " ".join(args), "running", {"cwd": str(cwd or WORKSPACE)})
    completed = subprocess.run(
        args,
        cwd=str(cwd or WORKSPACE),
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=timeout,
    )
    if completed.stdout:
        print(completed.stdout, end="", flush=True)
    if completed.stderr:
        print(completed.stderr, end="", file=sys.stderr, flush=True)
    if check and completed.returncode != 0:
        emit_event(
            "command_failed",
            "Command failed",
            " ".join(args),
            "failed",
            {"exit_code": completed.returncode, "stderr": completed.stderr[-4000:]},
        )
        raise SystemExit(completed.returncode)
    emit_event("command_completed", "Command completed", " ".join(args), "completed", {"exit_code": completed.returncode})
    return completed


def script_path(name: str) -> Path:
    path = SCRIPTS_DIR / name
    if not path.exists():
        raise FileNotFoundError(f"PPT Master script not found: {path}")
    return path


def validate_env(args: argparse.Namespace) -> None:
    set_status("validate_env")
    required_checks = [
        ("node", ["node", "-v"]),
        ("npm", ["npm", "-v"]),
        ("python3", ["python3", "--version"]),
        ("agent-compose-runtime", ["agent-compose-runtime", "--help"]),
        ("project_manager.py", ["python3", str(script_path("project_manager.py")), "--help"]),
        ("svg_to_pptx.py", ["python3", str(script_path("svg_to_pptx.py")), "--help"]),
    ]
    optional_checks = [
        ("soffice", ["soffice", "--version"]),
        ("pandoc", ["pandoc", "--version"]),
        ("pdftoppm", ["pdftoppm", "-v"]),
    ]
    checks = required_checks if args.profile == "mock" else required_checks + optional_checks
    results: list[dict[str, Any]] = []
    for name, command in checks:
        available = shutil.which(command[0]) is not None if command[0] != "python3" else shutil.which("python3") is not None
        completed = run_command(command, check=False, timeout=30)
        ok = available and completed.returncode == 0
        results.append({"name": name, "ok": ok, "exit_code": completed.returncode})
        if not ok:
            emit_event("validation_warning", f"{name} validation failed", "", "warning", results[-1])
    write_json(STATE_DIR / "validation.json", {"checked_at": utc_now(), "ppt_master_root": str(PPT_MASTER_ROOT), "skill_dir": str(SKILL_DIR), "results": results})
    if not all(item["ok"] for item in results):
        set_status("validate_env", "failed")
        raise SystemExit(1)
    set_status("validate_env", "completed")


def project_path_from_args(args: argparse.Namespace) -> Path:
    if getattr(args, "project_path", None):
        return Path(args.project_path).resolve()
    project = getattr(args, "project", "")
    if not project:
        raise ValueError("--project or --project-path is required")
    projects_dir = WORKSPACE / "projects"
    direct = projects_dir / project
    if direct.exists():
        return direct.resolve()
    matches = sorted(
        projects_dir.glob(f"{project}_ppt169_*"),
        key=lambda item: item.stat().st_mtime,
        reverse=True,
    )
    if matches:
        return matches[0].resolve()
    return direct.resolve()


def _contained_relative_path(root: Path, path: Path, label: str) -> Path:
    try:
        return path.relative_to(root)
    except ValueError as exc:
        raise ValueError(f"{label} is outside project: {path}") from exc


def _reject_symlink_components(root: Path, path: Path, label: str) -> None:
    relative = _contained_relative_path(root, path, label)
    current = root
    for component in relative.parts:
        current = current / component
        if current.is_symlink():
            raise ValueError(f"{label} must be regular and non-symlinked: {relative.as_posix()}")


def _require_template_fill_regular_file(
    project_path: Path,
    path: Path,
    label: str,
    *,
    nonempty: bool = False,
) -> None:
    relative = _contained_relative_path(project_path, path, label)
    _reject_symlink_components(project_path, path, label)
    if not path.is_file():
        raise ValueError(f"{label} must be a non-empty regular file: {relative.as_posix()}" if nonempty else f"{label} must be a regular file: {relative.as_posix()}")
    if nonempty and path.stat().st_size == 0:
        raise ValueError(f"{label} must be a non-empty regular file: {relative.as_posix()}")


def _validate_template_fill_output_path(project_path: Path, path: Path) -> None:
    relative = _contained_relative_path(project_path, path, "template fill output path")
    _reject_symlink_components(project_path, path, "template fill output path")
    current = project_path
    for component in relative.parts[:-1]:
        current = current / component
        if not current.exists():
            return
        if not current.is_dir():
            raise ValueError(f"template fill output parent must be a directory: {relative.as_posix()}")
    if path.exists() and not path.is_file():
        raise ValueError(f"template fill output path must be a regular file: {relative.as_posix()}")


def _template_fill_has_explicit_same_stem_markdown(project_path: Path, stem: str) -> bool:
    manifest_paths: list[tuple[Path, Path]] = [(project_path, project_path / ".slidesmith" / "source_inputs.json")]
    projects_dir = project_path.parent
    if projects_dir.name == "projects":
        workspace = projects_dir.parent
        manifest_paths.insert(0, (workspace, workspace / ".slidesmith" / "source_inputs.json"))

    for permitted_root, manifest_path in manifest_paths:
        _reject_symlink_components(permitted_root, manifest_path, "template fill source inputs manifest")
        if not manifest_path.exists():
            continue
        if not manifest_path.is_file():
            raise ValueError(f"template fill source inputs manifest must be a regular non-symlinked file: {manifest_path}")
        try:
            payload = json.loads(manifest_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise ValueError(f"parse template fill source inputs manifest: {exc}") from exc
        if not isinstance(payload, dict):
            raise ValueError("template fill source inputs manifest must be a JSON object")
        if payload.get("schema") != "slidesmith.source_inputs.v1":
            raise ValueError(f"unsupported template fill source inputs manifest schema: {payload.get('schema')!r}")
        files = payload.get("files", [])
        if not isinstance(files, list):
            raise ValueError("template fill source inputs manifest files must be a list")
        for index, entry in enumerate(files):
            if not isinstance(entry, dict):
                raise ValueError(f"template fill source inputs manifest files[{index}] must be an object")
            manifest_names: list[str] = []
            for field in ("name", "upload_path"):
                value = entry.get(field, "")
                if not isinstance(value, str):
                    raise ValueError(f"template fill source inputs manifest files[{index}].{field} must be a string")
                manifest_names.append(Path(value.replace("\\", "/")).name.strip())
            for name in manifest_names:
                if name.lower().endswith(".md") and Path(name).stem.casefold() == stem.casefold():
                    return True
        return False
    return False


def discover_template_fill_inputs(project_path: Path) -> dict[str, Any]:
    requested_path = Path(project_path).expanduser()
    absolute_path = Path(os.path.abspath(requested_path))
    if absolute_path.is_symlink():
        raise ValueError(f"template fill project path must be non-symlinked: {absolute_path}")
    if not absolute_path.exists():
        raise FileNotFoundError(f"template fill project path not found: {absolute_path}")
    if not absolute_path.is_dir():
        raise ValueError(f"template fill project path must be a directory: {absolute_path}")
    project_path = absolute_path.resolve()

    sources_path = project_path / "sources"
    _reject_symlink_components(project_path, sources_path, "template fill sources directory")
    if not sources_path.is_dir():
        raise ValueError("template fill requires sources directory: sources")
    try:
        entries = sorted(sources_path.iterdir(), key=lambda item: item.name)
    except OSError as exc:
        raise ValueError(f"read template fill sources directory: {exc}") from exc

    presentations: list[Path] = []
    for entry in entries:
        if entry.suffix.lower() not in PRESENTATION_SOURCE_SUFFIXES:
            continue
        _require_template_fill_regular_file(project_path, entry, "template fill presentation input")
        presentations.append(entry)
    if len(presentations) != 1 or presentations[0].suffix.lower() != ".pptx":
        relative_paths = sorted(path.relative_to(project_path).as_posix() for path in presentations)
        message = f"template fill requires exactly one source PPTX, found {len(presentations)} presentation files"
        if relative_paths:
            message += ": " + ", ".join(relative_paths)
        raise ValueError(message)

    source_pptx = presentations[0]
    slide_library = project_path / "analysis" / f"{source_pptx.stem}.slide_library.json"
    try:
        _require_template_fill_regular_file(
            project_path,
            slide_library,
            "template fill requires slide library",
            nonempty=True,
        )
    except ValueError as exc:
        relative = slide_library.relative_to(project_path).as_posix()
        raise ValueError(f"template fill requires slide library: {relative}: {exc}") from exc

    explicit_same_stem_markdown = _template_fill_has_explicit_same_stem_markdown(project_path, source_pptx.stem)
    content_sources: list[Path] = []
    for entry in entries:
        extension = entry.suffix.lower()
        if extension not in TEMPLATE_FILL_CONTENT_SOURCE_SUFFIXES:
            continue
        if extension == ".md" and entry.stem.casefold() == source_pptx.stem.casefold() and not explicit_same_stem_markdown:
            continue
        _require_template_fill_regular_file(project_path, entry, "template fill content source")
        content_sources.append(entry)
    content_sources.sort(key=lambda path: str(path))
    if not content_sources:
        raise ValueError("template fill requires content source beside template PPTX")

    inputs: dict[str, Any] = {
        "project_path": project_path,
        "source_pptx": source_pptx,
        "slide_library": slide_library,
        "fill_plan": project_path / "analysis" / "fill_plan.json",
        "check_report": project_path / "analysis" / "check_report.json",
        "validate_report": project_path / "validation" / "validate_report.json",
        "readback": project_path / "validation" / "readback.md",
        "export_base": project_path / "exports" / f"{project_path.name}_template_fill.pptx",
        "content_sources": content_sources,
    }
    for key in ("fill_plan", "check_report", "validate_report", "readback", "export_base"):
        _validate_template_fill_output_path(project_path, inputs[key])
    return inputs


def load_source_manifest(path: Path) -> list[Path]:
    manifest_path = path if path.is_absolute() else WORKSPACE / path
    manifest_path = manifest_path.resolve()
    if not manifest_path.exists():
        return []

    payload = json.loads(manifest_path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError("source manifest must be a JSON object")
    if payload.get("schema") != "slidesmith.source_inputs.v1":
        raise ValueError(f"unsupported source manifest schema: {payload.get('schema')!r}")
    files = payload.get("files")
    if not isinstance(files, list):
        raise ValueError("source manifest files must be a list")

    sources: list[Path] = []
    for index, entry in enumerate(files):
        if not isinstance(entry, dict):
            raise ValueError(f"source manifest files[{index}] must be an object")
        upload_path = entry.get("upload_path", "")
        if not isinstance(upload_path, str):
            raise ValueError(f"source manifest files[{index}].upload_path must be a string")
        upload_path = upload_path.strip()
        if not upload_path:
            continue

        source_path = (WORKSPACE / upload_path).resolve()
        try:
            source_path.relative_to(WORKSPACE)
        except ValueError as exc:
            raise ValueError(f"source path escapes workspace: {upload_path}") from exc
        if source_path.is_file():
            sources.append(source_path)
    return sources


def stage_prepare_inputs(args: argparse.Namespace, scratch_input_dir: Path) -> list[Path]:
    manifest_arg = getattr(args, "sources_manifest", "")
    selected = load_source_manifest(Path(manifest_arg)) if manifest_arg else []
    if not selected:
        input_arg = getattr(args, "input", "")
        if input_arg:
            input_path = Path(input_arg)
            if not input_path.is_absolute():
                input_path = WORKSPACE / input_path
            input_path = input_path.resolve()
            if not input_path.exists():
                raise FileNotFoundError(f"input not found: {input_path}")
            selected = [input_path]
    if not selected:
        raise FileNotFoundError("no source inputs found")

    staged: list[Path] = []
    for source_path in selected:
        staged_path = scratch_input_dir / source_path.name
        shutil.copy2(source_path, staged_path)
        normalize_staged_presentation_package(staged_path)
        staged.append(staged_path)
    return staged


def normalize_staged_presentation_package(staged_path: Path) -> None:
    normalization = STAGED_PRESENTATION_CONTENT_TYPES.get(staged_path.suffix.lower())
    preserved = PRESERVED_PRESENTATION_CONTENT_TYPES.get(staged_path.suffix.lower())
    if normalization is None and preserved is None:
        return
    should_normalize = normalization is not None
    if normalization is not None:
        expected_content_type, target_content_type, content_type_label = normalization
    else:
        expected_content_type, content_type_label = preserved
        target_content_type = expected_content_type
    temp_path: Path | None = None
    try:
        with zipfile.ZipFile(staged_path, "r") as source:
            try:
                content_types_raw = source.read("[Content_Types].xml")
            except KeyError as exc:
                raise ValueError(
                    f"non-OOXML presentation package {staged_path.name}: missing [Content_Types].xml"
                ) from exc
            try:
                content_types = ET.fromstring(content_types_raw)
            except ET.ParseError as exc:
                raise ValueError(
                    f"malformed OOXML presentation package {staged_path.name}: invalid [Content_Types].xml"
                ) from exc
            expected_types_tag = f"{{{OOXML_CONTENT_TYPES_NAMESPACE}}}Types"
            if content_types.tag != expected_types_tag:
                raise ValueError(
                    f"non-OOXML presentation package {staged_path.name}: invalid content-types namespace"
                )
            override_tag = f"{{{OOXML_CONTENT_TYPES_NAMESPACE}}}Override"
            presentation_overrides = [
                element
                for element in content_types
                if element.tag == override_tag
                and element.attrib.get("PartName") == "/ppt/presentation.xml"
            ]
            if len(presentation_overrides) != 1:
                raise ValueError(
                    f"{staged_path.name}: expected exactly one {content_type_label} main content type; "
                    f"found {len(presentation_overrides)} overrides"
                )
            presentation_override = presentation_overrides[0]
            actual_content_type = presentation_override.attrib.get("ContentType")
            if actual_content_type != expected_content_type:
                raise ValueError(
                    f"{staged_path.name}: expected {content_type_label} main content type "
                    f"{expected_content_type!r}; found {actual_content_type!r}"
                )

            try:
                presentation_raw = source.read("ppt/presentation.xml")
            except KeyError as exc:
                raise ValueError(
                    f"non-OOXML presentation package {staged_path.name}: missing ppt/presentation.xml"
                ) from exc
            try:
                presentation = ET.fromstring(presentation_raw)
            except ET.ParseError as exc:
                raise ValueError(
                    f"malformed OOXML presentation package {staged_path.name}: invalid ppt/presentation.xml"
                ) from exc
            expected_presentation_tag = f"{{{PRESENTATIONML_NAMESPACE}}}presentation"
            if presentation.tag != expected_presentation_tag:
                raise ValueError(
                    f"non-OOXML presentation package {staged_path.name}: "
                    "missing PresentationML presentation root"
                )
            if not should_normalize:
                return

            presentation_override.set("ContentType", target_content_type)
            ET.register_namespace("", OOXML_CONTENT_TYPES_NAMESPACE)
            normalized_content_types = ET.tostring(
                content_types,
                encoding="utf-8",
                xml_declaration=True,
            )

            with tempfile.NamedTemporaryFile(
                prefix=f".{staged_path.name}.",
                suffix=".tmp",
                dir=staged_path.parent,
                delete=False,
            ) as handle:
                temp_path = Path(handle.name)
            with zipfile.ZipFile(temp_path, "w") as destination:
                destination.comment = source.comment
                for entry in source.infolist():
                    data = (
                        normalized_content_types
                        if entry.filename == "[Content_Types].xml"
                        else source.read(entry)
                    )
                    destination.writestr(entry, data)
        shutil.copystat(staged_path, temp_path)
        temp_path.replace(staged_path)
        temp_path = None
    except zipfile.BadZipFile as exc:
        raise ValueError(
            f"malformed OOXML presentation package {staged_path.name}: invalid ZIP package"
        ) from exc
    finally:
        if temp_path is not None:
            temp_path.unlink(missing_ok=True)


def prepare(args: argparse.Namespace) -> None:
    set_status("source_converting", project=args.project)
    projects_dir = WORKSPACE / "projects"
    projects_dir.mkdir(parents=True, exist_ok=True)
    project_path = project_path_from_args(args)
    if not project_path.exists():
        completed = run_command(["python3", str(script_path("project_manager.py")), "init", args.project, "--format", args.format, "--dir", str(projects_dir)])
        project_path = parse_created_project_path(completed.stdout) or project_path_from_args(args)
    else:
        emit_event("project_exists", "Project already exists", str(project_path), "warning")

    scratch_input_dir = STATE_DIR / "input" / args.project
    if scratch_input_dir.exists():
        shutil.rmtree(scratch_input_dir)
    scratch_input_dir.mkdir(parents=True, exist_ok=True)
    staged_inputs = stage_prepare_inputs(args, scratch_input_dir)
    if not staged_inputs:
        raise FileNotFoundError("no staged source inputs")

    run_command([
        "python3", str(script_path("project_manager.py")), "import-sources",
        str(project_path), str(scratch_input_dir), "--move",
    ])

    confirmation_dir = project_path / "confirm_ui"
    confirmation_dir.mkdir(parents=True, exist_ok=True)
    recommendation = mock_recommendations() if args.profile == "smoke" else real_lite_recommendations(project_path)
    write_json(confirmation_dir / "recommendations.json", recommendation)
    set_status("awaiting_confirm", "waiting", project_path=str(project_path), recommendations=str(confirmation_dir / "recommendations.json"))


def parse_created_project_path(output: str) -> Path | None:
    match = re.search(r"Project created:\s*(.+)", output)
    if not match:
        return None
    return Path(match.group(1).strip()).resolve()


def mock_recommendations() -> dict[str, Any]:
    return {
        "lang": "zh",
        "tier": 2,
        "recommend": {
            "canvas": "ppt169",
            "mode": "pyramid",
            "visual_style": "swiss-minimal",
            "icons": "tabler-outline",
            "image_usage": ["none"],
            "formula_policy": "none",
            "generation_mode": "continuous",
            "delivery_purpose": "balanced",
        },
        "page_count": {"value": "3"},
        "audience": {"value": "SlideSmith MVP runtime smoke reviewers"},
        "content_divergence": {"value": "Use source content only for smoke validation."},
        "image_notes": {"value": "No external images in mock smoke."},
        "color": {
            "selected": 0,
            "candidates": [
                {
                    "name": "Slate and Amber",
                    "note": "Operational, readable, and not tied to one hue.",
                    "palette": {
                        "background": "#F7F7F2",
                        "secondary_bg": "#E8ECE7",
                        "primary": "#27313B",
                        "accent": "#C46A2D",
                        "secondary_accent": "#477A74",
                        "body_text": "#1E252B",
                    },
                }
            ],
        },
        "typography": {
            "selected": 0,
            "candidates": [
                {
                    "name": "Noto Sans CJK + Inter",
                    "note": "Stable CJK rendering for runtime validation.",
                    "sample_heading": "运行层验证",
                    "sample_heading_latin": "Runtime Validation",
                    "sample_body": "状态、日志与产物可以被平台读取。",
                    "sample_body_latin": "Status, logs, and artifacts are readable.",
                    "heading": {"cjk": "Noto Sans CJK SC", "latin": "Inter", "css": "'Noto Sans CJK SC','Inter',sans-serif"},
                    "body": {"cjk": "Noto Sans CJK SC", "latin": "Inter", "css": "'Noto Sans CJK SC','Inter',sans-serif"},
                    "body_size": 24,
                    "sizes": {"title": 46, "subtitle": 30, "annotation": 18},
                }
            ],
        },
        "refine_spec": {"value": False},
    }


def generate(args: argparse.Namespace) -> None:
    project_path = project_path_from_args(args)
    if not project_path.exists():
        raise FileNotFoundError(f"project path not found: {project_path}")

    if args.profile == "smoke":
        generate_smoke(args, project_path)
        return
    generate_real_lite(args, project_path)


def generate_smoke(args: argparse.Namespace, project_path: Path) -> None:
    set_status("spec_generating", project_path=str(project_path))
    if args.confirmation == "mock":
        write_json(project_path / "confirm_ui" / "result.json", mock_confirmation_result())
    write_mock_design_artifacts(project_path)

    set_status("svg_generating", project_path=str(project_path))
    write_mock_svgs(project_path)

    set_status("quality_checking", project_path=str(project_path))
    run_command(["python3", str(script_path("svg_quality_checker.py")), str(project_path / "svg_output"), "--format", "ppt169"])

    set_status("exporting", project_path=str(project_path))
    run_command(["python3", str(script_path("finalize_svg.py")), str(project_path), "--quiet"])
    run_command(["python3", str(script_path("svg_to_pptx.py")), str(project_path), "--no-notes", "-t", "none"])

    publish_project(project_path)
    set_status("completed", "completed", project_path=str(project_path))


def generate_real_lite(args: argparse.Namespace, project_path: Path) -> None:
    set_status("spec_generating", project_path=str(project_path), profile=args.profile)
    confirmation = load_confirmation_result(project_path, allow_mock=args.confirmation == "mock")
    source_docs = load_source_documents(project_path)
    if not source_docs:
        raise FileNotFoundError(f"no readable source documents under {project_path / 'sources'}")

    page_count = confirmed_page_count(confirmation, fallback=5)
    slides = build_real_lite_slides(project_path, source_docs, confirmation, page_count)
    palette = palette_for_confirmation(confirmation)
    typography = typography_for_confirmation(confirmation)
    write_real_lite_design_artifacts(project_path, source_docs, confirmation, slides, palette, typography)

    set_status("svg_generating", project_path=str(project_path), slides=len(slides))
    write_real_lite_svgs(project_path, slides, palette, typography)

    set_status("quality_checking", project_path=str(project_path))
    run_command(["python3", str(script_path("svg_quality_checker.py")), str(project_path / "svg_output"), "--format", "ppt169"])

    set_status("exporting", project_path=str(project_path))
    run_command(["python3", str(script_path("finalize_svg.py")), str(project_path), "--quiet"])
    run_command(["python3", str(script_path("svg_to_pptx.py")), str(project_path), "--no-notes", "-t", "none"])

    publish_project(project_path)
    set_status("completed", "completed", project_path=str(project_path), profile=args.profile)


def mock_confirmation_result() -> dict[str, Any]:
    return {
        "canvas": "ppt169",
        "page_count": "3",
        "audience": "SlideSmith MVP runtime smoke reviewers",
        "content_divergence": "Use source content only for smoke validation.",
        "mode": "pyramid",
        "visual_style": "swiss-minimal",
        "color": mock_recommendations()["color"]["candidates"][0],
        "icons": "tabler-outline",
        "typography": mock_recommendations()["typography"]["candidates"][0],
        "delivery_purpose": "balanced",
        "formula_policy": "none",
        "image_usage": ["none"],
        "image_notes": "No external images in mock smoke.",
        "generation_mode": "continuous",
        "refine_spec": False,
        "stage": "final",
        "status": "confirmed",
        "confirmed_at": utc_now(),
    }


def real_lite_recommendations(project_path: Path) -> dict[str, Any]:
    source_docs = load_source_documents(project_path)
    total_chars = sum(len(doc["text"]) for doc in source_docs)
    first_title = source_docs[0]["title"] if source_docs else "上传资料"
    page_count = "3"
    if total_chars > 7000:
        page_count = "8"
    elif total_chars > 3500:
        page_count = "5"
    return {
        "lang": "zh",
        "tier": 2,
        "recommend": {
            "canvas": "ppt169",
            "mode": "pyramid",
            "visual_style": "business",
            "icons": "tabler-outline",
            "image_usage": ["none"],
            "formula_policy": "none",
            "generation_mode": "continuous",
            "delivery_purpose": "standard",
        },
        "page_count": {"value": page_count},
        "audience": {"value": "面向业务评审者，强调结论、证据和落地步骤。"},
        "content_divergence": {"value": f"基于《{first_title}》重组为汇报型结构，事实只来自上传资料。"},
        "image_notes": {"value": "MVP real-lite 模式暂不联网取图，使用文本、结构块和基础图形完成页面。"},
        "color": {
            "selected": 0,
            "candidates": [
                {
                    "name": "Operational Teal",
                    "note": "适合内部评审和产品技术汇报。",
                    "palette": {
                        "background": "#F7F7F2",
                        "secondary_bg": "#E5ECE8",
                        "primary": "#263238",
                        "accent": "#D0643B",
                        "secondary_accent": "#2D7C75",
                        "body_text": "#1E252B",
                    },
                }
            ],
        },
        "typography": {
            "selected": 0,
            "candidates": [
                {
                    "name": "Noto Sans CJK + Inter",
                    "note": "兼容中文和英文，适合服务器端导出。",
                    "sample_heading": first_title[:24],
                    "sample_heading_latin": "SlideSmith Runtime",
                    "sample_body": "根据上传资料生成可预览和可下载的 PPTX。",
                    "sample_body_latin": "Generated from uploaded source material.",
                    "heading": {"cjk": "Noto Sans CJK SC", "latin": "Inter", "css": "'Noto Sans CJK SC','Inter',sans-serif"},
                    "body": {"cjk": "Noto Sans CJK SC", "latin": "Inter", "css": "'Noto Sans CJK SC','Inter',sans-serif"},
                    "body_size": 24,
                    "sizes": {"title": 46, "subtitle": 30, "annotation": 18},
                }
            ],
        },
        "refine_spec": {"value": False},
    }


def load_confirmation_result(project_path: Path, allow_mock: bool = False) -> dict[str, Any]:
    result_path = project_path / "confirm_ui" / "result.json"
    if result_path.exists():
        return json.loads(result_path.read_text(encoding="utf-8"))
    if allow_mock:
        result = mock_confirmation_result()
        write_json(result_path, result)
        return result
    raise FileNotFoundError(f"confirmation result not found: {result_path}")


def load_source_documents(project_path: Path) -> list[dict[str, str]]:
    source_dir = project_path / "sources"
    if not source_dir.exists():
        return []
    docs: list[dict[str, str]] = []
    for path in sorted(source_dir.rglob("*")):
        if not path.is_file() or path.suffix.lower() not in TEXT_SOURCE_SUFFIXES:
            continue
        try:
            raw = path.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue
        text = clean_source_text(raw)
        if not text.strip():
            continue
        docs.append({
            "path": str(path.relative_to(project_path)),
            "title": extract_document_title(text, path.stem),
            "text": text,
        })
    return docs


def clean_source_text(raw: str) -> str:
    text = raw.replace("\r\n", "\n").replace("\r", "\n")
    text = re.sub(r"```.*?```", "\n", text, flags=re.S)
    text = re.sub(r"!\[[^\]]*\]\([^)]+\)", "", text)
    text = re.sub(r"<[^>]+>", "", text)
    text = re.sub(r"\n{3,}", "\n\n", text)
    return text.strip()


def extract_document_title(text: str, fallback: str) -> str:
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("#"):
            title = stripped.lstrip("#").strip()
            if title:
                return title[:80]
    for line in text.splitlines():
        stripped = strip_markdown(line)
        if stripped:
            return stripped[:80]
    return fallback.replace("_", " ").replace("-", " ").strip().title() or "Untitled"


def confirmed_value(confirmation: dict[str, Any], keys: list[str], fallback: Any = "") -> Any:
    for key in keys:
        value = confirmation.get(key)
        if value not in (None, "", []):
            return value
    submitted = confirmation.get("submitted_values")
    if isinstance(submitted, dict):
        for key in keys:
            value = submitted.get(key)
            if value not in (None, "", []):
                return value
    return fallback


def confirmed_page_count(confirmation: dict[str, Any], fallback: int = 5) -> int:
    value = confirmed_value(confirmation, ["page_count", "slide_count"], fallback)
    try:
        count = int(str(value))
    except ValueError:
        count = fallback
    return max(3, min(10, count))


def build_real_lite_slides(project_path: Path, source_docs: list[dict[str, str]], confirmation: dict[str, Any], page_count: int) -> list[dict[str, Any]]:
    title = source_docs[0]["title"]
    audience = str(confirmed_value(confirmation, ["audience"], "业务评审者"))
    source_sections = extract_sections(source_docs)
    body_count = max(1, page_count - 2)
    body_sections = expand_or_trim_sections(source_sections, body_count)

    slides: list[dict[str, Any]] = [
        {
            "kind": "cover",
            "title": title,
            "subtitle": f"{audience_phrase(audience)}的资料生成汇报",
            "bullets": [
                f"资料来源：{', '.join(doc['path'] for doc in source_docs[:3])}",
                "生成方式：SlideSmith real-lite runner + PPT Master 导出链路",
                f"目标页数：{page_count}",
            ],
        }
    ]
    for section in body_sections:
        slides.append({
            "kind": "content",
            "title": section["title"],
            "bullets": section["bullets"][:5],
        })
    slides.append({
        "kind": "summary",
        "title": "结论与下一步",
        "bullets": build_summary_points(project_path, source_docs, confirmation),
    })
    return slides[:page_count]


def audience_phrase(audience: str) -> str:
    audience = audience.strip()
    if audience.startswith(("面向", "For ", "for ")):
        return audience
    return f"面向{audience}"


def extract_sections(source_docs: list[dict[str, str]]) -> list[dict[str, Any]]:
    sections: list[dict[str, Any]] = []
    for doc in source_docs:
        current_title = doc["title"]
        current_lines: list[str] = []
        for line in doc["text"].splitlines():
            stripped = line.strip()
            if not stripped:
                continue
            if stripped.startswith("#"):
                if current_lines:
                    sections.append(section_from_lines(current_title, current_lines))
                    current_lines = []
                current_title = stripped.lstrip("#").strip() or current_title
                continue
            current_lines.append(stripped)
        if current_lines:
            sections.append(section_from_lines(current_title, current_lines))
    if sections:
        return sections
    merged = "\n".join(doc["text"] for doc in source_docs)
    return [section_from_lines("核心内容", split_sentences(merged))]


def section_from_lines(title: str, lines: list[str]) -> dict[str, Any]:
    bullets: list[str] = []
    for line in lines:
        clean = strip_markdown(line)
        if not clean:
            continue
        bullets.append(truncate_text(clean, 92))
        if len(bullets) >= 6:
            break
    if not bullets:
        bullets = ["该部分来自上传资料，当前文本较短。"]
    return {"title": truncate_text(strip_markdown(title), 40), "bullets": bullets}


def expand_or_trim_sections(sections: list[dict[str, Any]], target: int) -> list[dict[str, Any]]:
    if not sections:
        sections = [{"title": "核心内容", "bullets": ["上传资料已导入，但未解析出足够的结构化段落。"]}]
    expanded = sections[:]
    while len(expanded) < target:
        source = expanded[len(expanded) % len(sections)]
        expanded.append({
            "title": f"{source['title']}：补充视角",
            "bullets": source["bullets"],
        })
    return expanded[:target]


def build_summary_points(project_path: Path, source_docs: list[dict[str, str]], confirmation: dict[str, Any]) -> list[str]:
    depth = str(confirmed_value(confirmation, ["content_depth", "mode"], "balanced"))
    style = str(confirmed_value(confirmation, ["visual_style"], "business"))
    return [
        "已基于上传资料完成项目创建、规格生成、SVG 生成和 PPTX 导出。",
        f"内容策略：{depth}；视觉风格：{style}。",
        "下一步可接入完整 Agent prompt 编排，替换 real-lite 的启发式内容组织。",
        f"本次生成读取了 {len(source_docs)} 个源文件。",
    ]


def split_sentences(text: str) -> list[str]:
    parts = re.split(r"(?<=[。！？.!?])\s+|\n+", text)
    return [part.strip() for part in parts if part.strip()]


def strip_markdown(value: str) -> str:
    value = value.strip()
    value = re.sub(r"^[-*+]\s+", "", value)
    value = re.sub(r"^\d+[.)]\s+", "", value)
    value = re.sub(r"`([^`]+)`", r"\1", value)
    value = re.sub(r"\*\*([^*]+)\*\*", r"\1", value)
    value = re.sub(r"\[([^\]]+)\]\([^)]+\)", r"\1", value)
    return value.strip()


def truncate_text(value: str, limit: int) -> str:
    value = re.sub(r"\s+", " ", value).strip()
    if len(value) <= limit:
        return value
    return value[: max(0, limit - 1)].rstrip() + "..."


def palette_for_confirmation(confirmation: dict[str, Any]) -> dict[str, str]:
    color = confirmed_value(confirmation, ["color"], None)
    if isinstance(color, dict):
        palette = color.get("palette")
        if isinstance(palette, dict):
            return normalize_palette(palette)
    style = str(confirmed_value(confirmation, ["visual_style"], "business"))
    palettes = {
        "tech": {
            "background": "#F4F7F8",
            "secondary_bg": "#DDE8EA",
            "primary": "#203040",
            "accent": "#C45131",
            "secondary_accent": "#1E7A82",
            "body_text": "#1E252B",
        },
        "editorial": {
            "background": "#F8F4EF",
            "secondary_bg": "#E7DDD2",
            "primary": "#2E2A27",
            "accent": "#A94E3B",
            "secondary_accent": "#4B766A",
            "body_text": "#27231F",
        },
        "swiss-minimal": {
            "background": "#F7F7F2",
            "secondary_bg": "#E8ECE7",
            "primary": "#27313B",
            "accent": "#C46A2D",
            "secondary_accent": "#477A74",
            "body_text": "#1E252B",
        },
        "business": {
            "background": "#F7F7F2",
            "secondary_bg": "#E5ECE8",
            "primary": "#263238",
            "accent": "#D0643B",
            "secondary_accent": "#2D7C75",
            "body_text": "#1E252B",
        },
    }
    return palettes.get(style, palettes["business"])


def normalize_palette(palette: dict[str, Any]) -> dict[str, str]:
    defaults = palette_for_confirmation({"visual_style": "business"})
    out = defaults.copy()
    for key in out:
        value = palette.get(key)
        if isinstance(value, str) and re.match(r"^#[0-9a-fA-F]{6}$", value):
            out[key] = value.upper()
    if "text" in palette and "body_text" not in palette and isinstance(palette["text"], str):
        out["body_text"] = palette["text"]
    return out


def typography_for_confirmation(confirmation: dict[str, Any]) -> dict[str, Any]:
    typography = confirmed_value(confirmation, ["typography"], None)
    if isinstance(typography, dict):
        sizes = typography.get("sizes") if isinstance(typography.get("sizes"), dict) else {}
        body_size = int(typography.get("body_size") or sizes.get("body") or 24)
        return {
            "font_family": "Noto Sans CJK SC, Inter, Arial, sans-serif",
            "title": int(sizes.get("title") or 46),
            "subtitle": int(sizes.get("subtitle") or 30),
            "body_large": max(body_size + 2, 26),
            "body": body_size,
            "annotation": int(sizes.get("annotation") or 18),
        }
    return {
        "font_family": "Noto Sans CJK SC, Inter, Arial, sans-serif",
        "title": 46,
        "subtitle": 30,
        "body_large": 26,
        "body": 24,
        "annotation": 18,
    }


def write_real_lite_design_artifacts(
    project_path: Path,
    source_docs: list[dict[str, str]],
    confirmation: dict[str, Any],
    slides: list[dict[str, Any]],
    palette: dict[str, str],
    typography: dict[str, Any],
) -> None:
    audience = confirmed_value(confirmation, ["audience"], "业务评审者")
    visual_style = confirmed_value(confirmation, ["visual_style"], "business")
    content_depth = confirmed_value(confirmation, ["content_depth", "mode"], "balanced")
    source_list = "\n".join(f"- {doc['path']}: {doc['title']}" for doc in source_docs)
    outline = "\n".join(f"{idx}. {slide['title']}" for idx, slide in enumerate(slides, start=1))
    image_usage = confirmed_value(confirmation, ["image_usage", "asset_strategy"], "none")
    if isinstance(image_usage, list):
        image_usage_text = ", ".join(str(item) for item in image_usage)
    else:
        image_usage_text = str(image_usage)
    design_spec = f"""# Design Specification

## I. Project Intent

Generate a concise PPT deck from uploaded SlideSmith source material.

- Audience: {audience}
- Content depth: {content_depth}
- Runtime profile: real-lite
- Source files:
{source_list}

## II. Content Outline

{outline}

## III. Visual Direction

Use a {visual_style} style with restrained structure blocks, strong headings, and content-first hierarchy.

## IV. Typography

Primary font family: {typography['font_family']}

## V. Color System

- background: {palette['background']}
- secondary_bg: {palette['secondary_bg']}
- primary: {palette['primary']}
- accent: {palette['accent']}
- secondary_accent: {palette['secondary_accent']}
- body_text: {palette['body_text']}

## VI. Layout System

Slides use a 16:9 canvas, fixed margins, left-aligned text, and repeated section markers.

## VII. Data And Charts

No generated charts in MVP real-lite mode.

## VIII. Image Resource List

Confirmed image usage: {image_usage_text}. MVP real-lite mode does not acquire external images.

## IX. Page Plan

{outline}

## X. Export

Export SVG pages through PPT Master's finalize and svg_to_pptx scripts.

## XI. Notes

This file is generated from actual uploaded source text. It is not the fixed smoke deck.
"""
    rhythm_lines = "\n".join(f"  {svg_filename_for_slide(idx, slide)}: {'anchor' if idx == 1 else 'breathing' if idx == len(slides) else 'dense'}" for idx, slide in enumerate(slides, start=1))
    spec_lock = f"""# Spec Lock

canvas: ppt169
viewBox: 0 0 1280 720
mode: pyramid
visual_style: {visual_style}

## colors
- background: {palette['background']}
- secondary_bg: {palette['secondary_bg']}
- primary: {palette['primary']}
- accent: {palette['accent']}
- secondary_accent: {palette['secondary_accent']}
- body_text: {palette['body_text']}

## typography
- font_family: {typography['font_family']}
- annotation_family: Inter, Arial, sans-serif
- title: {typography['title']}
- subtitle: {typography['subtitle']}
- body_large: {typography['body_large']}
- body: {typography['body']}
- annotation: {typography['annotation']}

icons: tabler-outline
images: []
page_rhythm:
{rhythm_lines}
page_layouts: {{}}
page_charts: {{}}
"""
    (project_path / "design_spec.md").write_text(design_spec, encoding="utf-8")
    (project_path / "spec_lock.md").write_text(spec_lock, encoding="utf-8")


def write_real_lite_svgs(project_path: Path, slides: list[dict[str, Any]], palette: dict[str, str], typography: dict[str, Any]) -> None:
    for folder in ["svg_output", "svg_final", "exports"]:
        target = project_path / folder
        if target.exists():
            shutil.rmtree(target)
    svg_dir = project_path / "svg_output"
    svg_dir.mkdir(parents=True, exist_ok=True)
    total = len(slides)
    for idx, slide in enumerate(slides, start=1):
        filename = svg_filename_for_slide(idx, slide)
        svg = render_slide_svg(idx, total, slide, palette, typography)
        (svg_dir / filename).write_text(svg, encoding="utf-8")


def svg_filename_for_slide(idx: int, slide: dict[str, Any]) -> str:
    slug = slugify(str(slide.get("title") or f"slide_{idx}"))
    return f"{idx:02d}_{slug}.svg"


def slugify(value: str) -> str:
    slug = re.sub(r"[^a-zA-Z0-9\u4e00-\u9fff]+", "_", value.lower()).strip("_")
    return slug[:32] or "slide"


def render_slide_svg(idx: int, total: int, slide: dict[str, Any], palette: dict[str, str], typography: dict[str, Any]) -> str:
    title_lines = wrap_text(str(slide["title"]), 24 if slide["kind"] == "cover" else 30, 2)
    subtitle = str(slide.get("subtitle", ""))
    bullets = [str(item) for item in slide.get("bullets", [])]
    if slide["kind"] == "cover":
        return render_cover_svg(idx, total, title_lines, subtitle, bullets, palette, typography)
    if slide["kind"] == "summary":
        return render_summary_svg(idx, total, title_lines, bullets, palette, typography)
    return render_content_svg(idx, total, title_lines, bullets, palette, typography)


def render_cover_svg(
    idx: int,
    total: int,
    title_lines: list[str],
    subtitle: str,
    bullets: list[str],
    palette: dict[str, str],
    typography: dict[str, Any],
) -> str:
    bullet_xml = render_bullet_list(bullets[:3], 128, 430, 34, 900, palette, typography)
    title_xml = render_text_lines(title_lines, 128, 164, typography["title"], 58, palette["primary"], weight="700")
    subtitle_lines = wrap_text(subtitle, 42, 2)
    subtitle_xml = render_text_lines(subtitle_lines, 128, 300, typography["subtitle"], 40, palette["body_text"])
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720">
  <rect width="1280" height="720" fill="{palette['background']}"/>
  <rect x="0" y="0" width="1280" height="720" fill="{palette['background']}"/>
  <rect x="64" y="56" width="1152" height="608" fill="{palette['secondary_bg']}"/>
  <rect x="92" y="88" width="16" height="168" fill="{palette['accent']}"/>
  <circle cx="1100" cy="152" r="74" fill="{palette['secondary_accent']}" opacity="0.22"/>
  <circle cx="1158" cy="210" r="38" fill="{palette['accent']}" opacity="0.28"/>
{title_xml}
{subtitle_xml}
{bullet_xml}
  <text x="1048" y="620" font-family="Inter, Arial, sans-serif" font-size="{typography['annotation']}" fill="{palette['secondary_accent']}">{idx:02d} / {total:02d}</text>
</svg>
"""


def render_content_svg(
    idx: int,
    total: int,
    title_lines: list[str],
    bullets: list[str],
    palette: dict[str, str],
    typography: dict[str, Any],
) -> str:
    title_xml = render_text_lines(title_lines, 112, 116, 38, 48, palette["primary"], weight="700")
    bullet_xml = render_bullet_list(bullets[:5], 138, 244, 70, 930, palette, typography)
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720">
  <rect width="1280" height="720" fill="{palette['background']}"/>
  <rect x="72" y="64" width="1136" height="592" fill="{palette['secondary_bg']}"/>
  <rect x="96" y="92" width="720" height="6" fill="{palette['accent']}"/>
{title_xml}
  <rect x="110" y="196" width="8" height="380" fill="{palette['secondary_accent']}"/>
{bullet_xml}
  <text x="1048" y="620" font-family="Inter, Arial, sans-serif" font-size="{typography['annotation']}" fill="{palette['secondary_accent']}">{idx:02d} / {total:02d}</text>
</svg>
"""


def render_summary_svg(
    idx: int,
    total: int,
    title_lines: list[str],
    bullets: list[str],
    palette: dict[str, str],
    typography: dict[str, Any],
) -> str:
    title_xml = render_text_lines(title_lines, 112, 126, 40, 50, palette["primary"], weight="700")
    cards = []
    for pos, bullet in enumerate(bullets[:4]):
        x = 112 + (pos % 2) * 532
        y = 236 + (pos // 2) * 154
        lines = wrap_text(bullet, 28, 3)
        text = render_text_lines(lines, x + 28, y + 54, typography["body"], 32, palette["body_text"])
        cards.append(f"""  <rect x="{x}" y="{y}" width="488" height="118" fill="{palette['background']}" stroke="{palette['secondary_accent']}" stroke-width="2"/>
  <circle cx="{x + 30}" cy="{y + 30}" r="11" fill="{palette['accent']}"/>
{text}""")
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720">
  <rect width="1280" height="720" fill="{palette['background']}"/>
  <rect x="72" y="64" width="1136" height="592" fill="{palette['secondary_bg']}"/>
  <rect x="96" y="92" width="16" height="96" fill="{palette['accent']}"/>
{title_xml}
{chr(10).join(cards)}
  <text x="1048" y="620" font-family="Inter, Arial, sans-serif" font-size="{typography['annotation']}" fill="{palette['secondary_accent']}">{idx:02d} / {total:02d}</text>
</svg>
"""


def render_bullet_list(
    bullets: list[str],
    x: int,
    y: int,
    gap: int,
    width: int,
    palette: dict[str, str],
    typography: dict[str, Any],
) -> str:
    rows: list[str] = []
    current_y = y
    for bullet in bullets:
        lines = wrap_text(bullet, max(18, width // 24), 3)
        rows.append(f'  <circle cx="{x}" cy="{current_y - 8}" r="7" fill="{palette["accent"]}"/>')
        rows.append(render_text_lines(lines, x + 24, current_y, typography["body"], 30, palette["body_text"]))
        current_y += gap + max(0, len(lines) - 1) * 28
    return "\n".join(rows)


def render_text_lines(lines: list[str], x: int, y: int, size: int, line_height: int, color: str, weight: str = "400") -> str:
    tspans = []
    for idx, line in enumerate(lines):
        tspans.append(f'<tspan x="{x}" y="{y + idx * line_height}">{escape_xml(line)}</tspan>')
    return f'  <text font-family="Noto Sans CJK SC, Inter, Arial, sans-serif" font-size="{size}" font-weight="{weight}" fill="{color}">' + "".join(tspans) + "</text>"


def wrap_text(value: str, max_units: int, max_lines: int) -> list[str]:
    words = split_wrappable_units(value)
    lines: list[str] = []
    current = ""
    for word in words:
        candidate = word if not current else current + (" " if needs_space_between(current[-1], word[0]) else "") + word
        if display_width(candidate) <= max_units:
            current = candidate
            continue
        if current:
            lines.append(current)
        current = word
        if len(lines) >= max_lines:
            break
    if current and len(lines) < max_lines:
        lines.append(current)
    if len(lines) > max_lines:
        lines = lines[:max_lines]
    if lines and display_width(lines[-1]) > max_units:
        lines[-1] = truncate_by_display_width(lines[-1], max_units - 2) + "..."
    return lines or [""]


def split_wrappable_units(value: str) -> list[str]:
    value = re.sub(r"\s+", " ", value.strip())
    if not value:
        return []
    units: list[str] = []
    for part in value.split(" "):
        units.extend(re.findall(r"[A-Za-z0-9_./:+#%&-]+|[\u4e00-\u9fff]|[^\s]", part))
    return units


def needs_space_between(previous: str, current: str) -> bool:
    if not previous or not current or ord(previous) >= 128 or ord(current) >= 128:
        return False
    if current in ".,;:!?)%":
        return False
    if previous in "([/":
        return False
    return True


def display_width(value: str) -> int:
    return sum(2 if ord(ch) > 127 else 1 for ch in value)


def truncate_by_display_width(value: str, limit: int) -> str:
    out = ""
    used = 0
    for ch in value:
        width = 2 if ord(ch) > 127 else 1
        if used + width > limit:
            break
        out += ch
        used += width
    return out.rstrip()


def write_mock_design_artifacts(project_path: Path) -> None:
    design_spec = """# Design Specification

## I. Project Intent

SlideSmith runtime smoke deck for validating the agent-compose + PPT Master execution chain.

## II. Content Outline

1. Runtime boundary
2. Workflow phases
3. Export contract

## III. Visual Direction

Operational, readable, low-decoration validation deck.

## VIII. Image Resource List

No external images are required for this smoke.
"""
    spec_lock = """# Spec Lock

canvas: ppt169
viewBox: 0 0 1280 720
mode: pyramid
visual_style: swiss-minimal

## colors
- background: #F7F7F2
- secondary_bg: #E8ECE7
- primary: #27313B
- accent: #C46A2D
- secondary_accent: #477A74
- body_text: #1E252B

## typography
- font_family: Noto Sans CJK SC, Inter, Arial, sans-serif
- annotation_family: Inter, Arial, sans-serif
- title: 46
- subtitle: 30
- body_large: 26
- body: 24
- annotation: 20

icons: tabler-outline
images: []
page_rhythm:
  01_runtime_boundary: anchor
  02_workflow_phases: dense
  03_export_contract: breathing
page_layouts: {}
page_charts: {}
"""
    (project_path / "design_spec.md").write_text(design_spec, encoding="utf-8")
    (project_path / "spec_lock.md").write_text(spec_lock, encoding="utf-8")


def write_mock_svgs(project_path: Path) -> None:
    svg_dir = project_path / "svg_output"
    svg_dir.mkdir(parents=True, exist_ok=True)
    slides = [
        ("01_runtime_boundary.svg", "SlideSmith Runtime Boundary", "agent-compose owns isolated execution; SlideSmith owns business state."),
        ("02_workflow_phases.svg", "Two-Phase Workflow", "Prepare stops at confirmation. Generate continues to SVG and PPTX export."),
        ("03_export_contract.svg", "Artifact Contract", "design_spec, spec_lock, svg_output, svg_final, exports/*.pptx."),
    ]
    for idx, (filename, title, body) in enumerate(slides, start=1):
        accent_x = 72 + (idx - 1) * 28
        svg = f"""<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720">
  <rect width="1280" height="720" fill="#F7F7F2"/>
  <rect x="64" y="56" width="1152" height="608" rx="0" fill="#E8ECE7"/>
  <rect x="{accent_x}" y="84" width="18" height="120" fill="#C46A2D"/>
  <text x="128" y="150" font-family="Noto Sans CJK SC, Inter, Arial, sans-serif" font-size="46" font-weight="700" fill="#27313B">{escape_xml(title)}</text>
  <text x="128" y="224" font-family="Noto Sans CJK SC, Inter, Arial, sans-serif" font-size="26" fill="#1E252B">{escape_xml(body)}</text>
  <line x1="128" y1="304" x2="1096" y2="304" stroke="#477A74" stroke-width="3"/>
  <text x="128" y="380" font-family="Noto Sans CJK SC, Inter, Arial, sans-serif" font-size="30" font-weight="700" fill="#27313B">Validation Signal</text>
  <text x="128" y="430" font-family="Noto Sans CJK SC, Inter, Arial, sans-serif" font-size="24" fill="#1E252B">This page was generated by the runtime smoke runner and exported by PPT Master scripts.</text>
  <text x="1048" y="620" font-family="Inter, Arial, sans-serif" font-size="20" fill="#477A74">0{idx} / 03</text>
</svg>
"""
        (svg_dir / filename).write_text(svg, encoding="utf-8")


def escape_xml(value: str) -> str:
    return value.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def _template_fill_project_path(args: argparse.Namespace) -> Path:
    value = str(getattr(args, "project_path", "") or "").strip()
    if not value:
        raise ValueError("--project-path is required")
    return Path(value).expanduser()


def _read_template_fill_report(
    project_path: Path,
    path: Path,
    expected_schema: str,
    label: str,
) -> tuple[dict[str, Any], dict[str, int]]:
    try:
        _require_template_fill_regular_file(project_path, path, label, nonempty=True)
    except ValueError as exc:
        raise RuntimeError(f"{label} was not newly produced: {exc}") from exc
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"parse {label}: {exc}") from exc
    if not isinstance(payload, dict):
        raise ValueError(f"{label} root must be a JSON object")
    if payload.get("schema") != expected_schema:
        raise ValueError(f"{label} schema = {payload.get('schema')!r}, expected {expected_schema!r}")
    raw_summary = payload.get("summary")
    if not isinstance(raw_summary, dict):
        raise ValueError(f"{label} summary must be an object")
    summary: dict[str, int] = {}
    for field in ("ok", "warn", "error"):
        value = raw_summary.get(field)
        if type(value) is not int or value < 0:
            raise ValueError(f"{label} summary.{field} must be a non-negative integer")
        summary[field] = value
    return payload, summary


def _template_fill_failure(command: str, completed: subprocess.CompletedProcess[str]) -> RuntimeError:
    details = (completed.stderr or completed.stdout or "").strip()
    suffix = f": {details}" if details else ""
    return RuntimeError(f"template fill {command} failed with exit {completed.returncode}{suffix}")


def template_fill_check(args: argparse.Namespace) -> dict[str, int]:
    inputs = discover_template_fill_inputs(_template_fill_project_path(args))
    project_path = inputs["project_path"]
    check_report = inputs["check_report"]
    set_status("template_fill_checking", project_path=str(project_path))
    check_report.unlink(missing_ok=True)

    completed = run_command(
        [
            "python3",
            str(script_path("template_fill_pptx.py")),
            "check-plan",
            str(inputs["slide_library"]),
            str(inputs["fill_plan"]),
            "-o",
            str(check_report),
        ],
        check=False,
    )
    _, summary = _read_template_fill_report(
        project_path,
        check_report,
        "template_fill_pptx_check.v1",
        "template fill check report",
    )
    if completed.returncode not in (0, 1):
        raise _template_fill_failure("check", completed)

    summary_text = f"ok={summary['ok']} warn={summary['warn']} error={summary['error']}"
    emit_event(
        "template_fill_check",
        "Template fill check completed",
        summary_text,
        "warning" if summary["error"] else "completed",
        {"summary": summary, "exit_code": completed.returncode, "check_report": str(check_report)},
    )
    set_status(
        "template_fill_checking",
        "completed",
        project_path=str(project_path),
        check_report=str(check_report),
        summary=summary,
    )
    return summary


def _timestamped_template_fill_export_pattern(export_base: Path) -> re.Pattern[str]:
    return re.compile(rf"^{re.escape(export_base.stem)}_\d{{8}}_\d{{6}}\.pptx$")


def _matching_template_fill_export_names(export_base: Path) -> set[str]:
    exports_dir = export_base.parent
    if not exports_dir.exists():
        return set()
    pattern = _timestamped_template_fill_export_pattern(export_base)
    return {path.name for path in exports_dir.iterdir() if pattern.fullmatch(path.name)}


def template_fill_apply(args: argparse.Namespace) -> Path:
    inputs = discover_template_fill_inputs(_template_fill_project_path(args))
    project_path = inputs["project_path"]
    export_base = inputs["export_base"]
    set_status("template_fill_applying", project_path=str(project_path))
    existing_exports = _matching_template_fill_export_names(export_base)
    transition = str(getattr(args, "transition", "fade") or "fade")

    completed = run_command(
        [
            "python3",
            str(script_path("template_fill_pptx.py")),
            "apply",
            str(inputs["source_pptx"]),
            str(inputs["fill_plan"]),
            "-o",
            str(export_base),
            "--transition",
            transition,
        ],
        check=False,
    )
    if completed.returncode != 0:
        raise _template_fill_failure("apply", completed)

    pattern = _timestamped_template_fill_export_pattern(export_base)
    new_exports = [
        path
        for path in export_base.parent.iterdir()
        if path.name not in existing_exports
        and pattern.fullmatch(path.name)
        and not path.is_symlink()
        and path.is_file()
        and path.stat().st_size > 0
    ] if export_base.parent.is_dir() else []
    if not new_exports:
        raise RuntimeError(f"template fill apply did not produce a new timestamped PPTX for {export_base.name}")
    export_path = max(new_exports, key=lambda path: path.name)
    emit_event(
        "template_fill_apply",
        "Template fill apply completed",
        str(export_path),
        "completed",
        {"export": str(export_path), "transition": transition},
    )
    set_status(
        "template_fill_applying",
        "completed",
        project_path=str(project_path),
        export=str(export_path),
    )
    return export_path


def template_fill_validate(args: argparse.Namespace) -> dict[str, int]:
    inputs = discover_template_fill_inputs(_template_fill_project_path(args))
    project_path = inputs["project_path"]
    readback = inputs["readback"]
    validate_report = inputs["validate_report"]
    set_status("template_fill_validating", project_path=str(project_path))
    readback.unlink(missing_ok=True)
    validate_report.unlink(missing_ok=True)

    completed = run_command(
        [
            "python3",
            str(script_path("template_fill_pptx.py")),
            "validate",
            str(project_path),
        ],
        check=False,
    )
    _, summary = _read_template_fill_report(
        project_path,
        validate_report,
        "template_fill_pptx_validate.v1",
        "template fill validate report",
    )
    try:
        _require_template_fill_regular_file(
            project_path,
            readback,
            "template fill validation readback",
            nonempty=True,
        )
    except ValueError as exc:
        raise RuntimeError(f"template fill validation readback was not newly produced: {exc}") from exc
    if completed.returncode != 0:
        raise _template_fill_failure("validate", completed)
    if summary["error"] != 0:
        raise RuntimeError(f"template fill validate report summary.error = {summary['error']}")

    summary_text = f"ok={summary['ok']} warn={summary['warn']} error={summary['error']}"
    emit_event(
        "template_fill_validate",
        "Template fill validation completed",
        summary_text,
        "warning" if summary["warn"] else "completed",
        {"summary": summary, "validate_report": str(validate_report), "readback": str(readback)},
    )
    set_status(
        "template_fill_validating",
        "completed",
        project_path=str(project_path),
        validate_report=str(validate_report),
        readback=str(readback),
        summary=summary,
    )
    return summary


def publish(args: argparse.Namespace) -> None:
    project_path = project_path_from_args(args)
    publish_project(project_path)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def publish_project(project_path: Path) -> None:
    set_status("publishing", project_path=str(project_path))
    artifact_roots = [
        project_path / "design_spec.md",
        project_path / "spec_lock.md",
        project_path / "svg_output",
        project_path / "svg_final",
        project_path / "exports",
        project_path / "validation",
        project_path / "logs",
    ]
    artifacts: list[dict[str, Any]] = []
    for root in artifact_roots:
        if not root.exists():
            continue
        files = [root] if root.is_file() else [path for path in root.rglob("*") if path.is_file()]
        for path in files:
            rel = path.relative_to(project_path)
            artifacts.append({
                "path": str(rel),
                "filename": path.name,
                "size": path.stat().st_size,
                "sha256": sha256_file(path),
                "created_at": utc_now(),
            })
    write_json(ARTIFACTS_PATH, {"project_path": str(project_path), "artifacts": artifacts})
    write_json(project_path / ".slidesmith-artifacts.json", {"project_path": str(project_path), "artifacts": artifacts})
    emit_event("artifacts", "Artifacts published", f"{len(artifacts)} files", "completed")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="SlideSmith PPT Master runtime runner")
    sub = parser.add_subparsers(dest="command", required=True)

    validate_parser = sub.add_parser("validate-env")
    validate_parser.add_argument("--profile", choices=["mock", "full"], default="full")

    prepare_parser = sub.add_parser("prepare")
    prepare_parser.add_argument("--input", default="")
    prepare_parser.add_argument("--sources-manifest", default=".slidesmith/source_inputs.json")
    prepare_parser.add_argument("--project", required=True)
    prepare_parser.add_argument("--format", default="ppt169")
    prepare_parser.add_argument("--profile", choices=["smoke", "real-lite"], default="smoke")

    generate_parser = sub.add_parser("generate")
    generate_parser.add_argument("--project", default="")
    generate_parser.add_argument("--project-path", default="")
    generate_parser.add_argument("--confirmation", choices=["mock", "existing"], default="mock")
    generate_parser.add_argument("--profile", choices=["smoke", "real-lite"], default="smoke")

    publish_parser = sub.add_parser("publish")
    publish_parser.add_argument("--project", default="")
    publish_parser.add_argument("--project-path", default="")

    template_fill_check_parser = sub.add_parser("template-fill-check")
    template_fill_check_parser.add_argument("--project-path", required=True)

    template_fill_apply_parser = sub.add_parser("template-fill-apply")
    template_fill_apply_parser.add_argument("--project-path", required=True)
    template_fill_apply_parser.add_argument("--transition", default="fade")

    template_fill_validate_parser = sub.add_parser("template-fill-validate")
    template_fill_validate_parser.add_argument("--project-path", required=True)

    return parser


def main() -> None:
    parser = build_parser()
    args = parser.parse_args()
    ensure_state_dir()
    try:
        if args.command == "validate-env":
            validate_env(args)
        elif args.command == "prepare":
            prepare(args)
        elif args.command == "generate":
            generate(args)
        elif args.command == "publish":
            publish(args)
        elif args.command == "template-fill-check":
            template_fill_check(args)
        elif args.command == "template-fill-apply":
            template_fill_apply(args)
        elif args.command == "template-fill-validate":
            template_fill_validate(args)
        else:
            parser.error(f"unsupported command: {args.command}")
    except Exception as exc:
        set_status(args.command, "failed", error=str(exc))
        raise


if __name__ == "__main__":
    main()
