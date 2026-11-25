package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/buildbars"
	"github.com/kiraemon/japan-stock-trading-lab/internal/jquantsbulk"
)

func importJQuantsBulk(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("import-jquants-bulk", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("input", "", "完了したbulk-run JSON（オフライン専用）")
	symbols := fs.String("symbols", "7203@1,8306@1,6501@1", "抽出する東証銘柄、最大50")
	turnover := fs.Bool("with-turnover", false, "元Vaを厳密に保持する別形式。2024/11/1の対象銘柄行は対応外として件数を保存")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *input == "" {
		return errors.New("import-jquants-bulkにinputを指定してください")
	}
	raw, err := readInput(*input)
	if err != nil {
		return err
	}
	var r jquantsbulk.Report
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Version != jquantsbulk.Version || (r.Dataset != "" && r.Dataset != jquantsbulk.Minute) || !r.Complete || r.ListOnly || r.CalendarOnly || len(r.Files) != 1 || r.Started.IsZero() || r.Captured.Before(r.Started) || jquantsbulk.ValidateMonth(r.Month) != nil {
		return errors.New("完了した月次bulkファイル1件の記録を確認してください")
	}
	file := r.Files[0]
	if file.Local == "" || file.Local != filepath.Base(file.Local) || strings.ContainsAny(file.Local, "/\\") || file.Size <= 0 || file.Size > 512<<20 {
		return errors.New("bulk保存先/容量を確認してください")
	}
	f, err := os.Open(filepath.Join(filepath.Dir(*input), file.Local))
	if err != nil {
		return errors.New("bulk原gzipを読み込めません")
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, file.Size+1))
	if err != nil || n != file.Size || hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
		return errors.New("bulk原gzipのhash/容量が取得記録と一致しません")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errors.New("bulk原gzipを再読込できません")
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("bulk gzip形式を確認してください")
	}
	defer z.Close()
	convert := buildbars.FromJQuantsBulkCSV
	if *turnover {
		convert = buildbars.FromJQuantsBulkCSVWithTurnover
	}
	datasets, err := convert(ctx, z, file.SHA256, r.Month, r.Captured, strings.Split(*symbols, ","))
	if err != nil {
		return err
	}
	for _, bars := range datasets {
		prefix := "bars"
		if *turnover {
			prefix = "bars-turnover"
		}
		path, body, err := save(prefix, bars)
		if err != nil {
			return err
		}
		settings := struct{ Month, Symbol, GzipSHA256 string }{r.Month, bars.Symbol, file.SHA256}
		command := "import-jquants-bulk"
		if *turnover {
			command += "-turnover"
		}
		record, err := recordRun(command, filepath.Base(*input), raw, hash(raw), path, body, settings)
		if err != nil {
			return err
		}
		count := 0
		for _, day := range bars.Days {
			count += len(day.Bars)
		}
		if _, err := fmt.Fprintf(out, "bulk分足保存: symbol=%s / month=%s / days=%d / bars=%d / quality_promoted=false\n保存: %s\n実行記録: %s\n", bars.Symbol, r.Month, len(bars.Days), count, path, record); err != nil {
			return err
		}
		if *turnover {
			excluded := 0
			for _, x := range bars.ExcludedRows {
				excluded += x.Rows
			}
			if _, err := fmt.Fprintf(out, "turnover_contract=%s / excluded_selected_symbol_rows=%d / original_gzip_retained=true\n", bars.Source.Format, excluded); err != nil {
				return err
			}
		}
	}
	return nil
}
