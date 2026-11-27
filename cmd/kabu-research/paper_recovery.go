package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/papertrade"
	"github.com/kiraemon/japan-stock-trading-lab/internal/paperrecordingv3"
)

const paperRecoveryVersion = "synthetic-paper-recovery-v1"
const paperRecoveryCheckpoint = "checkpoint.json"
const paperRecoveryFileLimit = 64 << 10
const paperRecoveryAttemptLimit = 64

type paperRecoveryReceipt struct {
	Version          string                     `json:"version"`
	PreviousSHA256   string                     `json:"previous_sha256"`
	Request          papertrade.RecoveryRequest `json:"request"`
	Result           papertrade.RecoveryResult  `json:"result"`
	CheckpointSHA256 string                     `json:"checkpoint_sha256"`
}

type paperRecoveryHistory struct {
	ledger   *papertrade.Simulator
	head     string
	receipts map[string]paperRecoveryReceipt
}

func runPaperRecovery(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("paper-recovery", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "合成試験の記録フォルダ")
	requestPath := fs.String("request", "", "applyで処理する合成操作JSON")
	fs.Usage = func() {
		fmt.Fprintln(out, "paper-recovery -dir <folder> [-request <file>] prepare|inspect|apply")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *dir == "" || fs.NArg() != 1 {
		return errors.New("復旧操作と記録フォルダを指定してください")
	}
	action := fs.Arg(0)
	if (action == "apply") != (*requestPath != "") {
		return errors.New("-request は apply のときに指定してください")
	}
	switch action {
	case "prepare":
		ledger, err := papertrade.StartRecoveryDemo()
		if err != nil {
			return err
		}
		checkpoint, err := ledger.Checkpoint()
		if err != nil {
			return err
		}
		if os.MkdirAll(filepath.Dir(*dir), 0700) != nil || os.Mkdir(*dir, 0700) != nil {
			return errors.New("新しい復旧試験フォルダを作成できません")
		}
		if err := publishPaperRecoveryFile(*dir, paperRecoveryCheckpoint, checkpoint); err != nil {
			return err
		}
		return printPaperRecovery(out, action, ledger.Snapshot(), nil, false)
	case "inspect", "apply":
		history, err := loadPaperRecovery(*dir)
		if err != nil {
			return err
		}
		if action == "inspect" {
			return printPaperRecovery(out, action, history.ledger.Snapshot(), nil, false)
		}
		return applyPaperRecovery(*dir, *requestPath, history, out)
	default:
		return errors.New("復旧操作は prepare / inspect / apply を指定してください")
	}
}

func applyPaperRecovery(dir, requestPath string, history *paperRecoveryHistory, out io.Writer) error {
	raw, err := readPaperRecoveryFile(requestPath)
	var request papertrade.RecoveryRequest
	if err != nil || decodePaperRecovery(raw, &request) != nil {
		return errors.New("復旧操作JSONの形式を確認してください")
	}
	if err := request.Validate(); err != nil {
		return err
	}
	receipt, reused := history.receipts[request.ID]
	if reused {
		before, _ := json.Marshal(receipt.Request)
		after, _ := json.Marshal(request)
		if !bytes.Equal(before, after) {
			return errors.New("同じ復旧操作IDに異なる入力が指定されました")
		}
	} else {
		if len(history.receipts) >= paperRecoveryAttemptLimit {
			return errors.New("合成復旧の操作件数上限に達しました")
		}
		var err error
		receipt, raw, err = recoveryReceipt(history.ledger, history.head, request)
		if err != nil {
			return err
		}
		if err := publishPaperRecoveryFile(dir, recoveryReceiptName(len(history.receipts)+1), raw); err != nil {
			return err
		}
	}
	if err := printPaperRecovery(out, "apply", history.ledger.Snapshot(), &receipt, reused); err != nil {
		return err
	}
	if !receipt.Result.Accepted {
		return errors.New("復旧操作を拒否しました")
	}
	return nil
}

func loadPaperRecovery(dir string) (*paperRecoveryHistory, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("復旧試験フォルダを確認してください")
	}
	raw, err := readPaperRecoveryFile(filepath.Join(dir, paperRecoveryCheckpoint))
	if err != nil {
		return nil, errors.New("復旧の元チェックポイントを読み取れません")
	}
	ledger, err := papertrade.Restore(raw)
	if err != nil {
		return nil, err
	}
	// Recovery v1 accepts only the stopped synthetic case created by prepare.
	fixture, err := papertrade.StartRecoveryDemo()
	if err != nil {
		return nil, err
	}
	expected, err := fixture.Checkpoint()
	if err != nil || !bytes.Equal(raw, expected) {
		return nil, errors.New("停止済みの合成復旧チェックポイントと一致しません")
	}
	attempts, err := listPaperRecoveryAttempts(dir)
	if err != nil {
		return nil, err
	}
	history := &paperRecoveryHistory{ledger: ledger, head: hash(raw), receipts: map[string]paperRecoveryReceipt{}}
	for sequence := 1; sequence <= len(attempts); sequence++ {
		name, exists := attempts[sequence]
		if !exists {
			return nil, errors.New("復旧操作の記録が連続していません")
		}
		raw, err := readPaperRecoveryFile(filepath.Join(dir, name))
		var stored paperRecoveryReceipt
		if err != nil || decodePaperRecovery(raw, &stored) != nil || stored.PreviousSHA256 != history.head {
			return nil, errors.New("復旧操作の記録を検証できません")
		}
		if _, duplicate := history.receipts[stored.Request.ID]; duplicate {
			return nil, errors.New("復旧操作の記録に重複IDがあります")
		}
		receipt, replayed, err := recoveryReceipt(ledger, history.head, stored.Request)
		if err != nil || !bytes.Equal(raw, replayed) {
			return nil, errors.New("復旧操作を再生した結果が保存記録と一致しません")
		}
		history.receipts[receipt.Request.ID] = receipt
		history.head = hash(raw)
	}
	return history, nil
}

