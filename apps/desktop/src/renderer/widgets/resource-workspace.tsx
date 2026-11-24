import { useCallback, useState } from 'react';
import { Box, FolderOpen, PackageCheck, RefreshCw } from 'lucide-react';
import type { AppEntry, Bridge, Host, OfflineResource } from '@relaydock/protocol';
import { Button, Card, DetailList, Empty, Field, Notice, PaneHeading, formatBytes, useAction, useConfirmation, useLocalQuery } from './ui';

function architecture(value: string) {
  const arch = value.trim().toLowerCase();
  return arch === 'x64' || arch === 'x86_64' || arch === 'amd64' ? 'amd64' : arch === 'aarch64' || arch === 'arm64' ? 'arm64' : '';
}
function targetOf(value: Pick<Host, 'os' | 'osVersion' | 'arch'>) {
  return [value.os, value.osVersion, value.arch].filter(Boolean).join(' / ') || '未识别目标平台';
}
function matches(resource: OfflineResource, host: Host) {
  const arch = architecture(host.arch);
  return Boolean(arch && host.osVersion) && resource.os === host.os.trim().toLowerCase() && resource.osVersion === host.osVersion && resource.arch === arch;
}
function TrustChip({ trust }: { trust: OfflineResource['trust'] }) {
  return <span className={`rd-w-chip rd-w-chip--${trust === 'signed' ? 'good' : 'warning'}`}>{trust === 'signed' ? '签名已验证' : '本地核验 · 未签名'}</span>;
}

