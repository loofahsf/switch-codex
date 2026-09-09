import { createRoot } from 'react-dom/client';
import AntdApp from 'antd/es/app';
import ConfigProvider from 'antd/es/config-provider';
import zhCN from 'antd/locale/zh_CN';
import App from './App';
import './styles.css';

document.documentElement.classList.add('is-desktop');

createRoot(document.getElementById('root')!).render(
  <ConfigProvider
    locale={zhCN}
    theme={{
      token: {
        colorPrimary: '#0d9488',
        borderRadius: 6,
        fontSize: 12
      }
    }}
  >
    <AntdApp>
      <App />
    </AntdApp>
  </ConfigProvider>
);
