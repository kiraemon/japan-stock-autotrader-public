package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/papertrade"
)

// Each CLI invocation has its own process and memory. The test child calls
// main so flag handling, output and exit status are the same as the binary.
func TestPaperRecoveryCLIChild(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			main()
			return
		}
	}
}

func TestPaperRecoveryCommandsAcrossProcesses(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		dir, request string
		original     []byte
	}
	command := func(t *testing.T, f fixture, action, request string, success bool, want ...string) {
		t.Helper()
		args := []string{"-test.run=^TestPaperRecoveryCLIChild$", "--", "paper-recovery", "-dir", f.dir}
		if request != "" {
			if err := os.WriteFile(f.request, []byte(request), 0600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "-request", f.request)
		}
		raw, err := exec.CommandContext(t.Context(), executable, append(args, action)...).CombinedOutput()
		if success != (err == nil) {
			t.Fatalf("%s success=%t, wanted %t: %v\n%s", action, err == nil, success, err, raw)
		}
		for _, token := range want {
			if !strings.Contains(string(raw), token) {
				t.Errorf("%s missing %q in %s", action, token, raw)
			}
		}
		if f.original != nil {
			current, err := os.ReadFile(filepath.Join(f.dir, paperRecoveryCheckpoint))
			if err != nil || !bytes.Equal(current, f.original) {
				t.Fatalf("%s changed the original checkpoint: %v", action, err)
			}
		}
	}
	prepare := func(t *testing.T) fixture {
		t.Helper()
		root := t.TempDir()
		f := fixture{dir: filepath.Join(root, "records"), request: filepath.Join(root, "request.json")}
		command(t, f, "prepare", "", true, "open_quantity=60", "position_known=false", "reconciliation_required=true", "active_orders=1", "processed_fills=entry-fill,quote-fill-6")
		f.original, err = os.ReadFile(filepath.Join(f.dir, paperRecoveryCheckpoint))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	reconcile := func(t *testing.T, id string, quantity int64, state string, observed *int64) string {
		t.Helper()
		raw, err := json.Marshal(papertrade.RecoveryRequest{ID: id, Kind: "reconcile", OrderID: "exit",
			CumulativeFilled: quantity, AveragePrice: 990000, ResolvedState: state, ObservedPosition: observed})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	order := func(id, kind string, quantity int64) string {
		return fmt.Sprintf(`{"id":%q,"kind":%q,"quantity":%d}`, id, kind, quantity)
	}

	t.Run("partial_recovery", func(t *testing.T) {
		f := prepare(t)
		command(t, f, "inspect", "", true, "open_quantity=60", "active_orders=1", "reconciliation_required=true", "processed_fills=entry-fill,quote-fill-6")
		command(t, f, "apply", `{"id":"dupe","kind":"repeat_fill","fill_event_id":"quote-fill-6"}`, true, "accepted=true duplicate=true", "open_quantity=60")
		command(t, f, "apply", reconcile(t, "missing", 100, "filled", nil), false, "reason=observed_position_required", "open_quantity=60", "position_known=false")
		wrong := int64(29)
		command(t, f, "apply", reconcile(t, "wrong", 70, "canceled", &wrong), false, "reason=reconciled_position_mismatch", "open_quantity=60", "reconciliation_required=true")
		command(t, f, "apply", order("pending-open", "open", 100), false, "reason=reconciliation_required", "active_orders=1")
		command(t, f, "apply", order("pending-close", "close", 1), false, "reason=reconciliation_required", "open_quantity=60")
		remaining := int64(30)
		command(t, f, "apply", reconcile(t, "confirmed", 70, "canceled", &remaining), true, "open_quantity=30", "position_known=true", "reconciliation_required=false", "new_entries_halted=true", "active_orders=0")
		command(t, f, "apply", order("blocked-open", "open", 100), false, "reason=new_entry_blocked", "open_quantity=30")
		command(t, f, "apply", order("oversized", "close", 31), false, "reason=close_exceeds_unreserved_position", "open_quantity=30")
		closeRequest := order("reduce", "close", 30)
		command(t, f, "apply", closeRequest, true, "virtual_fill=30", "open_quantity=0", "active_orders=0", "new_entries_halted=true")
		command(t, f, "apply", closeRequest, true, "receipt_reused=true", "open_quantity=0")
		command(t, f, "apply", order("reduce", "close", 1), false, "同じ復旧操作IDに異なる入力")
		command(t, f, "inspect", "", true, "open_quantity=0", "position_known=true", "active_orders=0")
		if _, err := os.Stat(filepath.Join(f.dir, recoveryReceiptName(10))); !os.IsNotExist(err) {
			t.Fatalf("a repeated operation produced another receipt: %v", err)
		}
	})

	t.Run("flat_and_corrupt_history", func(t *testing.T) {
		f := prepare(t)
		zero := int64(0)
		command(t, f, "apply", reconcile(t, "all-filled", 100, "filled", &zero), true, "open_quantity=0", "position_known=true", "reconciliation_required=false", "new_entries_halted=true", "active_orders=0")
		command(t, f, "apply", order("no-additional-close", "close", 1), false, "reason=close_exceeds_unreserved_position")
		command(t, f, "inspect", "", true, "open_quantity=0", "position_known=true")
		path := filepath.Join(f.dir, recoveryReceiptName(1))
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(raw, []byte(`"observed_position_quantity":0`)) {
			t.Fatalf("explicit zero missing from saved evidence: %v, %s", err, raw)
		}
		raw = bytes.Replace(raw, []byte(`"observed_position_quantity":0`), []byte(`"observed_position_quantity":1`), 1)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		command(t, f, "inspect", "", false, "復旧操作を再生した結果が保存記録と一致しません")
	})
}

func TestPaperRecoveryPublicationDoesNotReplaceAnExistingRecord(t *testing.T) {
	dir := t.TempDir()
	name := recoveryReceiptName(1)
	original := []byte("first")
	if err := publishPaperRecoveryFile(dir, name, original); err != nil {
		t.Fatal(err)
	}
	if err := publishPaperRecoveryFile(dir, name, []byte("second")); err == nil {
		t.Fatal("replaced a published receipt")
	}
	if current, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(current, original) {
		t.Fatalf("published receipt changed: %s, %v", current, err)
	}
}
