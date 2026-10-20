import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  BookOutlined,
  ArrowRightOutlined,
  SafetyCertificateOutlined,
  CloudServerOutlined,
  DeleteOutlined,
  EditOutlined,
  LockOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  PlusOutlined,
  ReloadOutlined,
  SearchOutlined,
  TeamOutlined,
  UserOutlined,
} from '@ant-design/icons';
import {
  Alert,
  App,
  Avatar,
  Breadcrumb,
  Button,
  Card,
  Col,
  Dropdown,
  Drawer,
  Empty,
  Form,
  Grid,
  Input,
  InputNumber,
  Layout,
  Menu,
  Modal,
  Popconfirm,
  Progress,
  Row,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import type { MenuProps, TableColumnsType } from 'antd';
import { APIError, adminFetch, changePassword, getStoredSession, initializeAPI, login, logout, storeSession } from './api';
import type { AdminUser, AuthSession, UsersResponse } from './types';
import BackupPanel from './BackupPanel';
import { AnimatedMetric, formatBytes } from './presentation';

const { Header, Sider, Content } = Layout;
const { Text, Title } = Typography;

type LoginFields = { email: string; password: string };
type CreateFields = { email: string; password: string; name?: string; useDefaultQuota: boolean; quotaMiB?: number };
type QuotaFields = { useDefaultQuota: boolean; quotaMiB?: number };
type PasswordFields = { password: string; confirmPassword: string };

const formatDate = (value: string | null) =>
  value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '从未登录';

function LoginView({ onAuthenticated }: { onAuthenticated: (session: AuthSession) => Promise<void> }) {
  const { message } = App.useApp();
  const [loading, setLoading] = useState(false);

  const submit = async (values: LoginFields) => {
    setLoading(true);
    try {
      const session = await login(values.email, values.password);
      await onAuthenticated(session);
    } catch (error) {
      storeSession(null);
      message.error(error instanceof APIError && error.status === 403 ? '该账号没有管理员权限' : '邮箱或密码错误');
    } finally {
      setLoading(false);
    }
  };

  return (
    <main className="login-shell">
      <div className="login-art" aria-hidden="true">
        <div className="art-caption"><span className="art-rule" /> YOUR READING SPACE</div>
        <div className="book-scene"><div className="scene-orbit orbit-one" /><div className="scene-orbit orbit-two" /><div className="scene-shelf"><i className="spine spine-one" /><i className="spine spine-two" /><i className="spine spine-three" /><i className="spine spine-four" /><span className="shelf-line" /></div><span className="scene-caption">A place for every story.</span></div>
        <div className="art-copy"><h1>让阅读，<br />拥有自己的空间。</h1><p>在这里，管理你的读者、书籍与每一份珍贵的数据。</p></div>
        <div className="art-footer"><SafetyCertificateOutlined /> 自托管 · 自由掌控</div>
      </div>
      <div className="login-form-area">
      <section className="login-panel" aria-labelledby="login-title">
        <div className="login-brand"><span className="brand-mark"><BookOutlined /></span><span>Readest</span></div>
        <div className="login-eyebrow">欢迎回来</div>
        <Title id="login-title" level={2}>管理员登录</Title>
        <Text type="secondary" className="login-subtitle">登录控制台，照看你的阅读空间。</Text>
        <Form<LoginFields> layout="vertical" requiredMark={false} onFinish={submit} autoComplete="on">
          <Form.Item label="邮箱" name="email" rules={[{ required: true, type: 'email', message: '请输入有效邮箱' }]}>
            <Input prefix={<UserOutlined />} size="large" autoComplete="username" />
          </Form.Item>
          <Form.Item label="密码" name="password" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} size="large" autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" size="large" block loading={loading}>进入控制台 <ArrowRightOutlined /></Button>
        </Form>
        <div className="login-note"><LockOutlined /> 仅限管理员访问</div>
      </section>
      <div className="login-footer">Readest Tiny · 私有阅读空间</div>
      </div>
    </main>
  );
}

