// Prevents a console window from appearing on Windows in release builds.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod store;
mod usage;

use std::sync::Mutex;
use store::{AccountsState, Store};
#[cfg(target_os = "macos")]
use tauri::tray::TrayIconBuilder;
use tauri::{
    menu::{
        CheckMenuItemBuilder, MenuBuilder, MenuItemBuilder, PredefinedMenuItem, SubmenuBuilder,
    },
    AppHandle, Emitter, Manager,
};

// ── Data dir resolution ────────────────────────────────────────────────────

fn get_data_dir(app: &tauri::App) -> std::path::PathBuf {
    if let Ok(dir) = std::env::var("CODEX_SWITCH_DATA_DIR") {
        return std::path::PathBuf::from(dir);
    }
    if cfg!(debug_assertions) {
        // Dev: use <project-root>/data, matching original Electron behaviour.
        std::env::current_dir().unwrap_or_default().join("data")
    } else {
        app.path()
            .app_local_data_dir()
            .unwrap_or_default()
            .join("data")
    }
}

// ── IPC return types ───────────────────────────────────────────────────────

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct ChosenFile {
    file_path: String,
    file_name: String,
    auth_json: String,
}

// ── Tauri commands ─────────────────────────────────────────────────────────
// Tauri automatically maps JS camelCase argument keys to Rust snake_case
// parameter names (e.g. JS `authJson` → Rust `auth_json`).

#[tauri::command]
fn list_accounts(store: tauri::State<Mutex<Store>>) -> Result<AccountsState, String> {
    store
        .lock()
        .map_err(|_| "Store lock poisoned".to_string())?
        .list_accounts()
}

#[tauri::command]
fn add_account(
    app: AppHandle,
    store: tauri::State<Mutex<Store>>,
    name: String,
    auth_json: String,
) -> Result<AccountsState, String> {
    store
        .lock()
        .map_err(|_| "Store lock poisoned".to_string())?
        .add_account(&name, &auth_json)?;
    notify_state_changed(&app, &store)
}

#[tauri::command]
fn remove_account(
    app: AppHandle,
    store: tauri::State<Mutex<Store>>,
    account_id: String,
) -> Result<AccountsState, String> {
    store
        .lock()
        .map_err(|_| "Store lock poisoned".to_string())?
        .remove_account(&account_id)?;
    notify_state_changed(&app, &store)
}

#[tauri::command]
fn switch_account(
    app: AppHandle,
    store: tauri::State<Mutex<Store>>,
    account_id: String,
) -> Result<AccountsState, String> {
    store
        .lock()
        .map_err(|_| "Store lock poisoned".to_string())?
        .switch_account(&account_id)?;
    notify_state_changed(&app, &store)
}

#[tauri::command]
async fn get_usage_stats(
    store: tauri::State<'_, Mutex<Store>>,
    days: u32,
    refresh_prices: Option<bool>,
) -> Result<usage::UsageStats, String> {
    let data_dir = store
        .lock()
        .map_err(|_| "Store lock poisoned".to_string())?
        .data_dir()
        .to_path_buf();
    usage::get_usage_stats(&data_dir, days, refresh_prices.unwrap_or(false)).await
}

#[tauri::command]
async fn get_account_quotas(
    store: tauri::State<'_, Mutex<Store>>,
) -> Result<usage::AccountQuotas, String> {
    let state = store
        .lock()
        .map_err(|_| "Store lock poisoned".to_string())?
        .list_accounts()?;
    Ok(usage::get_account_quotas(&state).await)
}

#[tauri::command]
fn open_url(url: String) -> Result<(), String> {
    open::that(&url).map_err(|e| format!("无法打开链接: {e}"))
}

#[tauri::command]
async fn choose_auth_file(_app: AppHandle) -> Result<Option<ChosenFile>, String> {
    let default_dir = dirs::home_dir()
        .map(|h| h.join(".codex"))
        .filter(|p| p.exists());

    // On macOS, open a native NSOpenPanel with showsHiddenFiles = YES to match
    // the original Electron behaviour (properties: ['openFile', 'showHiddenFiles']).
    // tauri-plugin-dialog 2.x does not expose a show-hidden-files toggle, so we
    // call NSOpenPanel directly via the objc2 bindings that are already in the dep
    // tree.  On other platforms we fall back to rfd (hidden-file support is not
    // yet released there, but the regression is macOS-specific).
    #[cfg(target_os = "macos")]
    let picked = pick_file_macos(&_app, default_dir).await?;

    #[cfg(not(target_os = "macos"))]
    let picked = {
        let mut dialog = rfd::AsyncFileDialog::new()
            .set_title("选择 Codex auth.json")
            .add_filter("JSON", &["json"]);
        if let Some(dir) = default_dir {
            dialog = dialog.set_directory(dir);
        }
        dialog.pick_file().await.map(|f| f.path().to_path_buf())
    };

    match picked {
        Some(path_buf) => {
            let path_str = path_buf.to_string_lossy().to_string();
            let auth_json =
                std::fs::read_to_string(&path_buf).map_err(|e| format!("读取文件失败: {e}"))?;
            let file_name = path_buf
                .file_name()
                .unwrap_or_default()
                .to_string_lossy()
                .to_string();
            Ok(Some(ChosenFile {
                file_path: path_str,
                file_name,
                auth_json,
            }))
        }
        None => Ok(None),
    }
}

