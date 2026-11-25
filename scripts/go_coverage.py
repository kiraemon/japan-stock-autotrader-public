"""Measure Go statement and gobco branch coverage on the current OS."""

import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
from tempfile import TemporaryDirectory


ROOT = Path(__file__).resolve().parents[1]
GOBCO_VERSION = "1.3.4"
STATEMENT_FLOOR = 100
BRANCH_FLOOR = 90
BRANCH_SUMMARY = re.compile(r"^Branch coverage: (\d+)/(\d+)$", re.MULTILINE)


def run(command, cwd):
    result = subprocess.run(command, cwd=cwd, capture_output=True, encoding="utf-8", errors="replace")
    if result.returncode:
        raise RuntimeError(f"{' '.join(map(str, command))} failed:\n{result.stdout}{result.stderr}")
    return result.stdout


def go_packages():
    # go list emits one JSON object per package, separated by whitespace.
    output = run(("go", "list", "-json", "./..."), ROOT)
    decoder = json.JSONDecoder()
    packages = []
    while output := output.lstrip():
        package, end = decoder.raw_decode(output)
        packages.append(package)
        output = output[end:]
    return packages


def copy_for_gobco(destination, packages):
    # gobco type-checks all .go files in the directory, even incompatible OS variants.
    ignored = {
        (Path(package["Dir"]).relative_to(ROOT) / name).as_posix()
        for package in packages
        for name in package.get("IgnoredGoFiles", [])
    }
    # Keep test fixtures and current edits, while leaving ignored data and Git metadata out.
    files = run(("git", "-c", "core.quotepath=false", "ls-files", "-z", "--cached", "--others", "--exclude-standard"), ROOT)
    for name in dict.fromkeys(files.split("\0")):
        if not name or name in ignored:
            continue
        source = ROOT / name
        if source.is_symlink() or not source.resolve().is_relative_to(ROOT):
            raise RuntimeError(f"Unsupported coverage input path: {name}")
        if source.is_file():
            target = destination / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)


def statement_coverage(profile):
    lines = profile.read_text(encoding="utf-8").splitlines()
    if not lines or lines[0] != "mode: atomic":
        raise ValueError("Expected an atomic Go coverage profile.")
    blocks = {}
    for line in lines[1:]:
        span, weight, count = line.rsplit(maxsplit=2)
        weight, count = int(weight), int(count)
        prior_weight, hit = blocks.get(span, (weight, False))
        if weight != prior_weight or weight < 0 or count < 0:
            raise ValueError(f"Invalid Go coverage block: {span}")
        blocks[span] = weight, hit or count > 0
    return sum(weight for weight, hit in blocks.values() if hit), sum(weight for weight, _ in blocks.values())


def branch_coverage(package, gobco, module, profiles):
    relative = Path(package["Dir"]).relative_to(ROOT).as_posix()
    profile = profiles / relative / "branches.json"
    profile.parent.mkdir(parents=True, exist_ok=True)
    output = run((gobco, "-branch", "-stats", profile, f"./{relative}"), module)
    matches = BRANCH_SUMMARY.findall(output)
    if len(matches) != 1 or not profile.is_file():
        raise RuntimeError(f"Expected statistics and one branch summary for {relative}:\n{output}")
    covered, total = map(int, matches[0])
    if covered > total:
        raise ValueError(f"Invalid branch count for {relative}: {covered}/{total}")
    return relative, covered, total


def print_result(metric, label, covered, total, floor):
    if total <= 0 or covered > total:
        raise ValueError(f"No valid coverage total for {label}.")
    passed = covered * 100 >= total * floor
    status = "PASS" if passed else "BELOW TARGET"
    print(f"{label}: {covered}/{total} ({100 * covered / total:.2f}%), target {floor}% [{status}]", flush=True)
    if output := os.environ.get("GITHUB_OUTPUT"):
        with Path(output).open("a", encoding="utf-8") as stream:
            stream.write(f"{metric}={covered}/{total}/{floor}\n")
    return passed


def measure_statements(work):
    profile = work / "statements.out"
    print("Measuring Go statements", flush=True)
    run(("go", "test", "-count=1", "-coverpkg=./...", "-covermode=atomic", f"-coverprofile={profile}", "./..."), ROOT)
    return print_result("c0", "C0 statements", *statement_coverage(profile), STATEMENT_FLOOR)


def measure_branches(work, jobs):
    gobco = shutil.which("gobco")
    if gobco is None or run((gobco, "-version"), ROOT).strip() != GOBCO_VERSION:
        raise RuntimeError(f"Install gobco: go install github.com/rillig/gobco@v{GOBCO_VERSION}")
    packages = go_packages()
    module = work / "module"
    copy_for_gobco(module, packages)
    source_packages = [package for package in packages if package.get("GoFiles") or package.get("CgoFiles")]
    print(f"Measuring gobco branches in {len(source_packages)} Go packages", flush=True)
    with ThreadPoolExecutor(max_workers=jobs) as executor:
        results = executor.map(lambda package: branch_coverage(package, gobco, module, work / "branches"), source_packages)
        covered_total, branch_total = 0, 0
        for relative, covered, total in results:
            print(f"  {relative}: {covered}/{total}", flush=True)
            covered_total += covered
            branch_total += total
    return print_result("c1", "C1 gobco branches", covered_total, branch_total, BRANCH_FLOOR)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--metric", choices=("all", "c0", "c1"), default="all", help="Coverage metric to measure.")
    parser.add_argument("--report-only", action="store_true", help="Report unmet targets with exit 0. Measurement errors still fail.")
    parser.add_argument("--jobs", type=int, default=2, help="Concurrent gobco packages (default: 2).")
    args = parser.parse_args()
    if args.jobs < 1:
        parser.error("--jobs must be positive")

    try:
        outcomes = []
        with TemporaryDirectory(prefix="go-coverage-") as temporary:
            work = Path(temporary)
            if args.metric in ("all", "c0"):
                outcomes.append(measure_statements(work))
            if args.metric in ("all", "c1"):
                outcomes.append(measure_branches(work, args.jobs))
    except (OSError, RuntimeError, ValueError) as error:
        print(f"Coverage measurement failed: {error}", file=sys.stderr)
        return 2
    return 0 if args.report_only or all(outcomes) else 1


if __name__ == "__main__":
    raise SystemExit(main())
