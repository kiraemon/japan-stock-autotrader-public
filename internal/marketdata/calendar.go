package marketdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"
)

// JQuantsSessions validates a complete calendar month. OSE-only holiday trading
// is not a TSE equity session; half-day sessions need a separate bar contract.
func JQuantsSessions(raw []byte, month string) ([]string, error) {
	first, err := time.Parse("2006-01", month)
	if err != nil || first.Format("2006-01") != month {
		return nil, errors.New("カレンダー対象月を確認してください")
	}
	var response struct {
		Data []struct {
			Date   string `json:"Date"`
			HolDiv string `json:"HolDiv"`
		} `json:"data"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 1<<20 || d.Decode(&response) != nil || d.Decode(new(any)) != io.EOF || len(response.Data) != first.AddDate(0, 1, -1).Day() {
		return nil, errors.New("全暦日を含む公式カレンダーJSONを確認してください")
	}
	seen := map[string]bool{}
	sessions := []string{}
	for _, row := range response.Data {
		at, err := time.Parse("2006-01-02", row.Date)
		if err != nil || at.Format("2006-01-02") != row.Date || at.Format("2006-01") != month || seen[row.Date] {
			return nil, errors.New("カレンダーの日付/重複を確認してください")
		}
		seen[row.Date] = true
		switch row.HolDiv {
		case "0", "3":
		case "1":
			if at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
				return nil, errors.New("東証営業日の曜日を確認してください")
			}
			sessions = append(sessions, row.Date)
		default:
			return nil, errors.New("未知または未対応の半日立会区分を確認してください")
		}
	}
	sort.Strings(sessions)
	return sessions, nil
}
