package paperrecordingv3

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func smallOptions() Options {
	l := DefaultLimits()
	l.SegmentBytes = 16 << 10
	l.RawBytes = 1 << 20
	l.Segments = 100
	l.ReportBytes = 16 << 10
	l.ReserveBytes = 32 << 10
	return Options{Limits: l, Free: func(string) (uint64, error) { return 100 << 30, nil }}
}
func newSmall(t *testing.T, o Options) (string, *Writer) {
	t.Helper()
	d := filepath.Join(t.TempDir(), "session")
	w, e := New(d, "synthetic-session", "test-model", []byte(`{"fixture":true}`), o)
	if e != nil {
		t.Fatal(e)
	}
	return d, w
}
func appendN(t *testing.T, w *Writer, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if e := w.Append(uint64(i), "synthetic", []byte(`{"type":"synthetic"}`), bytes.Repeat([]byte("sample"), 180)); e != nil {
			t.Fatal(e)
		}
	}
}
func finish(t *testing.T, w *Writer) {
	t.Helper()
	u := 0
	if _, e := w.Stop("normal", Summary{AppliedSequence: w.stats.Sequence, Unresolved: &u}, []byte(`{"value":"<>&","holding":0}`), true); e != nil {
		t.Fatal(e)
	}
}
func TestSegmentedStreamAndCanonicalSnapshot(t *testing.T) {
	dir, w := newSmall(t, smallOptions())
	appendN(t, w, 40)
	finish(t, w)
	r := Recover(dir, RecoverOptions{})
	if r.Issue != "" || !r.RunComplete || r.VerifiedSequence != 40 || r.ClosedSegments < 2 {
		t.Fatal(r)
	}
	if _, e := New(dir, "other", "test", []byte(`{}`), smallOptions()); e == nil {
		t.Fatal("existing session reused")
	}
}
func TestFiniteCapsStopWithSeparateReport(t *testing.T) {
	for _, capName := range []string{"raw", "events", "segments"} {
		t.Run(capName, func(t *testing.T) {
			o := smallOptions()
			switch capName {
			case "raw":
				o.Limits.RawBytes = 20 << 10
			case "events":
				o.Limits.Events = 3
			case "segments":
				o.Limits.Segments = 1
			}
			dir, w := newSmall(t, o)
			var failed bool
			for i := 1; i < 100; i++ {
				if w.Append(uint64(i), "synthetic", []byte(`{}`), bytes.Repeat([]byte("x"), 1000)) != nil {
					failed = true
					break
				}
			}
			if !failed {
				t.Fatal("cap not applied")
			}
			u := 1
			term, e := w.Stop("bounded_cap", Summary{AppliedSequence: w.stats.Sequence, Unresolved: &u}, []byte(`{"holding":100}`), false)
			if e != nil || term.RunComplete {
				t.Fatal(term, e)
			}
			r := Recover(dir, RecoverOptions{})
			if r.Issue != "" || r.RunComplete || r.VerifiedSequence != w.stats.Sequence || r.Terminal.Summary.Unresolved == nil || *r.Terminal.Summary.Unresolved != 1 {
				t.Fatal("cap lost terminal", r)
			}
			em, e := ReadEmergency(dir)
			if e != nil || !em.ReportSaved {
				t.Fatal("emergency", e)
			}
		})
	}
}
func TestTamperGapDuplicateSwapAndTruncationReturnVerifiedPrefix(t *testing.T) {
	for _, fault := range []string{"tamper", "duplicate", "swap", "sequence", "truncated_line", "truncated_footer", "missing_segment", "wrong_session", "config"} {
		t.Run(fault, func(t *testing.T) {
			dir, w := newSmall(t, smallOptions())
			appendN(t, w, 20)
			finish(t, w)
			path := filepath.Join(dir, segmentName(1))
			raw, _ := os.ReadFile(path)
			lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
			switch fault {
			case "tamper":
				lines[2] = bytes.Replace(lines[2], []byte("synthetic"), []byte("changedxx"), 1)
			case "duplicate":
				lines[2] = append([]byte{}, lines[1]...)
			case "swap":
				lines[1], lines[2] = lines[2], lines[1]
			case "sequence":
				var f Frame
				json.Unmarshal(lines[2], &f)
				f.Record.Body.Sequence++
				b, _ := json.Marshal(f.Record.Body)
				f.Record.SHA256 = Hash(b)
				lines[2], _ = json.Marshal(f)
			case "truncated_line":
				lines = lines[:2]
				lines[1] = lines[1][:len(lines[1])/2]
			case "truncated_footer":
				lines = lines[:len(lines)-1]
			case "missing_segment":
				alternate := filepath.Join(t.TempDir(), "missing-segment-copy")
				if os.Mkdir(alternate, 0700) != nil {
					t.Fatal("fixture dir")
				}
				entries, _ := os.ReadDir(dir)
				for _, entry := range entries {
					if entry.Name() == segmentName(2) {
						continue
					}
					data, e := os.ReadFile(filepath.Join(dir, entry.Name()))
					if e != nil {
						t.Fatal(e)
					}
					if os.WriteFile(filepath.Join(alternate, entry.Name()), data, 0600) != nil {
						t.Fatal("fixture copy")
					}
				}
				dir = alternate
			case "wrong_session":
				lines[0] = bytes.Replace(lines[0], []byte("synthetic-session"), []byte("different-session"), 1)
			case "config":
				os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"fixture":false}`), 0600)
			}
			if fault != "missing_segment" && fault != "config" {
				data := bytes.Join(lines, []byte{'\n'})
				if fault != "truncated_line" {
					data = append(data, '\n')
				}
				os.WriteFile(path, data, 0600)
			}
			r := Recover(dir, RecoverOptions{})
			if r.Issue == "" || r.RunComplete || r.RestartAllowed {
				t.Fatal("corruption accepted", fault, r)
			}
			if fault == "duplicate" && r.VerifiedSequence != 1 {
				t.Fatal("prefix advanced after duplicate", r)
			}
		})
	}
}

type faultFile struct {
	File
	mode  string
	armed *bool
}

func (f faultFile) Write(b []byte) (int, error) {
	if *f.armed && f.mode == "write" {
		n, _ := f.File.Write(b[:len(b)/2])
		return n, errors.New("synthetic IO")
	}
	return f.File.Write(b)
}
func (f faultFile) Sync() error {
	if *f.armed && f.mode == "sync" {
		return errors.New("synthetic IO")
	}
	return f.File.Sync()
}
func (f faultFile) Close() error {
	e := f.File.Close()
	if *f.armed && f.mode == "close" {
		return errors.New("synthetic IO")
	}
	return e
}
func TestDiskAndIOFailuresStillAttemptTerminalAndEmergency(t *testing.T) {
	for _, mode := range []string{"disk", "write", "sync", "close", "report", "manifest", "reserve_remove", "all_control"} {
		t.Run(mode, func(t *testing.T) {
			o := smallOptions()
			armed := false
			var touched []string
			o.Open = func(p string, flags int) (File, error) {
				touched = append(touched, filepath.Base(p))
				if armed && (mode == "report" && strings.HasSuffix(p, "terminal.json") || mode == "manifest" && strings.HasSuffix(p, "manifest.json") || mode == "all_control" && (strings.HasSuffix(p, "terminal.json") || strings.HasSuffix(p, "emergency-1.json"))) {
					return nil, errors.New("synthetic IO")
				}
				f, e := os.OpenFile(p, flags, 0600)
				if e != nil {
					return nil, e
				}
				if strings.Contains(filepath.Base(p), "segment-") {
					return faultFile{f, mode, &armed}, nil
				}
				return f, nil
			}
			o.Free = func(string) (uint64, error) {
				if armed && mode == "disk" {
					return 0, nil
				}
				return 100 << 30, nil
			}
			o.Remove = func(p string) error {
				if armed && mode == "reserve_remove" {
					return errors.New("synthetic IO")
				}
				return os.Remove(p)
			}
			dir, w := newSmall(t, o)
			appendN(t, w, 2)
			armed = true
			if mode == "disk" || mode == "write" || mode == "sync" {
				if w.Append(3, "synthetic", []byte(`{}`), nil) == nil {
					t.Fatal("IO not rejected")
				}
			}
			u := 1
			term, _ := w.Stop(mode, Summary{AppliedSequence: 2, Unresolved: &u}, []byte(`{"holding":100}`), false)
			if term.RunComplete {
				t.Fatal("failed run completed")
			}
			if !strings.Contains(strings.Join(touched, ","), "terminal.json") || !strings.Contains(strings.Join(touched, ","), "emergency-1.json") {
				t.Fatal("earlyreturn skipped control", touched)
			}
			em, e := ReadEmergency(dir)
			if mode != "all_control" && e != nil {
				t.Fatal("small emergency not retained", e)
			}
			if mode == "report" && em.ReportSaved {
				t.Fatal("false report claim")
			}
			r := Recover(dir, RecoverOptions{})
			if r.RunComplete {
				t.Fatal("failure recovered as success", r)
			}
		})
	}
}
func TestClosedCompressionPreservesRawAndRejectsDamage(t *testing.T) {
	dir, w := newSmall(t, smallOptions())
	appendN(t, w, 2)
	if ArchiveClosed(dir, 1) == nil {
		t.Fatal("open segment compressed")
	}
	finish(t, w)
	path := filepath.Join(dir, segmentName(1))
	before, _, _ := HashFile(path)
	if e := ArchiveClosed(dir, 1); e != nil {
		t.Fatal(e)
	}
	r := Recover(dir, RecoverOptions{PreferCompressed: true})
	if r.Issue != "" || r.VerifiedSequence != 2 {
		t.Fatal("compressed restore", r)
	}
	after, _, _ := HashFile(path)
	if before != after {
		t.Fatal("raw changed")
	}
	f, e := os.OpenFile(path+".gz", os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteAt([]byte{0}, 12)
	f.Close()
	r = Recover(dir, RecoverOptions{PreferCompressed: true})
	if r.Issue == "" {
		t.Fatal("corrupt gzip accepted")
	}
}
