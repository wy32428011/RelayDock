import { FileCode2, FolderOpen, SquareTerminal } from 'lucide-react';
import type { AppEntry, Bridge, Host, Task, Theme } from '@relaydock/protocol';
import { DeployWorkspace } from './deploy-workspace';
import { FileWorkspace } from './file-workspace';
import { HistoryWorkspace } from './history-workspace';
import { ResourceWorkspace } from './resource-workspace';
import { SettingsWorkspace } from './settings-workspace';
import { lazy, Suspense } from 'react';
import { Card, PaneHeading, TargetRequired, canConnect } from './ui';
import './widgets.css';

const TerminalPane = lazy(() => import('./terminal').then(module => ({ default: module.TerminalPane })));

export interface WorkspacePaneProps {
  pane: 'terminal' | 'files' | 'scripts' | 'resources' | 'history' | 'deploy' | 'settings';
  api: Bridge;
  host?: Host;
  tasks: Task[];
  catalog: AppEntry[];
  onTheme: (theme: Theme) => void;
  onImport?: () => void;
}

const remotePanes = {
  terminal: { title: '远程终端', icon: SquareTerminal },
  files: { title: '远程文件', icon: FolderOpen },
  scripts: { title: '脚本与配置', icon: FileCode2 },
};

export function WorkspacePane({ pane, api, host, tasks, catalog, onTheme, onImport }: WorkspacePaneProps) {
  // Re-key remote state even when an existing host is edited in place.
  const hostScope = host ? JSON.stringify([host.id, host.address, host.port, host.username, host.auth, host.fingerprint, host.credentialReady]) : '';
  if ((pane === 'terminal' || pane === 'files' || pane === 'scripts') && !canConnect(host)) {
    const { title, icon: Icon } = remotePanes[pane];
    return <section className="rd-widgets">
      <PaneHeading title={title} description="仅对所选并已验证的目标提供远程操作。" icon={<Icon size={25} />} host={host} />
      <Card title="目标主机"><TargetRequired host={host} /></Card>
    </section>;
  }
  return <section className="rd-widgets">
    {pane === 'terminal' && canConnect(host) && <Suspense fallback={<Card title="远程终端"><p className="rd-w-subtle" role="status">正在加载终端组件…</p></Card>}>
      <TerminalPane key={hostScope} api={api} host={host} />
    </Suspense>}
    {(pane === 'files' || pane === 'scripts') && canConnect(host) && <FileWorkspace key={`${pane}:${hostScope}`} api={api} host={host} mode={pane} />}
    {pane === 'resources' && <ResourceWorkspace api={api} host={host} catalog={catalog} />}
    {pane === 'history' && <HistoryWorkspace api={api} host={host} tasks={tasks} catalog={catalog} />}
    {pane === 'deploy' && <DeployWorkspace key={hostScope} api={api} host={host} />}
    {pane === 'settings' && <SettingsWorkspace api={api} onTheme={onTheme} onImport={onImport} />}
  </section>;
}
