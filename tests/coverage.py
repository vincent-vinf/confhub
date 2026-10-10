"""Merge statement profiles and enforce explicitly selected coverage targets."""

import argparse
import json
from pathlib import Path


def profile(paths):
    blocks = {}
    for path in paths:
        for line in path.read_text().splitlines()[1:]:
            location, statements, hits = line.rsplit(" ", 2)
            size = int(statements)
            count = int(hits)
            previous = blocks.get(location)
            if previous is not None and previous[0] != size:
                raise ValueError("incompatible coverage profiles; rerun from identical source")
            blocks[location] = (size, max(count, previous[1] if previous else 0))
    return blocks


def percentage(blocks):
    total = sum(size for size, _ in blocks.values())
    covered = sum(size for size, hits in blocks.values() if hits)
    if total == 0:
        raise ValueError("coverage contains no statements")
    return {"covered": covered, "statements": total, "percent": covered / total * 100}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report_dir", type=Path)
    parser.add_argument("--gate", action="store_true")
    options = parser.parse_args()
    root = options.report_dir
    backend = [path for path in (root / "backend.out", root / "process.out") if path.is_file()]
    summary = {}
    if backend:
        blocks = profile(backend)
        merged = root / "backend-merged.out"
        merged.write_text(
            "mode: atomic\n"
            + "".join(f"{name} {size} {count}\n" for name, (size, count) in sorted(blocks.items()))
        )
        summary["backend"] = percentage(blocks)
        summary["business"] = percentage(
            {name: value for name, value in blocks.items() if "/internal/config/" in name}
        )
    if (root / "sdk.out").is_file():
        summary["go_sdk"] = percentage(profile([root / "sdk.out"]))
    if (root / "python-coverage.json").is_file():
        totals = json.loads((root / "python-coverage.json").read_text())["totals"]
        summary["python_sdk"] = {
            "percent": totals["percent_covered"],
            "covered": totals["covered_lines"],
            "statements": totals["num_statements"],
        }
    if not summary:
        raise SystemExit("no coverage profiles found")
    (root / "coverage-summary.json").write_text(json.dumps(summary, indent=2))
    for name, stats in summary.items():
        print(f"{name}: {stats['percent']:.2f}% ({stats['covered']}/{stats['statements']})")
    if options.gate:
        for name, target in {"backend": 80, "business": 90, "go_sdk": 85, "python_sdk": 85}.items():
            if name not in summary or summary[name]["percent"] < target:
                raise SystemExit(f"coverage gate failed: {name}, target {target}%")


if __name__ == "__main__":
    main()
