import { describe, expect, it } from 'vitest';
import {
  applyAccounts, applyAuthSyncEvent, applyAuthSyncQuery, applyQuotas,
  completeUsage, emptyAccountData, hasMissingQuotas, invalidateUsage, missingQuotaIDs
} from './accountState';
import type { AccountQuota, AccountQuotas, AccountsState, AuthSyncStatus } from './types';

const account = (id: string, updatedAt = '2026-01-01') => ({
  id, name: id, createdAt: '2026-01-01', updatedAt, authPath: id, isActive: false
});
const state = (revision: number, ...ids: string[]): AccountsState => ({
  revision, dataDir: '', targetAuthPath: '', activeAccountId: null,
  accounts: ids.map((id) => account(id))
});
const quota = (id: string, requestId: number): AccountQuota => ({
  accountId: id, requestId, accountName: id, ok: true, planType: null,
  primary: null, secondary: null, tertiary: null, fiveHour: null, weekly: null,
  monthly: null, credits: null, resetCredits: null, fetchedAt: null, error: null
});
const batch = (revision: number, ...items: AccountQuota[]): AccountQuotas => ({
  revision, sourceUrl: 'test', accounts: items
});
const status = (checkedAt: string, state: AuthSyncStatus['state']): AuthSyncStatus => ({
  enabled: true, state, checkedAt, accountId: null, accountName: null,
  syncedAt: null, pendingId: null, message: null
});

describe('account snapshot ordering', () => {
  it('keeps an event newer than the initial list and an older operation response', () => {
    const event = applyAccounts(emptyAccountData, state(3, 'new'));
    expect(applyAccounts(event, state(2, 'old'))).toBe(event);
    expect(applyAccounts(event, state(1, 'initial'))).toBe(event);
    expect(applyAccounts(event, state(0, 'legacy'))).toBe(event);
  });

  it('prunes a deleted account immediately and rejects late quota data', () => {
    let data = applyQuotas(emptyAccountData, batch(1, quota('removed', 8)));
    data = applyAccounts(data, state(1, 'removed'));
    data = applyAccounts(data, state(2, 'survivor'));
    expect(data.quotas?.accounts).toEqual([]);
    expect(applyQuotas(data, batch(2, quota('removed', 99)))).toBe(data);
    expect(applyQuotas(data, batch(1, quota('removed', 100)))).toBe(data);
    expect(hasMissingQuotas(data)).toBe(true);
  });
});

describe('quota merging', () => {
  it('preserves other accounts through partial events and out-of-order full results', () => {
    let data = applyAccounts(emptyAccountData, state(4, 'a', 'b'));
    data = applyQuotas(data, batch(4, quota('a', 10), quota('b', 10)));
    data = applyQuotas(data, batch(4, quota('a', 12)));
    data = applyQuotas(data, batch(4, quota('a', 11), quota('b', 9)));
    expect(data.quotas?.accounts.map((item) => [item.accountId, item.requestId]))
      .toEqual([['a', 12], ['b', 10]]);
    expect(hasMissingQuotas(data)).toBe(false);
  });

  it('prefers credential revision even when an older revision has a higher request ID', () => {
    let data = applyAccounts(emptyAccountData, state(5, 'a'));
    data = applyQuotas(data, batch(5, quota('a', 2)));
    expect(applyQuotas(data, batch(4, quota('a', 100)))).toBe(data);
    data = applyQuotas(data, batch(6, quota('a', 3)));
    expect(applyQuotas(data, batch(5, quota('a', 99)))).toBe(data);
    expect(data.quotas?.accounts[0].requestId).toBe(3);
  });

  it('drops cached quota after the same account credential changes', () => {
    let data = applyAccounts(emptyAccountData, state(1, 'a', 'b'));
    data = applyQuotas(data, batch(1, quota('a', 2), quota('b', 2)));
    data = applyAccounts(data, {
      ...state(2, 'a', 'b'), accounts: [account('a', '2026-01-02'), account('b')]
    });
    expect(data.quotas?.accounts.map((item) => item.accountId)).toEqual(['b']);
    expect(hasMissingQuotas(data)).toBe(true);
    expect(missingQuotaIDs(data)).toEqual(['a']);
  });

  it('treats safe legacy account IDs as ordinary cache keys', () => {
    let data = applyAccounts(emptyAccountData, state(1, '__proto__', 'constructor'));
    expect(missingQuotaIDs(data)).toEqual(['__proto__', 'constructor']);
    data = applyQuotas(data, batch(1, quota('__proto__', 1), quota('constructor', 2)));
    expect(data.quotas?.accounts.map((item) => item.accountId)).toEqual(['__proto__', 'constructor']);
    expect(hasMissingQuotas(data)).toBe(false);
  });

  it('retains a fresh quota event that arrived before its account snapshot', () => {
    let data = applyAccounts(emptyAccountData, state(1, 'a'));
    data = applyQuotas(data, batch(2, quota('a', 3)));
    data = applyAccounts(data, {
      ...state(2, 'a'), accounts: [account('a', '2026-01-02')]
    });
    expect(data.quotas?.accounts[0].requestId).toBe(3);
    expect(hasMissingQuotas(data)).toBe(false);
  });
});

describe('auth sync and usage freshness', () => {
  it('does not let an initial auth sync query replace a later event', () => {
    const initial = { status: status('2026-01-01', 'checking'), eventGeneration: 0 };
    const afterEvent = applyAuthSyncEvent(initial, status('2026-01-03', 'followed'));
    expect(applyAuthSyncQuery(afterEvent, status('2026-01-02', 'up_to_date'), 0))
      .toBe(afterEvent);
  });

  it('does not mark an invalidated usage request complete', () => {
    const started = { epoch: 1, completedEpoch: 0 };
    const invalidated = invalidateUsage(started);
    expect(completeUsage(invalidated, started.epoch)).toBe(invalidated);
    expect(completeUsage(invalidated, invalidated.epoch).completedEpoch).toBe(2);
  });
});
