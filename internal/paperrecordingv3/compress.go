package paperrecordingv3

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Archive struct {
	Version, Session string
	Segment          int
	RawSHA           string
	RawBytes         int64
	CompressedSHA    string
	CompressedBytes  int64
}
type gzipInput struct {
	g       *gzip.Reader
	br      *bufio.Reader
	f       *os.File
	archive Archive
}

func (g *gzipInput) Read(p []byte) (int, error) { return g.g.Read(p) }
func (g *gzipInput) Close() error               { _ = g.g.Close(); return g.f.Close() }
func (g *gzipInput) finish() error {
	if _, e := g.br.ReadByte(); e != io.EOF {
		return fmt.Errorf("compressed trailing stream")
	}
	return nil
}
func openCompressed(dir string, h Header, index int) (*gzipInput, error) {
	var a Archive
	name := segmentName(index)
	raw, e := readSmall(filepath.Join(dir, name+".gzip.json"), 8192)
	if e != nil || strict(raw, 8192, &a) != nil || a.Version != Version || a.Session != h.Session || a.Segment != index || a.RawBytes > h.Limits.SegmentBytes || a.CompressedBytes > h.Limits.SegmentBytes+(1<<20) {
		return nil, fmt.Errorf("archive receipt invalid")
	}
	digest, n, e := HashFile(filepath.Join(dir, name+".gz"))
	if e != nil || digest != a.CompressedSHA || n != a.CompressedBytes {
		return nil, fmt.Errorf("compressed hash invalid")
	}
	f, e := os.Open(filepath.Join(dir, name+".gz"))
	if e != nil {
		return nil, e
	}
	br := bufio.NewReaderSize(f, 64<<10)
	g, e := gzip.NewReader(br)
	if e != nil {
		f.Close()
		return nil, e
	}
	g.Multistream(false)
	return &gzipInput{g, br, f, a}, nil
}

// ArchiveClosed preserves original raw. No automatic deletion or live/open compression.
func ArchiveClosed(dir string, index int) error {
	h, _, _, e := ReadHeader(dir)
	if e != nil {
		return e
	}
	var m Manifest
	raw, e := readSmall(filepath.Join(dir, "manifest.json"), 1<<20)
	if e != nil || strict(raw, 1<<20, &m) != nil || index < 1 || index > len(m.Segments) {
		return fmt.Errorf("only sealed manifest segments can be archived")
	}
	verification := Recover(dir, RecoverOptions{})
	if !verification.StorageSealed || verification.Issue != "" {
		return fmt.Errorf("session not fully verified")
	}
	seg := m.Segments[index-1]
	source := filepath.Join(dir, segmentName(index))
	digest, n, e := HashFile(source)
	if e != nil || n != seg.Bytes || digest != seg.SHA256 {
		return fmt.Errorf("raw seal mismatch")
	}
	src, e := os.Open(source)
	if e != nil {
		return e
	}
	defer src.Close()
	dest := source + ".gz"
	f, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	g, e := gzip.NewWriterLevel(f, gzip.BestSpeed)
	if e != nil {
		f.Close()
		return e
	}
	_, copyErr := io.CopyBuffer(g, io.LimitReader(src, h.Limits.SegmentBytes+1), make([]byte, 64<<10))
	closeErr := g.Close()
	syncErr := f.Sync()
	fileErr := f.Close()
	if copyErr != nil || closeErr != nil || syncErr != nil || fileErr != nil {
		return fmt.Errorf("gzip save failed")
	}
	zipSHA, zipN, e := HashFile(dest)
	if e != nil {
		return e
	}
	a := Archive{Version, h.Session, index, seg.SHA256, seg.Bytes, zipSHA, zipN}
	receipt, _ := json.Marshal(a)
	w := Writer{dir: dir, options: (Options{}).defaults()}
	return w.writeExclusive(segmentName(index)+".gzip.json", receipt, 8192)
}
