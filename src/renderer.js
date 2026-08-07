const { invoke } = window.__TAURI__.core;
const { listen } = window.__TAURI__.event;

const accountForm = document.querySelector('#accountForm');
const accountNameInput = document.querySelector('#accountName');
const chooseAuthFileButton = document.querySelector('#chooseAuthFile');
const selectedAuthFile = document.querySelector('#selectedAuthFile');
const formMessage = document.querySelector('#formMessage');
const activeStatus = document.querySelector('#activeStatus');
const accountCount = document.querySelector('#accountCount');
const accountList = document.querySelector('#accountList');
const contentGrid = document.querySelector('.content-grid');
const toggleAccountFormButton = document.querySelector('#toggleAccountForm');
const dataDir = document.querySelector('#dataDir');
const targetAuthPath = document.querySelector('#targetAuthPath');
const template = document.querySelector('#accountItemTemplate');
const viewTabs = document.querySelectorAll('.view-tab');
const views = {
  accounts: document.querySelector('#accountsView'),
  usage: document.querySelector('#usageView')
};
const refreshAccountQuotasButton = document.querySelector('#refreshAccountQuotas');
const usageRange = document.querySelector('#usageRange');
const refreshUsageButton = document.querySelector('#refreshUsage');
const usageMessage = document.querySelector('#usageMessage');
const quotaGrid = document.querySelector('#quotaGrid');
const usageGeneratedAt = document.querySelector('#usageGeneratedAt');
const estimatedCost = document.querySelector('#estimatedCost');
const totalTokens = document.querySelector('#totalTokens');
const cachedTokens = document.querySelector('#cachedTokens');
const outputTokens = document.querySelector('#outputTokens');
const sessionCount = document.querySelector('#sessionCount');
const cacheRatio = document.querySelector('#cacheRatio');
const modelCallCount = document.querySelector('#modelCallCount');
const dailyChart = document.querySelector('#dailyChart');
const pricingState = document.querySelector('#pricingState');
const pricingSource = document.querySelector('#pricingSource');
const pricingUpdatedAt = document.querySelector('#pricingUpdatedAt');
const modelUsageRows = document.querySelector('#modelUsageRows');

let currentState = { accounts: [], activeAccountId: null };
let selectedAuth = null;
let usageLoaded = false;
let usageLoading = false;
let currentQuotas = null;

function setAccountFormCollapsed(collapsed) {
  contentGrid.classList.toggle('is-form-collapsed', collapsed);
  accountForm.classList.toggle('is-collapsed', collapsed);
  toggleAccountFormButton.setAttribute('aria-expanded', String(!collapsed));
  toggleAccountFormButton.setAttribute('aria-label', collapsed ? '展开新增账号表单' : '收起新增账号表单');
  toggleAccountFormButton.textContent = collapsed ? '展开' : '收起';
}

function setMessage(message, type = 'neutral') {
  formMessage.textContent = message;
  formMessage.dataset.type = type;
}

function setUsageMessage(message, type = 'neutral') {
  usageMessage.textContent = message;
  usageMessage.dataset.type = type;
}

function render(state) {
  currentState = state;
  accountList.replaceChildren();
  dataDir.textContent = state.dataDir || '-';
  targetAuthPath.textContent = state.targetAuthPath || '-';
  accountCount.textContent = `${state.accounts.length} 个账号`;

  const activeAccount = state.accounts.find((account) => account.id === state.activeAccountId);
  activeStatus.textContent = activeAccount ? `当前生效：${activeAccount.name}` : '未配置账号';
  activeStatus.classList.toggle('is-active', Boolean(activeAccount));

  if (state.accounts.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'empty-state';
    empty.textContent = '还没有账号，先添加一份 Codex auth.json。';
    accountList.append(empty);
    return;
  }

  for (const account of state.accounts) {
    const item = template.content.firstElementChild.cloneNode(true);
    item.dataset.accountId = account.id;
    item.dataset.active = account.isActive ? 'true' : 'false';
    item.querySelector('.account-name').textContent = account.name;
    item.querySelector('.account-path').textContent = account.authPath;

    const switchButton = item.querySelector('.switch-button');
    switchButton.disabled = account.isActive;
    switchButton.addEventListener('click', () => switchAccount(account.id));

    item.querySelector('.danger-button').addEventListener('click', () => removeAccount(account.id, account.name));

    if (currentQuotas) {
      const quota = currentQuotas.accounts?.find((q) => q.accountId === account.id);
      renderAccountRowQuota(item, quota);
    }

    accountList.append(item);
  }

  if (currentQuotas) {
    renderQuotas(currentQuotas);
  }
}

