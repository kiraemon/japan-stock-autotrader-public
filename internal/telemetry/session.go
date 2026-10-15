// Package telemetry owns the secret-free collector status contract shared by
// process startup and observation. It has no broker or feature dependencies.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const StaleAfter = 20 * time.Second

type Record struct {
	Version     int        `json:"schema_version"`
	RunID       string     `json:"run_id"`
	Environment string     `json:"environment"`
	State       string     `json:"state"`
	Phase       string     `json:"phase"`
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	LastSavedAt *time.Time `json:"last_saved_at,omitempty"`
	Planned     int        `json:"planned"`
	Saved       int        `json:"saved"`
	Complete    int        `json:"complete"`
	Partial     int        `json:"partial"`
}

// Observation is safe for browser output. Invalid file contents are never echoed.
type Observation struct {
	State     string     `json:"state"`
	Phase     string     `json:"phase"`
	UpdatedAt *time.Time `json:"updated_at"`
	StartedAt *time.Time `json:"started_at"`
	Saved     int        `json:"saved"`
	Planned   int        `json:"planned"`
}

type Session struct {
	mu      sync.Mutex
	root    string
	record  Record
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	failure error
}

func Start(ctx context.Context, root, environment string, planned int) (*Session, error) {
	return start(ctx, root, environment, planned, 5*time.Second)
}

func start(parent context.Context, root, environment string, planned int, interval time.Duration) (*Session, error) {
	if !validEnvironment(environment) || planned < 1 || planned > 10000 {
		return nil, errors.New("状態記録の設定が不正です")
	}
	if parent.Err() != nil {
		return nil, parent.Err()
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, errors.New("実行IDを生成できません")
	}
	ctx, cancel := context.WithCancel(parent)
	now := time.Now().UTC()
	s := &Session{root: root, ctx: ctx, cancel: cancel, done: make(chan struct{}), record: Record{Version: 1, RunID: hex.EncodeToString(id[:]), Environment: environment, State: "active", Phase: "starting", StartedAt: now, UpdatedAt: now, Planned: planned}}
	if err := write(filepath.Join(root, "runtime", environment+".json"), s.record); err != nil {
		cancel()
		return nil, err
	}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				if s.failure == nil {
					s.record.UpdatedAt = time.Now().UTC()
					if err := write(filepath.Join(s.root, "runtime", environment+".json"), s.record); err != nil {
						s.failure = err
						s.cancel()
					}
				}
				s.mu.Unlock()
			}
		}
	}()
	return s, nil
}

func (s *Session) Context() context.Context { return s.ctx }

func (s *Session) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.failure }

func (s *Session) Phase(phase string) error {
	if !validPhase(phase) {
		return errors.New("状態の遷移先が不正です")
	}
	return s.update(func(r *Record) { r.Phase = phase })
}

func (s *Session) Saved(complete bool) error {
	return s.update(func(r *Record) {
		r.Saved++
		if complete {
			r.Complete++
		} else {
			r.Partial++
		}
		now := time.Now().UTC()
		r.LastSavedAt = &now
	})
}

func (s *Session) update(change func(*Record)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	if s.record.State != "active" {
		return errors.New("終了した状態記録は更新できません")
	}
	change(&s.record)
	s.record.UpdatedAt = time.Now().UTC()
	if err := write(filepath.Join(s.root, "runtime", s.record.Environment+".json"), s.record); err != nil {
		s.failure = err
		s.cancel()
		return err
	}
	return nil
}

