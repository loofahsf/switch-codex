package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"switch-codex/internal/store"
)

type clock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *clock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.value }
func (c *clock) Set(t time.Time) { c.mu.Lock(); c.value = t; c.mu.Unlock() }

type runFunc func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error)

func (f runFunc) Run(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (ProcessOutput, error) {
	return f(ctx, cmd, timeout)
}
func successOutput() ProcessOutput {
	return ProcessOutput{Success: true, Stdout: []byte("{\"item\":{\"id\":\"a\",\"type\":\"agent_message\",\"text\":\"synthetic response\"},\"type\":\"item.completed\"}\n{\"type\":\"turn.completed\"}\n")}
}
func newTestScheduler(t *testing.T, runner Runner, count int) (*Scheduler, *clock) {
	t.Helper()
	root := t.TempDir()
	st := store.New(filepath.Join(root, "data"), filepath.Join(root, "home", ".codex", "auth.json"))
	if err := st.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"}[:count] {
		if _, err := st.AddAccount(name, `{"tokens":{"account_id":"`+name+`","refresh_token":"synthetic-`+name+`"}}`); err != nil {
			t.Fatal(err)
		}
	}
	c := &clock{value: time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)}
	s, err := New(st, Options{
		Now: c.Now, Runner: runner,
		RandomIntn:  func(int) int { return 0 },
		Wait:        func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil },
		ResolveCLI:  func(*string) (string, error) { return "synthetic-cli", nil },
		ValidateCLI: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err = s.Save(context.Background(), Settings{Enabled: true, Time: ptr("09:00")}); err != nil {
		t.Fatal(err)
	}
	return s, c
}
func TestNextRunCalendarRules(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, at, schedule, want string
		last                     *string
	}{
		{"before", "2026-09-09T08:00:00-04:00", "09:00:00", "2026-09-09T09:00:00-04:00", nil},
		{"startup-exact", "2026-09-09T09:00:00-04:00", "09:00:00", "2026-09-10T09:00:00-04:00", nil},
		{"startup-missed", "2026-09-09T09:00:01-04:00", "09:00:00", "2026-09-10T09:00:00-04:00", nil},
		{"claimed", "2026-09-09T08:00:00-04:00", "09:00:00", "2026-09-10T09:00:00-04:00", ptr("2026-09-09")},
		{"clock-back", "2026-09-08T08:00:00-04:00", "09:00:00", "2026-09-10T09:00:00-04:00", ptr("2026-09-09")},
		{"spring-gap", "2026-03-08T00:00:00-05:00", "02:30:00", "2026-03-09T02:30:00-04:00", nil},
		{"fall-first", "2026-11-01T00:00:00-04:00", "01:30:00", "2026-11-01T01:30:00-04:00", nil},
		{"fall-no-repeat", "2026-11-01T01:45:00-04:00", "01:30:00", "2026-11-02T01:30:00-05:00", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, _ := time.Parse(time.RFC3339, tc.at)
			got := nextRun(Settings{Enabled: true, Time: &tc.schedule}, at.In(loc), tc.last)
			if got == nil || got.Format(time.RFC3339) != tc.want {
				t.Fatalf("got %v want %s", got, tc.want)
			}
		})
	}
}

