package main

import (
	"context"
	"embed"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"switch-codex/internal/authsync"
	"switch-codex/internal/platform"
	"switch-codex/internal/scheduler"
	"switch-codex/internal/store"
	"switch-codex/internal/usage"
)

//go:embed all:dist
var assets embed.FS

//go:embed build/tray-icon.png
var trayIcon []byte

//go:embed build/appicon.png
var appIcon []byte

// Set only by the development smoke-test build; production always uses the OS
// home. This lets native regression tests switch synthetic accounts safely.
var nativeTestRoot string

func init() {
	application.RegisterEvent[store.AccountsState]("accounts-changed")
	application.RegisterEvent[string]("switch-error")
	application.RegisterEvent[scheduler.RunStatus]("scheduled-run-changed")
	application.RegisterEvent[usage.AccountQuotas]("scheduled-quotas-changed")
	application.RegisterEvent[authsync.Status]("auth-sync-changed")
}
func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	root, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	override := os.Getenv("CODEX_SWITCH_DATA_DIR")
	if development && nativeTestRoot != "" {
		home = filepath.Join(nativeTestRoot, "home")
		override = filepath.Join(nativeTestRoot, "data")
	}
	dataDir, initErr := platform.DataDir(development, root, home, override)
	ctx, cancel := context.WithCancel(context.Background())
	svc := &AppService{ctx: ctx, cancel: cancel, home: home, usage: usage.NewClient()}
	a := application.New(application.Options{Name: "Switch Codex", Description: "Switch Codex " + appVersion, Icon: appIcon, Services: []application.Service{application.NewService(svc)}, Assets: application.AssetOptions{Handler: application.AssetFileServerFS(assets)}, Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false}, Linux: application.LinuxOptions{ProgramName: "switch-codex"}})
	svc.app = a
	w := a.Window.NewWithOptions(application.WebviewWindowOptions{Name: "main", Title: "Switch Codex", Width: 960, Height: 640, MinWidth: 780, MinHeight: 560, BackgroundColour: application.NewRGB(244, 244, 245), Mac: application.MacWindow{TitleBar: application.MacTitleBarHiddenInset}, URL: "/"})
	svc.window = w
	if initErr == nil {
		svc.store = store.New(dataDir, filepath.Join(home, ".codex", "auth.json"))
		svc.priceDir = dataDir
		if development {
			svc.priceDir = filepath.Join(root, ".cache", "switch-codex")
		}
		svc.scheduler, initErr = scheduler.New(svc.store, scheduler.Options{Emit: func(event string, data any) { a.Event.Emit(event, data) }, BatchFinished: svc.batchFinished})
		if initErr == nil {
			svc.authSync = authsync.New(svc.store, authsync.Options{
				Enabled:         svc.scheduler.Settings().AutoSyncAuth,
				Emit:            func(status authsync.Status) { a.Event.Emit("auth-sync-changed", status) },
				AccountsChanged: svc.notify,
			})
		}
	}
	if initErr != nil {
		a.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
			go func() {
				a.Dialog.Error().SetTitle("Switch Codex 无法启动").SetMessage(initErr.Error()).Show()
				a.Quit()
			}()
		})
	} else {
		if runtime.GOOS == "darwin" {
			svc.tray = a.SystemTray.New().SetIcon(trayIcon)
			svc.tray.SetTooltip("Switch Codex")
			svc.tray.OnClick(svc.tray.ShowMenu)
			w.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { w.Hide(); e.Cancel() })
			a.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(_ *application.ApplicationEvent) { svc.showWindow() })
		}
		state, _ := svc.store.ListAccounts()
		svc.rebuildMenus(state)
		a.Event.OnApplicationEvent(events.Common.SystemWillSleep, func(_ *application.ApplicationEvent) { svc.scheduler.Sleep() })
		a.Event.OnApplicationEvent(events.Common.SystemDidWake, func(_ *application.ApplicationEvent) {
			svc.scheduler.Wake()
			svc.authSync.Trigger(true)
		})
		a.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
			svc.scheduler.Start()
			svc.authSync.Start()
		})
	}
	a.OnShutdown(func() {
		cancel()
		if svc.authSync != nil {
			svc.authSync.Close()
		}
		if svc.scheduler != nil {
			svc.scheduler.Close()
		}
	})
	if err = a.Run(); err != nil {
		log.Print(err)
	}
}
