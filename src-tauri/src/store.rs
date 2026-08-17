use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};

const AUTH_BACKUP_FILE: &str = "auth.json.switch-codex.bak";

// ── Serialisable data types ────────────────────────────────────────────────

#[derive(Debug, Serialize, Deserialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct Account {
    pub id: String,
    pub name: String,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Index {
    active_account_id: Option<String>,
    accounts: Vec<Account>,
}

#[derive(Debug, Serialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct AccountItem {
    pub id: String,
    pub name: String,
    pub created_at: String,
    pub updated_at: String,
    pub auth_path: String,
    pub is_active: bool,
}

/// Mirror of the JS `listAccounts()` return value sent to the renderer.
#[derive(Debug, Serialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct AccountsState {
    pub data_dir: String,
    pub target_auth_path: String,
    pub active_account_id: Option<String>,
    pub accounts: Vec<AccountItem>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountAuthUpdate {
    pub updated: bool,
    pub stored_account_id: Option<String>,
    pub current_account_id: Option<String>,
}

// ── Store ──────────────────────────────────────────────────────────────────

pub struct Store {
    data_dir: PathBuf,
}

impl Store {
    pub fn new(data_dir: PathBuf) -> Self {
        Store { data_dir }
    }

    pub fn data_dir(&self) -> &Path {
        &self.data_dir
    }

    fn accounts_dir(&self) -> PathBuf {
        self.data_dir.join("accounts")
    }

    fn index_path(&self) -> PathBuf {
        self.data_dir.join("accounts.json")
    }

    fn account_auth_path(&self, account_id: &str) -> PathBuf {
        self.accounts_dir().join(account_id).join("auth.json")
    }

    fn codex_auth_path() -> PathBuf {
        dirs::home_dir()
            .unwrap_or_default()
            .join(".codex")
            .join("auth.json")
    }

    /// Idempotent first-run initialisation (creates dirs + default index file).
    pub fn ensure_ready(&self) -> Result<(), String> {
        std::fs::create_dir_all(self.accounts_dir()).map_err(|e| e.to_string())?;
        let index_path = self.index_path();
        if !index_path.exists() {
            let default = serde_json::json!({ "activeAccountId": null, "accounts": [] });
            let content = format!("{}\n", serde_json::to_string_pretty(&default).unwrap());
            write_file_atomic(&index_path, &content, 0o600).map_err(|e| e.to_string())?;
        }
        Ok(())
    }

    fn read_index(&self) -> Result<Index, String> {
        let raw = std::fs::read_to_string(self.index_path()).map_err(|e| e.to_string())?;
        serde_json::from_str(&raw).map_err(|e| e.to_string())
    }

    fn write_index(&self, index: &Index) -> Result<(), String> {
        let content = format!("{}\n", serde_json::to_string_pretty(index).unwrap());
        write_file_atomic(&self.index_path(), &content, 0o600).map_err(|e| e.to_string())
    }

    pub fn list_accounts(&self) -> Result<AccountsState, String> {
        let index = self.read_index()?;
        let data_dir = self.data_dir.to_string_lossy().to_string();
        let target_auth_path = Self::codex_auth_path().to_string_lossy().to_string();
        let accounts = index
            .accounts
            .iter()
            .map(|a| AccountItem {
                id: a.id.clone(),
                name: a.name.clone(),
                created_at: a.created_at.clone(),
                updated_at: a.updated_at.clone(),
                auth_path: self.account_auth_path(&a.id).to_string_lossy().to_string(),
                is_active: index.active_account_id.as_deref() == Some(a.id.as_str()),
            })
            .collect();

        Ok(AccountsState {
            data_dir,
            target_auth_path,
            active_account_id: index.active_account_id,
            accounts,
        })
    }

    pub fn add_account(&self, name: &str, auth_json: &str) -> Result<(), String> {
        let display_name = name.trim();
        if display_name.is_empty() {
            return Err("账号名字不能为空".to_string());
        }

        let normalized = normalize_auth_json(auth_json)?;
        let mut index = self.read_index()?;

        if index
            .accounts
            .iter()
            .any(|a| a.name.to_lowercase() == display_name.to_lowercase())
        {
            return Err("已经存在同名账号".to_string());
        }

        let id = uuid::Uuid::new_v4().to_string();
        let now = chrono::Utc::now().to_rfc3339();
        let auth_path = self.account_auth_path(&id);

        let auth_dir = auth_path
            .parent()
            .ok_or_else(|| "Invalid auth path (no parent directory)".to_string())?;
        std::fs::create_dir_all(auth_dir).map_err(|e| e.to_string())?;
        write_file_atomic(&auth_path, &normalized, 0o600).map_err(|e| e.to_string())?;

        let account = Account {
            id: id.clone(),
            name: display_name.to_string(),
            created_at: now.clone(),
            updated_at: now,
        };
        let is_first = index.active_account_id.is_none();
        index.accounts.push(account.clone());
        if is_first {
            index.active_account_id = Some(id);
            self.copy_account_to_codex(&account)?;
        }
        self.write_index(&index)
    }

