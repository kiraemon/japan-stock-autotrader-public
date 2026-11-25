package paperrecordingv3

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCompressionTruncationTrailingBombAndReceiptBound(t *testing.T) {
	for _, fault := range []string{"truncated", "trailing", "bomb", "raw_receipt"} {
		t.Run(fault, func(t *testing.T) {
			dir, w := newSmall(t, smallOptions())
			appendN(t, w, 2)
			finish(t, w)
			if e := ArchiveClosed(dir, 1); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(dir, segmentName(1)+".gz")
			data, _ := os.ReadFile(path)
			switch fault {
			case "truncated":
				data = data[:len(data)-5]
			case "trailing":
				data = append(data, []byte("extra-stream")...)
			case "bomb":
				var b bytes.Buffer
				g := gzip.NewWriter(&b)
				g.Write(bytes.Repeat([]byte{' '}, int(w.options.Limits.SegmentBytes)*4))
				g.Close()
				data = b.Bytes()
			}
			if e := os.WriteFile(path, data, 0600); e != nil {
				t.Fatal(e)
			}
			receiptPath := filepath.Join(dir, segmentName(1)+".gzip.json")
			var receipt Archive
			raw, _ := os.ReadFile(receiptPath)
			json.Unmarshal(raw, &receipt)
			receipt.CompressedSHA = Hash(data)
			receipt.CompressedBytes = int64(len(data))
			if fault == "raw_receipt" {
				receipt.RawSHA = Hash([]byte("wrong"))
			}
			raw, _ = json.Marshal(receipt)
			os.WriteFile(receiptPath, raw, 0600)
			r := Recover(dir, RecoverOptions{PreferCompressed: true})
			if r.Issue == "" || r.RunComplete || r.RestartAllowed || r.VerifiedBytes > w.options.Limits.SegmentBytes {
				t.Fatal("compressed invalid accepted or bound exceeded", fault, r)
			}
		})
	}
}
