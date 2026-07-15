#!/usr/bin/env python3
"""Deterministic SlideSmith resource acquisition worker for SPEC-05.

The runner consumes the hash-bound resource plan and immutable policy snapshot,
materializes project-local resources, and atomically writes the canonical
resources manifest. Networked providers are only invoked when policy permits.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import mimetypes
import os
import re
import shutil
import struct
import subprocess
import sys
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any


PLAN_SCHEMA = "slidesmith.resource_plan.v1"
POLICY_SCHEMA = "slidesmith.resource_policy.v1"
MANIFEST_SCHEMA = "slidesmith.resources_manifest.v1"
TERMINAL = {"ready", "degraded", "failed", "skipped"}
FALLBACKS = {"diagram", "shape", "text", "placeholder", "omit_optional"}


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"cannot read JSON {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise RuntimeError(f"JSON root must be an object: {path}")
    return value


def atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    tmp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    os.chmod(tmp, 0o644)
    tmp.replace(path)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def stable_hash(value: Any) -> str:
    raw = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def policy_digest(policy: dict[str, Any]) -> str:
    logical = dict(policy)
    logical["policy_sha256"] = ""
    raw = json.dumps(logical, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def contained_regular(root: Path, path: Path, *, nonempty: bool = True) -> Path:
    root = root.resolve(strict=True)
    candidate = path if path.is_absolute() else root / path
    current = root
    try:
        relative = candidate.relative_to(root)
    except ValueError as exc:
        raise RuntimeError(f"path is outside canonical root: {path}") from exc
    for part in relative.parts:
        current = current / part
        if current.is_symlink():
            raise RuntimeError(f"symlink resource path is forbidden: {current}")
    resolved = candidate.resolve(strict=True)
    try:
        resolved.relative_to(root)
    except ValueError as exc:
        raise RuntimeError(f"resource resolves outside canonical root: {path}") from exc
    if not resolved.is_file() or resolved.is_symlink():
        raise RuntimeError(f"resource is not a regular file: {path}")
    if nonempty and resolved.stat().st_size <= 0:
        raise RuntimeError(f"resource is empty: {path}")
    return resolved


def safe_output_name(value: str, resource_id: str, suffix: str) -> str:
    name = str(value or f"{resource_id}{suffix}").strip()
    if not name or name in {".", ".."} or Path(name).name != name or "/" in name or "\\" in name or "\x00" in name:
        raise RuntimeError(f"unsafe output_name: {name!r}")
    return name


def sniff_mime(path: Path) -> str:
    prefix = path.read_bytes()[:512]
    if prefix.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if prefix.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if prefix.startswith((b"GIF87a", b"GIF89a")):
        return "image/gif"
    if prefix.startswith(b"RIFF") and prefix[8:12] == b"WEBP":
        return "image/webp"
    if path.suffix.lower() == ".svg" or b"<svg" in prefix.lower():
        return "image/svg+xml"
    guessed = mimetypes.guess_type(path.name)[0]
    return guessed or "application/octet-stream"


def image_dimensions(path: Path, mime_type: str) -> tuple[int, int, bool]:
    if mime_type == "image/png":
        data = path.read_bytes()[:33]
        if len(data) >= 33:
            width, height = struct.unpack(">II", data[16:24])
            color_type = data[25]
            return width, height, color_type in {4, 6}
    if mime_type == "image/svg+xml":
        validate_svg(path)
        root = ET.parse(path).getroot()
        view_box = root.attrib.get("viewBox", "").split()
        if len(view_box) == 4:
            try:
                return int(float(view_box[2])), int(float(view_box[3])), True
            except ValueError:
                pass
        return 0, 0, True
    try:
        from PIL import Image

        with Image.open(path) as image:
            return int(image.width), int(image.height), "A" in image.getbands()
    except Exception:
        return 0, 0, False


def validate_svg(path: Path) -> None:
    raw = path.read_text(encoding="utf-8")
    lowered = raw.lower()
    if "<script" in lowered or "javascript:" in lowered:
        raise RuntimeError(f"unsafe SVG script reference: {path}")
    for marker in ("href=\"http://", "href=\"https://", "href='http://", "href='https://"):
        if marker in lowered:
            raise RuntimeError(f"external SVG reference is forbidden: {path}")
    try:
        ET.fromstring(raw)
    except ET.ParseError as exc:
        raise RuntimeError(f"invalid SVG XML {path}: {exc}") from exc


def output_contract(project: Path, relative: str, max_single: int) -> dict[str, Any]:
    relative = Path(relative).as_posix()
    path = contained_regular(project, project / relative)
    size = path.stat().st_size
    if size > max_single:
        raise RuntimeError(f"resource exceeds max_single_bytes: {relative}")
    mime_type = sniff_mime(path)
    width, height, has_alpha = image_dimensions(path, mime_type)
    return {
        "path": relative,
        "mime_type": mime_type,
        "size": size,
        "sha256": sha256_file(path),
        "width": width,
        "height": height,
        "has_alpha": has_alpha,
    }


def run_command(args: list[str], *, timeout: int, cwd: Path) -> None:
    completed = subprocess.run(
        args,
        cwd=cwd,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=timeout,
        check=False,
    )
    if completed.returncode != 0:
        command_name = Path(args[1] if len(args) > 1 else args[0]).name
        raise RuntimeError(f"{command_name}_failed_{completed.returncode}")


def policy_allows(item: dict[str, Any], policy: dict[str, Any]) -> tuple[bool, str]:
    via = str(item.get("acquire_via") or "").lower()
    confirmation = set(policy.get("confirmation_image_sources") or [])
    if via == "web":
        if "web" not in confirmation:
            return False, "confirmation_denied"
        if not policy.get("network_enabled") or not policy.get("web_image_enabled"):
            return False, "policy_denied"
    elif via == "ai":
        if "ai" not in confirmation:
            return False, "confirmation_denied"
        if not policy.get("network_enabled") or not policy.get("ai_image_enabled"):
            return False, "policy_denied"
        ai_path = str(policy.get("image_ai_path") or "api").lower()
        if ai_path not in set(policy.get("allowed_ai_paths") or []):
            return False, f"{ai_path}_path_not_supported"
        if ai_path != "api":
            return False, f"{ai_path}_path_not_supported"
    elif via == "formula":
        if str(policy.get("formula_policy") or "none").lower() not in {"mixed", "render-all"}:
            return False, "formula_policy_denied"
        if not policy.get("network_enabled") or not policy.get("formula_network_enabled"):
            return False, "formula_network_denied"
    elif via == "icon":
        icon_library = str(policy.get("icon_library") or "none").lower()
        icon_name = str(item.get("source_reference") or item.get("prompt_or_query") or "").lower()
        if icon_library == "none" or not icon_name.startswith(f"{icon_library}/"):
            return False, "icon_library_denied"
    elif via == "user":
        if not confirmation.intersection({"provided", "user", "source"}):
            return False, "confirmation_denied"
    return True, ""


def fallback_result(item: dict[str, Any], reason: str, attempt: int) -> dict[str, Any]:
    fallback = str(item.get("fallback") or "").lower()
    required = bool(item.get("required"))
    if fallback == "omit_optional" and not required:
        status = "skipped"
    elif fallback in FALLBACKS and fallback != "omit_optional":
        status = "degraded"
    else:
        status = "failed"
    return base_manifest_item(item, attempt) | {
        "status": status,
        "fallback": {"type": fallback, "reason": reason} if fallback else {"type": "", "reason": reason},
        "error": None if status in {"degraded", "skipped"} else {"code": reason, "message": reason},
    }


def base_manifest_item(item: dict[str, Any], attempt: int) -> dict[str, Any]:
    prompt_hash = stable_hash(str(item.get("prompt_or_query") or item.get("expression") or ""))
    publishable = item.get("publishable")
    return {
        "id": item["id"],
        "page": int(item["page"]),
        "type": item["type"],
        "purpose": item.get("purpose", ""),
        "required": bool(item.get("required")),
        "acquire_via": item["acquire_via"],
        "provider": item.get("_resolved_provider") or item.get("provider", ""),
        "status": "planned",
        "attempt": attempt,
        "input": {
            "source_reference": item.get("source_reference", ""),
            "prompt_or_query_sha256": prompt_hash,
        },
        "output": None,
        "provenance": {"source_url": "", "provider_asset_id": "", "license": "", "license_url": "", "author": ""},
        "fallback": {"type": item.get("fallback", ""), "reason": ""},
        "publishable": bool(publishable) if publishable is not None else True,
        "error": None,
    }


def source_input_hash(item: dict[str, Any], project: Path) -> str:
    source_reference = str(item.get("source_reference") or "").split("#", 1)[0]
    is_user_source = source_reference.startswith("sources/")
    is_beautify_source = str(item.get("acquire_via") or "") == "source" and source_reference.startswith("images/")
    if not source_reference or not (is_user_source or is_beautify_source):
        return ""
    try:
        return sha256_file(contained_regular(project, project / source_reference))
    except Exception:
        return "source_unavailable"


def cache_key(item: dict[str, Any], policy: dict[str, Any], project: Path) -> str:
    relevant = {
        "item": item,
        "network_enabled": policy.get("network_enabled"),
        "web_image_enabled": policy.get("web_image_enabled"),
        "ai_image_enabled": policy.get("ai_image_enabled"),
        "formula_network_enabled": policy.get("formula_network_enabled"),
        "image_ai_path": policy.get("image_ai_path"),
        "icon_library": policy.get("icon_library"),
        "formula_policy": policy.get("formula_policy"),
        "providers": [policy.get("allowed_web_providers"), policy.get("allowed_ai_providers")],
        "source_sha256": source_input_hash(item, project),
    }
    return stable_hash(relevant)


def reusable(existing: dict[str, Any] | None, item: dict[str, Any], policy: dict[str, Any], project: Path) -> dict[str, Any] | None:
    if not existing or existing.get("cache_key") != cache_key(item, policy, project):
        return None
    if existing.get("status") not in {"ready", "degraded", "skipped"}:
        return None
    output = existing.get("output")
    if output:
        try:
            current = output_contract(project, output["path"], int(policy["max_single_bytes"]))
        except Exception:
            return None
        if current.get("sha256") != output.get("sha256") or current.get("size") != output.get("size"):
            return None
        existing = dict(existing)
        existing["output"] = current
    existing = dict(existing)
    existing["cache_reused"] = True
    existing["attempt"] = max(1, int(existing.get("attempt") or 1))
    return existing


def cleanup_stale_outputs(
    existing_map: dict[str, dict[str, Any]],
    items: list[dict[str, Any]],
    policy: dict[str, Any],
    project: Path,
) -> None:
    by_id = {str(item.get("id")): item for item in items}
    reusable_paths: set[str] = set()
    for resource_id, item in by_id.items():
        cached = reusable(existing_map.get(resource_id), item, policy, project)
        if cached and isinstance(cached.get("output"), dict):
            reusable_paths.add(Path(str(cached["output"].get("path") or "")).as_posix())
    for resource_id, existing in existing_map.items():
        output = existing.get("output")
        if not isinstance(output, dict):
            continue
        relative = Path(str(output.get("path") or "")).as_posix()
        if not relative or relative in reusable_paths:
            continue
        if not any(relative.startswith(prefix) for prefix in ("images/", "icons/", "charts/")):
            raise RuntimeError("stale resource output has a forbidden path")
        candidate = project / relative
        if not candidate.exists() and not candidate.is_symlink():
            continue
        if candidate.is_symlink():
            raise RuntimeError("stale resource output is a symlink")
        contained_regular(project, candidate).unlink()


def cleanup_unused_intermediates(project: Path, items: list[dict[str, Any]]) -> None:
    vias = {str(item.get("acquire_via") or "") for item in items}
    by_via = {
        "ai": ("image_prompts.json", "image_prompts.md"),
        "web": ("image_queries.json", "image_sources.json"),
        "formula": ("formula_manifest.json",),
    }
    for via, names in by_via.items():
        if via in vias:
            continue
        for name in names:
            path = project / "images" / name
            if path.is_symlink():
                raise RuntimeError("resource intermediate is a symlink")
            if path.is_file():
                path.unlink()


def copy_local_resource(project: Path, source: Path, target_rel: str, max_single: int) -> dict[str, Any]:
    source = contained_regular(project, source)
    return copy_validated_resource(project, source, target_rel, max_single)


def copy_validated_resource(project: Path, source: Path, target_rel: str, max_single: int) -> dict[str, Any]:
    target = project / target_rel
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists() and target.is_symlink():
        raise RuntimeError(f"target is a symlink: {target}")
    tmp = target.with_name(f".{target.name}.{os.getpid()}.tmp")
    shutil.copyfile(source, tmp)
    os.chmod(tmp, 0o644)
    tmp.replace(target)
    return output_contract(project, target_rel, max_single)


def resolved_provider(item: dict[str, Any], allowed: set[str], kind: str) -> str:
    if not allowed:
        raise RuntimeError(f"{kind}_provider_not_configured")
    requested = str(item.get("provider") or "").lower().strip()
    provider = requested or sorted(allowed)[0]
    if provider not in allowed:
        raise RuntimeError(f"{kind}_provider_not_allowlisted")
    item["_resolved_provider"] = provider
    return provider


def safe_error_reason(exc: Exception) -> str:
    value = str(exc).lower()
    for token in (
        "provider_not_configured", "provider_not_allowlisted", "path_not_supported",
        "policy_denied", "confirmation_denied", "formula_network_denied",
        "formula_policy_denied", "icon_library_denied",
        "parent_not_ready", "max_single_bytes", "timeout",
    ):
        if token in value:
            return token
    if re.fullmatch(r"[a-z0-9_.-]+_failed_[0-9]+", value):
        return value
    return "resource_execution_failed"


def prepare_formula_outputs(items: list[dict[str, Any]], project: Path, skill_root: Path, policy: dict[str, Any]) -> dict[str, dict[str, Any]]:
    allowed_items = [item for item in items if policy_allows(item, policy)[0]]
    if not allowed_items:
        return {}
    manifest_path = project / "images" / "formula_manifest.json"
    manifest = {
        "items": [
            {
                "id": item["id"],
                "latex": item.get("expression") or item.get("prompt_or_query"),
                "filename": safe_output_name(item.get("output_name", ""), item["id"], ".png"),
                "status": "Pending",
            }
            for item in allowed_items
        ]
    }
    atomic_json(manifest_path, manifest)
    run_command(
        [sys.executable, str(skill_root / "scripts" / "latex_render.py"), str(project), "--manifest", str(manifest_path)],
        timeout=int(policy["timeout_seconds"]),
        cwd=project.parent.parent,
    )
    return {
        item["id"]: {
            "path": f"images/{safe_output_name(item.get('output_name', ''), item['id'], '.png')}",
            "provider": "latex_render",
        }
        for item in allowed_items
    }


def prepare_web_outputs(items: list[dict[str, Any]], project: Path, skill_root: Path, policy: dict[str, Any]) -> dict[str, dict[str, Any]]:
    allowed = [item for item in items if policy_allows(item, policy)[0]]
    if not allowed:
        return {}
    providers = set(policy.get("allowed_web_providers") or [])
    queries = {"items": []}
    for item in allowed:
        provider = resolved_provider(item, providers, "web")
        queries["items"].append({
            "id": item["id"], "query": item.get("prompt_or_query", ""),
            "purpose": item.get("purpose", ""), "slide": str(item.get("page", "")),
            "filename": safe_output_name(item.get("output_name", ""), item["id"], ".jpg"),
            "provider": provider or None, "status": "Pending",
        })
    query_path = project / "images" / "image_queries.json"
    source_path = project / "images" / "image_sources.json"
    atomic_json(query_path, queries)
    command = [sys.executable, str(skill_root / "scripts" / "image_search.py"), "--batch", str(query_path), "-o", str(project / "images"), "--manifest", str(source_path)]
    run_command(command, timeout=int(policy["timeout_seconds"]), cwd=project.parent.parent)
    source_items = []
    if source_path.exists():
        source_items = list(load_json(source_path).get("items") or [])
    by_id = {str(row.get("id")): row for row in source_items if isinstance(row, dict) and row.get("id")}
    by_filename = {str(row.get("filename")): row for row in source_items if isinstance(row, dict) and row.get("filename")}
    outputs: dict[str, dict[str, Any]] = {}
    for item in allowed:
        filename = safe_output_name(item.get("output_name", ""), item["id"], ".jpg")
        source = by_id.get(item["id"]) or by_filename.get(filename) or {}
        outputs[item["id"]] = {
            "path": f"images/{filename}",
            "provider": item["_resolved_provider"],
            "provenance": {
                "source_url": source.get("source_url") or source.get("url") or "",
                "provider_asset_id": source.get("provider_asset_id") or source.get("asset_id") or "",
                "license": source.get("license") or "",
                "license_url": source.get("license_url") or "",
                "author": source.get("author") or "",
            },
        }
    return outputs


def prepare_ai_outputs(items: list[dict[str, Any]], project: Path, skill_root: Path, policy: dict[str, Any]) -> dict[str, dict[str, Any]]:
    allowed = [item for item in items if policy_allows(item, policy)[0]]
    if not allowed:
        return {}
    providers = set(policy.get("allowed_ai_providers") or [])
    prompts = {"project": project.name, "items": []}
    for item in allowed:
        provider = resolved_provider(item, providers, "ai")
        prompts["items"].append({
            "filename": safe_output_name(item.get("output_name", ""), item["id"], ".png"),
            "prompt": item.get("prompt_or_query", ""),
            "purpose": item.get("purpose", ""),
            "type": item.get("type", "image"),
            "aspect_ratio": str((item.get("parameters") or {}).get("aspect_ratio") or "16:9"),
            "image_size": str((item.get("parameters") or {}).get("image_size") or "1K"),
            "status": "Pending",
        })
    prompt_path = project / "images" / "image_prompts.json"
    atomic_json(prompt_path, prompts)
    image_gen = str(skill_root / "scripts" / "image_gen.py")
    run_command([sys.executable, image_gen, "--render-md", str(prompt_path)], timeout=int(policy["timeout_seconds"]), cwd=project.parent.parent)
    command = [sys.executable, image_gen, "--manifest", str(prompt_path), "-o", str(project / "images")]
    chosen_providers = {str(item["_resolved_provider"]) for item in allowed}
    if len(chosen_providers) != 1:
        raise RuntimeError("ai_provider_batch_mismatch")
    command.extend(["--backend", next(iter(chosen_providers))])
    run_command(command, timeout=int(policy["timeout_seconds"]), cwd=project.parent.parent)
    return {
        item["id"]: {
            "path": f"images/{safe_output_name(item.get('output_name', ''), item['id'], '.png')}",
            "provider": item["_resolved_provider"],
        }
        for item in allowed
    }


def execute_item(
    item: dict[str, Any], project: Path, workspace: Path, skill_root: Path,
    policy: dict[str, Any], attempt: int, prepared: dict[str, dict[str, Any]],
    completed: dict[str, dict[str, Any]],
) -> dict[str, Any]:
    base = base_manifest_item(item, attempt)
    base["cache_key"] = cache_key(item, policy, project)
    allowed, reason = policy_allows(item, policy)
    if not allowed:
        result = fallback_result(item, reason, attempt)
        result["cache_key"] = base["cache_key"]
        return result
    via = item["acquire_via"]
    max_single = int(policy["max_single_bytes"])
    try:
        if via == "placeholder":
            result = fallback_result(item, "approved_placeholder", attempt)
            result["cache_key"] = base["cache_key"]
            return result
        if via in {"web", "ai", "formula"}:
            prepared_item = prepared[item["id"]]
            relative = str(prepared_item["path"])
            base["output"] = output_contract(project, relative, max_single)
            base["provider"] = prepared_item.get("provider", base["provider"])
            if prepared_item.get("provenance"):
                base["provenance"] = prepared_item["provenance"]
        elif via == "user":
            source_ref = Path(str(item.get("source_reference") or ""))
            if not source_ref.as_posix().startswith("sources/"):
                raise RuntimeError("user/source resource must reference project sources/")
            name = safe_output_name(item.get("output_name", ""), item["id"], source_ref.suffix or ".bin")
            base["output"] = copy_local_resource(project, project / source_ref, f"images/{name}", max_single)
        elif via == "source" and item["type"] == "image":
            source_ref = Path(str(item.get("source_reference") or ""))
            if source_ref.is_absolute() or ".." in source_ref.parts or not source_ref.as_posix().startswith(("images/", "sources/")):
                raise RuntimeError("source image must reference project images/ or sources/")
            name = safe_output_name(item.get("output_name", ""), item["id"], source_ref.suffix or ".bin")
            base["output"] = copy_local_resource(project, project / source_ref, f"images/acquired/{name}", max_single)
        elif via == "template":
            resolution = load_json(workspace / ".slidesmith" / "template_resolution.json")
            template_root = workspace / str(resolution.get("template_root") or "")
            template_root = template_root.resolve(strict=True)
            template_root.relative_to(workspace.resolve(strict=True))
            source = contained_regular(template_root, template_root / str(item.get("source_reference") or ""))
            name = safe_output_name(item.get("output_name", ""), item["id"], source.suffix or ".bin")
            base["output"] = copy_validated_resource(project, source, f"images/{name}", max_single)
        elif via == "icon":
            icon_name = str(item.get("source_reference") or item.get("prompt_or_query") or "")
            if not re.fullmatch(r"[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+)?", icon_name):
                raise RuntimeError("icon resource has no icon name")
            run_command([sys.executable, str(skill_root / "scripts" / "icon_sync.py"), str(project), icon_name], timeout=int(policy["timeout_seconds"]), cwd=workspace)
            lib, name = icon_name.split("/", 1) if "/" in icon_name else ("chunk-filled", icon_name)
            if lib == "chunk":
                lib = "chunk-filled"
            base["output"] = output_contract(project, f"icons/{lib}/{name}.svg", max_single)
        elif via == "chart_template":
            chart_name = str(item.get("source_reference") or item.get("prompt_or_query") or "").removesuffix(".svg")
            if not re.fullmatch(r"[A-Za-z0-9_-]+", chart_name):
                raise RuntimeError("invalid chart template name")
            index = load_json(skill_root / "templates" / "charts" / "charts_index.json")
            if chart_name not in (index.get("charts") or {}):
                raise RuntimeError(f"chart template is not in charts_index.json: {chart_name}")
            source = contained_regular(skill_root, skill_root / "templates" / "charts" / f"{chart_name}.svg")
            target_rel = f"charts/templates/{safe_output_name(item.get('output_name', ''), item['id'], '.svg')}"
            base["output"] = copy_validated_resource(project, source, target_rel, max_single)
        elif via == "source" and item["type"] == "chart_data":
            name = safe_output_name(item.get("output_name", ""), item["id"], ".json")
            relative = f"charts/data/{name}"
            payload = {"schema": "slidesmith.chart_data.v1", "resource_id": item["id"], "data": item.get("data"), "citation": item.get("citation"), "source_reference": item.get("source_reference", "")}
            atomic_json(project / relative, payload)
            base["output"] = output_contract(project, relative, max_single)
        elif via == "slice":
            parent_id = str(item.get("parent_id") or "")
            parent = completed.get(parent_id)
            if not parent or parent.get("status") != "ready" or not parent.get("output"):
                raise RuntimeError(f"slice parent is not ready: {parent_id}")
            parameters = item.get("parameters") or {}
            grid = str(parameters.get("grid") or "")
            if not grid:
                raise RuntimeError("slice resource has no grid")
            output_dir = project / "images" / "slices"
            command = [sys.executable, str(skill_root / "scripts" / "slice_images.py"), str(project / parent["output"]["path"]), "--grid", grid, "-o", str(output_dir)]
            if parameters.get("names"):
                names = parameters["names"]
                names = names if isinstance(names, list) else [str(names)]
                names = [safe_output_name(str(name), item["id"], ".png") for name in names]
                command.extend(["--names", ",".join(names)])
            if parameters.get("trim"):
                command.append("--trim")
            if parameters.get("alpha"):
                command.append("--alpha")
            run_command(command, timeout=int(policy["timeout_seconds"]), cwd=workspace)
            name = safe_output_name(item.get("output_name", ""), item["id"], ".png")
            base["output"] = output_contract(project, f"images/slices/{name}", max_single)
            base["input"]["parent_id"] = parent_id
            base["input"]["parent_sha256"] = parent["output"]["sha256"]
        else:
            raise RuntimeError(f"unsupported deterministic acquire path: {via}")
        base["status"] = "ready"
        return base
    except Exception as exc:
        result = fallback_result(item, safe_error_reason(exc), attempt)
        result["cache_key"] = base["cache_key"]
        return result


def refresh_image_analysis(project: Path, skill_root: Path, policy: dict[str, Any]) -> None:
    images = project / "images"
    images.mkdir(parents=True, exist_ok=True)
    analysis = project / "analysis"
    analysis.mkdir(parents=True, exist_ok=True)
    entries = list(images.rglob("*"))
    if any(path.is_symlink() for path in entries):
        raise RuntimeError("symlink in project images is forbidden")
    candidates = [path for path in entries if path.is_file() and path.suffix.lower() in {".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"}]
    if candidates:
        run_command([sys.executable, str(skill_root / "scripts" / "analyze_images.py"), str(images)], timeout=int(policy["timeout_seconds"]), cwd=project.parent.parent)
    csv_path = analysis / "image_analysis.csv"
    if not csv_path.exists():
        csv_path.write_text("No,Filename,Width,Height,AspectRatio,SizeKB,Category\n", encoding="utf-8")


def build_requirements(plan: dict[str, Any], policy: dict[str, Any]) -> dict[str, Any]:
    rows = []
    for item in plan.get("requirements") or []:
        allowed, reason = policy_allows(item, policy)
        rows.append({
            "id": item["id"], "page": item["page"], "type": item["type"],
            "acquire_via": item["acquire_via"], "required": bool(item.get("required")),
            "provider": item.get("provider", ""), "fallback": item.get("fallback", ""),
            "initial_status": "pending" if allowed else "degraded" if item.get("fallback") else "failed",
            "policy_reason": reason, "item_sha256": stable_hash(item),
        })
    return {"schema": "slidesmith.resource_requirements.v1", "task_id": plan["task_id"], "policy_sha256": policy["policy_sha256"], "requirements": rows}


def summarize(resources: list[dict[str, Any]]) -> dict[str, int]:
    summary = {"total": len(resources), "ready": 0, "degraded": 0, "failed": 0, "pending": 0, "required_failed": 0, "bytes": 0}
    for item in resources:
        status = item.get("status")
        if status in summary:
            summary[status] += 1
        if status not in TERMINAL:
            summary["pending"] += 1
        if status == "failed" and item.get("required"):
            summary["required_failed"] += 1
        if item.get("output"):
            summary["bytes"] += int(item["output"].get("size") or 0)
    return summary


def run(project: Path, skill_root: Path, phase_run_id: str) -> int:
    project = project.resolve(strict=True)
    workspace = project.parent.parent.resolve(strict=True)
    skill_root = skill_root.resolve(strict=True)
    skill_root.relative_to(workspace)
    plan_path = project / ".slidesmith" / "resource_plan.json"
    policy_path = project / ".slidesmith" / "resource_policy.json"
    plan = load_json(plan_path)
    policy = load_json(policy_path)
    if plan.get("schema") != PLAN_SCHEMA or policy.get("schema") != POLICY_SCHEMA:
        raise RuntimeError("resource plan or policy schema mismatch")
    if not policy.get("phase_enabled"):
        raise RuntimeError("resource_phase_disabled")
    if plan.get("task_id") != policy.get("task_id") or policy.get("phase_run_id") != phase_run_id:
        raise RuntimeError("resource plan/policy task or phase binding mismatch")
    if policy.get("policy_sha256") != policy_digest(policy):
        raise RuntimeError("resource policy digest mismatch")
    upstream_hashes = {
        "spec_sha256": project / "design_spec.md",
        "spec_lock_sha256": project / "spec_lock.md",
        "confirmation_sha256": project / "confirm_ui" / "result.json",
    }
    for field, path in upstream_hashes.items():
        if plan.get(field) != sha256_file(contained_regular(project, path)):
            raise RuntimeError(f"resource plan {field} binding mismatch")
    if policy.get("confirmation_sha256") != plan.get("confirmation_sha256"):
        raise RuntimeError("resource policy confirmation binding mismatch")
    requirements = build_requirements(plan, policy)
    atomic_json(project / "analysis" / "resource_requirements.json", requirements)

    manifest_path = project / ".slidesmith" / "resources_manifest.json"
    existing_map: dict[str, dict[str, Any]] = {}
    if manifest_path.exists():
        existing = load_json(manifest_path)
        existing_map = {str(item.get("id")): item for item in existing.get("resources") or [] if isinstance(item, dict)}

    items = list(plan.get("requirements") or [])
    cleanup_stale_outputs(existing_map, items, policy, project)
    cleanup_unused_intermediates(project, items)
    prepared: dict[str, dict[str, Any]] = {}
    group_errors: dict[str, str] = {}
    for via, prepare in (("formula", prepare_formula_outputs), ("web", prepare_web_outputs), ("ai", prepare_ai_outputs)):
        group = [item for item in items if item.get("acquire_via") == via and reusable(existing_map.get(item["id"]), item, policy, project) is None]
        if not group:
            continue
        try:
            prepared.update(prepare(group, project, skill_root, policy))
        except Exception as exc:
            for item in group:
                group_errors[item["id"]] = safe_error_reason(exc)

    resources: list[dict[str, Any]] = []
    completed: dict[str, dict[str, Any]] = {}
    ordered = [item for item in items if item.get("acquire_via") != "slice"] + [item for item in items if item.get("acquire_via") == "slice"]
    for item in ordered:
        cached = reusable(existing_map.get(item["id"]), item, policy, project)
        if cached is not None:
            result = cached
        elif item["id"] in group_errors:
            result = fallback_result(item, group_errors[item["id"]], int((existing_map.get(item["id"]) or {}).get("attempt") or 0) + 1)
            result["cache_key"] = cache_key(item, policy, project)
        else:
            attempt = int((existing_map.get(item["id"]) or {}).get("attempt") or 0) + 1
            result = execute_item(item, project, workspace, skill_root, policy, attempt, prepared, completed)
        resources.append(result)
        completed[result["id"]] = result

    refresh_image_analysis(project, skill_root, policy)
    resources.sort(key=lambda item: next(index for index, source in enumerate(items) if source["id"] == item["id"]))
    summary = summarize(resources)
    if len([item for item in resources if item.get("output")]) > int(policy["max_files"]):
        summary["required_failed"] += 1
    if summary["bytes"] > int(policy["max_total_bytes"]):
        summary["required_failed"] += 1
    from datetime import datetime, timezone

    now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    manifest = {
        "schema": MANIFEST_SCHEMA,
        "task_id": plan["task_id"],
        "route": policy["route"],
        "runner_profile": policy["runner_profile"],
        "project_path": f"projects/{project.name}",
        "resource_plan_sha256": sha256_file(plan_path),
        "policy_sha256": policy["policy_sha256"],
        "spec_sha256": plan["spec_sha256"],
        "spec_lock_sha256": plan["spec_lock_sha256"],
        "phase_run_id": phase_run_id,
        "resources": resources,
        "summary": summary,
        "created_at": now,
        "completed_at": now,
    }
    atomic_json(manifest_path, manifest)
    return 0 if summary["required_failed"] == 0 and summary["failed"] == 0 and summary["pending"] == 0 else 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Prepare and validate SlideSmith task resources")
    parser.add_argument("project_path")
    parser.add_argument("--skill-root", required=True)
    parser.add_argument("--phase-run-id", required=True)
    args = parser.parse_args(argv)
    try:
        return run(Path(args.project_path), Path(args.skill_root), args.phase_run_id)
    except Exception as exc:
        print(f"[resource_runner] {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
