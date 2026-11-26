package paperrecordingv3

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStartupNeverClaimsOrOverwritesAnUnusableSession(t *testing.T) {
	const session, model = "synthetic", "fixture"
	config := []byte(`{"fixture":true}`)
	t.Run("low disk stops before claim", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "recording")
		options := smallOptions()
		options.Free = func(string) (uint64, error) { return 0, nil }
		writer, err := New(dir, session, model, config, options)
		var failure *Failure
		if writer != nil || !errors.As(err, &failure) || failure.Code != "disk_low_or_unknown" {
			t.Fatalf("invalid disk state was admitted: %v", err)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("startup left a claim: %v", err)
		}
	})
	t.Run("existing session remains unchanged", func(t *testing.T) {
		dir, existing := newSmall(t, smallOptions())
		finish(t, existing)
		path := filepath.Join(dir, "session.json")
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writer, err := New(dir, session, model, config, smallOptions())
		var failure *Failure
		if writer != nil || !errors.As(err, &failure) || failure.Code != "directory_exists_or_unavailable" {
			t.Fatalf("an existing session was reclaimed: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(before) != string(after) {
			t.Fatalf("rejected restart changed the original session: %v", err)
		}
	})
}

func TestStartupLeavesEmergencyEvidenceWhenWritingControlFilesFails(t *testing.T) {
	for _, stage := range []struct{ file, mode, code string }{
		{"report.reserve", "open", "io_open"},
		{"report.reserve", "write", "io_write"},
		{"report.reserve", "sync", "io_sync"},
		{"report.reserve", "close", "io_close"},
		{"config.json", "write", "io_write"},
		{"config.json", "sync", "io_sync"},
		{"session.json", "close", "io_close"},
	} {
		t.Run(stage.file+" "+stage.mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "recording")
			options := smallOptions()
			armed := true
			options.Open = func(path string, flags int) (File, error) {
				target := filepath.Base(path) == stage.file
				if target && stage.mode == "open" {
					return nil, errors.New("synthetic startup fault")
				}
				file, err := os.OpenFile(path, flags, 0600)
				if err != nil {
					return nil, err
				}
				if target {
					return faultFile{file, stage.mode, &armed}, nil
				}
				return file, nil
			}
			writer, err := New(dir, "synthetic", "fixture", []byte("{}"), options)
			var failure *Failure
			if writer == nil || !errors.As(err, &failure) || failure.Code != stage.code || failure.Uncertain != (stage.mode != "open") {
				t.Fatalf("wrong startup failure: %v", err)
			}
			if writer.Stats().Sequence != 0 || writer.Stats().Segments != 0 {
				t.Fatalf("startup wrote a segment after a failed control file: %+v", writer.Stats())
			}
			emergency, err := ReadEmergency(dir)
			if err != nil || emergency.Reason != "not_finalized" || emergency.ReportSaved {
				t.Fatalf("emergency evidence lost: %+v, %v", emergency, err)
			}
			if recovered := Recover(dir, RecoverOptions{}); recovered.RunComplete || recovered.RestartAllowed {
				t.Fatalf("incomplete startup treated as recoverable run: %+v", recovered)
			}
		})
	}
}
