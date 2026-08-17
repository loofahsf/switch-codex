import type { AccountQuota, AccountsState, RateLimitWindow } from '../types';
import {
  fillState,
  formatDateTime,
  formatResetBadge,
  formatResetTime,
  windowLabel
} from '../format';

interface QuotaBarProps {
  window: RateLimitWindow | null | undefined;
  label: string;
  compact?: boolean;
}

function QuotaBar({ window, label, compact = false }: QuotaBarProps) {
  if (!window && compact) {
    return null;
  }

  const usedPercent = Math.max(0, Math.min(100, Number(window?.usedPercent) || 0));
  const level = fillState(window);
  const fillClass = `${compact ? 'quota-fill' : 'account-quota-fill'}${
    level === 'normal' ? '' : ` is-${level}`
  }`;

  if (compact && window) {
    return (
      <div className="quota-window">
        <div className="quota-window-head">
          <span>{windowLabel(window.windowMinutes)}</span>
          <strong>已用 {usedPercent.toFixed(0)}%</strong>
        </div>
        <div className="quota-track">
          <div className={fillClass} style={{ width: `${usedPercent}%` }} />
        </div>
        <div className="quota-reset">{formatResetTime(window.resetsAt)}</div>
      </div>
    );
  }

  return (
    <div className="account-quota-item">
      <div className="account-quota-label">
        <span>{label}</span>
        <strong>{window ? `已用 ${usedPercent.toFixed(0)}%` : '未启用'}</strong>
      </div>
      <div className="account-quota-track">
        <div className={fillClass} style={{ width: window ? `${usedPercent}%` : '0%' }} />
      </div>
      <span className="account-quota-reset">
        {window ? formatResetBadge(window.resetsAt, window.resetAfterSeconds) : '-'}
      </span>
    </div>
  );
}

function accountWindows(quota: AccountQuota | undefined) {
  if (!quota) {
    return { fiveHour: null, weekly: null, monthly: null };
  }
  return {
    fiveHour:
      quota.fiveHour ||
      (quota.primary && (quota.primary.windowMinutes || 300) <= 1440 ? quota.primary : null),
    weekly:
      quota.weekly ||
      (quota.secondary && (quota.secondary.windowMinutes || 10080) <= 20160
        ? quota.secondary
        : quota.primary &&
            (quota.primary.windowMinutes || 10080) > 1440 &&
            (quota.primary.windowMinutes || 10080) <= 20160
          ? quota.primary
          : null),
    monthly:
      quota.monthly ||
      (quota.tertiary && (quota.tertiary.windowMinutes || 43200) > 20160
        ? quota.tertiary
        : null)
  };
}

interface AccountRowQuotaProps {
  quota: AccountQuota | undefined;
  loading: boolean;
}

export function AccountRowQuota({ quota, loading }: AccountRowQuotaProps) {
  const windows = accountWindows(quota);
  let status = '点击“刷新额度”后查询';
  let hasError = false;

  if (loading) {
    status = '正在使用对应 auth.json 查询额度...';
  } else if (quota && !quota.ok) {
    status = quota.error || '额度查询失败';
    hasError = true;
  } else if (quota?.ok) {
    status = `用自身 auth.json 独占查询 · 更新于 ${
      quota.fetchedAt ? formatDateTime(quota.fetchedAt) : '刚刚'
    }`;
  }

  return (
    <div className="account-quota-box">
      <div className="account-quota-grid">
        <QuotaBar label="5h 限额" window={windows.fiveHour} />
        <QuotaBar label="周限额" window={windows.weekly} />
        <QuotaBar label="月限额" window={windows.monthly} />
      </div>
      <div className="account-quota-meta">
        <span className={`account-quota-status${hasError ? ' is-error' : ''}`}>{status}</span>
      </div>
    </div>
  );
}

interface UsageQuotaGridProps {
  state: AccountsState;
  quotas: AccountQuota[] | null;
}

export function UsageQuotaGrid({ state, quotas }: UsageQuotaGridProps) {
  if (!quotas?.length) {
    return (
      <div className="quota-grid">
        <div className="usage-empty">
          {state.accounts.length ? '没有可显示的账号配额' : '添加账号后可在这里查看独立配额。'}
        </div>
      </div>
    );
  }

  return (
    <div className="quota-grid">
      {quotas.map((quota) => (
        <article
          className="quota-card"
          data-active={quota.accountId === state.activeAccountId ? 'true' : 'false'}
          key={quota.accountId}
        >
          <div className="quota-card-head">
            <strong>{quota.accountName}</strong>
            <span className="plan-pill">{quota.planType || (quota.ok ? 'Codex' : '不可用')}</span>
          </div>
          {!quota.ok ? (
            <p className="quota-error">{quota.error || '配额查询失败'}</p>
          ) : (
            <>
              <div className="quota-windows">
                <QuotaBar compact label="" window={quota.primary} />
                <QuotaBar compact label="" window={quota.secondary} />
                {!quota.primary && !quota.secondary ? (
                  <p className="quota-error">OpenAI 暂未返回可用的配额窗口</p>
                ) : null}
              </div>
              {quota.credits ? (
                <p className="quota-credits">
                  {quota.credits.unlimited
                    ? '额外 credits：不限量'
                    : quota.credits.balance
                      ? `额外 credits：${quota.credits.balance}`
                      : quota.credits.hasCredits
                        ? '有可用的额外 credits'
                        : '无额外 credits'}
                </p>
              ) : null}
            </>
          )}
        </article>
      ))}
    </div>
  );
}
