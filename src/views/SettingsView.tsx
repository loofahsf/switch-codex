import { useEffect, useState, type FormEvent } from 'react';
import Button from 'antd/es/button';
import Input from 'antd/es/input';
import Switch from 'antd/es/switch';
import Tag from 'antd/es/tag';
import TimePicker from 'antd/es/time-picker';
import Modal from 'antd/es/modal';
import dayjs from 'dayjs';
import { getErrorMessage, invoke, listen } from '../tauri';
import type { ScheduledAccountResult, ScheduledAccountStatus, ScheduledRunStatus, Settings } from '../types';

const defaultSettings: Settings = { enabled: false, time: null, cliPath: null };
const statusLabels: Record<ScheduledAccountStatus, string> = {
  waiting: '等待中', running: '执行中', success: '成功', failed: '失败', interrupted: '已中断'
};
const statusColors: Record<ScheduledAccountStatus, string> = {
  waiting: 'default', running: 'processing', success: 'success', failed: 'error', interrupted: 'warning'
};

function formatTime(value: string | null | undefined): string {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
}

function responseText(account: ScheduledAccountResult): string {
  if (account.response == null) return '该历史记录未保存此内容';
  if (account.response) return account.response;
  if (account.status === 'waiting') return '等待执行，尚无响应。';
  if (account.status === 'running') return '执行中，尚未收到完整响应。';
  return '本次调用未收到完整的助手响应。';
}

