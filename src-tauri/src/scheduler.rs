use crate::store::{normalize_auth_json, write_file_atomic, Store};
use chrono::{DateTime, Duration as ChronoDuration, Local, NaiveTime, TimeZone, Utc};
use serde::{Deserialize, Serialize};
use std::{
    fs,
    io::Read,
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Condvar, Mutex,
    },
    thread,
    time::{Duration, Instant},
};
use tauri::{AppHandle, Emitter, Manager};

pub const MODEL: &str = "gpt-5.6-luna";
pub const PROMPT: &str = "What model are you?";
const POLL_SECONDS: u64 = 60;
const GRACE_SECONDS: i64 = 300;
const ACCOUNT_TIMEOUT: Duration = Duration::from_secs(120);

#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Settings {
    pub enabled: bool,
    pub time: Option<String>,
    pub cli_path: Option<String>,
}

impl Settings {
    fn validate(&mut self) -> Result<(), String> {
        if let Some(time) = &self.time {
            if time.len() != 5 || NaiveTime::parse_from_str(time, "%H:%M").is_err() {
                return Err("执行时间必须为 HH:mm".into());
            }
        }
        if self.enabled && self.time.is_none() {
            return Err("启用前请选择执行时间".into());
        }
        self.cli_path = self
            .cli_path
            .take()
            .map(|p| p.trim().to_owned())
            .filter(|p| !p.is_empty());
        Ok(())
    }
}

#[derive(Clone, Serialize, Deserialize, PartialEq, Debug)]
#[serde(rename_all = "camelCase")]
pub enum AccountStatus {
    Waiting,
    Running,
    Success,
    Failed,
    Interrupted,
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountResult {
    account_id: String,
    account_name: String,
    status: AccountStatus,
    started_at: Option<String>,
    finished_at: Option<String>,
    message: Option<String>,
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BatchResult {
    started_at: String,
    finished_at: Option<String>,
    accounts: Vec<AccountResult>,
}

#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct SavedState {
    #[serde(default)]
    settings: Settings,
    last_run_date: Option<String>,
    last_run: Option<BatchResult>,
}

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RunStatus {
    next_run_at: Option<String>,
    timezone: String,
    running: bool,
    last_run: Option<BatchResult>,
    error: Option<String>,
}

struct State {
    saved: SavedState,
    next: Option<DateTime<Local>>,
    running: bool,
    error: Option<String>,
    utc_offset: i32,
    resumed_at: i64,
}

// Credentials intentionally have no Serialize or Debug implementation.
struct AccountSnapshot {
    id: String,
    name: String,
    auth: Result<String, String>,
}

pub struct Scheduler {
    // An OS lock also prevents two app instances sharing a data directory from
    // starting duplicate batches or removing each other's temporary auth files.
    _lock: fs::File,
    path: PathBuf,
    runtime: PathBuf,
    state: Mutex<State>,
    wake: Condvar,
    stopping: AtomicBool,
    process: ProcessRunner,
}

fn next_run<T: TimeZone>(
    settings: &Settings,
    now: DateTime<T>,
    last: Option<&str>,
) -> Option<DateTime<T>> {
    if !settings.enabled {
        return None;
    }
    let time = NaiveTime::parse_from_str(settings.time.as_deref()?, "%H:%M").ok()?;
    for days in 0..370 {
        let date = now
            .date_naive()
            .checked_add_signed(ChronoDuration::days(days))?;
        if last.is_some_and(|last| date.to_string().as_str() <= last) {
            continue;
        }
        // A nonexistent DST time is skipped; a repeated time is used only once.
        let candidate = now
            .timezone()
            .from_local_datetime(&date.and_time(time))
            .earliest();
        if let Some(candidate) = candidate.filter(|value| value > &now) {
            return Some(candidate);
        }
    }
    None
}

#[derive(Debug, PartialEq)]
enum Due {
    Wait,
    Start,
    Missed,
}
fn due(now: i64, planned: i64) -> Due {
    match now - planned {
        delay if delay < 0 => Due::Wait,
        delay if delay <= GRACE_SECONDS => Due::Start,
        _ => Due::Missed,
    }
}

fn interrupt_batch(saved: &mut SavedState) {
    if let Some(batch) = &mut saved.last_run {
        if batch.finished_at.is_none() {
            let now = Utc::now().to_rfc3339();
            batch.finished_at = Some(now.clone());
            for account in &mut batch.accounts {
                if matches!(
                    account.status,
                    AccountStatus::Waiting | AccountStatus::Running
                ) {
                    account.status = AccountStatus::Interrupted;
                    account.finished_at = Some(now.clone());
                    account.message = Some("应用退出，任务已中断，不自动补跑".into());
                }
            }
        }
    }
}

impl Scheduler {
    pub fn new(data_dir: &Path, runtime: PathBuf) -> Result<Arc<Self>, String> {
        fs::create_dir_all(data_dir).map_err(|_| "无法创建设置目录")?;
        let lock = fs::OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .open(data_dir.join("scheduler.lock"))
            .map_err(|_| "无法打开任务锁")?;
        lock.try_lock()
            .map_err(|_| "该数据目录已由另一个 Switch Codex 实例使用")?;
        let path = data_dir.join("settings.json");
        let (mut saved, error) = match fs::read_to_string(&path) {
            Ok(raw) => match serde_json::from_str::<SavedState>(&raw)
                .ok()
                .and_then(|mut saved| {
                    saved.settings.validate().ok()?;
                    Some(saved)
                }) {
                Some(saved) => (saved, None),
                _ => (
                    SavedState::default(),
                    Some("设置文件损坏，定时调用已停用；请重新保存设置".into()),
                ),
            },
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => (SavedState::default(), None),
            Err(_) => return Err("无法读取设置文件".into()),
        };
        interrupt_batch(&mut saved);
        if runtime.exists() {
            fs::remove_dir_all(&runtime).map_err(|_| "无法清理上次任务的临时认证目录")?;
        }
        let now = Local::now();
        let next = next_run(&saved.settings, now, saved.last_run_date.as_deref());
        let scheduler = Arc::new(Self {
            _lock: lock,
            path,
            runtime,
            wake: Condvar::new(),
            stopping: AtomicBool::new(false),
            process: ProcessRunner::default(),
            state: Mutex::new(State {
                saved,
                next,
                running: false,
                error,
                utc_offset: now.offset().local_minus_utc(),
                resumed_at: crate::power::state().1,
            }),
        });
        // Preserve an invalid source file for inspection until an explicit save.
        let state = scheduler.state.lock().unwrap();
        if state.error.is_none() {
            scheduler.persist(&state.saved)?;
        }
        drop(state);
        Ok(scheduler)
    }