// ── macOS file picker (with hidden-file support) ───────────────────────────

#[cfg(target_os = "macos")]
async fn pick_file_macos(
    app: &AppHandle,
    default_dir: Option<std::path::PathBuf>,
) -> Result<Option<std::path::PathBuf>, String> {
    use objc2::MainThreadMarker;
    use objc2_app_kit::{NSModalResponseOK, NSOpenPanel};
    use objc2_foundation::{NSArray, NSString, NSURL};

    let (tx, rx) = tokio::sync::oneshot::channel::<Option<std::path::PathBuf>>();

    app.run_on_main_thread(move || {
        // SAFETY: Tauri guarantees this closure runs on the macOS main thread.
        let mtm = unsafe { MainThreadMarker::new_unchecked() };
        let panel = NSOpenPanel::openPanel(mtm);

        panel.setShowsHiddenFiles(true);
        panel.setCanChooseFiles(true);
        panel.setCanChooseDirectories(false);
        // NSOpenPanel uses `message` as its prompt text (not `title`).
        panel.setMessage(Some(&NSString::from_str("选择 Codex auth.json")));

        // Restrict picker to JSON files.
        let ext = NSString::from_str("json");
        #[allow(deprecated)]
        panel.setAllowedFileTypes(Some(&NSArray::from_retained_slice(&[ext])));

        // Default directory: ~/.codex (if it exists).
        if let Some(dir) = default_dir {
            if let Some(s) = dir.to_str() {
                let url = NSURL::fileURLWithPath_isDirectory(&NSString::from_str(s), true);
                panel.setDirectoryURL(Some(&url));
            }
        }

        let response = panel.runModal();
        let result = if response == NSModalResponseOK {
            panel
                .URLs()
                .firstObject()
                .and_then(|url| url.path())
                .map(|p| std::path::PathBuf::from(p.to_string()))
        } else {
            None
        };

        let _ = tx.send(result);
    })
    .map_err(|e| e.to_string())?;

    rx.await.map_err(|_| "文件选择器内部错误".to_string())
}

// ── State-change broadcast ─────────────────────────────────────────────────
// Reads current state, rebuilds both menus, and emits the `accounts-changed`
// event to the renderer. Called after every mutating command.

fn notify_state_changed(app: &AppHandle, store: &Mutex<Store>) -> Result<AccountsState, String> {
    let state = store
        .lock()
        .map_err(|_| "Store lock poisoned".to_string())?
        .list_accounts()?;

    // Rebuild menus on the macOS main thread — fire-and-forget so we never
    // block the calling thread waiting for the main thread to respond.
    //
    // Why fire-and-forget?  Sync Tauri commands on macOS are invoked directly
    // from WKWebView's userContentController callback, which fires on the main
    // thread.  Blocking here with rx.recv() would deadlock: the main thread
    // waits for itself to run the closure that sends on tx.  Scheduling the
    // rebuild as a queued main-thread task and returning immediately avoids
    // the deadlock; the closure executes once the current call stack returns
    // and the run-loop advances.
    let state_clone = state.clone();
    let app_clone = app.clone();
    let _ = app.run_on_main_thread(move || {
        // Catch panics so a menu-rebuild bug never kills the main thread.
        let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            rebuild_tray_menu(&app_clone, &state_clone)
                .and_then(|_| rebuild_app_menu(&app_clone, &state_clone))
        }));
        match result {
            Ok(Ok(())) => {}
            Ok(Err(e)) => eprintln!("[switch-codex] Menu rebuild failed: {e}"),
            Err(_) => eprintln!("[switch-codex] Menu rebuild panicked"),
        }
    });

    app.emit("accounts-changed", &state)
        .map_err(|e| e.to_string())?;
    Ok(state)
}

