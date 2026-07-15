#!/usr/bin/env python3
"""Shared, deterministic quality report primitives for SlideSmith SPEC-07."""

from __future__ import annotations

import hashlib
import json
import os
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable


SEVERITIES = ("blocking", "error", "warning", "info")
DECISIONS = ("pass", "pass_with_warnings", "fail")
STATUSES = ("open", "fixed_by_retry", "accepted_by_policy")


class QualityError(RuntimeError):
    """A stable, machine-classifiable quality runner failure."""

    def __init__(self, rule: str, message: str, *, stage: str = "quality_runner") -> None:
        super().__init__(message)
        self.rule = rule
        self.stage = stage


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        temp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        os.chmod(temp, 0o644)
        temp.replace(path)
    finally:
        if temp.exists():
            temp.unlink()


def safe_text(value: Any, limit: int = 320) -> str:
    text = " ".join(str(value or "").replace("\x00", "").split())
    return text[:limit]


def finding(
    *,
    rule: str,
    severity: str,
    stage: str,
    message: str,
    page_id: str = "",
    artifact: str = "",
    element_ids: Iterable[str] = (),
    evidence: dict[str, Any] | None = None,
    owner_phase: str = "svg_execute",
    retry_phase: str | None = None,
) -> dict[str, Any]:
    if severity not in SEVERITIES:
        raise ValueError(f"unsupported severity {severity!r}")
    if not rule or rule.lower() != rule or "." not in rule:
        raise ValueError(f"invalid stable rule {rule!r}")
    page = safe_text(page_id, 16)
    artifact_path = safe_text(artifact, 240)
    stable_id = ":".join(part for part in (stage, page or "deck", rule) if part)
    safe_evidence: dict[str, Any] = {}
    for key, value in (evidence or {}).items():
        if isinstance(value, (bool, int, float)) or value is None:
            safe_evidence[safe_text(key, 64)] = value
        elif isinstance(value, list):
            safe_evidence[safe_text(key, 64)] = [safe_text(item, 120) for item in value[:20]]
        else:
            safe_evidence[safe_text(key, 64)] = safe_text(value)
    return {
        "id": stable_id,
        "stage": stage,
        "rule": rule,
        "severity": severity,
        "status": "open",
        "page_id": page,
        "artifact": artifact_path,
        "element_ids": [safe_text(item, 128) for item in element_ids if safe_text(item, 128)][:32],
        "message": safe_text(message),
        "evidence": safe_evidence,
        "remediation": {
            "owner_phase": owner_phase,
            "retry_phase": retry_phase or owner_phase,
        },
    }


def summarize(findings: Iterable[dict[str, Any]]) -> dict[str, Any]:
    counts = {severity: 0 for severity in SEVERITIES}
    for item in findings:
        severity = item.get("severity")
        if severity not in counts:
            raise ValueError(f"finding has invalid severity {severity!r}")
        counts[severity] += 1
    if counts["blocking"] or counts["error"]:
        decision = "fail"
    elif counts["warning"]:
        decision = "pass_with_warnings"
    else:
        decision = "pass"
    return {**counts, "decision": decision}


def report_ref(project: Path, relative_path: str) -> dict[str, str]:
    path = project / relative_path
    return {"path": relative_path, "sha256": sha256_file(path)}
