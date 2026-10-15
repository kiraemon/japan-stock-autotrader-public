package instanceguard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func testName(t *testing.T) string {
	return fmt.Sprintf(`Global\JapanStockAutotrader.Test.%d.%d.%s`, os.Getpid(), time.Now().UnixNano(), strings.ReplaceAll(t.Name(), "/", "."))
}

func TestGuardExclusionAndRelease(t *testing.T) {
	name := testName(t)
	first, err := Acquire(name)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Acquire(name); !errors.Is(err, ErrHeld) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second acquire: %v", err)
	}
	first.Close()
	first.Close() // idempotent; must not close another owner's handle
	third, err := Acquire(name)
	if err != nil {
		t.Fatal(err)
	}
	third.Close()
}

// The helper owns only an isolated test gate, never an API session.
func TestGuardHelper(t *testing.T) {
	name := os.Getenv("JSA_TEST_GUARD")
	if name == "" {
		return
	}
	guard, err := Acquire(name)
	if err != nil {
		os.Exit(2)
	}
	defer guard.Close()
	fmt.Println("ready")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestGuardReleaseOnProcessExit(t *testing.T) {
	for _, kill := range []bool{false, true} {
		t.Run(fmt.Sprint("kill=", kill), func(t *testing.T) {
			name := testName(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestGuardHelper$")
			cmd.Env = append(os.Environ(), "JSA_TEST_GUARD="+name)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil || strings.TrimSpace(line) != "ready" {
				t.Fatalf("helper not ready: %q %v", line, err)
			}
			if guard, err := Acquire(name); !errors.Is(err, ErrHeld) {
				if guard != nil {
					guard.Close()
				}
				t.Fatalf("child ownership ignored: %v", err)
			}
			if kill {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				fmt.Fprintln(input)
				input.Close()
			}
			err = cmd.Wait()
			if !kill && err != nil {
				t.Fatal(err)
			}
			guard, err := Acquire(name)
			if err != nil {
				t.Fatalf("gate remained after exit: %v", err)
			}
			guard.Close()
		})
	}
}
