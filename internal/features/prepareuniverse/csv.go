// Package prepareuniverse converts saved daily/master inputs and selects the
// fixed research universe. It has no network, credentials, orders, or P&L.
package prepareuniverse

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
)

const dailyHeader = "Date,Code,O,H,L,C,UL,LL,Vo,Va,AdjFactor,MktCap,ExRT"
const masterHeader = "Date,Code,CoName,CoNameEn,S17,S17Nm,S33,S33Nm,ScaleCat,Mkt,MktNm,Mrgn,MrgnNm,ProdCat"

var codePattern = regexp.MustCompile(`^[0-9A-Z]{4}[0-9]$`)
var numberPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]{1,2})?$`)

type Listing struct{ Date, Code, Market, Product, Margin string }

func positive(value string) (string, error) {
	if len(value) > 40 || !numberPattern.MatchString(value) {
		return "", errors.New("日次の数値形式を確認してください")
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok || r.Sign() <= 0 {
		return "", errors.New("日次の正数を確認してください")
	}
	return r.RatString(), nil
}
func null(value string) bool { return value == "" || value == "null" }
func validDate(date string) bool {
	at, err := time.Parse("2006-01-02", date)
	return err == nil && at.Format("2006-01-02") == date
}

// A physical-line cap bounds csv.Reader's allocation even for corrupt quotes.
// Quoted multiline records are supported, up to the same per-line bound.
type lineBound struct {
	input io.Reader
	width int
}

func (r *lineBound) Read(p []byte) (int, error) {
	n, err := r.input.Read(p)
	for _, b := range p[:n] {
		if b == '\n' {
			r.width = 0
		} else {
			r.width++
		}
		if r.width > 64<<10 {
			return 0, errors.New("CSV行の上限を超えました")
		}
	}
	return n, err
}

func rows(ctx context.Context, input io.Reader, month, header string, receive func([]string) error) error {
	at, err := time.Parse("2006-01", month)
	if err != nil || at.Format("2006-01") != month || month < "2024-12" {
		return errors.New("日次入力のYYYY-MMを確認してください")
	}
	limited := &io.LimitedReader{R: input, N: (256 << 20) + 1}
	r := csv.NewReader(&lineBound{input: limited})
	r.ReuseRecord = true
	head, err := r.Read()
	if err != nil || strings.Join(head, ",") != header {
		return errors.New("日次CSVの見出しを確認してください")
	}
	seen := map[string]bool{}
	count := 0
	for {
		if ctx.Err() != nil {
			return errors.New("日次CSVの変換を中断しました")
		}
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || limited.N <= 0 || count >= 200000 {
			return errors.New("日次CSVの構造/容量/件数を確認してください")
		}
		if !validDate(row[0]) || !strings.HasPrefix(row[0], month+"-") || !codePattern.MatchString(row[1]) {
			return errors.New("日次CSVの日付/銘柄を確認してください")
		}
		key := row[0] + "/" + row[1]
		if seen[key] {
			return errors.New("日次CSVの日付/銘柄が重複しています")
		}
		seen[key] = true
		if err := receive(row); err != nil {
			return err
		}
		count++
	}
	if limited.N <= 0 || count == 0 {
		return errors.New("日次CSVの展開容量/空入力を確認してください")
	}
	return nil
}

func ReadDaily(ctx context.Context, input io.Reader, month string) ([]marketdata.DailyBar, error) {
	result := []marketdata.DailyBar{}
	err := rows(ctx, input, month, dailyHeader, func(row []string) error {
		d := marketdata.DailyBar{Date: strings.Clone(row[0]), Code: strings.Clone(row[1])}
		factor, err := positive(row[10])
		if err != nil {
			return err
		}
		d.AdjustmentFactor = factor
		if !null(row[12]) {
			if row[12] != "1" && row[12] != "2" && row[12] != "3" {
				return errors.New("日次の権利落種類を確認してください")
			}
			d.ExRights = strings.Clone(row[12])
		}
		for _, i := range []int{6, 7} {
			if !null(row[i]) && row[i] != "0" && row[i] != "1" {
				return errors.New("日次の制限値幅フラグを確認してください")
			}
		}
		missing := 0
		for _, i := range []int{2, 3, 4, 5, 8, 9} {
			if null(row[i]) {
				missing++
			}
		}
		if missing != 0 && missing != 6 {
			return errors.New("日次のOHLC/出来高/売買代金の一部だけが不明です")
		}
		if missing == 0 {
			prices := []*int64{&d.Open, &d.High, &d.Low, &d.Close}
			for i, dst := range prices {
				v, err := marketdata.ParsePrice(row[2+i])
				if err != nil {
					return errors.New("日次価格の形式を確認してください")
				}
				*dst = v
			}
			if !marketdata.ValidPrices(marketdata.Bar{Open: d.Open, High: d.High, Low: d.Low, Close: d.Close}) {
				return errors.New("日次OHLCの整合性を確認してください")
			}
			volume, err := marketdata.ParseVolume(row[8])
			if err != nil || *volume <= 0 {
				return errors.New("日次出来高の正の株数を確認してください")
			}
			d.Volume = *volume
			amount, err := positive(row[9])
			if err != nil {
				return err
			}
			d.Turnover = amount
			value, _ := new(big.Rat).SetString(amount)
			value.Mul(value, big.NewRat(marketdata.PriceScale, 1))
			lower := new(big.Int).Mul(big.NewInt(d.Low), big.NewInt(d.Volume))
			upper := new(big.Int).Mul(big.NewInt(d.High), big.NewInt(d.Volume))
			if value.Cmp(new(big.Rat).SetInt(lower)) < 0 || value.Cmp(new(big.Rat).SetInt(upper)) > 0 {
				return errors.New("日次の売買代金/株数/価格の整合性を確認してください")
			}
			d.HasTrades = true
		}
		result = append(result, d)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ReadListings(ctx context.Context, input io.Reader, month string) ([]Listing, error) {
	result := []Listing{}
	err := rows(ctx, input, month, masterHeader, func(row []string) error {
		result = append(result, Listing{Date: strings.Clone(row[0]), Code: strings.Clone(row[1]), Market: strings.Clone(row[9]), Product: strings.Clone(row[13]), Margin: strings.Clone(row[11])})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
