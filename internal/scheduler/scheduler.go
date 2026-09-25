package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"switch-codex/internal/platform"
	"switch-codex/internal/store"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Options struct {
	Now                          func() time.Time
	RandomIntn                   func(int) int
	Wait                         func(context.Context, time.Duration) bool
	Runner                       Runner
	ResolveCLI                   func(*string) (string, error)
	ValidateCLI                  func(context.Context, string) error
	Emit                         func(string, any)
	BatchFinished                func(context.Context)
	PollInterval, AccountTimeout time.Duration
}
type Scheduler struct {
	mu                        sync.Mutex
	saved                     savedState
	next                      map[string]*time.Time
	running                   int
	stopping, asleep, started bool
	offset                    int
	error                     *string
	store                     *store.Store
	path, runtime             string
	lock                      *os.File
	opts                      Options
	ctx                       context.Context
	cancel                    context.CancelFunc
	wake                      chan struct{}
	wg                        sync.WaitGroup
	closeOnce                 sync.Once
}

func New(st *store.Store, opts Options) (s *Scheduler, err error) {
	lock, err := platform.Lock(filepath.Join(st.DataDir, "scheduler.lock"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = lock.Close()
		}
	}()
	if err = st.EnsureReady(); err != nil {
		return nil, err
	}
	opts = defaultOptions(opts)
	ctx, cancel := context.WithCancel(context.Background())
	s = &Scheduler{store: st, path: filepath.Join(st.DataDir, "settings.json"), runtime: filepath.Join(st.DataDir, "scheduled-runtime"), lock: lock, opts: opts, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	raw, readErr := os.ReadFile(s.path)
	if readErr == nil {
		if json.Unmarshal(raw, &s.saved) != nil || !validSaved(raw, s.saved) || s.saved.Settings.Validate() != nil {
			s.saved = savedState{Settings: Settings{Schedules: []Schedule{{ID: "default"}}, AutoSyncAuth: true}}
			s.error = ptr("设置文件损坏，定时调用已停用；请重新保存设置")
		} else if s.saved.LastRun != nil && s.saved.LastRun.Accounts == nil {
			// Old records may omit task details. Preserve the record while keeping
			// the renderer contract that array fields are never null.
			s.saved.LastRun.Accounts = []AccountResult{}
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		s.saved.Settings = Settings{Schedules: []Schedule{{ID: "default"}}, AutoSyncAuth: true}
	} else {
		cancel()
		return nil, errors.New("无法读取设置文件")
	}
	interrupt(&s.saved, opts.Now())
	migrate(&s.saved)
	if err = os.RemoveAll(s.runtime); err != nil {
		cancel()
		return nil, errors.New("无法清理上次任务的临时认证目录")
	}
	now := opts.Now()
	_, s.offset = now.Zone()
	s.resetNext(now)
	if s.error == nil {
		if err = s.persist(s.saved); err != nil {
			cancel()
			return nil, err
		}
	}
	return s, nil
}

func defaultOptions(opts Options) Options {
	if opts.Now == nil {
		opts.Now = platform.LocalNow
	}
	if opts.Runner == nil {
		opts.Runner = ProcessRunner{}
	}
	if opts.RandomIntn == nil {
		opts.RandomIntn = rand.IntN
	}
	if opts.Wait == nil {
		opts.Wait = wait
	}
	if opts.ResolveCLI == nil {
		opts.ResolveCLI = ResolveCLI
	}
	if opts.ValidateCLI == nil {
		opts.ValidateCLI = func(ctx context.Context, path string) error { return ValidateCLI(ctx, path, ProcessRunner{}) }
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = PollInterval
	}
	if opts.AccountTimeout == 0 {
		opts.AccountTimeout = AccountTimeout
	}
	return opts
}
func validSaved(raw []byte, s savedState) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return false
	}
	if settings, ok := obj["settings"]; ok && bytes.Equal(bytes.TrimSpace(settings), []byte("null")) {
		return false
	}
	if s.LastRun != nil {
		for _, a := range s.LastRun.Accounts {
			switch a.Status {
			case Waiting, Running, Success, Failed, Interrupted:
			default:
				return false
			}
		}
	}
	return true
}
func clone[T any](v T) T {
	b, _ := json.Marshal(v)
	var result T
	_ = json.Unmarshal(b, &result)
	return result
}
func (s *Scheduler) persist(saved savedState) error {
	b, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return errors.New("无法序列化设置")
	}
	if platform.WriteAtomic(s.path, b, 0600) != nil {
		return errors.New("无法保存设置或任务状态")
	}
	return nil
}
func (s *Scheduler) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.saved.Settings)
}

