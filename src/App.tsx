import { useCallback, useEffect, useRef, useState } from 'react';
import AntdApp from 'antd/es/app';
import Sidebar from './components/Sidebar';
import AccountsView from './views/AccountsView';
import UsageView from './views/UsageView';
import SettingsView from './views/SettingsView';
import { confirm, getErrorMessage, invoke, listen } from './platform';
import type {
  AccountItem,
  AccountQuotas,
  AccountsState,
  AuthSyncStatus,
  ChosenFile,
  InlineMessage,
  UsageStats,
  ViewName
} from './types';

const emptyState: AccountsState = {
  dataDir: '',
  targetAuthPath: '',
  activeAccountId: null,
  accounts: []
};

const emptyMessage: InlineMessage = { text: '', type: 'neutral' };
const emptyAuthSyncStatus: AuthSyncStatus = {
  enabled: true,
  state: 'checking',
  accountId: null,
  accountName: null,
  checkedAt: null,
  syncedAt: null,
  pendingId: null,
  message: '正在检查当前认证文件'
};
const sidebarStorageKey = 'switch-codex:sidebar-collapsed';

interface RefreshUsageOptions {
  refreshPrices?: boolean;
  refreshQuotas?: boolean;
  days?: number;
}

function waitForPaint(): Promise<void> {
  return new Promise((resolve) => {
    window.requestAnimationFrame(() => window.requestAnimationFrame(() => resolve()));
  });
}

function initialSidebarState(): boolean {
  try {
    return window.localStorage.getItem(sidebarStorageKey) === 'true';
  } catch (error) {
    console.warn('无法读取侧边栏状态:', error);
    return false;
  }
}

