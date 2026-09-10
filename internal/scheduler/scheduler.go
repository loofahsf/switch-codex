package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"math/rand/v2"
	"os"
	"path/filepath"
	"switch-codex/internal/platform"
	"switch-codex/internal/store"
	"sync"
	"time"
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
	mu                                 sync.Mutex
	saved                              savedState
	next                               *time.Time
	running, stopping, asleep, started bool
	offset                             int
	error                              *string
	store                              *store.Store
	path, runtime                      string
	lock                               *os.File
	opts                               Options
	ctx                                context.Context
	cancel                             context.CancelFunc
	wake                               chan struct{}
	wg                                 sync.WaitGroup
	closeOnce                          sync.Once
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
	ctx, cancel := context.WithCancel(context.Background())
	s = &Scheduler{store: st, path: filepath.Join(st.DataDir, "settings.json"), runtime: filepath.Join(st.DataDir, "scheduled-runtime"), lock: lock, opts: opts, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	raw, readErr := os.ReadFile(s.path)
	if readErr == nil {
		if json.Unmarshal(raw, &s.saved) != nil || !validSaved(raw, s.saved) || s.saved.Settings.Validate() != nil {
			s.saved = savedState{Settings: Settings{AutoSyncAuth: true}}
			s.error = ptr("设置文件损坏，定时调用已停用；请重新保存设置")
		} else if s.saved.LastRun != nil && s.saved.LastRun.Accounts == nil {
			// Old records may omit task details. Preserve the record while keeping
			// the renderer contract that array fields are never null.
			s.saved.LastRun.Accounts = []AccountResult{}
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		s.saved.Settings.AutoSyncAuth = true
	} else {
		cancel()
		return nil, errors.New("无法读取设置文件")
	}
	interrupt(&s.saved, opts.Now())
	if err = os.RemoveAll(s.runtime); err != nil {
		cancel()
		return nil, errors.New("无法清理上次任务的临时认证目录")
	}
	now := opts.Now()
	_, s.offset = now.Zone()
	s.next = nextRun(s.saved.Settings, now, s.saved.LastRunDate)
	if s.error == nil {
		if err = s.persist(s.saved); err != nil {
			cancel()
			return nil, err
		}
	}
	return s, nil
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
func (s *Scheduler) Status() RunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	r := RunStatus{Timezone: now.Format("MST (UTC-07:00)"), Running: s.running, Error: s.error, LastRun: s.saved.LastRun}
	if s.next != nil {
		r.NextRunAt = ptr(s.next.Format(time.RFC3339Nano))
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
	if err := s.persist(saved); err != nil {
		s.mu.Unlock()
		return Settings{}, err
	}
	s.saved = saved
	s.next = nextRun(settings, s.opts.Now(), s.saved.LastRunDate)
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
	s.next = nextRun(s.saved.Settings, now, s.saved.LastRunDate)
	s.mu.Unlock()
	s.emit()
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
		s.next = nextRun(s.saved.Settings, now, s.saved.LastRunDate)
	}
	if s.next == nil || s.running {
		s.mu.Unlock()
		return
	}
	delay := now.Sub(*s.next)
	if delay < 0 {
		s.mu.Unlock()
		s.emit()
		return
	}
	if delay > Grace {
		s.next = nextRun(s.saved.Settings, now, s.saved.LastRunDate)
		s.mu.Unlock()
		s.emit()
		return
	}
	scheduledFor := *s.next
	// Snapshot all credentials under the Store mutex before claiming the date.
	snapshots, err := s.store.Snapshots()
	if err != nil {
		s.error = ptr("账号存储不可用")
		s.next = nextRun(s.saved.Settings, now, ptr(now.Format("2006-01-02")))
		s.mu.Unlock()
		s.emit()
		return
	}
	saved := clone(s.saved)
	saved.LastRunDate = ptr(now.Format("2006-01-02"))
	prompts := selectPrompts(len(snapshots), now, s.opts.RandomIntn)
	tasks := make([]scheduledAccount, 0, len(snapshots))
	saved.LastRun = &BatchResult{StartedAt: now.Format(time.RFC3339Nano), Accounts: make([]AccountResult, 0, len(snapshots))}
	for i, a := range snapshots {
		delay := MinAccountDelay + time.Duration(s.opts.RandomIntn(int((MaxAccountDelay-MinAccountDelay)/time.Second)+1))*time.Second
		scheduledAt := scheduledFor.Add(delay)
		prompt, scheduledStamp := prompts[i], scheduledAt.UTC().Format(time.RFC3339Nano)
		saved.LastRun.Accounts = append(saved.LastRun.Accounts, AccountResult{AccountID: a.ID, AccountName: a.Name, Status: Waiting, ScheduledAt: ptr(scheduledStamp), Prompt: ptr(prompt), Response: ptr("")})
		tasks = append(tasks, scheduledAccount{snapshot: a, index: i, prompt: prompt, scheduledAt: scheduledAt})
	}
	// A durable date claim must precede every external CLI invocation.
	if err = s.persist(saved); err != nil {
		s.error = ptr(err.Error())
		s.next = nil
		s.mu.Unlock()
		s.emit()
		return
	}
	s.saved = saved
	s.running = true
	s.error = nil
	s.next = nextRun(saved.Settings, now, saved.LastRunDate)
	path := clone(saved.Settings.CLIPath)
	s.wg.Add(1)
	s.mu.Unlock()
	s.emit()
	go func() { defer s.wg.Done(); s.runBatch(tasks, path) }()
}
func (s *Scheduler) update(i int, status AccountStatus, message, response *string) bool {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return false
	}
	a := &s.saved.LastRun.Accounts[i]
	if status == Success && message == nil {
		message = a.Message
	}
	a.Status = status
	a.Message = message
	now := ptr(s.opts.Now().UTC().Format(time.RFC3339Nano))
	if status == Running {
		a.StartedAt = now
	} else {
		a.FinishedAt = now
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

func (s *Scheduler) runBatch(accounts []scheduledAccount, path *string) {
	cli, cliErr := s.opts.ResolveCLI(path)
	if cliErr == nil {
		cliErr = s.opts.ValidateCLI(s.ctx, cli)
	}
	if cliErr != nil {
		for _, task := range accounts {
			s.update(task.index, Failed, ptr(cliErr.Error()), nil)
		}
	} else {
		var workers sync.WaitGroup
		workers.Add(len(accounts))
		for _, task := range accounts {
			go func() {
				defer workers.Done()
				if !s.opts.Wait(s.ctx, task.scheduledAt.Sub(s.opts.Now())) {
					return
				}
				if !s.update(task.index, Running, nil, nil) {
					return
				}
				text, err := s.runAccount(cli, task.snapshot, task.index, task.prompt)
				status := Success
				var message *string
				if err != nil {
					status = Failed
					message = ptr(err.Error())
				}
				s.update(task.index, status, message, &text)
			}()
		}
		workers.Wait()
	}
	_ = os.RemoveAll(s.runtime)
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.saved.LastRun.FinishedAt = ptr(s.opts.Now().UTC().Format(time.RFC3339Nano))
	if err := s.persist(s.saved); err != nil {
		s.error = ptr(err.Error())
	}
	s.mu.Unlock()
	s.emit()
	if s.opts.BatchFinished != nil {
		s.opts.BatchFinished(s.ctx)
	}
}
func (s *Scheduler) runAccount(cli string, a store.Snapshot, i int, prompt string) (string, error) {
	original, err := a.Credentials()
	if err != nil {
		return "", err
	}
	normalized, err := store.NormalizeAuth(original)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.runtime, uuid.NewString())
	defer os.RemoveAll(dir)
	home, work := filepath.Join(dir, "home"), filepath.Join(dir, "work")
	for _, d := range []string{dir, home, work} {
		if err = os.MkdirAll(d, 0700); err != nil {
			return "", errors.New("无法创建临时认证目录")
		}
		if err = os.Chmod(d, 0700); err != nil {
			return "", errors.New("无法设置认证目录权限")
		}
	}
	if err = platform.WriteAtomic(filepath.Join(home, "auth.json"), normalized, 0600); err != nil {
		return "", errors.New("无法准备临时认证文件")
	}
	if s.ctx.Err() != nil {
		return "", errors.New("任务已中断")
	}
	out, runErr := s.opts.Runner.Run(s.ctx, invocation(cli, home, work, prompt), s.opts.AccountTimeout)
	// A refresh may succeed even when the model request fails or times out.
	var warning error
	refreshed, e := os.ReadFile(filepath.Join(home, "auth.json"))
	if e != nil {
		warning = errors.New("无法读取 CLI 刷新后的凭证")
	} else if !bytes.Equal(refreshed, normalized) {
		warning = s.store.PersistRefreshedAuth(a.ID, original, refreshed)
	}
	if runErr == nil {
		runErr = completion(out)
	}
	if runErr != nil && warning != nil {
		return response(out), fmt.Errorf("%w；%v", runErr, warning)
	}
	if runErr != nil {
		return response(out), runErr
	}
	// A successful call with failed refresh is still successful in the UI.
	if warning != nil {
		s.mu.Lock()
		if !s.stopping {
			s.saved.LastRun.Accounts[i].Message = ptr(warning.Error())
		}
		s.mu.Unlock()
	}
	return response(out), nil
}
func (s *Scheduler) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait() // Includes credential write-back and child process cleanup.
		s.mu.Lock()
		wasRunning := s.running
		interrupt(&s.saved, s.opts.Now())
		s.running = false
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
