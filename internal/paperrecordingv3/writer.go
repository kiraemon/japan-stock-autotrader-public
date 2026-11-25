package paperrecordingv3

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"hash"
	"os"
	"path/filepath"
	"regexp"
)

type Writer struct {
	dir                string
	header             Header
	headerSHA          string
	options            Options
	stats              Stats
	file               File
	segmentHash        hash.Hash
	segmentBytes       int64
	first, count       uint64
	previousSegment    string
	closed             []Segment
	failure            error
	uncertain, stopped bool
}

func New(dir, session, model string, config []byte, o Options) (*Writer, error) {
	o = o.defaults()
	if o.Limits.Validate() != nil || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`).MatchString(session) || len(model) > 120 || len(config) > ConfigLimit || Unique(config) != nil {
		return nil, fail("header_input", "new", false)
	}
	free, e := o.Free(filepath.Dir(dir))
	if e != nil || free < o.Limits.StartFree {
		return nil, fail("disk_low_or_unknown", "start", false)
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return nil, fail("directory_exists_or_unavailable", "claim", false)
	}
	w := &Writer{dir: dir, options: o, header: Header{Version, session, model, Hash(config), o.Limits}}
	// Control and reserve are independent of the raw quota. No existing session is reopened.
	if e = w.saveEmergency("emergency-0.json", Emergency{Version: Version, Session: session, Reason: "not_finalized"}, true); e != nil {
		return w, e
	}
	if e = w.reserve(); e != nil {
		return w, e
	}
	if e = w.writeExclusive("config.json", config, ConfigLimit); e != nil {
		return w, e
	}
	header, _ := json.Marshal(w.header)
	w.headerSHA = Hash(header)
	w.stats.LastRecord = w.headerSHA
	if e = w.writeExclusive("session.json", header, HeaderLimit); e != nil {
		return w, e
	}
	if e = w.startSegment(); e != nil {
		return w, e
	}
	return w, nil
}
func (w *Writer) Stats() Stats { return w.stats }
func (w *Writer) trip(e error) error {
	if w.failure == nil {
		w.failure = e
	}
	return e
}
func (w *Writer) checkDisk() error {
	n, e := w.options.Free(w.dir)
	if e != nil || n < w.options.Limits.StopFree {
		return fail("disk_low_or_unknown", "append", false)
	}
	return nil
}
func (w *Writer) writeExclusive(name string, raw []byte, max int64) error {
	if int64(len(raw)) > max {
		return fail("control_limit", name, false)
	}
	f, e := w.options.Open(filepath.Join(w.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if e != nil {
		return fail("io_open", name, false)
	}
	n, e := f.Write(raw)
	if e != nil || n != len(raw) {
		_ = f.Close()
		return fail("io_write", name, true)
	}
	if e = f.Sync(); e != nil {
		_ = f.Close()
		return fail("io_sync", name, true)
	}
	if e = f.Close(); e != nil {
		return fail("io_close", name, true)
	}
	return nil
}
func (w *Writer) reserve() error {
	f, e := w.options.Open(filepath.Join(w.dir, "report.reserve"), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if e != nil {
		return fail("io_open", "reserve", false)
	}
	buf := make([]byte, 64<<10)
	remaining := w.options.Limits.ReserveBytes
	for remaining > 0 {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		k, err := f.Write(buf[:n])
		if err != nil || int64(k) != n {
			_ = f.Close()
			return fail("io_write", "reserve", true)
		}
		remaining -= n
	}
	if e = f.Sync(); e != nil {
		_ = f.Close()
		return fail("io_sync", "reserve", true)
	}
	if f.Close() != nil {
		return fail("io_close", "reserve", true)
	}
	return nil
}
func (w *Writer) writeLine(raw []byte) error {
	n, e := w.file.Write(raw)
	if n > 0 {
		w.segmentHash.Write(raw[:n])
		w.segmentBytes += int64(n)
		w.stats.StoredBytes += int64(n)
	}
	if e != nil || n != len(raw) {
		w.uncertain = true
		w.stats.Uncertain = true
		return w.trip(fail("io_write", "segment", true))
	}
	if e = w.file.Sync(); e != nil {
		w.uncertain = true
		w.stats.Uncertain = true
		return w.trip(fail("io_sync", "segment", true))
	}
	w.stats.DurableBytes = w.stats.StoredBytes
	return nil
}
func (w *Writer) startSegment() error {
	if w.stats.Segments >= w.options.Limits.Segments {
		return w.trip(fail("segment_limit", "roll", false))
	}
	if e := w.checkDisk(); e != nil {
		return w.trip(e)
	}
	i := w.stats.Segments + 1
	f, e := w.options.Open(filepath.Join(w.dir, segmentName(i)), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if e != nil {
		return w.trip(fail("io_open", "segment", false))
	}
	w.file = f
	w.stats.Segments = i
	w.segmentHash = sha256.New()
	w.segmentBytes = 0
	w.first = w.stats.Sequence + 1
	w.count = 0
	raw, _ := json.Marshal(Frame{Type: "segment_header", Header: &SegmentHeader{Version, w.header.Session, i, w.previousSegment, w.stats.LastRecord, w.first}})
	return w.writeLine(append(raw, '\n'))
}
func (w *Writer) closeSegment() error {
	if w.file == nil {
		return nil
	}
	if w.uncertain {
		e := w.file.Close()
		w.file = nil
		if e != nil {
			return fail("io_close", "uncertain_segment", true)
		}
		return fail("uncommitted_tail", "seal", true)
	}
	footer := Footer{Version, w.header.Session, w.stats.Segments, w.first, w.stats.Sequence, w.count, w.stats.LastRecord, w.previousSegment}
	raw, _ := json.Marshal(Frame{Type: "segment_footer", Footer: &footer})
	raw = append(raw, '\n')
	if int64(len(raw)) > FooterReserve || w.stats.StoredBytes+int64(len(raw)) > w.options.Limits.RawBytes {
		return fail("footer_reserve", "seal", false)
	}
	if e := w.writeLine(raw); e != nil {
		_ = w.file.Close()
		w.file = nil
		return e
	}
	e := w.file.Close()
	w.file = nil
	if e != nil {
		return w.trip(fail("io_close", "segment", true))
	}
	s := Segment{w.stats.Segments, segmentName(w.stats.Segments), w.segmentBytes, bytesHash(w.segmentHash), w.first, w.stats.Sequence, w.count, w.stats.LastRecord}
	w.closed = append(w.closed, s)
	w.previousSegment = s.SHA256
	return nil
}
func bytesHash(h hash.Hash) string { return __hex(h.Sum(nil)) }
func __hex(b []byte) string {
	const hex = "0123456789abcdef"
	s := make([]byte, len(b)*2)
	for i, v := range b {
		s[i*2] = hex[v>>4]
		s[i*2+1] = hex[v&15]
	}
	return string(s)
}
func (w *Writer) Append(sequence uint64, kind string, event, raw []byte) error {
	if w.stopped || w.failure != nil {
		return fail("writer_stopped", "append", false)
	}
	if sequence != w.stats.Sequence+1 || sequence > w.options.Limits.Events || len(kind) == 0 || len(kind) > 32 {
		return w.trip(fail("sequence_limit_or_order", "append", false))
	}
	if len(event) > RecordLimit || len(raw) > 1<<20 || Unique(event) != nil {
		return w.trip(fail("event_input", "append", false))
	}
	if e := w.checkDisk(); e != nil {
		return w.trip(e)
	}
	encode := func() ([]byte, string) {
		b := Body{Version, w.header.Session, w.stats.Segments, sequence, w.stats.LastRecord, kind, event, raw, ""}
		if len(raw) > 0 {
			b.RawSHA = Hash(raw)
		}
		body, _ := json.Marshal(b)
		r := Record{b, Hash(body)}
		line, _ := json.Marshal(Frame{Type: "event", Record: &r})
		return append(line, '\n'), r.SHA256
	}
	line, digest := encode()
	if len(line) > RecordLimit {
		return w.trip(fail("record_limit", "append", false))
	}
	if int64(len(line))+FooterReserve+1024 > w.options.Limits.SegmentBytes {
		return w.trip(fail("record_exceeds_segment", "append", false))
	}
	if w.segmentBytes+int64(len(line))+FooterReserve > w.options.Limits.SegmentBytes {
		if e := w.closeSegment(); e != nil {
			return w.trip(e)
		}
		if e := w.startSegment(); e != nil {
			return e
		}
		line, digest = encode()
	}
	if w.stats.StoredBytes+int64(len(line))+FooterReserve > w.options.Limits.RawBytes {
		return w.trip(fail("raw_limit", "append", false))
	}
	if e := w.writeLine(line); e != nil {
		return e
	}
	w.stats.Sequence = sequence
	w.stats.LastRecord = digest
	w.count++
	return nil
}
func (w *Writer) saveEmergency(name string, v Emergency, exclusive bool) error {
	raw, e := controlBytes(v, EmergencyBytes)
	if e != nil {
		return e
	}
	raw = append(raw, bytes.Repeat([]byte{' '}, EmergencyBytes-len(raw))...)
	if exclusive {
		return w.writeExclusive(name, raw, EmergencyBytes)
	}
	return errors.New("emergency overwrite forbidden")
}

// Stop never appends a raw event. Even a cap/failed footer cannot bypass report attempts.
func (w *Writer) Stop(reason string, summary Summary, snapshot []byte, runComplete bool) (Terminal, error) {
	if w.stopped {
		return Terminal{}, fail("already_stopped", "stop", false)
	}
	w.stopped = true
	terminal := Terminal{Version: Version, Session: w.header.Session, Reason: reason, Stats: w.stats, Summary: summary, StopOutsideRawChain: true}
	sealErr := w.closeSegment()
	if sealErr != nil {
		terminal.SealError = sealErr.Error()
	} else if len(w.closed) != w.stats.Segments || len(w.closed) == 0 {
		terminal.SealError = "segment_close_unconfirmed"
	} else {
		m := Manifest{Version, w.header.Session, w.headerSHA, w.closed, w.stats}
		data, _ := json.Marshal(m)
		if e := w.writeExclusive("manifest.json", data, 1<<20); e != nil {
			terminal.SealError = e.Error()
		} else {
			terminal.StorageSealed = true
		}
	}
	terminal.Stats = w.stats
	terminal.RunComplete = runComplete && w.failure == nil && terminal.StorageSealed && summary.AppliedSequence == w.stats.Sequence && !summary.StateUnknown && summary.Unresolved != nil && *summary.Unresolved == 0
	if len(snapshot) == 0 || Unique(snapshot) != nil || int64(len(snapshot)) > w.options.Limits.ReportBytes/2 {
		terminal.SnapshotError = "snapshot_missing_invalid_or_limit"
	} else {
		canonical, _ := json.Marshal(json.RawMessage(snapshot))
		terminal.Snapshot = canonical
		terminal.SnapshotSHA = Hash(canonical)
	}
	if terminal.SnapshotError != "" {
		terminal.RunComplete = false
	}
	// Releasing only our reserve is allowed; failure is explicit and does not skip either output.
	reserveErr := w.options.Remove(filepath.Join(w.dir, "report.reserve"))
	if reserveErr != nil && !os.IsNotExist(reserveErr) {
		terminal.SnapshotError += ";reserve_release_failed"
		terminal.RunComplete = false
	}
	data, e := controlBytes(terminal, w.options.Limits.ReportBytes)
	if e == nil {
		e = w.writeExclusive("terminal.json", data, w.options.Limits.ReportBytes)
	}
	em := Emergency{Version: w.header.Version, Session: w.header.Session, Reason: reason, Stats: w.stats, Summary: summary, ReportSaved: e == nil, SealError: terminal.SealError}
	if e != nil {
		em.ReportError = e.Error()
	}
	emErr := w.saveEmergency("emergency-1.json", em, true)
	if e != nil {
		return terminal, e
	}
	if emErr != nil {
		return terminal, emErr
	}
	if sealErr != nil {
		return terminal, sealErr
	}
	if terminal.SealError != "" {
		return terminal, fail("manifest_failed", "stop", false)
	}
	return terminal, nil
}
