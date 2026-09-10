package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"switch-codex/internal/store"
)

func (s *AppService) showWindow() {
	s.window.Show()
	s.window.Focus()
	if s.authSync != nil {
		s.authSync.Trigger(true)
	}
}
func (s *AppService) switchFromMenu(id string) {
	go func() {
		if _, err := s.SwitchAccount(id); err != nil {
			s.showWindow()
			s.app.Event.Emit("switch-error", err.Error())
		}
	}()
}
func (s *AppService) accountMenu(menu *application.Menu, state store.AccountsState) {
	if len(state.Accounts) == 0 {
		menu.Add("暂无账号").SetEnabled(false)
	}
	for _, account := range state.Accounts {
		id := account.ID
		menu.AddCheckbox(account.Name, account.IsActive).OnClick(func(_ *application.Context) { s.switchFromMenu(id) })
	}
}
func (s *AppService) rebuildMenus(state store.AccountsState) {
	m := application.NewMenu()
	m.AddRole(application.AppMenu)
	accounts := m.AddSubmenu("账号")
	s.accountMenu(accounts.AddSubmenu("切换 Codex 账号"), state)
	accounts.AddSeparator()
	accounts.Add("打开账号管理").SetAccelerator("CmdOrCtrl+,").OnClick(func(_ *application.Context) { s.showWindow() })
	edit := m.AddSubmenu("编辑")
	for _, item := range []struct {
		label string
		role  application.Role
	}{{"撤销", application.Undo}, {"重做", application.Redo}, {"剪切", application.Cut}, {"复制", application.Copy}, {"粘贴", application.Paste}, {"全选", application.SelectAll}} {
		edit.Add(item.label).SetRole(item.role)
	}
	m.AddRole(application.WindowMenu)
	s.app.Menu.Set(m)
	old := s.menu
	s.menu = m
	if old != nil {
		old.Destroy()
	}
	if s.tray != nil {
		menu := application.NewMenu()
		name := "未配置"
		for _, a := range state.Accounts {
			if a.IsActive {
				name = a.Name
			}
		}
		menu.Add("当前：" + name).SetEnabled(false)
		menu.AddSeparator()
		s.accountMenu(menu, state)
		menu.AddSeparator()
		menu.Add("打开账号管理").OnClick(func(_ *application.Context) { s.showWindow() })
		menu.Add("退出 Switch Codex").OnClick(func(_ *application.Context) { s.app.Quit() })
		s.tray.SetMenu(menu)
		old := s.trayMenu
		s.trayMenu = menu
		if old != nil {
			old.Destroy()
		}
	}
}