    fn persist(&self, saved: &SavedState) -> Result<(), String> {
        let raw = serde_json::to_string_pretty(saved).map_err(|_| "无法序列化设置")?;
        write_file_atomic(&self.path, &raw, 0o600).map_err(|_| "无法保存设置或任务状态".into())
    }

    pub fn settings(&self) -> Settings {
        self.state.lock().unwrap().saved.settings.clone()
    }

    pub fn status(&self) -> RunStatus {
        let state = self.state.lock().unwrap();
        RunStatus {
            next_run_at: state.next.map(|v| v.to_rfc3339()),
            timezone: Local::now().format("%Z (UTC%:z)").to_string(),
            running: state.running,
            last_run: state.saved.last_run.clone(),
            error: state.error.clone(),
        }
    }

    pub fn save(&self, mut settings: Settings) -> Result<Settings, String> {
        settings.validate()?;
        if settings.enabled {
            let executable = resolve_cli(settings.cli_path.as_deref())?;
            validate_cli(&executable)?;
        }
        let mut state = self.state.lock().unwrap();
        let mut saved = state.saved.clone();
        saved.settings = settings.clone();
        self.persist(&saved)?;
        state.saved = saved;
        state.next = next_run(
            &settings,
            Local::now(),
            state.saved.last_run_date.as_deref(),
        );
        state.error = None;
        self.wake.notify_all();
        Ok(settings)
    }

    fn emit(&self, app: &AppHandle) {
        let _ = app.emit("scheduled-run-changed", self.status());
    }

    pub fn start(self: &Arc<Self>, app: AppHandle) {
        let scheduler = self.clone();
        thread::spawn(move || {
            while !scheduler.stopping.load(Ordering::SeqCst) {
                scheduler.tick(&app);
                let state = scheduler.state.lock().unwrap();
                if scheduler.stopping.load(Ordering::SeqCst) {
                    break;
                }
                drop(
                    scheduler
                        .wake
                        .wait_timeout(state, Duration::from_secs(POLL_SECONDS))
                        .unwrap(),
                );
            }
        });
    }

    fn tick(self: &Arc<Self>, app: &AppHandle) {
        let now = Local::now();
        let (asleep, resumed_at) = crate::power::state();
        let mut state = self.state.lock().unwrap();
        if asleep || self.stopping.load(Ordering::SeqCst) {
            return;
        }
        let offset = now.offset().local_minus_utc();
        if resumed_at != state.resumed_at || offset != state.utc_offset {
            // Resume is not catch-up. Use the wake instant, so an upcoming time
            // between wake and the next poll can still start normally.
            let base = if offset != state.utc_offset {
                now
            } else {
                Local
                    .timestamp_millis_opt(resumed_at)
                    .single()
                    .unwrap_or(now)
            };
            state.next = next_run(
                &state.saved.settings,
                base,
                state.saved.last_run_date.as_deref(),
            );
            state.resumed_at = resumed_at;
            state.utc_offset = offset;
        }
        let Some(planned) = state.next else {
            return;
        };
        if state.running {
            return;
        }
        match due(now.timestamp(), planned.timestamp()) {
            Due::Wait => {
                drop(state);
                self.emit(app);
                return;
            }
            Due::Missed => {
                state.next = next_run(
                    &state.saved.settings,
                    now,
                    state.saved.last_run_date.as_deref(),
                );
                drop(state);
                self.emit(app);
                return;
            }
            Due::Start => {}
        }
        // Capture ALL auth files before launching the first CLI. The Store lock
        // coordinates snapshots and compare-and-write with account UI mutations.
        let snapshots = app
            .state::<Mutex<Store>>()
            .lock()
            .map_err(|_| "账号存储不可用".to_owned())
            .and_then(|store| snapshot_accounts(&store));
        let snapshots = match snapshots {
            Ok(accounts) => accounts,
            Err(error) => {
                state.error = Some(error);
                state.next = next_run(
                    &state.saved.settings,
                    now,
                    Some(&now.date_naive().to_string()),
                );
                drop(state);
                self.emit(app);
                return;
            }
        };
        let mut saved = state.saved.clone();
        saved.last_run_date = Some(now.date_naive().to_string());
        saved.last_run = Some(BatchResult {
            started_at: now.to_rfc3339(),
            finished_at: None,
            accounts: snapshots
                .iter()
                .map(|a| AccountResult {
                    account_id: a.id.clone(),
                    account_name: a.name.clone(),
                    status: AccountStatus::Waiting,
                    started_at: None,
                    finished_at: None,
                    message: None,
                })
                .collect(),
        });
        // Claim the day durably before any external call. Failed persistence
        // prevents execution; restarting can never replay a claimed batch.
        if let Err(error) = self.persist(&saved) {
            state.error = Some(error);
            state.next = None;
            drop(state);
            self.emit(app);
            return;
        }
        state.saved = saved;
        state.running = true;
        state.error = None;
        state.next = next_run(
            &state.saved.settings,
            now,
            state.saved.last_run_date.as_deref(),
        );
        let cli_path = state.saved.settings.cli_path.clone();
        drop(state);
        self.emit(app);
        let scheduler = self.clone();
        let app = app.clone();
        thread::spawn(move || scheduler.run_batch(&app, snapshots, cli_path));
    }