async function loadAccounts() {
  try {
    const state = await invoke('list_accounts');
    render(state);
    loadAccountQuotas();
  } catch (error) {
    setMessage(typeof error === 'string' ? error : error.message, 'error');
  }
}

async function addAccount(event) {
  event.preventDefault();
  if (!selectedAuth) {
    setMessage('请选择 auth.json 文件', 'error');
    return;
  }

  try {
    setMessage('正在保存账号...', 'neutral');
    const nextState = await invoke('add_account', {
      name: accountNameInput.value,
      authJson: selectedAuth.authJson
    });
    accountForm.reset();
    selectedAuth = null;
    selectedAuthFile.textContent = '未选择文件';
    render(nextState);
    setMessage('账号已添加', 'success');
  } catch (error) {
    setMessage(typeof error === 'string' ? error : error.message, 'error');
  }
}

async function chooseAuthFile() {
  try {
    const file = await invoke('choose_auth_file');
    if (!file) {
      return;
    }

    selectedAuth = file;
    selectedAuthFile.textContent = file.filePath;
    setMessage(`已选择 ${file.fileName}`, 'success');
  } catch (error) {
    setMessage(typeof error === 'string' ? error : error.message, 'error');
  }
}

async function switchAccount(accountId) {
  try {
    const nextState = await invoke('switch_account', { accountId });
    render(nextState);
    const account = nextState.accounts.find((item) => item.id === accountId);
    setMessage(account ? `已切换到 ${account.name}` : '已切换账号', 'success');
  } catch (error) {
    setMessage(typeof error === 'string' ? error : error.message, 'error');
  }
}

async function removeAccount(accountId, accountName) {
  const confirmed = await window.__TAURI__.dialog.confirm(
    `项目内保存的 auth.json 也会被删除，此操作无法撤销。`,
    { title: `删除账号「${accountName}」`, kind: 'warning' }
  );
  if (!confirmed) {
    return;
  }

  try {
    const nextState = await invoke('remove_account', { accountId });
    render(nextState);
    setMessage('账号已删除', 'success');
  } catch (error) {
    setMessage(typeof error === 'string' ? error : error.message, 'error');
  }
}

function formatNumber(value) {
  return new Intl.NumberFormat('zh-CN').format(value || 0);
}

function formatCompactNumber(value) {
  return new Intl.NumberFormat('zh-CN', {
    notation: 'compact',
    maximumFractionDigits: 1
  }).format(value || 0);
}

function formatUsd(value) {
  const amount = Number(value) || 0;
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: amount > 0 && amount < 1 ? 4 : 2
  }).format(amount);
}

function formatDuration(value) {
  if (value == null || !Number.isFinite(Number(value))) {
    return '—';
  }
  const milliseconds = Number(value);
  if (milliseconds < 1000) {
    return `${Math.round(milliseconds)} ms`;
  }
  const totalSeconds = Math.round(milliseconds / 1000);
  if (totalSeconds < 60) {
    return `${(milliseconds / 1000).toFixed(milliseconds < 10_000 ? 1 : 0)} 秒`;
  }
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return seconds > 0 ? `${minutes} 分 ${seconds} 秒` : `${minutes} 分`;
}

function formatDateTime(value) {
  if (!value) {
    return '未知';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return '未知';
  }
  return new Intl.DateTimeFormat('zh-CN', {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  }).format(date);
}

function formatResetTime(epochSeconds) {
  if (!epochSeconds) {
    return '重置时间未知';
  }
  return `重置于 ${formatDateTime(new Date(epochSeconds * 1000).toISOString())}`;
}

