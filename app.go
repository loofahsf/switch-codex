package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"switch-codex/internal/authsync"
	"switch-codex/internal/platform"
	"switch-codex/internal/scheduler"
	"switch-codex/internal/store"
	"switch-codex/internal/usage"
)

// AppService is the only bound service. Credentials and process internals never
// become generated bindings; business packages know nothing about Wails.
type AppService struct {
	ctx            context.Context
	cancel         context.CancelFunc
	app            *application.App
	window         *application.WebviewWindow
	tray           *application.SystemTray
	menu, trayMenu *application.Menu
	store          *store.Store
	usage          *usage.Client
	scheduler      *scheduler.Scheduler
	manualWarmup   *scheduler.ManualWarmup
	authSync       *authsync.Service
	home, priceDir string
}
type ChosenFile struct {
	FilePath string `json:"filePath"`
	FileName string `json:"fileName"`
	AuthJSON string `json:"authJson"`
}
type ChosenBackupFile struct {
	FilePath string `json:"filePath"`
	FileName string `json:"fileName"`
}
type BackupTransferResult struct {
	FilePath     string `json:"filePath"`
	FileName     string `json:"fileName"`
	AccountCount int    `json:"accountCount"`
}
type ImportAccountsResult struct {
	State            store.AccountsState `json:"state"`
	AccountCount     int                 `json:"accountCount"`
	AutoSyncDisabled bool                `json:"autoSyncDisabled"`
}
type ConfirmOptions struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