    pub fn remove_account(&self, account_id: &str) -> Result<(), String> {
        let mut index = self.read_index()?;
        if !index.accounts.iter().any(|a| a.id == account_id) {
            return Err("账号不存在".to_string());
        }

        let was_active = index.active_account_id.as_deref() == Some(account_id);
        index.accounts.retain(|a| a.id != account_id);

        if was_active {
            index.active_account_id = None;
        }

        let account_auth_path = self.account_auth_path(account_id);
        let account_dir = account_auth_path
            .parent()
            .ok_or_else(|| "Invalid account path (no parent directory)".to_string())?
            .to_path_buf();
        if account_dir.exists() {
            std::fs::remove_dir_all(&account_dir).map_err(|e| e.to_string())?;
        }
        self.write_index(&index)
    }

    pub fn switch_account(&self, account_id: &str) -> Result<(), String> {
        let mut index = self.read_index()?;
        let account = index
            .accounts
            .iter()
            .find(|a| a.id == account_id)
            .cloned()
            .ok_or_else(|| "账号不存在".to_string())?;
        self.copy_account_to_codex(&account)?;
        index.active_account_id = Some(account_id.to_string());
        self.write_index(&index)
    }

    pub fn update_account_auth(
        &self,
        account_id: &str,
        confirm_mismatch: bool,
    ) -> Result<AccountAuthUpdate, String> {
        let mut index = self.read_index()?;
        let account_index = index
            .accounts
            .iter()
            .position(|account| account.id == account_id)
            .ok_or_else(|| "账号不存在".to_string())?;

        let stored_path = self.account_auth_path(account_id);
        let stored_auth = std::fs::read_to_string(&stored_path)
            .map_err(|e| format!("无法读取账号中保存的 auth.json: {e}"))?;
        let stored_normalized = normalize_auth_json(&stored_auth)?;

        let current_path = Self::codex_auth_path();
        let current_auth = std::fs::read_to_string(&current_path)
            .map_err(|e| format!("无法读取当前 ~/.codex/auth.json: {e}"))?;
        let current_normalized = normalize_auth_json(&current_auth)?;

        let stored_account_id = extract_account_id(&stored_normalized)?;
        let current_account_id = extract_account_id(&current_normalized)?;
        let account_ids_match = account_ids_match(&stored_account_id, &current_account_id);

        if !account_ids_match && !confirm_mismatch {
            return Ok(AccountAuthUpdate {
                updated: false,
                stored_account_id,
                current_account_id,
            });
        }

        write_file_atomic(&stored_path, &current_normalized, 0o600)
            .map_err(|e| format!("更新账号 auth.json 失败: {e}"))?;
        index.accounts[account_index].updated_at = chrono::Utc::now().to_rfc3339();
        self.write_index(&index)?;

        Ok(AccountAuthUpdate {
            updated: true,
            stored_account_id,
            current_account_id,
        })
    }

    fn copy_account_to_codex(&self, account: &Account) -> Result<(), String> {
        let source = self.account_auth_path(&account.id);
        let target = Self::codex_auth_path();
        let auth_json = std::fs::read_to_string(&source).map_err(|e| e.to_string())?;
        let normalized = normalize_auth_json(&auth_json)?;

        let target_dir = target
            .parent()
            .ok_or_else(|| "Invalid target path (no parent directory)".to_string())?;
        std::fs::create_dir_all(target_dir).map_err(|e| e.to_string())?;

        let backup = target_dir.join(AUTH_BACKUP_FILE);
        if target.exists() {
            std::fs::copy(&target, &backup).map_err(|e| e.to_string())?;
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                std::fs::set_permissions(&backup, std::fs::Permissions::from_mode(0o600))
                    .map_err(|e| e.to_string())?;
            }
        }

        write_file_atomic(&target, &normalized, 0o600).map_err(|e| e.to_string())
    }
}

// ── Helpers ────────────────────────────────────────────────────────────────

