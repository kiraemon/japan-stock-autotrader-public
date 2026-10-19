package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

func checkRound3Freeze(path string, manifest []byte) (string, error) {
	revision, dirty := studyBuildIdentity()
	return checkRound3FreezeIdentity(path, manifest, revision, dirty)
}

func checkRound3FreezeIdentity(path string, manifest []byte, revision, dirty string) (string, error) {
	fail := func() (string, error) {
		return "", errors.New("第3回の最終には同一clean版/入力/312検証ブック/価格・指標・費用の独立検算を固定してください")
	}
	raw, e := readInput(path)
	if e != nil {
		return "", e
	}
	var f studyFreeze
	if strictJSON(raw, &f) != nil || f.Version != 1 || f.PlanSHA != round3ScopeSHA || f.ManifestSHA != hash(manifest) || len(f.Revision) != 40 || f.Revision != revision || f.OriginalRevision != revision || dirty != "false" {
		return fail()
	}
	body, e := pinnedInput(f.Development.Path, f.Development.SHA)
	if e != nil {
		return "", e
	}
	var report round3StudyReport
	if strictJSON(body, &report) != nil || report.Version != round3Version || report.Phase != "validation" || report.HoldoutRead || !report.NewPNL || report.ManifestSHA != f.ManifestSHA || report.ScopeSHA != round3ScopeSHA || report.PeriodSHA != round3PeriodSHA || len(report.Books) != round3BookCount || len(report.Sessions) != 58 || len(report.Matrices) != 44 {
		return fail()
	}
	for _, matrix := range report.Matrices {
		if _, e := pinnedInput(matrix.Path, matrix.SHA); e != nil {
			return "", e
		}
	}
	for _, book := range report.Books {
		if _, e := pinnedInput(book.Artifact.Path, book.Artifact.SHA); e != nil {
			return "", e
		}
	}
	body, e = pinnedInput(f.DevelopmentRecord.Path, f.DevelopmentRecord.SHA)
	if e != nil {
		return "", e
	}
	var record struct {
		Command   string `json:"command"`
		Revision  string `json:"code_revision"`
		Dirty     string `json:"working_tree_at_build"`
		ReportSHA string `json:"report_sha256"`
		InputSHA  string `json:"input_sha256"`
	}
	if json.Unmarshal(body, &record) != nil || record.Command != "study-round3" || record.Revision != revision || record.Dirty != "false" || record.ReportSHA != f.Development.SHA || record.InputSHA != f.ManifestSHA {
		return fail()
	}
	body, e = pinnedInput(f.IndependentAudit.Path, f.IndependentAudit.SHA)
	if e != nil {
		return "", e
	}
	var audit struct {
		ReportSHA  string `json:"report_sha256"`
		Accounting bool   `json:"independent_decimal_accounting"`
		Prices     bool   `json:"independent_price_and_indicators"`
		Books      int    `json:"books"`
		Cases      int    `json:"price_cases"`
		Holdout    bool   `json:"holdout_pnl_read"`
	}
	if json.Unmarshal(body, &audit) != nil || audit.ReportSHA != f.Development.SHA || !audit.Accounting || !audit.Prices || audit.Holdout || audit.Books != round3BookCount || audit.Cases != 58*30*3 {
		return fail()
	}
	body, e = pinnedInput(f.Verification.Path, f.Verification.SHA)
	if e != nil {
		return "", e
	}
	if !strings.Contains(string(body), "RESULT=PASS") {
		return fail()
	}
	sources := map[string]bool{}
	for _, source := range f.CoreSources {
		p := filepath.ToSlash(filepath.Clean(source.Path))
		if (!strings.HasPrefix(p, "internal/") && !strings.HasPrefix(p, "cmd/kabu-research/")) || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || sources[p] {
			return fail()
		}
		if _, e := pinnedInput(p, source.SHA); e != nil {
			return "", e
		}
		sources[p] = true
	}
	count := 0
	for _, root := range []string{"internal", "cmd/kabu-research"} {
		e := filepath.WalkDir(root, func(path string, entry fs.DirEntry, e error) error {
			if e != nil {
				return errors.New("第3回の固定ソースを確認できません")
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				count++
				if !sources[filepath.ToSlash(path)] {
					return errors.New("第3回の計算/組立ソースが未固定です")
				}
			}
			return nil
		})
		if e != nil {
			return "", e
		}
	}
	if count == 0 || count != len(sources) {
		return fail()
	}
	return hash(raw), nil
}