func (s *AppService) ListAccounts() (store.AccountsState, error) {
	if s.store == nil {
		return store.AccountsState{}, errors.New("账号存储尚未就绪")
	}
	return s.store.ListAccounts()
}
func (s *AppService) AddAccount(name, authJSON string) (store.AccountsState, error) {
	state, err := s.store.AddAccount(name, authJSON)
	if err == nil {
		s.notify(state)
		s.authSync.Trigger(true)
	}
	return state, err
}
func (s *AppService) RemoveAccount(accountID string) (store.AccountsState, error) {
	state, err := s.store.RemoveAccount(accountID)
	if err == nil {
		s.notify(state)
		s.authSync.Trigger(true)
	}
	return state, err
}
func (s *AppService) SwitchAccount(accountID string) (store.AccountsState, error) {
	state, err := s.store.SwitchAccount(accountID)
	if err == nil {
		s.notify(state)
		s.authSync.Trigger(true)
	}
	return state, err
}
func (s *AppService) GetAuthSyncStatus() authsync.Status {
	if s.authSync == nil {
		return authsync.Status{Enabled: false, State: authsync.Disabled}
	}
	return s.authSync.Status()
}
func (s *AppService) CheckAuthSyncNow() authsync.Status { return s.authSync.CheckNow() }
func (s *AppService) AddPendingCurrentAccount(pendingID, name string) (store.AccountsState, error) {
	return s.authSync.AddPendingCurrentAccount(pendingID, name)
}
func (s *AppService) GetUsageStats(days uint32, refreshPrices *bool) (usage.UsageStats, error) {
	return s.usage.UsageStats(s.ctx, s.priceDir, filepath.Join(s.home, ".codex", "sessions"), days, refreshPrices != nil && *refreshPrices)
}
func (s *AppService) GetAccountQuotas() (usage.AccountQuotas, error) {
	state, err := s.store.ListAccounts()
	if err != nil {
		return usage.AccountQuotas{}, err
	}
	return s.usage.AccountQuotas(s.ctx, state), nil
}
func (s *AppService) GetAccountQuota(accountID string) (usage.AccountQuotas, error) {
	state, err := s.store.ListAccounts()
	if err != nil {
		return usage.AccountQuotas{}, err
	}
	return s.usage.AccountQuota(s.ctx, state, accountID)
}
func (s *AppService) WarmupAccount(accountID string) error {
	if s.manualWarmup == nil || s.scheduler == nil {
		return errors.New("手动预热尚未就绪")
	}
	// CLI configuration is shared, but the manual executor deliberately does
	// not inspect or mutate any scheduled-task state.
	configuredCLI := s.scheduler.Settings().CLIPath
	defer s.refreshAccountsAfterWarmup(s.ctx)
	return s.manualWarmup.WarmupAccount(accountID, configuredCLI)
}
func (s *AppService) WarmupAllAccounts() error {
	if s.manualWarmup == nil || s.scheduler == nil {
		return errors.New("手动预热尚未就绪")
	}
	configuredCLI := s.scheduler.Settings().CLIPath
	defer s.refreshAccountsAfterWarmup(s.ctx)
	return s.manualWarmup.WarmupAll(configuredCLI)
}
func (s *AppService) GetSettings() scheduler.Settings {
	if s.scheduler == nil {
		return scheduler.Settings{}
	}
	return s.scheduler.Settings()
}
func (s *AppService) DetectCodexCLIPath() *string { return scheduler.DetectCLIPath() }
func (s *AppService) SaveSettings(settings scheduler.Settings) (scheduler.Settings, error) {
	saved, err := s.scheduler.Save(s.ctx, settings)
	if err == nil {
		s.authSync.SetEnabled(saved.AutoSyncAuth)
	}
	return saved, err
}
func (s *AppService) GetScheduledRunStatus() scheduler.RunStatus {
	if s.scheduler == nil {
		return scheduler.RunStatus{}
	}
	return s.scheduler.Status()
}
func (s *AppService) OpenURL(url string) error { return s.app.Browser.OpenURL(url) }
func (s *AppService) ChooseAuthFile() (*ChosenFile, error) {
	d := s.app.Dialog.OpenFile().SetTitle("选择 Codex auth.json").AddFilter("JSON", "*.json").CanChooseFiles(true).CanChooseDirectories(false).ShowHiddenFiles(true).AttachToWindow(s.window)
	if dir := filepath.Join(s.home, ".codex"); isDir(dir) {
		d.SetDirectory(dir)
	}
	path, err := d.PromptForSingleSelection()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("读取文件失败")
	}
	return &ChosenFile{FilePath: path, FileName: filepath.Base(path), AuthJSON: string(b)}, nil
}
func (s *AppService) ChooseAccountsBackup() (*ChosenBackupFile, error) {
	if s.store == nil {
		return nil, errors.New("账号存储尚未就绪")
	}
	state, err := s.store.ListAccounts()
	if err != nil {
		return nil, err
	}
	if len(state.Accounts) != 0 {
		return nil, errors.New("仅允许导入到没有账号的目标库")
	}
	d := s.app.Dialog.OpenFile().SetTitle("选择 Switch Codex 账号备份").
		AddFilter("Switch Codex 账号备份", "*"+store.BackupExtension).
		CanChooseFiles(true).CanChooseDirectories(false).AttachToWindow(s.window)
	path, err := d.PromptForSingleSelection()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	return &ChosenBackupFile{FilePath: path, FileName: filepath.Base(path)}, nil
}

func (s *AppService) ExportAccountsBackup(passphrase string) (*BackupTransferResult, error) {
	if s.store == nil {
		return nil, errors.New("账号存储尚未就绪")
	}
	contents, count, err := s.store.CreateAccountsBackup(passphrase, appVersion)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range contents {
			contents[i] = 0
		}
	}()
	filename := "switch-codex-accounts-" + time.Now().Format("20060102-150405") + store.BackupExtension
	d := s.app.Dialog.SaveFile().SetFilename(filename).
		AddFilter("Switch Codex 账号备份", "*"+store.BackupExtension).
		CanCreateDirectories(true).AttachToWindow(s.window)
	path, err := d.PromptForSingleSelection()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	if !strings.EqualFold(filepath.Ext(path), store.BackupExtension) {
		path += store.BackupExtension
	}
	if err = platform.WriteAtomic(path, contents, 0600); err != nil {
		return nil, errors.New("无法写入账号备份文件")
	}
	return &BackupTransferResult{FilePath: path, FileName: filepath.Base(path), AccountCount: count}, nil
}

