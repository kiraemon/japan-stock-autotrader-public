package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadStudyValidatesPinnedPartitionAndSessionOrder(t *testing.T) {
	const first, last, sessions = "2024-12-02", "2025-02-28", 58
	date := clock(first, 0)
	source := study{
		Version: "jp-intraday-round3-study-v0.1", Phase: "validation", ScopeSHA: scopeSHA,
		ManifestSHA: strings.Repeat("a", 64), IndexSHA: strings.Repeat("b", 64), Books: make([]bookRef, 312),
	}
	for len(source.Dates) < sessions-1 {
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			source.Dates = append(source.Dates, date.Format(time.DateOnly))
		}
		date = date.AddDate(0, 0, 1)
	}
	source.Dates = append(source.Dates, last)
	for i, model := range models {
		source.Books[i] = bookRef{
			Model: model, Phase: source.Phase, Cohort: "native",
			Summary: book{Scenario: scenario{BPS: 10, Commission: "sor_conditional_zero"}},
		}
	}

	path := filepath.Join(t.TempDir(), "study.json")
	write := func(t *testing.T, input study) pin {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return pin{Path: path, SHA: digest(raw)}
	}
	m := manifest{Input: pin{SHA: source.ManifestSHA}}
	loaded, refs, err := loadStudy(write(t, source), m, source.IndexSHA)
	if err != nil || len(loaded.Dates) != sessions || len(refs) != len(models) {
		t.Fatalf("valid study scope rejected: %v", err)
	}
	for i, ref := range refs {
		if ref.Model != models[i] || ref.Phase != source.Phase || ref.Cohort != "native" {
			t.Fatalf("wrong primary book selected: %+v", ref)
		}
	}
	for _, tc := range []struct {
		name, message string
		mutate        func(*study)
	}{
		{"sealed partition", "restricted to opened", func(s *study) { s.Phase = "protected" }},
		{"changed index", "contract mismatch", func(s *study) { s.IndexSHA = strings.Repeat("c", 64) }},
		{"invalid ordering", "sessions invalid", func(s *study) { s.Dates[1], s.Dates[2] = s.Dates[2], s.Dates[1] }},
		{"weekend", "sessions invalid", func(s *study) { s.Dates[5] = "2024-12-07" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := source
			input.Dates = append([]string(nil), source.Dates...)
			tc.mutate(&input)
			_, refs, err := loadStudy(write(t, input), m, source.IndexSHA)
			if err == nil || !strings.Contains(err.Error(), tc.message) || refs != nil {
				t.Fatalf("invalid scope returned primary books: %v, %v", refs, err)
			}
		})
	}
}

func TestLoadDaysOnlyReadsMatchingPinnedMonths(t *testing.T) {
	const date, month, symbol = "2026-06-22", "2026-06", "0000@1"
	at := clock(date, 540)
	source := dataset{Version: 1, Scale: 10000, Symbol: symbol, Origin: "observed", Environment: "production"}
	source.Source.Format = "jquants-bulk-minute-turnover-v0.1"
	source.Source.Label = "start"
	source.Source.SHA = strings.Repeat("a", 64)
	source.Days = []day{{Date: date, Bars: []bar{
		{At: at, Open: 10000, High: 10100, Low: 9800, Close: 9900},
		{At: at.Add(time.Minute), Open: 9900, High: 10000, Low: 9800, Close: 10000},
	}}}
	path := filepath.Join(t.TempDir(), "monthly.json")
	months := map[string]bool{month: true}
	write := func(t *testing.T, input dataset) minuteFile {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return minuteFile{Phase: "validation", Month: month, Symbol: symbol, Path: path, SHA: digest(raw), SourceSHA: source.Source.SHA}
	}
	valid := write(t, source)
	files := []minuteFile{
		{Phase: "holdout", Month: month, Symbol: symbol},
		{Phase: "validation", Month: "2026-07", Symbol: symbol},
		{Phase: "validation", Month: month, Symbol: "0001@1"},
		valid,
	}
	days, pins, err := loadDays(context.Background(), "validation", symbol, months, files)
	if err != nil || len(pins) != 1 || pins[0] != (pin{Path: path, SHA: valid.SHA}) || len(days) != 1 {
		t.Fatalf("scope or provenance changed: %v, %v", pins, err)
	}
	if got := days[date].Bars; len(got) != 2 || !got[1].At.Equal(at.Add(time.Minute)) || got[1].Close != 10000 {
		t.Fatalf("normalized bars changed: %+v", got)
	}
	if _, _, err := loadDays(context.Background(), "validation", symbol, months, files[:3]); err == nil ||
		!strings.Contains(err.Error(), "No matching normalized source") {
		t.Fatalf("missing source was not reported: %v", err)
	}

	for _, tc := range []struct {
		name, want string
		mutate     func(*dataset)
	}{
		{"wrong source hash", "Normalized observed source mismatch", func(d *dataset) { d.Source.SHA = strings.Repeat("b", 64) }},
		{"duplicate day", "duplicated or out of month", func(d *dataset) { d.Days = append(d.Days, d.Days[0]) }},
		{"unaligned bar", "Normalized path bar invalid", func(d *dataset) { d.Days[0].Bars[0].At = at.Add(time.Second) }},
		{"duplicate minute", "Normalized path bar invalid", func(d *dataset) { d.Days[0].Bars[1].At = at }},
		{"close exceeds high", "Normalized path bar invalid", func(d *dataset) { d.Days[0].Bars[1].Close = 10100 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := source
			input.Days = []day{{Date: date, Bars: append([]bar(nil), source.Days[0].Bars...)}}
			tc.mutate(&input)
			ref := write(t, input)
			days, pins, err := loadDays(context.Background(), "validation", symbol, months, []minuteFile{ref})
			if err == nil || !strings.Contains(err.Error(), tc.want) || days != nil || pins != nil {
				t.Fatalf("invalid input returned data: %v, %v, %v", days, pins, err)
			}
		})
	}
}
