#!/usr/bin/env python3
"""Generate the pinned Unicode Default full case-fold asset used by Go and Python."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path


UNICODE_VERSION = "15.0.0"
SOURCE_SHA256 = "cdd49e55eae3bbf1f0a3f6580c974a0263cb86a6a08daa10fbf705b4808a56f7"
SOURCE_URL = "https://www.unicode.org/Public/15.0.0/ucd/CaseFolding.txt"
EXPECTED_MAPPING_COUNT = 1530
ASSET_SHA256 = "11272a5b74c86e20065be587da38ef2291c08caec383908b3acbad8ed583feb1"


def parse_default_full_casefold(source: bytes) -> dict[str, str]:
    digest = hashlib.sha256(source).hexdigest()
    if digest != SOURCE_SHA256:
        raise ValueError(f"CaseFolding.txt SHA-256 = {digest}, want {SOURCE_SHA256}")
    text = source.decode("utf-8")
    if not text.startswith("# CaseFolding-15.0.0.txt\n"):
        raise ValueError("CaseFolding.txt is not the pinned Unicode 15.0.0 source")

    mappings: dict[str, str] = {}
    for line_number, raw_line in enumerate(text.splitlines(), 1):
        data = raw_line.split("#", 1)[0].strip()
        if not data:
            continue
        fields = [field.strip() for field in data.split(";")]
        if fields and fields[-1] == "":
            fields.pop()
        if len(fields) != 3:
            raise ValueError(f"line {line_number}: malformed CaseFolding entry")
        codepoint, status, mapping = fields
        if status not in {"C", "F"}:
            continue
        source_value = int(codepoint, 16)
        targets = [int(item, 16) for item in mapping.split()]
        if not targets:
            raise ValueError(f"line {line_number}: empty mapping")
        key = f"{source_value:06X}"
        value = " ".join(f"{target:06X}" for target in targets)
        if key in mappings:
            raise ValueError(f"line {line_number}: duplicate default-full mapping for {key}")
        mappings[key] = value

    if len(mappings) != EXPECTED_MAPPING_COUNT:
        raise ValueError(
            f"default-full mapping count = {len(mappings)}, want {EXPECTED_MAPPING_COUNT}"
        )
    return mappings


def render_asset(mappings: dict[str, str]) -> bytes:
    payload = {
        "schema": "slidesmith.unicode_casefold.v1",
        "unicode_version": UNICODE_VERSION,
        "mapping_statuses": ["C", "F"],
        "source_url": SOURCE_URL,
        "source_sha256": SOURCE_SHA256,
        "mapping_count": EXPECTED_MAPPING_COUNT,
        "mappings": mappings,
    }
    return (json.dumps(payload, ensure_ascii=True, indent=2, sort_keys=True) + "\n").encode("ascii")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("outputs", nargs="+", type=Path)
    args = parser.parse_args()

    asset = render_asset(parse_default_full_casefold(args.source.read_bytes()))
    digest = hashlib.sha256(asset).hexdigest()
    if digest != ASSET_SHA256:
        raise ValueError(f"generated asset SHA-256 = {digest}, want {ASSET_SHA256}")
    for output in args.outputs:
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_bytes(asset)
    print(f"wrote {len(asset)} bytes to {len(args.outputs)} outputs")
    print(f"asset_sha256={digest}")


if __name__ == "__main__":
    main()
