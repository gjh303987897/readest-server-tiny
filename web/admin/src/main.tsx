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
          colorPrimary: '#5266d4',
          colorInfo: '#5266d4',
          colorSuccess: '#2f8b69',
          colorWarning: '#b36b18',
          colorError: '#bd3636',
          borderRadius: 10,
          controlHeight: 40,
          colorText: '#252a36',
          colorTextSecondary: '#6f7787',
          colorBorder: '#e2e6ee',
          colorBgLayout: '#f6f7f9',
          motion: !reduced,
          fontFamily: "Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
        },
        components: {
          Button: { primaryShadow: '0 3px 8px rgba(54, 70, 154, 0.14)', fontWeight: 550 },
          Table: { headerBg: '#f8f9fb', headerColor: '#6f7787', headerSplitColor: 'transparent', rowHoverBg: '#f6f8fc', cellPaddingBlock: 18 },
          Card: { borderRadiusLG: 16 },
          Modal: { borderRadiusLG: 18 },
          Menu: { darkItemBg: 'transparent', darkItemSelectedBg: '#303646', darkItemHoverBg: '#272c38', itemHeight: 48, itemBorderRadius: 10 },
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
