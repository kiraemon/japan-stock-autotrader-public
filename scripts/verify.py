"""Run the public repository's checks from a local clone or CI."""

from pathlib import Path
import subprocess
import sys


def main() -> int:
    root = Path(__file__).resolve().parents[1]
    checks = (
        ("Go tests", ("go", "test", "-count=1", "./...")),
        ("Go vet", ("go", "vet", "./...")),
        (
            "Synthetic paper session",
            ("go", "run", "./cmd/kabu-research", "session-demo", "-direction", "long", "-variant", "A"),
        ),
        ("Synthetic paper ledger", ("go", "run", "./cmd/kabu-research", "paper-demo")),
        (
            "Python MACD tests",
            (sys.executable, "-B", "-m", "unittest", "discover", "-s", "research/python", "-t", "research/python", "-v"),
        ),
    )
    for label, command in checks:
        print(f"\n== {label} ==", flush=True)
        try:
            result = subprocess.run(command, cwd=root)
        except FileNotFoundError:
            print("Go is required. Install Go 1.26 or later and add it to PATH.", file=sys.stderr)
            return 127
        if result.returncode:
            print(f"{label} failed (exit {result.returncode}).", file=sys.stderr)
            return result.returncode
    print("\nAll verification checks passed.", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
