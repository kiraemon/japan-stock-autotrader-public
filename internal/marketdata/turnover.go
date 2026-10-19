package marketdata

import (
	"errors"
	"math/big"
	"regexp"
	"time"
)

const JQuantsTurnoverFormat = "jquants-bulk-minute-turnover-v0.1"
const JQuantsTurnoverJoinedFormat = "jquants-bulk-minute-turnover-joined-v0.1"

var turnoverExactPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(/[1-9][0-9]*)?$`)

// SourceExclusion counts selected-symbol CSV rows outside this converter's
// supported session contract. Original gzip bytes and row positions are kept.
type SourceExclusion struct {
	Date   string `json:"date_jst"`
	Rows   int    `json:"selected_symbol_rows"`
	Reason string `json:"reason"`
}

func IsJQuantsTurnover(format string) bool {
	return format == JQuantsTurnoverFormat || format == JQuantsTurnoverJoinedFormat
}

// ParseTurnover canonicalizes the provider's decimal yen without rounding or
// approximating price*volume. The normalized representation is an exact ratio.
func ParseTurnover(value string) (string, error) {
	r, ok := decimal(value)
	if !ok || r.Sign() < 0 {
		return "", errors.New("売買代金の非負の円数を確認してください")
	}
	return r.RatString(), nil
}

func ValidTurnover(value string, volume *int64, low, high int64) bool {
	if len(value) == 0 || len(value) > 128 || !turnoverExactPattern.MatchString(value) || volume == nil || *volume < 0 || low <= 0 || high < low || high > MaxPrice {
		return false
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok || r.Sign() < 0 || r.RatString() != value {
		return false
	}
	r.Mul(r, big.NewRat(PriceScale, 1))
	lower := new(big.Int).Mul(big.NewInt(low), big.NewInt(*volume))
	upper := new(big.Int).Mul(big.NewInt(high), big.NewInt(*volume))
	return r.Cmp(new(big.Rat).SetInt(lower)) >= 0 && r.Cmp(new(big.Rat).SetInt(upper)) <= 0
}

func validateTurnoverContract(d Dataset) error {
	extended := IsJQuantsTurnover(d.Source.Format)
	fail := func() error {
		return errors.New("売買代金付き分足の出典/数量/価格範囲/除外記録を確認してください")
	}
	if !extended && len(d.ExcludedRows) > 0 {
		return fail()
	}
	if extended && (d.Origin != "observed" || d.Environment != "production" || d.Source.Label != "start") {
		return fail()
	}
	previous := ""
	for _, x := range d.ExcludedRows {
		if x.Date != "2024-11-01" || x.Date <= previous || x.Rows <= 0 || x.Rows > 20000 || x.Reason != "session_before_supported_20241105" {
			return fail()
		}
		previous = x.Date
	}
	if len(d.ExcludedRows) > 1 {
		return fail()
	}
	for _, day := range d.Days {
		if extended && day.Date < "2024-11-05" {
			return fail()
		}
		for _, b := range day.Bars {
			if extended && b.TurnoverJPY == "" || b.TurnoverJPY != "" && (!extended || !ValidTurnover(b.TurnoverJPY, b.Volume, b.Low, b.High)) {
				return fail()
			}
		}
		seen := map[time.Time]bool{}
		for _, e := range day.Events {
			if extended {
				clock := e.Time.In(JST).Format("15:04:05")
				if Date(e.Time) != day.Date || !e.Time.Equal(e.Time.Truncate(time.Minute)) || (clock != "11:30:00" && clock != "15:30:00") || seen[e.Time.UTC()] || !ValidTurnover(e.TurnoverJPY, e.Volume, e.Price, e.Price) {
					return fail()
				}
				seen[e.Time.UTC()] = true
			} else if e.TurnoverJPY != "" {
				return fail()
			}
		}
		if extended && len(day.Events) > 2 {
			return fail()
		}
	}
	return nil
}
