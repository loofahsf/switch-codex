package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"switch-codex/internal/store"
	"sync"
)

// ManualWarmup runs user-triggered calls without reading or updating the
// Scheduler's schedule, persisted batch record, or running state. It has a
// separate runtime directory so manual and scheduled calls can overlap safely.
type ManualWarmup struct {
	mu        sync.Mutex
	store     *store.Store
	runtime   string
	opts      Options
	ctx       context.Context
	cancel    context.CancelFunc
	stopping  bool
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func NewManualWarmup(st *store.Store, opts Options) (*ManualWarmup, error) {
	if st == nil {
		return nil, errors.New("账号存储尚未就绪")
	}
	if err := st.EnsureReady(); err != nil {
		return nil, err
	}
	runtime := filepath.Join(st.DataDir, "manual-runtime")
	if err := os.RemoveAll(runtime); err != nil {
		return nil, errors.New("无法清理上次手动预热的临时认证目录")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ManualWarmup{
		store:   st,
		runtime: runtime,
		opts:    defaultOptions(opts),
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

func (m *ManualWarmup) begin() (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return nil, nil, errors.New("应用正在退出")
	}
	m.wg.Add(1)
	return m.ctx, m.wg.Done, nil
}

func (m *ManualWarmup) resolveCLI(ctx context.Context, configured *string) (string, error) {
	cli, err := m.opts.ResolveCLI(configured)
	if err != nil {
		return "", err
	}
	if err = m.opts.ValidateCLI(ctx, cli); err != nil {
		return "", err
	}
	return cli, nil
}

// WarmupAccount starts the selected account immediately with one random prompt.
// It deliberately does not wait for, cancel, or inspect a scheduled batch.
func (m *ManualWarmup) WarmupAccount(accountID string, configuredCLI *string) error {
	ctx, done, err := m.begin()
	if err != nil {
		return err
	}
	defer done()

	snapshots, err := m.store.Snapshots()
	if err != nil {
		return errors.New("账号存储不可用")
	}
	var account store.Snapshot
	found := false
	for _, snapshot := range snapshots {
		if snapshot.ID == accountID {
			account, found = snapshot, true
			break
		}
	}
	if !found {
		return errors.New("账号不存在")
	}
	cli, err := m.resolveCLI(ctx, configuredCLI)
	if err != nil {
		return err
	}
	prompt := selectPrompts(1, m.opts.Now(), m.opts.RandomIntn)[0]
	result := executeAccount(ctx, m.store, m.runtime, cli, m.opts.Runner, m.opts.AccountTimeout, account, prompt)
	return result.err
}

// WarmupAll starts an independent delayed batch. Each account keeps the same
// 60–300 second randomized delay used by the daily scheduler, while the batch
// itself neither reads nor mutates the scheduler's task record.
func (m *ManualWarmup) WarmupAll(configuredCLI *string) error {
	ctx, done, err := m.begin()
	if err != nil {
		return err
	}
	defer done()

	accounts, err := m.store.Snapshots()
	if err != nil {
		return errors.New("账号存储不可用")
	}
	if len(accounts) == 0 {
		return nil
	}
	cli, err := m.resolveCLI(ctx, configuredCLI)
	if err != nil {
		return err
	}
	prompts := selectPrompts(len(accounts), m.opts.Now(), m.opts.RandomIntn)
	var workers sync.WaitGroup
	var failuresMu sync.Mutex
	failures := make([]string, 0)
	addFailure := func(name string, err error) {
		failuresMu.Lock()
		failures = append(failures, fmt.Sprintf("「%s」：%v", name, err))
		failuresMu.Unlock()
	}
	workers.Add(len(accounts))
	for i, account := range accounts {
		account, prompt := account, prompts[i]
		delay := randomAccountDelay(m.opts.RandomIntn)
		go func() {
			defer workers.Done()
			if !m.opts.Wait(ctx, delay) {
				if ctx.Err() != nil {
					addFailure(account.Name, errors.New("任务已中断"))
				}
				return
			}
			result := executeAccount(ctx, m.store, m.runtime, cli, m.opts.Runner, m.opts.AccountTimeout, account, prompt)
			if result.err != nil {
				addFailure(account.Name, result.err)
			}
		}()
	}
	workers.Wait()
	if len(failures) > 0 {
		return fmt.Errorf("%d 个账号预热失败：%s", len(failures), strings.Join(failures, "；"))
	}
	return nil
}

func (m *ManualWarmup) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.stopping = true
		m.cancel()
		m.mu.Unlock()
		m.wg.Wait()
		_ = os.RemoveAll(m.runtime)
	})
}
