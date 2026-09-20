package scheduler

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestManualWarmup(t *testing.T, s *Scheduler, c *clock, runner Runner, waitFn func(context.Context, time.Duration) bool, randomIntn func(int) int) *ManualWarmup {
	t.Helper()
	m, err := NewManualWarmup(s.store, Options{
		Now:         c.Now,
		Runner:      runner,
		Wait:        waitFn,
		RandomIntn:  randomIntn,
		ResolveCLI:  func(*string) (string, error) { return "synthetic-cli", nil },
		ValidateCLI: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestManualAccountRunsImmediatelyWithoutChangingScheduledState(t *testing.T) {
	s, c := newTestScheduler(t, runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) {
		return ProcessOutput{}, errors.New("scheduled runner should not be used")
	}), 1)
	before := s.Status()
	var calls, waits atomic.Int32
	m := newTestManualWarmup(t, s, c, runFunc(func(_ context.Context, cmd *exec.Cmd, timeout time.Duration) (ProcessOutput, error) {
		calls.Add(1)
		if timeout != AccountTimeout || !strings.Contains(cmd.Dir, "manual-runtime") {
			return ProcessOutput{}, errors.New("manual invocation was not isolated")
		}
		prompt := cmd.Args[len(cmd.Args)-1]
		found := false
		for _, candidate := range promptPool(c.Now().Format("2006-01-02")) {
			found = found || prompt == candidate
		}
		if !found {
			return ProcessOutput{}, errors.New("manual invocation did not use a random task")
		}
		return successOutput(), nil
	}), func(context.Context, time.Duration) bool {
		waits.Add(1)
		return true
	}, func(int) int { return 0 })

	state, err := s.store.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.WarmupAccount(state.Accounts[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || waits.Load() != 0 {
		t.Fatalf("calls=%d waits=%d, want immediate single call", calls.Load(), waits.Load())
	}
	if after := s.Status(); !reflect.DeepEqual(after, before) {
		t.Fatalf("manual task changed scheduled state: before=%+v after=%+v", before, after)
	}
	entries, err := os.ReadDir(m.runtime)
	if err != nil || len(entries) != 0 {
		t.Fatalf("manual temporary credentials remained: entries=%v err=%v", entries, err)
	}
}

func TestManualBatchUsesScheduledDelayRulesWithoutChangingScheduledState(t *testing.T) {
	s, c := newTestScheduler(t, runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) {
		return ProcessOutput{}, errors.New("scheduled runner should not be used")
	}), 2)
	before := s.Status()
	var calls atomic.Int32
	waits := make(chan time.Duration, 2)
	delayCalls := 0
	m := newTestManualWarmup(t, s, c, runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) {
		calls.Add(1)
		return successOutput(), nil
	}), func(_ context.Context, delay time.Duration) bool {
		waits <- delay
		return true
	}, func(n int) int {
		if n == int((MaxAccountDelay-MinAccountDelay)/time.Second)+1 {
			delayCalls++
			if delayCalls == 2 {
				return n - 1
			}
		}
		return 0
	})

	if err := m.WarmupAll(nil); err != nil {
		t.Fatal(err)
	}
	first, second := <-waits, <-waits
	if !((first == MinAccountDelay && second == MaxAccountDelay) || (first == MaxAccountDelay && second == MinAccountDelay)) {
		t.Fatalf("waits=%v, %v; want %v and %v", first, second, MinAccountDelay, MaxAccountDelay)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
	if after := s.Status(); !reflect.DeepEqual(after, before) {
		t.Fatalf("manual batch changed scheduled state: before=%+v after=%+v", before, after)
	}
}