    fn update_account(
        &self,
        app: &AppHandle,
        index: usize,
        status: AccountStatus,
        message: Option<String>,
    ) {
        let mut state = self.state.lock().unwrap();
        if self.stopping.load(Ordering::SeqCst) {
            return;
        }
        if let Some(account) = state
            .saved
            .last_run
            .as_mut()
            .and_then(|b| b.accounts.get_mut(index))
        {
            if status == AccountStatus::Running {
                account.started_at = Some(Utc::now().to_rfc3339());
            } else {
                account.finished_at = Some(Utc::now().to_rfc3339());
            }
            account.status = status;
            account.message = message;
        }
        if let Err(error) = self.persist(&state.saved) {
            state.error = Some(error);
        }
        drop(state);
        self.emit(app);
    }

    fn run_batch(
        &self,
        app: &AppHandle,
        snapshots: Vec<AccountSnapshot>,
        cli_path: Option<String>,
    ) {
        let cli = resolve_cli(cli_path.as_deref()).and_then(|path| {
            validate_cli(&path)?;
            Ok(path)
        });
        // No scheduling deadline checks in this loop: a claimed queue always
        // drains serially, including accounts waiting more than five minutes.
        drain_queue(&snapshots, &self.stopping, |index, account| {
            self.update_account(app, index, AccountStatus::Running, None);
            let result = cli
                .as_ref()
                .map_err(Clone::clone)
                .and_then(|cli| self.run_account(app, cli, account));
            match result {
                Ok(warning) => self.update_account(app, index, AccountStatus::Success, warning),
                Err(error) => self.update_account(app, index, AccountStatus::Failed, Some(error)),
            }
        });
        let _ = fs::remove_dir_all(&self.runtime);
        let mut state = self.state.lock().unwrap();
        if self.stopping.load(Ordering::SeqCst) {
            return;
        }
        state.running = false;
        if let Some(batch) = &mut state.saved.last_run {
            batch.finished_at = Some(Utc::now().to_rfc3339());
        }
        if let Err(error) = self.persist(&state.saved) {
            state.error = Some(error);
        }
        drop(state);
        self.emit(app);
        let accounts = app
            .state::<Mutex<Store>>()
            .lock()
            .ok()
            .and_then(|store| store.list_accounts().ok());
        if let Some(accounts) = accounts {
            let _ = app.emit("accounts-changed", &accounts);
            let app = app.clone();
            tauri::async_runtime::spawn(async move {
                let quotas = crate::usage::get_account_quotas(&accounts).await;
                let _ = app.emit("scheduled-quotas-changed", &quotas);
            });
        }
    }

    fn run_account(
        &self,
        app: &AppHandle,
        cli: &Path,
        account: &AccountSnapshot,
    ) -> Result<Option<String>, String> {
        let original = account.auth.as_ref().map_err(Clone::clone)?;
        let normalized = normalize_auth_json(original)?;
        let directory = self.runtime.join(uuid::Uuid::new_v4().to_string());
        let _cleanup = Cleanup(directory.clone());
        let home = directory.join("home");
        let work = directory.join("work");
        private_directory(&home)?;
        private_directory(&work)?;
        write_file_atomic(&home.join("auth.json"), &normalized, 0o600)
            .map_err(|_| "无法准备临时认证文件")?;
        let mut command = invocation(cli, &home, &work);
        let result = self.process.run(&mut command, ACCOUNT_TIMEOUT);
        // Refresh can succeed even when the model request fails. Always attempt
        // write-back before cleanup, without logging or serializing the tokens.
        let refreshed = fs::read_to_string(home.join("auth.json"))
            .map_err(|_| "无法读取 CLI 刷新后的凭证".to_owned());
        let warning = refreshed
            .and_then(|refreshed| {
                if refreshed == normalized {
                    return Ok(());
                }
                app.state::<Mutex<Store>>()
                    .lock()
                    .map_err(|_| "账号存储不可用".to_owned())?
                    .persist_refreshed_auth(&account.id, original, &refreshed)
            })
            .err();
        match result.and_then(|output| parse_completion(&output)) {
            Ok(()) => Ok(warning),
            Err(error) => Err(match warning {
                Some(warning) => format!("{error}；{warning}"),
                None => error,
            }),
        }
    }

