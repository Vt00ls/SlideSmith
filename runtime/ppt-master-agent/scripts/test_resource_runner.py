from __future__ import annotations

import base64
import json
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

import resource_runner


PNG_1X1 = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nGQAAAAASUVORK5CYII="
)


class ResourceRunnerTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.workspace = Path(self.temp.name) / "workspace"
        self.project = self.workspace / "projects" / "deck"
        self.skill = self.workspace / "skills" / "ppt-master"
        (self.project / ".slidesmith").mkdir(parents=True)
        (self.project / "sources").mkdir(parents=True)
        (self.skill / "scripts").mkdir(parents=True)
        (self.skill / "templates" / "charts").mkdir(parents=True)
        (self.project / "sources" / "input.md").write_text("# Metrics\nA=10 B=20\n", encoding="utf-8")
        (self.project / "confirm_ui").mkdir(parents=True)
        (self.project / "design_spec.md").write_text("# Design Spec\n", encoding="utf-8")
        (self.project / "spec_lock.md").write_text("# Spec Lock\n", encoding="utf-8")
        (self.project / "confirm_ui" / "result.json").write_text('{"page_count":3}\n', encoding="utf-8")
        self._write_fake_scripts()

    def _write_fake_scripts(self) -> None:
        scripts = self.skill / "scripts"
        (scripts / "analyze_images.py").write_text(
            """#!/usr/bin/env python3
import pathlib, sys
images = pathlib.Path(sys.argv[1])
analysis = images.parent / 'analysis'
analysis.mkdir(parents=True, exist_ok=True)
(analysis / 'image_analysis.csv').write_text('No,Filename,Width,Height\\n', encoding='utf-8')
""",
            encoding="utf-8",
        )
        (scripts / "icon_sync.py").write_text(
            """#!/usr/bin/env python3
import pathlib, sys
project = pathlib.Path(sys.argv[1])
for raw in sys.argv[2:]:
    lib, name = raw.split('/', 1) if '/' in raw else ('chunk-filled', raw)
    target = project / 'icons' / lib / f'{name}.svg'
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text('<svg viewBox="0 0 24 24"><path d="M1 1h2v2z"/></svg>\\n', encoding='utf-8')
""",
            encoding="utf-8",
        )
        (scripts / "latex_render.py").write_text(
            """#!/usr/bin/env python3
import base64, json, pathlib, sys
project = pathlib.Path(sys.argv[1])
manifest = pathlib.Path(sys.argv[sys.argv.index('--manifest') + 1])
png = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nGQAAAAASUVORK5CYII=')
for item in json.loads(manifest.read_text(encoding='utf-8'))['items']:
    (project / 'images' / item['filename']).write_bytes(png)
""",
            encoding="utf-8",
        )
        (scripts / "image_search.py").write_text(
            """#!/usr/bin/env python3
import base64, json, pathlib, sys
batch = pathlib.Path(sys.argv[sys.argv.index('--batch') + 1])
output = pathlib.Path(sys.argv[sys.argv.index('-o') + 1])
manifest = pathlib.Path(sys.argv[sys.argv.index('--manifest') + 1])
output.mkdir(parents=True, exist_ok=True)
png = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nGQAAAAASUVORK5CYII=')
items = json.loads(batch.read_text(encoding='utf-8'))['items']
for item in items:
    (output / item['filename']).write_bytes(png)
manifest.write_text(json.dumps({'items': [{'filename': item['filename'], 'provider': item.get('provider') or 'openverse', 'license': 'CC0'} for item in items]}), encoding='utf-8')
(output / 'web-called.marker').write_text('called', encoding='utf-8')
""",
            encoding="utf-8",
        )
        (scripts / "image_gen.py").write_text(
            """#!/usr/bin/env python3
import base64, json, pathlib, sys
path = pathlib.Path(sys.argv[sys.argv.index('--render-md') + 1] if '--render-md' in sys.argv else sys.argv[sys.argv.index('--manifest') + 1])
log = path.parent / 'image-gen-args.log'
with log.open('a', encoding='utf-8') as handle: handle.write(json.dumps(sys.argv[1:]) + '\\n')
if '--render-md' in sys.argv:
    path.with_suffix('.md').write_text('# Prompts\\n', encoding='utf-8')
else:
    output = pathlib.Path(sys.argv[sys.argv.index('-o') + 1])
    output.mkdir(parents=True, exist_ok=True)
    png = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nGQAAAAASUVORK5CYII=')
    for item in json.loads(path.read_text(encoding='utf-8'))['items']:
        (output / item['filename']).write_bytes(png)
""",
            encoding="utf-8",
        )
        (scripts / "slice_images.py").write_text(
            """#!/usr/bin/env python3
import base64, pathlib, sys
output = pathlib.Path(sys.argv[sys.argv.index('-o') + 1])
output.mkdir(parents=True, exist_ok=True)
names = sys.argv[sys.argv.index('--names') + 1].split(',') if '--names' in sys.argv else ['slice.png']
png = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nGQAAAAASUVORK5CYII=')
for name in names:
    target = output / name
    if not target.suffix: target = target.with_suffix('.png')
    target.write_bytes(png)
""",
            encoding="utf-8",
        )
        (self.skill / "templates" / "charts" / "charts_index.json").write_text(
            json.dumps({"charts": {"bar_chart": {"summary": "fixture"}}}), encoding="utf-8"
        )
        (self.skill / "templates" / "charts" / "bar_chart.svg").write_text(
            '<svg viewBox="0 0 1280 720"></svg>\n', encoding="utf-8"
        )

    def _policy(self, sources: list[str], **overrides: object) -> dict[str, object]:
        value: dict[str, object] = {
            "schema": resource_runner.POLICY_SCHEMA,
            "task_id": "task-resource",
            "route": "main",
            "runner_profile": "full-ppt-master",
            "phase_run_id": "phase-resource",
            "confirmation_sha256": resource_runner.sha256_file(self.project / "confirm_ui" / "result.json"),
            "policy_sha256": "",
            "phase_enabled": True,
            "network_enabled": False,
            "web_image_enabled": False,
            "ai_image_enabled": False,
            "formula_network_enabled": False,
            "confirmation_image_sources": sources,
            "icon_library": "tabler-outline",
            "formula_policy": "render-all",
            "image_ai_path": "api",
            "allowed_ai_paths": ["api"],
            "allowed_web_providers": ["openverse", "wikimedia"],
            "allowed_ai_providers": ["openai"],
            "max_files": 100,
            "max_total_bytes": 10_000_000,
            "max_single_bytes": 1_000_000,
            "timeout_seconds": 30,
        }
        value.update(overrides)
        return value

    def _write_contract_inputs(self, requirements: list[dict[str, object]], sources: list[str], **policy: object) -> None:
        plan = {
            "schema": resource_runner.PLAN_SCHEMA,
            "task_id": "task-resource",
            "page_count": 3,
            "spec_sha256": resource_runner.sha256_file(self.project / "design_spec.md"),
            "spec_lock_sha256": resource_runner.sha256_file(self.project / "spec_lock.md"),
            "confirmation_sha256": resource_runner.sha256_file(self.project / "confirm_ui" / "result.json"),
            "requirements": requirements,
        }
        (self.project / ".slidesmith" / "resource_plan.json").write_text(json.dumps(plan), encoding="utf-8")
        policy_value = self._policy(sources, **policy)
        policy_value["policy_sha256"] = resource_runner.policy_digest(policy_value)
        (self.project / ".slidesmith" / "resource_policy.json").write_text(json.dumps(policy_value), encoding="utf-8")

    def _run(self) -> dict[str, object]:
        result = resource_runner.run(self.project, self.skill, "phase-resource")
        self.assertEqual(result, 0)
        leftovers = list((self.project / ".slidesmith").glob("*.tmp"))
        self.assertEqual(leftovers, [])
        return json.loads((self.project / ".slidesmith" / "resources_manifest.json").read_text(encoding="utf-8"))

    def test_offline_fixtures(self) -> None:
        fixture_root = SCRIPT_DIR.parent / "fixtures"
        for name in ("resource-none", "resource-user-images", "resource-icons-chart", "resource-offline-degrade"):
            with self.subTest(name=name):
                fixture = json.loads((fixture_root / name / "fixture.json").read_text(encoding="utf-8"))
                if name == "resource-user-images":
                    (self.project / "sources" / "user.png").write_bytes(PNG_1X1)
                self._write_contract_inputs(fixture["requirements"], fixture["confirmation_sources"])
                manifest = self._run()
                self.assertEqual(manifest["summary"]["total"], len(fixture["requirements"]))
                if name == "resource-offline-degrade":
                    self.assertEqual(manifest["resources"][0]["status"], "degraded")
                    self.assertFalse((self.project / "images" / "web-called.marker").exists())
                if name == "resource-user-images":
                    self.assertEqual(manifest["resources"][0]["status"], "ready")
                if name == "resource-icons-chart":
                    self.assertTrue(all(item["status"] == "ready" for item in manifest["resources"]))

    def test_offline_formula_uses_approved_text_fallback(self) -> None:
        requirement = {
            "id": "res-formula", "page": 1, "type": "formula", "purpose": "equation",
            "required": True, "acquire_via": "formula", "fallback": "text",
            "output_name": "formula.png", "expression": "E=mc^2",
        }
        self._write_contract_inputs([requirement], ["none"])
        manifest = self._run()
        self.assertEqual(manifest["resources"][0]["status"], "degraded")
        self.assertEqual(manifest["resources"][0]["fallback"]["type"], "text")

    def test_template_asset_copies_only_contained_concrete_file(self) -> None:
        template_root = self.workspace / "templates" / "brand"
        (template_root / "assets").mkdir(parents=True)
        (template_root / "assets" / "brand_mark.svg").write_text(
            '<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0h1v1z"/></svg>', encoding="utf-8"
        )
        (self.workspace / ".slidesmith").mkdir(exist_ok=True)
        (self.workspace / ".slidesmith" / "template_resolution.json").write_text(
            json.dumps({"template_root": "templates/brand"}), encoding="utf-8"
        )
        requirement = {
            "id": "template_asset.p02.brand_mark", "page": 2, "type": "template_asset",
            "purpose": "Brand mark used on P02", "required": True, "acquire_via": "template",
            "fallback": "shape", "output_name": "p02_brand_mark.svg",
            "source_reference": "assets/brand_mark.svg", "publishable": True,
        }
        self._write_contract_inputs([requirement], ["provided"])
        manifest = self._run()
        item = manifest["resources"][0]
        self.assertEqual(item["status"], "ready")
        self.assertEqual(item["output"]["path"], "images/p02_brand_mark.svg")
        self.assertEqual(item["output"]["sha256"], resource_runner.sha256_file(template_root / "assets" / "brand_mark.svg"))

    def test_beautify_source_image_copies_to_acquired_namespace(self) -> None:
        (self.project / "images").mkdir(exist_ok=True)
        (self.project / "images" / "source_logo.png").write_bytes(PNG_1X1)
        requirement = {
            "id": "image.p01.source_logo", "page": 1, "type": "image", "purpose": "Frozen source logo",
            "required": True, "acquire_via": "source", "fallback": "", "output_name": "p01_source_logo.png",
            "source_reference": "images/source_logo.png", "publishable": True,
        }
        self._write_contract_inputs([requirement], ["provided"])
        manifest = self._run()
        item = manifest["resources"][0]
        self.assertEqual(item["status"], "ready")
        self.assertEqual(item["output"]["path"], "images/acquired/p01_source_logo.png")
        self.assertEqual((self.project / "images" / "source_logo.png").read_bytes(), PNG_1X1)

    def test_beautify_source_image_cache_is_invalidated_by_source_mutation(self) -> None:
        (self.project / "images").mkdir(exist_ok=True)
        source = self.project / "images" / "source_logo.png"
        source.write_bytes(PNG_1X1)
        requirement = {
            "id": "image.p01.source_logo", "page": 1, "type": "image", "purpose": "Frozen source logo",
            "required": True, "acquire_via": "source", "fallback": "", "output_name": "p01_source_logo.png",
            "source_reference": "images/source_logo.png", "publishable": True,
        }
        self._write_contract_inputs([requirement], ["provided"])
        first = self._run()["resources"][0]
        second = self._run()["resources"][0]
        self.assertTrue(second["cache_reused"])
        source.write_bytes(PNG_1X1 + b"source-revision")
        third = self._run()["resources"][0]
        self.assertFalse(third.get("cache_reused", False))
        self.assertEqual(third["attempt"], first["attempt"] + 1)
        self.assertNotEqual(third["output"]["sha256"], first["output"]["sha256"])

    def test_ai_uses_manifest_mode_and_slice_parent(self) -> None:
        requirements = [
            {
                "id": "res-sheet", "page": 1, "type": "illustration_sheet", "purpose": "spot sheet",
                "required": True, "acquire_via": "ai", "fallback": "", "output_name": "sheet.png",
                "prompt_or_query": "three matching secure workflow illustrations", "provider": "openai",
            },
            {
                "id": "res-slice", "page": 1, "type": "illustration_slice", "purpose": "spot",
                "required": True, "acquire_via": "slice", "fallback": "", "output_name": "slice.png",
                "parent_id": "res-sheet", "parameters": {"grid": "1x1", "names": ["slice.png"]},
            },
        ]
        self._write_contract_inputs(
            requirements, ["ai"], network_enabled=True, ai_image_enabled=True
        )
        manifest = self._run()
        self.assertTrue(all(item["status"] == "ready" for item in manifest["resources"]))
        calls = [json.loads(line) for line in (self.project / "images" / "image-gen-args.log").read_text().splitlines()]
        self.assertTrue(any("--render-md" in call for call in calls))
        self.assertTrue(any("--manifest" in call for call in calls))
        self.assertTrue(all(not call or call[0].startswith("--") for call in calls))

    def test_ready_cache_is_reused_and_hash_change_becomes_stale(self) -> None:
        (self.project / "sources" / "user.png").write_bytes(PNG_1X1)
        requirement = {
            "id": "res-user", "page": 1, "type": "image", "purpose": "hero",
            "required": True, "acquire_via": "user", "fallback": "", "output_name": "hero.png",
            "source_reference": "sources/user.png",
        }
        self._write_contract_inputs([requirement], ["provided"])
        first = self._run()["resources"][0]
        second = self._run()["resources"][0]
        self.assertTrue(second["cache_reused"])
        self.assertEqual(second["attempt"], first["attempt"])
        (self.project / "images" / "hero.png").write_bytes(b"changed")
        third = self._run()["resources"][0]
        self.assertFalse(third.get("cache_reused", False))
        self.assertEqual(third["attempt"], first["attempt"] + 1)
        self.assertEqual(third["status"], "ready")

        (self.project / "sources" / "user.png").write_bytes(PNG_1X1 + b"source-revision")
        fourth = self._run()["resources"][0]
        self.assertFalse(fourth.get("cache_reused", False))
        self.assertEqual(fourth["attempt"], third["attempt"] + 1)
        self.assertNotEqual(fourth["output"]["sha256"], third["output"]["sha256"])

    def test_web_provider_runs_only_when_all_gates_allow(self) -> None:
        requirement = {
            "id": "res-web", "page": 1, "type": "image", "purpose": "evidence",
            "required": True, "acquire_via": "web", "fallback": "", "output_name": "web.png",
            "prompt_or_query": "secure workflow", "provider": "openverse",
        }
        self._write_contract_inputs(
            [requirement], ["web"], network_enabled=True, web_image_enabled=True
        )
        manifest = self._run()
        self.assertEqual(manifest["resources"][0]["status"], "ready")
        self.assertTrue((self.project / "images" / "web-called.marker").exists())

    def test_ai_empty_allowlist_fails_without_provider_call(self) -> None:
        requirement = {
            "id": "res-ai", "page": 1, "type": "image", "purpose": "hero",
            "required": True, "acquire_via": "ai", "fallback": "", "output_name": "ai.png",
            "prompt_or_query": "confidential launch prompt",
        }
        self._write_contract_inputs(
            [requirement], ["ai"], network_enabled=True, ai_image_enabled=True,
            allowed_ai_providers=[],
        )
        result = resource_runner.run(self.project, self.skill, "phase-resource")
        self.assertEqual(result, 1)
        self.assertFalse((self.project / "images" / "image-gen-args.log").exists())
        manifest = json.loads((self.project / ".slidesmith" / "resources_manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(manifest["resources"][0]["status"], "failed")
        self.assertEqual(manifest["resources"][0]["error"]["code"], "provider_not_configured")

    def test_user_copy_rejects_symlink_target_before_writing(self) -> None:
        (self.project / "sources" / "user.png").write_bytes(PNG_1X1)
        outside = Path(self.temp.name) / "outside.png"
        outside.write_bytes(b"outside-safe")
        (self.project / "images").mkdir(parents=True)
        (self.project / "images" / "hero.png").symlink_to(outside)
        requirement = {
            "id": "res-user", "page": 1, "type": "image", "purpose": "hero",
            "required": True, "acquire_via": "user", "fallback": "", "output_name": "hero.png",
            "source_reference": "sources/user.png",
        }
        self._write_contract_inputs([requirement], ["provided"])
        with self.assertRaisesRegex(RuntimeError, "symlink in project images"):
            resource_runner.run(self.project, self.skill, "phase-resource")
        self.assertEqual(outside.read_bytes(), b"outside-safe")

    def test_provider_stderr_is_not_persisted_in_manifest(self) -> None:
        (self.skill / "scripts" / "image_gen.py").write_text(
            "import sys\nprint('Authorization: Bearer secret-provider-token', file=sys.stderr)\nraise SystemExit(3)\n",
            encoding="utf-8",
        )
        requirement = {
            "id": "res-ai", "page": 1, "type": "image", "purpose": "hero",
            "required": False, "acquire_via": "ai", "fallback": "diagram", "output_name": "ai.png",
            "prompt_or_query": "confidential launch prompt", "provider": "openai",
        }
        self._write_contract_inputs(
            [requirement], ["ai"], network_enabled=True, ai_image_enabled=True,
        )
        manifest = self._run()
        encoded = json.dumps(manifest)
        self.assertNotIn("secret-provider-token", encoded)
        self.assertNotIn("confidential launch prompt", encoded)
        self.assertEqual(manifest["resources"][0]["fallback"]["reason"], "image_gen.py_failed_3")

    def test_spec_revision_removes_orphan_outputs_and_intermediates(self) -> None:
        (self.project / "sources" / "user.png").write_bytes(PNG_1X1)
        requirement = {
            "id": "res-user", "page": 1, "type": "image", "purpose": "hero",
            "required": True, "acquire_via": "user", "fallback": "", "output_name": "hero.png",
            "source_reference": "sources/user.png",
        }
        self._write_contract_inputs([requirement], ["provided"])
        first = self._run()
        self.assertEqual(first["resources"][0]["status"], "ready")
        self.assertTrue((self.project / "images" / "hero.png").exists())
        (self.project / "images" / "image_prompts.json").write_text("{}", encoding="utf-8")
        (self.project / "images" / "image_prompts.md").write_text("# stale", encoding="utf-8")

        self._write_contract_inputs([], ["provided"])
        revised = self._run()
        self.assertEqual(revised["resources"], [])
        self.assertFalse((self.project / "images" / "hero.png").exists())
        self.assertFalse((self.project / "images" / "image_prompts.json").exists())
        self.assertFalse((self.project / "images" / "image_prompts.md").exists())


if __name__ == "__main__":
    unittest.main()
