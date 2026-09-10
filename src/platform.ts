import { Events } from '@wailsio/runtime';
import * as AppService from './bindings/switch-codex/appservice';
import type { Settings } from './types';

interface ConfirmOptions {
  title?: string;
  kind?: 'info' | 'warning' | 'error';
}

type Args = Record<string, unknown>;

// Keep the page-level contract independent of the desktop framework. Each
// command maps to a generated, typed method; no generic Go RPC dispatcher.
const commands: Record<string, (args: Args) => PromiseLike<unknown>> = {
  list_accounts: () => AppService.ListAccounts(),
  add_account: (args) => AppService.AddAccount(args.name as string, args.authJson as string),
  remove_account: (args) => AppService.RemoveAccount(args.accountId as string),
  switch_account: (args) => AppService.SwitchAccount(args.accountId as string),
  get_auth_sync_status: () => AppService.GetAuthSyncStatus(),
  check_auth_sync_now: () => AppService.CheckAuthSyncNow(),
  add_pending_current_account: (args) => AppService.AddPendingCurrentAccount(args.pendingId as string, args.name as string),
  choose_auth_file: () => AppService.ChooseAuthFile(),
  get_usage_stats: (args) => AppService.GetUsageStats(args.days as number, args.refreshPrices as boolean | undefined ?? null),
  get_account_quotas: () => AppService.GetAccountQuotas(),
  get_account_quota: (args) => AppService.GetAccountQuota(args.accountId as string),
  get_settings: () => AppService.GetSettings(),
  detect_codex_cli_path: () => AppService.DetectCodexCLIPath(),
  save_settings: (args) => AppService.SaveSettings(args.settings as Settings),
  get_scheduled_run_status: () => AppService.GetScheduledRunStatus(),
  open_url: (args) => AppService.OpenURL(args.url as string)
};

export async function invoke<T>(command: string, args: Args = {}): Promise<T> {
  if (!Object.hasOwn(commands, command)) throw new Error(`未知桌面命令：${command}`);
  return await commands[command](args) as T;
}

export async function listen<T>(event: string, handler: (payload: T) => void): Promise<() => void> {
  const off = Events.On(event, (message) => handler(message.data as T));
  let disposed = false;
  return () => { if (!disposed) { disposed = true; off(); } };
}

export async function confirm(message: string, options: ConfirmOptions = {}): Promise<boolean> {
  return AppService.Confirm(message, { title: options.title ?? '确认', kind: options.kind ?? 'info' });
}

export function getErrorMessage(error: unknown, fallback: string): string {
  if (typeof error === 'string' && error.trim()) return error;
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === 'object' && error !== null && 'message' in error
    && typeof error.message === 'string' && error.message.trim()) return error.message;
  return fallback;
}
