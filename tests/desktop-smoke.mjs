import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, join, resolve } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { _electron as electron, expect } from '@playwright/test';
import { corePath, metadata, root } from '../scripts/build-core.mjs';

const require=createRequire(import.meta.url);
const packaged=process.argv.includes('--packaged');
const archPart=process.arch==='arm64'?'-arm64':'';
const build=resolve(root,'artifacts',`${process.platform}-${process.arch}`);
const defaultPaths={win32:[resolve(build,`win${archPart}-unpacked/RelayDock.exe`)],linux:[resolve(build,`linux${archPart}-unpacked/relaydock`),resolve(build,`linux${archPart}-unpacked/RelayDock`)],darwin:[resolve(build,`mac${archPart}/RelayDock.app/Contents/MacOS/RelayDock`)]};
const executable=packaged?(process.env.RELAYDOCK_SMOKE_EXECUTABLE||defaultPaths[process.platform]?.find(existsSync)):require('electron');
assert.ok(executable && existsSync(executable),'先生成打包后的桌面目录，或提供 RELAYDOCK_SMOKE_EXECUTABLE');

async function comparablePath(path) {
  const absolute = await realpath(path);
  return process.platform === 'win32' ? absolute.toLowerCase() : absolute;
}

function findCorePid(parentPid) {
  const options = { encoding: 'utf8', timeout: 15_000, windowsHide: true };
  let matches;
  if (process.platform === 'win32') {
    const filter = `ParentProcessId = ${parentPid} AND Name = 'installer-core.exe'`;
    const output = execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command',
      `Get-CimInstance Win32_Process -Filter "${filter}" | Select-Object -ExpandProperty ProcessId`], options);
    matches = output.trim().split(/\s+/).filter(Boolean).map(Number);
  } else {
    const output = execFileSync('ps', ['-A', '-o', 'pid=,ppid=,comm='], options);
    matches = output.split('\n').map(line => line.trim().match(/^(\d+)\s+(\d+)\s+(.+)$/))
      .filter(match => match && Number(match[2]) === parentPid && basename(match[3]) === 'installer-core')
      .map(match => Number(match[1]));
  }
  assert.ok(matches.length === 1 && matches[0] > 0, '桌面进程应启动一个 Go 核心');
  return matches[0];
}