// Finish stops the heartbeat before publishing final status and a JST-day summary.
// The caller must still own the broker startup guard throughout this operation.
func (s *Session) Finish(state string) error {
	s.cancel()
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if !terminal(state) {
		return errors.New("終了状態が不正です")
	}
	if s.record.State != "active" {
		return errors.New("状態記録は既に終了しています")
	}
	if s.failure != nil {
		state = "failed"
	}
	now := time.Now().UTC()
	s.record.State = state
	s.record.UpdatedAt = now
	s.record.FinishedAt = &now
	if err := write(filepath.Join(s.root, "runtime", s.record.Environment+".json"), s.record); err != nil {
		return err
	}
	day := s.record.StartedAt.In(time.FixedZone("JST", 9*3600)).Format("2006-01-02")
	if err := write(filepath.Join(s.root, "sessions", day, "run-"+s.record.RunID+".json"), s.record); err != nil {
		s.record.State = "failed"
		_ = write(filepath.Join(s.root, "runtime", s.record.Environment+".json"), s.record)
		return err
	}
	return s.failure
}

func write(path string, value Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("状態記録の保存先を作成できません")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".status-*.tmp")
	if err != nil {
		return errors.New("状態記録を作成できません")
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err := json.NewEncoder(f).Encode(value); err != nil {
		f.Close()
		return errors.New("状態記録を生成できません")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errors.New("状態記録を書き込めません")
	}
	if err := f.Close(); err != nil {
		return errors.New("状態記録を閉じられません")
	}
	if err := publish(temp, path); err != nil {
		return &publishError{cause: err}
	}
	return nil
}

type publishError struct{ cause error }

func (e *publishError) Error() string { return "状態記録を確定できません" }
func (e *publishError) Unwrap() error { return e.cause }

func Observe(root, environment string, now time.Time) Observation {
	unknown := Observation{State: "unavailable"}
	if !validEnvironment(environment) {
		return unknown
	}
	path := filepath.Join(root, "runtime", environment+".json")
	unlock, err := lockFile(path)
	if err != nil {
		return unknown
	}
	defer unlock()
	f, err := openStatus(path)
	if os.IsNotExist(err) {
		return Observation{State: "unrecorded"}
	}
	if err != nil {
		return unknown
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return unknown
	}
	var r Record
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil || decoder.Decode(new(any)) != io.EOF || !valid(r, environment) {
		return unknown
	}
	state := r.State
	if r.UpdatedAt.After(now.Add(2 * time.Second)) {
		state = "clock_error"
	} else if state == "active" && now.Sub(r.UpdatedAt) > StaleAfter {
		state = "stale"
	}
	return Observation{State: state, Phase: r.Phase, UpdatedAt: &r.UpdatedAt, StartedAt: &r.StartedAt, Saved: r.Saved, Planned: r.Planned}
}

func valid(r Record, environment string) bool {
	if r.Version != 1 || r.Environment != environment || len(r.RunID) != 32 || !validPhase(r.Phase) || (r.State != "active" && !terminal(r.State)) {
		return false
	}
	if _, err := hex.DecodeString(r.RunID); err != nil {
		return false
	}
	if r.StartedAt.IsZero() || r.UpdatedAt.Before(r.StartedAt) || r.Planned < 1 || r.Planned > 10000 || r.Saved < 0 || r.Saved > r.Planned || r.Complete < 0 || r.Partial < 0 || r.Complete+r.Partial != r.Saved {
		return false
	}
	if (r.Saved > 0) != (r.LastSavedAt != nil) {
		return false
	}
	if r.LastSavedAt != nil && (r.LastSavedAt.Before(r.StartedAt) || r.LastSavedAt.After(r.UpdatedAt)) {
		return false
	}
	if r.State == "active" {
		return r.FinishedAt == nil
	}
	if r.State == "completed" && (r.Complete != r.Planned || r.Partial != 0) {
		return false
	}
	return r.FinishedAt != nil && r.FinishedAt.Equal(r.UpdatedAt)
}
func validEnvironment(s string) bool { return s == "test" || s == "production" }
func validPhase(s string) bool {
	switch s {
	case "starting", "credentials", "authenticating", "collecting", "waiting":
		return true
	}
	return false
}
func terminal(s string) bool {
	return s == "completed" || s == "scheduled_end" || s == "interrupted" || s == "failed"
}
