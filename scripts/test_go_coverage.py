"""Check that the two coverage metrics apply the same exclusions."""

import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from go_coverage import ARCHIVED_RESEARCH, ROOT, scoped_branches, statement_coverage, statement_exclusions


class CoverageScopeTests(unittest.TestCase):
    def test_only_listed_source_files_are_excluded(self):
        packages = [
            {"Dir": str(ROOT / package), "ImportPath": f"example.test/{package}",
             "GoFiles": sorted(files) + ["shared.go", "new_study.go"]}
            for package, files in ARCHIVED_RESEARCH.items()
        ]
        excluded = statement_exclusions(packages)
        self.assertIn("example.test/cmd/kabu-research/study.go", excluded)
        self.assertNotIn("example.test/cmd/kabu-research/shared.go", excluded)
        self.assertNotIn("example.test/cmd/kabu-research/new_study.go", excluded)
        with self.assertRaisesRegex(ValueError, "absent from"):
            statement_exclusions(packages[1:])

    def test_statement_profile_keeps_shared_code_and_merges_duplicate_spans(self):
        with TemporaryDirectory() as temporary:
            profile = Path(temporary) / "cover.out"
            profile.write_text(
                "mode: atomic\n"
                "example.test/cmd/study.go:1.1,2.1 9 0\n"
                "example.test/cmd/shared.go:1.1,2.1 2 0\n"
                "example.test/cmd/shared.go:1.1,2.1 2 1\n"
                "example.test/cmd/shared.go:3.1,4.1 1 0\n",
                encoding="utf-8",
            )
            self.assertEqual(statement_coverage(profile, {"example.test/cmd/study.go"}), (2, 3))

    def test_branch_profile_excludes_only_archive_and_checks_raw_counts(self):
        rows = [
            {"Start": "cmd/study.go:1:3", "TrueCount": 0, "FalseCount": 1},
            {"Start": "cmd/shared.go:2:3", "TrueCount": 1, "FalseCount": 1},
            {"Start": "cmd/shared.go:3:3", "TrueCount": 0, "FalseCount": 0},
        ]
        with TemporaryDirectory() as temporary:
            profile = Path(temporary) / "branches.json"
            profile.write_text(json.dumps(rows), encoding="utf-8")
            source = {"study.go", "shared.go"}
            self.assertEqual(scoped_branches(profile, source, {"study.go"}, (3, 6)), (2, 4))
            with self.assertRaisesRegex(ValueError, "do not match"):
                scoped_branches(profile, source, {"study.go"}, (2, 6))
            with self.assertRaisesRegex(ValueError, "Unexpected gobco source position"):
                scoped_branches(profile, {"study.go"}, {"study.go"}, (3, 6))