function formatResetBadge(epochSeconds, resetAfterSeconds) {
  if (!epochSeconds && !resetAfterSeconds) {
    return '时间未知';
  }
  let timeStr = '';
  if (epochSeconds) {
    const d = new Date(epochSeconds * 1000);
    const hours = String(d.getHours()).padStart(2, '0');
    const minutes = String(d.getMinutes()).padStart(2, '0');
    const month = String(d.getMonth() + 1).padStart(2, '0');
    const date = String(d.getDate()).padStart(2, '0');
    timeStr = `${month}-${date} ${hours}:${minutes}`;
  }

  let relativeStr = '';
  let secondsLeft = resetAfterSeconds;
  if (secondsLeft === undefined && epochSeconds) {
    secondsLeft = epochSeconds - Math.floor(Date.now() / 1000);
  }
  if (secondsLeft && secondsLeft > 0) {
    const mins = Math.ceil(secondsLeft / 60);
    if (mins < 60) {
      relativeStr = `${mins}分后`;
    } else if (mins < 1440) {
      const h = Math.floor(mins / 60);
      const m = mins % 60;
      relativeStr = m > 0 ? `${h}h${m}m后` : `${h}h后`;
    } else {
      const days = Math.floor(mins / 1440);
      const h = Math.floor((mins % 1440) / 60);
      relativeStr = h > 0 ? `${days}天${h}h后` : `${days}天后`;
    }
  }

  if (timeStr && relativeStr) {
    return `${timeStr} (${relativeStr})`;
  }
  return timeStr ? `${timeStr} 刷新` : `${relativeStr}刷新`;
}

function renderAccountRowQuota(cardEl, quota) {
  if (!cardEl) return;
  const planBadge = cardEl.querySelector('.account-plan-badge');
  const statusEl = cardEl.querySelector('.account-quota-status');

  if (planBadge) {
    planBadge.textContent = quota?.planType || (quota?.ok ? 'Codex' : '');
    planBadge.style.display = quota?.planType || quota?.ok ? 'inline-block' : 'none';
  }

  if (!quota) {
    if (statusEl) statusEl.textContent = '等待加载额度...';
    return;
  }

  if (!quota.ok) {
    if (statusEl) {
      statusEl.textContent = quota.error || '额度查询失败';
      statusEl.classList.add('is-error');
    }
    return;
  }

  if (statusEl) {
    statusEl.classList.remove('is-error');
    const updateTime = quota.fetchedAt ? formatDateTime(quota.fetchedAt) : '刚刚';
    statusEl.textContent = `用自身 auth.json 独占查询 · 更新于 ${updateTime}`;
  }

  const windows = {
    five_hour:
      quota.fiveHour ||
      (quota.primary && (quota.primary.windowMinutes || 300) <= 1440 ? quota.primary : null),
    weekly:
      quota.weekly ||
      (quota.secondary && (quota.secondary.windowMinutes || 10080) <= 20160
        ? quota.secondary
        : quota.primary && (quota.primary.windowMinutes || 10080) > 1440 && (quota.primary.windowMinutes || 10080) <= 20160
        ? quota.primary
        : null),
    monthly:
      quota.monthly ||
      (quota.tertiary && (quota.tertiary.windowMinutes || 43200) > 20160 ? quota.tertiary : null)
  };

  for (const [key, win] of Object.entries(windows)) {
    const itemEl = cardEl.querySelector(`[data-window="${key}"]`);
    if (!itemEl) continue;

    const valEl = itemEl.querySelector('.account-quota-val');
    const fillEl = itemEl.querySelector('.account-quota-fill');
    const resetEl = itemEl.querySelector('.account-quota-reset');

    if (win) {
      const usedPercent = Math.max(0, Math.min(100, Number(win.usedPercent) || 0));
      valEl.textContent = `已用 ${usedPercent.toFixed(0)}%`;
      fillEl.style.width = `${usedPercent}%`;
      fillEl.className = 'account-quota-fill';
      if (usedPercent >= 90) {
        fillEl.classList.add('is-danger');
      } else if (usedPercent >= 70) {
        fillEl.classList.add('is-warning');
      }
      resetEl.textContent = formatResetBadge(win.resetsAt, win.resetAfterSeconds);
    } else {
      valEl.textContent = '未启用';
      fillEl.style.width = '0%';
      fillEl.className = 'account-quota-fill';
      resetEl.textContent = '-';
    }
  }
}

