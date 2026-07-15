from __future__ import annotations

import json
import shutil
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import beautify_runner
from quality_schema import QualityError, sha256_file


class BeautifyRunnerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.project = Path(self.temp.name) / "project"
        for relative in ("sources", "analysis", "images", ".slidesmith/contracts"):
            (self.project / relative).mkdir(parents=True, exist_ok=True)
        self.source = self.project / "sources/source.pptx"
        self.write_pptx(self.source)
        (self.project / "sources/source.md").write_text("## Slide 1\n\n标题\n", encoding="utf-8")
        self.write_json("analysis/source.identity.json", {
            "source": str(self.source), "slide_count": 1,
            "canvas": {"width_px": 1280, "height_px": 720, "aspect": 1.7778},
            "theme": {"palette": {}, "fonts": {}, "sizes": {}}, "observed": {}, "layout_sizes_pt": [],
        })
        self.write_json("analysis/source.slide_library.json", self.library())
        self.write_json("analysis/source_profile.json", {
            "schema": "pptx_intake_index.v1", "deck_count": 1,
            "decks": [{"schema": "pptx_intake_profile.v1", "stem": "source", "slide_count": 1}],
        })
        self.inventory_script = Path(self.temp.name) / "beautify_inventory.py"
        self.inventory_script.write_text(
            """#!/usr/bin/env python3
import argparse, json
from pathlib import Path
p=argparse.ArgumentParser(); p.add_argument('library'); p.add_argument('--images'); p.add_argument('-o','--output',required=True); a=p.parse_args()
lib=json.loads(Path(a.library).read_text())
images=json.loads(Path(a.images).read_text()) if a.images else []
by_slide={}
for image in images:
  for occurrence in image.get('occurrences',[]):
    by_slide.setdefault(occurrence['slide_index'],[]).append({'filename':image['filename'],'shape_name':occurrence.get('shape_name')})
slides=[]
for slide in lib['slides']:
  slides.append({'slide_index':slide['slide_index'],'page_type':slide.get('page_type'),'text_blocks':[{'slot_id':x.get('slot_id'),'role':x.get('role'),'text':x.get('text',''),'paragraph_count':x.get('paragraph_count'),'geometry':x.get('geometry')} for x in slide.get('slots',[])],'tables':[{'table_id':x.get('table_id'),'row_count':x.get('row_count'),'column_count':x.get('column_count'),'cells':[[c.get('text','') for c in r.get('cells',[])] for r in x.get('rows',[])],'rows':x.get('rows',[])} for x in slide.get('tables',[])],'charts':slide.get('charts',[]),'images':by_slide.get(slide['slide_index'],[]),'ignored':[],'needs_confirmation':[]})
Path(a.output).write_text(json.dumps({'schema':'beautify_inventory.v1','source':lib.get('source_pptx'),'slide_count':lib['slide_count'],'canvas_px':lib['canvas_px'],'slides':slides},ensure_ascii=False))
""",
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_json(self, relative: str, value: object) -> None:
        path = self.project / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, ensure_ascii=False) + "\n", encoding="utf-8")

    def library(self, *, slots: int = 1) -> dict[str, object]:
        return {
            "schema": "template_fill_pptx_library.v1", "source_pptx": str(self.source),
            "slide_count": 1, "canvas_px": {"width": 1280, "height": 720},
            "slides": [{
                "slide_index": 1, "page_type": "cover_candidate",
                "slots": [{"slot_id": f"s01_sh{i}", "role": "title_candidate" if i == 1 else "body_candidate", "text": f"冻结文字{i}", "paragraph_count": 1, "geometry": {"x": 10, "y": i * 20, "width": 300, "height": 20}} for i in range(1, slots + 1)],
                "tables": [], "charts": [],
            }],
        }

    def write_pptx(self, path: Path, *, complex_objects: bool = False, canvas_emu: tuple[int, int] | None = None) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        transition = '<p:transition/><p:timing/>' if complex_objects else ""
        merged = '<a:tc gridSpan="2"><a:txBody><a:p><a:r><a:t>合并</a:t></a:r></a:p></a:txBody></a:tc>' if complex_objects else ""
        slide_rels = ""
        with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as package:
            package.writestr("[Content_Types].xml", '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>')
            package.writestr("_rels/.rels", f'<Relationships xmlns="{beautify_runner.REL_NS}"><Relationship Id="rId1" Type="officeDocument" Target="ppt/presentation.xml"/></Relationships>')
            slide_size = f'<p:sldSz cx="{canvas_emu[0]}" cy="{canvas_emu[1]}"/>' if canvas_emu else ""
            package.writestr("ppt/presentation.xml", f'<p:presentation xmlns:p="{beautify_runner.P_NS}" xmlns:r="{beautify_runner.DOC_REL_NS}">{slide_size}<p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>')
            package.writestr("ppt/_rels/presentation.xml.rels", f'<Relationships xmlns="{beautify_runner.REL_NS}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/></Relationships>')
            if complex_objects:
                slide_rels = f'<Relationships xmlns="{beautify_runner.REL_NS}"><Relationship Id="rId8" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/audio" Target="../media/audio1.mp3"/><Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/diagramData" Target="../diagrams/data1.xml"/></Relationships>'
                package.writestr("ppt/slides/_rels/slide1.xml.rels", slide_rels)
                package.writestr("ppt/media/audio1.mp3", b"ID3test")
                package.writestr("ppt/diagrams/data1.xml", '<dgm:dataModel xmlns:dgm="http://schemas.openxmlformats.org/drawingml/2006/diagram" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><dgm:ptLst><dgm:pt><dgm:t><a:p><a:r><a:t>智能图文字</a:t></a:r></a:p></dgm:t></dgm:pt></dgm:ptLst></dgm:dataModel>')
            package.writestr("ppt/slides/slide1.xml", f'<p:sld xmlns:p="{beautify_runner.P_NS}" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:nvSpPr><p:cNvPr id="2" name="Title"/></p:nvSpPr><p:txBody><a:p><a:r><a:t>冻结文字1</a:t></a:r></a:p></p:txBody></p:sp>{merged}</p:spTree></p:cSld>{transition}</p:sld>')

    def run_inventory(self) -> dict[str, object]:
        return beautify_runner.run_inventory(
            self.project, "task-beautify", "full-ppt-master", "inventory-1", str(self.inventory_script)
        )

    def test_inventory_writes_hash_bound_contracts_and_relative_source(self) -> None:
        source_image = self.project / "images/hero.png"
        source_image.write_bytes(b"source-image-bytes")
        self.write_json("images/image_manifest.json", [{
            "filename": "hero.png", "occurrences": [{"slide_index": 1, "shape_name": "Hero"}],
        }])
        contract = self.run_inventory()
        self.assertEqual(contract["schema"], beautify_runner.INVENTORY_CONTRACT_SCHEMA)
        inventory = json.loads((self.project / "analysis/beautify_inventory.json").read_text(encoding="utf-8"))
        inputs = json.loads((self.project / ".slidesmith/contracts/beautify_inputs.json").read_text(encoding="utf-8"))
        self.assertEqual(inventory["source_pptx_sha256"], inputs["source_pptx"]["sha256"])
        self.assertEqual(inventory["task_id"], "task-beautify")
        self.assertEqual(inputs["source_slide_count"], 1)
        self.assertEqual(contract["inventory_sha256"], sha256_file(self.project / "analysis/beautify_inventory.json"))
        self.assertTrue((self.project / "analysis/beautify_risk_report.json").is_file())
        image = inventory["slides"][0]["images"][0]
        self.assertEqual(image["sha256"], sha256_file(source_image))
        self.assertEqual(image["size"], source_image.stat().st_size)

    def test_inventory_detects_density_and_complex_object_risks(self) -> None:
        self.write_pptx(self.source, complex_objects=True)
        self.write_json("analysis/source.slide_library.json", self.library(slots=17))
        self.run_inventory()
        risk = json.loads((self.project / "analysis/beautify_risk_report.json").read_text(encoding="utf-8"))
        page = risk["pages"][0]
        self.assertEqual(page["density"], "overcrowded")
        self.assertIn("P01.audio", {item["id"] for item in page["needs_confirmation"]})
        self.assertIn("P01.diagramdata", {item["id"] for item in page["needs_confirmation"]})
        self.assertIn("P01.transition", {item["id"] for item in page["ignored"]})
        self.assertIn("P01.animation", {item["id"] for item in page["ignored"]})
        self.assertIn("P01.merged_table", {item["id"] for item in page["needs_confirmation"]})
        inventory = json.loads((self.project / "analysis/beautify_inventory.json").read_text(encoding="utf-8"))
        self.assertIn("智能图文字", {item["text"] for item in inventory["slides"][0]["text_blocks"]})

    def test_exactly_one_pptx_and_variants_are_fail_closed(self) -> None:
        second = self.project / "sources/other.pptx"
        shutil.copy2(self.source, second)
        self.write_json(".slidesmith/contracts/beautify_inventory.json", {"stale": True})
        with self.assertRaises(QualityError) as caught:
            self.run_inventory()
        self.assertEqual(caught.exception.rule, "beautify_inventory.multiple_pptx")
        self.assertFalse((self.project / ".slidesmith/contracts/beautify_inventory.json").exists())
        second.unlink()
        variant = self.source.with_suffix(".pptm")
        self.source.rename(variant)
        with self.assertRaises(QualityError) as caught:
            self.run_inventory()
        self.assertEqual(caught.exception.rule, "beautify_inventory.unsupported_source_type")

    def test_image_manifest_rejects_path_escape_and_symlink(self) -> None:
        outside = Path(self.temp.name) / "outside.png"
        outside.write_bytes(b"png")
        (self.project / "images/escape.png").symlink_to(outside)
        self.write_json("images/image_manifest.json", [{"filename": "escape.png", "occurrences": [{"slide_index": 1}]}])
        with self.assertRaises(QualityError) as caught:
            self.run_inventory()
        self.assertEqual(caught.exception.rule, "beautify_inventory.inputs")

    def prepare_fidelity(self, text_units: list[str], *, structured: bool = False) -> Path:
        output = self.project / "exports/output.pptx"
        output.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(self.source, output)
        source_ref = {"path": "sources/source.pptx", "sha256": sha256_file(self.source), "size": self.source.stat().st_size}
        self.write_json(".slidesmith/contracts/beautify_inputs.json", {
            "schema": beautify_runner.INPUTS_SCHEMA, "task_id": "task-beautify", "source_pptx": source_ref,
        })
        self.write_json("analysis/beautify_inventory.json", {"schema": beautify_runner.INVENTORY_SCHEMA, "slides": []})
        self.write_json("analysis/beautify_plan.json", {"schema": "slidesmith.beautify_plan.v1", "pages": []})
        page: dict[str, object] = {"source_slide": 1, "text_units": [{"id": f"t{i}", "text": value} for i, value in enumerate(text_units, start=1)]}
        if structured:
            source_image = self.project / "images/hero.png"
            source_image.write_bytes(b"source-image-bytes")
            page.update({
                "tables": [{"id": "table-1", "row_count": 1, "column_count": 1, "cells": [["单元格"]]}],
                "charts": [{"id": "chart-1", "type": "barChart", "categories": ["Q1"], "series": [{"name": "收入", "values": [10]}]}],
                "images": [{
                    "id": "image-1", "filename": "hero.png", "source_occurrence": "P01:Picture 1",
                    "source_path": "images/hero.png", "sha256": sha256_file(source_image),
                    "size": source_image.stat().st_size, "required": True,
                }],
            })
        inputs_sha = sha256_file(self.project / ".slidesmith/contracts/beautify_inputs.json")
        inventory_sha = sha256_file(self.project / "analysis/beautify_inventory.json")
        plan_sha = sha256_file(self.project / "analysis/beautify_plan.json")
        self.write_json(".slidesmith/beautify_lock.json", {
            "schema": beautify_runner.LOCK_SCHEMA, "inputs_sha256": inputs_sha,
            "inventory_sha256": inventory_sha, "plan_sha256": plan_sha, "slides": [page],
            "ignored": None, "unsupported": None,
        })
        return output

    def write_structured_receipts(self) -> None:
        lock_sha = sha256_file(self.project / ".slidesmith/beautify_lock.json")
        lock = json.loads((self.project / ".slidesmith/beautify_lock.json").read_text(encoding="utf-8"))
        page = lock["slides"][0]
        self.write_json("analysis/beautify_svg_fidelity.json", {
            "schema": beautify_runner.SVG_FIDELITY_SCHEMA, "beautify_lock_sha256": lock_sha,
            "pages": [{
                "page_id": "P01",
                "tables": [{"id": "table-1", "content_sha256": beautify_runner.canonical_sha(beautify_runner._content_payload(page["tables"][0], "table")), "decision": "pass"}],
                "charts": [{"id": "chart-1", "content_sha256": beautify_runner.canonical_sha(beautify_runner._content_payload(page["charts"][0], "chart")), "decision": "pass"}],
                "images": [{"id": "image-1", "content_sha256": beautify_runner.canonical_sha(beautify_runner._content_payload(page["images"][0], "image")), "decision": "pass"}],
            }],
        })

    def test_text_fidelity_consumes_duplicates_and_enforces_order(self) -> None:
        output = self.prepare_fidelity(["重复", "重复"])
        report, findings, _ = beautify_runner.build_fidelity_report(self.project, [["重复"]], output, "validate-1")
        self.assertEqual(report["decision"], "fail")
        self.assertEqual({item["rule"] for item in findings}, {"beautify.text_missing_or_reordered"})
        report, findings, _ = beautify_runner.build_fidelity_report(self.project, [["重复", "重复"]], output, "validate-2")
        self.assertEqual(report["decision"], "pass", findings)
        self.assertEqual(findings, [])

    def test_added_visible_text_is_rejected(self) -> None:
        output = self.prepare_fidelity(["冻结文字1"])
        report, findings, _ = beautify_runner.build_fidelity_report(
            self.project, [["冻结文字1", "擅自新增标题"]], output, "validate-added"
        )
        self.assertEqual(report["decision"], "fail")
        self.assertIn("beautify.text_added_or_changed", {item["rule"] for item in findings})

    def test_go_receipt_null_collections_are_treated_as_empty(self) -> None:
        output = self.prepare_fidelity(["冻结文字1"])
        self.write_json("analysis/beautify_svg_fidelity.json", {
            "schema": beautify_runner.SVG_FIDELITY_SCHEMA,
            "beautify_lock_sha256": sha256_file(self.project / ".slidesmith/beautify_lock.json"),
            "pages": [{"page_id": "P01", "tables": None, "charts": None, "images": None}],
        })
        report, findings, _ = beautify_runner.build_fidelity_report(
            self.project, [["冻结文字1"]], output, "validate-go-null-receipt"
        )
        self.assertEqual(report["decision"], "pass", findings)
        self.assertEqual(findings, [])

    def test_structured_fidelity_fix_and_verify(self) -> None:
        output = self.prepare_fidelity(["冻结文字1"], structured=True)
        report, findings, _ = beautify_runner.build_fidelity_report(self.project, [["冻结文字1"]], output, "validate-broken")
        self.assertEqual(report["decision"], "fail")
        self.assertEqual({item["rule"] for item in findings}, {"beautify.table_fidelity", "beautify.chart_fidelity", "beautify.image_fidelity"})
        self.write_structured_receipts()
        report, findings, bindings = beautify_runner.build_fidelity_report(self.project, [["冻结文字1"]], output, "validate-fixed")
        self.assertEqual(report["decision"], "pass")
        self.assertEqual(findings, [])
        self.assertEqual(bindings["source_slide_count"], 1)

    def test_existing_resource_and_chart_sidecars_prove_structured_fidelity(self) -> None:
        output = self.prepare_fidelity(["冻结文字1"], structured=True)
        image_output = self.project / "images/acquired/hero.png"
        image_output.parent.mkdir(parents=True, exist_ok=True)
        image_output.write_bytes(b"png-data")
        chart_data = self.project / "charts/data/chart.json"
        chart_data.parent.mkdir(parents=True, exist_ok=True)
        self.write_json("charts/data/chart.json", {
            "schema": "slidesmith.chart_data.v1", "data": {"categories": ["Q1"], "series": [{"name": "收入", "values": [10]}]},
        })
        self.write_json(".slidesmith/resources_manifest.json", {
            "schema": "slidesmith.resources_manifest.v1",
            "resources": [
                {"id": "image-resource", "page": 1, "type": "image", "status": "ready", "input": {"source_reference": "images/hero.png"}, "output": {"path": "images/acquired/hero.png", "sha256": sha256_file(image_output)}},
                {"id": "chart-data", "page": 1, "type": "chart_data", "status": "ready", "input": {"source_reference": "sources/source.md"}, "output": {"path": "charts/data/chart.json", "sha256": sha256_file(chart_data)}},
            ],
        })
        self.write_json("analysis/svg_resource_usage.json", {
            "schema": "slidesmith.svg_resource_usage.v1",
            "pages": [{"page_id": "P01", "resources": [{"resource_id": "image-resource"}]}],
        })
        self.write_json("analysis/chart_usage.json", {
            "schema": "slidesmith.chart_usage.v1",
            "charts": [{"page_id": "P01", "chart_id": "chart-1", "data_resource_id": "chart-data", "data_sha256": sha256_file(chart_data)}],
        })
        report, findings, _ = beautify_runner.build_fidelity_report(
            self.project, [["冻结文字1", "单元格"]], output, "validate-sidecars"
        )
        self.assertEqual(report["decision"], "pass", findings)
        self.assertEqual(findings, [])

    def test_source_mutation_is_stale(self) -> None:
        output = self.prepare_fidelity(["冻结文字1"])
        with self.source.open("ab") as handle:
            handle.write(b"mutation")
        with self.assertRaises(QualityError) as caught:
            beautify_runner.build_fidelity_report(self.project, [["冻结文字1"]], output, "validate-stale")
        self.assertEqual(caught.exception.rule, "beautify_fidelity.source_stale")

    def test_source_image_mutation_is_stale(self) -> None:
        output = self.prepare_fidelity(["冻结文字1"], structured=True)
        (self.project / "images/hero.png").write_bytes(b"mutated-source-image")
        with self.assertRaises(QualityError) as caught:
            beautify_runner.build_fidelity_report(self.project, [["冻结文字1"]], output, "validate-image-stale")
        self.assertEqual(caught.exception.rule, "beautify_fidelity.source_image_stale")

    def test_canvas_mismatch_is_blocking(self) -> None:
        output = self.prepare_fidelity(["冻结文字1"])
        self.write_pptx(output, canvas_emu=(9144000, 6858000))
        lock_path = self.project / ".slidesmith/beautify_lock.json"
        lock = json.loads(lock_path.read_text(encoding="utf-8"))
        lock["canvas"] = {"width": 1280, "height": 720, "unit": "px"}
        lock_path.write_text(json.dumps(lock, ensure_ascii=False) + "\n", encoding="utf-8")
        report, findings, _ = beautify_runner.build_fidelity_report(self.project, [["冻结文字1"]], output, "validate-canvas")
        self.assertEqual(report["decision"], "fail")
        self.assertIn("beautify.canvas_mismatch", {item["rule"] for item in findings})


if __name__ == "__main__":
    unittest.main()