// ── Tray context menu ──────────────────────────────────────────────────────

fn build_tray_context_menu(
    app: &AppHandle,
    state: &AccountsState,
) -> tauri::Result<tauri::menu::Menu<tauri::Wry>> {
    let active_name = state
        .active_account_id
        .as_ref()
        .and_then(|id| state.accounts.iter().find(|a| &a.id == id))
        .map(|a| a.name.as_str())
        .unwrap_or("未配置");

    let mut builder = MenuBuilder::new(app)
        .item(
            &MenuItemBuilder::new(format!("当前：{active_name}"))
                .enabled(false)
                .build(app)?,
        )
        .separator();

    if state.accounts.is_empty() {
        builder = builder.item(&MenuItemBuilder::new("暂无账号").enabled(false).build(app)?);
    } else {
        for account in &state.accounts {
            let item = CheckMenuItemBuilder::new(&account.name)
                .id(format!("switch-{}", account.id))
                .checked(state.active_account_id.as_deref() == Some(account.id.as_str()))
                .build(app)?;
            builder = builder.item(&item);
        }
    }

    builder
        .separator()
        .item(
            &MenuItemBuilder::new("打开账号管理")
                .id("open-manager")
                .build(app)?,
        )
        .item(
            &MenuItemBuilder::new("退出 Switch Codex")
                .id("quit")
                .build(app)?,
        )
        .build()
}

fn rebuild_tray_menu(app: &AppHandle, state: &AccountsState) -> tauri::Result<()> {
    if let Some(tray) = app.tray_by_id("main") {
        let menu = build_tray_context_menu(app, state)?;
        tray.set_menu(Some(menu))?;
    }
    Ok(())
}

// ── Application menu (macOS native menu bar) ───────────────────────────────

fn build_app_menu(
    app: &AppHandle,
    state: &AccountsState,
) -> tauri::Result<tauri::menu::Menu<tauri::Wry>> {
    let app_submenu = SubmenuBuilder::new(app, "Switch Codex")
        .item(&PredefinedMenuItem::about(app, None, None)?)
        .separator()
        .item(&PredefinedMenuItem::hide(app, None)?)
        .item(&PredefinedMenuItem::hide_others(app, None)?)
        .item(&PredefinedMenuItem::show_all(app, None)?)
        .separator()
        .item(&PredefinedMenuItem::quit(app, None)?)
        .build()?;

    let mut switch_submenu_builder = SubmenuBuilder::new(app, "切换 Codex 账号");
    if state.accounts.is_empty() {
        switch_submenu_builder = switch_submenu_builder
            .item(&MenuItemBuilder::new("暂无账号").enabled(false).build(app)?);
    } else {
        for account in &state.accounts {
            let item = CheckMenuItemBuilder::new(&account.name)
                .id(format!("switch-{}", account.id))
                .checked(state.active_account_id.as_deref() == Some(account.id.as_str()))
                .build(app)?;
            switch_submenu_builder = switch_submenu_builder.item(&item);
        }
    }
    let switch_submenu = switch_submenu_builder.build()?;

    let accounts_submenu = SubmenuBuilder::new(app, "账号")
        .item(&switch_submenu)
        .separator()
        .item(
            &MenuItemBuilder::new("打开账号管理")
                .id("open-manager")
                .accelerator("CmdOrCtrl+,")
                .build(app)?,
        )
        .build()?;

    let edit_submenu = SubmenuBuilder::new(app, "编辑")
        .item(&PredefinedMenuItem::undo(app, Some("撤销"))?)
        .item(&PredefinedMenuItem::redo(app, Some("重做"))?)
        .separator()
        .item(&PredefinedMenuItem::cut(app, Some("剪切"))?)
        .item(&PredefinedMenuItem::copy(app, Some("复制"))?)
        .item(&PredefinedMenuItem::paste(app, Some("粘贴"))?)
        .item(&PredefinedMenuItem::select_all(app, Some("全选"))?)
        .build()?;

    let window_submenu = SubmenuBuilder::new(app, "窗口")
        .item(&PredefinedMenuItem::minimize(app, Some("最小化"))?)
        .item(&PredefinedMenuItem::close_window(app, Some("关闭"))?)
        .build()?;

    MenuBuilder::new(app)
        .item(&app_submenu)
        .item(&accounts_submenu)
        .item(&edit_submenu)
        .item(&window_submenu)
        .build()
}

fn rebuild_app_menu(app: &AppHandle, state: &AccountsState) -> tauri::Result<()> {
    let menu = build_app_menu(app, state)?;
    app.set_menu(menu)?;
    Ok(())
}