async function loadAccountQuotas() {
  if (refreshAccountQuotasButton) {
    refreshAccountQuotasButton.disabled = true;
    refreshAccountQuotasButton.textContent = '查询中...';
  }

  document.querySelectorAll('.account-quota-status').forEach((el) => {
    if (!el.classList.contains('is-error')) {
      el.textContent = '正在使用对应 auth.json 查询额度...';
    }
  });

  try {
    const quotas = await invoke('get_account_quotas');
    renderQuotas(quotas);
  } catch (error) {
    console.error('获取账号额度失败:', error);
    document.querySelectorAll('.account-quota-status').forEach((el) => {
      el.textContent = '额度查询失败';
      el.classList.add('is-error');
    });
  } finally {
    if (refreshAccountQuotasButton) {
      refreshAccountQuotasButton.disabled = false;
      refreshAccountQuotasButton.textContent = '刷新额度';
    }
  }
}

function windowLabel(minutes) {
  if (!minutes) {
    return '使用窗口';
  }
  if (minutes >= 1440 && minutes % 1440 === 0) {
    return `${minutes / 1440} 天`;
  }
  if (minutes >= 60 && minutes % 60 === 0) {
    return `${minutes / 60} 小时`;
  }
  return `${minutes} 分钟`;
}

function renderQuotaWindow(window) {
  const wrapper = document.createElement('div');
  wrapper.className = 'quota-window';

  const head = document.createElement('div');
  head.className = 'quota-window-head';
  const label = document.createElement('span');
  label.textContent = windowLabel(window.windowMinutes);
  const percent = document.createElement('strong');
  const usedPercent = Math.max(0, Math.min(100, Number(window.usedPercent) || 0));
  percent.textContent = `已用 ${usedPercent.toFixed(0)}%`;
  head.append(label, percent);

  const track = document.createElement('div');
  track.className = 'quota-track';
  const fill = document.createElement('div');
  fill.className = 'quota-fill';
  if (usedPercent >= 90) {
    fill.classList.add('is-danger');
  } else if (usedPercent >= 70) {
    fill.classList.add('is-warning');
  }
  fill.style.width = `${usedPercent}%`;
  track.append(fill);

  const reset = document.createElement('div');
  reset.className = 'quota-reset';
  reset.textContent = formatResetTime(window.resetsAt);
  wrapper.append(head, track, reset);
  return wrapper;
}

function renderQuotas(payload) {
  currentQuotas = payload;

  for (const item of accountList.querySelectorAll('.account-item')) {
    const accountId = item.dataset.accountId;
    if (accountId) {
      const quota = payload?.accounts?.find((q) => q.accountId === accountId);
      renderAccountRowQuota(item, quota);
    }
  }

  quotaGrid.replaceChildren();

  if (!payload?.accounts?.length) {
    const empty = document.createElement('div');
    empty.className = 'usage-empty';
    empty.textContent = currentState.accounts.length
      ? '没有可显示的账号配额'
      : '添加账号后可在这里查看独立配额。';
    quotaGrid.append(empty);
    return;
  }

  for (const quota of payload.accounts) {
    const card = document.createElement('article');
    card.className = 'quota-card';
    card.dataset.active = quota.accountId === currentState.activeAccountId ? 'true' : 'false';

    const head = document.createElement('div');
    head.className = 'quota-card-head';
    const name = document.createElement('strong');
    name.textContent = quota.accountName;
    const plan = document.createElement('span');
    plan.className = 'plan-pill';
    plan.textContent = quota.planType || (quota.ok ? 'Codex' : '不可用');
    head.append(name, plan);
    card.append(head);

    if (!quota.ok) {
      const error = document.createElement('p');
      error.className = 'quota-error';
      error.textContent = quota.error || '配额查询失败';
      card.append(error);
      quotaGrid.append(card);
      continue;
    }

    const windows = document.createElement('div');
    windows.className = 'quota-windows';
    if (quota.primary) {
      windows.append(renderQuotaWindow(quota.primary));
    }
    if (quota.secondary) {
      windows.append(renderQuotaWindow(quota.secondary));
    }
    if (!quota.primary && !quota.secondary) {
      const unavailable = document.createElement('p');
      unavailable.className = 'quota-error';
      unavailable.textContent = 'OpenAI 暂未返回可用的配额窗口';
      windows.append(unavailable);
    }
    card.append(windows);

    if (quota.credits) {
      const credits = document.createElement('p');
      credits.className = 'quota-credits';
      if (quota.credits.unlimited) {
        credits.textContent = '额外 credits：不限量';
      } else if (quota.credits.balance) {
        credits.textContent = `额外 credits：${quota.credits.balance}`;
      } else {
        credits.textContent = quota.credits.hasCredits ? '有可用的额外 credits' : '无额外 credits';
      }
      card.append(credits);
    }
    quotaGrid.append(card);
  }
}

