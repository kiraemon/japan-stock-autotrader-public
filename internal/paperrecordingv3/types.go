// Package paperrecordingv3 is bounded, local-only segmented paper storage.
// It has no broker, credentials, clock override, or execution capability.
package paperrecordingv3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const Version = "paper-recording-v3-20261026"
const MaxEvents uint64 = 12000000
const FooterReserve int64 = 4096
const EmergencyBytes = 8192
const ConfigLimit = 32 << 20
const HeaderLimit = 34 << 20
const RecordLimit = 2 << 20

type Limits struct {
	SegmentBytes, RawBytes    int64
	Segments                  int
	Events                    uint64
	ReportBytes, ReserveBytes int64
	StartFree, StopFree       uint64
}

// At observed24.6MB/min,390min is~9.6GB. These are finite stops, not a throughput guarantee.
func DefaultLimits() Limits {
	return Limits{64 << 20, 16 << 30, 256, MaxEvents, 8 << 20, 16 << 20, 8 << 30, 2 << 30}
}
func (l Limits) Validate() error {
	d := DefaultLimits()
	if l.SegmentBytes < 16<<10 || l.SegmentBytes > d.SegmentBytes || l.RawBytes < l.SegmentBytes || l.RawBytes > d.RawBytes || l.Segments < 1 || l.Segments > d.Segments || l.Events < 1 || l.Events > d.Events || l.ReportBytes < 1024 || l.ReportBytes > d.ReportBytes || l.ReserveBytes < l.ReportBytes || l.ReserveBytes > d.ReserveBytes || l.StartFree < l.StopFree || l.StartFree > d.StartFree || l.StopFree > d.StopFree {
		return errors.New("invalid bounded recording limits")
	}
	return nil
}

type Failure struct {
	Code, Operation string
	Uncertain       bool
}

func (e *Failure) Error() string                 { return "paper recording v3: " + e.Code + "/" + e.Operation }
func fail(code, op string, uncertain bool) error { return &Failure{code, op, uncertain} }

type File interface {
	io.Writer
	Sync() error
	Close() error
}
type Options struct {
	Limits Limits
	Open   func(string, int) (File, error)
	Remove func(string) error
	Free   func(string) (uint64, error)
}

func (o Options) defaults() Options {
	if o.Limits == (Limits{}) {
		o.Limits = DefaultLimits()
	}
	if o.Open == nil {
		o.Open = func(p string, flags int) (File, error) { return os.OpenFile(p, flags, 0600) }
	}
	if o.Remove == nil {
		o.Remove = os.Remove
	}
	if o.Free == nil {
		o.Free = FreeBytes
	}
	return o
}

type Header struct {
	Version, Session, Model, ConfigSHA string
	Limits                             Limits
}
type SegmentHeader struct {
	Version, Session                string
	Segment                         int
	PreviousSegment, PreviousRecord string
	FirstSequence                   uint64
}
type Body struct {
	Version, Session string
	Segment          int
	Sequence         uint64
	Previous, Kind   string
	Event            json.RawMessage
	Raw              []byte `json:",omitempty"`
	RawSHA           string `json:",omitempty"`
}
type Record struct {
	Body   Body
	SHA256 string
}
type Footer struct {
	Version, Session            string
	Segment                     int
	First, Last, Count          uint64
	LastRecord, PreviousSegment string
}
type Frame struct {
	Type   string
	Header *SegmentHeader `json:",omitempty"`
	Record *Record        `json:",omitempty"`
	Footer *Footer        `json:",omitempty"`
}
type Segment struct {
	Index              int
	File               string
	Bytes              int64
	SHA256             string
	First, Last, Count uint64
	LastRecord         string
}
type Stats struct {
	StoredBytes, DurableBytes int64
	Sequence                  uint64
	LastRecord                string
	Segments                  int
	Uncertain                 bool
}
type Manifest struct {
	Version, Session, HeaderSHA string
	Segments                    []Segment
	Stats                       Stats
}
type Summary struct {
	AppliedSequence uint64
	Unresolved      *int
	Equity          string
	StateUnknown    bool
}
type Terminal struct {
	Version, Session, Reason                                        string
	Stats                                                           Stats
	Summary                                                         Summary
	SealError, SnapshotError                                        string
	Snapshot                                                        json.RawMessage `json:",omitempty"`
	SnapshotSHA                                                     string
	StorageSealed, RunComplete, RestartAllowed, StopOutsideRawChain bool
}
type Control struct {
	Body   json.RawMessage
	SHA256 string
}
type Emergency struct {
	Version, Session, Reason string
	Stats                    Stats
	Summary                  Summary
	ReportSaved              bool
	ReportError, SealError   string
	RestartAllowed           bool
}
type Recovery struct {
	Version, Session, Status, Issue, HeaderSHA, LastRecord string
	Config                                                 []byte `json:"-"`
	VerifiedSequence                                       uint64
	VerifiedBytes                                          int64
	ClosedSegments                                         int
	StorageSealed, RunComplete, RestartAllowed             bool
	Terminal                                               *Terminal `json:",omitempty"`
}

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func HashFile(p string) (string, int64, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.CopyBuffer(h, f, make([]byte, 64<<10))
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func strict(raw []byte, limit int64, target any) error {
	if int64(len(raw)) > limit || Unique(raw) != nil {
		return fail("invalid_json", "decode", false)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return fail("invalid_schema", "decode", false)
	}
	return nil
}

// Unique is bounded by caller's byte limit, nesting32, and one record at a time.
func Unique(raw []byte) error {
	if len(raw) == 0 || len(raw) > HeaderLimit {
		return errors.New("json size")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("json depth")
		}
		tok, e := d.Token()
		if e != nil {
			return e
		}
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					tok, e = d.Token()
					k, ok := tok.(string)
					if e != nil || !ok || seen[strings.ToLower(k)] {
						return errors.New("duplicate key")
					}
					seen[strings.ToLower(k)] = true
					if e = walk(depth + 1); e != nil {
						return e
					}
				}
				tok, e = d.Token()
				if e != nil || tok != json.Delim('}') {
					return errors.New("json object")
				}
			case '[':
				for d.More() {
					if e = walk(depth + 1); e != nil {
						return e
					}
				}
				tok, e = d.Token()
				if e != nil || tok != json.Delim(']') {
					return errors.New("json array")
				}
			default:
				return errors.New("json delimiter")
			}
		}
		return nil
	}
	if walk(0) != nil {
		return errors.New("json invalid")
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("json tail")
	}
	return nil
}
func segmentName(i int) string { return fmt.Sprintf("segment-%06d.jsonl", i) }
func controlBytes(v any, max int64) ([]byte, error) {
	body, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(Control{body, Hash(body)})
	if e != nil || int64(len(raw)+1) > max {
		return nil, fail("control_limit", "marshal", false)
	}
	return append(raw, '\n'), nil
}
