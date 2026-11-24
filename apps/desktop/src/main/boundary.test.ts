// @vitest-environment node
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { test } from 'vitest';
import { MAX_FRAME, VERSION } from '@relaydock/protocol';
import { BridgeError, coreFault } from './faults';
import { FileGrants } from './file-grants';
import { corePaths, rendererRequestAllowed, userDataPath } from './paths';
import { FrameReader, encodeRequest } from './protocol';
import type { CoreFrame } from './protocol';
import { isMethod, validate } from './validation';

const code = (expected: string) => (error: unknown) => error instanceof BridgeError && error.fault.code === expected;

async function fixture<T>(use: (path: string) => Promise<T>): Promise<T> {
  const root = await mkdtemp(join(tmpdir(), 'relaydock-smoke-边界-'));
  try {
    return await use(await realpath(root));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

test('NDJSON supports split UTF-8 and rejects overlong, invalid or unfinished frames', () => {
  const reader = new FrameReader();
  const read: CoreFrame[] = [];
  const event = { v: VERSION, type: 'event', topic: 'task.log', taskId: 'demo', seq: 1, data: '已就绪' };
  const response = { v: VERSION, type: 'response', id: 'r1', ok: true, result: { ok: true } };
  const bytes = Buffer.from(`${JSON.stringify(event)}\n${JSON.stringify(response)}\n`);
  const split = bytes.indexOf(Buffer.from('已')) + 1;
  reader.push(bytes.subarray(0, split), frame => read.push(frame));
  assert.equal(read.length, 0);
  reader.push(bytes.subarray(split), frame => read.push(frame));
  reader.finish();
  assert.deepEqual(read, [event, { type: 'response', id: 'r1', ok: true, result: { ok: true } }]);
  assert.throws(() => new FrameReader().push(Buffer.alloc(MAX_FRAME), () => undefined), code('FRAME_LIMIT'));
  assert.throws(() => encodeRequest('r1', 'files.write', { content: '测'.repeat(MAX_FRAME / 2) }), code('FRAME_LIMIT'));
  assert.throws(() => new FrameReader().push(Buffer.from('[]\n'), () => undefined), code('BAD_FRAME'));
  assert.throws(() => new FrameReader().push(Buffer.from([0xff, 0x0a]), () => undefined), code('BAD_FRAME'));
  const partial = new FrameReader();
  partial.push(Buffer.from('{}'), () => undefined);
  assert.throws(() => partial.finish(), code('BAD_FRAME'));
});

test('method and argument validation permits optional fields, not local paths or unknown keys', () => {
  assert.equal(isMethod('hello'), false);
  assert.equal(isMethod('dialog.pick'), true);
  assert.equal(isMethod('core.status'), true);
  assert.equal(isMethod('update.check'), true);
  assert.equal(isMethod('update.download'), true);
  assert.equal(isMethod('update.install'), true);
  assert.throws(() => validate('update.check', { extra: 'x' }), code('BAD_PARAMS'));
  const draft = { name: '测试', address: 'local.invalid', port: 22, username: 'fixture', group: '', auth: 'password', keyToken: undefined };
  assert.doesNotThrow(() => validate('host.probe', { host: draft, hostId: undefined }));
  assert.throws(() => validate('host.probe', { host: { ...draft, auth: 'key', keyToken: 'file:///fixture/key' } }), code('BAD_PARAMS'));
  assert.throws(() => validate('host.probe', { host: { ...draft, keyPath: '/fixture/key' } }), code('BAD_PARAMS'));
  assert.throws(() => validate('host.probe', { host: draft, hostId: 'demo' }), code('BAD_PARAMS'));
  assert.throws(() => validate('dialog.pick', { purpose: 'arbitrary' }), code('BAD_PARAMS'));
  assert.throws(() => validate('core.status', { extra: 'x' }), code('BAD_PARAMS'));
  assert.throws(() => validate('terminal.input', { sessionId: 'demo', bytesB64: Buffer.alloc(16_385).toString('base64') }), code('BAD_PARAMS'));
  assert.doesNotThrow(() => validate('terminal.input', { sessionId: 'demo', bytesB64: Buffer.alloc(16_384).toString('base64') }));
  assert.deepEqual(coreFault({ code: 'AUTH_FAILED', message: 'synthetic secret and local path', retryable: false }).fault, {
    code: 'AUTH_FAILED', message: '身份验证失败，请检查用户名、密码或私钥。', retryable: false,
  });
});

test('renderer access is limited to its own file bundle and test profiles are temporary', () => fixture(async root => {
  const bundle = join(root, 'renderer');
  const entry = join(bundle, 'index.html');
  const css = join(bundle, 'style.css');
  const outside = join(root, 'test-secret.txt');
  await mkdir(bundle);
  await Promise.all([writeFile(entry, ''), writeFile(css, ''), writeFile(outside, 'synthetic')]);
  assert.equal(rendererRequestAllowed(entry, bundle, pathToFileURL(entry).href, 'mainFrame'), true);
  assert.equal(rendererRequestAllowed(entry, bundle, pathToFileURL(css).href, 'stylesheet'), true);
  assert.equal(rendererRequestAllowed(entry, bundle, pathToFileURL(css).href, 'mainFrame'), false);
  assert.equal(rendererRequestAllowed(entry, bundle, pathToFileURL(entry).href, 'subFrame'), false);
  assert.equal(rendererRequestAllowed(entry, bundle, pathToFileURL(outside).href, 'xhr'), false);
  assert.equal(rendererRequestAllowed(entry, bundle, 'https://local.invalid/', 'script'), false);
  assert.equal(rendererRequestAllowed(entry, bundle, 'file://local.invalid/private.txt', 'script'), false);
  assert.ok(userDataPath(root, root));
  assert.throws(() => userDataPath(root, bundle), code('INVALID_TEST_PROFILE'));
  const packaged = corePaths(true, root, { RELAYDOCK_CORE: outside, RELAYDOCK_RESOURCES: outside });
  assert.notEqual(packaged.executable, outside);
  assert.equal(packaged.resources, join(root, 'resources'));
}));

test('file grants expire, bind to a document and purpose, and exports never use another grant', () => fixture(async root => {
  const keyFile = join(root, '选定私钥');
  await writeFile(keyFile, 'synthetic key placeholder');
  const scope = {};
  let now = 0;
  const grants = new FileGrants(() => now);
  const key = await grants.issue(scope, 'key', keyFile);
  assert.equal(key.name, '选定私钥');
  assert.ok(!key.token.includes(keyFile));
  const draft = { host: { auth: 'key', keyToken: key.token } };
  await assert.rejects(grants.translate('host.probe', draft, {}), code('DIALOG_TOKEN_INVALID'));
  await assert.rejects(grants.translate('files.upload', { token: key.token }, scope), code('DIALOG_TOKEN_INVALID'));
  for (let i = 0; i < 2; i++) {
    assert.deepEqual(await grants.translate('host.probe', draft, scope), { host: { auth: 'key', keyToken: keyFile } });
  }
  now += 10 * 60_000;
  await assert.rejects(grants.translate('host.probe', draft, scope), code('DIALOG_TOKEN_INVALID'));

  const resource = await grants.issue(scope, 'resource', root);
  assert.deepEqual(await grants.translate('resource.inspect', { token: resource.token }, scope), { token: root });
  assert.deepEqual(await grants.translate('resource.import', { token: resource.token }, scope), { token: root });
  await assert.rejects(grants.translate('resource.import', { token: resource.token }, scope), code('DIALOG_TOKEN_INVALID'));

  const destination = join(root, '日志.txt');
  const download = await grants.issue(scope, 'download', destination);
  await assert.rejects(grants.export(scope, download.token, 'test', () => true), code('DIALOG_TOKEN_INVALID'));
  const output = await grants.issue(scope, 'export', destination);
  await grants.export(scope, output.token, 'password="synthetic secret"\n使用已审核资源', () => true);
  assert.equal(await readFile(destination, 'utf8'), 'password=[已脱敏]\n使用已审核资源');
  await assert.rejects(grants.export(scope, output.token, 'test', () => true), code('DIALOG_TOKEN_INVALID'));
  const replacement = await grants.issue(scope, 'export', destination);
  await writeFile(destination, 'updated elsewhere');
  await assert.rejects(grants.export(scope, replacement.token, 'test', () => true), code('LOCAL_PATH_CHANGED'));
  assert.equal(await readFile(destination, 'utf8'), 'updated elsewhere');
  grants.revoke(scope);
  await assert.rejects(grants.translate('files.download', { token: download.token }, scope), code('DIALOG_TOKEN_INVALID'));
}));
