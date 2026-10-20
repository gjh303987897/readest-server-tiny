import React from 'react';
import ReactDOM from 'react-dom/client';
import { App as AntdApp, ConfigProvider } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import AdminApp from './AdminApp';
import './styles.css';
import { useReducedMotion } from './presentation';

function AdminRoot() {
  const reduced = useReducedMotion();
  return (
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: '#15765b',
          colorInfo: '#15765b',
          colorSuccess: '#238b61',
          colorWarning: '#b36b18',
          colorError: '#bd3636',
          borderRadius: 10,
          controlHeight: 40,
          colorText: '#233c33',
          colorTextSecondary: '#718078',
          colorBorder: '#dfe7e2',
          colorBgLayout: '#f4f7f5',
          motion: !reduced,
          fontFamily: "Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
        },
        components: {
          Button: { primaryShadow: '0 4px 10px rgba(21, 118, 91, 0.16)', fontWeight: 550 },
          Table: { headerBg: '#f7f9f8', headerColor: '#718078', headerSplitColor: 'transparent', rowHoverBg: '#f3f9f6', cellPaddingBlock: 18 },
          Card: { borderRadiusLG: 16 },
          Modal: { borderRadiusLG: 18 },
          Menu: { darkItemBg: 'transparent', darkItemSelectedBg: '#245747', darkItemHoverBg: '#1c4036', itemHeight: 48, itemBorderRadius: 10 },
        },
      }}
    >
      <AntdApp>
        <AdminApp />
      </AntdApp>
    </ConfigProvider>
  );
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode><AdminRoot /></React.StrictMode>,
);
