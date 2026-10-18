package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/credentials"
	"github.com/kiraemon/japan-stock-autotrader/internal/instanceguard"
	"github.com/kiraemon/japan-stock-autotrader/internal/jquantsbulk"
	"github.com/kiraemon/japan-stock-autotrader/internal/jquantsrate"
)

func runBulk(ctx context.Context, root, month string, listOnly, calendarOnly bool, out io.Writer) error {
	if err := jquantsbulk.ValidateMonth(month); err != nil {
		return err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return errors.New("プロジェクトのパスを確認してください")
	}
	gate, err := instanceguard.Acquire("Global\\JapanStockAutotrader.JQuantsCapture.v1")
	if err != nil {
		return errors.New("J-Quants取得が起動中、またはロックを取得できません")
	}
	defer gate.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	lease, err := jquantsrate.Start(ctx, filepath.Join(absolute, ".cache", "jquants-rate.json"), 10*time.Minute)
	if err != nil {
		return err
	}
	rateLimited := false
	defer func() {
		if lease.Finish(rateLimited) != nil {
			fmt.Fprintln(out, "送信間隔記録の更新失敗。次回起動は予約時刻まで停止します")
		}
	}()
	key, err := credentials.JQuants(ctx, absolute)
	if err != nil {
		return err
	}
	directory := filepath.Join(absolute, "data", "jquants", "bulk")
	r, captureErr := jquantsbulk.Capture(ctx, key, month, directory, listOnly, calendarOnly)
	rateLimited = r.LastStatus == 429
	key = ""
	if os.MkdirAll(directory, 0700) != nil {
		return errors.New("bulk記録を保存できません")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return errors.New("bulk記録を作成できません")
	}
	f, err := os.CreateTemp(directory, ".bulk-run-*.tmp")
	if err != nil {
		return errors.New("bulk記録を保存できません")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("bulk記録の保存が未完了です")
	}
	base := filepath.Base(f.Name())
	final := filepath.Join(directory, base[1:len(base)-4]+".json")
	if os.Rename(f.Name(), final) != nil {
		return errors.New("bulk記録を確定できません")
	}
	var total int64
	for _, f := range r.Files {
		total += f.Size
	}
	_, err = fmt.Fprintf(out, "J-Quants bulk: month=%s / complete=%t / list_only=%t / calendar_only=%t / files=%d / compressed_bytes=%d / api_requests=%d / last_status=%d\n保存: %s\n", month, r.Complete, listOnly, calendarOnly, len(r.Files), total, r.Requests, r.LastStatus, final)
	if captureErr != nil {
		return captureErr
	}
	return err
}
