import type { RateLimitWindow } from './types';

export function formatNumber(value: number | null | undefined): string {
  return new Intl.NumberFormat('zh-CN').format(value || 0);
}

export function formatCompactNumber(value: number | null | undefined): string {
  return new Intl.NumberFormat('zh-CN', {
    notation: 'compact',
    maximumFractionDigits: 1
  }).format(value || 0);
}

export function formatUsd(value: number | null | undefined): string {
  const amount = Number(value) || 0;
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: amount > 0 && amount < 1 ? 4 : 2
  }).format(amount);
}

export function formatDateTime(value: string | null | undefined): string {
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

export function formatResetTime(epochSeconds: number | null | undefined): string {
  if (!epochSeconds) {
    return '重置时间未知';
  }
  return `重置于 ${formatDateTime(new Date(epochSeconds * 1000).toISOString())}`;
}

export function formatResetBadge(
  epochSeconds: number | null | undefined,
  resetAfterSeconds: number | null | undefined
): string {
  if (!epochSeconds && !resetAfterSeconds) {
    return '时间未知';
  }

  let time = '';
  if (epochSeconds) {
    const date = new Date(epochSeconds * 1000);
    const hours = String(date.getHours()).padStart(2, '0');
    const minutes = String(date.getMinutes()).padStart(2, '0');
    const month = String(date.getMonth() + 1).padStart(2, '0');
    const day = String(date.getDate()).padStart(2, '0');
    time = `${month}-${day} ${hours}:${minutes}`;
  }

  let relative = '';
  let secondsLeft = resetAfterSeconds;
  if (secondsLeft == null && epochSeconds) {
    secondsLeft = epochSeconds - Math.floor(Date.now() / 1000);
  }
  if (secondsLeft && secondsLeft > 0) {
    const minutes = Math.ceil(secondsLeft / 60);
    if (minutes < 60) {
      relative = `${minutes}分后`;
    } else if (minutes < 1440) {
      const hours = Math.floor(minutes / 60);
      const remainingMinutes = minutes % 60;
      relative = remainingMinutes > 0 ? `${hours}h${remainingMinutes}m后` : `${hours}h后`;
    } else {
      const days = Math.floor(minutes / 1440);
      const hours = Math.floor((minutes % 1440) / 60);
      relative = hours > 0 ? `${days}天${hours}h后` : `${days}天后`;
    }
  }

  if (time && relative) {
    return `${time} (${relative})`;
  }
  return time ? `${time} 刷新` : `${relative}刷新`;
}

export function formatAccountId(accountId: string | null | undefined): string {
  if (!accountId) {
    return '缺失';
  }
  if (accountId.length <= 14) {
    return accountId;
  }
  return `${accountId.slice(0, 7)}…${accountId.slice(-5)}`;
}

export function windowLabel(minutes: number | null | undefined): string {
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

export function fillState(window: RateLimitWindow | null | undefined): 'normal' | 'warning' | 'danger' {
  const percent = Math.max(0, Math.min(100, Number(window?.usedPercent) || 0));
  if (percent >= 90) return 'danger';
  if (percent >= 70) return 'warning';
  return 'normal';
}
