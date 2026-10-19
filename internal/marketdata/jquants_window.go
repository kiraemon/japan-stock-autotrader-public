package marketdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"
)

const JQuantsWindowVersion = "jquants-daily-matched-fixed-window-v0.1"

type JQuantsWindow struct {
	Version         string   `json:"version"`
	Date            string   `json:"date_jst"`
	Symbol          string   `json:"symbol"`
	Source          Source   `json:"minute_source"`
	DailySHA        string   `json:"daily_reference_sha256"`
	WindowStart     string   `json:"window_start_jst"`
	WindowLast      string   `json:"window_last_bar_start_jst"`
	Required        int      `json:"required_minutes"`
	Observed        int      `json:"observed_minutes"`
	Missing         []string `json:"missing_minutes_jst"`
	LeadingAssumed  []string `json:"leading_minutes_assumed_no_trades"`
	DailyMatch      bool     `json:"same_provider_daily_ohlcv_match"`
	TurnoverMatch   *bool    `json:"same_provider_daily_turnover_match,omitempty"`
	Eligible        bool     `json:"eligible_for_price_diagnostic"`
	Reasons         []string `json:"reasons"`
	OriginalIssues  []string `json:"original_day_issues"`
	Limitations     []string `json:"limitations"`
	QualityPromoted bool     `json:"quality_promoted"`
	TradingEligible bool     `json:"trading_eligible"`
}

// CheckJQuantsWindow is an ex-post input check, never a pre-trade feature or
// market-quality promotion. It recalculates daily OHLCV including boundary
// auctions and requires the fixed 09:00-10:50 window regardless of early exits.
// Only a leading absence ending by 09:04 follows the already explicit provider
// no-trades assumption; no rows are filled or added to the caller's data.
func CheckJQuantsWindow(ctx context.Context, d Dataset, date string, daily DailyBar) (JQuantsWindow, error) {
	return checkJQuantsWindow(ctx, d, date, daily, false)
}

// CheckJQuantsTurnoverWindow additionally reconciles original Va with daily Va;
// callers cannot relabel this extended contract as the legacy OHLCV source.
func CheckJQuantsTurnoverWindow(ctx context.Context, d Dataset, date string, daily DailyBar) (JQuantsWindow, error) {
	return checkJQuantsWindow(ctx, d, date, daily, true)
}