    pub fn shutdown(&self) {
        self.stopping.store(true, Ordering::SeqCst);
        self.process.stop();
        let mut state = self.state.lock().unwrap();
        let was_running = state.running;
        interrupt_batch(&mut state.saved);
        state.running = false;
        if was_running {
            let _ = self.persist(&state.saved);
        }
        self.wake.notify_all();
    }
}

fn drain_queue<T>(queue: &[T], stopping: &AtomicBool, mut run: impl FnMut(usize, &T)) {
    for (index, item) in queue.iter().enumerate() {
        if stopping.load(Ordering::SeqCst) {
            break;
        }
        run(index, item);
    }
}

fn snapshot_accounts(store: &Store) -> Result<Vec<AccountSnapshot>, String> {
    Ok(store
        .list_accounts()?
        .accounts
        .into_iter()
        .map(|a| AccountSnapshot {
            id: a.id,
            name: a.name,
            auth: fs::read_to_string(a.auth_path).map_err(|_| "无法读取账号认证文件".into()),
        })
        .collect())
}

fn private_directory(path: &Path) -> Result<(), String> {
    fs::create_dir_all(path).map_err(|_| "无法创建临时认证目录")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        // Also protect the account's parent, which contains both home and work.
        for path in [Some(path), path.parent()].into_iter().flatten() {
            fs::set_permissions(path, fs::Permissions::from_mode(0o700))
                .map_err(|_| "无法设置认证目录权限")?;
        }
    }
    Ok(())
}
struct Cleanup(PathBuf);
impl Drop for Cleanup {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn invocation(cli: &Path, home: &Path, work: &Path) -> Command {
    let mut command = cli_command(cli);
    command
        .args([
            "exec",
            "--model",
            MODEL,
            "--sandbox",
            "read-only",
            "--json",
            "--ephemeral",
            "--skip-git-repo-check",
            "--ignore-user-config",
            "--color",
            "never",
        ])
        .args([
            "-c",
            "approval_policy=\"never\"",
            "-c",
            "cli_auth_credentials_store=\"file\"",
            "-c",
            "model_reasoning_effort=\"low\"",
            "-c",
            "web_search=\"disabled\"",
            "-c",
            "features.shell_tool=false",
        ])
        .arg(PROMPT)
        .current_dir(work)
        .env("CODEX_HOME", home)
        .env_remove("CODEX_API_KEY")
        .env_remove("OPENAI_API_KEY")
        .env_remove("OPENAI_BASE_URL")
        .env_remove("CODEX_THREAD_ID")
        .env_remove("CODEX_INTERNAL_ORIGINATOR_OVERRIDE")
        .stdin(Stdio::null())
        .stderr(Stdio::null());
    command
}

fn cli_command(path: &Path) -> Command {
    let mut command = Command::new(path);
    // Finder-launched apps often lack the Node installation directory in PATH.
    // npm's Codex script uses /usr/bin/env node, so include its sibling binary.
    let mut paths = path
        .parent()
        .map(Path::to_path_buf)
        .into_iter()
        .collect::<Vec<_>>();
    paths.extend(
        std::env::var_os("PATH")
            .map(|p| std::env::split_paths(&p).collect::<Vec<_>>())
            .unwrap_or_default(),
    );
    if let Ok(path) = std::env::join_paths(paths) {
        command.env("PATH", path);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x08000000);
    }
    command
}

struct ProcessOutput {
    success: bool,
    stdout: Vec<u8>,
}
#[derive(Default)]
struct ProcessRunner {
    child: Mutex<Option<Child>>,
    stopping: AtomicBool,
}
impl ProcessRunner {
    fn stop(&self) {
        self.stopping.store(true, Ordering::SeqCst);
        if let Some(mut child) = self.child.lock().unwrap().take() {
            terminate(&mut child);
        }
    }

