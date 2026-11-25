package backtest

import (
	"context"

	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/strategy/r001"
)

// DiagnoseJQuants does its own bounded provider/window/daily checks. Normal Run
// and the original kabu CSV/history crosscheck remain unchanged. In particular,
// a provider leading-absence assumption does not invent bars for the strict
// R001 scanner: such cases still remain unevaluable under the original rule.
func DiagnoseJQuants(ctx context.Context, d marketdata.Dataset, date string, daily marketdata.DailyBar, direction r001.Direction, variant r001.Variant) (Report, error) {
	check, err := marketdata.CheckJQuantsWindow(ctx, d, date, daily)
	if err != nil {
		return Report{}, err
	}
	subset := d
	subset.Days = []marketdata.Day{}
	for _, day := range d.Days {
		if day.Date == date {
			subset.Days = append(subset.Days, day)
			break
		}
	}
	if len(subset.Days) == 0 {
		subset.Days = []marketdata.Day{{Date: date, Issues: []string{"target_minute_day_missing"}}}
	}
	r, err := run(ctx, subset, direction, variant, map[string][]string{date: check.Reasons})
	if err != nil {
		return r, err
	}
	r.Version = 3
	r.Scope = "jquants_same_provider_price_pattern_diagnostic"
	r.JQuantsDiagnostic = &check
	return r, nil
}
