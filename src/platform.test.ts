import { beforeEach, describe, expect, it, vi } from 'vitest';

const names = ['ListAccounts', 'AddAccount', 'RemoveAccount', 'SwitchAccount', 'UpdateAccountAuth',
  'ChooseAuthFile', 'GetUsageStats', 'GetAccountQuotas', 'GetAccountQuota', 'GetSettings',
  'DetectCodexCLIPath', 'SaveSettings', 'GetScheduledRunStatus', 'OpenURL', 'Confirm'];
const mocks = vi.hoisted(() => ({ calls: {} as Record<string, ReturnType<typeof vi.fn>>, on: vi.fn() }));
vi.mock('@wailsio/runtime', () => ({ Events: { On: mocks.on } }));
vi.mock('./bindings/switch-codex/appservice', () => Object.fromEntries([
  'ListAccounts', 'AddAccount', 'RemoveAccount', 'SwitchAccount', 'UpdateAccountAuth',
  'ChooseAuthFile', 'GetUsageStats', 'GetAccountQuotas', 'GetAccountQuota', 'GetSettings',
  'DetectCodexCLIPath', 'SaveSettings', 'GetScheduledRunStatus', 'OpenURL', 'Confirm'
].map((name) => [name, mocks.calls[name] = vi.fn().mockResolvedValue(null)])));
import { confirm, getErrorMessage, invoke, listen } from './platform';

beforeEach(() => { vi.clearAllMocks(); for (const name of names) mocks.calls[name]?.mockResolvedValue(null); });

describe('desktop command contract', () => {
  it.each([
    ['list_accounts', {}, 'ListAccounts', []],
    ['add_account', { name: 'test', authJson: 'synthetic' }, 'AddAccount', ['test', 'synthetic']],
    ['remove_account', { accountId: 'id' }, 'RemoveAccount', ['id']],
    ['switch_account', { accountId: 'id' }, 'SwitchAccount', ['id']],
    ['update_account_auth', { accountId: 'id', confirmMismatch: false }, 'UpdateAccountAuth', ['id', false]],
    ['choose_auth_file', {}, 'ChooseAuthFile', []],
    ['get_usage_stats', { days: 30, refreshPrices: true }, 'GetUsageStats', [30, true]],
    ['get_usage_stats', { days: 0 }, 'GetUsageStats', [0, null]],
    ['get_account_quotas', {}, 'GetAccountQuotas', []],
    ['get_account_quota', { accountId: 'id' }, 'GetAccountQuota', ['id']],
    ['get_settings', {}, 'GetSettings', []],
    ['detect_codex_cli_path', {}, 'DetectCodexCLIPath', []],
    ['save_settings', { settings: { enabled: false, time: null, cliPath: null } }, 'SaveSettings', [{ enabled: false, time: null, cliPath: null }]],
    ['get_scheduled_run_status', {}, 'GetScheduledRunStatus', []],
    ['open_url', { url: 'https://example.com' }, 'OpenURL', ['https://example.com']]
  ] as const)('%s maps parameters without changing values', async (command, args, method, parameters) => {
    expect(await invoke(command, args)).toBeNull();
    expect(mocks.calls[method]).toHaveBeenCalledWith(...parameters);
  });
  it('preserves results, cancellation and errors', async () => {
    await invoke('list_accounts');
    const result = { accounts: [], activeAccountId: null };
    mocks.calls.ListAccounts.mockResolvedValueOnce(result).mockRejectedValueOnce(new Error('无法读取账号'));
    expect(await invoke('list_accounts')).toBe(result);
    await expect(invoke('list_accounts')).rejects.toThrow('无法读取账号');
    await expect(invoke('toString')).rejects.toThrow('未知桌面命令');
  });
  it('adapts native confirmation defaults and rejection', async () => {
    expect(await confirm('删除？')).toBeNull();
    expect(mocks.calls.Confirm).toHaveBeenCalledWith('删除？', { title: '确认', kind: 'info' });
    mocks.calls.Confirm.mockResolvedValueOnce(false);
    expect(await confirm('删除？', { title: '删除账号', kind: 'warning' })).toBe(false);
  });
});

it('unwraps event data and disposes only this subscription once', async () => {
  const off = vi.fn(); mocks.on.mockReturnValue(off);
  const handler = vi.fn(); const unlisten = await listen('accounts-changed', handler);
  const callback = mocks.on.mock.calls[0][1]; callback({ data: { accounts: [] } });
  expect(handler).toHaveBeenCalledWith({ accounts: [] }); unlisten(); unlisten(); expect(off).toHaveBeenCalledTimes(1);
});
it('formats strings, Go errors, Error objects and empty errors', () => {
  expect(getErrorMessage('错误', 'fallback')).toBe('错误');
  expect(getErrorMessage(new Error('错误'), 'fallback')).toBe('错误');
  expect(getErrorMessage({ message: '错误' }, 'fallback')).toBe('错误');
  expect(getErrorMessage(null, 'fallback')).toBe('fallback');
});
