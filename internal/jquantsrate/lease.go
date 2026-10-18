// Package jquantsrate preserves a conservative inter-run cooldown. The caller
// holds the shared Windows account gate throughout Start, API work and Finish.
package jquantsrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const Interval = 1250 * time.Millisecond

type Lease struct{ path string }
type state struct {
	NotBefore time.Time `json:"not_before"`
}

func Start(ctx context.Context, path string, maximumRun time.Duration) (*Lease, error) {
	if maximumRun <= 0 || maximumRun > 10*time.Minute {
		return nil, errors.New("J-Quantsの実行時間上限を確認してください")
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		var s state
		if len(raw) > 4096 || json.Unmarshal(raw, &s) != nil || s.NotBefore.IsZero() {
			return nil, errors.New("J-Quantsの送信間隔記録を確認してください")
		}
		delay := time.Until(s.NotBefore)
		if delay > Interval {
			return nil, errors.New("J-Quantsは前回の停止/異常終了後の待機中です。送信間隔記録の時刻後に再開してください")
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return nil, errors.New("J-Quantsの起動待機を中断しました")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("J-Quantsの送信間隔記録を読み込めません")
	}
	lease := &Lease{path: path}
	// A killed process leaves a reservation beyond its deadline and a 429 pause.
	if err := lease.write(time.Now().Add(maximumRun + 2*time.Minute)); err != nil {
		return nil, err
	}
	return lease, nil
}

func (l *Lease) Finish(rateLimited bool) error {
	delay := Interval
	if rateLimited {
		delay = 2 * time.Minute
	}
	return l.write(time.Now().Add(delay))
}

func (l *Lease) write(notBefore time.Time) error {
	if os.MkdirAll(filepath.Dir(l.path), 0700) != nil {
		return errors.New("J-Quantsの送信間隔記録を作成できません")
	}
	raw, _ := json.Marshal(state{NotBefore: notBefore.UTC()})
	f, err := os.CreateTemp(filepath.Dir(l.path), ".rate-*.tmp")
	if err != nil {
		return errors.New("J-Quantsの送信間隔を予約できません")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || os.Rename(f.Name(), l.path) != nil {
		return errors.New("J-Quantsの送信間隔予約を確定できません")
	}
	return nil
}
