// Package authsync keeps the file-backed Codex login associated with the
// matching saved account without observing any other files under CODEX_HOME.
package authsync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"os"
	"switch-codex/internal/store"
	"sync"
	"sync/atomic"
	"time"
)

const (
	PollInterval = 30 * time.Second
	MaxAuthBytes = 1024 * 1024
)

type State string

const (
	Disabled  State = "disabled"
	Checking  State = "checking"
	UpToDate  State = "up_to_date"
	Synced    State = "synced"
	Followed  State = "followed"
	Unknown   State = "unknown"
	Ambiguous State = "ambiguous"
	Invalid   State = "invalid"
	Missing   State = "missing"
	Failed    State = "error"
)

type Status struct {
	Enabled     bool    `json:"enabled"`
	State       State   `json:"state"`
	AccountID   *string `json:"accountId"`
	AccountName *string `json:"accountName"`
	CheckedAt   *string `json:"checkedAt"`
	SyncedAt    *string `json:"syncedAt"`
	PendingID   *string `json:"pendingId"`
	Message     *string `json:"message"`
}

type Options struct {
	Enabled         bool
	Now             func() time.Time
	PollInterval    time.Duration
	Ticks           <-chan time.Time
	ReadFile        func(string, int64) ([]byte, error)
	Wait            func(context.Context, time.Duration) bool
	Emit            func(Status)
	AccountsChanged func(store.AccountsState)
}

type pendingAccount struct {
	id       string
	digest   [sha256.Size]byte
	identity store.CredentialIdentity
}

type Service struct {
	store *store.Store
	opts  Options

	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	force     atomic.Bool
	checkMu   sync.Mutex
	mu        sync.Mutex
	status    Status
	pending   *pendingAccount
	digest    [sha256.Size]byte
	hasDigest bool
	started   bool
	wg        sync.WaitGroup
	closeOnce sync.Once
}

var (
	errTooLarge   = errors.New("auth.json 超过 1 MiB 安全限制")
	errNotRegular = errors.New("auth.json 不是普通文件")
)

type invalidAuthError struct{ err error }

func (e invalidAuthError) Error() string { return e.err.Error() }

func (e invalidAuthError) Unwrap() error { return e.err }

func New(st *store.Store, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = PollInterval
	}
	if opts.ReadFile == nil {
		opts.ReadFile = readBoundedFile
	}
	if opts.Wait == nil {
		opts.Wait = wait
	}
	ctx, cancel := context.WithCancel(context.Background())
	state := Checking
	if !opts.Enabled {
		state = Disabled
	}
	return &Service{
		store:  st,
		opts:   opts,
		ctx:    ctx,
		cancel: cancel,
		wake:   make(chan struct{}, 1),
		status: Status{Enabled: opts.Enabled, State: state},
	}
}

func (s *Service) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.loop()
	s.Trigger(true)
}

func (s *Service) loop() {
	defer s.wg.Done()
	ticks := s.opts.Ticks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(s.opts.PollInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticks:
			s.check(false, false)
		case <-s.wake:
			s.check(s.force.Swap(false), false)
		}
	}
}

func (s *Service) Trigger(force bool) {
	if force {
		s.force.Store(true)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) SetEnabled(enabled bool) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	s.mu.Lock()
	if s.status.Enabled == enabled {
		s.mu.Unlock()
		return
	}
	current := s.status
	current.Enabled = enabled
	if !enabled {
		current.State = Disabled
		current.PendingID = nil
		current.Message = ptr("自动同步已关闭")
		s.pending = nil
	}
	s.mu.Unlock()
	if !enabled {
		s.publish(current)
		return
	}
	s.publish(Status{Enabled: true, State: Checking, CheckedAt: current.CheckedAt, SyncedAt: current.SyncedAt, Message: ptr("正在检查当前认证文件")})
	s.Trigger(true)
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStatus(s.status)
}

