package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/observespread"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
)

type spreadManifest struct {
	Version    int             `json:"version"`
	Inspection time.Time       `json:"inspection_at"`
	Files      []studyArtifact `json:"snapshot_files"`
}

func runSpread(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("quote-spread-check", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "本番スナップショットのhashと検査時刻を固定したmanifest（オフライン）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *input == "" {
		return errors.New("quote-spread-checkにはinput manifestを指定してください")
	}
	raw, err := readInput(*input)
	if err != nil {
		return err
	}
	var m spreadManifest
	if strictJSON(raw, &m) != nil || m.Version != 1 || m.Inspection.IsZero() || m.Inspection.After(time.Now().UTC()) || len(m.Files) == 0 || len(m.Files) > 10000 {
		return errors.New("気配観測manifestの件数/版/検査時刻を確認してください")
	}
	rows := []observespread.Input{}
	files := map[string]bool{}
	digests := map[string]bool{}
	for _, f := range m.Files {
		if ctx.Err() != nil {
			return errors.New("保存気配の読込を中断しました")
		}
		path := relativeORBPath(*input, f.Path)
		absolute, err := filepath.Abs(path)
		if err != nil || files[absolute] || digests[f.SHA] {
			return errors.New("気配観測の原ファイルが重複しています")
		}
		files[absolute] = true
		digests[f.SHA] = true
		body, err := pinnedInput(path, f.SHA)
		if err != nil {
			return err
		}
		var snapshot struct {
			Version     int    `json:"schema_version"`
			Environment string `json:"environment"`
			Complete    bool   `json:"complete"`
			Records     []struct {
				Endpoint string          `json:"endpoint"`
				Received time.Time       `json:"received_at"`
				Data     json.RawMessage `json:"data"`
				Error    string          `json:"error"`
			} `json:"records"`
		}
		if json.Unmarshal(body, &snapshot) != nil || snapshot.Version != 1 || snapshot.Environment != "production" || len(snapshot.Records) > 54 {
			return errors.New("気配観測は保存済み本番スナップショットに限定しています")
		}
		counts := map[string]int{}
		for _, record := range snapshot.Records {
			if strings.HasPrefix(record.Endpoint, "/board/") {
				counts[record.Endpoint]++
			}
		}
		for _, record := range snapshot.Records {
			if !strings.HasPrefix(record.Endpoint, "/board/") {
				continue
			}
			symbol := strings.TrimPrefix(record.Endpoint, "/board/")
			q, decodeErr := marketdata.KabuBoardQuote(record.Data, symbol, "production", record.Received)
			if decodeErr != nil {
				// Keep the failed observation's identity and hash, never the error
				// body or account records. The pure analyzer still checks identity.
				q = marketdata.Quote{Version: marketdata.QuoteVersion, Symbol: symbol, Environment: "production", Origin: "observed", ReceivedAt: record.Received, SourceSHA256: hash(record.Data)}
			}
			rows = append(rows, observespread.Input{Quote: q, SnapshotSHA: f.SHA, RecordIssue: !snapshot.Complete || record.Error != "" || decodeErr != nil || counts[record.Endpoint] != 1})
			if len(rows) > 100000 {
				return errors.New("気配観測の件数上限を超えました")
			}
		}
	}
	report, err := observespread.Analyze(ctx, rows, m.Inspection)
	if err != nil {
		return err
	}
	path, body, err := save("quote-spread-observation", report)
	if err != nil {
		return err
	}
	record, err := recordRun("quote-spread-check", filepath.Base(*input), raw, hash(raw), path, body, m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "保存気配の観測: snapshots=%d / samples=%d / structurally_valid=%d / fresh_at_receipt_2s=%d / groups=%d / execution_included=false / strategy_forward_test=false\n保存: %s\n実行記録: %s\n", len(m.Files), report.Samples, report.StructurallyValid, report.Fresh2, len(report.Groups), path, record)
	return err
}