// ── Shared menu-event handler ──────────────────────────────────────────────
// Used by both the application menu and the tray context menu.

// Ensure the main window is visible and focused so the user can see any error
// event emitted to the renderer (mirrors Electron's dialog.showErrorBox which
// was always visible regardless of window state).
fn show_and_focus_main_window(app: &AppHandle) {
    if let Some(win) = app.get_webview_window("main") {
        let _ = win.show();
        let _ = win.set_focus();
    }
}

fn handle_menu_event(app: &AppHandle, id: &str) {
    if id == "open-manager" {
        show_and_focus_main_window(app);
    } else if id == "quit" {
        app.exit(0);
    } else if let Some(account_id) = id.strip_prefix("switch-") {
        let account_id = account_id.to_string();
        let app = app.clone();
        tauri::async_runtime::spawn(async move {
            let store_state = app.state::<Mutex<Store>>();
            let result = {
                store_state
                    .lock()
                    .map_err(|_| "Store lock poisoned".to_string())
                    .and_then(|store| store.switch_account(&account_id))
            };
            match result {
                Ok(()) => {
                    if let Err(e) = notify_state_changed(&app, &store_state) {
                        eprintln!("[switch-codex] notify error: {e}");
                        show_and_focus_main_window(&app);
                        let _ = app.emit("switch-error", e);
                    }
                }
                Err(e) => {
                    eprintln!("[switch-codex] switch account error: {e}");
                    show_and_focus_main_window(&app);
                    let _ = app.emit("switch-error", e);
                }
            }
        });
    }
}

// ── App setup ─────────────────────────────────────────────────────────────

fn setup(app: &mut tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    let data_dir = get_data_dir(app);
    let store = Store::new(data_dir);
    store.ensure_ready()?;

    let initial_state = store.list_accounts()?;

    // Register store as app-managed state (Arc<Mutex<Store>> under the hood).
    app.manage(Mutex::new(store));

    // Application menu (visible on macOS menu bar).
    let app_menu = build_app_menu(app.handle(), &initial_state)?;
    app.set_menu(app_menu)?;

    // Application-menu event handler.
    app.on_menu_event(|app, event| {
        handle_menu_event(app, event.id.0.as_str());
    });

    // Tray icon — macOS only, matching original Electron behaviour where
    // `buildMenuBarTray` early-returned on non-darwin platforms.
    #[cfg(target_os = "macos")]
    {
        let tray_icon_bytes = include_bytes!("../icons/tray-icon.png");
        let tray_icon = tauri::image::Image::from_bytes(tray_icon_bytes)?;

        let tray_menu = build_tray_context_menu(app.handle(), &initial_state)?;

        TrayIconBuilder::with_id("main")
            .icon(tray_icon)
            .tooltip("Switch Codex")
            .menu(&tray_menu)
            .show_menu_on_left_click(true)
            .build(app.handle())?;
    }

    Ok(())
}

// ── Platform-specific window event handling ────────────────────────────────

// On macOS: hide window on close so the tray remains functional (original
// Electron behaviour — the app only truly quits from the tray "Quit" item).
// On other platforms: allow normal close → the OS will quit the app when the
// last window closes, matching the original Electron `window-all-closed` handler.
#[cfg(target_os = "macos")]
fn handle_window_event(window: &tauri::Window, event: &tauri::WindowEvent) {
    if let tauri::WindowEvent::CloseRequested { api, .. } = event {
        window.hide().ok();
        api.prevent_close();
    }
}

#[cfg(not(target_os = "macos"))]
fn handle_window_event(_window: &tauri::Window, _event: &tauri::WindowEvent) {
    // Let the window close normally; the app exits when all windows are closed.
}

// ── Entry point ────────────────────────────────────────────────────────────

fn main() {
    let context = tauri::generate_context!();
    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .setup(setup)
        // Platform-specific window close behaviour — see handle_window_event above.
        .on_window_event(handle_window_event)
        .invoke_handler(tauri::generate_handler![
            list_accounts,
            add_account,
            remove_account,
            switch_account,
            choose_auth_file,
            get_usage_stats,
            get_account_quotas,
            open_url,
        ])
        .build(context)
        .expect("error while building tauri application")
        .run(|_app, event| {
            // macOS Dock reopen: when the user clicks the Dock icon to restore a
            // hidden window, show and focus the main window.
            #[cfg(target_os = "macos")]
            if let tauri::RunEvent::Reopen { .. } = event {
                show_and_focus_main_window(_app);
            }
            #[cfg(not(target_os = "macos"))]
            let _ = (event, &_app);
        });
}
