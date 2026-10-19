// Package marketdata owns the small, versioned contract shared by conversion,
// strategy evaluation, and offline replay. It contains no broker operations.
package marketdata

import (
	"errors"
	"math/big"
	"regexp"
	"time"
)

const PriceScale int64 = 10000
const JQuantsJoinedFormat = "jquants-bulk-minute-joined-v0.1"
const MaxPrice int64 = 1_000_000_000_000

var JST = time.FixedZone("JST", 9*3600)
var symbolPattern = regexp.MustCompile(`^[0-9A-Z]{4}@(1|3|5|6)$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]{1,2})?$`)

type Source struct {
	Format     string    `json:"format"`
	SHA256     string    `json:"sha256"`
	Label      string    `json:"time_label"`
	ReceivedAt time.Time `json:"received_at"`
}
type Dataset struct {
	Version      int               `json:"schema_version"`
	PriceScale   int64             `json:"price_scale"`
	Symbol       string            `json:"symbol"`
	Environment  string            `json:"environment"`
	Origin       string            `json:"origin"` // synthetic or observed; never a quality override.
	Converter    string            `json:"converter_version"`
	Source       Source            `json:"source"`
	Days         []Day             `json:"days"`
	ExcludedRows []SourceExclusion `json:"excluded_source_rows,omitempty"`
}
type Day struct {
	Date   string   `json:"date_jst"`
	Issues []string `json:"issues"`
	Bars   []Bar    `json:"bars"`
	Events []Event  `json:"boundary_events"`
}
type Bar struct {
	Start       time.Time `json:"start"`
	Open        int64     `json:"open"`
	High        int64     `json:"high"`
	Low         int64     `json:"low"`
	Close       int64     `json:"close"`
	Volume      *int64    `json:"volume_shares"`
	Issues      []string  `json:"issues"`
	FirstRow    int       `json:"first_source_row"`
	LastRow     int       `json:"last_source_row"`
	FirstTime   time.Time `json:"first_market_time"`
	LastTime    time.Time `json:"last_market_time"`
	TurnoverJPY string    `json:"turnover_jpy_exact,omitempty"`
}
type Event struct {
	Time        time.Time `json:"time"`
	Price       int64     `json:"price"`
	Volume      *int64    `json:"volume_shares"`
	Row         int       `json:"source_row"`
	TurnoverJPY string    `json:"turnover_jpy_exact,omitempty"`
}

// ParsePrice refuses rounding; comparisons use a common integer scale.
func ParsePrice(s string) (int64, error) {
	v, ok := decimal(s)
	if !ok {
		return 0, errors.New("価格の形式を確認してください")
	}
	v.Mul(v, big.NewRat(PriceScale, 1))
	if !v.IsInt() || !v.Num().IsInt64() || v.Sign() <= 0 || v.Num().Int64() > MaxPrice {
		return 0, errors.New("価格は正数・小数4桁以内の範囲で指定してください")
	}
	return v.Num().Int64(), nil
}
func ParseVolume(s string) (*int64, error) {
	v, ok := decimal(s)
	if !ok || !v.IsInt() || !v.Num().IsInt64() || v.Sign() < 0 {
		return nil, errors.New("出来高の株数を確認してください")
	}
	n := v.Num().Int64()
	return &n, nil
}
func decimal(s string) (*big.Rat, bool) {
	if len(s) > 40 || !decimalPattern.MatchString(s) {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}
func Date(t time.Time) string { return t.In(JST).Format("2006-01-02") }
func Clock(day string, hour, minute int) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", day, JST)
	return t.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}
func RegularMinute(t time.Time) bool {
	t = t.In(JST)
	m := t.Hour()*60 + t.Minute()
	return (m >= 9*60 && m < 11*60+30) || (m >= 12*60+30 && m < 15*60+25)
}
func Validate(d Dataset) error {
	if d.Version != 1 || d.PriceScale != PriceScale || !symbolPattern.MatchString(d.Symbol) || (d.Environment != "production" && d.Environment != "test") || (d.Origin != "observed" && d.Origin != "synthetic") || len(d.Days) == 0 || len(d.Days) > 3660 {
		return errors.New("分足データの版・銘柄・環境・日数を確認してください")
	}
	if !shaPattern.MatchString(d.Source.SHA256) || d.Source.ReceivedAt.IsZero() || d.Converter == "" || (d.Source.Label != "start" && d.Source.Label != "end") || (d.Origin == "synthetic" && d.Source.Format != "synthetic") {
		return errors.New("分足の出典・ハッシュ・変換版を確認してください")
	}
	previousDay := ""
	for _, day := range d.Days {
		date, err := time.ParseInLocation("2006-01-02", day.Date, JST)
		if err != nil || day.Date <= previousDay || Date(date) != day.Date || len(day.Bars) > 325 {
			return errors.New("分足の日付・順序・件数を確認してください")
		}
		previousDay = day.Date
		var previous time.Time
		for _, b := range day.Bars {
			if Date(b.Start) != day.Date || !b.Start.Equal(b.Start.Truncate(time.Minute)) || !RegularMinute(b.Start) || (!previous.IsZero() && !b.Start.After(previous)) || !ValidPrices(b) || (b.Volume != nil && *b.Volume < 0) {
				return errors.New("分足の時刻・順序・OHLC・数量を確認してください")
			}
			previous = b.Start
		}
	}
	return validateTurnoverContract(d)
}
func ValidPrices(b Bar) bool {
	return b.Low > 0 && b.Low <= b.Open && b.Low <= b.Close && b.High >= b.Open && b.High >= b.Close && b.High <= MaxPrice
}
