import { FormEvent, useState } from 'react';
import Button from 'antd/es/button';
import Input from 'antd/es/input';
import type {
  AccountItem,
  AccountQuotas,
  AccountsState,
  ChosenFile,
  InlineMessage
} from '../types';
import { AccountRowQuota } from '../components/Quota';
import plusIcon from '../assets/plus.svg';
import refreshIcon from '../assets/refresh.svg';

interface AccountsViewProps {
  active: boolean;
  state: AccountsState;
  quotas: AccountQuotas | null;
  quotaLoading: boolean;
  refreshingQuotaAccountId: string | null;
  inlineMessage: InlineMessage;
  onChooseFile: () => Promise<ChosenFile | null>;
  onAddAccount: (name: string, authJson: string) => Promise<boolean>;
  onSwitchAccount: (account: AccountItem) => Promise<void>;
  onUpdateAccount: (account: AccountItem) => Promise<void>;
  onRemoveAccount: (account: AccountItem) => Promise<void>;
  onRefreshQuotas: () => Promise<void>;
  onRefreshAccountQuota: (account: AccountItem) => Promise<void>;
}

export default function AccountsView({
  active,
  state,
  quotas,
  quotaLoading,
  refreshingQuotaAccountId,
  inlineMessage,
  onChooseFile,
  onAddAccount,
  onSwitchAccount,
  onUpdateAccount,
  onRemoveAccount,
  onRefreshQuotas,
  onRefreshAccountQuota
}: AccountsViewProps) {
  const [formCollapsed, setFormCollapsed] = useState(true);
  const [name, setName] = useState('');
  const [selectedAuth, setSelectedAuth] = useState<ChosenFile | null>(null);
  const [saving, setSaving] = useState(false);
  const [switchingId, setSwitchingId] = useState<string | null>(null);
  const [syncingId, setSyncingId] = useState<string | null>(null);

  async function chooseAuthFile() {
    const file = await onChooseFile();
    if (file) {
      setSelectedAuth(file);
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedAuth) {
      await onAddAccount(name, '');
      return;
    }
    setSaving(true);
    try {
      if (await onAddAccount(name, selectedAuth.authJson)) {
        setName('');
        setSelectedAuth(null);
      }
    } finally {
      setSaving(false);
    }
  }

  async function switchAccount(account: AccountItem) {
    setSwitchingId(account.id);
    try {
      await onSwitchAccount(account);
    } finally {
      setSwitchingId(null);
    }
  }

  async function updateAccount(account: AccountItem) {
    setSyncingId(account.id);
    try {
      await onUpdateAccount(account);
    } finally {
      setSyncingId(null);
    }
  }

  return (
    <section className={`view${active ? ' is-active' : ''}`}>
      <header className="content-header" data-window-drag-region="true">
        <h1 data-window-drag-region="true">账号管理</h1>
        <Button
          type="primary"
          className="primary-button add-account-button"
          aria-expanded={!formCollapsed}
          aria-controls="accountFormContent"
          aria-label={formCollapsed ? '展开新增账号表单' : '收起新增账号表单'}
          onClick={() => setFormCollapsed((value) => !value)}
        >
          <img src={plusIcon} alt="" />
          <span>{formCollapsed ? '添加账号' : '收起表单'}</span>
        </Button>
      </header>

      <div className={`content-grid${formCollapsed ? ' is-form-collapsed' : ''}`}>
        <form className={`panel account-form${formCollapsed ? ' is-collapsed' : ''}`} onSubmit={submit}>
          <div className="panel-heading">
            <div>
              <h2>添加账号</h2>
              <span>导入一份 Codex auth.json 并设置易于识别的名称</span>
            </div>
          </div>
          <div className="account-form-content" id="accountFormContent">
            <label className="field">
              <span>账号名称</span>
              <Input
                value={name}
                autoComplete="off"
                placeholder="例如 work / personal"
                required
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            <div className="field file-field">
              <span>认证文件</span>
              <Button type="text" className="file-button" onClick={chooseAuthFile}>
                选择 auth.json
              </Button>
              <div className="selected-file">{selectedAuth?.filePath || '未选择文件'}</div>
            </div>
            <Button type="primary" htmlType="submit" className="primary-button form-submit" loading={saving}>
              保存账号
            </Button>
            <p className="form-message" data-type={inlineMessage.type} role="status">
              {inlineMessage.text}
            </p>
          </div>
        </form>

        <section className="account-list-panel">
          <div className="list-heading">
            <span>{state.accounts.length} 个账号</span>
            <Button
              type="text"
              className="refresh-quota-btn"
              disabled={quotaLoading}
              onClick={onRefreshQuotas}
            >
              <img src={refreshIcon} alt="" />
              <span>{quotaLoading ? '查询中...' : '刷新额度'}</span>
            </Button>
          </div>
          <div className="account-list">
            {state.accounts.length === 0 ? (
              <div className="empty-state">还没有账号，先添加一份 Codex auth.json。</div>
            ) : (
              state.accounts.map((account) => {
                const quota = quotas?.accounts.find((item) => item.accountId === account.id);
                return (
                  <article
                    className="account-item"
                    data-active={account.isActive ? 'true' : 'false'}
                    key={account.id}
                  >
                    <div className="account-header">
                      <div className="account-main">
                        <div className="account-title-row">
                          <span className="account-name">{account.name}</span>
                          {quota?.planType || quota?.ok ? (
                            <span className="account-plan-badge">{quota?.planType || 'Codex'}</span>
                          ) : null}
                          <span className="active-pill">当前生效</span>
                        </div>
                        <div className="account-path">{account.authPath}</div>
                      </div>
                      <div className="account-actions">
                        <Button
                          type="primary"
                          className="switch-button"
                          disabled={account.isActive}
                          loading={switchingId === account.id}
                          onClick={() => switchAccount(account)}
                        >
                          切换
                        </Button>
                        <Button
                          type="text"
                          className="refresh-account-quota-button"
                          title="刷新该账号的限额信息"
                          aria-label="刷新该账号的限额信息"
                          disabled={quotaLoading || refreshingQuotaAccountId !== null}
                          loading={refreshingQuotaAccountId === account.id}
                          onClick={() => onRefreshAccountQuota(account)}
                        />
                        <Button
                          type="text"
                          className="sync-auth-button"
                          title="用当前 ~/.codex/auth.json 覆盖保存的文件"
                          aria-label="反向同步认证文件"
                          disabled={syncingId === account.id}
                          onClick={() => updateAccount(account)}
                        />
                        <Button
                          type="text"
                          className="danger-button"
                          aria-label="删除账号"
                          onClick={() => onRemoveAccount(account)}
                        />
                      </div>
                    </div>
                    <AccountRowQuota
                      quota={quota}
                      loading={quotaLoading || refreshingQuotaAccountId === account.id}
                    />
                  </article>
                );
              })
            )}
          </div>
        </section>
      </div>

      <footer className="paths">
        <div>
          <span>数据目录</span>
          <code>{state.dataDir || '-'}</code>
        </div>
        <div>
          <span>当前生效文件</span>
          <code>{state.targetAuthPath || '-'}</code>
        </div>
      </footer>
    </section>
  );
}