    fn run(&self, command: &mut Command, timeout: Duration) -> Result<ProcessOutput, String> {
        let mut slot = self.child.lock().unwrap();
        if self.stopping.load(Ordering::SeqCst) {
            return Err("任务已中断".into());
        }
        if slot.is_some() {
            return Err("已有 CLI 调用正在运行".into());
        }
        command
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .stdin(Stdio::null());
        #[cfg(unix)]
        {
            use std::os::unix::process::CommandExt;
            command.process_group(0);
        }
        let mut child = command
            .spawn()
            .map_err(|_| "无法启动 Codex CLI，请检查路径和执行权限")?;
        #[cfg(windows)]
        let _job = match WindowsJob::attach(&child) {
            Ok(job) => job,
            Err(error) => {
                terminate(&mut child);
                return Err(error);
            }
        };
        let stdout = child.stdout.take().ok_or("无法读取 CLI 输出")?;
        *slot = Some(child);
        drop(slot);
        // Bounded output; drain concurrently so pipe capacity never stalls CLI.
        let (sender, receiver) = std::sync::mpsc::channel();
        thread::spawn(move || {
            let mut output = Vec::new();
            let result = stdout
                .take(1024 * 1024 + 1)
                .read_to_end(&mut output)
                .map(|_| output);
            let _ = sender.send(result);
        });
        let start = Instant::now();
        let result = loop {
            let mut slot = self.child.lock().unwrap();
            let Some(child) = slot.as_mut() else {
                break Err("任务已中断".to_owned());
            };
            if start.elapsed() >= timeout {
                terminate(child);
                *slot = None;
                break Err("Codex CLI 调用超时".to_owned());
            }
            match child.try_wait() {
                Ok(Some(status)) => {
                    terminate(child);
                    *slot = None;
                    break Ok(status.success());
                }
                Err(_) => {
                    terminate(child);
                    *slot = None;
                    break Err("无法获取 CLI 执行状态".to_owned());
                }
                Ok(None) => {}
            }
            drop(slot);
            thread::sleep(Duration::from_millis(50));
        };
        #[cfg(windows)]
        drop(_job);
        let success = result?;
        let output = receiver
            .recv_timeout(Duration::from_secs(1))
            .map_err(|_| "无法读取 CLI 输出")?
            .map_err(|_| "无法读取 CLI 输出")?;
        if output.len() > 1024 * 1024 {
            return Err("CLI 输出超出限制".into());
        }
        Ok(ProcessOutput {
            success,
            stdout: output,
        })
    }
}

fn terminate(child: &mut Child) {
    #[cfg(unix)]
    {
        extern "C" {
            fn kill(pid: i32, signal: i32) -> i32;
        }
        // Each invocation owns its process group; kill descendants too, including
        // a child keeping stdout open after the Codex process has exited.
        unsafe {
            kill(-(child.id() as i32), 9);
        }
    }
    let _ = child.kill();
    let _ = child.wait();
}

#[cfg(windows)]
struct WindowsJob(windows_sys::Win32::Foundation::HANDLE);

#[cfg(windows)]
impl WindowsJob {
    fn attach(child: &Child) -> Result<Self, String> {
        use std::os::windows::io::AsRawHandle;
        use windows_sys::Win32::System::JobObjects::*;
        unsafe {
            let handle = CreateJobObjectW(std::ptr::null(), std::ptr::null());
            if handle.is_null() {
                return Err("无法创建 CLI 进程组".into());
            }
            let job = Self(handle);
            let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = std::mem::zeroed();
            info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
            if SetInformationJobObject(
                handle,
                JobObjectExtendedLimitInformation,
                &info as *const _ as *const _,
                std::mem::size_of_val(&info) as u32,
            ) == 0
                || AssignProcessToJobObject(handle, child.as_raw_handle()) == 0
            {
                return Err("无法管理 CLI 进程组".into());
            }
            Ok(job)
        }
    }
}

#[cfg(windows)]
impl Drop for WindowsJob {
    fn drop(&mut self) {
        // Closing this handle also kills descendants if the app exits abruptly.
        unsafe {
            windows_sys::Win32::Foundation::CloseHandle(self.0);
        }
    }
}

fn parse_completion(output: &ProcessOutput) -> Result<(), String> {
    let mut completed = false;
    let mut failed = false;
    for line in output.stdout.split(|b| *b == b'\n') {
        if let Ok(event) = serde_json::from_slice::<serde_json::Value>(line) {
            match event.get("type").and_then(|v| v.as_str()) {
                Some("turn.completed") => completed = true,
                Some("turn.failed") => failed = true,
                _ => {}
            }
        }
    }
    if output.success && completed && !failed {
        Ok(())
    } else {
        Err("调用失败：未收到成功完成事件，请检查账号认证、模型权限、额度或网络".into())
    }
}

fn resolve_cli(configured: Option<&str>) -> Result<PathBuf, String> {
    if let Some(path) = configured {
        let path = PathBuf::from(path);
        #[cfg(windows)]
        if !path
            .extension()
            .is_some_and(|ext| ext.eq_ignore_ascii_case("exe"))
        {
            return Err("Windows 请指定 codex.exe，不支持 .cmd 或 .bat 脚本".into());
        }
        if path.is_absolute() && path.is_file() {
            return Ok(path);
        }
        return Err("CLI 路径必须是存在的可执行文件绝对路径".into());
    }
    let executable = if cfg!(windows) { "codex.exe" } else { "codex" };
    let mut directories: Vec<PathBuf> = std::env::var_os("PATH")
        .map(|p| std::env::split_paths(&p).collect())
        .unwrap_or_default();
    if let Some(home) = dirs::home_dir() {
        directories.extend([
            home.join(".local/bin"),
            home.join(".cargo/bin"),
            home.join(".npm-global/bin"),
        ]);
        if let Ok(versions) = fs::read_dir(home.join(".nvm/versions/node")) {
            let mut versions: Vec<_> = versions
                .flatten()
                .map(|entry| entry.path().join("bin"))
                .collect();
            versions.sort();
            versions.reverse();
            directories.extend(versions);
        }
    }
    directories.extend([
        PathBuf::from("/opt/homebrew/bin"),
        PathBuf::from("/usr/local/bin"),
    ]);
    for directory in &directories {
        let candidate = directory.join(executable);
        if candidate.is_file() {
            return Ok(candidate);
        }
        #[cfg(windows)]
        {
            // npm's .cmd shim cannot be exec'd directly. Prefer its native vendor binary.
            let root = directory.join("node_modules/@openai");
            for package in ["codex/node_modules/@openai/codex-win32-x64", "codex"] {
                let candidate = root
                    .join(package)
                    .join("vendor/x86_64-pc-windows-msvc/codex/codex.exe");
                if candidate.is_file() {
                    return Ok(candidate);
                }
            }
        }
    }
    Err("未找到 Codex CLI，请安装 CLI 或填写可执行文件绝对路径".into())
}