func TestSaveSchedulesTodayUntilTheConfiguredTimePasses(t *testing.T) {
	s, c := newTestScheduler(t, runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) {
		return successOutput(), nil
	}), 0)

	// At 08:00, moving the daily call to noon still means noon today.
	noon := "12:00"
	if _, err := s.Save(context.Background(), Settings{Enabled: true, Time: &noon}); err != nil {
		t.Fatal(err)
	}
	if got := s.Status().NextRunAt; got == nil || *got != "2026-09-09T12:00:00Z" {
		t.Fatalf("future time scheduled for %v, want today at noon", got)
	}

	// Once noon has passed, the same setting rolls forward exactly one day.
	c.Set(time.Date(2026, 9, 9, 12, 0, 1, 0, time.UTC))
	if _, err := s.Save(context.Background(), Settings{Enabled: true, Time: &noon}); err != nil {
		t.Fatal(err)
	}
	if got := s.Status().NextRunAt; got == nil || *got != "2026-09-10T12:00:00Z" {
		t.Fatalf("past time scheduled for %v, want tomorrow at noon", got)
	}
}
func TestSettingsCompatibility(t *testing.T) {
	s := Settings{Enabled: true, Time: ptr("23:59"), CLIPath: ptr("  /some/cli  ")}
	if err := s.Validate(); err != nil || *s.Time != "23:59:00" || *s.CLIPath != "/some/cli" {
		t.Fatalf("%+v: %v", s, err)
	}
	for _, bad := range []string{"9:00", "24:00:00", "00:00:60", "12:34:56Z", ""} {
		s.Time = &bad
		if s.Validate() == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	var legacy savedState
	if err := json.Unmarshal([]byte(`{"settings":{"enabled":false,"time":"08:00"},"lastRun":{"startedAt":"2026-09-08T08:00:00Z","finishedAt":null,"accounts":[{"accountId":"old","accountName":"Old","status":"running"}]}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	interrupt(&legacy, time.Now())
	a := legacy.LastRun.Accounts[0]
	if a.Status != Interrupted || a.ScheduledAt != nil || a.Prompt != nil || a.Response != nil || legacy.LastRun.FinishedAt == nil {
		t.Fatalf("%+v", a)
	}
	if !legacy.Settings.AutoSyncAuth {
		t.Fatal("legacy settings did not default autoSyncAuth to true")
	}
	var explicitlyDisabled Settings
	if err := json.Unmarshal([]byte(`{"enabled":false,"autoSyncAuth":false}`), &explicitlyDisabled); err != nil {
		t.Fatal(err)
	}
	if explicitlyDisabled.AutoSyncAuth {
		t.Fatal("explicit autoSyncAuth=false was not preserved")
	}
}
func TestGraceSleepTimezoneAndDailyClaim(t *testing.T) {
	for _, tc := range []struct {
		name              string
		delay             time.Duration
		sleep, zone, want bool
	}{
		{"on-time", 0, false, false, true}, {"grace-boundary", Grace, false, false, true}, {"late", Grace + time.Second, false, false, false}, {"wake-missed", time.Minute, true, false, false}, {"timezone-changed", time.Minute, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s, c := newTestScheduler(t, runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) {
				calls.Add(1)
				return successOutput(), nil
			}), 1)
			if tc.sleep {
				s.Sleep()
			}
			at := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC).Add(tc.delay)
			if tc.zone {
				at = at.In(time.FixedZone("new", 3600))
			}
			c.Set(at)
			if tc.sleep {
				s.Tick()
				if calls.Load() != 0 {
					t.Fatal("ran asleep")
				}
				s.Wake()
			}
			s.Tick()
			s.wg.Wait()
			s.Tick()
			s.wg.Wait()
			if (calls.Load() == 1) != tc.want {
				t.Fatalf("calls=%d want run=%v", calls.Load(), tc.want)
			}
			if tc.want {
				status := s.Status()
				if status.Running || status.LastRun.Accounts[0].Status != Success || *status.LastRun.Accounts[0].Response != "synthetic response" {
					t.Fatalf("%+v", status)
				}
				c.Set(at.Add(-2 * time.Hour))
				s.Tick()
				s.wg.Wait()
				if calls.Load() != 1 {
					t.Fatal("repeated after clock moved backwards")
				}
				s.Close()
				restarted, err := New(s.store, s.opts)
				if err != nil {
					t.Fatal(err)
				}
				defer restarted.Close()
				c.Set(at)
				restarted.Tick()
				restarted.wg.Wait()
				if calls.Load() != 1 {
					t.Fatal("repeated after restart")
				}
			}
		})
	}
}
func TestPromptSelectionAvoidsRepeatsWithinEachPool(t *testing.T) {
	date := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	poolSize := len(promptPool("2026-09-09"))
	prompts := selectPrompts(poolSize*2, date, func(int) int { return 0 })
	for cycle := 0; cycle < 2; cycle++ {
		seen := map[string]bool{}
		for _, prompt := range prompts[cycle*poolSize : (cycle+1)*poolSize] {
			if seen[prompt] {
				t.Fatalf("duplicate prompt within cycle: %q", prompt)
			}
			seen[prompt] = true
		}
	}
	if !strings.Contains(strings.Join(prompts, "\n"), "2026-09-09") {
		t.Fatal("local batch date was not included in the daily-plan prompt")
	}
}

func TestBatchPersistsPromptAndInclusiveRandomDelayBeforeInvocation(t *testing.T) {
	var calls atomic.Int32
	s, c := newTestScheduler(t, runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) {
		calls.Add(1)
		return successOutput(), nil
	}), 2)
	delayCalls := 0
	s.opts.RandomIntn = func(n int) int {
		if n != int((MaxAccountDelay-MinAccountDelay)/time.Second)+1 {
			return 0
		}
		delayCalls++
		if delayCalls == 2 {
			return n - 1
		}
		return 0
	}
	waits := make(chan time.Duration, 2)
	s.opts.Wait = func(ctx context.Context, delay time.Duration) bool {
		waits <- delay
		<-ctx.Done()
		return false
	}
	// The poll happens 30 seconds late, but planned times still use the exact
	// configured time as their base.
	c.Set(time.Date(2026, 9, 9, 9, 0, 30, 0, time.UTC))
	s.Tick()
	got := []time.Duration{<-waits, <-waits}
	wantMin, wantMax := MinAccountDelay-30*time.Second, MaxAccountDelay-30*time.Second
	if !((got[0] == wantMin && got[1] == wantMax) || (got[1] == wantMin && got[0] == wantMax)) {
		t.Fatalf("waits=%v", got)
	}
	result := s.Status().LastRun
	if result == nil || len(result.Accounts) != 2 {
		t.Fatalf("missing persisted batch: %+v", result)
	}
	wantTimes := map[string]bool{"2026-09-09T09:01:00Z": true, "2026-09-09T09:05:00Z": true}
	seenPrompts := map[string]bool{}
	for _, account := range result.Accounts {
		if account.Prompt == nil || *account.Prompt == "" || seenPrompts[*account.Prompt] {
			t.Fatalf("prompts were not assigned uniquely before invocation: %+v", result.Accounts)
		}
		seenPrompts[*account.Prompt] = true
		if account.ScheduledAt == nil || !wantTimes[*account.ScheduledAt] {
			t.Fatalf("unexpected scheduled time: %+v", account)
		}
		delete(wantTimes, *account.ScheduledAt)
		if account.Status != Waiting {
			t.Fatalf("account started before its wait completed: %+v", account)
		}
	}
	s.Close()
	if calls.Load() != 0 {
		t.Fatal("CLI ran while randomized tasks were still waiting")
	}
	for _, account := range s.Status().LastRun.Accounts {
		if account.Status != Interrupted {
			t.Fatalf("waiting task was not interrupted on close: %+v", account)
		}
	}
}

func TestBatchUsesCredentialSnapshotAndRunsAccountsInParallel(t *testing.T) {
	entered, release := make(chan string, 2), make(chan struct{})
	var calls atomic.Int32
	var finished atomic.Int32
	var homesMu sync.Mutex
	var homes []string
	s, c := newTestScheduler(t, runFunc(func(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (ProcessOutput, error) {
		home := ""
		for _, v := range cmd.Env {
			if strings.HasPrefix(v, "CODEX_HOME=") {
				home = strings.TrimPrefix(v, "CODEX_HOME=")
			}
		}
		homesMu.Lock()
		homes = append(homes, home)
		homesMu.Unlock()
		raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
		if err != nil {
			return ProcessOutput{}, err
		}
		if timeout != 120*time.Second || !strings.HasSuffix(cmd.Dir, "work") {
			return ProcessOutput{}, errors.New("bad invocation")
		}
		calls.Add(1)
		if strings.Contains(string(raw), "synthetic-one") {
			entered <- "one"
			select {
			case <-release:
			case <-ctx.Done():
				return ProcessOutput{}, ctx.Err()
			}
			return ProcessOutput{}, errors.New("synthetic first failure")
		}
		if !strings.Contains(string(raw), "synthetic-two") {
			return ProcessOutput{}, errors.New("lost snapshot")
		}
		entered <- "two"
		return successOutput(), nil
	}), 2)
	s.opts.BatchFinished = func(context.Context) { finished.Add(1) }
	c.Set(c.Now().Add(time.Hour))
	s.Tick()
	first, second := <-entered, <-entered
	if first == second {
		t.Fatalf("accounts did not enter independently: %q, %q", first, second)
	}
	if batch := s.Status().LastRun; batch.FinishedAt != nil || finished.Load() != 0 {
		t.Fatal("batch finished while one parallel worker was blocked")
	}
	accounts, _ := s.store.ListAccounts()
	if _, err := s.store.RemoveAccount(accounts.Accounts[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), Settings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	c.Set(c.Now().Add(time.Hour))
	close(release)
	s.wg.Wait()
	r := s.Status().LastRun
	if calls.Load() != 2 || r.Accounts[0].Status != Failed || r.Accounts[1].Status != Success || finished.Load() != 1 || r.FinishedAt == nil {
		t.Fatalf("calls=%d result=%+v", calls.Load(), r.Accounts)
	}
	homesMu.Lock()
	defer homesMu.Unlock()
	if homes[0] == homes[1] {
		t.Fatal("shared credentials")
	}
	for _, h := range homes {
		if _, err := os.Stat(h); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("temporary credentials retained")
		}
	}
}
func TestExitInterruptsAndReleasesLock(t *testing.T) {
	entered := make(chan struct{})
	s, c := newTestScheduler(t, runFunc(func(ctx context.Context, _ *exec.Cmd, _ time.Duration) (ProcessOutput, error) {
		close(entered)
		<-ctx.Done()
		return ProcessOutput{}, ctx.Err()
	}), 2)
	if other, err := New(s.store, s.opts); err == nil {
		other.Close()
		t.Fatal("second scheduler acquired lock")
	}
	c.Set(c.Now().Add(time.Hour))
	s.Tick()
	<-entered
	s.Close()
	for _, a := range s.Status().LastRun.Accounts {
		if a.Status != Interrupted {
			t.Fatalf("%+v", a)
		}
	}
	restarted, err := New(s.store, s.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.Status().Running || restarted.Status().LastRun.FinishedAt == nil {
		t.Fatal("lost interruption")
	}
}
func TestFailedDatePersistencePreventsInvocation(t *testing.T) {
	var calls atomic.Int32
	s, c := newTestScheduler(t, runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) {
		calls.Add(1)
		return successOutput(), nil
	}), 1)
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	c.Set(c.Now().Add(time.Hour))
	s.Tick()
	s.wg.Wait()
	if calls.Load() != 0 || s.Status().Error == nil || s.Status().Running {
		t.Fatal("ran without durable claim")
	}
}
func TestCorruptSettingsArePreservedAndDisabled(t *testing.T) {
	s, _ := newTestScheduler(t, runFunc(func(context.Context, *exec.Cmd, time.Duration) (ProcessOutput, error) { return successOutput(), nil }), 0)
	s.Close()
	bad := []byte(`{"settings":{"enabled":true,"time":"99:99"}}`)
	if err := os.WriteFile(s.path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.store, s.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got, _ := os.ReadFile(s.path)
	if string(got) != string(bad) || restarted.Settings().Enabled || restarted.Status().Error == nil {
		t.Fatal("corrupt data replaced or enabled")
	}
}

func TestLegacyRecordWithoutAccountDetailsUsesEmptyArray(t *testing.T) {
	root := t.TempDir()
	st := store.New(filepath.Join(root, "data"), filepath.Join(root, "auth.json"))
	if err := st.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"settings":{"enabled":false},"lastRun":{"startedAt":"2026-09-08T08:00:00Z","finishedAt":"2026-09-08T08:01:00Z"}}`)
	if err := os.WriteFile(filepath.Join(st.DataDir, "settings.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(st, Options{Now: func() time.Time { return time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	encoded, err := json.Marshal(s.Status())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"accounts":[]`) {
		t.Fatalf("array contract changed: %s", encoded)
	}
}