export default function App() {
  const { message: toast } = AntdApp.useApp();
  const [state, setState] = useState<AccountsState>(emptyState);
  const [view, setView] = useState<ViewName>('accounts');
  const [sidebarCollapsed, setSidebarCollapsed] = useState(initialSidebarState);
  const [accountMessage, setAccountMessage] = useState<InlineMessage>(emptyMessage);
  const [authSyncStatus, setAuthSyncStatus] = useState<AuthSyncStatus>(emptyAuthSyncStatus);
  const [authSyncLoading, setAuthSyncLoading] = useState(false);
  const [usageMessage, setUsageMessage] = useState<InlineMessage>(emptyMessage);
  const [quotas, setQuotas] = useState<AccountQuotas | null>(null);
  const [quotaLoading, setQuotaLoading] = useState(false);
  const [refreshingQuotaAccountId, setRefreshingQuotaAccountId] = useState<string | null>(null);
  const [usageStats, setUsageStats] = useState<UsageStats | null>(null);
  const [usageRange, setUsageRange] = useState(14);
  const [usageLoading, setUsageLoading] = useState(false);
  const [usageLoadingText, setUsageLoadingText] = useState('正在查询用量数据…');

  const viewRef = useRef<ViewName>('accounts');
  const quotasRef = useRef<AccountQuotas | null>(null);
  const usageRangeRef = useRef(14);
  const usageLoadedRef = useRef(false);
  const usageLoadingRef = useRef(false);
  const quotaLoadingRef = useRef(false);
  const refreshingQuotaAccountIdRef = useRef<string | null>(null);
  const followedAtRef = useRef<string | null>(null);

  const updateQuotas = useCallback((nextQuotas: AccountQuotas | null) => {
    quotasRef.current = nextQuotas;
    setQuotas(nextQuotas);
  }, []);

  const refreshUsage = useCallback(
    async ({
      refreshPrices = true,
      refreshQuotas = true,
      days = usageRangeRef.current
    }: RefreshUsageOptions = {}) => {
      if (usageLoadingRef.current) {
        return;
      }

      usageLoadingRef.current = true;
      setUsageLoading(true);
      const loadingText = refreshPrices
        ? '正在读取 OpenAI 官方用量与价格…'
        : '正在重新统计本地 Token…';
      setUsageLoadingText(loadingText);
      setUsageMessage({ text: loadingText, type: 'neutral' });

      try {
        await waitForPaint();
        const statsPromise = invoke<UsageStats>('get_usage_stats', {
          days,
          refreshPrices
        });
        const quotasPromise: Promise<AccountQuotas | null> = refreshQuotas
          ? invoke<AccountQuotas>('get_account_quotas')
          : Promise.resolve(quotasRef.current);
        const [nextStats, nextQuotas] = await Promise.all([statsPromise, quotasPromise]);

        setUsageStats(nextStats);
        if (nextQuotas) {
          updateQuotas(nextQuotas);
        }
        usageLoadedRef.current = true;

        const warning = nextStats.pricingSource.warning;
        const unpriced = nextStats.summary.unpricedModelCount;
        if (warning) {
          setUsageMessage({ text: warning, type: 'error' });
        } else if (unpriced > 0) {
          setUsageMessage({
            text: `统计完成；${unpriced} 个内部或未知模型没有公开价格，未计入估算。`,
            type: 'neutral'
          });
        } else {
          setUsageMessage({ text: '用量与价格已更新', type: 'success' });
        }
      } catch (error) {
        setUsageMessage({ text: getErrorMessage(error, '查询用量失败'), type: 'error' });
      } finally {
        usageLoadingRef.current = false;
        setUsageLoading(false);
      }
    },
    [updateQuotas]
  );

  useEffect(() => {
    let disposed = false;
    const unlisteners: Array<() => void> = [];

    invoke<AccountsState>('list_accounts')
      .then((nextState) => {
        if (!disposed) setState(nextState);
      })
      .catch((error) => {
        if (!disposed) {
          setAccountMessage({ text: getErrorMessage(error, '读取账号失败'), type: 'error' });
        }
      });

    invoke<AuthSyncStatus>('get_auth_sync_status')
      .then((nextStatus) => {
        if (!disposed) setAuthSyncStatus(nextStatus);
      })
      .catch((error) => {
        if (!disposed) {
          setAccountMessage({ text: getErrorMessage(error, '读取自动同步状态失败'), type: 'error' });
        }
      });

    listen<AccountsState>('accounts-changed', (nextState) => {
      if (disposed) return;
      setState(nextState);
      if (usageLoadedRef.current && viewRef.current === 'usage') {
        void refreshUsage({ refreshPrices: false, refreshQuotas: true });
      }
    }).then((unlisten) => {
      if (disposed) unlisten();
      else unlisteners.push(unlisten);
    }).catch((error) => {
      console.error('Failed to listen for accounts-changed:', error);
    });

    listen<string>('switch-error', (payload) => {
      if (disposed) return;
      const text = payload || '切换账号失败';
      setAccountMessage({ text, type: 'error' });
      void toast.error(text);
    }).then((unlisten) => {
      if (disposed) unlisten();
      else unlisteners.push(unlisten);
    }).catch((error) => {
      console.error('Failed to listen for switch-error:', error);
    });

    listen<AuthSyncStatus>('auth-sync-changed', (nextStatus) => {
      if (disposed) return;
      setAuthSyncStatus(nextStatus);
      if (nextStatus.state === 'followed' && nextStatus.checkedAt !== followedAtRef.current) {
        followedAtRef.current = nextStatus.checkedAt;
        void toast.success(nextStatus.message || '已跟随当前认证文件切换账号');
      }
    }).then((unlisten) => {
      if (disposed) unlisten();
      else unlisteners.push(unlisten);
    }).catch((error) => {
      console.error('Failed to listen for auth-sync-changed:', error);
    });

    listen<AccountQuotas>('scheduled-quotas-changed', (nextQuotas) => {
      if (!disposed) updateQuotas(nextQuotas);
    }).then((unlisten) => {
      if (disposed) unlisten();
      else unlisteners.push(unlisten);
    }).catch((error) => {
      console.error('Failed to listen for scheduled-quotas-changed:', error);
    });

    return () => {
      disposed = true;
      unlisteners.forEach((unlisten) => unlisten());
    };
  }, [refreshUsage, toast, updateQuotas]);

  function changeSidebarCollapsed(collapsed: boolean) {
    setSidebarCollapsed(collapsed);
    try {
      window.localStorage.setItem(sidebarStorageKey, String(collapsed));
    } catch (error) {
      console.warn('无法保存侧边栏状态:', error);
    }
  }

  function changeView(nextView: ViewName) {
    viewRef.current = nextView;
    setView(nextView);
    if (nextView === 'usage' && !usageLoadedRef.current) {
      void refreshUsage();
    }
  }

  async function chooseAuthFile(): Promise<ChosenFile | null> {
    try {
      const file = await invoke<ChosenFile | null>('choose_auth_file');
      if (file) {
        setAccountMessage({ text: `已选择 ${file.fileName}`, type: 'success' });
      }
      return file;
    } catch (error) {
      setAccountMessage({ text: getErrorMessage(error, '选择认证文件失败'), type: 'error' });
      return null;
    }
  }

  async function addAccount(name: string, authJson: string): Promise<boolean> {
    if (!authJson) {
      setAccountMessage({ text: '请选择 auth.json 文件', type: 'error' });
      return false;
    }
    try {
      setAccountMessage({ text: '正在保存账号...', type: 'neutral' });
      const nextState = await invoke<AccountsState>('add_account', { name, authJson });
      setState(nextState);
      setAccountMessage({ text: '账号已添加', type: 'success' });
      return true;
    } catch (error) {
      setAccountMessage({ text: getErrorMessage(error, '添加账号失败'), type: 'error' });
      return false;
    }
  }

  async function switchAccount(account: AccountItem) {
    try {
      const nextState = await invoke<AccountsState>('switch_account', { accountId: account.id });
      setState(nextState);
      const text = `已切换到「${account.name}」，~/.codex/auth.json 已更新`;
      setAccountMessage({ text, type: 'success' });
      void toast.success(text);
    } catch (error) {
      const text = getErrorMessage(error, '切换账号失败');
      setAccountMessage({ text, type: 'error' });
      void toast.error(text);
    }
  }

  async function checkAuthSync() {
    if (authSyncLoading) return;
    setAuthSyncLoading(true);
    try {
      setAuthSyncStatus(await invoke<AuthSyncStatus>('check_auth_sync_now'));
    } catch (error) {
      const text = getErrorMessage(error, '检查认证文件失败');
      setAccountMessage({ text, type: 'error' });
      void toast.error(text);
    } finally {
      setAuthSyncLoading(false);
    }
  }

  async function addPendingCurrentAccount(name: string): Promise<boolean> {
    if (!authSyncStatus.pendingId) return false;
    try {
      const nextState = await invoke<AccountsState>('add_pending_current_account', {
        pendingId: authSyncStatus.pendingId,
        name
      });
      setState(nextState);
      setAccountMessage({ text: '当前登录账号已保存', type: 'success' });
      return true;
    } catch (error) {
      const text = getErrorMessage(error, '保存当前登录账号失败');
      setAccountMessage({ text, type: 'error' });
      void toast.error(text);
      return false;
    }
  }

  async function removeAccount(account: AccountItem) {
    const confirmed = await confirm('项目内保存的 auth.json 也会被删除，此操作无法撤销。', {
      title: `删除账号「${account.name}」`,
      kind: 'warning'
    });
    if (!confirmed) return;

    try {
      const nextState = await invoke<AccountsState>('remove_account', { accountId: account.id });
      setState(nextState);
      setAccountMessage({ text: '账号已删除', type: 'success' });
    } catch (error) {
      setAccountMessage({ text: getErrorMessage(error, '删除账号失败'), type: 'error' });
    }
  }

  async function refreshAccountQuotas() {
    if (quotaLoadingRef.current || refreshingQuotaAccountIdRef.current) return;
    quotaLoadingRef.current = true;
    setQuotaLoading(true);
    try {
      updateQuotas(await invoke<AccountQuotas>('get_account_quotas'));
    } catch (error) {
      const text = getErrorMessage(error, '额度查询失败');
      setAccountMessage({ text, type: 'error' });
      void toast.error(text);
    } finally {
      quotaLoadingRef.current = false;
      setQuotaLoading(false);
    }
  }

  async function refreshAccountQuota(account: AccountItem) {
    if (quotaLoadingRef.current || refreshingQuotaAccountIdRef.current) return;
    refreshingQuotaAccountIdRef.current = account.id;
    setRefreshingQuotaAccountId(account.id);
    try {
      const result = await invoke<AccountQuotas>('get_account_quota', { accountId: account.id });
      const quota = result.accounts[0];
      if (!quota) return;

      const previous = quotasRef.current;
      const accounts = previous?.accounts ?? [];
      const existingIndex = accounts.findIndex((item) => item.accountId === quota.accountId);
      const nextAccounts = [...accounts];
      if (existingIndex >= 0) {
        nextAccounts[existingIndex] = quota;
      } else {
        nextAccounts.push(quota);
      }
      updateQuotas({ sourceUrl: result.sourceUrl, accounts: nextAccounts });
    } catch (error) {
      const text = getErrorMessage(error, '额度查询失败');
      setAccountMessage({ text, type: 'error' });
      void toast.error(text);
    } finally {
      refreshingQuotaAccountIdRef.current = null;
      setRefreshingQuotaAccountId(null);
    }
  }

  function changeUsageRange(range: number) {
    usageRangeRef.current = range;
    setUsageRange(range);
    void refreshUsage({ days: range, refreshPrices: false, refreshQuotas: false });
  }

  function openUrl(url: string) {
    invoke<void>('open_url', { url }).catch((error) => {
      void toast.error(getErrorMessage(error, '无法打开链接'));
    });
  }

  return (
    <main className={`app-shell${sidebarCollapsed ? ' is-sidebar-collapsed' : ''}`}>
      <Sidebar
        collapsed={sidebarCollapsed}
        view={view}
        state={state}
        onCollapsedChange={changeSidebarCollapsed}
        onViewChange={changeView}
      />
      <section className="workspace">
        <SettingsView active={view === 'settings'} />
        <AccountsView
          active={view === 'accounts'}
          state={state}
          quotas={quotas}
          quotaLoading={quotaLoading}
          refreshingQuotaAccountId={refreshingQuotaAccountId}
          authSyncStatus={authSyncStatus}
          authSyncLoading={authSyncLoading}
          inlineMessage={accountMessage}
          onChooseFile={chooseAuthFile}
          onAddAccount={addAccount}
          onSwitchAccount={switchAccount}
          onCheckAuthSync={checkAuthSync}
          onAddPendingCurrentAccount={addPendingCurrentAccount}
          onRemoveAccount={removeAccount}
          onRefreshQuotas={refreshAccountQuotas}
          onRefreshAccountQuota={refreshAccountQuota}
        />
        <UsageView
          active={view === 'usage'}
          state={state}
          quotas={quotas}
          stats={usageStats}
          range={usageRange}
          loading={usageLoading}
          loadingText={usageLoadingText}
          inlineMessage={usageMessage}
          onRangeChange={changeUsageRange}
          onRefresh={() => void refreshUsage()}
          onOpenUrl={openUrl}
        />
      </section>
    </main>
  );
}