func (s *Service) CheckNow() Status {
	s.check(true, true)
	return s.Status()
}

func (s *Service) check(force, announce bool) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if !s.enabled() {
		return
	}
	if announce {
		current := s.Status()
		current.State = Checking
		current.Message = ptr("正在检查当前认证文件")
		s.publish(current)
	}
	now := s.opts.Now().UTC().Format(time.RFC3339Nano)
	auth, err := s.readNormalized()
	if err != nil {
		s.publish(s.failureStatus(err, now))
		return
	}
	digest := sha256.Sum256(auth)
	s.mu.Lock()
	unchanged := !force && s.hasDigest && s.digest == digest
	if unchanged {
		s.status.CheckedAt = &now
	}
	s.mu.Unlock()
	if unchanged {
		return
	}
	result, err := s.store.ReconcileTargetAuth(auth)
	if err != nil {
		s.mu.Lock()
		syncedAt := copyString(s.status.SyncedAt)
		s.pending = nil
		s.hasDigest = false
		s.mu.Unlock()
		s.publish(Status{Enabled: true, State: Failed, CheckedAt: &now, SyncedAt: syncedAt, Message: ptr("自动同步失败，已保留原凭证")})
		return
	}
	next := Status{Enabled: true, CheckedAt: &now, AccountID: result.AccountID, AccountName: result.AccountName}
	s.mu.Lock()
	next.SyncedAt = s.status.SyncedAt
	previousPending := s.pending
	s.digest, s.hasDigest = digest, true
	s.pending = nil
	s.mu.Unlock()
	switch result.Outcome {
	case store.ReconcileUpToDate:
		next.State = UpToDate
		next.Message = ptr("当前认证文件与已保存账号一致")
	case store.ReconcileSynced:
		next.State = Synced
		next.SyncedAt = &now
		next.Message = ptr("已自动同步当前账号的认证更新")
	case store.ReconcileFollowed:
		next.State = Followed
		next.SyncedAt = &now
		next.Message = ptr("已跟随当前认证文件切换账号")
	case store.ReconcileUnknown:
		identity, identityErr := store.IdentifyAuth(auth)
		if identityErr != nil {
			next.State = Ambiguous
			next.Message = ptr("当前认证缺少可安全识别的用户或工作空间信息")
			break
		}
		pending := &pendingAccount{id: uuid.NewString(), digest: digest, identity: identity}
		s.mu.Lock()
		if previousPending != nil && previousPending.digest == digest {
			pending = previousPending
		}
		s.pending = pending
		s.mu.Unlock()
		next.State = Unknown
		next.PendingID = &pending.id
		next.Message = ptr("检测到尚未保存的当前登录账号")
	case store.ReconcileAmbiguous:
		next.State = Ambiguous
		next.Message = ptr("多个已保存账号匹配当前身份，未自动覆盖")
	case store.ReconcileIdentityMissing:
		next.State = Ambiguous
		next.Message = ptr("当前认证缺少可安全识别的用户或工作空间信息")
	default:
		next.State = Failed
		next.Message = ptr("自动同步返回了未知状态")
	}
	s.publish(next)
	if result.State != nil && s.opts.AccountsChanged != nil {
		s.opts.AccountsChanged(*result.State)
	}
}