// SetAutoSyncAuth persists only the authentication-sync preference. It avoids
// revalidating machine-specific CLI configuration when account import needs to
// disable sync without changing the scheduled task.
func (s *Scheduler) SetAutoSyncAuth(enabled bool) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return Settings{}, errors.New("应用正在退出")
	}
	saved := clone(s.saved)
	saved.Settings.AutoSyncAuth = enabled
	if err := s.persist(saved); err != nil {
		return Settings{}, err
	}
	s.saved = saved
	return clone(saved.Settings), nil
}
func (s *Scheduler) Status() RunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	r := RunStatus{Timezone: now.Format("MST (UTC-07:00)"), Running: s.running > 0, Error: s.error, LastRun: s.saved.LastRun}
	for _, next := range s.next {
		if next != nil && (r.NextRunAt == nil || next.Before(parseStatusTime(*r.NextRunAt))) {
			r.NextRunAt = ptr(next.Format(time.RFC3339Nano))
		}
	}
	return clone(r)
}
func (s *Scheduler) emit() {
	if s.opts.Emit != nil {
		s.opts.Emit("scheduled-run-changed", s.Status())
	}
}
func (s *Scheduler) Save(ctx context.Context, settings Settings) (Settings, error) {
	settings = clone(settings)
	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}
	if settings.Enabled {
		path, err := s.opts.ResolveCLI(settings.CLIPath)
		if err != nil {
			return Settings{}, err
		}
		if err = s.opts.ValidateCLI(ctx, path); err != nil {
			return Settings{}, err
		}
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return Settings{}, errors.New("应用正在退出")
	}
	saved := clone(s.saved)
	saved.Settings = settings
	for id := range saved.LastRunDates {
		keep := false
		for _, item := range settings.Schedules {
			if item.ID == id {
				keep = true
				break
			}
		}
		if !keep {
			delete(saved.LastRunDates, id)
		}
	}
	if err := s.persist(saved); err != nil {
		s.mu.Unlock()
		return Settings{}, err
	}
	s.saved = saved
	s.resetNext(s.opts.Now())
	s.error = nil
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	s.emit()
	return clone(settings), nil
}
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.started || s.stopping {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		timer := time.NewTicker(s.opts.PollInterval)
		defer timer.Stop()
		for {
			s.Tick()
			select {
			case <-s.ctx.Done():
				return
			case <-timer.C:
			case <-s.wake:
			}
		}
	}()
}
func (s *Scheduler) Sleep() { s.mu.Lock(); s.asleep = true; s.mu.Unlock() }
func (s *Scheduler) Wake() {
	s.mu.Lock()
	s.asleep = false
	now := s.opts.Now()
	_, s.offset = now.Zone()
	s.resetNext(now)
	s.mu.Unlock()
	s.emit()
}
func parseStatusTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
func (s *Scheduler) resetNext(now time.Time) {
	s.next = make(map[string]*time.Time, len(s.saved.Settings.Schedules))
	if !s.saved.Settings.Enabled {
		return
	}
	for _, item := range s.saved.Settings.Schedules {
		s.next[item.ID] = nextScheduleRun(item, now, s.saved.LastRunDates[item.ID])
	}
}

type batchLaunch struct {
	key, runtime string
	tasks        []scheduledAccount
	path         *string
}

func (s *Scheduler) Tick() {
	s.mu.Lock()
	now := s.opts.Now()
	if s.asleep || s.stopping {
		s.mu.Unlock()
		return
	}
	_, offset := now.Zone()
	if offset != s.offset {
		s.offset = offset
		s.resetNext(now)
	}
	launches := []batchLaunch{}
	changed := false
	for _, item := range s.saved.Settings.Schedules {
		next := s.next[item.ID]
		if next == nil {
			continue
		}
		delay := now.Sub(*next)
		if delay < 0 {
			continue
		}
		if delay > Grace {
			s.next[item.ID] = nextScheduleRun(item, now, s.saved.LastRunDates[item.ID])
			changed = true
			continue
		}
		scheduledFor := *next
		snapshots, err := s.store.Snapshots()
		if err != nil {
			s.error = ptr("账号存储不可用")
			s.next[item.ID] = nextScheduleRun(item, now, scheduledFor.Format("2006-01-02"))
			changed = true
			continue
		}
		saved := clone(s.saved)
		if saved.LastRunDates == nil {
			saved.LastRunDates = map[string]string{}
		}
		saved.LastRunDates[item.ID] = scheduledFor.Format("2006-01-02")
		prompts := selectPrompts(len(snapshots), scheduledFor, s.opts.RandomIntn)
		tasks := make([]scheduledAccount, 0, len(snapshots))
		batch := &BatchResult{ScheduleID: item.ID, StartedAt: now.Format(time.RFC3339Nano), Accounts: make([]AccountResult, 0, len(snapshots))}
		for i, account := range snapshots {
			scheduledAt := scheduledFor.Add(randomAccountDelay(s.opts.RandomIntn))
			prompt := prompts[i]
			batch.Accounts = append(batch.Accounts, AccountResult{AccountID: account.ID, AccountName: account.Name, Status: Waiting, ScheduledAt: ptr(scheduledAt.UTC().Format(time.RFC3339Nano)), Prompt: ptr(prompt), Response: ptr("")})
			tasks = append(tasks, scheduledAccount{snapshot: account, index: i, prompt: prompt, scheduledAt: scheduledAt})
		}
		saved.LastRun = batch
		// Claim the planned local date durably before starting any CLI task.
		if err := s.persist(saved); err != nil {
			s.error = ptr(err.Error())
			s.next[item.ID] = nil
			changed = true
			continue
		}
		s.saved = saved
		s.running++
		s.error = nil
		s.next[item.ID] = nextScheduleRun(item, now, saved.LastRunDates[item.ID])
		s.wg.Add(1)
		launches = append(launches, batchLaunch{key: batchKey(batch), runtime: filepath.Join(s.runtime, uuid.NewString()), tasks: tasks, path: clone(saved.Settings.CLIPath)})
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.emit()
	}
	for _, launch := range launches {
		go func(launch batchLaunch) { defer s.wg.Done(); s.runBatch(launch) }(launch)
	}
}
func batchKey(batch *BatchResult) string { return batch.ScheduleID + "|" + batch.StartedAt }
func (s *Scheduler) update(key string, i int, status AccountStatus, message, response *string) bool {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return false
	}
	if s.saved.LastRun == nil || batchKey(s.saved.LastRun) != key {
		s.mu.Unlock()
		return true
	}
	a := &s.saved.LastRun.Accounts[i]
	if status == Success && message == nil {
		message = a.Message
	}
	a.Status, a.Message = status, message
	stamp := ptr(s.opts.Now().UTC().Format(time.RFC3339Nano))
	if status == Running {
		a.StartedAt = stamp
	} else {
		a.FinishedAt = stamp
	}
	if response != nil {
		a.Response = response
	}
	if err := s.persist(s.saved); err != nil {
		s.error = ptr(err.Error())
	}
	s.mu.Unlock()
	s.emit()
	return true
}

