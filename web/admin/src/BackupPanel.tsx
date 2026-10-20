import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Card, Empty, Form, Input, Modal, Segmented, Space, Switch, Table, Tag, Typography } from 'antd';
import { CheckCircleOutlined, CloudOutlined, CloudServerOutlined, CloudUploadOutlined, CloseCircleOutlined, DatabaseOutlined, FileProtectOutlined, LoadingOutlined, LockOutlined, ReloadOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { APIError, adminFetch, storeSession } from './api';
import { formatBytes } from './presentation';

type Provider = 'webdav' | 's3';
type Destination = { provider: Provider; endpoint: string; prefix: string; username?: string; password?: string; hasPassword?: boolean; region?: string; bucket?: string; accessKeyId?: string; secretAccessKey?: string; hasSecretAccessKey?: boolean; pathStyle?: boolean };
type BackupFile = { name: string; size: number; modifiedAt: string };
type Job = { id: string; operation: string; provider: string; name: string; status: string; phase: string; error?: string; safetyBackup?: string; startedAt: string };
type Credentials = { passphrase: string; repeat?: string; adminPassword?: string; confirmation?: string };

const date = (value: string) => !value || value.startsWith('0001') ? '—' : new Date(value).toLocaleString('zh-CN');

export default function BackupPanel({ onSignedOut }: { onSignedOut: () => void }) {
  const { message } = App.useApp();
  const [provider, setProvider] = useState<Provider>('webdav');
  const [destinations, setDestinations] = useState<Partial<Record<Provider, Destination>>>({});
  const [files, setFiles] = useState<BackupFile[]>([]);
  const [job, setJob] = useState<Job | null>(null);
  const [busy, setBusy] = useState(false);
  const [listing, setListing] = useState(false);
  const [listedProvider, setListedProvider] = useState<Provider | null>(null);
  const [maxBytes, setMaxBytes] = useState(0);
  const [action, setAction] = useState<'backup' | BackupFile | null>(null);
  const [configForm] = Form.useForm<Destination>();
  const [credentialsForm] = Form.useForm<Credentials>();
  const restoring = useRef(false);
  const seenJob = useRef('');
  const listRequest = useRef(0);
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
    listRequest.current += 1;
    configForm.resetFields();
    configForm.setFieldsValue({ provider, prefix: 'readest-backups', region: 'us-east-1', pathStyle: true, ...destinations[provider], password: '', secretAccessKey: '' });
    setFiles([]);
    setListedProvider(null);
    setListing(false);
  }, [provider, destinations, configForm]);

  const loadFiles = useCallback(async () => {
    const request = ++listRequest.current;
    setListing(true);
    try {
      const response = await adminFetch(`/api/admin/backup/files?provider=${provider}`);
      const data = await response.json() as { files: BackupFile[] };
      if (request === listRequest.current) { setFiles(data.files); setListedProvider(provider); }
    } catch (error) { if (request === listRequest.current) handleError(error); }
    finally { if (request === listRequest.current) setListing(false); }
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
    <div className="content-heading"><div><span className="page-eyebrow">数据保障</span><Typography.Title level={3}>为阅读留一份安心</Typography.Title><Typography.Text type="secondary">完整备份你的阅读空间，在需要时从容恢复。</Typography.Text></div><Button type="primary" icon={<CloudUploadOutlined aria-hidden />} disabled={!saved || running || busy} onClick={() => openAction('backup')}>立即备份</Button></div>
    {job && <section key={job.id} className={`backup-job job-${job.status}`} role="status" aria-live="polite">
      <div className="job-icon">{running ? <LoadingOutlined spin /> : job.status === 'failed' ? <CloseCircleOutlined /> : <CheckCircleOutlined />}</div>
      <div className="job-content"><div className="job-heading"><Typography.Text strong>{job.operation === 'restore' ? '恢复' : '备份'} · {job.phase}</Typography.Text><Typography.Text type="secondary">{date(job.startedAt)}</Typography.Text></div><Typography.Text type="secondary" className="job-filename">{job.name}</Typography.Text>{job.error && <p>{job.error}</p>}{job.safetyBackup && <p>恢复前安全备份：DATA_DIR/{job.safetyBackup}（使用本次恢复输入的备份密码）</p>}{running && <div className="task-progress" role="progressbar" aria-label="备份或恢复任务进行中"><span /></div>}</div>
    </section>}
    <div className="backup-workspace">
    <Card className="destination-card" title={<Space><CloudServerOutlined />备份目标</Space>} extra={<Tag color={saved ? 'green' : 'default'}>{saved ? '已保存' : '待配置'}</Tag>}>
      <Typography.Paragraph type="secondary">选择存储方式并保存配置，后续操作将使用已保存的目标。</Typography.Paragraph>
      <Segmented<Provider> className="backup-provider" block aria-label="存储方式" value={provider} disabled={running || busy} onChange={next => { listRequest.current += 1; setProvider(next); }} options={[{ value: 'webdav', label: 'WebDAV', icon: <CloudOutlined aria-hidden /> }, { value: 's3', label: 'S3 / S3 兼容', icon: <DatabaseOutlined aria-hidden /> }]} />
      <Form form={configForm} layout="vertical" onFinish={saveConfig} disabled={running || busy}>
        <div key={provider} className="config-fields">
        <Form.Item className="field-wide" name="endpoint" label={provider === 'webdav' ? 'WebDAV 地址' : 'S3 Endpoint（AWS S3 可留空）'} rules={[{ required: provider === 'webdav', message: '请填写 WebDAV 地址' }, { type: 'url', message: '请输入 HTTP(S) 地址' }]}><Input placeholder={provider === 'webdav' ? 'https://dav.example.com/remote.php/dav/files/user/' : 'https://s3.example.com'} /></Form.Item>
        <Form.Item className="field-wide" name="prefix" label="备份目录 / 对象前缀" rules={[{ required: true }]}><Input placeholder="readest-backups" /></Form.Item>
        {provider === 'webdav' ? <>
          <Form.Item name="username" label="用户名"><Input autoComplete="off" /></Form.Item>
          <Form.Item name="password" label="WebDAV 密码" extra={saved?.hasPassword ? '已保存，留空保留原密码' : undefined}><Input.Password autoComplete="new-password" /></Form.Item>
        </> : <>
          <Form.Item name="region" label="区域" rules={[{ required: true }]}><Input placeholder="us-east-1" /></Form.Item>
          <Form.Item name="bucket" label="存储桶" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="accessKeyId" label="Access Key ID" rules={[{ required: true }]}><Input autoComplete="off" /></Form.Item>
          <Form.Item name="secretAccessKey" label="Secret Access Key" rules={[{ required: !saved?.hasSecretAccessKey, message: '请填写访问密钥' }]} extra={saved?.hasSecretAccessKey ? '已保存，留空保留原密钥' : undefined}><Input.Password autoComplete="new-password" /></Form.Item>
          <Form.Item className="field-wide" name="pathStyle" label="使用 Path Style（适用于 MinIO 等兼容服务）" valuePropName="checked"><Switch /></Form.Item>
        </>}
        </div>
        <div className="form-actions"><Space wrap><Button type="primary" htmlType="submit" loading={busy}>保存配置</Button><Button disabled={!saved || running || busy} onClick={() => { void test(); }}>测试连接</Button></Space><Typography.Text type="secondary"><LockOutlined /> 凭据加密保存</Typography.Text></div>
      </Form>
    </Card>
    <aside className="backup-guide">
      <div className="protection-card"><span className="protection-icon"><SafetyCertificateOutlined /></span><Typography.Title level={4}>每一份数据，都值得被守护</Typography.Title><Typography.Paragraph>账号、书籍文件、同步记录和隐私密文，一次完整保存。</Typography.Paragraph><div className="protection-detail"><FileProtectOutlined /><span>独立密码加密</span><Tag>age</Tag></div><div className="protection-detail"><DatabaseOutlined /><span>单次大小上限</span><strong>{maxBytes ? formatBytes(maxBytes) : '—'}</strong></div></div>
      <Card className="backup-tips" title="开始之前">
        <ol className="backup-steps"><li><span>01</span><div><strong>先保存，再备份</strong><p>填写存储目标并测试连接，确认远端可访问。</p></div></li><li><span>02</span><div><strong>保管独立备份密码</strong><p>服务器不保存此密码，遗失后无法恢复。</p></div></li><li><span>03</span><div><strong>恢复前，保留一份当前数据</strong><p>恢复流程会自动生成安全备份，再整体替换数据。</p></div></li></ol>
        <div className="backup-footnote">生成快照与替换数据时会暂停数据请求；远端传输期间可正常使用。请预留足够的本地磁盘空间。</div>
      </Card>
    </aside>
    </div>
    <Card className="backup-files-card" title={<Space>远端备份<span className="count-badge">{listedProvider === provider ? files.length : '—'}</span></Space>} extra={<Button icon={<ReloadOutlined />} loading={listing} disabled={!saved || running || busy} onClick={() => { void loadFiles(); }}>加载列表</Button>}>
      <div className="backup-list-caption"><Typography.Text type="secondary">{provider === 'webdav' ? 'WebDAV' : 'S3'} · {saved?.prefix || '保存目标后，即可查看和恢复远端备份'}</Typography.Text><span className="encryption-label"><LockOutlined /> 加密存储</span></div>
      <Table<BackupFile> rowKey="name" dataSource={files} loading={listing} scroll={{ x: 800 }} pagination={{ pageSize: 10, hideOnSinglePage: true }} locale={{ emptyText: <Empty image={<CloudOutlined className="backup-empty-icon" />} description={<><strong>{!saved ? '先为数据选择一个存储空间' : listedProvider === provider ? '这里还没有备份' : '你的备份，将在这里集合'}</strong><p>{!saved ? '保存上方配置后，即可创建第一份加密备份。' : listedProvider === provider ? '点击“立即备份”，为阅读空间保留一份副本。' : '点击“加载列表”，查看已保存目标中的备份。'}</p></>} /> }} columns={[
        { title: '备份文件', dataIndex: 'name', render: (name: string) => <Space align="start"><span className="file-icon"><FileProtectOutlined /></span><Typography.Text className="backup-file-name">{name}</Typography.Text></Space> },
        { title: '大小', dataIndex: 'size', width: 110, render: formatBytes },
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