export function ResourceWorkspace({ api, host, catalog }: { api: Bridge; host?: Host | undefined; catalog: AppEntry[] }) {
  const load = useCallback(() => api.call('resource.list', {}), [api]);
  const query = useLocalQuery(load);
  const action = useAction();
  const { confirm, dialog } = useConfirmation();
  const [preview, setPreview] = useState<{ name: string; token: string; record: OfflineResource }>();
  const [filter, setFilter] = useState('');
  const [limitToHost, setLimitToHost] = useState(false);
  const labels = new Map(catalog.map(app => [app.id, app.label]));
  const resources = (query.data ?? []).filter(entry => (!filter || entry.appId === filter) && (!limitToHost || !host || matches(entry, host)));

  function inspect() {
    void action.run(async current => {
      const selected = await api.call('dialog.pick', { purpose: 'resource' });
      if (!selected || !current()) return;
      setPreview(undefined);
      const result = await api.call('resource.inspect', { token: selected.token });
      if (current()) setPreview({ ...selected, record: result });
    });
  }
  function importResource() {
    if (!preview || !preview.record.digest) return;
    const { name, token, record } = preview;
    confirm({ title: '导入这个离线资源？', description: record.trust === 'signed' ? '核心会重新核验所选内容与预览摘要，成功后写入本地资源库，不连接主机。' : '此资源完成了本地清单与文件核验，但未验证发布签名。仅继续导入你确认来源的内容，不会发起安装。',
      acceptLabel: '确认导入', danger: record.trust !== 'signed', info: [
        ['来源目录', name], ['声明来源', record.source], ['应用 / 版本', `${labels.get(record.appId) ?? record.appId} ${record.version}`],
        ['目标平台', targetOf(record)], ['核验摘要', <code>{record.digest}</code>],
      ], perform: () => {
        void action.run(async current => {
          // File grants are single-use for import. A failed attempt must be selected and checked again.
          setPreview(undefined);
          const imported = await api.call('resource.import', { token, digest: record.digest });
          if (!current()) return;
          if (imported.digest !== record.digest) throw new Error('导入返回的摘要与已确认预览不同，请刷新资源库核对。');
          query.refresh();
          action.setNotice({ tone: 'success', text: `资源已写入本地库：${labels.get(imported.appId) ?? imported.appId} ${imported.version}。` });
        });
      } });
  }

  return <>
    <PaneHeading title="离线资源库" description="检查本地资源目录，再按远端系统、版本和 CPU 选择。无匹配资源时不会自动转为在线。" icon={<Box size={25} />} host={host} />
    <Card title="核对并导入" subtitle="选择包含 manifest.json 和所列文件的目录。先完成核验，再决定是否导入。"
      actions={<Button variant={preview ? 'quiet' : 'primary'} onClick={inspect} disabled={action.busy}><FolderOpen aria-hidden="true" size={16} />{preview ? '重新选择目录' : '选择资源目录'}</Button>}>
      <Notice value={action.notice} />
      {action.busy && <p className="rd-w-subtle" role="status">正在操作资源目录，大文件校验可能需要一些时间…</p>}
      {preview && <section className="rd-w-subcard" aria-label="资源核对结果">
        <div className="rd-w-toolbar"><strong>{labels.get(preview.record.appId) ?? preview.record.appId} {preview.record.version}</strong><TrustChip trust={preview.record.trust} /></div>
        <DetailList items={[
          ['本地选择', preview.name], ['声明来源', preview.record.source], ['适用远端', targetOf(preview.record)],
          ['文件 / 体积', `${preview.record.fileCount} 个 · ${formatBytes(preview.record.bytes)}`], ['核验摘要', <code>{preview.record.digest}</code>],
        ]} />
        {host && !!host.osVersion && !!architecture(host.arch) && <p className="rd-w-subtle">{matches(preview.record, host) ? '所选主机的平台元数据相符；安装时仍需独立预检。' : '此资源的平台与所选主机不符，仍可导入供其他目标使用。'}</p>}
        <div className="rd-w-actions"><Button disabled={action.busy} onClick={() => setPreview(undefined)}>放弃预览</Button>
          <Button variant="primary" disabled={action.busy || !preview.record.digest} onClick={importResource}><PackageCheck aria-hidden="true" size={16} />核对并导入</Button></div>
      </section>}
    </Card>
    <Card title="已导入资源" subtitle="平台筛选只比较资源的目标元数据，不代表该安装组合已经真机验证。"
      actions={<Button disabled={query.pending} onClick={query.refresh}><RefreshCw aria-hidden="true" size={16} />刷新列表</Button>}>
      <div className="rd-w-command-row">
        <Field label="按应用浏览"><select value={filter} onChange={event => setFilter(event.target.value)}><option value="">所有应用</option>
          {Array.from(new Set((query.data ?? []).map(item => item.appId))).sort().map(id => <option key={id} value={id}>{labels.get(id) ?? id}</option>)}
        </select></Field>
        {host && <label className="rd-w-check"><input type="checkbox" checked={limitToHost} onChange={event => setLimitToHost(event.target.checked)} />仅看所选主机平台</label>}
      </div>
      <Notice value={query.error ? { tone: 'error', text: query.error } : undefined} />
      {query.pending ? <p className="rd-w-subtle" role="status">正在读取资源库…</p> : query.data && (
        resources.length ? <div className="rd-w-table-wrap"><table className="rd-w-table">
          <caption className="rd-w-sr">离线资源清单</caption><thead><tr><th scope="col">应用 / 版本</th><th scope="col">目标平台</th><th scope="col">体积 / 数量</th><th scope="col">核验状态</th><th scope="col">详情</th></tr></thead>
          <tbody>{resources.map(item => <tr key={item.id}>
            <th scope="row">{labels.get(item.appId) ?? item.appId} · {item.version}</th><td>{targetOf(item)}</td><td>{formatBytes(item.bytes)} / {item.fileCount} 个</td>
            <td><TrustChip trust={item.trust} /></td><td><details><summary>来源与摘要</summary><p>{item.source}</p><code>{item.digest}</code></details></td>
          </tr>)}</tbody>
        </table></div> : <Empty icon={<Box size={22} />} title="没有符合条件的资源" description={limitToHost && host && (!host.osVersion || !architecture(host.arch)) ? '所选主机的系统版本或 CPU 尚未识别。请先测试连接，或关闭此筛选。' : '可改变筛选条件，或先核对并导入适用于所需远端平台的资源。'} />
      )}
    </Card>
    {dialog}
  </>;
}
