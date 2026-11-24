// @vitest-environment node
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'vitest';
import type { CoreEvent } from '@relaydock/protocol';
import {
  AppUpdater,
  compareSemver,
  isAllowedUpdateUrl,
  parseSha256Sums,
  selectReleaseAsset,
} from './updater';

async function withMockServer(
  handler: (reqUrl: string) => { status: number; body: Buffer | string; contentType?: string },
  run: (baseUrl: string) => Promise<void>,
): Promise<void> {
  const server = createServer((req, res) => {
    const reply = handler(req.url ?? '/');
    const buf = Buffer.isBuffer(reply.body) ? reply.body : Buffer.from(reply.body, 'utf8');
    res.writeHead(reply.status, {
      'Content-Type': reply.contentType ?? 'application/octet-stream',
      'Content-Length': String(buf.byteLength),
    });
    res.end(buf);
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()));
  const { port } = server.address() as AddressInfo;
  try {
    await run(`http://127.0.0.1:${port}`);
  } finally {
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
}

async function withTempDir(run: (dir: string) => Promise<void>): Promise<void> {
  const dir = await mkdtemp(join(tmpdir(), 'relaydock-updater-'));
  try {
    await run(dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test('compareSemver, parseSha256Sums, selectReleaseAsset and URL checks enforce architecture and integrity rules', () => {
  assert.ok(compareSemver('v0.1.2', '0.1.1') > 0);
  assert.ok(compareSemver('0.1.2', 'v0.1.2') === 0);
  assert.ok(compareSemver('0.2.0', '0.2.0-rc.1') > 0);
  assert.ok(compareSemver('0.2.0-rc.2', '0.2.0-rc.1') > 0);
  assert.ok(compareSemver('0.1.1', '0.1.2') < 0);

  const hashA = 'a'.repeat(64);
  const hashB = 'b'.repeat(64);
  const parsed = parseSha256Sums(`# comment\n${hashA}  RelayDock-0.1.2-win-x64.exe\n${hashB} *./RelayDock-0.1.2-win-arm64.exe\ninvalid line\n`);
  assert.equal(parsed.get('RelayDock-0.1.2-win-x64.exe'), hashA);
  assert.equal(parsed.get('RelayDock-0.1.2-win-arm64.exe'), hashB);

  const assets = [
    { name: 'RelayDock-0.1.2-win-x64.exe', browser_download_url: 'https://example.invalid/x64.exe', size: 100 },
    { name: 'SHA256SUMS.txt', browser_download_url: 'https://example.invalid/SHA256SUMS.txt', size: 100 },
  ];
  assert.equal(selectReleaseAsset(assets, '0.1.2', 'win32', 'x64').installer?.name, 'RelayDock-0.1.2-win-x64.exe');
  // ARM64 must never fall back to x64.
  assert.equal(selectReleaseAsset(assets, '0.1.2', 'win32', 'arm64').installer, undefined);
  assert.equal(selectReleaseAsset(assets, '0.1.2', 'linux', 'x64').installer, undefined);

  assert.equal(isAllowedUpdateUrl('https://api.github.com/repos/wy32428011/RelayDock/releases/latest', false), true);
  assert.equal(isAllowedUpdateUrl('http://example.invalid/file.exe', false), false);
  assert.equal(isAllowedUpdateUrl('http://127.0.0.1:8080/latest', false), false);
  assert.equal(isAllowedUpdateUrl('http://127.0.0.1:8080/latest', true), true);
});

test('AppUpdater checks, downloads, verifies SHA-256 and detects tampering before install', async () => {
  const payload = Buffer.from('synthetic-windows-installer-binary-content');
  const sha256 = createHash('sha256').update(payload).digest('hex');
  let serveCorrupt = false;
  let releaseTag = 'v0.1.2';

  await withTempDir(async dir => {
    await withMockServer(reqUrl => {
      if (reqUrl === '/releases/latest') {
        return {
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            tag_name: releaseTag,
            name: `RelayDock ${releaseTag}`,
            body: '修复问题并支持在线更新。',
            html_url: 'https://github.com/wy32428011/RelayDock/releases/tag/v0.1.2',
            published_at: '2026-11-24T00:00:00Z',
            assets: [
              { name: 'RelayDock-0.1.2-win-x64.exe', browser_download_url: '__BASE__/assets/RelayDock-0.1.2-win-x64.exe', size: payload.byteLength },
              { name: 'SHA256SUMS.txt', browser_download_url: '__BASE__/assets/SHA256SUMS.txt', size: 120 },
            ],
          }),
        };
      }
      if (reqUrl === '/assets/SHA256SUMS.txt') {
        return { status: 200, contentType: 'text/plain', body: `${sha256}  RelayDock-0.1.2-win-x64.exe\n` };
      }
      if (reqUrl === '/assets/RelayDock-0.1.2-win-x64.exe') {
        return { status: 200, body: serveCorrupt ? Buffer.from('corrupted-bytes') : payload };
      }
      return { status: 404, body: 'not found' };
    }, async baseUrl => {
      const events: CoreEvent[] = [];
      const launched: string[] = [];
      // Patch release asset URLs by wrapping a tiny proxy server with known baseUrl.
      await withMockServer(reqUrl => {
        if (reqUrl === '/releases/latest') {
          return {
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
              tag_name: releaseTag,
              name: `RelayDock ${releaseTag}`,
              body: '修复问题并支持在线更新。',
              html_url: 'https://github.com/wy32428011/RelayDock/releases/tag/v0.1.2',
              published_at: '2026-11-24T00:00:00Z',
              assets: [
                { name: 'RelayDock-0.1.2-win-x64.exe', browser_download_url: `${baseUrl}/assets/RelayDock-0.1.2-win-x64.exe`, size: payload.byteLength },
                { name: 'SHA256SUMS.txt', browser_download_url: `${baseUrl}/assets/SHA256SUMS.txt`, size: 120 },
              ],
            }),
          };
        }
        return { status: 500, body: 'error' };
      }, async apiBase => {
        const updater = new AppUpdater({
          currentVersion: '0.1.1',
          platform: 'win32',
          arch: 'x64',
          updatesDir: join(dir, 'updates'),
          publish: event => events.push(event),
          apiUrl: `${apiBase}/releases/latest`,
          allowLoopbackHttp: true,
          spawnInstaller: path => launched.push(path),
        });

        const checked = await updater.check();
        assert.equal(checked.status, 'available');
        assert.equal(checked.latestVersion, '0.1.2');
        assert.equal(checked.assetName, 'RelayDock-0.1.2-win-x64.exe');

        // Corrupt download must be rejected by SHA-256 verification.
        serveCorrupt = true;
        const failed = await updater.download();
        assert.equal(failed.status, 'error');
        assert.equal(failed.canInstall, false);
        assert.match(failed.message, /SHA-256/u);

        // Valid download passes SHA-256 and can prepare install.
        serveCorrupt = false;
        const downloaded = await updater.download();
        assert.equal(downloaded.status, 'downloaded');
        assert.equal(downloaded.verified, true);
        assert.equal(downloaded.canInstall, true);
        const installerPath = await updater.prepareInstall();
        assert.ok(installerPath.endsWith('RelayDock-0.1.2-win-x64.exe'));
        updater.launchInstaller(installerPath);
        assert.deepEqual(launched, [installerPath]);

        // Tampering after download must be caught by prepareInstall.
        await writeFile(installerPath, 'tampered');
        await assert.rejects(updater.prepareInstall(), /复检失败/u);
        assert.equal(updater.getState().status, 'error');

        // Up-to-date check when release matches current version.
        releaseTag = 'v0.1.1';
        const upToDate = await updater.check();
        assert.equal(upToDate.status, 'up-to-date');
        assert.ok(events.some(event => event.topic === 'update.status'));
      });
    });
  });
});

test('AppUpdater silent check keeps idle state on network errors while manual check reports error', async () => {
  await withTempDir(async dir => {
    await withMockServer(() => ({ status: 503, body: 'unavailable' }), async baseUrl => {
      const updater = new AppUpdater({
        currentVersion: '0.1.1',
        platform: 'win32',
        arch: 'x64',
        updatesDir: join(dir, 'updates'),
        publish: () => undefined,
        apiUrl: `${baseUrl}/releases/latest`,
        allowLoopbackHttp: true,
      });

      const silentResult = await updater.check({ silent: true });
      assert.equal(silentResult.status, 'idle');

      const manualResult = await updater.check({ silent: false });
      assert.equal(manualResult.status, 'error');
      assert.match(manualResult.message, /503/u);
    });
  });
});