const data=await mkdtemp(join(tmpdir(),'relaydock-smoke-'));
const env={...process.env,RELAYDOCK_DATA_DIR:data};
if(!packaged){env.RELAYDOCK_CORE=corePath();env.RELAYDOCK_RESOURCES=resolve(root,'resources');}
let app;
let corePid;
try {
  app=await electron.launch({executablePath:executable,args:packaged?[]:[root],cwd:root,env,timeout:30000});
  const runtime = await app.evaluate(({ app }) => ({
    data: app.getPath('userData'), packaged: app.isPackaged, executable: process.execPath, pid: process.pid,
  }));
  assert.equal(await comparablePath(runtime.data), await comparablePath(data), '必须使用临时数据目录');
  assert.equal(runtime.packaged, packaged);
  assert.equal(await comparablePath(runtime.executable), await comparablePath(executable));
  const window=await app.firstWindow();
  const messages=[];
  const netRequests = [];
  window.on('pageerror', e=>messages.push(e.message));
  window.on('request', request => { if (/^https?:/u.test(request.url())) netRequests.push(request.url()); });
  await expect.poll(()=>window.evaluate(async()=>{
    const bridge=globalThis.relayDock;
    if(!bridge) return false;
    try {return (await bridge.call('core.status',{})).ready;} catch {return false;}
  }),{timeout:25000}).toBe(true);
  corePid = findCorePid(runtime.pid);
  await expect(window.locator('html')).toHaveAttribute('lang', 'zh-CN');
  await expect(window).toHaveTitle(/远程安装工作台/);
  await expect(window.getByRole('heading', { name: '主机工作区' })).toBeVisible();
  const statusBar = window.getByRole('contentinfo', { name: '工作台状态' });
  await expect(statusBar).toContainText('本地核心就绪');
  await expect(statusBar).toContainText(`核心版本 ${metadata.version}`);
  await expect(statusBar).toContainText('协议 v1');
  const result=await window.evaluate(async()=>{
    const api=globalThis.relayDock;
    let blocked=false;
    try{await api.call('test.unknown',{});}catch{blocked=true;}
    const status=await api.call('core.status',{});
    const hosts=await api.call('host.list',{});
    const catalog=await api.call('catalog.list',{});
    return {status,hostCount:hosts.length,catalogCount:catalog.length,blocked,node:typeof globalThis.require,rendererLocation:globalThis.location.protocol,
      parameterLabels: catalog.flatMap(app => app.params.map(field => field.label))};
  });
  assert.equal(result.rendererLocation,'file:');
  assert.equal(result.hostCount,0);assert.equal(result.catalogCount,12);
  assert.equal(result.node,'undefined');assert.equal(result.blocked,true);
  assert.equal(result.status.version, metadata.version);
  assert.ok(result.parameterLabels.length > 0 && result.parameterLabels.every(label => /\p{Script=Han}/u.test(label)), '内置应用的安装参数应有中文标签');

  const panes = [
    ['主机工作区功能', '终端', '远程终端'],
    ['主机工作区功能', '远端文件', '远程文件'],
    ['主机工作区功能', '脚本库', '脚本与配置'],
    ['主机工作区功能', '自定义部署', '自定义部署'],
    ['全局工具', '离线资源', '离线资源库'],
    ['全局工具', '任务历史', '任务历史'],
    ['全局工具', '设置', '系统设置'],
  ];
  for (const [navigation, button, heading] of panes) {
    await window.getByRole('navigation', { name: navigation }).getByRole('button', { name: button, exact: true }).click();
    await expect(window.getByRole('heading', { level: 2, name: heading, exact: true })).toBeVisible();
    if (button === '自定义部署') {
      await window.getByRole('button', { name: '新建', exact: true }).click();
      const kind = window.getByLabel('类型', { exact: true });
      await expect(kind).toHaveValue('GENERIC');
      await expect(kind.getByRole('option', { name: '通用服务', exact: true })).toHaveText('通用服务');
    }
  }
  await window.getByRole('button', { name: '工作区', exact: true }).click();
  await window.getByRole('button',{name:'添加主机',exact:true}).first().click();
  const hostName = window.getByLabel('主机名称');
  await expect(hostName).toBeVisible();
  assert.match(await hostName.evaluate(element => element.validationMessage), /\p{Script=Han}/u, '必填项校验提示应为中文');
  await hostName.fill('只在本窗口的测试文本');
  await window.getByRole('button',{name:'取消',exact:true}).click();
  await expect(window.getByLabel('主机名称')).toHaveCount(0);
  const remaining=await window.evaluate(()=>globalThis.relayDock.call('host.list',{}));
  assert.deepEqual(remaining,[],'取消后不应写入主机');

  const fixtureDirectory = join(data, 'offline-fixture');
  const payload = '合成资源，仅用于测试本地导入';
  const digest = createHash('sha256').update(payload).digest('hex');
  await mkdir(fixtureDirectory);
  await writeFile(join(fixtureDirectory, 'package.tar.gz'), payload);
  await writeFile(join(fixtureDirectory, 'manifest.json'), JSON.stringify({
    schema: 1, appId: 'jdk17', version: '17.0.15_6', os: 'ubuntu', osVersion: '24.04', arch: 'amd64',
    source: '桌面冒烟测试', entrypoint: 'package.tar.gz',
    files: [{ path: 'package.tar.gz', size: Buffer.byteLength(payload), sha256: digest, dependencies: [] }],
  }));
  // Replace only the chooser result. The packaged grant, IPC and import paths remain active.
  await app.evaluate(({ dialog }, selected) => {
    dialog.showOpenDialog = async () => ({ canceled: false, filePaths: [selected] });
  }, fixtureDirectory);
  await window.getByRole('navigation',{name:'全局工具'}).getByRole('button',{name:'离线资源',exact:true}).click();
  await window.getByRole('button',{name:'选择资源目录',exact:true}).click();
  await expect(window.getByRole('region',{name:'资源核对结果'})).toContainText('桌面冒烟测试');
  assert.deepEqual(await window.evaluate(()=>globalThis.relayDock.call('resource.list',{})),[],'预览不应写入资源库');
  await window.getByRole('button',{name:'核对并导入',exact:true}).click();
  await window.getByRole('button',{name:'确认导入',exact:true}).click();
  await expect(window.getByRole('table',{name:'离线资源清单'})).toContainText('17.0.15_6');
  const imported = await window.evaluate(()=>globalThis.relayDock.call('resource.list',{}));
  assert.equal(imported.length,1);
  assert.equal(imported[0].trust,'reviewed');

  const snapshot = join(data, '旧版演练.sqlite');
  const old = new DatabaseSync(snapshot);
  try {
    old.exec(`CREATE TABLE hosts(id TEXT PRIMARY KEY, name TEXT, ip_address TEXT, port INT, username TEXT, auth_type INT, os_type INT);
      INSERT INTO hosts VALUES('old-host', '迁移演练主机', '192.0.2.200', 22, 'tester', 0, 2);`);
  } finally {
    old.close();
  }
  const sourceBytes = await readFile(snapshot);
  await app.evaluate(({ dialog }, selected) => {
    dialog.showOpenDialog = async () => ({ canceled: false, filePaths: [selected] });
  }, snapshot);
  await window.getByRole('navigation', { name: '全局工具' }).getByRole('button', { name: '设置', exact: true }).click();
  await window.getByRole('button', { name: '旧版数据', exact: true }).click();
  await window.getByRole('button', { name: '选择快照并预览' }).click();
  await expect(window.getByRole('table', { name: '迁移变更预览' })).toContainText('迁移演练主机');
  assert.deepEqual(await window.evaluate(() => globalThis.relayDock.call('host.list', {})), []);
  await window.getByRole('button', { name: '核对范围并导入' }).click();
  await window.getByRole('button', { name: '确认导入', exact: true }).click();
  await expect(window.getByRole('heading', { name: '导入报告' })).toBeVisible();
  const hosts = await window.evaluate(() => globalThis.relayDock.call('host.list', {}));
  assert.equal(hosts.length, 1);
  assert.equal(hosts[0].address, '192.0.2.200');
  assert.equal(hosts[0].fingerprint, '');
  assert.equal(hosts[0].credentialReady, false);
  assert.deepEqual(await readFile(snapshot), sourceBytes, '源快照应保持字节级不变');

  await window.getByRole('button',{name:'工作区',exact:true}).click();
  await expect(window.getByRole('heading',{name:'应用目录',exact:true})).toBeVisible();
  const html = window.locator('html');
  const initialTheme = await html.getAttribute('data-theme');
  const switchedTheme = initialTheme === 'dark' ? 'light' : 'dark';
  const themeLabel = { light: '亮色', dark: '暗色' };
  await window.getByRole('button',{name:`切换本窗口为${themeLabel[switchedTheme]}主题`,exact:true}).click();
  await expect(html).toHaveAttribute('data-theme',switchedTheme);
  await window.getByRole('button',{name:`切换本窗口为${themeLabel[initialTheme]}主题`,exact:true}).click();
  await expect(html).toHaveAttribute('data-theme',initialTheme);
  assert.deepEqual(messages,[]);
  assert.deepEqual(netRequests, [], '工作台应从安装目录加载资源');
  if(packaged){
    const output=resolve(root,'artifacts','verification',metadata.version,'workbench.png');
    await mkdir(join(output,'..'),{recursive:true});
    await window.screenshot({path:output,animations:'disabled'});
  }
  console.log(`${packaged?'打包':'本地'}桌面通过：真实 Go ${result.status.version}、12 个应用 ID、隔离 preload、合成资源导入、旧库预览及导入、亮暗主题切换。`);
} finally {
  try {
    if (app) await app.close();
    if (corePid) {
      assert.throws(() => process.kill(corePid, 0), { code: 'ESRCH' }, '退出后 Go 核心仍在运行');
      console.log('桌面退出后 Go 核心已停止。');
    }
  } finally {
    await rm(data, { recursive: true, force: true, maxRetries: 4 });
  }
}