pub fn normalize_auth_json(auth_json: &str) -> Result<String, String> {
    let parsed: serde_json::Value =
        serde_json::from_str(auth_json).map_err(|_| "auth.json 内容不是合法 JSON".to_string())?;

    if !parsed.is_object() {
        return Err("auth.json 必须是 JSON 对象".to_string());
    }

    let obj = parsed.as_object().unwrap();

    let has_api_key = obj
        .get("OPENAI_API_KEY")
        .and_then(|v| v.as_str())
        .map(|s| !s.trim().is_empty())
        .unwrap_or(false);

    let has_refresh_token = obj
        .get("tokens")
        .and_then(|v| v.as_object())
        .and_then(|t| t.get("refresh_token"))
        .and_then(|v| v.as_str())
        .map(|s| !s.trim().is_empty())
        .unwrap_or(false);

    if !has_api_key && !has_refresh_token {
        return Err("auth.json 缺少 Codex 登录凭据".to_string());
    }

    Ok(format!(
        "{}\n",
        serde_json::to_string_pretty(&parsed).unwrap()
    ))
}

fn extract_account_id(auth_json: &str) -> Result<Option<String>, String> {
    let parsed: serde_json::Value =
        serde_json::from_str(auth_json).map_err(|_| "auth.json 内容不是合法 JSON".to_string())?;
    Ok(parsed
        .pointer("/tokens/account_id")
        .and_then(|value| value.as_str())
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned))
}

fn account_ids_match(stored: &Option<String>, current: &Option<String>) -> bool {
    stored.is_some() && stored.as_deref() == current.as_deref()
}

/// Atomic write: temp-file + fsync + chmod + rename.
/// Unix: enforces file permissions via `mode`.
/// Non-Unix: mode is ignored (Windows has no POSIX permission bits).
#[cfg(unix)]
pub fn write_file_atomic(path: &Path, contents: &str, mode: u32) -> std::io::Result<()> {
    use std::io::Write;
    use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};

    let dir = path.parent().ok_or_else(|| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "path has no parent directory",
        )
    })?;
    let temp_name = format!(
        ".{}.{}.{}.tmp",
        path.file_name().unwrap_or_default().to_string_lossy(),
        std::process::id(),
        uuid::Uuid::new_v4()
    );
    let temp_path = dir.join(&temp_name);
    std::fs::create_dir_all(dir)?;

    let result = (|| -> std::io::Result<()> {
        let file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true) // O_EXCL equivalent
            .mode(mode)
            .open(&temp_path)?;
        {
            let mut w = std::io::BufWriter::new(&file);
            w.write_all(contents.as_bytes())?;
            w.flush()?;
        }
        file.sync_all()?;
        std::fs::set_permissions(&temp_path, std::fs::Permissions::from_mode(mode))?;
        std::fs::rename(&temp_path, path)?;
        // Best-effort directory fsync
        if let Ok(d) = std::fs::File::open(dir) {
            let _ = d.sync_all();
        }
        Ok(())
    })();

    if result.is_err() {
        let _ = std::fs::remove_file(&temp_path);
    }
    result
}

#[cfg(not(unix))]
pub fn write_file_atomic(path: &Path, contents: &str, _mode: u32) -> std::io::Result<()> {
    use std::io::Write;

    let dir = path.parent().ok_or_else(|| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "path has no parent directory",
        )
    })?;
    let temp_name = format!(
        ".{}.{}.{}.tmp",
        path.file_name().unwrap_or_default().to_string_lossy(),
        std::process::id(),
        uuid::Uuid::new_v4()
    );
    let temp_path = dir.join(&temp_name);
    std::fs::create_dir_all(dir)?;

    let result = (|| -> std::io::Result<()> {
        let file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temp_path)?;
        {
            let mut w = std::io::BufWriter::new(&file);
            w.write_all(contents.as_bytes())?;
            w.flush()?;
        }
        file.sync_all()?;
        std::fs::rename(&temp_path, path)?;
        Ok(())
    })();

    if result.is_err() {
        let _ = std::fs::remove_file(&temp_path);
    }
    result
}

#[cfg(test)]
mod tests {
    use super::{account_ids_match, extract_account_id};

    #[test]
    fn extracts_trimmed_account_id() {
        let auth = r#"{"tokens":{"account_id":"  account-123  "}}"#;
        assert_eq!(
            extract_account_id(auth).unwrap().as_deref(),
            Some("account-123")
        );
    }

    #[test]
    fn only_present_equal_account_ids_match() {
        let account_id = Some("account-123".to_string());
        assert!(account_ids_match(&account_id, &account_id));
        assert!(!account_ids_match(
            &account_id,
            &Some("account-456".to_string())
        ));
        assert!(!account_ids_match(&None, &None));
    }
}
