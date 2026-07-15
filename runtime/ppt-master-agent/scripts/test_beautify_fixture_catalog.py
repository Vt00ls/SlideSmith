from __future__ import annotations

import json
import unittest
from pathlib import Path


class BeautifyFixtureCatalogTest(unittest.TestCase):
    def test_spec08_fixture_catalog_is_complete_and_stable(self) -> None:
        root = Path(__file__).resolve().parent.parent / "fixtures"
        names = {
            "beautify-text", "beautify-images", "beautify-table", "beautify-charts",
            "beautify-complex", "beautify-cjk", "beautify-overcrowded", "beautify-identity-override",
        }
        found = {path.parent.name for path in root.glob("beautify-*/fixture.json")}
        self.assertEqual(found, names)
        all_rules: set[str] = set()
        for name in sorted(names):
            payload = json.loads((root / name / "fixture.json").read_text(encoding="utf-8"))
            self.assertEqual(payload["name"], name)
            self.assertTrue(payload["source_features"])
            self.assertIn(payload["expected_decision"], {"pass", "pass_with_warnings", "fail"})
            self.assertTrue(payload["negative_mutations"])
            for mutation in payload["negative_mutations"]:
                self.assertTrue(mutation["kind"])
                self.assertIn(".", mutation["expected_rule"])
                all_rules.add(mutation["expected_rule"])
        self.assertTrue({
            "beautify.text_missing_or_reordered", "beautify.table_fidelity", "beautify.chart_fidelity",
            "beautify.image_fidelity", "beautify.page_count", "beautify.canvas_mismatch",
        }.issubset(all_rules))
        charts = json.loads((root / "beautify-charts/fixture.json").read_text(encoding="utf-8"))
        self.assertEqual(charts["fix_verify"], ["fail-fidelity", "retry-svg", "clean-stale-validation", "pass-fidelity"])


if __name__ == "__main__":
    unittest.main()
