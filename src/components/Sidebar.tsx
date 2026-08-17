import type { AccountsState, ViewName } from '../types';
import appIcon from '../assets/app-icon.svg';
import chartIcon from '../assets/chart-bar.svg';
import userIcon from '../assets/user.svg';

interface SidebarProps {
  collapsed: boolean;
  view: ViewName;
  state: AccountsState;
  onCollapsedChange: (collapsed: boolean) => void;
  onViewChange: (view: ViewName) => void;
}

export default function Sidebar({
  collapsed,
  view,
  state,
  onCollapsedChange,
  onViewChange
}: SidebarProps) {
  const activeAccount = state.accounts.find((account) => account.id === state.activeAccountId);

  return (
    <aside className="sidebar" id="appSidebar">
      <div className="sidebar-top">
        <div className="traffic-lights" aria-hidden="true" data-tauri-drag-region="true">
          <span />
          <span />
          <span />
        </div>
        <div className="brand" data-tauri-drag-region="true">
          <img src={appIcon} alt="" data-tauri-drag-region="true" />
          <strong data-tauri-drag-region="true">Switch Codex</strong>
        </div>
        <nav className="view-tabs" aria-label="应用页面">
          <button
            type="button"
            className={`view-tab${view === 'accounts' ? ' is-active' : ''}`}
            aria-current={view === 'accounts' ? 'page' : undefined}
            title="账号管理"
            onClick={() => onViewChange('accounts')}
          >
            <img src={userIcon} alt="" />
            <span>账号管理</span>
          </button>
          <button
            type="button"
            className={`view-tab${view === 'usage' ? ' is-active' : ''}`}
            aria-current={view === 'usage' ? 'page' : undefined}
            title="用量统计"
            onClick={() => onViewChange('usage')}
          >
            <img src={chartIcon} alt="" />
            <span>用量统计</span>
          </button>
        </nav>
      </div>
      <button
        type="button"
        className="sidebar-toggle"
        aria-label={collapsed ? '展开侧边栏' : '收起侧边栏'}
        aria-controls="appSidebar"
        aria-expanded={!collapsed}
        title={collapsed ? '展开侧边栏' : '收起侧边栏'}
        onClick={() => onCollapsedChange(!collapsed)}
      >
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M10 3.5 5.5 8l4.5 4.5" />
        </svg>
      </button>
      <div className="sidebar-bottom">
        <div className={`status${activeAccount ? ' is-active' : ''}`}>
          <span>当前生效账号</span>
          <strong>{activeAccount?.name || '未配置账号'}</strong>
        </div>
        <small>Switch Codex v{__APP_VERSION__}</small>
      </div>
    </aside>
  );
}
