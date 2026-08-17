use crate::store::{write_file_atomic, AccountsState};
use chrono::{DateTime, Local, Utc};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{
    collections::{BTreeMap, HashMap},
    fs::File,
    io::{BufRead, BufReader},
    path::{Path, PathBuf},
    time::Duration,
};

const OFFICIAL_PRICING_URL: &str = "https://developers.openai.com/api/docs/pricing.md";
const CODEX_USAGE_URL: &str = "https://chatgpt.com/backend-api/wham/usage";
const PRICING_CACHE_FILE: &str = "openai-model-prices.json";
const LONG_CONTEXT_THRESHOLD: u64 = 272_000;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ModelPrice {
    model: String,
    input: f64,
    cached_input: Option<f64>,
    cache_write: Option<f64>,
    output: f64,
    long_input: Option<f64>,
    long_cached_input: Option<f64>,
    long_cache_write: Option<f64>,
    long_output: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct CachedPriceCatalog {
    source_url: String,
    fetched_at: String,
    prices: Vec<ModelPrice>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PricingSource {
    url: String,
    fetched_at: String,
    kind: String,
    model_count: usize,
    warning: Option<String>,
}

#[derive(Debug, Clone)]
struct PriceCatalog {
    source: PricingSource,
    prices: Vec<ModelPrice>,
}

#[derive(Debug, Clone, Copy, Default, Deserialize)]
#[serde(default)]
struct TokenUsage {
    input_tokens: u64,
    cached_input_tokens: u64,
    cache_write_input_tokens: u64,
    output_tokens: u64,
    reasoning_output_tokens: u64,
}

impl TokenUsage {
    fn saturating_delta(self, previous: Self) -> Self {
        Self {
            input_tokens: self.input_tokens.saturating_sub(previous.input_tokens),
            cached_input_tokens: self
                .cached_input_tokens
                .saturating_sub(previous.cached_input_tokens),
            cache_write_input_tokens: self
                .cache_write_input_tokens
                .saturating_sub(previous.cache_write_input_tokens),
            output_tokens: self.output_tokens.saturating_sub(previous.output_tokens),
            reasoning_output_tokens: self
                .reasoning_output_tokens
                .saturating_sub(previous.reasoning_output_tokens),
        }
    }

    fn add_assign(&mut self, other: Self) {
        self.input_tokens = self.input_tokens.saturating_add(other.input_tokens);
        self.cached_input_tokens = self
            .cached_input_tokens
            .saturating_add(other.cached_input_tokens);
        self.cache_write_input_tokens = self
            .cache_write_input_tokens
            .saturating_add(other.cache_write_input_tokens);
        self.output_tokens = self.output_tokens.saturating_add(other.output_tokens);
        self.reasoning_output_tokens = self
            .reasoning_output_tokens
            .saturating_add(other.reasoning_output_tokens);
    }

    fn total_tokens(self) -> u64 {
        self.input_tokens.saturating_add(self.output_tokens)
    }

    fn is_empty(self) -> bool {
        self.input_tokens == 0 && self.output_tokens == 0
    }
}

#[derive(Debug, Deserialize)]
struct RolloutEvent {
    timestamp: Option<String>,
    #[serde(rename = "type")]
    event_type: String,
    #[serde(default)]
    payload: RolloutPayload,
}

#[derive(Debug, Default, Deserialize)]
struct RolloutPayload {
    #[serde(rename = "type")]
    event_type: Option<String>,
    turn_id: Option<String>,
    model: Option<String>,
    info: Option<TokenInfo>,
    duration_ms: Option<u64>,
    time_to_first_token_ms: Option<u64>,
}

#[derive(Debug, Deserialize)]
struct TokenInfo {
    total_token_usage: Option<TokenUsage>,
    last_token_usage: Option<TokenUsage>,
}

#[derive(Debug, Clone, Default)]
struct ModelAccumulator {
    usage: TokenUsage,
    model_calls: u64,
    turn_count: u64,
    turn_tokens: u64,
    duration_samples: u64,
    total_duration_ms: u64,
    time_to_first_token_samples: u64,
    total_time_to_first_token_ms: u64,
    estimated_cost_usd: f64,
    has_price: bool,
}

#[derive(Debug, Clone, Default)]
struct TurnAccumulator {
    model: String,
    usage: TokenUsage,
}

#[derive(Debug, Clone, Default)]
struct DailyAccumulator {
    usage: TokenUsage,
    estimated_cost_usd: f64,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UsageSummary {
    input_tokens: u64,
    cached_input_tokens: u64,
    cache_write_input_tokens: u64,
    output_tokens: u64,
    reasoning_output_tokens: u64,
    total_tokens: u64,
    sessions: u64,
    model_calls: u64,
    estimated_cost_usd: f64,
    unpriced_model_count: usize,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelUsageItem {
    model: String,
    input_tokens: u64,
    cached_input_tokens: u64,
    cache_write_input_tokens: u64,
    output_tokens: u64,
    reasoning_output_tokens: u64,
    total_tokens: u64,
    model_calls: u64,
    turn_count: u64,
    average_tokens_per_turn: Option<f64>,
    average_duration_ms: Option<f64>,
    average_time_to_first_token_ms: Option<f64>,
    estimated_cost_usd: Option<f64>,
    price: Option<DisplayedPrice>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DisplayedPrice {
    input: f64,
    cached_input: Option<f64>,
    cache_write: Option<f64>,
    output: f64,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DailyUsageItem {
    date: String,
    total_tokens: u64,
    estimated_cost_usd: f64,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UsageStats {
    generated_at: String,
    range_days: u32,
    sessions_dir: String,
    pricing_source: PricingSource,
    summary: UsageSummary,
    models: Vec<ModelUsageItem>,
    daily: Vec<DailyUsageItem>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RateLimitWindow {
    used_percent: f64,
    window_minutes: Option<i64>,
    resets_at: Option<i64>,
    reset_after_seconds: Option<i64>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CreditsInfo {
    has_credits: bool,
    unlimited: bool,
    balance: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountQuota {
    account_id: String,
    account_name: String,
    ok: bool,
    plan_type: Option<String>,
    primary: Option<RateLimitWindow>,
    secondary: Option<RateLimitWindow>,
    tertiary: Option<RateLimitWindow>,
    five_hour: Option<RateLimitWindow>,
    weekly: Option<RateLimitWindow>,
    monthly: Option<RateLimitWindow>,
    credits: Option<CreditsInfo>,
    fetched_at: Option<String>,
    error: Option<String>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountQuotas {
    source_url: String,
    accounts: Vec<AccountQuota>,
}

pub async fn get_usage_stats(
    data_dir: &Path,
    days: u32,
    refresh_prices: bool,
) -> Result<UsageStats, String> {
    let catalog = load_price_catalog(data_dir, refresh_prices).await;
    let sessions_dir = dirs::home_dir()
        .unwrap_or_default()
        .join(".codex")
        .join("sessions");
    aggregate_sessions(&sessions_dir, days, catalog)
}

pub async fn get_account_quotas(state: &AccountsState) -> AccountQuotas {
    let client = match reqwest::Client::builder()
        .timeout(Duration::from_secs(12))
        .build()
    {
        Ok(client) => client,
        Err(error) => {
            let message = format!("无法创建网络客户端: {error}");
            return AccountQuotas {
                source_url: CODEX_USAGE_URL.to_string(),
                accounts: state
                    .accounts
                    .iter()
                    .map(|account| quota_error(account.id.clone(), account.name.clone(), &message))
                    .collect(),
            };
        }
    };

    let mut accounts = Vec::with_capacity(state.accounts.len());
    for (index, account) in state.accounts.iter().enumerate() {
        if index > 0 {
            tokio::time::sleep(Duration::from_secs(3)).await;
        }
        let auth_path = if account.is_active {
            PathBuf::from(&state.target_auth_path)
        } else {
            PathBuf::from(&account.auth_path)
        };
        let quota = fetch_account_quota(&client, &account.id, &account.name, &auth_path).await;
        accounts.push(quota);
    }

    AccountQuotas {
        source_url: CODEX_USAGE_URL.to_string(),
        accounts,
    }
}

async fn fetch_account_quota(
    client: &reqwest::Client,
    account_id: &str,
    account_name: &str,
    auth_path: &Path,
) -> AccountQuota {
    let raw = match std::fs::read_to_string(auth_path) {
        Ok(raw) => raw,
        Err(_) => {
            return quota_error(
                account_id.to_string(),
                account_name.to_string(),
                "无法读取该账号的 auth.json",
            );
        }
    };
    let auth: Value = match serde_json::from_str(&raw) {
        Ok(auth) => auth,
        Err(_) => {
            return quota_error(
                account_id.to_string(),
                account_name.to_string(),
                "该账号的 auth.json 不是合法 JSON",
            );
        }
    };

    let access_token = auth
        .pointer("/tokens/access_token")
        .and_then(Value::as_str)
        .filter(|value| !value.trim().is_empty());
    let Some(access_token) = access_token else {
        let message = if auth
            .get("OPENAI_API_KEY")
            .and_then(Value::as_str)
            .is_some_and(|value| !value.trim().is_empty())
        {
            "API Key 账号不提供 ChatGPT Codex 订阅配额"
        } else {
            "auth.json 中没有可用的 Codex access token"
        };
        return quota_error(account_id.to_string(), account_name.to_string(), message);
    };

    let mut request = client
        .get(CODEX_USAGE_URL)
        .bearer_auth(access_token)
        .header(reqwest::header::ACCEPT, "application/json")
        .header(
            reqwest::header::USER_AGENT,
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
        );

    if let Some(chatgpt_account_id) = auth
        .pointer("/tokens/account_id")
        .and_then(Value::as_str)
        .filter(|value| !value.trim().is_empty())
    {
        request = request.header("ChatGPT-Account-Id", chatgpt_account_id);
    }

    let response = match request.send().await {
        Ok(response) => response,
        Err(error) => {
            let err_msg = if error.is_timeout() {
                "网络请求超时，请检查代理连接是否正常".to_string()
            } else if error.is_connect() {
                "网络连接失败，请检查网络代理（建议开启全局代理或 TUN 模式）".to_string()
            } else {
                format!("查询失败: {error}")
            };
            return quota_error(account_id.to_string(), account_name.to_string(), &err_msg);
        }
    };

    if !response.status().is_success() {
        let message = match response.status().as_u16() {
            401 => "登录凭据已过期，请先切换到该账号并让 Codex 完成登录刷新".to_string(),
            403 => "当前账号无权读取 Codex 配额".to_string(),
            status => format!("OpenAI 用量接口返回 HTTP {status}"),
        };
        return quota_error(account_id.to_string(), account_name.to_string(), &message);
    }

    let payload: Value = match response.json().await {
        Ok(payload) => payload,
        Err(_) => {
            return quota_error(
                account_id.to_string(),
                account_name.to_string(),
                "OpenAI 用量接口返回了无法识别的数据",
            );
        }
    };

    let rate_limit = payload.get("rate_limit").unwrap_or(&Value::Null);
    let primary = parse_rate_limit_window(rate_limit.get("primary_window"));
    let secondary = parse_rate_limit_window(rate_limit.get("secondary_window"));
    let tertiary = parse_rate_limit_window(rate_limit.get("tertiary_window"));

    let mut five_hour = None;
    let mut weekly = None;
    let mut monthly = None;

    let mut classify = |window: Option<&RateLimitWindow>, default_sec: i64| {
        if let Some(w) = window {
            let sec = w.window_minutes.map(|m| m * 60).unwrap_or(default_sec);
            if sec <= 86400 {
                if five_hour.is_none() {
                    five_hour = Some(w.clone());
                }
            } else if sec <= 1209600 {
                if weekly.is_none() {
                    weekly = Some(w.clone());
                }
            } else if monthly.is_none() {
                monthly = Some(w.clone());
            }
        }
    };

    classify(primary.as_ref(), 18000);
    classify(secondary.as_ref(), 604800);
    classify(tertiary.as_ref(), 2592000);

    if let Some(obj) = rate_limit.as_object() {
        for (key, val) in obj {
            if key != "primary_window" && key != "secondary_window" && key != "tertiary_window" {
                if let Some(w) = parse_rate_limit_window(Some(val)) {
                    classify(Some(&w), 0);
                }
            }
        }
    }

    AccountQuota {
        account_id: account_id.to_string(),
        account_name: account_name.to_string(),
        ok: true,
        plan_type: payload
            .get("plan_type")
            .and_then(Value::as_str)
            .map(ToString::to_string),
        primary,
        secondary,
        tertiary,
        five_hour,
        weekly,
        monthly,
        credits: payload.get("credits").and_then(parse_credits),
        fetched_at: Some(Utc::now().to_rfc3339()),
        error: None,
    }
}

fn quota_error(account_id: String, account_name: String, message: &str) -> AccountQuota {
    AccountQuota {
        account_id,
        account_name,
        ok: false,
        plan_type: None,
        primary: None,
        secondary: None,
        tertiary: None,
        five_hour: None,
        weekly: None,
        monthly: None,
        credits: None,
        fetched_at: None,
        error: Some(message.to_string()),
    }
}

fn parse_rate_limit_window(value: Option<&Value>) -> Option<RateLimitWindow> {
    let value = value?;
    let used_percent = value.get("used_percent")?.as_f64()?;
    let limit_window_seconds = value
        .get("limit_window_seconds")
        .and_then(Value::as_i64)
        .filter(|seconds| *seconds > 0);
    let window_minutes = limit_window_seconds.map(|seconds| (seconds + 59) / 60);
    let resets_at = value.get("reset_at").and_then(Value::as_i64);
    let reset_after_seconds = value.get("reset_after_seconds").and_then(Value::as_i64);

    Some(RateLimitWindow {
        used_percent,
        window_minutes,
        resets_at,
        reset_after_seconds,
    })
}

fn parse_credits(value: &Value) -> Option<CreditsInfo> {
    Some(CreditsInfo {
        has_credits: value
            .get("has_credits")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        unlimited: value
            .get("unlimited")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        balance: value.get("balance").and_then(value_to_string),
    })
}

fn value_to_string(value: &Value) -> Option<String> {
    match value {
        Value::String(value) => Some(value.clone()),
        Value::Number(value) => Some(value.to_string()),
        _ => None,
    }
}

async fn load_price_catalog(data_dir: &Path, refresh: bool) -> PriceCatalog {
    let cache_path = data_dir.join(PRICING_CACHE_FILE);
    let mut refresh_warning = None;

    if refresh {
        match fetch_official_prices().await {
            Ok(catalog) => {
                if let Ok(content) = serde_json::to_string_pretty(&catalog) {
                    let _ = write_file_atomic(&cache_path, &(content + "\n"), 0o600);
                }
                return PriceCatalog {
                    source: PricingSource {
                        url: catalog.source_url,
                        fetched_at: catalog.fetched_at,
                        kind: "live".to_string(),
                        model_count: catalog.prices.len(),
                        warning: None,
                    },
                    prices: catalog.prices,
                };
            }
            Err(error) => {
                refresh_warning = Some(format!("官方价格刷新失败，已使用本地价格: {error}"));
            }
        }
    }

    if let Ok(raw) = std::fs::read_to_string(&cache_path) {
        if let Ok(catalog) = serde_json::from_str::<CachedPriceCatalog>(&raw) {
            if !catalog.prices.is_empty() {
                return PriceCatalog {
                    source: PricingSource {
                        url: catalog.source_url,
                        fetched_at: catalog.fetched_at,
                        kind: "cache".to_string(),
                        model_count: catalog.prices.len(),
                        warning: refresh_warning,
                    },
                    prices: catalog.prices,
                };
            }
        }
    }

    let fallback: CachedPriceCatalog =
        serde_json::from_str(include_str!("openai_pricing_fallback.json"))
            .expect("bundled OpenAI pricing fallback must be valid JSON");
    PriceCatalog {
        source: PricingSource {
            url: fallback.source_url,
            fetched_at: fallback.fetched_at,
            kind: "bundled".to_string(),
            model_count: fallback.prices.len(),
            warning: refresh_warning,
        },
        prices: fallback.prices,
    }
}

async fn fetch_official_prices() -> Result<CachedPriceCatalog, String> {
    let client = reqwest::Client::builder()
        .timeout(Duration::from_secs(12))
        .build()
        .map_err(|error| error.to_string())?;
    let response = client
        .get(OFFICIAL_PRICING_URL)
        .header(reqwest::header::ACCEPT, "text/markdown")
        .header(reqwest::header::USER_AGENT, "switch-codex/pricing")
        .send()
        .await
        .map_err(|error| error.to_string())?;
    if !response.status().is_success() {
        return Err(format!("HTTP {}", response.status().as_u16()));
    }
    let body = response.text().await.map_err(|error| error.to_string())?;
    let prices = parse_official_pricing(&body);
    if prices.is_empty() {
        return Err("官方价格页格式无法识别".to_string());
    }
    Ok(CachedPriceCatalog {
        source_url: OFFICIAL_PRICING_URL.to_string(),
        fetched_at: Utc::now().to_rfc3339(),
        prices,
    })
}

fn parse_official_pricing(markdown: &str) -> Vec<ModelPrice> {
    let mut prices = HashMap::<String, ModelPrice>::new();

    if let Some(section) = markdown
        .split("### Standard pricing data")
        .nth(1)
        .and_then(|section| section.split("Regional processing").next())
    {
        for line in section.lines() {
            let columns = table_columns(line);
            if columns.len() < 9 || columns[0] == "Model" || columns[0].starts_with("---") {
                continue;
            }
            let model = columns[0]
                .split(" (<")
                .next()
                .unwrap_or(columns[0])
                .trim()
                .to_string();
            let Some(input) = parse_money(columns[1]) else {
                continue;
            };
            let Some(output) = parse_money(columns[4]) else {
                continue;
            };
            prices.insert(
                model.clone(),
                ModelPrice {
                    model,
                    input,
                    cached_input: parse_money(columns[2]),
                    cache_write: parse_money(columns[3]),
                    output,
                    long_input: parse_money(columns[5]),
                    long_cached_input: parse_money(columns[6]),
                    long_cache_write: parse_money(columns[7]),
                    long_output: parse_money(columns[8]),
                },
            );
        }
    }

    if let Some(section) = markdown
        .rsplit_once("Specialized models")
        .map(|(_, section)| section)
        .and_then(|section| section.split("Batch").next())
    {
        for line in section.lines() {
            let columns = table_columns(line);
            if columns.len() < 5 || columns[0] != "Codex" {
                continue;
            }
            let Some(input) = parse_money(columns[2]) else {
                continue;
            };
            let Some(output) = parse_money(columns[4]) else {
                continue;
            };
            let model = columns[1].to_string();
            prices.insert(
                model.clone(),
                ModelPrice {
                    model,
                    input,
                    cached_input: parse_money(columns[3]),
                    cache_write: None,
                    output,
                    long_input: None,
                    long_cached_input: None,
                    long_cache_write: None,
                    long_output: None,
                },
            );
        }
    }

    let mut prices = prices.into_values().collect::<Vec<_>>();
    prices.sort_by(|left, right| left.model.cmp(&right.model));
    prices
}

fn table_columns(line: &str) -> Vec<&str> {
    if !line.trim_start().starts_with('|') {
        return Vec::new();
    }
    line.trim()
        .trim_matches('|')
        .split('|')
        .map(str::trim)
        .collect()
}

fn parse_money(value: &str) -> Option<f64> {
    let value = value.trim();
    if value == "-" || value.is_empty() {
        return None;
    }
    if value.eq_ignore_ascii_case("free") {
        return Some(0.0);
    }
    value
        .trim_start_matches('$')
        .split_whitespace()
        .next()?
        .replace(',', "")
        .parse()
        .ok()
}

fn aggregate_sessions(
    sessions_dir: &Path,
    days: u32,
    catalog: PriceCatalog,
) -> Result<UsageStats, String> {
    // 根据用户选择的时间范围确定统计截止点，并收集所有本机会话日志。
    let cutoff = if days == 0 {
        None
    } else {
        Some(Utc::now() - chrono::Duration::days(i64::from(days)))
    };
    let mut files = Vec::new();
    collect_jsonl_files(sessions_dir, &mut files)?;

    let mut models = HashMap::<String, ModelAccumulator>::new();
    let mut daily = BTreeMap::<String, DailyAccumulator>::new();
    let mut session_count = 0_u64;

    for path in files {
        let file = match File::open(&path) {
            Ok(file) => file,
            Err(_) => continue,
        };
        let mut current_model = "unknown".to_string();
        let mut active_turn_id = None;
        let mut turns = HashMap::<String, TurnAccumulator>::new();
        let mut previous_total = TokenUsage::default();
        let mut counted_session = false;

        // 逐行解析结构化事件，避免加载或保留用户消息正文。
        for line in BufReader::new(file).lines().map_while(Result::ok) {
            let Ok(event) = serde_json::from_str::<RolloutEvent>(&line) else {
                continue;
            };
            match event.event_type.as_str() {
                "turn_context" => {
                    // 回合上下文同时提供模型和 turn_id，用于后续 Token 与耗时归属。
                    if let Some(model) =
                        event.payload.model.filter(|model| !model.trim().is_empty())
                    {
                        current_model = model;
                    }
                    if let Some(turn_id) = event.payload.turn_id {
                        active_turn_id = Some(turn_id.clone());
                        turns.entry(turn_id).or_default().model = current_model.clone();
                    }
                }
                "event_msg" if event.payload.event_type.as_deref() == Some("task_started") => {
                    // 提前建立回合容器，兼容 turn_context 晚于 task_started 的日志顺序。
                    if let Some(turn_id) = event.payload.turn_id {
                        active_turn_id = Some(turn_id.clone());
                        turns.entry(turn_id).or_default();
                    }
                }
                "event_msg" if event.payload.event_type.as_deref() == Some("token_count") => {
                    // 单个问题可能触发多次模型调用，先累计到当前回合再更新模型总量。
                    let Some(info) = event.payload.info else {
                        continue;
                    };
                    let total = info.total_token_usage;
                    let usage = info
                        .last_token_usage
                        .or_else(|| total.map(|total| total.saturating_delta(previous_total)))
                        .unwrap_or_default();
                    if let Some(total) = total {
                        previous_total = total;
                    }
                    if usage.is_empty() {
                        continue;
                    }

                    let Some(timestamp) = event
                        .timestamp
                        .as_deref()
                        .and_then(|value| DateTime::parse_from_rfc3339(value).ok())
                        .map(|value| value.with_timezone(&Utc))
                    else {
                        continue;
                    };
                    if cutoff.is_some_and(|cutoff| timestamp < cutoff) {
                        continue;
                    }

                    if let Some(turn) = active_turn_id
                        .as_deref()
                        .and_then(|turn_id| turns.get_mut(turn_id))
                    {
                        if turn.model.is_empty() {
                            turn.model = current_model.clone();
                        }
                        turn.usage.add_assign(usage);
                    }

                    if !counted_session {
                        session_count = session_count.saturating_add(1);
                        counted_session = true;
                    }

                    let price = find_price(&current_model, &catalog.prices);
                    let cost = price
                        .map(|price| estimate_cost(usage, price))
                        .unwrap_or(0.0);
                    let model = models.entry(current_model.clone()).or_default();
                    model.usage.add_assign(usage);
                    model.model_calls = model.model_calls.saturating_add(1);
                    model.estimated_cost_usd += cost;
                    model.has_price |= price.is_some();

                    let local_date = timestamp
                        .with_timezone(&Local)
                        .format("%Y-%m-%d")
                        .to_string();
                    let day = daily.entry(local_date).or_default();
                    day.usage.add_assign(usage);
                    day.estimated_cost_usd += cost;
                }
                "event_msg" if event.payload.event_type.as_deref() == Some("task_complete") => {
                    // 仅完整且有 Token 数据的回合进入问题均值，避免旧日志缺字段时被当成零消耗。
                    let Some(turn_id) = event.payload.turn_id else {
                        continue;
                    };
                    if active_turn_id.as_deref() == Some(turn_id.as_str()) {
                        active_turn_id = None;
                    }
                    let Some(turn) = turns.remove(&turn_id).filter(|turn| !turn.usage.is_empty())
                    else {
                        continue;
                    };
                    let Some(timestamp) = event
                        .timestamp
                        .as_deref()
                        .and_then(|value| DateTime::parse_from_rfc3339(value).ok())
                        .map(|value| value.with_timezone(&Utc))
                    else {
                        continue;
                    };
                    if cutoff.is_some_and(|cutoff| timestamp < cutoff) {
                        continue;
                    }

                    let model_name = if turn.model.trim().is_empty() {
                        current_model.clone()
                    } else {
                        turn.model
                    };
                    let model = models.entry(model_name).or_default();
                    model.turn_count = model.turn_count.saturating_add(1);
                    model.turn_tokens = model.turn_tokens.saturating_add(turn.usage.total_tokens());
                    if let Some(duration_ms) = event.payload.duration_ms {
                        model.duration_samples = model.duration_samples.saturating_add(1);
                        model.total_duration_ms =
                            model.total_duration_ms.saturating_add(duration_ms);
                    }
                    if let Some(time_to_first_token_ms) = event.payload.time_to_first_token_ms {
                        model.time_to_first_token_samples =
                            model.time_to_first_token_samples.saturating_add(1);
                        model.total_time_to_first_token_ms = model
                            .total_time_to_first_token_ms
                            .saturating_add(time_to_first_token_ms);
                    }
                }
                "event_msg" if event.payload.event_type.as_deref() == Some("turn_aborted") => {
                    // 中止回合没有完整响应，不参与平均值并释放临时累计数据。
                    if let Some(turn_id) = event.payload.turn_id {
                        turns.remove(&turn_id);
                        if active_turn_id.as_deref() == Some(turn_id.as_str()) {
                            active_turn_id = None;
                        }
                    }
                }
                _ => {}
            }
        }
    }

    // 汇总全局 Token、调用次数与成本，模型性能均值保留在各模型条目中。
    let mut total_usage = TokenUsage::default();
    let mut total_calls = 0_u64;
    let mut total_cost = 0.0;
    for accumulator in models.values() {
        total_usage.add_assign(accumulator.usage);
        total_calls = total_calls.saturating_add(accumulator.model_calls);
        total_cost += accumulator.estimated_cost_usd;
    }

    let mut model_items = models
        .into_iter()
        .map(|(model, accumulator)| {
            let price = find_price(&model, &catalog.prices);
            ModelUsageItem {
                model,
                input_tokens: accumulator.usage.input_tokens,
                cached_input_tokens: accumulator.usage.cached_input_tokens,
                cache_write_input_tokens: accumulator.usage.cache_write_input_tokens,
                output_tokens: accumulator.usage.output_tokens,
                reasoning_output_tokens: accumulator.usage.reasoning_output_tokens,
                total_tokens: accumulator.usage.total_tokens(),
                model_calls: accumulator.model_calls,
                turn_count: accumulator.turn_count,
                average_tokens_per_turn: average(accumulator.turn_tokens, accumulator.turn_count),
                average_duration_ms: average(
                    accumulator.total_duration_ms,
                    accumulator.duration_samples,
                ),
                average_time_to_first_token_ms: average(
                    accumulator.total_time_to_first_token_ms,
                    accumulator.time_to_first_token_samples,
                ),
                estimated_cost_usd: accumulator
                    .has_price
                    .then_some(accumulator.estimated_cost_usd),
                price: price.map(|price| DisplayedPrice {
                    input: price.input,
                    cached_input: price.cached_input,
                    cache_write: price.cache_write,
                    output: price.output,
                }),
            }
        })
        .collect::<Vec<_>>();
    model_items.sort_by_key(|item| std::cmp::Reverse(item.total_tokens));

    let unpriced_model_count = model_items
        .iter()
        .filter(|item| item.estimated_cost_usd.is_none())
        .count();
    let daily = daily
        .into_iter()
        .map(|(date, accumulator)| DailyUsageItem {
            date,
            total_tokens: accumulator.usage.total_tokens(),
            estimated_cost_usd: accumulator.estimated_cost_usd,
        })
        .collect();

    Ok(UsageStats {
        generated_at: Utc::now().to_rfc3339(),
        range_days: days,
        sessions_dir: sessions_dir.to_string_lossy().to_string(),
        pricing_source: catalog.source,
        summary: UsageSummary {
            input_tokens: total_usage.input_tokens,
            cached_input_tokens: total_usage.cached_input_tokens,
            cache_write_input_tokens: total_usage.cache_write_input_tokens,
            output_tokens: total_usage.output_tokens,
            reasoning_output_tokens: total_usage.reasoning_output_tokens,
            total_tokens: total_usage.total_tokens(),
            sessions: session_count,
            model_calls: total_calls,
            estimated_cost_usd: total_cost,
            unpriced_model_count,
        },
        models: model_items,
        daily,
    })
}

fn average(total: u64, count: u64) -> Option<f64> {
    (count > 0).then(|| total as f64 / count as f64)
}

fn collect_jsonl_files(dir: &Path, files: &mut Vec<PathBuf>) -> Result<(), String> {
    if !dir.exists() {
        return Ok(());
    }
    let entries = std::fs::read_dir(dir).map_err(|error| error.to_string())?;
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            collect_jsonl_files(&path, files)?;
        } else if path.extension().and_then(|value| value.to_str()) == Some("jsonl") {
            files.push(path);
        }
    }
    Ok(())
}

fn find_price<'a>(model: &str, prices: &'a [ModelPrice]) -> Option<&'a ModelPrice> {
    let model = if model == "gpt-5.6" {
        "gpt-5.6-sol"
    } else {
        model
    };
    prices
        .iter()
        .filter(|price| {
            model == price.model
                || model
                    .strip_prefix(&price.model)
                    .is_some_and(|suffix| suffix.starts_with('-'))
        })
        .max_by_key(|price| price.model.len())
}

fn estimate_cost(usage: TokenUsage, price: &ModelPrice) -> f64 {
    let cached = usage.cached_input_tokens.min(usage.input_tokens);
    let cache_write = usage
        .cache_write_input_tokens
        .min(usage.input_tokens.saturating_sub(cached));
    let regular = usage
        .input_tokens
        .saturating_sub(cached)
        .saturating_sub(cache_write);
    let use_long_context =
        usage.input_tokens > LONG_CONTEXT_THRESHOLD && price.long_input.is_some();

    let input_rate = if use_long_context {
        price.long_input.unwrap_or(price.input)
    } else {
        price.input
    };
    let cached_rate = if use_long_context {
        price
            .long_cached_input
            .or(price.cached_input)
            .unwrap_or(input_rate)
    } else {
        price.cached_input.unwrap_or(input_rate)
    };
    let cache_write_rate = if use_long_context {
        price
            .long_cache_write
            .or(price.cache_write)
            .unwrap_or(input_rate)
    } else {
        price.cache_write.unwrap_or(input_rate)
    };
    let output_rate = if use_long_context {
        price.long_output.unwrap_or(price.output)
    } else {
        price.output
    };

    (regular as f64 * input_rate
        + cached as f64 * cached_rate
        + cache_write as f64 * cache_write_rate
        + usage.output_tokens as f64 * output_rate)
        / 1_000_000.0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_standard_and_codex_prices() {
        let markdown = r#"
### Standard pricing data
| Model | Short context input | Short context cached input | Short context cache writes | Short context output | Long context input | Long context cached input | Long context cache writes | Long context output |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| gpt-5.6-sol | $5.00 | $0.50 | $6.25 | $30.00 | $10.00 | $1.00 | $12.50 | $45.00 |
Regional processing
Specialized models
Standard
| Category | Model | Input | Cached input | Output |
| --- | --- | --- | --- | --- |
| Codex | gpt-5.3-codex | $1.75 | $0.175 | $14.00 |
Batch
"#;
        let prices = parse_official_pricing(markdown);
        let sol = find_price("gpt-5.6-sol", &prices).unwrap();
        assert_eq!(sol.input, 5.0);
        assert_eq!(sol.cache_write, Some(6.25));
        assert_eq!(sol.long_output, Some(45.0));
        let codex = find_price("gpt-5.3-codex", &prices).unwrap();
        assert_eq!(codex.cached_input, Some(0.175));
    }

    #[test]
    fn estimates_cached_and_long_context_cost() {
        let price = ModelPrice {
            model: "gpt-test".to_string(),
            input: 2.0,
            cached_input: Some(0.2),
            cache_write: Some(2.5),
            output: 10.0,
            long_input: Some(4.0),
            long_cached_input: Some(0.4),
            long_cache_write: Some(5.0),
            long_output: Some(15.0),
        };
        let short = estimate_cost(
            TokenUsage {
                input_tokens: 100_000,
                cached_input_tokens: 80_000,
                cache_write_input_tokens: 10_000,
                output_tokens: 5_000,
                reasoning_output_tokens: 0,
            },
            &price,
        );
        assert!((short - 0.111).abs() < 0.000_001);

        let long = estimate_cost(
            TokenUsage {
                input_tokens: 300_000,
                output_tokens: 10_000,
                ..TokenUsage::default()
            },
            &price,
        );
        assert!((long - 1.35).abs() < 0.000_001);
    }

    #[test]
    fn model_snapshots_use_the_longest_matching_price_name() {
        let prices = vec![
            ModelPrice {
                model: "gpt-5.4".to_string(),
                input: 1.0,
                cached_input: None,
                cache_write: None,
                output: 1.0,
                long_input: None,
                long_cached_input: None,
                long_cache_write: None,
                long_output: None,
            },
            ModelPrice {
                model: "gpt-5.4-mini".to_string(),
                input: 0.5,
                cached_input: None,
                cache_write: None,
                output: 0.5,
                long_input: None,
                long_cached_input: None,
                long_cache_write: None,
                long_output: None,
            },
        ];
        assert_eq!(
            find_price("gpt-5.4-mini-2026-01-01", &prices)
                .unwrap()
                .model,
            "gpt-5.4-mini"
        );
    }

    #[test]
    fn aggregates_rollout_token_events_without_reading_message_content() {
        let sessions_dir =
            std::env::temp_dir().join(format!("switch-codex-usage-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&sessions_dir).unwrap();
        let timestamp = Utc::now().to_rfc3339();
        let rollout = format!(
            "{{\"timestamp\":\"{timestamp}\",\"type\":\"turn_context\",\"payload\":{{\"model\":\"gpt-test\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"user_message\",\"message\":\"must not be returned\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"token_count\",\"info\":{{\"total_token_usage\":{{\"input_tokens\":1000,\"cached_input_tokens\":200,\"output_tokens\":100,\"reasoning_output_tokens\":25}},\"last_token_usage\":{{\"input_tokens\":1000,\"cached_input_tokens\":200,\"output_tokens\":100,\"reasoning_output_tokens\":25}}}}}}}}\n"
        );
        std::fs::write(sessions_dir.join("rollout.jsonl"), rollout).unwrap();

        let catalog = PriceCatalog {
            source: PricingSource {
                url: OFFICIAL_PRICING_URL.to_string(),
                fetched_at: timestamp,
                kind: "test".to_string(),
                model_count: 1,
                warning: None,
            },
            prices: vec![ModelPrice {
                model: "gpt-test".to_string(),
                input: 5.0,
                cached_input: Some(0.5),
                cache_write: None,
                output: 30.0,
                long_input: None,
                long_cached_input: None,
                long_cache_write: None,
                long_output: None,
            }],
        };
        let stats = aggregate_sessions(&sessions_dir, 30, catalog).unwrap();
        assert_eq!(stats.summary.sessions, 1);
        assert_eq!(stats.summary.total_tokens, 1100);
        assert_eq!(stats.models[0].model, "gpt-test");
        assert!((stats.summary.estimated_cost_usd - 0.0071).abs() < 0.000_001);

        std::fs::remove_dir_all(sessions_dir).unwrap();
    }

    #[test]
    fn aggregates_model_per_turn_performance_metrics() {
        let sessions_dir =
            std::env::temp_dir().join(format!("switch-codex-turns-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&sessions_dir).unwrap();
        let timestamp = Utc::now().to_rfc3339();
        let rollout = format!(
            "{{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"task_started\",\"turn_id\":\"turn-1\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"turn_context\",\"payload\":{{\"turn_id\":\"turn-1\",\"model\":\"gpt-test\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"token_count\",\"info\":{{\"last_token_usage\":{{\"input_tokens\":600,\"output_tokens\":100}}}}}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"token_count\",\"info\":{{\"last_token_usage\":{{\"input_tokens\":300,\"output_tokens\":100}}}}}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"task_complete\",\"turn_id\":\"turn-1\",\"duration_ms\":2000,\"time_to_first_token_ms\":400}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"task_started\",\"turn_id\":\"turn-2\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"turn_context\",\"payload\":{{\"turn_id\":\"turn-2\",\"model\":\"gpt-test\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"token_count\",\"info\":{{\"last_token_usage\":{{\"input_tokens\":500,\"output_tokens\":50}}}}}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"task_complete\",\"turn_id\":\"turn-2\",\"duration_ms\":4000,\"time_to_first_token_ms\":600}}}}\n"
        );
        std::fs::write(sessions_dir.join("rollout.jsonl"), rollout).unwrap();

        let catalog = PriceCatalog {
            source: PricingSource {
                url: OFFICIAL_PRICING_URL.to_string(),
                fetched_at: timestamp,
                kind: "test".to_string(),
                model_count: 0,
                warning: None,
            },
            prices: Vec::new(),
        };
        let stats = aggregate_sessions(&sessions_dir, 30, catalog).unwrap();
        let model = &stats.models[0];

        assert_eq!(model.model, "gpt-test");
        assert_eq!(model.model_calls, 3);
        assert_eq!(model.turn_count, 2);
        assert_eq!(model.average_tokens_per_turn, Some(825.0));
        assert_eq!(model.average_duration_ms, Some(3000.0));
        assert_eq!(model.average_time_to_first_token_ms, Some(500.0));

        std::fs::remove_dir_all(sessions_dir).unwrap();
    }

    #[test]
    fn excludes_aborted_turns_and_keeps_missing_timing_metrics_nullable() {
        let sessions_dir =
            std::env::temp_dir().join(format!("switch-codex-turns-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&sessions_dir).unwrap();
        let timestamp = Utc::now().to_rfc3339();
        let rollout = format!(
            "{{\"timestamp\":\"{timestamp}\",\"type\":\"turn_context\",\"payload\":{{\"turn_id\":\"turn-aborted\",\"model\":\"gpt-test\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"token_count\",\"info\":{{\"last_token_usage\":{{\"input_tokens\":100,\"output_tokens\":50}}}}}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"turn_aborted\",\"turn_id\":\"turn-aborted\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"turn_context\",\"payload\":{{\"turn_id\":\"turn-complete\",\"model\":\"gpt-test\"}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"token_count\",\"info\":{{\"last_token_usage\":{{\"input_tokens\":150,\"output_tokens\":50}}}}}}}}\n\
             {{\"timestamp\":\"{timestamp}\",\"type\":\"event_msg\",\"payload\":{{\"type\":\"task_complete\",\"turn_id\":\"turn-complete\",\"duration_ms\":3000}}}}\n"
        );
        std::fs::write(sessions_dir.join("rollout.jsonl"), rollout).unwrap();

        let catalog = PriceCatalog {
            source: PricingSource {
                url: OFFICIAL_PRICING_URL.to_string(),
                fetched_at: timestamp,
                kind: "test".to_string(),
                model_count: 0,
                warning: None,
            },
            prices: Vec::new(),
        };
        let stats = aggregate_sessions(&sessions_dir, 30, catalog).unwrap();
        let model = &stats.models[0];

        assert_eq!(model.turn_count, 1);
        assert_eq!(model.average_tokens_per_turn, Some(200.0));
        assert_eq!(model.average_duration_ms, Some(3000.0));
        assert_eq!(model.average_time_to_first_token_ms, None);

        std::fs::remove_dir_all(sessions_dir).unwrap();
    }
}
