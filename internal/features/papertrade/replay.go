package papertrade

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kiraemon/japan-stock-autotrader/internal/marketdata"
	"github.com/kiraemon/japan-stock-autotrader/internal/strategy/r001"
)

const ReplayVersion = "synthetic-strategy-ledger-replay-v0.1"

// These are fixed synthetic fixture assumptions, never live position limits.
const SyntheticEntryQuantity int64 = 100
const SyntheticPartialQuantity int64 = 40
const SyntheticEntryTTLSeconds = 60

type FillModel string

const (
	FullFill    FillModel = "full_next_open"
	PartialFill FillModel = "partial_40_then_cancel"
	Unfilled    FillModel = "unfilled_then_cancel"
	UnknownFill FillModel = "unknown_no_retry"
)

type ReplayReport struct {
	Version                  string      `json:"version"`
	Rule                     string      `json:"rule"`
	Model                    FillModel   `json:"fill_model"`
	SourceSHA256             string      `json:"source_sha256"`
	ForwardTest              bool        `json:"forward_test"`
	LiveSubmissionCapability bool        `json:"live_submission_capability"`
	Days                     []ReplayDay `json:"days"`
}

type ReplayDay struct {
	Date       string         `json:"date_jst"`
	Status     string         `json:"status"`
	Reason     string         `json:"reason,omitempty"`
	Observed   int            `json:"observed_closed_minutes"`
	Decision   *r001.Decision `json:"decision,omitempty"`
	ExitReason string         `json:"exit_reason,omitempty"`
	Final      Snapshot       `json:"final"`
}

// Replay is a synthetic-only wiring experiment. Each minute exposes its open
// before its completed OHLCV. Quantities and immediate cancellation are fixed
// test assumptions, not real account settings or a claim about liquidity.
func Replay(ctx context.Context, input marketdata.Dataset, direction r001.Direction, variant r001.Variant, model FillModel) (ReplayReport, error) {
	r := ReplayReport{Version: ReplayVersion, Rule: r001.VersionFor(direction, variant), Model: model, SourceSHA256: input.Source.SHA256, Days: []ReplayDay{}}
	if input.Origin != "synthetic" || input.Environment != "test" || input.Symbol != "0000@1" || len(input.Days) != 1 || !r001.Valid(direction) || !r001.ValidVariant(variant) || (model != FullFill && model != PartialFill && model != Unfilled && model != UnknownFill) {
		return r, errors.New("戦略台帳再生は仮銘柄の合成データと固定モデルだけを受け付けます")
	}
	if err := marketdata.Validate(input); err != nil {
		return r, err
	}
	for _, day := range input.Days {
		if ctx.Err() != nil {
			return r, errors.New("戦略台帳再生を中断しました")
		}
		result, err := replayDay(ctx, day, direction, variant, model)
		if err != nil {
			return r, err
		}
		r.Days = append(r.Days, result)
	}
	return r, nil
}

func replayDay(ctx context.Context, day marketdata.Day, direction r001.Direction, variant r001.Variant, model FillModel) (ReplayDay, error) {
	s, _ := New(direction)
	scanner, _ := r001.NewScanner(day.Date, direction, variant)
	r := ReplayDay{Date: day.Date, Status: "completed"}
	sequence := 0
	emit := func(at time.Time, kind, order string, quantity, price int64) error {
		sequence++
		e := Event{ID: fmt.Sprintf("e%d", sequence), At: at, Kind: kind, OrderID: order}
		if kind == "fill" {
			e.CumulativeFilled, e.AveragePrice = quantity, price
		} else {
			e.Quantity = quantity
		}
		if outcome := s.Apply(e); !outcome.Accepted {
			return errors.New("戦略台帳の合成イベントが拒否されました")
		}
		return nil
	}
	stop := func(at time.Time, reason string) error {
		r.Status, r.Reason = "stopped", reason
		return emit(at, "stop", "", 0, 0)
	}
	at := marketdata.Clock(day.Date, 9, 0)
	var entryAt time.Time
	var boundary int64
	pendingEntry, entryRemainder := false, false
	pendingExit := ""
	if len(day.Issues) > 0 {
		if err := stop(at, "day_quality_pending"); err != nil {
			return r, err
		}
		r.Final = s.Snapshot()
		return r, nil
	}
	for minute := 0; minute < 111; minute++ {
		if ctx.Err() != nil {
			return r, errors.New("戦略台帳再生を中断しました")
		}
		at = marketdata.Clock(day.Date, 9, 0).Add(time.Duration(minute) * time.Minute)
		if minute >= len(day.Bars) || !day.Bars[minute].Start.Equal(at) {
			if err := stop(at, "missing_minute"); err != nil {
				return r, err
			}
			break
		}
		bar := day.Bars[minute]
		// Only the open is used at this clock phase. Future HLCV cannot change a fill.
		if pendingEntry {
			pendingEntry = false
			entryAt = at
			switch model {
			case UnknownFill:
				if err := emit(at, "transport_unknown", "entry", 0, 0); err != nil {
					return r, err
				}
				r.Status, r.Reason = "stopped", "execution_unknown"
			case FullFill, PartialFill:
				quantity := SyntheticEntryQuantity
				if model == PartialFill {
					quantity = SyntheticPartialQuantity
				}
				if err := emit(at, "fill", "entry", quantity, bar.Open); err != nil {
					return r, err
				}
			}
			entryRemainder = model == PartialFill || model == Unfilled
		}
		if r.Status == "stopped" {
			break
		}
		if entryRemainder && !at.Before(entryAt.Add(SyntheticEntryTTLSeconds*time.Second)) {
			// One-minute synthetic TTL; cancellation succeeds immediately by assumption.
			if err := emit(at, "cancel_requested", "entry", 0, 0); err != nil {
				return r, err
			}
			if err := emit(at, "canceled", "entry", 0, 0); err != nil {
				return r, err
			}
			entryRemainder = false
		}
		position := s.Snapshot().OpenQuantity
		if position > 0 && !at.Before(entryAt.Add(r001.MaximumHold)) {
			pendingExit = "maximum_hold"
		}
		if position > 0 && pendingExit != "" {
			if err := emit(at, "close_intent", "exit", position, 0); err != nil {
				return r, err
			}
			if err := emit(at, "accepted", "exit", 0, 0); err != nil {
				return r, err
			}
			if err := emit(at, "fill", "exit", position, bar.Open); err != nil {
				return r, err
			}
			r.ExitReason, pendingExit = pendingExit, ""
		}
		closedAt := at.Add(time.Minute)
		decision, err := scanner.Observe(closedAt, bar)
		if err != nil {
			if err := stop(closedAt, "closed_bar_rejected"); err != nil {
				return r, err
			}
			break
		}
		r.Observed++
		if decision != nil {
			r.Decision = decision
			if decision.Accepted {
				boundary = decision.Boundary
				if err := emit(closedAt, "open_intent", "entry", SyntheticEntryQuantity, 0); err != nil {
					return r, err
				}
				if err := emit(closedAt, "accepted", "entry", 0, 0); err != nil {
					return r, err
				}
				pendingEntry = true
			} else if variant == r001.B && (decision.Reason == "volume_input_unavailable" || decision.Reason == "volume_baseline_zero") {
				if err := stop(closedAt, decision.Reason); err != nil {
					return r, err
				}
				break
			}
		}
		if s.Snapshot().OpenQuantity > 0 && r001.Reversed(direction, bar.Close, boundary) {
			pendingExit = "range_reversal"
		}
	}
	r.Final = s.Snapshot()
	return r, nil
}
