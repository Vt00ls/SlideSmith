from __future__ import annotations

import json
import unittest
from pathlib import Path


class QualityFixtureCatalogTest(unittest.TestCase):
    def test_spec07_fixture_catalog_is_complete_and_stable(self) -> None:
        root = Path(__file__).resolve().parent.parent / "fixtures"
        names = {
            "quality-clean", "quality-svg-error", "quality-chart-mismatch",
            "quality-export-text-loss", "quality-render-blank", "quality-warning",
            "quality-broken-media", "quality-cjk",
        }
        found = {path.parent.name for path in root.glob("quality-*/fixture.json")}
        self.assertEqual(found, names)
        for name in sorted(names):
            payload = json.loads((root / name / "fixture.json").read_text(encoding="utf-8"))
            self.assertEqual(payload["name"], name)
            self.assertIn(payload["expected_decision"], {"pass", "pass_with_warnings", "fail"})
            if payload["expected_decision"] == "fail":
                self.assertIn(".", payload["expected_rule"])


if __name__ == "__main__":
    unittest.main()
