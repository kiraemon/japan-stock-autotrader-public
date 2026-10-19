package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

type studyFreeze struct {
	Version           int             `json:"version"`
	Revision          string          `json:"final_code_revision"`
	ManifestSHA       string          `json:"manifest_sha256"`
	PlanSHA           string          `json:"strategy_plan_sha256"`
	Development       studyArtifact   `json:"development_report"`
	DevelopmentRecord studyArtifact   `json:"development_run_record"`
	IndependentAudit  studyArtifact   `json:"independent_development_audit"`
	Verification      studyArtifact   `json:"verification_log"`
	OriginalRevision  string          `json:"development_core_revision"`
	CoreSources       []studyArtifact `json:"unchanged_core_sources"`
}

func studyBuildIdentity() (string, string) {
	revision, dirty := "unavailable", "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				revision = s.Value
			}
			if s.Key == "vcs.modified" {
				dirty = s.Value
			}
		}
	}
	return revision, dirty
}
func checkStudyFreeze(path string, manifest []byte) (string, error) {
	return checkStudyFreezeMode(path, manifest, false)
}

func checkRound2Freeze(path string, manifest []byte) (string, error) {
	return checkStudyFreezeMode(path, manifest, true)
}

func checkStudyFreezeMode(path string, manifest []byte, round2 bool) (string, error) {
	planSHA, books, version := s01.PlanSHA256, 480, "s01-comparative-study-v0.1"
	if round2 {
		planSHA, books, version = s01.Round2ScopeSHA256, 1440, "s01-round2-study-v0.1"
	}
	raw, err := readInput(path)
	if err != nil {
		return "", err
	}
	var f studyFreeze
	revision, dirty := studyBuildIdentity()
	if strictJSON(raw, &f) != nil || f.Version != 1 || len(f.Revision) != 40 || f.Revision != revision || dirty != "false" || f.ManifestSHA != hash(manifest) || f.PlanSHA != planSHA || round2 && f.OriginalRevision != revision {
		return "", errors.New("最終評価には同じ入力/条件とcleanビルド版を固定してください")
	}
	dRaw, err := pinnedInput(f.Development.Path, f.Development.SHA)
	if err != nil {
		return "", err
	}
	var d studyReport
	if strictJSON(dRaw, &d) != nil || d.Version != version || d.Phase != "development" || d.HoldoutRead || d.ManifestSHA != f.ManifestSHA || d.PlanSHA != f.PlanSHA || len(d.Books) != books {
		return "", errors.New("検査済みの開発/検証レポートを固定してください")
	}
	rRaw, err := pinnedInput(f.DevelopmentRecord.Path, f.DevelopmentRecord.SHA)
	if err != nil {
		return "", err
	}
	var record struct {
		Revision  string `json:"code_revision"`
		Dirty     string `json:"working_tree_at_build"`
		ReportSHA string `json:"report_sha256"`
	}
	if json.Unmarshal(rRaw, &record) != nil || record.Revision != f.OriginalRevision || record.Dirty != "false" || record.ReportSHA != f.Development.SHA {
		return "", errors.New("開発/検証の固定コードと実行記録を確認してください")
	}
	aRaw, err := pinnedInput(f.IndependentAudit.Path, f.IndependentAudit.SHA)
	if err != nil {
		return "", err
	}
	var audit struct {
		ReportSHA   string `json:"report_sha256"`
		Independent bool   `json:"independent_decimal_accounting"`
		Holdout     bool   `json:"holdout_pnl_read"`
		Books       int    `json:"books"`
	}
	if json.Unmarshal(aRaw, &audit) != nil || audit.ReportSHA != f.Development.SHA || !audit.Independent || audit.Holdout || audit.Books != books {
		return "", errors.New("独立した開発ブックの検算を固定してください")
	}
	vRaw, err := pinnedInput(f.Verification.Path, f.Verification.SHA)
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(vRaw), "RESULT=PASS") {
		return "", errors.New("最終コードの全体検証記録を確認してください")
	}
	sources := map[string]string{}
	for _, source := range f.CoreSources {
		p := filepath.ToSlash(filepath.Clean(source.Path))
		if !strings.HasPrefix(p, "internal/") || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || sources[p] != "" {
			return "", errors.New("固定する本体ソースの一覧を確認してください")
		}
		if _, err := pinnedInput(p, source.SHA); err != nil {
			return "", err
		}
		sources[p] = source.SHA
	}
	count := 0
	err = filepath.WalkDir("internal", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.New("固定ソースの一覧を確認できません")
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			count++
			if sources[filepath.ToSlash(path)] == "" {
				return errors.New("未固定の計算ソースがあります")
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if count == 0 || count != len(sources) {
		return "", errors.New("完全な計算ソースの固定を確認してください")
	}
	return hash(raw), nil
}

type studyOpening struct {
	FreezeSHA   string    `json:"freeze_sha256"`
	ManifestSHA string    `json:"manifest_sha256"`
	Revision    string    `json:"code_revision"`
	Opened      time.Time `json:"opened_at_utc"`
}

// Replays are allowed only for exactly the same frozen inputs/code. This is a
// persistent one-version opening record, not permission to tune on holdout.
func reserveStudyOpening(freezeSHA, manifestSHA, revision string) error {
	return reserveStudyOpeningMode(freezeSHA, manifestSHA, revision, false)
}

func reserveStudyOpeningMode(freezeSHA, manifestSHA, revision string, round2 bool) error {
	path := filepath.Join("data", "research", "s01-holdout-opening-20261019.json")
	if round2 {
		path = filepath.Join("data", "research", "s01-r2-holdout-opening-20261019.json")
	}
	return reserveStudyOpeningAt(path, freezeSHA, manifestSHA, revision)
}

func reserveStudyOpeningAt(path, freezeSHA, manifestSHA, revision string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("最終評価の開封記録先を確認できません")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		raw, err := readInput(path)
		if err != nil {
			return err
		}
		var prior studyOpening
		if strictJSON(raw, &prior) != nil || prior.FreezeSHA != freezeSHA || prior.ManifestSHA != manifestSHA || prior.Revision != revision {
			return errors.New("最終評価は別の条件/コードで既に開封されています。再調整して開き直せません")
		}
		return nil
	}
	if err != nil {
		return errors.New("最終評価の開封記録を確保できません")
	}
	raw, _ := json.Marshal(studyOpening{freezeSHA, manifestSHA, revision, time.Now().UTC()})
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("最終評価の開封記録を保存できません")
	}
	return nil
}
