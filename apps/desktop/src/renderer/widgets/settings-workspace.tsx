import { useCallback, useEffect, useState } from 'react';
import { Database, FolderOpen, RefreshCw, Save, Settings2 } from 'lucide-react';
import type { Bridge, Methods, MigrationPreview, Settings, Theme } from '@relaydock/protocol';
import { Button, Card, DetailList, Empty, Field, Notice, PaneHeading, faultText, useAction, useConfirmation, useLocalQuery } from './ui';

const themeLabel: Record<Theme, string> = { dark: '深色', light: '浅色', system: '跟随系统' };
const numberFields = [
  { key: 'connectionTimeoutSeconds', label: '连接超时（秒）', min: 1, max: 300 },
  { key: 'retryCount', label: '重试次数', min: 0, max: 10 },
  { key: 'retryIntervalSeconds', label: '重试间隔（秒）', min: 0, max: 300 },
  { key: 'maxConcurrentHosts', label: '并发主机上限', min: 1, max: 32 },
  { key: 'cacheLimitMiB', label: '缓存上限（MiB）', min: 1, max: 1024 * 1024 },
] as const;
type NumberKey = typeof numberFields[number]['key'];
type Update = Methods['settings.updateCheck'][1];
type ImportResult = Methods['migration.apply'][1];

