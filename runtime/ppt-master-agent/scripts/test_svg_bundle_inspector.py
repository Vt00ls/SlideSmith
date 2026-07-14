from __future__ import annotations

import json
import io
import sys
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
FIXTURE_DIR = SCRIPT_DIR.parent / "fixtures"
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

import svg_bundle_inspector as inspector


class SVGBundleInspectorTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.project = Path(self.temp.name) / "project"
        for relative in (".slidesmith", "confirm_ui", "svg_output", "notes", "analysis", "images"):
            (self.project / relative).mkdir(parents=True, exist_ok=True)
        (self.project / "confirm_ui" / "result.json").write_text(
            json.dumps({"canvas": "ppt169", "page_count": 2}), encoding="utf-8"
        )
        (self.project / "design_spec.md").write_text("# Design Spec\n\nP01\nP02\n", encoding="utf-8")
        (self.project / "spec_lock.md").write_text(
            "# Spec Lock\n\ncanvas: ppt169\nviewBox: 0 0 1280 720\n", encoding="utf-8"
        )
        self.write_manifest([])
        self.write_svg(1, "封面")
        self.write_svg(2, "overview")
        (self.project / "notes" / "total.md").write_text(
            "## P01 | Cover\n\nCover notes.\n\n## P02 | Overview\n\nOverview notes.\n",
            encoding="utf-8",
        )
        self.write_empty_sidecars()

    def write_svg(
        self,
        page: int,
        slug: str,
        *,
        body: str = '<text id="page-title">Title</text><text id="page-number">1</text>',
        root_attributes: str = "",
        namespace: str = inspector.SVG_NS,
        filename: str | None = None,
        page_id: str | None = None,
        view_box: str = "0 0 1280 720",
        width: str = "1280",
        height: str = "720px",
    ) -> Path:
        target = self.project / "svg_output" / (filename or f"{page:02d}_{slug}.svg")
        actual_page_id = page_id or f"P{page:02d}"
        target.write_text(
            f'<svg xmlns="{namespace}" width="{width}" height="{height}" viewBox="{view_box}" '
            f'data-page-id="{actual_page_id}" data-spec-page-id="{actual_page_id}" {root_attributes}>{body}</svg>\n',
            encoding="utf-8",
        )
        return target

    def reset_svgs(self) -> None:
        for path in (self.project / "svg_output").iterdir():
            path.unlink()

    def write_empty_sidecars(self) -> None:
        manifest_sha = inspector.sha256_file(self.project / ".slidesmith" / "resources_manifest.json")
        pages = []
        for path in sorted((self.project / "svg_output").glob("*.svg")):
            match = inspector.FILENAME_PATTERN.fullmatch(path.name)
            if not match:
                continue
            page = int(match.group("page"))
            pages.append(
                {
                    "page_id": f"P{page:02d}",
                    "svg": path.relative_to(self.project).as_posix(),
                    "svg_sha256": inspector.sha256_file(path),
                    "resources": [],
                }
            )
        (self.project / "analysis" / "svg_resource_usage.json").write_text(
            json.dumps(
                {
                    "schema": inspector.RESOURCE_USAGE_SCHEMA,
                    "resources_manifest_sha256": manifest_sha,
                    "pages": pages,
                }
            ),
            encoding="utf-8",
        )
        (self.project / "analysis" / "chart_usage.json").write_text(
            json.dumps(
                {
                    "schema": inspector.CHART_USAGE_SCHEMA,
                    "resources_manifest_sha256": manifest_sha,
                    "charts": [],
                }
            ),
            encoding="utf-8",
        )

    def write_manifest(self, resources: list[dict[str, object]]) -> None:
        (self.project / ".slidesmith" / "resources_manifest.json").write_text(
            json.dumps(
                {
                    "schema": inspector.MANIFEST_SCHEMA,
                    "task_id": "task-svg",
                    "runner_profile": "full-ppt-master",
                    "resources": resources,
                }
            ),
            encoding="utf-8",
        )

    def write_resource_rows(self, rows_by_page: dict[str, list[dict[str, object]]]) -> None:
        self.write_empty_sidecars()
        path = self.project / "analysis" / "svg_resource_usage.json"
        value = json.loads(path.read_text(encoding="utf-8"))
        for page in value["pages"]:
            page["resources"] = rows_by_page.get(page["page_id"], [])
        path.write_text(json.dumps(value), encoding="utf-8")

    def assert_contract_error(self, code: str) -> None:
        with self.assertRaises(inspector.ContractError) as caught:
            inspector.inspect_bundle(self.project)
        self.assertEqual(caught.exception.code, code)

    def test_valid_bundle_generates_live_hash_bound_inventory_atomically(self) -> None:
        inventory = inspector.inspect_bundle(self.project)
        self.assertEqual(inventory["schema"], inspector.INVENTORY_SCHEMA)
        self.assertEqual(inventory["canvas"], "ppt169")
        self.assertEqual([page["page_id"] for page in inventory["pages"]], ["P01", "P02"])
        self.assertEqual(inventory["pages"][0]["path"], "svg_output/01_封面.svg")
        self.assertEqual(
            inventory["pages"][0]["sha256"],
            inspector.sha256_file(self.project / "svg_output" / "01_封面.svg"),
        )
        output = self.project / "analysis" / "svg_inventory.json"
        self.assertTrue(output.is_file())
        self.assertEqual(json.loads(output.read_text(encoding="utf-8"))["summary"]["pages"], 2)
        self.assertEqual(list((self.project / "analysis").glob("*.tmp")), [])

    def test_filename_page_and_canvas_contracts_fail_closed(self) -> None:
        cases = [
            ("filename_invalid", lambda: self.write_svg(2, "bad name", filename="02_bad name.svg")),
            ("page_sequence_invalid", lambda: self.write_svg(3, "overview", filename="03_overview.svg")),
            ("page_mapping_invalid", lambda: self.write_svg(
                2,
                "overview",
                page_id="P01",
            )),
            ("canvas_mismatch", lambda: self.write_svg(
                2,
                "overview",
                view_box="0 0 1024 768",
            )),
            ("canvas_mismatch", lambda: self.write_svg(2, "overview", width="1024")),
        ]
        for expected_code, mutate in cases:
            with self.subTest(expected_code=expected_code):
                self.reset_svgs()
                self.write_svg(1, "cover")
                mutate()
                self.assert_contract_error(expected_code)

    def test_spec_lock_canvas_must_match_confirmation(self) -> None:
        (self.project / "spec_lock.md").write_text("canvas: ppt43\n", encoding="utf-8")
        self.assert_contract_error("canvas_mismatch")

    def test_design_spec_page_ids_must_match_the_confirmed_bundle(self) -> None:
        (self.project / "design_spec.md").write_text("# Design Spec\n\nP01 Cover\nP03 Wrong\n", encoding="utf-8")
        self.assert_contract_error("page_mapping_invalid")

    def test_design_spec_page_references_do_not_override_ordered_page_headings(self) -> None:
        (self.project / "design_spec.md").write_text(
            "# Design Spec\n\n"
            "P02 chart data must retain its source citation.\n\n"
            "#### P01 - Cover\n\n"
            "#### P02 - Evidence\n",
            encoding="utf-8",
        )

        inventory = inspector.inspect_bundle(self.project)

        self.assertEqual([page["page_id"] for page in inventory["pages"]], ["P01", "P02"])

    def test_xml_and_static_security_rejections(self) -> None:
        cases = [
            ("xml_invalid", "<g>"),
            ("doctype_forbidden", '<!DOCTYPE svg><svg xmlns="http://www.w3.org/2000/svg"/>'),
            ("element_forbidden", "<script/>"),
            ("element_forbidden", "<foreignObject/>"),
            ("element_forbidden", "<iframe/>"),
            ("element_forbidden", "<object/>"),
            ("element_forbidden", "<embed/>"),
            ("event_handler_forbidden", '<g id="safe" onclick="run()"/>'),
            ("external_uri", '<image id="hero" href="https://example.com/a.png"/>'),
            ("external_uri", '<image id="hero" href="javascript:alert(1)"/>'),
            ("external_uri", '<image id="hero" href="data:image/png;base64,AAAA"/>'),
            ("external_uri", '<image id="hero" href="file:///tmp/a.png"/>'),
            ("external_uri", '<rect id="box" style="fill:url(https://example.com/a.svg)"/>'),
            ("element_id_duplicate", '<g id="dup"/><g id="dup"/>'),
        ]
        for expected_code, body in cases:
            with self.subTest(expected_code=expected_code, body=body):
                self.reset_svgs()
                self.write_svg(1, "cover", body=body)
                self.write_svg(2, "overview")
                self.assert_contract_error(expected_code)

    def test_root_namespace_is_required(self) -> None:
        self.reset_svgs()
        self.write_svg(1, "cover", namespace="")
        self.write_svg(2, "overview")
        self.assert_contract_error("root_invalid")

    def test_project_local_reference_must_be_manifest_bound_and_escape_is_rejected(self) -> None:
        (self.project / "images" / "hero.png").write_bytes(b"PNG")
        self.reset_svgs()
        self.write_svg(1, "cover", body='<image id="hero" href="../images/hero.png"/>')
        self.write_svg(2, "overview")
        self.write_empty_sidecars()
        self.assert_contract_error("resource_usage_invalid")

        hero = self.project / "images" / "hero.png"
        self.write_manifest(
            [
                {
                    "id": "res-hero",
                    "page": 1,
                    "type": "image",
                    "required": True,
                    "status": "ready",
                    "output": {"path": "images/hero.png", "sha256": inspector.sha256_file(hero)},
                }
            ]
        )
        self.write_svg(
            1,
            "cover",
            body='<image id="hero" data-resource-id="res-hero" href="../images/hero.png"/>',
        )
        self.write_resource_rows(
            {
                "P01": [
                    {
                        "resource_id": "res-hero",
                        "element_id": "hero",
                        "usage": "image",
                        "href": "../images/hero.png",
                        "fallback": "",
                    }
                ]
            }
        )
        inventory = inspector.inspect_bundle(self.project)
        self.assertEqual(inventory["summary"]["images"], 1)

        outside = self.project.parent / "outside.png"
        outside.write_bytes(b"outside")
        self.write_svg(1, "cover", body='<image id="hero" href="../../outside.png"/>')
        self.assert_contract_error("path_escape")

    def test_omit_optional_is_explicit_without_fake_svg_owner(self) -> None:
        self.write_manifest(
            [
                {
                    "id": "res-omitted",
                    "page": 1,
                    "type": "image",
                    "required": False,
                    "status": "degraded",
                    "fallback": {"type": "omit_optional", "reason": "not_needed"},
                }
            ]
        )
        self.write_resource_rows(
            {
                "P01": [
                    {
                        "resource_id": "res-omitted",
                        "element_id": "",
                        "usage": "omit_optional",
                        "href": "",
                        "fallback": "omit_optional",
                    }
                ]
            }
        )
        inventory = inspector.inspect_bundle(self.project)
        self.assertEqual(inventory["resource_summary"], {"bindings": 1, "resources": 1})

    def test_resource_usage_requires_real_owner_href_and_degraded_fallback(self) -> None:
        image = self.project / "images" / "hero.png"
        image.write_bytes(b"PNG")
        resources: list[dict[str, object]] = [
            {
                "id": "res-ready",
                "page": 1,
                "type": "image",
                "required": True,
                "status": "ready",
                "output": {
                    "path": "images/hero.png",
                    "sha256": inspector.sha256_file(image),
                    "size": image.stat().st_size,
                },
            },
            {
                "id": "res-fallback",
                "page": 2,
                "type": "image",
                "required": False,
                "status": "degraded",
                "fallback": {"type": "diagram", "reason": "policy_denied"},
            },
        ]
        self.write_manifest(resources)
        self.reset_svgs()
        self.write_svg(
            1,
            "cover",
            body='<image id="hero-image" data-resource-id="res-ready" href="../images/hero.png"/>',
        )
        self.write_svg(
            2,
            "overview",
            body='<g id="fallback-diagram" data-resource-id="res-fallback"><rect id="fallback-box"/></g>',
        )
        rows = {
            "P01": [
                {
                    "resource_id": "res-ready",
                    "element_id": "hero-image",
                    "usage": "image",
                    "href": "../images/hero.png",
                    "fallback": "",
                }
            ],
            "P02": [
                {
                    "resource_id": "res-fallback",
                    "element_id": "fallback-diagram",
                    "usage": "diagram",
                    "href": "",
                    "fallback": "diagram",
                }
            ],
        }
        self.write_resource_rows(rows)
        inventory = inspector.inspect_bundle(self.project)
        self.assertEqual(inventory["resource_summary"], {"bindings": 2, "resources": 2})

        self.write_svg(1, "cover", body='<text id="hero-image">res-ready</text>')
        self.write_resource_rows(rows)
        self.assert_contract_error("resource_usage_invalid")

    def test_chart_usage_binds_live_owner_resources_data_and_source(self) -> None:
        for relative in ("charts/data", "charts/templates", "sources"):
            (self.project / relative).mkdir(parents=True, exist_ok=True)
        data = self.project / "charts" / "data" / "revenue.json"
        data.write_text(
            json.dumps(
                {
                    "schema": "slidesmith.chart_data.v1",
                    "resource_id": "res-chart-data",
                    "data": {
                        "categories": ["Q1", "Q2"],
                        "series": [{"name": "Revenue", "values": [10, 20]}],
                    },
                    "citation": {"source_file": "sources/metrics.md", "section": "Revenue"},
                }
            ),
            encoding="utf-8",
        )
        template = self.project / "charts" / "templates" / "bar.svg"
        template.write_text('<svg xmlns="http://www.w3.org/2000/svg"/>', encoding="utf-8")
        (self.project / "sources" / "metrics.md").write_text("# Revenue\nQ1 10 Q2 20\n", encoding="utf-8")
        self.write_manifest(
            [
                {
                    "id": "res-chart-data",
                    "type": "chart_data",
                    "required": True,
                    "status": "ready",
                    "output": {"path": "charts/data/revenue.json", "sha256": inspector.sha256_file(data)},
                },
                {
                    "id": "res-chart-template",
                    "type": "chart_template",
                    "required": True,
                    "status": "ready",
                    "output": {"path": "charts/templates/bar.svg", "sha256": inspector.sha256_file(template)},
                },
            ]
        )
        self.reset_svgs()
        self.write_svg(1, "cover")
        self.write_svg(
            2,
            "chart",
            body=(
                '<g id="chart-revenue" data-chart-id="chart-revenue" '
                'data-chart-data-resource-id="res-chart-data" '
                'data-chart-template-resource-id="res-chart-template"><rect id="bar-q1"/></g>'
            ),
        )
        self.write_empty_sidecars()
        chart_path = self.project / "analysis" / "chart_usage.json"
        chart_value = json.loads(chart_path.read_text(encoding="utf-8"))
        chart_value["charts"] = [
            {
                "chart_id": "chart-revenue",
                "page_id": "P02",
                "svg": "svg_output/02_chart.svg",
                "element_id": "chart-revenue",
                "chart_type": "bar",
                "verification_mode": "direct-calc",
                "template_resource_id": "res-chart-template",
                "data_resource_id": "res-chart-data",
                "data_sha256": inspector.sha256_file(data),
                "source_citation": {"file": "sources/metrics.md", "section": "Revenue"},
                "plot_area": [100, 120, 1180, 650],
                "series": ["Revenue"],
                "categories": ["Q1", "Q2"],
            }
        ]
        chart_path.write_text(json.dumps(chart_value), encoding="utf-8")
        inventory = inspector.inspect_bundle(self.project)
        self.assertEqual(inventory["chart_summary"], {"charts": 1})

        chart_value["charts"][0]["data_sha256"] = "0" * 64
        chart_path.write_text(json.dumps(chart_value), encoding="utf-8")
        self.assert_contract_error("chart_usage_invalid")

        self.write_svg(2, "chart")
        self.write_empty_sidecars()
        self.assert_contract_error("chart_usage_invalid")

    def test_chart_ids_are_unique_across_the_bundle(self) -> None:
        self.reset_svgs()
        chart = '<g id="chart-owner" data-chart-id="duplicate-chart"/>'
        self.write_svg(1, "cover", body=chart)
        self.write_svg(2, "overview", body=chart)
        self.write_empty_sidecars()
        self.assert_contract_error("chart_usage_invalid")

    def test_notes_require_exact_sections_and_reject_placeholders(self) -> None:
        notes = self.project / "notes" / "total.md"
        notes.write_text("## P01 | Cover\n\nOnly one section.\n", encoding="utf-8")
        self.assert_contract_error("notes_invalid")

        notes.write_text(
            "## P01 | Cover\n\nTODO\n\n## P02 | Overview\n\nUseful notes.\n",
            encoding="utf-8",
        )
        self.assert_contract_error("notes_invalid")

        notes.write_text(
            "## P01 | Cover\n\n\n## P02 | Overview\n\nUseful notes.\n",
            encoding="utf-8",
        )
        inventory = inspector.inspect_bundle(self.project)
        notes_inventory = json.loads(
            (self.project / "analysis" / "notes_inventory.json").read_text(encoding="utf-8")
        )
        self.assertEqual(inventory["notes_sha256"], notes_inventory["notes_sha256"])
        self.assertTrue(notes_inventory["pages"][0]["empty"])

    def test_cli_paths_are_project_relative_and_failures_are_machine_readable(self) -> None:
        with self.assertRaises(inspector.ContractError) as caught:
            inspector.inspect_bundle(
                self.project,
                resources_manifest=self.project / ".slidesmith" / "resources_manifest.json",
            )
        self.assertEqual(caught.exception.code, "path_invalid")

        stderr = io.StringIO()
        with redirect_stderr(stderr):
            exit_code = inspector.main([str(self.project), "--notes", "../notes.md"])
        self.assertEqual(exit_code, 1)
        failure = json.loads(stderr.getvalue())
        self.assertEqual(failure["schema"], "slidesmith.svg_bundle_inspection_error.v1")
        self.assertEqual(failure["code"], "path_invalid")

    def test_fixed_fixture_catalog_covers_basic_resources_degraded_chart_and_security(self) -> None:
        expected = {
            "svg-basic": "pass",
            "svg-resources": "pass",
            "svg-degraded": "pass",
            "svg-chart": "pass",
            "svg-security-negative": "fail",
        }
        for name, result in expected.items():
            with self.subTest(name=name):
                descriptor = json.loads((FIXTURE_DIR / name / "fixture.json").read_text(encoding="utf-8"))
                self.assertEqual(descriptor["schema"], "slidesmith.svg_fixture.v1")
                self.assertEqual(descriptor["name"], name)
                self.assertEqual(descriptor["expected"], result)
                self.assertEqual(descriptor["canvas"], "ppt169")
                self.assertEqual(descriptor["page_count"], 3)
        resources = json.loads((FIXTURE_DIR / "svg-resources" / "fixture.json").read_text(encoding="utf-8"))
        self.assertEqual({item["type"] for item in resources["resources"]}, {"image", "icon", "formula"})
        degraded = json.loads((FIXTURE_DIR / "svg-degraded" / "fixture.json").read_text(encoding="utf-8"))
        self.assertEqual(
            {item["fallback"] for item in degraded["resources"]},
            {"diagram", "text", "omit_optional"},
        )
        chart = json.loads((FIXTURE_DIR / "svg-chart" / "fixture.json").read_text(encoding="utf-8"))
        self.assertEqual(chart["charts"][0]["verification_mode"], "direct-calc")
        security = json.loads(
            (FIXTURE_DIR / "svg-security-negative" / "fixture.json").read_text(encoding="utf-8")
        )
        for required in ("doctype", "script", "event-handler", "external-uri", "path-escape", "duplicate-id"):
            self.assertIn(required, security["cases"])


if __name__ == "__main__":
    unittest.main()