func (s *Service) AddPendingCurrentAccount(pendingID, name string) (store.AccountsState, error) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	s.mu.Lock()
	pending := s.pending
	s.mu.Unlock()
	if pending == nil || pending.id != pendingID {
		return store.AccountsState{}, errors.New("待添加的当前账号已变化，请重新检查")
	}
	auth, err := s.readNormalized()
	if err != nil {
		return store.AccountsState{}, errors.New("无法读取待添加的当前认证文件")
	}
	digest := sha256.Sum256(auth)
	identity, err := store.IdentifyAuth(auth)
	if err != nil || digest != pending.digest || !identity.Equal(pending.identity) {
		return store.AccountsState{}, errors.New("当前认证文件已变化，请重新检查")
	}
	state, err := s.store.AddCurrentAccount(name, auth)
	if err != nil {
		return store.AccountsState{}, err
	}
	now := s.opts.Now().UTC().Format(time.RFC3339Nano)
	var accountID, accountName *string
	for _, account := range state.Accounts {
		if account.IsActive {
			id, displayName := account.ID, account.Name
			accountID, accountName = &id, &displayName
			break
		}
	}
	s.mu.Lock()
	s.pending = nil
	s.digest, s.hasDigest = digest, true
	s.mu.Unlock()
	s.publish(Status{Enabled: true, State: UpToDate, AccountID: accountID, AccountName: accountName, CheckedAt: &now, SyncedAt: &now, Message: ptr("当前登录账号已保存并启用")})
	if s.opts.AccountsChanged != nil {
		s.opts.AccountsChanged(state)
	}
	return state, nil
}

func (s *Service) readNormalized() ([]byte, error) {
	var raw []byte
	var err error
	delays := []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}
	for attempt := 0; attempt <= len(delays); attempt++ {
		raw, err = s.opts.ReadFile(s.store.TargetAuthPath, MaxAuthBytes)
		if err == nil && json.Valid(raw) {
			normalized, normalizeErr := store.NormalizeAuth(raw)
			if normalizeErr != nil {
				return nil, invalidAuthError{err: normalizeErr}
			}
			return normalized, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if attempt == len(delays) || !s.opts.Wait(s.ctx, delays[attempt]) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	return nil, invalidAuthError{err: errors.New("auth.json 内容不是合法 JSON")}
}

func (s *Service) failureStatus(err error, checkedAt string) Status {
	next := Status{Enabled: true, CheckedAt: &checkedAt, State: Failed, Message: ptr("无法检查当前认证文件")}
	var invalid invalidAuthError
	s.mu.Lock()
	next.SyncedAt = s.status.SyncedAt
	s.pending = nil
	s.hasDigest = false
	s.mu.Unlock()
	switch {
	case errors.Is(err, os.ErrNotExist):
		next.State = Missing
		next.Message = ptr("未检测到文件形式的 Codex 认证")
	case errors.Is(err, errTooLarge), errors.Is(err, errNotRegular):
		next.State = Invalid
		next.Message = ptr(err.Error())
	case errors.As(err, &invalid):
		next.State = Invalid
		next.Message = ptr("当前 auth.json 无效，未修改已保存账号")
	default:
		next.State = Failed
		next.Message = ptr("无法读取当前 auth.json，已保留原凭证")
	}
	return next
}

func (s *Service) enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Enabled
}

func (s *Service) publish(next Status) {
	s.mu.Lock()
	changed := !sameEventStatus(s.status, next)
	s.status = cloneStatus(next)
	s.mu.Unlock()
	if changed && s.opts.Emit != nil {
		s.opts.Emit(cloneStatus(next))
	}
}

func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()
		s.checkMu.Lock()
		s.checkMu.Unlock()
	})
}

func readBoundedFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errNotRegular
	}
	if info.Size() > max {
		return nil, errTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errTooLarge
	}
	return b, nil
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func sameEventStatus(a, b Status) bool {
	return a.Enabled == b.Enabled && a.State == b.State && equal(a.AccountID, b.AccountID) &&
		equal(a.AccountName, b.AccountName) && equal(a.SyncedAt, b.SyncedAt) &&
		equal(a.PendingID, b.PendingID) && equal(a.Message, b.Message)
}

func equal(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func cloneStatus(status Status) Status {
	status.AccountID = copyString(status.AccountID)
	status.AccountName = copyString(status.AccountName)
	status.CheckedAt = copyString(status.CheckedAt)
	status.SyncedAt = copyString(status.SyncedAt)
	status.PendingID = copyString(status.PendingID)
	status.Message = copyString(status.Message)
	return status
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func ptr(value string) *string { return &value }