function editableNumbers(settings: Settings): Record<NumberKey, string> {
  return { connectionTimeoutSeconds: String(settings.connectionTimeoutSeconds), retryCount: String(settings.retryCount),
    retryIntervalSeconds: String(settings.retryIntervalSeconds), maxConcurrentHosts: String(settings.maxConcurrentHosts), cacheLimitMiB: String(settings.cacheLimitMiB) };
}
function endpointIssue(raw: string, proxy: boolean) {
  const text = raw.trim();
  if (!text) return '';
  const invalid = '请输入有效地址，不要添加密码、查询参数或 URL 片段。';
  if (/[\\\r\n?#]/.test(text)) return invalid;
  try {
    const address = new URL(proxy || text.includes('://') ? text : `https://${text}`);
    if (!address.hostname || address.password || (!proxy && address.username)) return invalid;
    if (proxy) {
      if (!['http:', 'https:', 'socks5:', 'socks5h:'].includes(address.protocol) || (address.pathname && address.pathname !== '/')) return '代理地址需要 HTTP(S) 或 SOCKS5 协议，不含路径。';
    } else if (address.protocol !== 'http:' && address.protocol !== 'https:') return '地址应使用 HTTP 或 HTTPS。';
  } catch { return invalid; }
  return '';
}

type BusyObserver = { onBusy: (busy: boolean) => void };

function SettingsForm({ api, initial, onTheme, onDirty, onBusy }: { api: Bridge; initial: Settings; onTheme: (theme: Theme) => void; onDirty: (yes: boolean) => void } & BusyObserver) {
  const [saved, setSaved] = useState(initial);
  const [form, setForm] = useState(initial);
  const [numbers, setNumbers] = useState(() => editableNumbers(initial));
  const [repoToken, setRepoToken] = useState('');
  const [proxyPassword, setProxyPassword] = useState('');
  const [clearToken, setClearToken] = useState(false);
  const [clearPassword, setClearPassword] = useState(false);
  const action = useAction();
  const { confirm, dialog } = useConfirmation();
  const dirty = JSON.stringify(saved) !== JSON.stringify(form) || numberFields.some(({ key }) => numbers[key] !== String(saved[key])) || !!repoToken || !!proxyPassword || clearToken || clearPassword;
  const invalidRepo = endpointIssue(form.repositoryUrl, false);
  const invalidUpdate = endpointIssue(form.updateCheckUrl, false);
  const invalidProxy = endpointIssue(form.proxyUrl, true);

  useEffect(() => { onDirty(dirty); }, [dirty, onDirty]);
  useEffect(() => { onBusy(action.busy); }, [action.busy, onBusy]);

  function change<K extends keyof Settings>(key: K, value: Settings[K]) { setForm(old => ({ ...old, [key]: value })); }
  function reset(next: Settings) {
    setSaved(next); setForm(next); setNumbers(editableNumbers(next));
    setRepoToken(''); setProxyPassword(''); setClearToken(false); setClearPassword(false);
  }
  function save() {
    if (invalidRepo || invalidUpdate || invalidProxy || !dirty || action.busy) return;
    const { theme, repositoryUrl, updateCheckUrl, proxyUrl } = form;
    const data: Methods['settings.save'][0] = {
      theme, repositoryUrl: repositoryUrl.trim(), updateCheckUrl: updateCheckUrl.trim(), proxyUrl: proxyUrl.trim(),
      connectionTimeoutSeconds: Number(numbers.connectionTimeoutSeconds), retryCount: Number(numbers.retryCount),
      retryIntervalSeconds: Number(numbers.retryIntervalSeconds), maxConcurrentHosts: Number(numbers.maxConcurrentHosts), cacheLimitMiB: Number(numbers.cacheLimitMiB),
      ...(clearToken ? { repositoryToken: '' } : repoToken ? { repositoryToken: repoToken } : {}),
      ...(clearPassword ? { proxyPassword: '' } : proxyPassword ? { proxyPassword } : {}),
    };
    const perform = () => {
      void action.run(async current => {
        let result: Settings;
        try { result = await api.call('settings.save', data); }
        catch (error) {
          let text = faultText(error);
          for (const secret of [repoToken, proxyPassword]) if (secret) text = text.split(secret).join('[已脱敏]');
          throw new Error(text);
        }
        if (!current()) return;
        reset(result);
        onTheme(result.theme);
        action.setNotice({ tone: 'success', text: '工作台设置已保存。' });
      });
    };
    if (clearToken || clearPassword || (saved.repositoryTokenSet && saved.repositoryUrl !== data.repositoryUrl) || (saved.proxyPasswordSet && saved.proxyUrl !== data.proxyUrl)) {
      confirm({ title: '保存凭据设置的变更？', description: '清除已选凭据或更换服务来源后，原凭据可能不再使用。工作台不会把原凭据发送给新来源。',
        acceptLabel: '确认保存', danger: clearToken || clearPassword, perform });
    } else perform();
  }

  return <form className="rd-w-stack" onSubmit={event => { event.preventDefault(); save(); }}>
    <Card title="外观与连接" subtitle="连接设置用于核心支持的连接和调度步骤，不会自动重放远端写操作。">
      <div className="rd-w-form-grid">
        <Field label="工作台主题"><select value={form.theme} onChange={event => change('theme', event.target.value as Theme)} disabled={action.busy}>
          {Object.entries(themeLabel).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select></Field>
        {numberFields.map(({ key, label, min, max }) => <Field key={key} label={label}><input type="number" min={min} max={max} step={1} required disabled={action.busy}
          value={numbers[key]} onChange={event => { setNumbers(old => ({ ...old, [key]: event.target.value })); }} /></Field>)}
      </div>
    </Card>
    <Card title="仓库、代理与凭据" subtitle="仍可离线启动；只有显式检查更新或操作需要时才使用配置好的服务。">
      <div className="rd-w-form-grid">
        <Field label="仓库地址" hint={invalidRepo || '填写 HTTP(S) 仓库地址；不要在地址中填写凭据。'}><input inputMode="url" autoComplete="off" spellCheck={false} value={form.repositoryUrl} disabled={action.busy}
          onChange={event => change('repositoryUrl', event.target.value)} aria-invalid={!!invalidRepo} maxLength={2048} /></Field>
        <Field label="更新检查地址" hint={invalidUpdate || '留空时使用已保存仓库地址下的 /api/version。'}><input inputMode="url" autoComplete="off" spellCheck={false} value={form.updateCheckUrl} disabled={action.busy}
          onChange={event => change('updateCheckUrl', event.target.value)} aria-invalid={!!invalidUpdate} maxLength={2048} /></Field>
        <Field label="代理地址" hint={invalidProxy || '如需代理密码，请使用带用户名的 HTTPS 代理。'}><input inputMode="url" autoComplete="off" spellCheck={false} value={form.proxyUrl} disabled={action.busy}
          onChange={event => change('proxyUrl', event.target.value)} aria-invalid={!!invalidProxy} maxLength={2048} /></Field>
        <Field label="仓库令牌" hint={saved.repositoryTokenSet ? '已配置，不回显；留空表示不主动更换。' : '可选，设置令牌需使用 HTTPS 仓库。'}><input type="password" autoComplete="new-password" value={repoToken} disabled={action.busy || clearToken} maxLength={4096}
          onChange={event => { setRepoToken(event.target.value); }} /></Field>
        <Field label="代理密码" hint={saved.proxyPasswordSet ? '已配置，不回显；留空表示不主动更换。' : '可选，密码与代理 URL 分开存储。'}><input type="password" autoComplete="new-password" value={proxyPassword} disabled={action.busy || clearPassword} maxLength={4096}
          onChange={event => { setProxyPassword(event.target.value); }} /></Field>
      </div>
      <div className="rd-w-actions">
        {saved.repositoryTokenSet && <label className="rd-w-check"><input type="checkbox" checked={clearToken} disabled={action.busy} onChange={event => { setClearToken(event.target.checked); }} />清除仓库令牌</label>}
        {saved.proxyPasswordSet && <label className="rd-w-check"><input type="checkbox" checked={clearPassword} disabled={action.busy} onChange={event => { setClearPassword(event.target.checked); }} />清除代理密码</label>}
      </div>
      <Notice value={action.notice} />
      <div className="rd-w-actions">
        <Button type="submit" variant="primary" disabled={action.busy || !dirty || !!(invalidRepo || invalidUpdate || invalidProxy)}><Save aria-hidden="true" size={16} />{action.busy ? '正在保存' : '保存设置'}</Button>
        <Button disabled={action.busy || !dirty} onClick={() => reset(saved)}>撤销未保存的修改</Button>
      </div>
    </Card>
    {dialog}
  </form>;
}

function Preferences({ api, onTheme, onDirty, onBusy }: { api: Bridge; onTheme: (theme: Theme) => void; onDirty: (value: boolean) => void } & BusyObserver) {
  const load = useCallback(() => api.call('settings.get', {}), [api]);
  const query = useLocalQuery(load);
  const action = useAction();
  const [result, setResult] = useState<Update>();
  function check() {
    void action.run(async current => {
      setResult(undefined);
      const response = await api.call('settings.updateCheck', {});
      if (current()) setResult(response);
    });
  }
  return <>
    {query.pending && <Card title="正在读取设置"><p className="rd-w-subtle" role="status">从本地核心读取工作台偏好…</p></Card>}
    {query.error && <Card title="无法读取设置" actions={<Button onClick={query.refresh}>重新读取</Button>}><Notice value={{ tone: 'error', text: query.error }} /></Card>}
    {query.data && <SettingsForm api={api} initial={query.data} onTheme={onTheme} onDirty={onDirty} onBusy={onBusy} />}
    <Card title="检查应用更新" subtitle="只使用已保存的地址。检查仅显示版本信息，不会下载或安装更新。"
      actions={<Button onClick={check} disabled={action.busy}><RefreshCw aria-hidden="true" size={16} />{action.busy ? '正在检查' : '检查更新'}</Button>}>
      <Notice value={action.notice} />
      {result && <>
        <DetailList items={[
          ['当前核心版本', result.current || '未知'], ['服务器版本', result.latest || '未取得'],
          ['检查结果', <span className={`rd-w-chip${result.available ? ' rd-w-chip--accent' : ''}`}>{result.available ? '有新版本' : result.latest ? '无可用更新' : '未确认'}</span>],
        ]} />
        <Notice value={{ tone: 'info', text: result.message }} />
      </>}
    </Card>
  </>;
}

const changeLabel: Record<MigrationPreview['changes'][number]['kind'], string> = { add: '新增', reuse: '复用', update: '可更新', conflict: '冲突', missing: '源记录缺失' };
const tableLabel: Record<string, string> = { hosts: '主机', tasks: '任务', logs: '日志', install_history: '安装历史', custom_apps: '自定义应用', settings: '设置' };
const PREVIEW_PAGE = 20;
const counts = (data: Record<string, number>) => Object.entries(data).sort(([a], [b]) => a.localeCompare(b)).map(([name, count]) => [tableLabel[name] ?? name, count] as const);

function LegacyImport({ api, onTheme, onImport, onBusy }: { api: Bridge; onTheme: (theme: Theme) => void; onImport?: (() => void) | undefined } & BusyObserver) {
  const action = useAction();
  const { confirm, dialog } = useConfirmation();
  useEffect(() => { onBusy(action.busy); }, [action.busy, onBusy]);
  const [preview, setPreview] = useState<{ name: string; data: MigrationPreview }>();
  const [result, setResult] = useState<ImportResult>();
  const [hostsOnly, setHostsOnly] = useState(true);
  const [acceptUpdates, setAcceptUpdates] = useState(false);
  const [page, setPage] = useState(0);
  function previewDatabase() {
    void action.run(async current => {
      const choice = await api.call('dialog.pick', { purpose: 'migration' });
      if (!choice || !current()) return;
      setPreview(undefined); setResult(undefined); setPage(0); setHostsOnly(true); setAcceptUpdates(false);
      const data = await api.call('migration.preview', { token: choice.token });
      if (current()) setPreview({ name: choice.name, data });
    });
  }
  function apply() {
    if (!preview) return;
    const source = preview;
    const payload = { previewId: source.data.previewId, hostsOnly, acceptUpdates };
    confirm({ title: '把预览结果导入新版？', description: '应用只写自己的数据库，不会修改旧库、连接远端主机或重放历史任务。导入后请核对计数并单独补录凭据。',
      acceptLabel: '确认导入', danger: acceptUpdates, info: [['快照', source.name], ['范围', hostsOnly ? '仅主机元数据' : '所有可迁移记录'], ['更新策略', acceptUpdates ? '接受预览中的可更新项目' : '不接受更新'], ['预览中待补凭据', source.data.needsCredentials]],
      perform: () => {
        void action.run(async current => {
          const applied = await api.call('migration.apply', payload);
          if (!current()) return;
          setResult(applied); setPreview(undefined);
          onImport?.();
          if (!payload.hostsOnly) {
            try {
              const latest = await api.call('settings.get', {});
              if (current()) onTheme(latest.theme);
            } catch (error) {
              if (current()) action.setNotice({ tone: 'info', text: `核心已返回导入报告，但读取导入后的外观设置失败。${faultText(error)}` });
            }
          }
        });
      } });
  }
  const data = preview?.data;
  return <>
    <Card title="旧版 SQLite 导入" subtitle="先选择一致性快照并预览。请退出旧版再制作快照，或使用 SQLite 备份；不要只拷贝仍在写入的数据库文件。"
      actions={<Button variant="primary" onClick={previewDatabase} disabled={action.busy}><FolderOpen aria-hidden="true" size={16} />选择快照并预览</Button>}>
      <Notice value={action.notice} />
      {action.busy && <p className="rd-w-subtle" role="status">核心正在处理旧库快照…</p>}
      {!data && !result && <Empty icon={<Database size={22} />} title="尚未选择快照" description="不会自动查找旧版数据库，也不会显示或导出账号密码。" />}
      {data && preview && <>
        <div className="rd-w-toolbar"><strong>只读预览 · {preview.name}</strong><span className="rd-w-chip rd-w-chip--warning">{data.needsCredentials} 项待补凭据</span></div>
        {!!counts(data.totals).length && <DetailList items={counts(data.totals)} />}
        {data.warnings.length > 0 && <div className="rd-w-subcard"><strong>核心报告的警告</strong><ul className="rd-w-inline-list">{data.warnings.map((warning, index) => <li key={index}>{warning}</li>)}</ul></div>}
        {data.changes.length > 0 && <>
          <div className="rd-w-table-wrap"><table className="rd-w-table">
            <caption className="rd-w-sr">迁移变更预览</caption><thead><tr><th scope="col">数据表</th><th scope="col">记录</th><th scope="col">预计处理</th></tr></thead>
            <tbody>{data.changes.slice(page * PREVIEW_PAGE, (page + 1) * PREVIEW_PAGE).map((item, index) => <tr key={`${item.table}:${item.key}:${index}`}>
              <td>{tableLabel[item.table] ?? item.table}</td><th scope="row">{item.label || item.key}</th><td>{changeLabel[item.kind]}</td>
            </tr>)}</tbody>
          </table></div>
          <nav className="rd-w-page-nav" aria-label="预览记录分页"><Button disabled={page === 0} onClick={() => setPage(old => old - 1)}>上一页</Button>
            <span>第 {page + 1} 页（共 {Math.ceil(data.changes.length / PREVIEW_PAGE)} 页）</span>
            <Button disabled={(page + 1) * PREVIEW_PAGE >= data.changes.length} onClick={() => setPage(old => old + 1)}>下一页</Button></nav>
        </>}
        <div className="rd-w-actions">
          <label className="rd-w-check"><input type="checkbox" checked={hostsOnly} disabled={action.busy} onChange={event => setHostsOnly(event.target.checked)} />仅导入主机元数据</label>
          <label className="rd-w-check"><input type="checkbox" checked={acceptUpdates} disabled={action.busy} onChange={event => setAcceptUpdates(event.target.checked)} />接受已预览的可更新项目</label>
        </div>
        <p className="rd-w-subtle">“接受更新”不等于授权覆盖双边冲突。旧身份验证和主机信任仍需按核心报告逐项核对。</p>
        <div className="rd-w-actions"><Button disabled={action.busy} onClick={() => setPreview(undefined)}>放弃预览</Button>
          <Button variant="primary" disabled={action.busy || !data.previewId} onClick={apply}>核对范围并导入</Button></div>
      </>}
    </Card>
    {result && <Card title="导入报告" subtitle={result.message}>
      <div className="rd-w-form-grid"><section className="rd-w-subcard"><strong>已导入</strong>{counts(result.imported).length ? <DetailList items={counts(result.imported)} /> : <p className="rd-w-subtle">核心未报告新增或更新。</p>}</section>
        <section className="rd-w-subcard"><strong>跳过</strong>{counts(result.skipped).length ? <DetailList items={counts(result.skipped)} /> : <p className="rd-w-subtle">核心未报告跳过项。</p>}</section></div>
      <p className="rd-w-subtle">旧版历史可从任务历史查看；旧主机可能需要重新测试连接并确认指纹。</p>
    </Card>}
    {dialog}
  </>;
}

export function SettingsWorkspace({ api, onTheme, onImport }: { api: Bridge; onTheme: (theme: Theme) => void; onImport?: (() => void) | undefined }) {
  const [section, setSection] = useState<'preferences' | 'migration'>('preferences');
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const { confirm, dialog } = useConfirmation();
  function select(next: typeof section) {
    if (next === section || busy) return;
    if (section === 'preferences' && dirty) {
      confirm({ title: '离开未保存的设置？', description: '尚未保存的偏好和新填凭据不会迁移或写入。', acceptLabel: '放弃并切换', danger: true,
        perform: () => { setDirty(false); setSection(next); } });
    } else setSection(next);
  }
  return <>
    <PaneHeading title="系统设置" description="配置这台工作站的连接与外观，按需检查更新或导入旧版快照。" icon={<Settings2 size={25} />} />
    <div className="rd-w-actions" role="group" aria-label="设置分类">
      <Button aria-pressed={section === 'preferences'} disabled={busy} onClick={() => select('preferences')}>工作台偏好</Button>
      <Button aria-pressed={section === 'migration'} disabled={busy} onClick={() => select('migration')}>旧版数据</Button>
    </div>
    {section === 'preferences' ? <Preferences api={api} onTheme={onTheme} onDirty={setDirty} onBusy={setBusy} /> : <LegacyImport api={api} onTheme={onTheme} onImport={onImport} onBusy={setBusy} />}
    {dialog}
  </>;
}