func (s *AppService) ImportAccountsBackup(path, passphrase string) (ImportAccountsResult, error) {
	if s.store == nil || s.scheduler == nil || s.authSync == nil {
		return ImportAccountsResult{}, errors.New("账号存储尚未就绪")
	}
	contents, err := readBackupFile(path)
	if err != nil {
		return ImportAccountsResult{}, err
	}
	defer func() {
		for i := range contents {
			contents[i] = 0
		}
	}()
	backup, err := store.DecodeAccountsBackup(contents, passphrase)
	if err != nil {
		return ImportAccountsResult{}, err
	}
	defer backup.Clear()
	previousSync := s.scheduler.Settings().AutoSyncAuth
	s.authSync.SetEnabled(false)
	if previousSync {
		if _, err = s.scheduler.SetAutoSyncAuth(false); err != nil {
			s.authSync.SetEnabled(true)
			return ImportAccountsResult{}, errors.New("无法在导入前关闭认证文件自动同步")
		}
	}
	state, err := s.store.ImportAccountsBackup(backup)
	if err != nil {
		if previousSync {
			if _, restoreErr := s.scheduler.SetAutoSyncAuth(true); restoreErr != nil {
				return ImportAccountsResult{}, fmt.Errorf("%w；恢复认证文件自动同步失败: %v", err, restoreErr)
			}
			s.authSync.SetEnabled(true)
		}
		return ImportAccountsResult{}, err
	}
	s.notify(state)
	return ImportAccountsResult{State: state, AccountCount: backup.AccountCount(), AutoSyncDisabled: true}, nil
}

func readBackupFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("无法读取账号备份文件")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("账号备份不是普通文件")
	}
	if info.Size() > store.MaxBackupBytes {
		return nil, errors.New("账号备份超过 64 MiB 安全限制")
	}
	contents, err := io.ReadAll(io.LimitReader(f, store.MaxBackupBytes+1))
	if err != nil {
		return nil, errors.New("无法读取账号备份文件")
	}
	if len(contents) > store.MaxBackupBytes {
		return nil, errors.New("账号备份超过 64 MiB 安全限制")
	}
	return contents, nil
}
func isDir(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }
func (s *AppService) Confirm(message string, options ConfirmOptions) (bool, error) {
	d := s.app.Dialog.Question()
	if options.Kind == "warning" {
		d = s.app.Dialog.Warning()
	}
	if options.Kind == "error" {
		d = s.app.Dialog.Error()
	}
	d.SetTitle(options.Title).SetMessage(message).AttachToWindow(s.window)
	result := make(chan bool, 1)
	d.AddButton("取消").SetAsCancel().OnClick(func() { result <- false })
	d.AddButton("确认").SetAsDefault().OnClick(func() { result <- true })
	d.Show()
	select {
	case v := <-result:
		return v, nil
	case <-s.ctx.Done():
		return false, nil
	}
}
func (s *AppService) notify(state store.AccountsState) {
	// Queue native menu work, never wait for the UI thread while holding a store
	// lock. A synchronous main-thread round trip deadlocked the old Tauri version.
	application.InvokeAsync(func() { s.rebuildMenus(state) })
	s.app.Event.Emit("accounts-changed", state)
}

// refreshAccountsAfterWarmup only refreshes account-facing data after either
// execution path completes. It does not read or update scheduled task state.
func (s *AppService) refreshAccountsAfterWarmup(ctx context.Context) {
	state, err := s.store.ListAccounts()
	if err != nil {
		return
	}
	s.notify(state)
	if s.authSync != nil {
		s.authSync.Trigger(true)
	}
	quotas := s.usage.AccountQuotas(ctx, state)
	if ctx.Err() == nil {
		s.app.Event.Emit("account-quotas-changed", quotas)
	}
}