export default function SettingsView({ active }: { active: boolean }) {
  const [settings, setSettings] = useState<Settings>(defaultSettings);
  const [saved, setSaved] = useState<Settings | null>(null);
  const [status, setStatus] = useState<ScheduledRunStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [detectedCliPath, setDetectedCliPath] = useState<string | null>(null);
  const [detectingCli, setDetectingCli] = useState(true);
  const [cliDetectionError, setCliDetectionError] = useState('');
  const [selected, setSelected] = useState<{ batchStartedAt: string; accountId: string } | null>(null);
  const needsAutoPath = !settings.cliPath?.trim();

  useEffect(() => {
    // Detect on mount and when a custom value is cleared. It remains a hint,
    // never a form value, so saving an empty path still means auto-discovery.
    if (!needsAutoPath) { setDetectingCli(false); return; }
    let disposed = false;
    setDetectingCli(true);
    setCliDetectionError('');
    invoke<string | null>('detect_codex_cli_path')
      .then((path) => { if (!disposed) setDetectedCliPath(path); })
      .catch((reason) => {
        if (!disposed) {
          setDetectedCliPath(null);
          setCliDetectionError(getErrorMessage(reason, '自动查找 Codex CLI 失败'));
        }
      })
      .finally(() => { if (!disposed) setDetectingCli(false); });
    return () => { disposed = true; };
  }, [needsAutoPath]);

  useEffect(() => {
    let disposed = false;
    let unlisten: (() => void) | undefined;
    let eventReceived = false;
    async function load() {
      try {
        unlisten = await listen<ScheduledRunStatus>('scheduled-run-changed', (next) => {
          eventReceived = true;
          if (!disposed) setStatus(next);
        });
        if (disposed) { unlisten(); return; }
        const [nextSettings, nextStatus] = await Promise.all([
          invoke<Settings>('get_settings'), invoke<ScheduledRunStatus>('get_scheduled_run_status')
        ]);
        if (disposed) return;
        setSettings(nextSettings);
        setSaved(nextSettings);
        if (!eventReceived) setStatus(nextStatus);
      } catch (reason) {
        if (!disposed) setError(getErrorMessage(reason, '读取设置失败'));
      } finally {
        if (!disposed) setLoading(false);
      }
    }
    void load();
    return () => { disposed = true; unlisten?.(); };
  }, []);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (settings.enabled && !settings.time) {
      setError('启用前请选择有效的执行时间（HH:mm:ss）');
      return;
    }
    setSaving(true);
    setError('');
    setMessage('');
    try {
      const next = await invoke<Settings>('save_settings', { settings });
      setSettings(next);
      setSaved(next);
      setMessage('设置已保存，下次任务将按新设置执行。');
    } catch (reason) {
      setError(getErrorMessage(reason, '保存设置失败'));
    } finally {
      setSaving(false);
    }
  }

  const dirty = saved !== null && JSON.stringify(saved) !== JSON.stringify(settings);
  const batch = status?.lastRun;
  const selectedAccount = batch?.startedAt === selected?.batchStartedAt
    ? batch?.accounts.find((account) => account.accountId === selected?.accountId)
    : undefined;
  const successCount = batch?.accounts.filter((account) => account.status === 'success').length ?? 0;
  const finishedCount = batch?.accounts.filter((account) => !['waiting', 'running'].includes(account.status)).length ?? 0;

  return (
    <section className={`view${active ? ' is-active' : ''}`}>
      <header className="content-header" data-tauri-drag-region="true">
        <h1 data-tauri-drag-region="true">设置</h1>
      </header>
      <div className="content-grid settings-content" aria-busy={loading}>
        <form className="panel settings-panel" onSubmit={(event) => void save(event)}>
          <div className="panel-heading settings-heading">
            <div>
              <h2>每日定时调用</h2>
              <span>每天调用全部账号，尝试开启五小时用量窗口。</span>
            </div>
            <Switch
              aria-label="启用定时调用"
              checked={settings.enabled}
              disabled={loading || saving || saved === null}
              onChange={(enabled) => { setSettings((value) => ({ ...value, enabled })); setMessage(''); }}
            />
          </div>
          <div className="settings-fields">
            <label className="field">
              <span>每日执行时间</span>
              <TimePicker
                className="settings-time-picker"
                aria-label="每日执行时间"
                format="HH:mm:ss" use12Hours={false} needConfirm={false}
                hourStep={1} minuteStep={1} secondStep={1}
                value={settings.time ? dayjs(`2000-01-01T${settings.time}`) : null}
                placeholder="请选择时分秒" disabled={loading || saving}
                onChange={(time) => { setSettings((value) => ({ ...value, time: time?.isValid() ? time.format('HH:mm:ss') : null })); setMessage(''); }}
              />
            </label>
            <div className="settings-schedule">
              <span>电脑本地时区：{status?.timezone ?? '读取中…'}</span>
              <strong>下次执行：{saved?.enabled ? formatTime(status?.nextRunAt) : '未启用'}</strong>
              {dirty && <span>尚未保存，当前计划仍使用已保存的设置。</span>}
            </div>
          </div>
          <dl className="settings-details">
            <div><dt>模型</dt><dd><code>gpt-5.6-luna</code></dd></div>
            <div><dt>提示词</dt><dd>What model are you?</dd></div>
            <div><dt>执行方式</dt><dd>串行调用，每次只运行一个账号</dd></div>
          </dl>
          <p className="settings-help">每分钟检查一次，整批启动允许延迟最多 5 分钟。开始时固定账号队列，排队超过 5 分钟仍继续执行；单账号超过 120 秒会结束并继续下一个。</p>
          <p className="settings-help">请保持应用运行、电脑清醒并联网。退出或休眠错过的任务不补跑。关闭定时调用或修改设置不会停止已经开始的队列。五小时窗口以服务端额度数据为准。</p>
          <details className="settings-cli">
            <summary>Codex CLI 路径（可选）</summary>
            <label className="field">
              <span>可执行文件绝对路径</span>
              <Input
                value={settings.cliPath ?? ''} disabled={loading || saving}
                placeholder={detectingCli ? '正在查找 Codex CLI…' : detectedCliPath ?? '未找到 Codex CLI，请填写绝对路径'}
                onChange={(event) => { setSettings((value) => ({ ...value, cliPath: event.target.value || null })); setMessage(''); }}
              />
            </label>
            {cliDetectionError && <p className="settings-error" role="alert">{cliDetectionError}</p>}
            <p className="settings-help">未安装或版本不兼容时，请先安装或升级 Codex CLI。Windows 请填写 codex.exe 的路径。</p>
          </details>
          <div className="settings-actions">
            <Button type="primary" htmlType="submit" loading={saving} disabled={loading || saved === null || !dirty}>保存设置</Button>
            <span role="status" className="settings-success">{message}</span>
          </div>
          {error && <p role="alert" className="settings-error">{error}</p>}
          {status?.error && <p role="alert" className="settings-error">{status.error}</p>}
        </form>
        <section className="panel settings-panel" aria-label="最近一次任务">
          <div className="panel-heading settings-heading">
            <div>
              <h2>最近一次任务</h2>
              <span>{batch ? `开始于 ${formatTime(batch.startedAt)}` : '尚无执行记录'}</span>
            </div>
            {batch && <Tag color={status?.running ? 'processing' : 'default'}>{status?.running ? '执行中' : '已结束'}</Tag>}
          </div>
          {batch ? <>
            <p className="settings-help">已处理 {finishedCount} / {batch.accounts.length} 个账号，成功 {successCount} 个{batch.finishedAt ? ` · 结束于 ${formatTime(batch.finishedAt)}` : ''}</p>
            {batch.accounts.length === 0 ? <div className="empty-state">任务开始时没有已保存账号。</div> : (
              <div className="settings-results">
                {batch.accounts.map((account) => (
                  <button
                    type="button" className="settings-result" key={account.accountId}
                    aria-label={`查看 ${account.accountName} 的调用详情`}
                    onClick={() => setSelected({ batchStartedAt: batch.startedAt, accountId: account.accountId })}
                  >
                    <span className="settings-result-title"><strong>{account.accountName}</strong><Tag color={statusColors[account.status]}>{statusLabels[account.status]}</Tag></span>
                    <span className="settings-help">{account.startedAt ? `开始：${formatTime(account.startedAt)}` : '等待前面的账号完成'}{account.finishedAt ? ` · 结束：${formatTime(account.finishedAt)}` : ''}</span>
                    {account.message && <span className={account.status === 'failed' ? 'settings-error' : 'settings-help'}>{account.message}</span>}
                  </button>
                ))}
              </div>
            )}
          </> : <p className="settings-help">启用并保存后，任务会在设定时间自动执行。</p>}
        </section>
      </div>
      <Modal
        title={selectedAccount ? `${selectedAccount.accountName} · 调用详情` : '调用详情'}
        open={Boolean(selectedAccount)} onCancel={() => setSelected(null)} footer={null} width={720}
        styles={{ body: { maxHeight: 'calc(100vh - 240px)', overflowY: 'auto' } }}
      >
        {selectedAccount && <div className="call-detail-content">
          <dl className="settings-details">
            <div><dt>账号</dt><dd>{selectedAccount.accountName}</dd></div>
            <div><dt>执行状态</dt><dd><Tag color={statusColors[selectedAccount.status]}>{statusLabels[selectedAccount.status]}</Tag></dd></div>
            <div><dt>开始时间</dt><dd>{formatTime(selectedAccount.startedAt)}</dd></div>
            <div><dt>结束时间</dt><dd>{formatTime(selectedAccount.finishedAt)}</dd></div>
          </dl>
          <h3>实际提示词</h3>
          <pre>{selectedAccount.prompt ?? (selectedAccount.response == null ? '该历史记录未保存此内容' : '尚未发送提示词。')}</pre>
          <h3>响应结果</h3>
          <pre>{responseText(selectedAccount)}</pre>
          {selectedAccount.message && <p className={selectedAccount.status === 'failed' ? 'settings-error' : 'settings-help'}>{selectedAccount.message}</p>}
        </div>}
      </Modal>
    </section>
  );
}
