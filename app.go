package main

import (
	"context"
	"errors"
	"github.com/wailsapp/wails/v3/pkg/application"
	"os"
	"path/filepath"
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
	home, priceDir string
}
type ChosenFile struct {
	FilePath string `json:"filePath"`
	FileName string `json:"fileName"`
	AuthJSON string `json:"authJson"`
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
	}
	return state, err
}
func (s *AppService) RemoveAccount(accountID string) (store.AccountsState, error) {
	state, err := s.store.RemoveAccount(accountID)
	if err == nil {
		s.notify(state)
	}
	return state, err
}
func (s *AppService) SwitchAccount(accountID string) (store.AccountsState, error) {
	state, err := s.store.SwitchAccount(accountID)
	if err == nil {
		s.notify(state)
	}
	return state, err
}
func (s *AppService) UpdateAccountAuth(accountID string, confirmMismatch bool) (store.AuthUpdate, error) {
	r, err := s.store.UpdateAccountAuth(accountID, confirmMismatch)
	if err == nil && r.State != nil {
		s.notify(*r.State)
	}
	return r, err
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
func (s *AppService) GetSettings() scheduler.Settings {
	if s.scheduler == nil {
		return scheduler.Settings{}
	}
	return s.scheduler.Settings()
}
func (s *AppService) DetectCodexCLIPath() *string { return scheduler.DetectCLIPath() }
func (s *AppService) SaveSettings(settings scheduler.Settings) (scheduler.Settings, error) {
	return s.scheduler.Save(s.ctx, settings)
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
func (s *AppService) batchFinished(ctx context.Context) {
	state, err := s.store.ListAccounts()
	if err != nil {
		return
	}
	s.notify(state)
	quotas := s.usage.AccountQuotas(ctx, state)
	if ctx.Err() == nil {
		s.app.Event.Emit("scheduled-quotas-changed", quotas)
	}
}
