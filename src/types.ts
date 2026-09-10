export type ViewName = 'accounts' | 'usage' | 'settings';
export interface Settings {
  enabled: boolean;
  time: string | null;
  cliPath: string | null;
  autoSyncAuth: boolean;
}
export type AuthSyncState = 'disabled' | 'checking' | 'up_to_date' | 'synced' | 'followed'
  | 'unknown' | 'ambiguous' | 'invalid' | 'missing' | 'error';
export interface AuthSyncStatus {
  enabled: boolean;
  state: AuthSyncState;
  accountId: string | null;
  accountName: string | null;
  checkedAt: string | null;
  syncedAt: string | null;
  pendingId: string | null;
  message: string | null;
}
export type ScheduledAccountStatus = 'waiting' | 'running' | 'success' | 'failed' | 'interrupted';
export interface ScheduledAccountResult {
  accountId: string;
  accountName: string;
  status: ScheduledAccountStatus;
  scheduledAt: string | null;
  startedAt: string | null;
  finishedAt: string | null;
  message: string | null;
  prompt: string | null;
  response: string | null;
}
export interface ScheduledRunStatus {
  nextRunAt: string | null;
  timezone: string;
  running: boolean;
  error: string | null;
  lastRun: {
    startedAt: string;
    finishedAt: string | null;
    accounts: ScheduledAccountResult[];
  } | null;
}
export type MessageType = 'neutral' | 'success' | 'error';

export interface InlineMessage {
  text: string;
  type: MessageType;
}

export interface AccountItem {
  id: string;
  name: string;
  createdAt: string;
  updatedAt: string;
  authPath: string;
  isActive: boolean;
}

export interface AccountsState {
  dataDir: string;
  targetAuthPath: string;
  activeAccountId: string | null;
  accounts: AccountItem[];
}

export interface ChosenFile {
  filePath: string;
  fileName: string;
  authJson: string;
}

export interface RateLimitWindow {
  usedPercent: number;
  windowMinutes: number | null;
  resetsAt: number | null;
  resetAfterSeconds: number | null;
}

export interface CreditsInfo {
  hasCredits: boolean;
  unlimited: boolean;
  balance: string | null;
}

export interface AccountQuota {
  accountId: string;
  accountName: string;
  ok: boolean;
  planType: string | null;
  primary: RateLimitWindow | null;
  secondary: RateLimitWindow | null;
  tertiary: RateLimitWindow | null;
  fiveHour: RateLimitWindow | null;
  weekly: RateLimitWindow | null;
  monthly: RateLimitWindow | null;
  credits: CreditsInfo | null;
  fetchedAt: string | null;
  error: string | null;
}

export interface AccountQuotas {
  sourceUrl: string;
  accounts: AccountQuota[];
}

export interface PricingSource {
  url: string;
  fetchedAt: string;
  kind: string;
  modelCount: number;
  warning: string | null;
}

export interface UsageSummary {
  inputTokens: number;
  cachedInputTokens: number;
  cacheWriteInputTokens: number;
  outputTokens: number;
  reasoningOutputTokens: number;
  totalTokens: number;
  sessions: number;
  modelCalls: number;
  estimatedCostUsd: number;
  unpricedModelCount: number;
}

export interface DisplayedPrice {
  input: number;
  cachedInput: number | null;
  cacheWrite: number | null;
  output: number;
}

export interface ModelUsageItem {
  model: string;
  inputTokens: number;
  cachedInputTokens: number;
  cacheWriteInputTokens: number;
  outputTokens: number;
  reasoningOutputTokens: number;
  totalTokens: number;
  modelCalls: number;
  turnCount: number;
  averageTokensPerTurn: number | null;
  averageDurationMs: number | null;
  averageTimeToFirstTokenMs: number | null;
  estimatedCostUsd: number | null;
  price: DisplayedPrice | null;
}

export interface DailyUsageItem {
  date: string;
  totalTokens: number;
  estimatedCostUsd: number;
}

export interface UsageStats {
  generatedAt: string;
  rangeDays: number;
  sessionsDir: string;
  pricingSource: PricingSource;
  summary: UsageSummary;
  models: ModelUsageItem[];
  daily: DailyUsageItem[];
}
