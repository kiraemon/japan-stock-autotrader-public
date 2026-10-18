package marketdata

import (
	"encoding/json"
	"testing"
	"time"
)

func calendarFixture() map[string]any {
	rows := []map[string]string{}
	for day := 1; day <= 30; day++ {
		at := time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC)
		division := "1"
		if at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
			division = "0"
		}
		if day == 8 {
			division = "3"
		}
		rows = append(rows, map[string]string{"Date": at.Format("2006-01-02"), "HolDiv": division})
	}
	return map[string]any{"data": rows}
}

func TestCalendarRequiresAllDatesAndExcludesOSEOnlyHoliday(t *testing.T) {
	fixture := calendarFixture()
	raw, _ := json.Marshal(fixture)
	sessions, err := JQuantsSessions(raw, "2026-06")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 21 || sessions[0] != "2026-06-01" || sessions[len(sessions)-1] != "2026-06-30" {
		t.Fatal("wrong equity sessions")
	}
	for _, day := range sessions {
		if day == "2026-06-08" {
			t.Fatal("OSE-only holiday treated as TSE equity session")
		}
	}
	for _, change := range []func(map[string]any){
		func(a map[string]any) { a["data"] = a["data"].([]map[string]string)[1:] },
		func(a map[string]any) { r := a["data"].([]map[string]string); r[1]["Date"] = r[0]["Date"] },
		func(a map[string]any) { a["data"].([]map[string]string)[0]["HolDiv"] = "2" },
		func(a map[string]any) { a["data"].([]map[string]string)[0]["HolDiv"] = "9" },
		func(a map[string]any) { a["data"].([]map[string]string)[5]["HolDiv"] = "1" },
	} {
		a := calendarFixture()
		change(a)
		raw, _ := json.Marshal(a)
		if _, err := JQuantsSessions(raw, "2026-06"); err == nil {
			t.Fatal("invalid calendar admitted")
		}
	}
}
