import { useEffect, useState, type FormEvent } from 'react';
import Button from 'antd/es/button';
import Input from 'antd/es/input';
import Switch from 'antd/es/switch';
import Tag from 'antd/es/tag';
import { getErrorMessage, invoke, listen } from '../tauri';
import type { ScheduledAccountStatus, ScheduledRunStatus, Settings } from '../types';

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

export default function SettingsView({ active }: { active: boolean }) {
  const [settings, setSettings] = useState<Settings>(defaultSettings);
  const [saved, setSaved] = useState<Settings | null>(null);
  const [status, setStatus] = useState<ScheduledRunStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

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
              <Input
                type="time" step={60} value={settings.time ?? ''}
                required={settings.enabled} disabled={loading || saving}
                onChange={(event) => { setSettings((value) => ({ ...value, time: event.target.value || null })); setMessage(''); }}
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
                placeholder="留空自动查找 Codex CLI"
                onChange={(event) => { setSettings((value) => ({ ...value, cliPath: event.target.value || null })); setMessage(''); }}
              />
            </label>
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
                  <div className="settings-result" key={account.accountId}>
                    <div className="settings-result-title"><strong>{account.accountName}</strong><Tag color={statusColors[account.status]}>{statusLabels[account.status]}</Tag></div>
                    <span className="settings-help">{account.startedAt ? `开始：${formatTime(account.startedAt)}` : '等待前面的账号完成'}{account.finishedAt ? ` · 结束：${formatTime(account.finishedAt)}` : ''}</span>
                    {account.message && <p className={account.status === 'failed' ? 'settings-error' : 'settings-help'}>{account.message}</p>}
                  </div>
                ))}
              </div>
            )}
          </> : <p className="settings-help">启用并保存后，任务会在设定时间自动执行。</p>}
        </section>
      </div>
    </section>
  );
}
