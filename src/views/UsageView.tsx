import { lazy, Suspense } from 'react';
import Button from 'antd/es/button';
import Select from 'antd/es/select';
import Spin from 'antd/es/spin';
import type {
  AccountQuotas,
  AccountsState,
  InlineMessage,
  UsageStats
} from '../types';
import { formatCompactNumber, formatDateTime, formatNumber, formatUsd } from '../format';
import { UsageQuotaGrid } from '../components/Quota';

interface UsageViewProps {
  active: boolean;
  state: AccountsState;
  quotas: AccountQuotas | null;
  stats: UsageStats | null;
  range: number;
  loading: boolean;
  loadingText: string;
  inlineMessage: InlineMessage;
  onRangeChange: (range: number) => void;
  onRefresh: () => void;
  onOpenUrl: (url: string) => void;
}

const rangeOptions = [
  { value: 7, label: '最近 7 天' },
  { value: 14, label: '最近 14 天' },
  { value: 30, label: '最近 30 天' },
  { value: 90, label: '最近 90 天' },
  { value: 0, label: '全部记录' }
];

const DailyTokenChart = lazy(() => import('../components/DailyTokenChart'));

export default function UsageView({
  active,
  state,
  quotas,
  stats,
  range,
  loading,
  loadingText,
  inlineMessage,
  onRangeChange,
  onRefresh,
  onOpenUrl
}: UsageViewProps) {
  const rangeLabel = rangeOptions.find((option) => option.value === range)?.label ?? '当前范围';
  const summary = stats?.summary;
  const cacheRatio = summary?.inputTokens
    ? (summary.cachedInputTokens / summary.inputTokens) * 100
    : 0;
  const source = stats?.pricingSource;
  const sourceLabels: Record<string, string> = {
    live: '官方实时价格',
    cache: '官方价格缓存',
    bundled: '内置官方快照'
  };
  const pricingUrl = (source?.url || 'https://developers.openai.com/api/docs/pricing').replace(
    /\.md$/,
    ''
  );

  return (
    <section
      className={`view usage-view${active ? ' is-active' : ''}`}
      aria-busy={loading}
    >
      <header className="content-header usage-header" data-window-drag-region="true">
        <h1 data-window-drag-region="true">订阅配额与本地 Token</h1>
        <div className="usage-controls">
          <span className="sr-only" id="usageRangeLabel">
            统计范围
          </span>
          <Select
            className="usage-range-select"
            aria-labelledby="usageRangeLabel"
            value={range}
            options={rangeOptions}
            disabled={loading}
            onChange={onRangeChange}
          />
          <Button
            type="primary"
            className="refresh-button"
            disabled={loading}
            onClick={onRefresh}
          >
            刷新官方数据
          </Button>
        </div>
      </header>

      {loading ? (
        <div className="usage-loading" role="status" aria-live="polite">
          <div className="usage-loading-card">
            <Spin size="large" />
            <strong>{loadingText}</strong>
            <span>数据较多时可能需要一点时间</span>
          </div>
        </div>
      ) : null}

      <div className="usage-scroll">
        <p className="usage-message" data-type={inlineMessage.type} role="status">
          {inlineMessage.text}
        </p>

        <section className="usage-section quota-section">
          <div className="section-heading">
            <h3>账号配额（独立查询）</h3>
            <span className="source-badge">OpenAI Codex</span>
          </div>
          <UsageQuotaGrid state={state} quotas={quotas?.accounts || null} />
        </section>

        <section className="usage-section local-usage-section">
          <div className="section-heading">
            <h3>本机使用量</h3>
            <span>{stats ? `统计于 ${formatDateTime(stats.generatedAt)}` : '尚未统计'}</span>
          </div>
          <div className="metric-grid">
            <article className="metric-card metric-card-featured">
              <strong>{formatUsd(summary?.estimatedCostUsd)}</strong>
              <span>API 等价估算</span>
              <small>当前统计范围</small>
            </article>
            <article className="metric-card">
              <strong>{formatCompactNumber(summary?.totalTokens)}</strong>
              <span>总 Token</span>
              <small>{formatNumber(summary?.sessions)} 个会话</small>
            </article>
            <article className="metric-card">
              <strong>{formatCompactNumber(summary?.cachedInputTokens)}</strong>
              <span>缓存占比 {cacheRatio.toFixed(0)}%</span>
              <small>缓存输入 Token</small>
            </article>
            <article className="metric-card">
              <strong>{formatCompactNumber(summary?.outputTokens)}</strong>
              <span>模型输出</span>
              <small>{formatNumber(summary?.modelCalls)} 次模型调用</small>
            </article>
          </div>
        </section>

        <div className="usage-detail-grid">
          <article className="usage-subpanel chart-panel">
            <div className="subpanel-heading">
              <h4>每日 Token 趋势</h4>
              <span>{rangeLabel}</span>
            </div>
            <div className="daily-chart">
              {!stats?.daily.length ? (
                <div className="usage-empty compact">暂无 Token 记录</div>
              ) : (
                <Suspense fallback={<div className="chart-loading">正在加载图表…</div>}>
                  <DailyTokenChart items={stats.daily} />
                </Suspense>
              )}
            </div>
          </article>

          <div className="model-table-wrap">
            <table className="model-table">
              <thead>
                <tr>
                  <th>模型</th>
                  <th>输入</th>
                  <th>缓存</th>
                  <th>总 Token</th>
                  <th title="包含完整响应且有 Token 数据的回合">问题数</th>
                  <th>平均 Token/问题</th>
                  <th>费用</th>
                </tr>
              </thead>
              <tbody>
                {!stats?.models.length ? (
                  <tr>
                    <td colSpan={7} className="table-empty">
                      暂无 Token 记录
                    </td>
                  </tr>
                ) : (
                  stats.models.map((model) => (
                    <tr
                      key={model.model}
                      title={
                        model.price
                          ? `每百万 Token：输入 ${formatUsd(model.price.input)} · 缓存 ${formatUsd(
                              model.price.cachedInput ?? model.price.input
                            )} · 输出 ${formatUsd(model.price.output)}`
                          : undefined
                      }
                    >
                      <td className="model-name">{model.model}</td>
                      <td>{formatNumber(model.inputTokens)}</td>
                      <td>{formatNumber(model.cachedInputTokens)}</td>
                      <td>{formatNumber(model.totalTokens)}</td>
                      <td>{formatNumber(model.turnCount)}</td>
                      <td>
                        {model.averageTokensPerTurn == null
                          ? '—'
                          : formatNumber(Math.round(model.averageTokensPerTurn))}
                      </td>
                      <td className={model.estimatedCostUsd == null ? 'unpriced' : undefined}>
                        {model.estimatedCostUsd == null
                          ? '暂无官方价格'
                          : formatUsd(model.estimatedCostUsd)}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>

        <footer className="usage-footer">
          <div className="pricing-panel">
            <span>{source ? sourceLabels[source.kind] || 'OpenAI 官方价格' : '等待刷新'}</span>
            <a
              href={pricingUrl}
              target="_blank"
              rel="noreferrer"
              onClick={(event) => {
                event.preventDefault();
                onOpenUrl(pricingUrl);
              }}
            >
              OpenAI 官方模型价格
            </a>
            <small>
              {source
                ? `价格时间 ${formatDateTime(source.fetchedAt)} · ${source.modelCount} 个模型`
                : '尚未加载价格'}
            </small>
          </div>
          <p className="usage-footnote">
            金额是基于公开 API 价格的等价估算，不是 ChatGPT/Codex 订阅账单。
          </p>
        </footer>
      </div>
    </section>
  );
}
