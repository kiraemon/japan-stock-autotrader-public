package collectquotes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/kabu"
)

// A run can be partial. Each record has its own receive time; snapshots are not atomic.
type Record struct {
	Endpoint   string          `json:"endpoint"`
	ReceivedAt time.Time       `json:"received_at"`
	Data       json.RawMessage `json:"data,omitempty"`
	Error      string          `json:"error,omitempty"`
}

type Snapshot struct {
	SchemaVersion int              `json:"schema_version"`
	Environment   kabu.Environment `json:"environment"`
	StartedAt     time.Time        `json:"started_at"`
	FinishedAt    time.Time        `json:"finished_at"`
	Complete      bool             `json:"complete"`
	Records       []Record         `json:"records"`
}

type Reader interface {
	Cash(context.Context) (json.RawMessage, error)
	Margin(context.Context) (json.RawMessage, error)
	Positions(context.Context, bool) (json.RawMessage, error)
	Board(context.Context, string) (json.RawMessage, error)
}

func Collect(ctx context.Context, client Reader, environment kabu.Environment, symbols []string) (Snapshot, error) {
	run := Snapshot{SchemaVersion: 1, Environment: environment, StartedAt: time.Now().UTC(), Records: []Record{}}
	type operation struct {
		path string
		call func(context.Context) (json.RawMessage, error)
	}
	ops := []operation{
		{"/wallet/cash", client.Cash},
		{"/wallet/margin", client.Margin},
		{"/positions?product=1&addinfo=true", func(ctx context.Context) (json.RawMessage, error) { return client.Positions(ctx, false) }},
		{"/positions?product=2&addinfo=true", func(ctx context.Context) (json.RawMessage, error) { return client.Positions(ctx, true) }},
	}
	for _, symbol := range symbols {
		if err := kabu.ValidateSymbol(symbol); err != nil {
			return run, err
		}
		ops = append(ops, operation{"/board/" + symbol, func(ctx context.Context) (json.RawMessage, error) { return client.Board(ctx, symbol) }})
	}
	for _, op := range ops {
		raw, err := op.call(ctx)
		record := Record{Endpoint: op.path, ReceivedAt: time.Now().UTC(), Data: raw}
		if err != nil {
			record.Error = err.Error()
		}
		run.Records = append(run.Records, record)
		if err != nil {
			run.FinishedAt = time.Now().UTC()
			return run, err
		}
	}
	run.Complete = true
	run.FinishedAt = time.Now().UTC()
	return run, nil
}

// Save writes a new file without overwriting an earlier run. The file is closed
// and synced before being published under its final .json name.
func Save(directory string, run Snapshot) (string, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", errors.New("保存ディレクトリーを作成できません")
	}
	f, err := os.CreateTemp(directory, ".snapshot-*.tmp")
	if err != nil {
		return "", errors.New("保存ファイルを作成できません")
	}
	temp := f.Name()
	defer os.Remove(temp)
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(run); err != nil {
		f.Close()
		return "", errors.New("保存データを生成できません")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", errors.New("保存データを同期できません")
	}
	if err := f.Close(); err != nil {
		return "", errors.New("保存ファイルを閉じられません")
	}
	final := filepath.Join(directory, "snapshot-"+filepath.Base(temp)[len(".snapshot-"):len(filepath.Base(temp))-len(".tmp")]+".json")
	if err := os.Rename(temp, final); err != nil {
		return "", errors.New("保存ファイルを確定できません")
	}
	return final, nil
}