function renderDailyChart(items) {
  dailyChart.replaceChildren();
  const visibleItems = (items || []).slice(-14);
  if (visibleItems.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'usage-empty compact';
    empty.textContent = '暂无 token 记录';
    dailyChart.append(empty);
    return;
  }

  const maxTokens = Math.max(...visibleItems.map((item) => item.totalTokens), 1);
  for (const item of visibleItems) {
    const wrapper = document.createElement('div');
    wrapper.className = 'daily-bar-item';
    wrapper.title = `${item.date} · ${formatNumber(item.totalTokens)} tokens · ${formatUsd(item.estimatedCostUsd)}`;

    const track = document.createElement('div');
    track.className = 'daily-bar-track';
    const bar = document.createElement('div');
    bar.className = 'daily-bar';
    bar.style.height = `${Math.max(3, (item.totalTokens / maxTokens) * 100)}%`;
    track.append(bar);

    const label = document.createElement('span');
    label.className = 'daily-bar-label';
    label.textContent = item.date.slice(5).replace('-', '/');
    wrapper.append(track, label);
    dailyChart.append(wrapper);
  }
}

function renderModelRows(models) {
  modelUsageRows.replaceChildren();
  if (!models?.length) {
    const row = document.createElement('tr');
    const cell = document.createElement('td');
    cell.colSpan = 10;
    cell.className = 'table-empty';
    cell.textContent = '暂无 token 记录';
    row.append(cell);
    modelUsageRows.append(row);
    return;
  }

  for (const model of models) {
    const row = document.createElement('tr');
    const values = [
      model.model,
      formatNumber(model.inputTokens),
      formatNumber(model.cachedInputTokens),
      formatNumber(model.outputTokens),
      formatNumber(model.totalTokens),
      formatNumber(model.turnCount),
      model.averageTokensPerTurn == null ? '—' : formatNumber(Math.round(model.averageTokensPerTurn)),
      formatDuration(model.averageTimeToFirstTokenMs),
      formatDuration(model.averageDurationMs),
      model.estimatedCostUsd == null ? '暂无官方价格' : formatUsd(model.estimatedCostUsd)
    ];
    values.forEach((value, index) => {
      const cell = document.createElement('td');
      cell.textContent = value;
      if (index === 0) {
        cell.className = 'model-name';
      }
      if (index === 9 && model.estimatedCostUsd == null) {
        cell.className = 'unpriced';
      }
      row.append(cell);
    });
    if (model.price) {
      row.title = `每百万 token：输入 ${formatUsd(model.price.input)} · 缓存 ${formatUsd(model.price.cachedInput ?? model.price.input)} · 输出 ${formatUsd(model.price.output)}`;
    }
    modelUsageRows.append(row);
  }
}

