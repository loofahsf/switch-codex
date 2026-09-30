import type { AccountQuota, AccountQuotas, AccountsState, AuthSyncStatus } from './types';

interface QuotaEntry {
  quota: AccountQuota;
  revision: number;
}

export interface AccountData {
  accounts: AccountsState | null;
  quotas: AccountQuotas | null;
  quotaEntries: Record<string, QuotaEntry>;
}

export const emptyAccountData: AccountData = {
  accounts: null,
  quotas: null,
  quotaEntries: Object.create(null)
};

export function accountRevision(state: AccountsState | null): number {
  return state?.revision ?? 0;
}

export function applyAccounts(data: AccountData, incoming: AccountsState): AccountData {
  const revision = incoming.revision ?? 0;
  const previous = data.accounts;
  if (previous && (revision < accountRevision(previous) ||
    (revision === accountRevision(previous) && revision !== 0))) return data;

  const accounts = { ...incoming, revision };
  const allowed = new Set(accounts.accounts.map((account) => account.id));
  const previousAccounts = new Map(previous?.accounts.map((account) => [account.id, account]));
  const changedCredentials = new Set(accounts.accounts
    .filter((account) => previousAccounts.get(account.id)?.updatedAt !== undefined &&
      previousAccounts.get(account.id)?.updatedAt !== account.updatedAt)
    .map((account) => account.id));
  const entries = Object.fromEntries(
    Object.entries(data.quotaEntries).filter(([id, entry]) => allowed.has(id) &&
      (!changedCredentials.has(id) || entry.revision >= revision) &&
      (previous || entry.revision >= revision))
  );
  const quotas = data.quotas && {
    ...data.quotas,
    accounts: Object.values(entries).map((entry) => entry.quota)
  };
  return { accounts, quotas, quotaEntries: entries };
}

export function applyQuotas(data: AccountData, incoming: AccountQuotas): AccountData {
  const revision = incoming.revision ?? 0;
  if (data.accounts && revision < accountRevision(data.accounts)) return data;

  const allowed = data.accounts && new Set(data.accounts.accounts.map((account) => account.id));
  const entries: Record<string, QuotaEntry> = Object.assign(Object.create(null), data.quotaEntries);
  let changed = false;
  for (const rawQuota of incoming.accounts ?? []) {
    if (allowed && !allowed.has(rawQuota.accountId)) continue;
    const quota = { ...rawQuota, requestId: rawQuota.requestId ?? 0 };
    const old = entries[quota.accountId];
    if (old && (old.revision > revision ||
      (old.revision === revision && old.quota.requestId > quota.requestId))) continue;
    // Legacy backends have no request ID; use arrival order for their equal IDs.
    entries[quota.accountId] = { quota, revision };
    changed = true;
  }
  if (!changed) return data;
  return {
    ...data,
    quotaEntries: entries,
    quotas: {
      sourceUrl: incoming.sourceUrl || data.quotas?.sourceUrl || '',
      revision,
      accounts: Object.values(entries).map((entry) => entry.quota)
    }
  };
}

export function hasMissingQuotas(data: AccountData): boolean {
  return missingQuotaIDs(data).length > 0;
}

export function missingQuotaIDs(data: AccountData): string[] {
  return data.accounts?.accounts
    .filter((account) => !Object.hasOwn(data.quotaEntries, account.id))
    .map((account) => account.id) ?? [];
}

export interface AuthSyncData {
  status: AuthSyncStatus;
  eventGeneration: number;
}

export function applyAuthSyncEvent(data: AuthSyncData, status: AuthSyncStatus): AuthSyncData {
  if (data.status.checkedAt && status.checkedAt &&
    Date.parse(status.checkedAt) < Date.parse(data.status.checkedAt)) return data;
  return { status, eventGeneration: data.eventGeneration + 1 };
}

export function applyAuthSyncQuery(
  data: AuthSyncData, status: AuthSyncStatus, startedGeneration: number
): AuthSyncData {
  if (data.eventGeneration !== startedGeneration) return data;
  if (data.status.checkedAt && status.checkedAt &&
    Date.parse(status.checkedAt) < Date.parse(data.status.checkedAt)) return data;
  return { ...data, status };
}

export interface UsageFreshness {
  epoch: number;
  completedEpoch: number;
}

export function invalidateUsage(freshness: UsageFreshness): UsageFreshness {
  return { ...freshness, epoch: freshness.epoch + 1 };
}

export function completeUsage(freshness: UsageFreshness, startedEpoch: number): UsageFreshness {
  return startedEpoch === freshness.epoch
    ? { ...freshness, completedEpoch: startedEpoch }
    : freshness;
}
