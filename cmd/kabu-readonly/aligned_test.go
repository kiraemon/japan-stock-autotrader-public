package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiraemon/japan-stock-trading-lab/internal/features/capturehistory"
	"github.com/kiraemon/japan-stock-trading-lab/internal/kabu"
	"github.com/kiraemon/japan-stock-trading-lab/internal/marketdata"
	"github.com/kiraemon/japan-stock-trading-lab/internal/telemetry"
)

type cliAlignedClock struct {
	at    time.Time
	waits []time.Time
}

func (c *cliAlignedClock) Now() time.Time { return c.at }
func (c *cliAlignedClock) WaitUntil(ctx context.Context, at time.Time) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.waits = append(c.waits, at)
	if c.at.Before(at) {
		c.at = at
	}
	return nil
}

type cliAlignedSession struct {
	observedSession
	clock                       *cliAlignedClock
	auths, histories, snapshots int
	expireAuth                  bool
	failHistory                 bool
}

func (s *cliAlignedSession) Authenticate(ctx context.Context, _ string) error {
	s.auths++
	if s.expireAuth {
		s.clock.at = marketdata.Clock(marketdata.Date(s.clock.at), 9, 1).Add(11 * time.Second)
	}
	return ctx.Err()
}
func (s *cliAlignedSession) Board(ctx context.Context, symbol string) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.clock.at = s.clock.at.Add(250 * time.Millisecond)
	raw, _ := json.Marshal(map[string]any{"Symbol": strings.Split(symbol, "@")[0], "Exchange": 1, "TradingVolume": 0})
	return raw, nil
}
func (s *cliAlignedSession) TimeAndSales(ctx context.Context, symbol string) (json.RawMessage, error) {
	s.histories++
	s.clock.at = s.clock.at.Add(250 * time.Millisecond)
	if s.failHistory {
		return nil, io.ErrUnexpectedEOF
	}
	raw, _ := json.Marshal(map[string]any{"Symbol": strings.Split(symbol, "@")[0], "Exchange": 1, "TradingPriceCount": 0, "TradingPrice": []any{}})
	return raw, nil
}
func (s *cliAlignedSession) Cash(ctx context.Context) (json.RawMessage, error) {
	// All three histories finish before an ordinary snapshot starts.
	if s.histories != (s.snapshots+1)*3 {
		return nil, io.ErrUnexpectedEOF
	}
	s.snapshots++
	return s.observedSession.Cash(ctx)
}

func cliAlignedFixture() (*cliAlignedClock, []string) {
	date := marketdata.Date(time.Now().Add(48 * time.Hour))
	c := &cliAlignedClock{at: marketdata.Clock(date, 9, 0)}
	return c, []string{"-environment", "test", "-symbols", "7203@1,8306@1,6501@1", "-history-symbols", "7203@1,8306@1,6501@1", "-history-minute-date", date, "-password-stdin"}
}

func TestAlignedCLIUsesSingleAuthenticatedOwnerFor111Rounds(t *testing.T) {
	t.Chdir(t.TempDir())
	c, args := cliAlignedFixture()
	client := &cliAlignedSession{clock: c}
	opens := 0
	var out bytes.Buffer
	err := runWithSessionClock(context.Background(), args, strings.NewReader("synthetic-only\n"), &out, func(kabu.Environment) (session, error) { opens++; return client, nil }, c)
	status := telemetry.Observe("data", "test", time.Now())
	if err != nil || opens != 1 || client.auths != 1 || client.histories != 333 || client.snapshots != 111 || !client.closed || status.State != "completed" || status.Planned != 111 || status.Saved != 111 {
		t.Fatal("aligned lifecycle", err, opens, client.auths, client.histories, client.snapshots, status)
	}
	histories, _ := filepath.Glob("data/history/test/history-*.json")
	rounds, _ := filepath.Glob("data/history/test/aligned-*.json")
	snapshots, _ := filepath.Glob("data/test/snapshot-*.json")
	if len(histories) != 333 || len(rounds) != 111 || len(snapshots) != 111 || len(c.waits) != 111 {
		t.Fatal("wrong archives, startup duplicated, or missing snapshots")
	}
	for i, at := range c.waits {
		want := marketdata.Clock(marketdata.Date(c.waits[0]), 9, 0).Add(time.Duration(i+1)*time.Minute + 2*time.Second)
		if !at.Equal(want) {
			t.Fatal("schedule drift")
		}
	}
	if !strings.Contains(out.String(), "conversion_included=false / strategy_eligible=false") {
		t.Fatal("quality promoted")
	}
	t.Log("synthetic=true / authenticated_sessions=1 / aligned_rounds=111 / raw_archives=333 / normal_snapshots=111 / closed=true")
}