func recoveryReceipt(ledger *papertrade.Simulator, previous string, request papertrade.RecoveryRequest) (paperRecoveryReceipt, []byte, error) {
	result, err := ledger.ApplyRecovery(request)
	if err != nil {
		return paperRecoveryReceipt{}, nil, err
	}
	checkpoint, err := ledger.Checkpoint()
	if err != nil {
		return paperRecoveryReceipt{}, nil, err
	}
	receipt := paperRecoveryReceipt{
		Version: paperRecoveryVersion, PreviousSHA256: previous, Request: request,
		Result: result, CheckpointSHA256: hash(checkpoint),
	}
	raw, err := json.Marshal(receipt)
	return receipt, raw, err
}

func listPaperRecoveryAttempts(dir string) (map[int]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, errors.New("復旧操作の記録を列挙できません")
	}
	defer f.Close()
	const directoryLimit = paperRecoveryAttemptLimit + 32
	files, err := f.ReadDir(directoryLimit + 1)
	if (err != nil && !errors.Is(err, io.EOF)) || len(files) > directoryLimit {
		return nil, errors.New("復旧操作の記録数を確認してください")
	}
	attempts := make(map[int]string)
	for _, file := range files {
		name := file.Name()
		if !strings.HasPrefix(name, "attempt-") {
			continue
		}
		number := strings.TrimSuffix(strings.TrimPrefix(name, "attempt-"), ".json")
		sequence, err := strconv.Atoi(number)
		if err != nil || sequence < 1 || sequence > paperRecoveryAttemptLimit || name != recoveryReceiptName(sequence) {
			return nil, errors.New("復旧操作の記録名が不正です")
		}
		attempts[sequence] = name
	}
	return attempts, nil
}

func recoveryReceiptName(sequence int) string {
	return fmt.Sprintf("attempt-%03d.json", sequence)
}

func readPaperRecoveryFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > paperRecoveryFileLimit {
		return nil, errors.New("合成復旧の入力には64KiB以内の通常ファイルを指定してください")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("合成復旧の入力を開けません")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, paperRecoveryFileLimit+1))
	if err != nil || len(raw) > paperRecoveryFileLimit {
		return nil, errors.New("合成復旧の入力を読み取れません")
	}
	return raw, nil
}

func decodePaperRecovery(raw []byte, value any) error {
	if err := paperrecordingv3.Unique(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

// Publish only after writing and syncing the bytes. The hard link claims the
// name without replacing an earlier receipt if two processes start together.
func publishPaperRecoveryFile(dir, name string, raw []byte) error {
	f, err := os.CreateTemp(dir, ".paper-recovery-*")
	if err != nil {
		return errors.New("復旧記録を作成できません")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("復旧記録を同期できません")
	}
	if err := os.Link(f.Name(), filepath.Join(dir, name)); err != nil {
		return errors.New("復旧記録を確定できません。並行実行や保存先を確認してください")
	}
	return nil
}

func printPaperRecovery(out io.Writer, action string, state papertrade.Snapshot, receipt *paperRecoveryReceipt, reused bool) error {
	fills := []string{}
	for _, record := range state.Records {
		if record.Event.Kind == "fill" && record.Outcome.Accepted && !record.Outcome.Duplicate {
			fills = append(fills, record.Event.ID)
		}
	}
	if receipt != nil {
		var filled int64
		if receipt.Result.Fill != nil {
			filled = receipt.Result.Fill.Delta
		}
		if _, err := fmt.Fprintf(out, "request_id=%s accepted=%t duplicate=%t receipt_reused=%t virtual_fill=%d reason=%s\n",
			receipt.Request.ID, receipt.Result.Accepted, receipt.Result.Duplicate, reused, filled, receipt.Result.Reason); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "synthetic=true action=%s open_quantity=%d position_known=%t reconciliation_required=%t new_entries_halted=%t active_orders=%d processed_fills=%s\n",
		action, state.OpenQuantity, state.PositionKnown, state.ReconcileRequired, state.Halted, state.ActiveOrders, strings.Join(fills, ","))
	return err
}
