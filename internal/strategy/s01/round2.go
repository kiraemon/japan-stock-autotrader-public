package s01

import (
	"errors"
	"math/big"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

// Scope was fixed before candidate PnL. There are no cross-product variants.
const Round2ScopeSHA256 = "2027976d0dfa07554e718b81d4a1b58cf1f544b3ab3b12e09dd41935979bd0ad"

type Round2Policy struct {
	ID           string `json:"id"`
	ScopeSHA     string `json:"scope_plan_sha256"`
	StopFraction string `json:"atr_stop_fraction_exact"`
	MinimumRVOL  string `json:"minimum_rvol_exact"`
}

func Round2Rule(id string) (Round2Policy, error) {
	p := Round2Policy{ID: id, ScopeSHA: Round2ScopeSHA256, StopFraction: "1/10", MinimumRVOL: "1"}
	switch id {
	case "R2-P0":
	case "R2-P1":
		p.StopFraction = "1/5"
	case "R2-P2":
		p.MinimumRVOL = "3/2"
	default:
		return Round2Policy{}, errors.New("第2回研究の固定した3候補から指定してください")
	}
	return p, nil
}

// Round2StopDistance rounds the exact fraction of ATR once. Doubling an
// already rounded legacy distance would produce a different rule.
func Round2StopDistance(id, atrScaledExact string) (int64, error) {
	p, err := Round2Rule(id)
	if err != nil {
		return 0, err
	}
	atr, ok := Exact(atrScaledExact)
	if !ok || atr.Sign() <= 0 || atr.Cmp(big.NewRat(marketdata.MaxPrice, 1)) > 0 {
		return 0, errors.New("固定した正のATRと価格スケールを確認してください")
	}
	fraction, _ := Exact(p.StopFraction)
	distance := new(big.Rat).Mul(atr, fraction)
	q := new(big.Int).Add(distance.Num(), new(big.Int).Sub(distance.Denom(), big.NewInt(1)))
	q.Quo(q, distance.Denom())
	if !q.IsInt64() || q.Sign() <= 0 || q.Int64() > marketdata.MaxPrice {
		return 0, errors.New("研究用の損切り距離が範囲外です")
	}
	return q.Int64(), nil
}
