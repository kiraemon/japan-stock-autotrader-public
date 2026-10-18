package marketdata

// DailyBar carries unadjusted daily prices. HasTrades=false means unavailable
// prices/quantity/turnover, never a zero-price bar. Factor and ex-rights fields
// are retained for explicit point-in-time adjustment in later calculations.
type DailyBar struct {
	Date             string `json:"date_jst"`
	Code             string `json:"code"`
	HasTrades        bool   `json:"has_trades"`
	Open             int64  `json:"open_scaled"`
	High             int64  `json:"high_scaled"`
	Low              int64  `json:"low_scaled"`
	Close            int64  `json:"close_scaled"`
	Volume           int64  `json:"volume_shares"`
	Turnover         string `json:"turnover_jpy_exact,omitempty"`
	AdjustmentFactor string `json:"adjustment_factor_exact"`
	ExRights         string `json:"ex_rights,omitempty"`
}
