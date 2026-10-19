package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"

	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/s01"
)

const round2PeriodSHA = "2f7b838afde52af357f055385075f502183cb554e40fa40d8bdf9c9c12f2c9f7"

type round2Partition struct {
	Sessions int      `json:"sessions"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Dates    []string `json:"dates"`
}
type round2PeriodPlan struct {
	ScopeSHA string                     `json:"scope_plan_sha256"`
	Sessions int                        `json:"calendar_sessions"`
	Parts    map[string]round2Partition `json:"partitions"`
	Warmup   []string                   `json:"warmup_sessions"`
	Pins     []studyArtifact            `json:"calendar_capture_pins"`
}

func round2Periods(raw []byte, sessions []string) (map[string][]string, error) {
	fail := func() (map[string][]string, error) {
		return nil, errors.New("損益前に固定した第2回研究の99/33/34営業日を確認してください")
	}
	var p round2PeriodPlan
	if hash(raw) != round2PeriodSHA || json.Unmarshal(raw, &p) != nil || p.ScopeSHA != s01.Round2ScopeSHA256 || p.Sessions != 166 || len(sessions) != 166 || len(p.Parts) != 3 {
		return fail()
	}
	joined := []string{}
	parts := map[string][]string{}
	for _, name := range []string{"train", "validation", "holdout"} {
		x := p.Parts[name]
		if len(x.Dates) != x.Sessions || x.Sessions == 0 || x.Dates[0] != x.From || x.Dates[len(x.Dates)-1] != x.To {
			return fail()
		}
		parts[name] = append([]string{}, x.Dates...)
		joined = append(joined, x.Dates...)
	}
	if !slices.Equal(joined, sessions) {
		return fail()
	}
	return parts, nil
}

// Bind the period's original calendar captures and complete warmup/evaluation
// sessions to those revalidated by the universe loader. No weekday inference.
func round2CalendarPins(raw []byte, sources []universeSource, sessions []string) error {
	var p round2PeriodPlan
	if hash(raw) != round2PeriodSHA || json.Unmarshal(raw, &p) != nil {
		return errors.New("第2回研究の公式カレンダー固定を確認してください")
	}
	pins := map[string]string{}
	for _, pin := range p.Pins {
		pins[filepath.Clean(pin.Path)] = pin.SHA
	}
	seen := map[string]bool{}
	for _, source := range sources {
		if source.Kind != "calendar" {
			continue
		}
		path := filepath.Clean(source.Capture)
		// Capture pins are repository-relative; universe records are absolute.
		absolute, err := filepath.Abs(path)
		if err != nil {
			return errors.New("公式カレンダーのパスを確認できません")
		}
		matched := false
		for pin, sha := range pins {
			pinAbsolute, err := filepath.Abs(pin)
			if err == nil && pinAbsolute == absolute && sha == source.CaptureSHA && !seen[pin] {
				seen[pin], matched = true, true
				break
			}
		}
		if !matched {
			return errors.New("固定した10か月の公式カレンダー出典と一致しません")
		}
	}
	all := append([]string{}, p.Warmup...)
	for _, name := range []string{"train", "validation", "holdout"} {
		all = append(all, p.Parts[name].Dates...)
	}
	if len(seen) != len(pins) || !slices.Equal(all, sessions) {
		return errors.New("前段を含む公式203営業日と固定期間が一致しません")
	}
	return nil
}
