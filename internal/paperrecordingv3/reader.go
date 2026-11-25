package paperrecordingv3

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
)

type RecoverOptions struct {
	PreferCompressed  bool
	ExpectedHeaderSHA string
	Apply             func(Body) error
}

func readSmall(path string, max int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, fail("file_limit", "read", false)
	}
	return b, nil
}
func ReadHeader(dir string) (Header, []byte, string, error) {
	var h Header
	raw, e := readSmall(filepath.Join(dir, "session.json"), HeaderLimit)
	if e != nil || strict(raw, HeaderLimit, &h) != nil || h.Version != Version || h.Limits.Validate() != nil || h.Session == "" {
		return h, nil, "", fail("header_invalid", "recover", false)
	}
	c, e := readSmall(filepath.Join(dir, "config.json"), ConfigLimit)
	if e != nil || Unique(c) != nil || Hash(c) != h.ConfigSHA {
		return h, nil, "", fail("config_invalid", "recover", false)
	}
	return h, c, Hash(raw), nil
}
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var b []byte
	for {
		v, e := r.ReadSlice('\n')
		if len(b)+len(v) > max {
			return nil, fail("line_limit", "recover", false)
		}
		b = append(b, v...)
		if e == bufio.ErrBufferFull {
			continue
		}
		return b, e
	}
}
func Recover(dir string, o RecoverOptions) Recovery {
	h, c, headerSHA, e := ReadHeader(dir)
	r := Recovery{Version: Version, Session: h.Session, HeaderSHA: headerSHA, Config: c, Status: "incomplete", LastRecord: headerSHA}
	reject := func(issue string) Recovery { r.Issue = issue; return r }
	if e != nil {
		return reject("header_or_config_invalid")
	}
	if o.ExpectedHeaderSHA != "" && o.ExpectedHeaderSHA != headerSHA {
		return reject("header_binding")
	}
	// Fixed-size inventory; do not retain an unbounded directory listing.
	indices := map[int]bool{}
	d, e := os.Open(dir)
	if e != nil {
		return reject("directory_unreadable")
	}
	defer d.Close()
	entries := 0
	pattern := regexp.MustCompile(`^segment-(\d{6})\.jsonl(?:\.gz)?$`)
	for {
		es, err := d.ReadDir(128)
		for _, entry := range es {
			entries++
			if entries > 1024 {
				return reject("directory_entry_limit")
			}
			match := pattern.FindStringSubmatch(entry.Name())
			if match == nil {
				continue
			}
			i, _ := strconv.Atoi(match[1])
			if entry.IsDir() || i < 1 || i > h.Limits.Segments {
				return reject("segment_name_or_limit")
			}
			indices[i] = true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return reject("directory_unreadable")
		}
	}
	var closed []Segment
	previousSegment := ""
	for i := 1; i <= len(indices); i++ {
		if !indices[i] {
			return reject("segment_missing_or_reordered")
		}
		seg, issue := scanSegment(dir, h, i, previousSegment, &r, o)
		if issue != "" {
			return reject(issue)
		}
		closed = append(closed, seg)
		r.ClosedSegments++
		previousSegment = seg.SHA256
	}
	if len(indices) == 0 {
		return reject("segment_missing")
	}
	var m Manifest
	raw, e := readSmall(filepath.Join(dir, "manifest.json"), 1<<20)
	if e != nil || strict(raw, 1<<20, &m) != nil {
		return reject("manifest_missing_or_invalid")
	}
	if m.Version != Version || m.Session != h.Session || m.HeaderSHA != headerSHA || !reflect.DeepEqual(m.Segments, closed) || m.Stats.Sequence != r.VerifiedSequence || m.Stats.LastRecord != r.LastRecord || m.Stats.StoredBytes != r.VerifiedBytes || m.Stats.DurableBytes != r.VerifiedBytes || m.Stats.Segments != len(closed) || m.Stats.Uncertain {
		return reject("manifest_binding")
	}
	r.StorageSealed = true
	var control Control
	raw, e = readSmall(filepath.Join(dir, "terminal.json"), h.Limits.ReportBytes)
	if e != nil || strict(raw, h.Limits.ReportBytes, &control) != nil || Hash(control.Body) != control.SHA256 {
		return reject("terminal_missing_or_invalid")
	}
	var terminal Terminal
	if strict(control.Body, h.Limits.ReportBytes, &terminal) != nil || terminal.Version != Version || terminal.Session != h.Session || terminal.Stats != m.Stats || !terminal.StorageSealed || terminal.RestartAllowed || terminal.Summary.AppliedSequence > terminal.Stats.Sequence || len(terminal.Snapshot) > 0 && Hash(terminal.Snapshot) != terminal.SnapshotSHA {
		return reject("terminal_binding")
	}
	r.Terminal = &terminal
	emergency, emergencyErr := ReadEmergency(dir)
	if emergencyErr != nil || !emergency.ReportSaved || emergency.Reason != terminal.Reason || emergency.Stats != terminal.Stats {
		return reject("terminal_durability_or_emergency_unconfirmed")
	}
	r.RunComplete = terminal.RunComplete
	r.Status = "sealed_stopped"
	if terminal.RunComplete {
		r.Status = "complete"
	}
	return r
}
func scanSegment(dir string, h Header, index int, previousSegment string, r *Recovery, o RecoverOptions) (Segment, string) {
	var input io.ReadCloser
	var compressed *gzipInput
	path := filepath.Join(dir, segmentName(index))
	var e error
	if o.PreferCompressed {
		compressed, e = openCompressed(dir, h, index)
		if e == nil {
			input = compressed
		}
	} else {
		input, e = os.Open(path)
	}
	if e != nil {
		return Segment{}, "segment_unreadable_or_compression_invalid"
	}
	defer input.Close()
	hasher := sha256.New()
	lr := io.LimitReader(input, h.Limits.SegmentBytes+1)
	reader := bufio.NewReaderSize(io.TeeReader(lr, hasher), 64<<10)
	var used int64
	first := r.VerifiedSequence + 1
	count := uint64(0)
	headerSeen, footerSeen := false, false
	for {
		line, err := readLine(reader, RecordLimit)
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil {
			return Segment{}, "truncated_line_or_read_error"
		}
		used += int64(len(line))
		if used > h.Limits.SegmentBytes || r.VerifiedBytes+int64(len(line)) > h.Limits.RawBytes {
			return Segment{}, "segment_or_raw_limit"
		}
		var f Frame
		if strict(bytes.TrimSpace(line), RecordLimit, &f) != nil {
			return Segment{}, "record_json_or_schema_invalid"
		}
		if footerSeen {
			return Segment{}, "data_after_footer"
		}
		switch f.Type {
		case "segment_header":
			if headerSeen || f.Header == nil || f.Record != nil || f.Footer != nil || *f.Header != (SegmentHeader{Version, h.Session, index, previousSegment, r.LastRecord, first}) {
				return Segment{}, "segment_header_binding"
			}
			headerSeen = true
		case "event":
			if !headerSeen || f.Record == nil || f.Header != nil || f.Footer != nil {
				return Segment{}, "event_frame"
			}
			v := f.Record
			b := v.Body
			body, _ := json.Marshal(b)
			if b.Version != Version || b.Session != h.Session || b.Segment != index || b.Sequence != r.VerifiedSequence+1 || b.Sequence > h.Limits.Events || b.Previous != r.LastRecord || v.SHA256 != Hash(body) || len(b.Kind) == 0 || len(b.Kind) > 32 || len(b.Raw) > 1<<20 || len(b.Raw) > 0 && Hash(b.Raw) != b.RawSHA || len(b.Raw) == 0 && b.RawSHA != "" || Unique(b.Event) != nil {
				return Segment{}, "record_chain_sequence_or_raw_hash"
			}
			if o.Apply != nil && o.Apply(b) != nil {
				return Segment{}, "event_semantics_or_apply"
			}
			r.VerifiedSequence = b.Sequence
			r.LastRecord = v.SHA256
			count++
		case "segment_footer":
			if !headerSeen || f.Footer == nil || f.Record != nil || f.Header != nil || *f.Footer != (Footer{Version, h.Session, index, first, r.VerifiedSequence, count, r.LastRecord, previousSegment}) {
				return Segment{}, "footer_binding"
			}
			footerSeen = true
		default:
			return Segment{}, "unknown_frame"
		}
		r.VerifiedBytes += int64(len(line))
	}
	if !headerSeen || !footerSeen {
		return Segment{}, "open_or_missing_footer"
	}
	if compressed != nil && (compressed.finish() != nil || compressed.archive.RawSHA != bytesHash(hasher) || compressed.archive.RawBytes != used) {
		return Segment{}, "compression_trailing_or_checksum"
	}
	return Segment{index, segmentName(index), used, bytesHash(hasher), first, r.VerifiedSequence, count, r.LastRecord}, ""
}
func ReadEmergency(dir string) (Emergency, error) {
	for _, name := range []string{"emergency-1.json", "emergency-0.json"} {
		raw, e := readSmall(filepath.Join(dir, name), EmergencyBytes)
		var c Control
		var em Emergency
		if e == nil && strict(raw, EmergencyBytes, &c) == nil && Hash(c.Body) == c.SHA256 && strict(c.Body, EmergencyBytes, &em) == nil && em.Version == Version {
			return em, nil
		}
	}
	return Emergency{}, fmt.Errorf("no verified emergency record")
}
