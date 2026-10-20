import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Card, Form, Input, Modal, Select, Space, Switch, Table, Tag, Typography } from 'antd';
import { CloudUploadOutlined, ReloadOutlined } from '@ant-design/icons';
import { APIError, adminFetch, storeSession } from './api';

type Provider = 'webdav' | 's3';
type Destination = { provider: Provider; endpoint: string; prefix: string; username?: string; password?: string; hasPassword?: boolean; region?: string; bucket?: string; accessKeyId?: string; secretAccessKey?: string; hasSecretAccessKey?: boolean; pathStyle?: boolean };
type BackupFile = { name: string; size: number; modifiedAt: string };
type Job = { id: string; operation: string; provider: string; name: string; status: string; phase: string; error?: string; safetyBackup?: string; startedAt: string };
type Credentials = { passphrase: string; repeat?: string; adminPassword?: string; confirmation?: string };

const bytes = (size: number) => size < 1048576 ? `${(size / 1024).toFixed(1)} KiB` : `${(size / 1048576).toFixed(1)} MiB`;
const date = (value: string) => !value || value.startsWith('0001') ? '—' : new Date(value).toLocaleString('zh-CN');

export default function BackupPanel({ onSignedOut }: { onSignedOut: () => void }) {
  const { message } = App.useApp();
  const [provider, setProvider] = useState<Provider>('webdav');
  const [destinations, setDestinations] = useState<Partial<Record<Provider, Destination>>>({});
  const [files, setFiles] = useState<BackupFile[]>([]);
  const [job, setJob] = useState<Job | null>(null);
  const [busy, setBusy] = useState(false);
  const [listing, setListing] = useState(false);
  const [maxBytes, setMaxBytes] = useState(0);
  const [action, setAction] = useState<'backup' | BackupFile | null>(null);
  const [configForm] = Form.useForm<Destination>();
  const [credentialsForm] = Form.useForm<Credentials>();
  const restoring = useRef(false);
  const seenJob = useRef('');
  const running = job?.status === 'running';
  const saved = destinations[provider];

  const handleError = useCallback((error: unknown) => {
    if (error instanceof APIError && (error.status === 401 || error.status === 403)) {
      storeSession(null);
      onSignedOut();
      message.info(restoring.current ? '会话已结束，请使用恢复后的管理员账号重新登录并检查数据。' : '登录已失效，请重新登录');
      return;
    }
    message.error(error instanceof Error ? error.message : '操作失败');
  }, [message, onSignedOut]);

  useEffect(() => {
    let active = true;
    void adminFetch('/api/admin/backup/config').then(r => r.json()).then((data: { destinations: Partial<Record<Provider, Destination>>; maxBytes: number }) => {
      if (active) { setDestinations(data.destinations); setMaxBytes(data.maxBytes); }
    }).catch(e => { if (active) handleError(e); });
    return () => { active = false; };
  }, [handleError]);

  useEffect(() => {
    configForm.resetFields();
    configForm.setFieldsValue({ provider, prefix: 'readest-backups', region: 'us-east-1', pathStyle: true, ...destinations[provider], password: '', secretAccessKey: '' });
    setFiles([]);
  }, [provider, destinations, configForm]);

  const loadFiles = useCallback(async () => {
    setListing(true);
    try {
      const response = await adminFetch(`/api/admin/backup/files?provider=${provider}`);
      const data = await response.json() as { files: BackupFile[] };
      setFiles(data.files);
    } catch (error) { handleError(error); } finally { setListing(false); }
  }, [provider, handleError]);

  useEffect(() => {
    if (job?.status === 'succeeded' && job.provider === provider) void loadFiles();
  }, [job?.id, job?.status, job?.provider, provider, loadFiles]);

  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const response = await adminFetch('/api/admin/backup/status');
        const data = await response.json() as { job: Job | null };
        if (stopped) return;
        setJob(data.job);
        restoring.current = data.job?.operation === 'restore' && data.job.status === 'running';
        if (data.job && data.job.status !== 'running' && seenJob.current !== data.job.id) {
          seenJob.current = data.job.id;
          if (data.job.status === 'succeeded') message.success(data.job.operation === 'backup' ? '备份已上传' : '恢复已完成');
          else message.error(data.job.error || '任务失败');
        }
      } catch (error) { if (!stopped) handleError(error); }
      if (!stopped) timer = setTimeout(() => { void poll(); }, 3000);
    };
    void poll();
    return () => { stopped = true; clearTimeout(timer); };
  }, [handleError, message]);

  const saveConfig = async (values: Destination) => {
    setBusy(true);
    try {
      const response = await adminFetch('/api/admin/backup/config', { method: 'PUT', body: JSON.stringify({ ...values, provider }) });
      const data = await response.json() as Destination;
      setDestinations(previous => ({ ...previous, [provider]: data }));
      message.success('备份目标已保存');
    } catch (error) { handleError(error); } finally { setBusy(false); }
  };

  const test = async () => {
    setBusy(true);
    try {
      const response = await adminFetch('/api/admin/backup/test', { method: 'POST', body: JSON.stringify({ provider }) });
      const data = await response.json() as { message: string };
      message.success(data.message);
    } catch (error) { handleError(error); } finally { setBusy(false); }
  };

  const start = async (values: Credentials) => {
    if (!action) return;
    setBusy(true);
    const isRestore = action !== 'backup';
    try {
      const response = await adminFetch(`/api/admin/backup/${isRestore ? 'restore' : 'create'}`, { method: 'POST', body: JSON.stringify({ provider, name: isRestore ? action.name : undefined, passphrase: values.passphrase, adminPassword: values.adminPassword, confirmation: values.confirmation }) });
      const data = await response.json() as { job: Job };
      restoring.current = isRestore;
      setJob(data.job);
      setAction(null);
      credentialsForm.resetFields();
      message.success('任务已开始，可在下方查看状态');
    } catch (error) { handleError(error); } finally { setBusy(false); }
  };

  const openAction = (next: 'backup' | BackupFile) => { credentialsForm.resetFields(); setAction(next); };

  return <div className="backup-panel">
    <div className="content-heading"><div><Typography.Title level={3}>备份与恢复</Typography.Title><Typography.Text type="secondary">整个服务器的账号、图书文件、同步数据和隐私密文</Typography.Text></div></div>
    <Alert showIcon type="info" title="备份使用独立密码加密，请妥善保管密码。服务器不保存备份密码，遗失后无法恢复。" description={`备份和替换数据期间会暂停数据请求；远端传输期间可正常使用。${maxBytes ? `大小上限 ${bytes(maxBytes)}，本地磁盘需要足够的临时空间。` : ''}`} />
    <Card title="备份目标" extra={<Typography.Text type="secondary">操作使用已保存的配置</Typography.Text>}>
      <Space className="backup-provider"><Typography.Text>存储方式</Typography.Text><Select<Provider> aria-label="存储方式" value={provider} disabled={running || busy} style={{ width: 160 }} onChange={setProvider} options={[{ value: 'webdav', label: 'WebDAV' }, { value: 's3', label: 'S3 / S3 兼容' }]} /></Space>
      <Form form={configForm} layout="vertical" onFinish={saveConfig} disabled={running || busy}>
        <Form.Item name="endpoint" label={provider === 'webdav' ? 'WebDAV 地址' : 'S3 Endpoint（AWS S3 可留空）'} rules={[{ required: provider === 'webdav', message: '请填写 WebDAV 地址' }, { type: 'url', message: '请输入 HTTP(S) 地址' }]}><Input placeholder={provider === 'webdav' ? 'https://dav.example.com/remote.php/dav/files/user/' : 'https://s3.example.com'} /></Form.Item>
        <Form.Item name="prefix" label="备份目录 / 对象前缀" rules={[{ required: true }]}><Input placeholder="readest-backups" /></Form.Item>
        {provider === 'webdav' ? <>
          <Form.Item name="username" label="用户名"><Input autoComplete="off" /></Form.Item>
          <Form.Item name="password" label="WebDAV 密码" extra={saved?.hasPassword ? '已保存，留空保留原密码' : undefined}><Input.Password autoComplete="new-password" /></Form.Item>
        </> : <>
          <Form.Item name="region" label="区域" rules={[{ required: true }]}><Input placeholder="us-east-1" /></Form.Item>
          <Form.Item name="bucket" label="存储桶" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="accessKeyId" label="Access Key ID" rules={[{ required: true }]}><Input autoComplete="off" /></Form.Item>
          <Form.Item name="secretAccessKey" label="Secret Access Key" rules={[{ required: !saved?.hasSecretAccessKey, message: '请填写访问密钥' }]} extra={saved?.hasSecretAccessKey ? '已保存，留空保留原密钥' : undefined}><Input.Password autoComplete="new-password" /></Form.Item>
          <Form.Item name="pathStyle" label="使用 Path Style（适用于 MinIO 等兼容服务）" valuePropName="checked"><Switch /></Form.Item>
        </>}
        <Space wrap><Button type="primary" htmlType="submit" loading={busy}>保存配置</Button><Button disabled={!saved || running || busy} onClick={() => { void test(); }}>测试已保存配置</Button></Space>
      </Form>
    </Card>
    {job && <Alert showIcon type={job.status === 'failed' ? 'error' : job.status === 'running' ? 'info' : 'success'} title={`${job.operation === 'restore' ? '恢复' : '备份'}：${job.phase}`} description={<><div>{job.name} · {date(job.startedAt)}</div>{job.error && <div>{job.error}</div>}{job.safetyBackup && <div>恢复前安全备份：DATA_DIR/{job.safetyBackup}（使用本次恢复输入的备份密码）</div>}</>} />}
    <Card title={`${provider === 'webdav' ? 'WebDAV' : 'S3'} 中的备份`} extra={<Space wrap><Button icon={<ReloadOutlined />} loading={listing} disabled={!saved || running || busy} onClick={() => { void loadFiles(); }}>加载列表</Button><Button type="primary" icon={<CloudUploadOutlined />} disabled={!saved || running || busy} onClick={() => openAction('backup')}>立即备份</Button></Space>}>
      <Table<BackupFile> rowKey="name" dataSource={files} loading={listing} scroll={{ x: 800 }} pagination={{ pageSize: 10 }} locale={{ emptyText: '点击“加载列表”查看已保存目标中的备份' }} columns={[
        { title: '备份文件', dataIndex: 'name', render: (name: string) => <Typography.Text style={{ wordBreak: 'break-all' }}>{name}</Typography.Text> },
        { title: '大小', dataIndex: 'size', width: 110, render: bytes },
        { title: '修改时间', dataIndex: 'modifiedAt', width: 190, render: date },
        { title: '格式', width: 90, render: () => <Tag>加密</Tag> },
        { title: '操作', width: 90, render: (_, file) => <Button danger size="small" disabled={running || busy} onClick={() => openAction(file)}>恢复</Button> },
      ]} />
    </Card>
    <Modal title={action === 'backup' ? '创建全服务器备份' : '从备份整体恢复'} open={action !== null} onCancel={() => { if (!busy) { setAction(null); credentialsForm.resetFields(); } }} okText={action === 'backup' ? '加密并备份' : '确认整体恢复'} cancelText="取消" confirmLoading={busy} okButtonProps={{ danger: action !== 'backup' }} onOk={() => credentialsForm.submit()} destroyOnHidden>
      {action && action !== 'backup' && <Alert type="warning" showIcon title="将整体替换当前服务器数据，所有账号需要重新登录。" description={<>请确认备份中的管理员密码可用。恢复前将保留当前数据的加密安全备份，使用下面输入的同一备份密码。<div style={{ wordBreak: 'break-all' }}>{action.name}</div></>} />}
      <Form form={credentialsForm} layout="vertical" onFinish={start} className="backup-credentials">
        <Form.Item name="passphrase" label={action === 'backup' ? '设置备份密码' : '备份密码'} rules={[{ required: true }, { validator: (_, value: string) => value && new TextEncoder().encode(value).length >= 12 && new TextEncoder().encode(value).length <= 1024 ? Promise.resolve() : Promise.reject(new Error('请输入 12 至 1024 字节的密码')) }]}><Input.Password autoComplete="new-password" /></Form.Item>
        {action === 'backup' ? <Form.Item name="repeat" label="再次输入备份密码" dependencies={['passphrase']} rules={[{ required: true }, ({ getFieldValue }) => ({ validator: (_, value) => value === getFieldValue('passphrase') ? Promise.resolve() : Promise.reject(new Error('两次输入不一致')) })]}><Input.Password autoComplete="new-password" /></Form.Item> : <>
          <Form.Item name="adminPassword" label="当前管理员登录密码" rules={[{ required: true }]}><Input.Password autoComplete="current-password" /></Form.Item>
          <Form.Item name="confirmation" label="输入 RESTORE 确认整体替换" rules={[{ required: true }, { pattern: /^RESTORE$/, message: '请输入 RESTORE' }]}><Input autoComplete="off" /></Form.Item>
        </>}
      </Form>
    </Modal>
  </div>;
}