type scheduledAccount struct {
	snapshot    store.Snapshot
	index       int
	prompt      string
	scheduledAt time.Time
}

func selectPrompts(count int, date time.Time, randomIntn func(int) int) []string {
	result := make([]string, 0, count)
	for len(result) < count {
		cycle := promptPool(date.Format("2006-01-02"))
		for i := len(cycle) - 1; i > 0; i-- {
			j := randomIntn(i + 1)
			cycle[i], cycle[j] = cycle[j], cycle[i]
		}
		if len(result) > 0 && len(cycle) > 1 && result[len(result)-1] == cycle[0] {
			cycle[0], cycle[1] = cycle[1], cycle[0]
		}
		remaining := count - len(result)
		result = append(result, cycle[:min(remaining, len(cycle))]...)
	}
	return result
}

func wait(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func randomAccountDelay(randomIntn func(int) int) time.Duration {
	return MinAccountDelay + time.Duration(randomIntn(int((MaxAccountDelay-MinAccountDelay)/time.Second)+1))*time.Second
}

func (s *Scheduler) runBatch(batch batchLaunch) {
	cli, cliErr := s.opts.ResolveCLI(batch.path)
	if cliErr == nil {
		cliErr = s.opts.ValidateCLI(s.ctx, cli)
	}
	if cliErr != nil {
		for _, task := range batch.tasks {
			s.update(batch.key, task.index, Failed, ptr(cliErr.Error()), nil)
		}
	} else {
		var workers sync.WaitGroup
		workers.Add(len(batch.tasks))
		for _, task := range batch.tasks {
			go func(task scheduledAccount) {
				defer workers.Done()
				if !s.opts.Wait(s.ctx, task.scheduledAt.Sub(s.opts.Now())) {
					return
				}
				if !s.update(batch.key, task.index, Running, nil, nil) {
					return
				}
				result := executeAccount(s.ctx, s.store, batch.runtime, cli, s.opts.Runner, s.opts.AccountTimeout, task.snapshot, task.prompt)
				status := Success
				var message *string
				if result.err != nil {
					status = Failed
					message = ptr(result.err.Error())
				} else if result.warning != nil {
					message = ptr(result.warning.Error())
				}
				s.update(batch.key, task.index, status, message, &result.response)
			}(task)
		}
		workers.Wait()
	}
	_ = os.RemoveAll(batch.runtime)
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.running--
	if s.saved.LastRun != nil && batchKey(s.saved.LastRun) == batch.key {
		s.saved.LastRun.FinishedAt = ptr(s.opts.Now().UTC().Format(time.RFC3339Nano))
		if err := s.persist(s.saved); err != nil {
			s.error = ptr(err.Error())
		}
	}
	s.mu.Unlock()
	s.emit()
	if s.opts.BatchFinished != nil {
		s.opts.BatchFinished(s.ctx)
	}
}
func (s *Scheduler) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait() // Includes credential write-back and child process cleanup.
		s.mu.Lock()
		wasRunning := s.running > 0
		interrupt(&s.saved, s.opts.Now())
		s.running = 0
		if wasRunning {
			if err := s.persist(s.saved); err != nil {
				s.error = ptr(err.Error())
			}
		}
		s.mu.Unlock()
		_ = os.RemoveAll(s.runtime)
		_ = s.lock.Close()
	})
}
