package buildbars

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func jqFixture() map[string]any {
	row := func(clock string) map[string]any {
		return map[string]any{"Date": "2026-06-22", "Time": clock, "Code": "00000", "O": 100, "H": 102, "L": 99, "C": 101, "Vo": 10, "Va": 1000}
	}
	a, b := row("09:01"), row("09:00:00")
	edge := row("11:30")
	edge["O"], edge["H"], edge["L"], edge["C"] = 100, 100, 100, 100
	return map[string]any{"version": "jquants-cli-capture-v0.1", "provider": "jquants", "cli_version": "1.6.0", "request_stats": map[string]any{"api_requests": 1}, "complete": true, "results": []any{map[string]any{"code": "0000", "from": "2026-06-22", "to": "2026-06-22", "started_at": "2026-06-23T00:00:00Z", "captured_at": "2026-06-23T00:00:01Z", "data": []any{a, edge, b}}}}
}

func TestJQuantsConvertsUnorderedMinutesAndSeparatesAuctions(t *testing.T) {
	a := jqFixture()
	raw, _ := json.Marshal(a)
	d, err := FromJQuantsCLI(context.Background(), raw, "0000@1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Days) != 1 || len(d.Days[0].Bars) != 2 || len(d.Days[0].Events) != 1 || d.Days[0].Bars[0].Start.Hour() != 9 || d.Days[0].Bars[0].Start.Minute() != 0 || *d.Days[0].Bars[0].Volume != 10 || d.Days[0].Bars[0].Open != 1000000 || len(d.Days[0].Issues) == 0 || d.Source.SHA256 != sourceHash(raw) || d.Origin != "observed" {
		t.Fatal("normalization or provenance changed")
	}
	if !strings.Contains(d.Source.Format, "jquants") {
		t.Fatal("source disguised as kabu")
	}
}

func TestJQuantsRefusesIncompleteDuplicateUnknownAndInvalidRows(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(a map[string]any) { a["complete"] = false },
		func(a map[string]any) { a["quality_override"] = true },
		func(a map[string]any) { a["results"].([]any)[0].(map[string]any)["from"] = "2024-10-01" },
		func(a map[string]any) {
			r := a["results"].([]any)[0].(map[string]any)
			rows := r["data"].([]any)
			r["data"] = append(rows, rows[0])
		},
		func(a map[string]any) {
			a["results"].([]any)[0].(map[string]any)["data"].([]any)[0].(map[string]any)["Code"] = "11110"
		},
		func(a map[string]any) {
			a["results"].([]any)[0].(map[string]any)["data"].([]any)[0].(map[string]any)["Time"] = "12:00"
		},
		func(a map[string]any) {
			a["results"].([]any)[0].(map[string]any)["data"].([]any)[0].(map[string]any)["Vo"] = nil
		},
		func(a map[string]any) {
			a["results"].([]any)[0].(map[string]any)["data"].([]any)[1].(map[string]any)["H"] = 102
		},
	} {
		a := jqFixture()
		mutate(a)
		raw, _ := json.Marshal(a)
		if _, err := FromJQuantsCLI(context.Background(), raw, "0000@1"); err == nil {
			t.Fatal("bad capture admitted")
		}
	}
	a := jqFixture()
	raw, _ := json.Marshal(a)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FromJQuantsCLI(ctx, raw, "0000@1"); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := FromJQuantsCLI(context.Background(), raw, "1111@1"); err == nil {
		t.Fatal("unknown requested symbol admitted")
	}
}