func checkJQuantsWindow(ctx context.Context, d Dataset, date string, daily DailyBar, turnover bool) (JQuantsWindow, error) {
	r := JQuantsWindow{Version: JQuantsWindowVersion, Date: date, Symbol: d.Symbol, Source: d.Source, WindowStart: "09:00", WindowLast: "10:50", Required: 111, Missing: []string{}, LeadingAssumed: []string{}, Reasons: []string{}, OriginalIssues: []string{}, Limitations: []string{"ex_post_input_check_not_pretrade_information", "same_provider_daily_aggregation_not_independent_verification", "leading_absence_assumed_no_trades_only_in_opening_prefix", "full_fixed_window_required_independent_of_trade_result", "unadjusted_prices_and_volume", "minute_open_fills_and_intrabar_stop_prices_assumed", "historical_receive_times_trade_status_inventory_and_actual_fills_unverified"}}
	formatOK := d.Source.Format == "jquants-bulk-minute-v0.1" || d.Source.Format == JQuantsJoinedFormat
	if turnover {
		formatOK = IsJQuantsTurnover(d.Source.Format)
		r.Version = "jquants-turnover-daily-matched-fixed-window-v0.1"
		r.Limitations = append(r.Limitations, "original_turnover_exact_not_typical_price_approximation")
	}
	if Validate(d) != nil || d.Origin != "observed" || d.Environment != "production" || d.Source.Label != "start" || !formatOK || !strings.HasSuffix(d.Symbol, "@1") {
		return r, errors.New("固定窓の価格診断は未昇格のJ-Quants一括分足に限定しています")
	}
	at, err := time.Parse("2006-01-02", date)
	if err != nil || at.Format("2006-01-02") != date || at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
		return r, errors.New("価格診断の営業日を確認してください")
	}
	if ctx.Err() != nil {
		return r, errors.New("価格診断を中断しました")
	}
	body, _ := json.Marshal(daily)
	sum := sha256.Sum256(body)
	r.DailySHA = hex.EncodeToString(sum[:])
	add := func(reason string) {
		for _, known := range r.Reasons {
			if known == reason {
				return
			}
		}
		r.Reasons = append(r.Reasons, reason)
	}
	var day *Day
	for i := range d.Days {
		if d.Days[i].Date == date {
			day = &d.Days[i]
			break
		}
	}
	if day == nil {
		add("target_minute_day_missing")
		return r, nil
	}
	r.OriginalIssues = append(r.OriginalIssues, day.Issues...)
	for _, bar := range day.Bars {
		if bar.Start.Add(time.Minute).After(d.Source.ReceivedAt) {
			add("daily_aggregation_input_unfinished")
		}
	}
	for _, event := range day.Events {
		if event.Time.After(d.Source.ReceivedAt) {
			add("daily_aggregation_input_unfinished")
		}
	}
	for _, issue := range day.Issues {
		switch issue {
		case "coverage_unverified", "trading_status_unverified", "source_crosscheck_unverified", "corporate_actions_unverified":
		default:
			add("unsupported_day_quality_issue")
		}
	}
	if daily.Date != date || daily.Code != strings.TrimSuffix(d.Symbol, "@1")+"0" || !daily.HasTrades || !ValidPrices(Bar{Open: daily.Open, High: daily.High, Low: daily.Low, Close: daily.Close}) || daily.Volume < 0 {
		add("daily_reference_unavailable_or_invalid")
	} else {
		if daily.AdjustmentFactor != "1" || daily.ExRights != "" {
			add("current_day_action_basis_unverified")
		}
		match, err := jquantsDailyMatch(ctx, *day, daily)
		if err != nil {
			return r, err
		}
		r.DailyMatch = match
		if !match {
			add("same_provider_daily_ohlcv_mismatch")
		}
		if turnover {
			sum := new(big.Rat)
			for _, b := range day.Bars {
				v, _ := new(big.Rat).SetString(b.TurnoverJPY)
				sum.Add(sum, v)
			}
			for _, e := range day.Events {
				v, _ := new(big.Rat).SetString(e.TurnoverJPY)
				sum.Add(sum, v)
			}
			var expected *big.Rat
			ok := false
			if len(daily.Turnover) > 0 && len(daily.Turnover) <= 128 && turnoverExactPattern.MatchString(daily.Turnover) {
				expected, ok = new(big.Rat).SetString(daily.Turnover)
			}
			matched := ok && expected.Sign() > 0 && sum.Cmp(expected) == 0
			r.TurnoverMatch = &matched
			if !matched {
				add("same_provider_daily_turnover_mismatch")
			}
		}
	}
	byTime := map[time.Time]Bar{}
	for _, bar := range day.Bars {
		byTime[bar.Start.UTC()] = bar
	}
	first := 111
	start := Clock(date, 9, 0)
	for minute := 0; minute < 111; minute++ {
		if ctx.Err() != nil {
			return r, errors.New("価格診断を中断しました")
		}
		clock := start.Add(time.Duration(minute) * time.Minute)
		bar, exists := byTime[clock.UTC()]
		if !exists {
			r.Missing = append(r.Missing, clock.Format("15:04"))
			continue
		}
		if first == 111 {
			first = minute
		}
		r.Observed++
		if len(bar.Issues) > 0 {
			add("unsupported_minute_quality_issue")
		}
		if bar.Volume == nil || *bar.Volume <= 0 {
			add("minute_volume_unknown_or_zero")
		}
		if clock.Add(time.Minute).After(d.Source.ReceivedAt) {
			add("fixed_window_minute_unfinished")
		}
	}
	for _, missing := range r.Missing {
		clock, _ := time.Parse("15:04", missing)
		minute := clock.Hour()*60 + clock.Minute() - 9*60
		if first <= 4 && minute < first {
			r.LeadingAssumed = append(r.LeadingAssumed, missing)
		} else {
			add("fixed_window_required_minute_missing")
		}
	}
	r.Eligible = r.DailyMatch && len(r.Reasons) == 0 && r.Observed+len(r.LeadingAssumed) == r.Required
	return r, nil
}

func jquantsDailyMatch(ctx context.Context, day Day, daily DailyBar) (bool, error) {
	type point struct {
		at                     time.Time
		open, high, low, close int64
		volume                 *int64
	}
	points := []point{}
	for _, bar := range day.Bars {
		points = append(points, point{bar.Start, bar.Open, bar.High, bar.Low, bar.Close, bar.Volume})
	}
	if len(day.Events) > 2 {
		return false, nil
	}
	seen := map[time.Time]bool{}
	for _, event := range day.Events {
		clock := event.Time.In(JST).Format("15:04:05")
		key := event.Time.UTC()
		if Date(event.Time) != day.Date || !event.Time.Equal(event.Time.Truncate(time.Minute)) || (clock != "11:30:00" && clock != "15:30:00") || seen[key] || event.Price <= 0 || event.Price > MaxPrice || event.Volume == nil || *event.Volume < 0 {
			return false, nil
		}
		seen[key] = true
		points = append(points, point{event.Time, event.Price, event.Price, event.Price, event.Price, event.Volume})
	}
	if len(points) == 0 {
		return false, nil
	}
	sort.Slice(points, func(i, j int) bool { return points[i].at.Before(points[j].at) })
	first, last := points[0], points[len(points)-1]
	high, low := first.high, first.low
	volume := new(big.Int)
	for _, p := range points {
		if ctx.Err() != nil {
			return false, errors.New("日次集計の照合を中断しました")
		}
		if p.volume == nil || *p.volume < 0 {
			return false, nil
		}
		volume.Add(volume, big.NewInt(*p.volume))
		if p.high > high {
			high = p.high
		}
		if p.low < low {
			low = p.low
		}
	}
	return first.open == daily.Open && high == daily.High && low == daily.Low && last.close == daily.Close && volume.Cmp(big.NewInt(daily.Volume)) == 0, nil
}
