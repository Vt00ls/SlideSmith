#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parent))
import ppt_runner  # noqa: E402


CONTENT_TYPES = {
    ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml",
    ".pptm": "application/vnd.ms-powerpoint.presentation.macroEnabled.main+xml",
    ".ppsx": "application/vnd.openxmlformats-officedocument.presentationml.slideshow.main+xml",
    ".ppsm": "application/vnd.ms-powerpoint.slideshow.macroEnabled.main+xml",
    ".potx": "application/vnd.openxmlformats-officedocument.presentationml.template.main+xml",
    ".potm": "application/vnd.ms-powerpoint.template.macroEnabled.main+xml",
}

CONTENT_TYPES_NAMESPACE = "http://schemas.openxmlformats.org/package/2006/content-types"
PRESENTATIONML_NAMESPACE = "http://schemas.openxmlformats.org/presentationml/2006/main"

RUNNER_PATH = Path(__file__).resolve().with_name("ppt_runner.py")


class PPTSourceStagingTests(unittest.TestCase):
    def test_stage_normalizes_slideshow_and_template_main_content_types(self) -> None:
        expected = {
            ".ppsx": CONTENT_TYPES[".pptx"],
            ".ppsm": CONTENT_TYPES[".pptm"],
            ".potx": CONTENT_TYPES[".pptx"],
            ".potm": CONTENT_TYPES[".pptm"],
        }
        for extension, expected_content_type in expected.items():
            with self.subTest(extension=extension), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                source = root / f"deck{extension}"
                write_ooxml_package(source, CONTENT_TYPES[extension])

                staged = stage_one(source, root / "staged")

                self.assertEqual(read_main_content_type(staged), expected_content_type)
                self.assertEqual(read_zip_entry(staged, "ppt/marker.bin"), b"preserve-me")
                self.assertEqual(staged.name, source.name)

    def test_stage_keeps_pptx_and_pptm_byte_for_byte(self) -> None:
        for extension in (".pptx", ".pptm"):
            with self.subTest(extension=extension), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                source = root / f"deck{extension}"
                write_ooxml_package(source, CONTENT_TYPES[extension])
                original = source.read_bytes()

                staged = stage_one(source, root / "staged")

                self.assertEqual(staged.read_bytes(), original)

    def test_stage_rejects_malformed_byte_preserved_and_normalized_packages(self) -> None:
        for extension in (".pptx", ".ppsx"):
            with self.subTest(extension=extension), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                source = root / f"broken{extension}"
                source.write_bytes(b"not a zip package")

                with self.assertRaisesRegex(ValueError, "malformed OOXML presentation package"):
                    stage_one(source, root / "staged")

    def test_stage_rejects_missing_expected_main_content_type(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "wrong.ppsx"
            write_ooxml_package(source, CONTENT_TYPES[".pptx"])

            with self.assertRaisesRegex(ValueError, "expected slideshow main content type"):
                stage_one(source, root / "staged")

    def test_stage_rejects_missing_presentation_part_for_byte_preserved_and_normalized_packages(self) -> None:
        for extension in (".pptx", ".potx"):
            with self.subTest(extension=extension), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                source = root / f"missing-part{extension}"
                write_ooxml_package(source, CONTENT_TYPES[extension], include_presentation=False)

                with self.assertRaisesRegex(ValueError, "missing ppt/presentation.xml"):
                    stage_one(source, root / "staged")

    def test_stage_rejects_non_presentationml_root_for_byte_preserved_and_normalized_packages(self) -> None:
        invalid_root = b"<p:presentation xmlns:p='urn:not-presentationml'/>"
        for extension in (".pptm", ".potm"):
            with self.subTest(extension=extension), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                source = root / f"wrong-root{extension}"
                write_ooxml_package(source, CONTENT_TYPES[extension], presentation_xml=invalid_root)

                with self.assertRaisesRegex(ValueError, "PresentationML presentation root"):
                    stage_one(source, root / "staged")

    def test_stage_rejects_invalid_content_types_namespace_and_duplicate_main_override(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "wrong-namespace.pptx"
            write_ooxml_package(
                source,
                CONTENT_TYPES[".pptx"],
                content_types_namespace="urn:not-opc-content-types",
            )
            with self.assertRaisesRegex(ValueError, "content-types namespace"):
                stage_one(source, root / "staged-namespace")

            duplicate = root / "duplicate.ppsm"
            write_ooxml_package(
                duplicate,
                CONTENT_TYPES[".ppsm"],
                duplicate_main_override=True,
            )
            with self.assertRaisesRegex(ValueError, "exactly one.*main content type"):
                stage_one(duplicate, root / "staged-duplicate")

    def test_text_sources_are_readable_by_real_lite_reader(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            project = Path(tmp) / "project"
            source = project / "sources" / "notes.text"
            source.parent.mkdir(parents=True)
            source.write_text("# Notes\nReadable downstream content.\n", encoding="utf-8")

            docs = ppt_runner.load_source_documents(project)

            self.assertEqual(len(docs), 1)
            self.assertEqual(docs[0]["path"], "sources/notes.text")
            self.assertIn("Readable downstream content", docs[0]["text"])


@unittest.skipUnless(
    os.environ.get("SLIDESMITH_RUN_REAL_SOURCE_SMOKES") == "1",
    "set SLIDESMITH_RUN_REAL_SOURCE_SMOKES=1 for external converter smokes",
)
class RealSourceConversionSmokeTests(unittest.TestCase):
    def test_all_six_ooxml_extensions_convert_and_analyze(self) -> None:
        from pptx import Presentation

        with tempfile.TemporaryDirectory() as tmp:
            workspace = Path(tmp) / "workspace"
            uploads = workspace / "uploads" / "all-six"
            uploads.mkdir(parents=True)
            base = Path(tmp) / "base.pptx"
            presentation = Presentation()
            slide = presentation.slides.add_slide(presentation.slide_layouts[1])
            slide.shapes.title.text = "Six format conversion smoke"
            slide.placeholders[1].text = "Readable by the real PPT Master converter."
            presentation.save(base)

            manifest_files = []
            for extension, content_type in CONTENT_TYPES.items():
                source = uploads / f"deck_{extension[1:]}{extension}"
                rewrite_ooxml_main_content_type(base, source, content_type)
                manifest_files.append(
                    {
                        "name": source.name,
                        "upload_path": source.relative_to(workspace).as_posix(),
                    }
                )
            write_source_manifest(workspace, manifest_files)

            completed = run_runner(
                workspace,
                "prepare",
                "--project",
                "all_six",
                "--profile",
                "smoke",
                "--sources-manifest",
                ".slidesmith/source_inputs.json",
            )
            self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
            project = only_project(workspace)
            self.assertTrue((project / "analysis" / "source_profile.json").is_file())
            for extension in CONTENT_TYPES:
                stem = f"deck_{extension[1:]}"
                archived = project / "sources" / f"{stem}{extension}"
                self.assertTrue(archived.is_file(), archived)
                self.assertTrue((project / "sources" / f"{stem}.md").is_file())
                self.assertTrue((project / "sources" / f"{stem}.conversion_profile.json").is_file())
                self.assertTrue((project / "analysis" / f"{stem}.identity.json").is_file())
                self.assertTrue((project / "analysis" / f"{stem}.slide_library.json").is_file())
                expected_type = (
                    CONTENT_TYPES[".pptm"]
                    if extension in {".pptm", ".ppsm", ".potm"}
                    else CONTENT_TYPES[".pptx"]
                )
                self.assertEqual(read_main_content_type(archived), expected_type)
                if extension in {".pptx", ".pptm"}:
                    self.assertEqual(archived.read_bytes(), (uploads / archived.name).read_bytes())

    def test_manifest_text_priority_and_legacy_input_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            manifest_workspace = root / "manifest-workspace"
            uploads = manifest_workspace / "uploads" / "manifest"
            uploads.mkdir(parents=True)
            selected = uploads / "notes.text"
            selected.write_text("# Text Source Signal\nselected-manifest-text\n", encoding="utf-8")
            fallback = uploads / "fallback.md"
            fallback.write_text("# Wrong source\nmust-not-be-selected\n", encoding="utf-8")
            write_source_manifest(
                manifest_workspace,
                [{"name": selected.name, "upload_path": selected.relative_to(manifest_workspace).as_posix()}],
            )

            manifest_run = run_runner(
                manifest_workspace,
                "prepare",
                "--project",
                "manifest_text",
                "--profile",
                "real-lite",
                "--sources-manifest",
                ".slidesmith/source_inputs.json",
                "--input",
                fallback.relative_to(manifest_workspace).as_posix(),
            )
            self.assertEqual(manifest_run.returncode, 0, manifest_run.stdout + manifest_run.stderr)
            manifest_project = only_project(manifest_workspace)
            self.assertTrue((manifest_project / "sources" / "notes.text").is_file())
            self.assertFalse((manifest_project / "sources" / "fallback.md").exists())
            recommendations = json.loads(
                (manifest_project / "confirm_ui" / "recommendations.json").read_text(encoding="utf-8")
            )
            self.assertIn("Text Source Signal", recommendations["content_divergence"]["value"])

            legacy_workspace = root / "legacy-workspace"
            legacy = legacy_workspace / "uploads" / "legacy" / "legacy.md"
            legacy.parent.mkdir(parents=True)
            legacy.write_text("# Legacy Signal\nselected-legacy-input\n", encoding="utf-8")
            legacy_run = run_runner(
                legacy_workspace,
                "prepare",
                "--project",
                "legacy_input",
                "--profile",
                "smoke",
                "--input",
                legacy.relative_to(legacy_workspace).as_posix(),
            )
            self.assertEqual(legacy_run.returncode, 0, legacy_run.stdout + legacy_run.stderr)
            legacy_project = only_project(legacy_workspace)
            self.assertEqual(
                (legacy_project / "sources" / "legacy.md").read_text(encoding="utf-8"),
                legacy.read_text(encoding="utf-8"),
            )


def stage_one(source: Path, destination: Path) -> Path:
    destination.mkdir(parents=True)
    args = argparse.Namespace(sources_manifest="", input=str(source))
    staged = ppt_runner.stage_prepare_inputs(args, destination)
    if len(staged) != 1:
        raise AssertionError(f"staged inputs = {staged!r}, want exactly one")
    return staged[0]


def write_ooxml_package(
    path: Path,
    main_content_type: str,
    *,
    include_presentation: bool = True,
    presentation_xml: bytes | None = None,
    content_types_namespace: str = CONTENT_TYPES_NAMESPACE,
    duplicate_main_override: bool = False,
) -> None:
    override = f'  <Override PartName="/ppt/presentation.xml" ContentType="{main_content_type}"/>\n'
    overrides = override * (2 if duplicate_main_override else 1)
    content_types = f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="{content_types_namespace}">
  <Default Extension="xml" ContentType="application/xml"/>
{overrides.rstrip()}
</Types>
'''.encode("utf-8")
    with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED) as package:
        package.writestr("[Content_Types].xml", content_types)
        if include_presentation:
            package.writestr(
                "ppt/presentation.xml",
                presentation_xml
                or f"<p:presentation xmlns:p='{PRESENTATIONML_NAMESPACE}'/>".encode("utf-8"),
            )
        package.writestr("ppt/marker.bin", b"preserve-me")


def rewrite_ooxml_main_content_type(source: Path, destination: Path, main_content_type: str) -> None:
    with zipfile.ZipFile(source) as source_package, zipfile.ZipFile(destination, "w") as destination_package:
        destination_package.comment = source_package.comment
        for entry in source_package.infolist():
            data = source_package.read(entry)
            if entry.filename == "[Content_Types].xml":
                original = CONTENT_TYPES[".pptx"].encode("utf-8")
                replacement = main_content_type.encode("utf-8")
                if data.count(original) != 1:
                    raise AssertionError("base PPTX does not contain exactly one presentation main content type")
                data = data.replace(original, replacement, 1)
            destination_package.writestr(entry, data)


def write_source_manifest(workspace: Path, files: list[dict[str, str]]) -> None:
    manifest = workspace / ".slidesmith" / "source_inputs.json"
    manifest.parent.mkdir(parents=True, exist_ok=True)
    manifest.write_text(
        json.dumps(
            {
                "schema": "slidesmith.source_inputs.v1",
                "task_id": "source-smoke",
                "files": files,
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )


def run_runner(workspace: Path, *args: str) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env["WORKSPACE"] = str(workspace)
    return subprocess.run(
        [sys.executable, str(RUNNER_PATH), *args],
        cwd=RUNNER_PATH.parent.parent,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=180,
    )


def only_project(workspace: Path) -> Path:
    projects = [path for path in (workspace / "projects").iterdir() if path.is_dir()]
    if len(projects) != 1:
        raise AssertionError(f"projects = {projects!r}, want exactly one")
    return projects[0]


def read_main_content_type(path: Path) -> str:
    raw = read_zip_entry(path, "[Content_Types].xml").decode("utf-8")
    marker = 'PartName="/ppt/presentation.xml" ContentType="'
    return raw.split(marker, 1)[1].split('"', 1)[0]


def read_zip_entry(path: Path, name: str) -> bytes:
    with zipfile.ZipFile(path) as package:
        return package.read(name)


if __name__ == "__main__":
    unittest.main()
