from __future__ import annotations

import hashlib
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import quality_runner
from quality_schema import sha256_file


class QualityRunnerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.project = Path(self.temp.name) / "project"
        self.project.mkdir()
        files = {
            "design_spec.md": "# P01\n",
            "spec_lock.md": "P01\n",
            "notes/total.md": "## P01 | Test\nNotes\n",
            ".slidesmith/resources_manifest.json": '{"schema":"slidesmith.resources_manifest.v1","resources":[]}\n',
            "analysis/svg_resource_usage.json": '{"schema":"slidesmith.svg_resource_usage.v1","pages":[]}\n',
            "analysis/chart_usage.json": '{"schema":"slidesmith.chart_usage.v1","charts":[]}\n',
            "analysis/notes_inventory.json": '{"schema":"slidesmith.notes_inventory.v1","pages":[]}\n',
            "svg_output/01_test.svg": '<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720"><rect width="1280" height="720" fill="#fff"/><text id="title" x="100" y="100">Hello 世界 123</text></svg>\n',
        }
        for relative, content in files.items():
            path = self.project / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8")
        svg_sha = sha256_file(self.project / "svg_output/01_test.svg")
        inventory = {
            "schema": "slidesmith.svg_inventory.v1",
            "task_id": "task-quality",
            "pages": [{"page_id": "P01", "path": "svg_output/01_test.svg", "sha256": svg_sha}],
        }
        self.write_json("analysis/svg_inventory.json", inventory)
        self.checker = Path(self.temp.name) / "svg_quality_checker.py"
        self.write_checker(errors=[], warnings=[])
        contract = {"task_id": "task-quality", "runner_profile": "full-ppt-master"}
        for field, relative in quality_runner.UPSTREAM_HASHES.items():
            contract[field] = sha256_file(self.project / relative)
        file_sha = sha256_file(self.project / "svg_output/01_test.svg")
        aggregate = hashlib.sha256()
        aggregate.update(b"01_test.svg\x00" + file_sha.encode("ascii") + b"\n")
        contract["svg_output_sha256"] = aggregate.hexdigest()
        self.write_json(".slidesmith/contracts/svg_execute.json", contract)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_json(self, relative: str, value: object) -> None:
        path = self.project / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, ensure_ascii=False) + "\n", encoding="utf-8")

    def write_checker(self, *, errors: list[str], warnings: list[str]) -> None:
        self.checker.write_text(
            "class SVGQualityChecker:\n"
            "    def check_directory(self, project):\n"
            f"        return [{{'file':'01_test.svg','errors':{errors!r},'warnings':{warnings!r},'passed':{not errors!r}}}]\n",
            encoding="utf-8",
        )

    def refresh_upstream(self) -> None:
        contract = json.loads((self.project / ".slidesmith/contracts/svg_execute.json").read_text(encoding="utf-8"))
        svg_sha = sha256_file(self.project / "svg_output/01_test.svg")
        inventory = json.loads((self.project / "analysis/svg_inventory.json").read_text(encoding="utf-8"))
        inventory["pages"][0]["sha256"] = svg_sha
        self.write_json("analysis/svg_inventory.json", inventory)
        for field, relative in quality_runner.UPSTREAM_HASHES.items():
            contract[field] = sha256_file(self.project / relative)
        aggregate = hashlib.sha256()
        aggregate.update(b"01_test.svg\x00" + svg_sha.encode("ascii") + b"\n")
        contract["svg_output_sha256"] = aggregate.hexdigest()
        self.write_json(".slidesmith/contracts/svg_execute.json", contract)

    def test_structured_checker_adapter_and_warning_gate(self) -> None:
        self.write_checker(errors=[], warnings=["Font fallback may be used"])
        contract = quality_runner.run(self.project, "phase-quality-1", str(self.checker))
        self.assertEqual(contract["decision"], "pass_with_warnings")
        report = json.loads((self.project / "validation/svg_quality_report.json").read_text(encoding="utf-8"))
        self.assertEqual(report["findings"][0]["rule"], "svg.checker_warning")
        self.assertEqual(report["checker"]["adapter"], "python_api")

    def test_blocking_checker_exception_is_not_silent(self) -> None:
        self.checker.write_text(
            "class SVGQualityChecker:\n    def check_directory(self, project):\n        raise RuntimeError('secret')\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(quality_runner.QualityError, "SVG checker raised RuntimeError") as caught:
            quality_runner.run(self.project, "phase-quality-2", str(self.checker))
        self.assertEqual(caught.exception.rule, "quality.checker_exception")

    def test_fix_and_verify_replaces_stale_failed_report(self) -> None:
        svg = self.project / "svg_output/01_test.svg"
        svg.write_text(svg.read_text(encoding="utf-8").replace("Hello 世界 123", "TODO"), encoding="utf-8")
        self.refresh_upstream()
        failed = quality_runner.run(self.project, "phase-bad", str(self.checker))
        self.assertEqual(failed["decision"], "fail")
        svg.write_text(svg.read_text(encoding="utf-8").replace("TODO", "Fixed 世界 123"), encoding="utf-8")
        self.refresh_upstream()
        passed = quality_runner.run(self.project, "phase-fixed", str(self.checker))
        self.assertEqual(passed["decision"], "pass")
        report = json.loads((self.project / "validation/svg_quality_report.json").read_text(encoding="utf-8"))
        self.assertEqual(report["phase_run_id"], "phase-fixed")
        self.assertFalse(any(item["rule"] == "svg.placeholder_text" for item in report["findings"]))

    def test_chart_mismatch_and_manual_receipts(self) -> None:
        usage = {
            "schema": "slidesmith.chart_usage.v1",
            "charts": [
                {
                    "chart_id": "chart-a", "page_id": "P01", "svg": "svg_output/01_test.svg",
                    "element_id": "chart", "verification_mode": "direct-calc", "data_sha256": "a" * 64,
                    "plot_area": [100, 100, 900, 600],
                    "comparisons": [{"element_id": "bar-a", "attribute": "height", "expected": 100, "actual": 103, "tolerance": 1}],
                },
                {
                    "chart_id": "chart-b", "page_id": "P01", "svg": "svg_output/01_test.svg",
                    "element_id": "chart-b", "verification_mode": "manual-verify", "data_sha256": "b" * 64,
                    "plot_area": [100, 100, 900, 600],
                },
            ],
        }
        self.write_json("analysis/chart_usage.json", usage)
        self.refresh_upstream()
        contract = quality_runner.run(self.project, "phase-chart", str(self.checker))
        self.assertEqual(contract["decision"], "fail")
        report = json.loads((self.project / "validation/chart_verify_report.json").read_text(encoding="utf-8"))
        rules = {item["rule"] for item in report["findings"]}
        self.assertIn("chart.geometry_mismatch", rules)
        self.assertIn("chart.manual_review_required", rules)


if __name__ == "__main__":
    unittest.main()