function Dashboard({ session, onSignedOut }: { session: AuthSession; onSignedOut: () => void }) {
  const { message } = App.useApp();
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const screens = Grid.useBreakpoint();
  const isMobile = screens.lg === false;
  const [section, setSection] = useState('users');
  const [loading, setLoading] = useState(true);
  const [data, setData] = useState<UsersResponse | null>(null);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [quotaUser, setQuotaUser] = useState<AdminUser | null>(null);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [createForm] = Form.useForm<CreateFields>();
  const [quotaForm] = Form.useForm<QuotaFields>();
  const [passwordForm] = Form.useForm<PasswordFields>();

  const handleAuthFailure = useCallback((error: unknown) => {
    if (error instanceof APIError && (error.status === 401 || error.status === 403)) {
      storeSession(null);
      onSignedOut();
      message.error(error.status === 403 ? '该账号没有管理员权限' : '登录已失效');
      return true;
    }
    return false;
  }, [message, onSignedOut]);

  const loadUsers = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
      if (query) params.set('search', query);
      const response = await adminFetch(`/api/admin/users?${params}`);
      setData((await response.json()) as UsersResponse);
    } catch (error) {
      if (!handleAuthFailure(error)) message.error(error instanceof Error ? error.message : '加载账号失败');
    } finally {
      setLoading(false);
    }
  }, [handleAuthFailure, message, page, pageSize, query]);

  useEffect(() => { void loadUsers(); }, [loadUsers]);

  const createUser = async (values: CreateFields) => {
    setSaving(true);
    try {
      await adminFetch('/api/admin/users', {
        method: 'POST',
        body: JSON.stringify({
          email: values.email,
          password: values.password,
          name: values.name ?? '',
          quotaBytes: values.useDefaultQuota ? null : Math.round((values.quotaMiB ?? 0) * 1024 * 1024),
        }),
      });
      message.success('账号已添加');
      setCreateOpen(false);
      createForm.resetFields();
      setPage(1);
      await loadUsers();
    } catch (error) {
      if (!handleAuthFailure(error)) message.error(error instanceof Error ? error.message : '添加账号失败');
    } finally {
      setSaving(false);
    }
  };

  const updateQuota = async (values: QuotaFields) => {
    if (!quotaUser) return;
    setSaving(true);
    try {
      await adminFetch(`/api/admin/users/${quotaUser.id}/quota`, {
        method: 'PATCH',
        body: JSON.stringify({ quotaBytes: values.useDefaultQuota ? null : Math.round((values.quotaMiB ?? 0) * 1024 * 1024) }),
      });
      message.success('用量限制已更新');
      setQuotaUser(null);
      await loadUsers();
    } catch (error) {
      if (!handleAuthFailure(error)) message.error(error instanceof Error ? error.message : '更新失败');
    } finally {
      setSaving(false);
    }
  };

  const deleteUser = async (user: AdminUser) => {
    try {
      await adminFetch(`/api/admin/users/${user.id}`, { method: 'DELETE' });
      message.success('账号及其数据已删除');
      if (data && data.users.length === 1 && page > 1) setPage(page - 1);
      else await loadUsers();
    } catch (error) {
      if (!handleAuthFailure(error)) message.error(error instanceof Error ? error.message : '删除失败');
    }
  };

  const updatePassword = async (values: PasswordFields) => {
    setSaving(true);
    try {
      await changePassword(values.password);
      message.success('管理员密码已更新');
      setPasswordOpen(false);
      passwordForm.resetFields();
    } catch (error) {
      if (!handleAuthFailure(error)) message.error(error instanceof Error ? error.message : '密码更新失败');
    } finally {
      setSaving(false);
    }
  };

  const openQuota = (user: AdminUser) => {
    setQuotaUser(user);
    quotaForm.setFieldsValue({ useDefaultQuota: user.usesDefaultQuota, quotaMiB: user.quotaBytes / 1024 / 1024 });
  };

  const columns = useMemo<TableColumnsType<AdminUser>>(() => [
    {
      title: '账号', dataIndex: 'email', key: 'account', fixed: 'left', width: 260,
      render: (_, user) => (
        <Space><Avatar className={user.isAdmin ? 'admin-avatar' : ''} icon={<UserOutlined />} />
          <span className="account-cell"><Text strong>{user.name || user.email.split('@')[0]}</Text><Text type="secondary">{user.email}</Text></span>
          {user.isAdmin && <Tag color="green">管理员</Tag>}
        </Space>
      ),
    },
    { title: '图书', dataIndex: 'bookCount', width: 90, render: (value: number) => value.toLocaleString('zh-CN') },
    { title: '文件', dataIndex: 'fileCount', width: 90, render: (value: number) => value.toLocaleString('zh-CN') },
    {
      title: '存储用量', key: 'usage', width: 230,
      render: (_, user) => {
        const percent = user.quotaBytes === 0 ? (user.usageBytes > 0 ? 100 : 0) : Math.min(100, Math.round(user.usageBytes / user.quotaBytes * 100));
        return <div className="usage-cell"><div><Text>{formatBytes(user.usageBytes)}</Text><Text type="secondary"> / {formatBytes(user.quotaBytes)}</Text></div><Progress percent={percent} showInfo={false} size="small" status={percent >= 100 ? 'exception' : 'normal'} /></div>;
      },
    },
    { title: '配额来源', dataIndex: 'usesDefaultQuota', width: 110, render: (value: boolean) => value ? <Tag>默认</Tag> : <Tag color="gold">自定义</Tag> },
    { title: '最近登录', dataIndex: 'lastSignInAt', width: 180, render: (value: string | null) => formatDate(value) },
    { title: '创建时间', dataIndex: 'createdAt', width: 180, render: (value: string) => formatDate(value) },
    {
      title: '操作', key: 'actions', fixed: 'right', width: 106,
      render: (_, user) => <Space size={4}>
        <Tooltip title="调整配额"><Button type="text" aria-label="调整配额" icon={<EditOutlined />} onClick={() => openQuota(user)} /></Tooltip>
        {user.isAdmin ? (
          <Tooltip title="管理员账号不可删除"><Button type="text" danger disabled icon={<DeleteOutlined />} /></Tooltip>
        ) : (
          <Popconfirm title="删除账号" description="账号、同步数据和存储文件将被永久删除。" okText="删除" cancelText="取消" okButtonProps={{ danger: true }} onConfirm={() => deleteUser(user)}>
            <Button type="text" danger aria-label="删除账号" icon={<DeleteOutlined />} />
          </Popconfirm>
        )}
      </Space>,
    },
  ], [data, page]);

  const accountMenu: MenuProps['items'] = [
    { key: 'password', icon: <LockOutlined />, label: '修改密码' },
    { type: 'divider' },
    { key: 'logout', icon: <LogoutOutlined />, label: '退出登录', danger: true },
  ];

  const menuClick: MenuProps['onClick'] = async ({ key }) => {
    if (key === 'password') setPasswordOpen(true);
    if (key === 'logout') {
      await logout();
      onSignedOut();
    }
  };

  const navigation = <>
    <div className="sider-brand"><span className="brand-mark"><BookOutlined /></span><span className="brand-name">Readest<small>管理控制台</small></span></div>
    <div className="nav-caption">工作空间</div>
    <Menu theme="dark" mode="inline" selectedKeys={[section]} onClick={({ key }) => { setSection(key); setMobileOpen(false); }} items={[{ key: 'users', icon: <TeamOutlined />, label: '账号管理' }, { key: 'backup', icon: <CloudServerOutlined />, label: '备份与恢复' }]} />
    <div className="sider-footer"><span className="sider-footer-icon"><SafetyCertificateOutlined /></span><div><strong>你的数据，由你掌控</strong><span>Readest Tiny · 自托管</span></div></div>
  </>;

  return (
    <Layout className="admin-layout">
      <a className="skip-link" href="#main-content">跳到主要内容</a>
      {!isMobile && <Sider collapsedWidth={80} width={240} collapsed={collapsed} trigger={null} className="admin-sider">{navigation}</Sider>}
      <Drawer title="导航" open={isMobile && mobileOpen} onClose={() => setMobileOpen(false)} placement="left" size={260} className="mobile-navigation">{navigation}</Drawer>
      <Layout className="main-layout">
        <Header className="admin-header">
          <div className="header-navigation"><Button type="text" className="collapse-button" icon={isMobile || collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />} onClick={() => isMobile ? setMobileOpen(true) : setCollapsed(!collapsed)} aria-label="切换导航" aria-expanded={isMobile ? mobileOpen : !collapsed} /><Breadcrumb items={[{ title: '工作空间' }, { title: section === 'backup' ? '备份与恢复' : '账号管理' }]} /></div>
          <Dropdown menu={{ items: accountMenu, onClick: menuClick }} placement="bottomRight" trigger={['click']}>
            <Button type="text" className="account-menu" aria-label="管理员菜单"><Avatar size="small" className="admin-avatar" icon={<UserOutlined />} /><span className="account-email">{session.user.email}</span><span className="header-role">管理员</span></Button>
          </Dropdown>
        </Header>
        <Content className="admin-content" id="main-content" tabIndex={-1}>
          <div key={section} className="page-view">
          {section === 'backup' ? <BackupPanel onSignedOut={onSignedOut} /> : <>
          <div className="content-heading"><div><span className="page-eyebrow">读者与权限</span><Title level={3}>照看每一位读者</Title><Text type="secondary">管理账号与存储配额，让阅读有序发生。</Text></div><Button type="primary" icon={<PlusOutlined />} onClick={() => { createForm.setFieldsValue({ useDefaultQuota: true }); setCreateOpen(true); }}>添加账号</Button></div>
          <Row gutter={[16, 16]} className="summary-row">
            <Col xs={8}><Card className="metric-card"><div className="stat"><span className="stat-icon green"><TeamOutlined /></span><div><Text type="secondary">账号总数</Text><AnimatedMetric value={data?.summary.totalUsers} /></div></div><span className="metric-caption">一起阅读的伙伴</span></Card></Col>
            <Col xs={8}><Card className="metric-card"><div className="stat"><span className="stat-icon amber"><CloudServerOutlined /></span><div><Text type="secondary">存储用量</Text><AnimatedMetric value={data?.summary.totalUsageBytes} format={formatBytes} /></div></div><span className="metric-caption">已保存到此服务器</span></Card></Col>
            <Col xs={8}><Card className="metric-card"><div className="stat"><span className="stat-icon gray"><BookOutlined /></span><div><Text type="secondary">存储文件</Text><AnimatedMetric value={data?.summary.totalFiles} /></div></div><span className="metric-caption">书籍与封面文件</span></Card></Col>
          </Row>
          <section className="table-section">
            <div className="section-heading"><div><Title level={5}>全部账号 <span className="count-badge">{data?.total ?? '—'}</span></Title><Text type="secondary">查看账号用量，按需调整存储配额</Text></div><Tag icon={<LockOutlined />}>仅管理员可管理</Tag></div>
            <div className="table-toolbar">
              <Input value={search} aria-label="搜索邮箱" allowClear prefix={<SearchOutlined aria-hidden />} placeholder="搜索邮箱" onChange={(event) => setSearch(event.target.value)} onPressEnter={() => { setPage(1); setQuery(search.trim()); }} />
              <Space><Button icon={<SearchOutlined aria-hidden />} onClick={() => { setPage(1); setQuery(search.trim()); }}>搜索</Button><Tooltip title="刷新"><Button aria-label="刷新账号列表" icon={<ReloadOutlined />} onClick={() => loadUsers()} /></Tooltip></Space>
            </div>
            <Table<AdminUser>
              rowKey="id" columns={columns} dataSource={data?.users ?? []} loading={loading}
              scroll={{ x: 1280 }} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无账号" /> }}
              pagination={{ current: page, pageSize, total: data?.total ?? 0, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100], showTotal: (total) => `共 ${total} 个账号`, onChange: (nextPage, nextSize) => { setPage(nextSize !== pageSize ? 1 : nextPage); setPageSize(nextSize); } }}
            />
          </section>
          </>}
          </div>
        </Content>
      </Layout>

      <Modal title="添加账号" open={createOpen} confirmLoading={saving} okText="添加" cancelText="取消" onOk={() => createForm.submit()} onCancel={() => setCreateOpen(false)} destroyOnHidden>
        <Form<CreateFields> form={createForm} layout="vertical" requiredMark={false} initialValues={{ useDefaultQuota: true }} onFinish={createUser}>
          <Form.Item label="邮箱" name="email" rules={[{ required: true, type: 'email', message: '请输入有效邮箱' }]}><Input autoComplete="off" /></Form.Item>
          <Form.Item label="显示名称" name="name" rules={[{ max: 120, message: '显示名称过长' }]}><Input maxLength={120} /></Form.Item>
          <Form.Item label="初始密码" name="password" rules={[{ required: true, min: 12, max: 72, message: '密码长度应为 12 至 72 个字符' }]}><Input.Password autoComplete="new-password" /></Form.Item>
          <Form.Item label="使用默认配额" name="useDefaultQuota" valuePropName="checked"><Switch /></Form.Item>
          <Form.Item noStyle shouldUpdate={(previous, current) => previous.useDefaultQuota !== current.useDefaultQuota}>{({ getFieldValue }) => !getFieldValue('useDefaultQuota') && <Form.Item label="存储配额（MiB）" name="quotaMiB" rules={[{ required: true, type: 'number', min: 0, max: 10485760, message: '请输入 0 至 10485760' }]}><InputNumber min={0} max={10485760} precision={0} className="full-width" /></Form.Item>}</Form.Item>
        </Form>
      </Modal>

      <Modal title="调整存储配额" open={Boolean(quotaUser)} confirmLoading={saving} okText="保存" cancelText="取消" onOk={() => quotaForm.submit()} onCancel={() => setQuotaUser(null)} destroyOnHidden>
        {quotaUser && <Alert type="info" showIcon message={quotaUser.email} className="modal-account" />}
        <Form<QuotaFields> form={quotaForm} layout="vertical" requiredMark={false} onFinish={updateQuota}>
          <Form.Item label="使用默认配额" name="useDefaultQuota" valuePropName="checked"><Switch /></Form.Item>
          <Form.Item noStyle shouldUpdate={(previous, current) => previous.useDefaultQuota !== current.useDefaultQuota}>{({ getFieldValue }) => !getFieldValue('useDefaultQuota') && <Form.Item label="存储配额（MiB）" name="quotaMiB" rules={[{ required: true, type: 'number', min: 0, max: 10485760, message: '请输入 0 至 10485760' }]}><InputNumber min={0} max={10485760} precision={0} className="full-width" /></Form.Item>}</Form.Item>
        </Form>
      </Modal>

      <Modal title="修改管理员密码" open={passwordOpen} confirmLoading={saving} okText="保存" cancelText="取消" onOk={() => passwordForm.submit()} onCancel={() => setPasswordOpen(false)} destroyOnHidden>
        <Form<PasswordFields> form={passwordForm} layout="vertical" requiredMark={false} onFinish={updatePassword}>
          <Form.Item label="新密码" name="password" rules={[{ required: true, min: 12, max: 72, message: '密码长度应为 12 至 72 个字符' }]}><Input.Password autoComplete="new-password" /></Form.Item>
          <Form.Item label="确认密码" name="confirmPassword" dependencies={['password']} rules={[{ required: true, message: '请再次输入密码' }, ({ getFieldValue }) => ({ validator(_, value) { return !value || getFieldValue('password') === value ? Promise.resolve() : Promise.reject(new Error('两次输入的密码不一致')); } })]}><Input.Password autoComplete="new-password" /></Form.Item>
        </Form>
      </Modal>
    </Layout>
  );
}

export default function AdminApp() {
  const [ready, setReady] = useState(false);
  const [session, setSession] = useState<AuthSession | null>(getStoredSession());
  const [configurationError, setConfigurationError] = useState('');

  const validateAdmin = useCallback(async (nextSession: AuthSession) => {
    try {
      await adminFetch('/api/admin/session');
      setSession(nextSession);
    } catch (error) {
      storeSession(null);
      setSession(null);
      throw error;
    }
  }, []);

  useEffect(() => {
    void initializeAPI().then(async () => {
      const stored = getStoredSession();
      if (stored) {
        try { await validateAdmin(stored); } catch { /* Login view handles an invalid stored session. */ }
      }
    }).catch((error: unknown) => setConfigurationError(error instanceof Error ? error.message : '服务器连接失败')).finally(() => setReady(true));
  }, [validateAdmin]);

  if (!ready) return <div className="loading-screen"><Spin size="large" /></div>;
  if (configurationError) return <main className="error-screen"><Alert type="error" showIcon message="无法连接服务器" description={configurationError} /></main>;
  if (!session) return <LoginView onAuthenticated={validateAdmin} />;
  return <Dashboard session={session} onSignedOut={() => setSession(null)} />;
}
