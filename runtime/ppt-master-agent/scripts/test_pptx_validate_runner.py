from __future__ import annotations

import json
import shutil
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import pptx_validate_runner
import beautify_runner
from quality_schema import sha256_file


class PPTXValidateRunnerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.project = Path(self.temp.name) / "project"
        self.project.mkdir()
        svg = self.project / "svg_output/01_test.svg"
        svg.parent.mkdir(parents=True)
        svg.write_text(
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1280 720"><rect width="1280" height="720" fill="#fff"/><text id="title" data-role="title" x="80" y="100">标题 Hello 123</text></svg>\n',
            encoding="utf-8",
        )
        self.write_json("analysis/svg_inventory.json", {
            "schema": "slidesmith.svg_inventory.v1",
            "pages": [{"page_id": "P01", "path": "svg_output/01_test.svg", "sha256": sha256_file(svg)}],
        })
        self.write_json("validation/quality_summary.json", {
            "schema": "slidesmith.quality_summary.v1", "task_id": "task-pptx", "decision": "pass",
            "svg_output_sha256": "b" * 64,
        })
        self.pptx = self.project / "exports/result.pptx"
        self.write_pptx(["标题 Hello 123"])
        self.write_export_contract()

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_json(self, relative: str, value: object) -> None:
        path = self.project / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, ensure_ascii=False) + "\n", encoding="utf-8")

    def write_pptx(self, pages: list[str], *, broken_target: bool = False) -> None:
        self.pptx.parent.mkdir(parents=True, exist_ok=True)
        with zipfile.ZipFile(self.pptx, "w", zipfile.ZIP_DEFLATED) as package:
            package.writestr("[Content_Types].xml", '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/></Types>')
            package.writestr("_rels/.rels", f'<Relationships xmlns="{pptx_validate_runner.REL_NS}"><Relationship Id="rId1" Type="officeDocument" Target="ppt/presentation.xml"/></Relationships>')
            slide_ids = "".join(f'<p:sldId id="{255 + index}" r:id="rId{index}"/>' for index in range(1, len(pages) + 1))
            package.writestr("ppt/presentation.xml", f'<p:presentation xmlns:p="{pptx_validate_runner.P_NS}" xmlns:r="{pptx_validate_runner.DOC_REL_NS}"><p:sldIdLst>{slide_ids}</p:sldIdLst></p:presentation>')
            relationships = []
            for index, text in enumerate(pages, start=1):
                target = "../missing.xml" if broken_target and index == 1 else f"slides/slide{index}.xml"
                relationships.append(f'<Relationship Id="rId{index}" Type="slide" Target="{target}"/>')
                package.writestr(
                    f"ppt/slides/slide{index}.xml",
                    f'<p:sld xmlns:p="{pptx_validate_runner.P_NS}" xmlns:a="{pptx_validate_runner.A_NS}"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>{text}</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>',
                )
            package.writestr("ppt/_rels/presentation.xml.rels", f'<Relationships xmlns="{pptx_validate_runner.REL_NS}">{"".join(relationships)}</Relationships>')

    def write_export_contract(self) -> None:
        self.write_json(".slidesmith/contracts/finalize_export.json", {
            "schema": "slidesmith.finalize_export_contract.v1",
            "task_id": "task-pptx",
            "expected_pages": 1,
            "notes_policy": "no-notes",
            "canonical_pptx": {"path": "exports/result.pptx", "sha256": sha256_file(self.pptx), "size": self.pptx.stat().st_size, "slide_count": 1},
        })

    def test_package_readback_cjk_and_contact_sheet(self) -> None:
        contract = pptx_validate_runner.run(self.project, ".slidesmith/contracts/finalize_export.json", "validate-1", render=False)
        self.assertEqual(contract["decision"], "pass")
        report = json.loads((self.project / "validation/pptx_validate_report.json").read_text(encoding="utf-8"))
        self.assertEqual(report["text_fidelity"]["deck_coverage"], 1.0)
        self.assertTrue((self.project / "validation/render/contact_sheet.png").is_file())
        self.assertIn("标题 Hello 123", (self.project / "validation/pptx_readback.md").read_text(encoding="utf-8"))

    def test_missing_page_text_is_error(self) -> None:
        self.write_pptx(["Only something else"])
        self.write_export_contract()
        contract = pptx_validate_runner.run(self.project, ".slidesmith/contracts/finalize_export.json", "validate-text", render=False)
        self.assertEqual(contract["decision"], "fail")
        report = json.loads((self.project / "validation/pptx_validate_report.json").read_text(encoding="utf-8"))
        self.assertIn("pptx.text_missing", {item["rule"] for item in report["findings"]})

    def test_missing_relationship_target_is_blocking(self) -> None:
        self.write_pptx(["标题 Hello 123"], broken_target=True)
        self.write_export_contract()
        with self.assertRaises(pptx_validate_runner.QualityError) as caught:
            pptx_validate_runner.run(self.project, ".slidesmith/contracts/finalize_export.json", "validate-broken", render=False)
        self.assertEqual(caught.exception.rule, "pptx.relationship_missing")

    def test_blank_render_detection(self) -> None:
        image = self.project / "blank.png"
        from PIL import Image
        Image.new("RGB", (640, 360), "#112233").save(image)
        blank, ratio = pptx_validate_runner.blank_image(image)
        self.assertTrue(blank)
        self.assertEqual(ratio, 1.0)

    def test_unicode_and_whitespace_normalization(self) -> None:
        self.assertEqual(pptx_validate_runner.normalize_text("Ａ  \r\n 中\u00a0文"), "A 中 文")

    def test_text_fidelity_ignores_layout_only_whitespace(self) -> None:
        units = [
            {
                "element_id": "body",
                "text": "生成页面结构 与基础视觉",
                "normalized": "生成页面结构 与基础视觉",
                "role": "body",
                "required_fidelity": "error",
            },
            {
                "element_id": "mixed",
                "text": "转换为 PPTX 发布形态",
                "normalized": "转换为 PPTX 发布形态",
                "role": "body",
                "required_fidelity": "error",
            },
            {
                "element_id": "summary",
                "text": "发布按顺序串联, 阻断级问题必须拦截。",
                "normalized": "发布按顺序串联, 阻断级问题必须拦截。",
                "role": "body",
                "required_fidelity": "error",
            },
        ]
        pages, findings, coverage = pptx_validate_runner.compare_text(
            [{"page_id": "P01", "units": units}],
            [["生成页面结构与基础视觉", "转换为 PPTX发布形态", "发布按顺序串联，阻断级问题必须拦截。"]],
        )
        self.assertEqual(findings, [])
        self.assertEqual(coverage, 1.0)
        self.assertEqual(
            pages[0]["pptx_aggregate"],
            "生成页面结构与基础视觉 转换为 PPTX发布形态 发布按顺序串联,阻断级问题必须拦截。",
        )

    def write_beautify_contracts(self) -> None:
        source = self.project / "sources/source.pptx"
        source.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(self.pptx, source)
        self.write_json(".slidesmith/contracts/beautify_inputs.json", {
            "schema": beautify_runner.INPUTS_SCHEMA,
            "task_id": "task-pptx",
            "source_pptx": {"path": "sources/source.pptx", "sha256": sha256_file(source), "size": source.stat().st_size},
        })
        self.write_json("analysis/beautify_inventory.json", {"schema": beautify_runner.INVENTORY_SCHEMA, "slides": []})
        self.write_json("analysis/beautify_plan.json", {"schema": "slidesmith.beautify_plan.v1", "pages": []})
        inputs_sha = sha256_file(self.project / ".slidesmith/contracts/beautify_inputs.json")
        inventory_sha = sha256_file(self.project / "analysis/beautify_inventory.json")
        plan_sha = sha256_file(self.project / "analysis/beautify_plan.json")
        self.write_json(".slidesmith/beautify_lock.json", {
            "schema": beautify_runner.LOCK_SCHEMA,
            "inputs_sha256": inputs_sha,
            "inventory_sha256": inventory_sha,
            "plan_sha256": plan_sha,
            "identity": {"source": "source"},
            "slides": [{"source_slide": 1, "output_page": 1, "text_blocks": [{"id": "title", "text": "标题 Hello 123"}]}],
        })

    def test_beautify_fidelity_is_atomically_promoted_and_contract_bound(self) -> None:
        self.write_beautify_contracts()
        contract = pptx_validate_runner.run(self.project, ".slidesmith/contracts/finalize_export.json", "validate-beautify", render=False)
        self.assertEqual(contract["decision"], "pass")
        self.assertEqual(contract["beautify_fidelity_decision"], "pass")
        self.assertEqual(contract["source_slide_count"], 1)
        report_path = self.project / "validation/beautify_fidelity_report.json"
        self.assertTrue(report_path.is_file())
        self.assertEqual(contract["beautify_fidelity_report_sha256"], sha256_file(report_path))
        report = json.loads((self.project / "validation/pptx_validate_report.json").read_text(encoding="utf-8"))
        self.assertEqual(report["beautify_fidelity"]["decision"], "pass")


if __name__ == "__main__":
    unittest.main()
