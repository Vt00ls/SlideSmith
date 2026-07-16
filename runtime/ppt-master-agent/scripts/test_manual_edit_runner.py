import json
import subprocess
import sys
import tempfile
import unittest
from hashlib import sha256
from pathlib import Path

import manual_edit_runner as runner


class ManualEditRunnerTests(unittest.TestCase):
    def fixture(self):
        temp = tempfile.TemporaryDirectory()
        project = Path(temp.name)
        (project / "svg_output").mkdir()
        svg = project / "svg_output/01_title.svg"
        svg.write_text('<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720"><text id="title" x="10" y="20">Old</text></svg>', encoding="utf-8")
        root = runner.ET.parse(svg).getroot()
        by_id, source_ids = runner.assign_editor_ids(root)
        fp = runner.fingerprint(by_id["title"], runner.parent_map(root), source_ids)
        patch = {
            "schema": "slidesmith.manual_edit_draft.v1", "task_id": "task-1", "edit_session_id": "edit-1",
            "pages": [{"page_id": "P01", "base_svg_sha256": sha256(svg.read_bytes()).hexdigest(), "operations": [
                {"operation_id": "op-1", "type": "set_text", "target": {"element_id": "title", "tag": "text", "element_fingerprint": fp}, "value": {"text": "新标题"}},
                {"operation_id": "op-2", "type": "translate", "target": {"element_id": "title", "tag": "text", "element_fingerprint": ""}, "value": {"dx": 5, "dy": 7}},
            ]}], "annotations": [],
        }
        # Every operation binds to the same immutable base element fingerprint.
        patch["pages"][0]["operations"][1]["target"]["element_fingerprint"] = fp
        (project / "analysis").mkdir()
        (project / "analysis/manual_edit_patch.json").write_text(json.dumps(patch, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
        return temp, project, svg

    def test_applies_ordered_text_and_translate_with_receipt(self):
        temp, project, svg = self.fixture()
        self.addCleanup(temp.cleanup)
        args = type("Args", (), {"project": project, "patch": "analysis/manual_edit_patch.json", "task_id": "task-1", "session_id": "edit-1", "report": "analysis/manual_edit_apply_report.json"})()
        report = runner.run(args)
        self.assertEqual(report["summary"], {"requested": 2, "applied": 2, "rejected": 0})
        text = runner.ET.parse(svg).getroot().find("{http://www.w3.org/2000/svg}text")
        self.assertEqual((text.text, text.get("x"), text.get("y")), ("新标题", "15", "27"))

    def test_rejects_fingerprint_mismatch_without_writing(self):
        temp, project, svg = self.fixture()
        self.addCleanup(temp.cleanup)
        patch_path = project / "analysis/manual_edit_patch.json"
        patch = json.loads(patch_path.read_text())
        patch["pages"][0]["operations"][0]["target"]["element_fingerprint"] = "sha256:bad"
        patch_path.write_text(json.dumps(patch), encoding="utf-8")
        before = svg.read_bytes()
        args = type("Args", (), {"project": project, "patch": "analysis/manual_edit_patch.json", "task_id": "task-1", "session_id": "edit-1", "report": "analysis/manual_edit_apply_report.json"})()
        with self.assertRaises(runner.EditError):
            runner.run(args)
        self.assertEqual(svg.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