func TestAlignedCLIFailureRetainsPartialAndStopsNormalCollection(t *testing.T) {
	t.Chdir(t.TempDir())
	c, args := cliAlignedFixture()
	client := &cliAlignedSession{clock: c, failHistory: true}
	err := runWithSessionClock(context.Background(), args, strings.NewReader("synthetic-only\n"), &bytes.Buffer{}, func(kabu.Environment) (session, error) { return client, nil }, c)
	status := telemetry.Observe("data", "test", time.Now())
	files, _ := filepath.Glob("data/history/test/history-*.json")
	rounds, _ := filepath.Glob("data/history/test/aligned-*.json")
	if err == nil || client.histories != 1 || client.snapshots != 0 || !client.closed || len(files) != 1 || len(rounds) != 1 || status.State != "failed" {
		t.Fatal("failure retried or normal collection continued")
	}
	raw, _ := os.ReadFile(files[0])
	var archive capturehistory.Archive
	if json.Unmarshal(raw, &archive) != nil || archive.Complete {
		t.Fatal("partial archive lost")
	}
	t.Log("synthetic=true / history_failure=terminal / partial_archive=retained / next_symbol=false / normal_snapshot=false / closed=true")
}

func TestAlignedCLIRejectsInvalidSchedulesBeforeSessionAndPassword(t *testing.T) {
	clock, args := cliAlignedFixture()
	for _, extra := range [][]string{{"-count", "1"}, {"-interval", "1s"}, {"-history-every", "0"}, {"-history-symbols", "7203@1"}, {"-symbols", "9433@1,8306@1,6501@1"}, {"-history-minute-date", "bad"}, {"-history-minute-date", "2020-01-01"}} {
		err := runWithSessionClock(context.Background(), append(append([]string{}, args...), extra...), unreadableInput{t}, &bytes.Buffer{}, func(kabu.Environment) (session, error) { t.Fatal("session opened before validation"); return nil, nil }, clock)
		if err == nil {
			t.Fatal("invalid schedule accepted", extra)
		}
	}
}

type alignedDelayedPassword struct{ clock *cliAlignedClock }

func (r alignedDelayedPassword) Read(p []byte) (int, error) {
	r.clock.at = r.clock.at.Add(2 * time.Minute)
	return copy(p, []byte("synthetic-only\n")), io.EOF
}

func TestAlignedCLIRechecksDeadlineBeforeAndAfterAuthentication(t *testing.T) {
	for _, delayedPassword := range []bool{true, false} {
		t.Run(map[bool]string{true: "password_delay", false: "authentication_delay"}[delayedPassword], func(t *testing.T) {
			t.Chdir(t.TempDir())
			c, args := cliAlignedFixture()
			client := &cliAlignedSession{clock: c, expireAuth: !delayedPassword}
			var input io.Reader = strings.NewReader("synthetic-only\n")
			if delayedPassword {
				input = alignedDelayedPassword{c}
			}
			err := runWithSessionClock(context.Background(), args, input, &bytes.Buffer{}, func(kabu.Environment) (session, error) { return client, nil }, c)
			wantAuth := 1
			if delayedPassword {
				wantAuth = 0
			}
			if err == nil || client.auths != wantAuth || client.histories != 0 || client.snapshots != 0 || !client.closed {
				t.Fatal("missed first deadline not rechecked")
			}
		})
	}
}