fn validate_cli(path: &Path) -> Result<(), String> {
    let mut command = cli_command(path);
    command.args(["exec", "--help"]);
    let output = ProcessRunner::default().run(&mut command, Duration::from_secs(10))?;
    let help = String::from_utf8_lossy(&output.stdout);
    if !output.success
        || ![
            "--model",
            "--sandbox",
            "--json",
            "--ephemeral",
            "--skip-git-repo-check",
            "--ignore-user-config",
        ]
        .iter()
        .all(|flag| help.contains(flag))
    {
        return Err("Codex CLI 版本不兼容，请升级至支持非交互任务所需参数的版本".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{FixedOffset, Timelike};

    fn temp() -> Cleanup {
        let path = std::env::temp_dir().join(format!(
            "switch-codex-scheduler-test-{}",
            uuid::Uuid::new_v4()
        ));
        fs::create_dir_all(&path).unwrap();
        Cleanup(path)
    }
    fn settings() -> Settings {
        Settings {
            enabled: true,
            time: Some("09:00".into()),
            cli_path: None,
        }
    }
    fn at(hour: u32, minute: u32, second: u32) -> DateTime<FixedOffset> {
        FixedOffset::east_opt(8 * 3600)
            .unwrap()
            .with_ymd_and_hms(2026, 9, 9, hour, minute, second)
            .unwrap()
    }
    fn auth(id: &str, token: &str) -> String {
        serde_json::json!({"tokens":{"account_id":id,"refresh_token":token}}).to_string()
    }
    fn store_with_accounts(root: &Path, count: usize) -> Store {
        let store = Store::new(root.to_owned());
        store.ensure_ready().unwrap();
        let accounts: Vec<_> = (0..count).map(|index| {
            let id = index.to_string();
            let directory = root.join("accounts").join(&id);
            fs::create_dir_all(&directory).unwrap();
            fs::write(directory.join("auth.json"), auth(&id, "original")).unwrap();
            serde_json::json!({"id": id,"name": format!("Account {index}"),"createdAt":"2026-09-09","updatedAt":"2026-09-09"})
        }).collect();
        fs::write(
            root.join("accounts.json"),
            serde_json::json!({"accounts":accounts,"activeAccountId":null}).to_string(),
        )
        .unwrap();
        store
    }

    #[test]
    fn minute_poll_and_five_minute_start_boundary() {
        assert_eq!(POLL_SECONDS, 60);
        let planned = at(9, 0, 0).timestamp();
        assert_eq!(due(at(8, 59, 59).timestamp(), planned), Due::Wait);
        assert_eq!(due(at(9, 0, 0).timestamp(), planned), Due::Start);
        assert_eq!(due(at(9, 5, 0).timestamp(), planned), Due::Start);
        assert_eq!(due(at(9, 5, 1).timestamp(), planned), Due::Missed);
    }

    #[test]
    fn startup_and_resume_skip_past_schedule_even_inside_grace() {
        for now in [at(9, 0, 0), at(9, 0, 1), at(9, 4, 0), at(23, 59, 59)] {
            let next = next_run(&settings(), now, None).unwrap();
            assert_eq!(next.date_naive(), now.date_naive().succ_opt().unwrap());
            assert_eq!(next.hour(), 9);
        }
        let wake = at(8, 59, 50);
        let planned = next_run(&settings(), wake, None).unwrap();
        assert_eq!(
            due(at(9, 0, 30).timestamp(), planned.timestamp()),
            Due::Start
        );
    }

    #[test]
    fn claimed_day_survives_time_edit_and_clock_rewind() {
        let mut changed = settings();
        changed.time = Some("15:00".into());
        let now = at(10, 0, 0);
        assert_eq!(
            next_run(&changed, now, Some("2026-09-09"))
                .unwrap()
                .date_naive(),
            now.date_naive().succ_opt().unwrap()
        );
        assert!(
            next_run(&changed, now, Some("2026-09-10"))
                .unwrap()
                .date_naive()
                .to_string()
                > "2026-09-10".to_string()
        );
        changed.enabled = false;
        assert!(next_run(&changed, now, None).is_none());
    }

    #[test]
    fn local_offset_changes_keep_wall_clock_time() {
        let west = FixedOffset::west_opt(7 * 3600).unwrap();
        let next = next_run(&settings(), at(8, 0, 0).with_timezone(&west), None).unwrap();
        assert_eq!(next.hour(), 9);
        assert_eq!(next.offset().local_minus_utc(), -7 * 3600);
    }

    #[test]
    fn settings_validate_required_time_and_normalize_path() {
        assert!(Settings {
            enabled: true,
            ..Settings::default()
        }
        .validate()
        .is_err());
        for time in ["9:00", "25:00", "12:60", "09:00:00", "abcde"] {
            assert!(Settings {
                time: Some(time.into()),
                ..settings()
            }
            .validate()
            .is_err());
        }
        let mut value = Settings {
            cli_path: Some("   ".into()),
            ..settings()
        };
        value.validate().unwrap();
        assert!(value.cli_path.is_none());
    }

    #[test]
    fn snapshots_survive_account_removal_update_and_addition() {
        let root = temp();
        let store = store_with_accounts(&root.0, 3);
        let queue = snapshot_accounts(&store).unwrap();
        store.remove_account("0").unwrap();
        fs::write(
            root.0.join("accounts/1/auth.json"),
            auth("1", "replacement"),
        )
        .unwrap();
        // Add without changing the user's actual ~/.codex/auth.json.
        let mut index: serde_json::Value =
            serde_json::from_str(&fs::read_to_string(root.0.join("accounts.json")).unwrap())
                .unwrap();
        index["accounts"].as_array_mut().unwrap().push(
            serde_json::json!({"id":"new","name":"New","createdAt":"today","updatedAt":"today"}),
        );
        fs::write(root.0.join("accounts.json"), index.to_string()).unwrap();
        assert_eq!(
            queue.iter().map(|a| a.id.as_str()).collect::<Vec<_>>(),
            ["0", "1", "2"]
        );
        assert_eq!(queue[1].auth.as_ref().unwrap(), &auth("1", "original"));
    }

    #[test]
    fn queue_drains_serially_after_grace_and_future_setting_changes() {
        let queue = [0, 1, 2, 3, 4];
        let stopping = AtomicBool::new(false);
        let mut seconds = 0;
        let mut visits = Vec::new();
        let mut future = settings();
        drain_queue(&queue, &stopping, |index, item| {
            visits.push((*item, seconds));
            if index == 1 {
                future.enabled = false;
                future.time = Some("22:00".into());
            }
            seconds += 120;
        });
        assert_eq!(visits, [(0, 0), (1, 120), (2, 240), (3, 360), (4, 480)]);
        assert!(!future.enabled);
        let mut visits = Vec::new();
        drain_queue(&queue, &stopping, |_, item| {
            visits.push(*item);
            stopping.store(true, Ordering::SeqCst);
        });
        assert_eq!(visits, [0]);
    }

    #[test]
    fn refresh_writeback_rejects_conflicts_removed_accounts_and_wrong_identity() {
        let root = temp();
        let store = store_with_accounts(&root.0, 3);
        let queue = snapshot_accounts(&store).unwrap();
        store
            .persist_refreshed_auth(
                "0",
                queue[0].auth.as_ref().unwrap(),
                &auth("0", "refreshed"),
            )
            .unwrap();
        assert!(fs::read_to_string(root.0.join("accounts/0/auth.json"))
            .unwrap()
            .contains("refreshed"));
        assert!(store
            .persist_refreshed_auth(
                "0",
                queue[0].auth.as_ref().unwrap(),
                &auth("0", "stale-write")
            )
            .is_err());
        assert!(store
            .persist_refreshed_auth(
                "1",
                queue[1].auth.as_ref().unwrap(),
                &auth("other", "refreshed")
            )
            .is_err());
        store.remove_account("2").unwrap();
        assert!(store
            .persist_refreshed_auth(
                "2",
                queue[2].auth.as_ref().unwrap(),
                &auth("2", "refreshed")
            )
            .is_err());
        assert!(!root.0.join("accounts/2").exists());
    }

    #[test]
    fn persistence_lock_recovery_and_disable_preserve_active_batch() {
        let root = temp();
        let runtime = root.0.join("runtime");
        let scheduler = Scheduler::new(&root.0, runtime.clone()).unwrap();
        assert!(Scheduler::new(&root.0, runtime.clone()).is_err());
        {
            let mut state = scheduler.state.lock().unwrap();
            state.running = true;
            state.saved.last_run_date = Some("2026-09-09".into());
            state.saved.last_run = Some(BatchResult {
                started_at: "2026-09-09T09:00:00+08:00".into(),
                finished_at: None,
                accounts: [
                    AccountStatus::Success,
                    AccountStatus::Running,
                    AccountStatus::Waiting,
                ]
                .into_iter()
                .enumerate()
                .map(|(i, status)| AccountResult {
                    account_id: i.to_string(),
                    account_name: i.to_string(),
                    status,
                    started_at: None,
                    finished_at: None,
                    message: None,
                })
                .collect(),
            });
        }
        scheduler
            .save(Settings {
                time: Some("10:00".into()),
                ..Settings::default()
            })
            .unwrap();
        assert!(scheduler.status().running);
        fs::create_dir_all(&runtime).unwrap();
        fs::write(runtime.join("stale-auth"), "fake").unwrap();
        drop(scheduler); // Simulate a crash; no shutdown hook.
        let scheduler = Scheduler::new(&root.0, runtime.clone()).unwrap();
        assert!(!runtime.exists());
        assert!(!scheduler.settings().enabled);
        let batch = scheduler.status().last_run.unwrap();
        assert!(batch.finished_at.is_some());
        assert_eq!(
            batch
                .accounts
                .iter()
                .map(|a| a.status.clone())
                .collect::<Vec<_>>(),
            [
                AccountStatus::Success,
                AccountStatus::Interrupted,
                AccountStatus::Interrupted
            ]
        );
        assert_eq!(
            scheduler
                .state
                .lock()
                .unwrap()
                .saved
                .last_run_date
                .as_deref(),
            Some("2026-09-09")
        );
    }

    #[test]
    fn invalid_settings_fail_closed_without_overwriting_source() {
        let root = temp();
        fs::write(root.0.join("settings.json"), "broken").unwrap();
        let scheduler = Scheduler::new(&root.0, root.0.join("runtime")).unwrap();
        assert!(!scheduler.settings().enabled);
        assert!(scheduler.status().error.is_some());
        scheduler.shutdown();
        assert_eq!(
            fs::read_to_string(root.0.join("settings.json")).unwrap(),
            "broken"
        );
    }

    #[test]
    fn completion_requires_exit_success_and_final_event() {
        for (success, text, expected) in [
            (true, "{\"type\":\"turn.completed\"}\n", true),
            (false, "{\"type\":\"turn.completed\"}\n", false),
            (true, "I am GPT-5.6 Luna", false),
            (
                true,
                "{\"type\":\"turn.failed\"}\n{\"type\":\"turn.completed\"}\n",
                false,
            ),
        ] {
            assert_eq!(
                parse_completion(&ProcessOutput {
                    success,
                    stdout: text.as_bytes().to_vec()
                })
                .is_ok(),
                expected
            );
        }
    }

    #[cfg(unix)]
    fn fake_cli(root: &Path, script: &str) -> PathBuf {
        use std::os::unix::fs::PermissionsExt;
        let path = root.join("fake codex");
        fs::write(&path, format!("#!/bin/sh\n{script}\n")).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
        path
    }

    #[test]
    #[cfg(unix)]
    fn fake_cli_uses_isolated_auth_fixed_prompt_and_preserves_refresh_on_failure() {
        let root = temp();
        let cli = fake_cli(
            &root.0,
            r#"
[ "$1" = exec ] || exit 1
[ "$3" = gpt-5.6-luna ] || exit 2
[ -z "$CODEX_API_KEY$OPENAI_API_KEY" ] || exit 3
[ -f "$CODEX_HOME/auth.json" ] || exit 4
for last in "$@"; do :; done
[ "$last" = 'What model are you?' ] || exit 5
printf '%s' '{"tokens":{"account_id":"a","refresh_token":"new"}}' > "$CODEX_HOME/auth.json"
printf '%s\n' '{"type":"turn.failed"}'
exit 7
"#,
        );
        let home = root.0.join("isolated");
        let work = root.0.join("empty work");
        private_directory(&home).unwrap();
        private_directory(&work).unwrap();
        fs::write(home.join("auth.json"), auth("a", "old")).unwrap();
        let output = ProcessRunner::default()
            .run(&mut invocation(&cli, &home, &work), Duration::from_secs(2))
            .unwrap();
        assert!(!output.success);
        assert!(fs::read_to_string(home.join("auth.json"))
            .unwrap()
            .contains("new"));
        assert!(parse_completion(&output).is_err());
        assert_eq!(fs::read_dir(&work).unwrap().count(), 0);
    }

    #[test]
    #[cfg(unix)]
    fn timeout_kills_process_and_queue_continues_without_overlap() {
        let root = temp();
        let cli = fake_cli(
            &root.0,
            r#"
case "$1" in
 timeout) exec /bin/sleep 30 ;;
 failure) exit 8 ;;
 *) printf '%s\n' '{"type":"turn.completed"}' ;;
esac
"#,
        );
        let runner = ProcessRunner::default();
        let started = Instant::now();
        let mut results = Vec::new();
        drain_queue(
            &["timeout", "failure", "success", "success"],
            &AtomicBool::new(false),
            |_, arg| {
                let result = runner.run(
                    Command::new(&cli).arg(arg),
                    if *arg == "timeout" {
                        Duration::from_millis(150)
                    } else {
                        Duration::from_secs(3)
                    },
                );
                results.push(result.and_then(|output| parse_completion(&output)).is_ok());
                assert!(runner.child.lock().unwrap().is_none());
            },
        );
        assert_eq!(results, [false, false, true, true]);
        assert!(started.elapsed() < Duration::from_secs(3));
    }

    #[test]
    #[cfg(unix)]
    fn stop_terminates_active_child_and_prevents_new_calls() {
        let runner = Arc::new(ProcessRunner::default());
        let worker = runner.clone();
        let task = thread::spawn(move || {
            worker.run(
                Command::new("/bin/sleep").arg("30"),
                Duration::from_secs(30),
            )
        });
        let start = Instant::now();
        while runner.child.lock().unwrap().is_none() {
            assert!(start.elapsed() < Duration::from_secs(3));
            thread::sleep(Duration::from_millis(10));
        }
        runner.stop();
        assert!(task.join().unwrap().is_err());
        assert!(runner
            .run(&mut Command::new("/usr/bin/true"), Duration::from_secs(1))
            .is_err());
    }

    #[test]
    #[cfg(unix)]
    fn cli_validation_rejects_missing_flags() {
        let root = temp();
        let cli = fake_cli(&root.0, "echo '--model --json'");
        assert!(validate_cli(&cli).is_err());
        assert!(resolve_cli(Some("relative/codex")).is_err());
        let cli = fake_cli(&root.0, "echo '--model --sandbox --json --ephemeral --skip-git-repo-check --ignore-user-config'");
        validate_cli(&cli).unwrap();
    }
}