function renderUsageStats(stats) {
  const summary = stats.summary;
  estimatedCost.textContent = formatUsd(summary.estimatedCostUsd);
  totalTokens.textContent = formatCompactNumber(summary.totalTokens);
  cachedTokens.textContent = formatCompactNumber(summary.cachedInputTokens);
  outputTokens.textContent = formatCompactNumber(summary.outputTokens);
  sessionCount.textContent = `${formatNumber(summary.sessions)} 个会话`;
  const ratio = summary.inputTokens > 0 ? (summary.cachedInputTokens / summary.inputTokens) * 100 : 0;
  cacheRatio.textContent = `缓存占比 ${ratio.toFixed(0)}%`;
  modelCallCount.textContent = `${formatNumber(summary.modelCalls)} 次模型调用`;
  usageGeneratedAt.textContent = `统计于 ${formatDateTime(stats.generatedAt)}`;

  const source = stats.pricingSource;
  const sourceLabels = {
    live: '官方实时价格',
    cache: '官方价格缓存',
    bundled: '内置官方快照'
  };
  pricingState.textContent = sourceLabels[source.kind] || 'OpenAI 官方价格';
  pricingSource.href = (source.url || 'https://developers.openai.com/api/docs/pricing').replace(/\.md$/, '');
  pricingUpdatedAt.textContent = `价格时间 ${formatDateTime(source.fetchedAt)} · ${source.modelCount} 个模型`;

  renderDailyChart(stats.daily);
  renderModelRows(stats.models);
}

async function refreshUsage(options = {}) {
  if (usageLoading) {
    return;
  }
  const { refreshPrices = true, refreshQuotas = true } = options;
  usageLoading = true;
  refreshUsageButton.disabled = true;
  setUsageMessage(refreshPrices ? '正在读取 OpenAI 官方用量与价格…' : '正在重新统计本地 token…');

  try {
    const statsPromise = invoke('get_usage_stats', {
      days: Number(usageRange.value),
      refreshPrices
    });
    const quotasPromise = refreshQuotas
      ? invoke('get_account_quotas')
      : Promise.resolve(currentQuotas);
    const [stats, quotas] = await Promise.all([statsPromise, quotasPromise]);
    renderUsageStats(stats);
    if (quotas) {
      renderQuotas(quotas);
    }
    usageLoaded = true;

    const warning = stats.pricingSource.warning;
    const unpriced = stats.summary.unpricedModelCount;
    if (warning) {
      setUsageMessage(warning, 'error');
    } else if (unpriced > 0) {
      setUsageMessage(`统计完成；${unpriced} 个内部或未知模型没有公开价格，未计入估算。`, 'neutral');
    } else {
      setUsageMessage('用量与价格已更新', 'success');
    }
  } catch (error) {
    setUsageMessage(typeof error === 'string' ? error : error.message, 'error');
  } finally {
    usageLoading = false;
    refreshUsageButton.disabled = false;
  }
}

function switchView(viewName) {
  for (const [name, view] of Object.entries(views)) {
    view.classList.toggle('is-active', name === viewName);
  }
  for (const tab of viewTabs) {
    const active = tab.dataset.view === viewName;
    tab.classList.toggle('is-active', active);
    if (active) {
      tab.setAttribute('aria-current', 'page');
    } else {
      tab.removeAttribute('aria-current');
    }
  }
  if (viewName === 'usage' && !usageLoaded) {
    refreshUsage();
  }
}

chooseAuthFileButton.addEventListener('click', chooseAuthFile);
accountForm.addEventListener('submit', addAccount);
toggleAccountFormButton.addEventListener('click', () => {
  setAccountFormCollapsed(!accountForm.classList.contains('is-collapsed'));
});
refreshUsageButton.addEventListener('click', () => refreshUsage());
if (refreshAccountQuotasButton) {
  refreshAccountQuotasButton.addEventListener('click', loadAccountQuotas);
}
usageRange.addEventListener('change', () => refreshUsage({ refreshPrices: false, refreshQuotas: false }));
for (const tab of viewTabs) {
  tab.addEventListener('click', () => switchView(tab.dataset.view));
}

async function init() {
  await loadAccounts();
  listen('accounts-changed', (event) => {
    render(event.payload);
    if (usageLoaded && views.usage.classList.contains('is-active')) {
      refreshUsage({ refreshPrices: false, refreshQuotas: true });
    }
  }).catch((e) => {
    console.error('Failed to listen for accounts-changed:', e);
  });
  listen('switch-error', (event) => {
    setMessage(event.payload || '切换账号失败', 'error');
  }).catch((e) => {
    console.error('Failed to listen for switch-error:', e);
  });
}

init();
