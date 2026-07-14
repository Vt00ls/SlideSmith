#!/usr/bin/env python3
"""Run SlideSmith's deterministic SPEC-04 full-main contract fixtures."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path


def run(command: list[str], cwd: Path) -> None:
    print(f"+ {' '.join(command)}", flush=True)
    subprocess.run(command, cwd=cwd, check=True)


def main() -> None:
    root = Path(__file__).resolve().parents[3]
    run(["go", "test", "./internal/service", "-run", "^TestFullMainFixedFixtures$", "-count=1"], root / "backend")
    run([sys.executable, "-m", "unittest", "runtime/ppt-master-agent/scripts/test_ppt_runner.py"], root)
    print("SPEC-04 full-main contract smoke passed", flush=True)


if __name__ == "__main__":
    main()
