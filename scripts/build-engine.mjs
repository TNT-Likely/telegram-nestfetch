import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = fileURLToPath(new URL('../', import.meta.url));
const triples = {
  darwin: 'apple-darwin',
  win32: 'pc-windows-msvc',
  linux: 'unknown-linux-gnu',
};
const nativeArch = { arm64: 'aarch64', x64: 'x86_64' }[process.arch];
const nativeTarget = [nativeArch, triples[process.platform]].filter(Boolean).join('-');
const target = process.argv[2] || process.env.TAURI_ENV_TARGET_TRIPLE || nativeTarget;
const [arch] = target.split('-');
const goArch = { aarch64: 'arm64', x86_64: 'amd64' }[arch];
const goOS = Object.entries(triples).find(([, suffix]) => target === arch + '-' + suffix)?.[0];
if (!goArch || !goOS || (goOS === 'darwin' && process.platform !== 'darwin')) {
  throw new Error('Unsupported build target: ' + target);
}

const tauri = path.join(root, 'src-tauri');
const binaries = path.join(tauri, 'binaries');
mkdirSync(binaries, { recursive: true });
const output = path.join(binaries, 'nestfetch-engine-' + target + (goOS === 'win32' ? '.exe' : ''));
const engine = path.join(root, 'engine');
const args = ['build', '-mod=readonly', '-trimpath', '-ldflags=-s -w', '-o', output];
const telegramAppFile = process.env.TG_NESTFETCH_TELEGRAM_APP_FILE;
let overlayDir;

try {
  if (telegramAppFile) {
    let credentials;
    try {
      credentials = JSON.parse(readFileSync(telegramAppFile, 'utf8'));
    } catch {
      throw new Error('Could not read the project Telegram credentials JSON.');
    }
    const { apiId, apiHash } = credentials ?? {};
    if (!Number.isInteger(apiId) || apiId < 1 || apiId > 2_147_483_647 ||
        typeof apiHash !== 'string' || !/^[a-f0-9]{32}$/i.test(apiHash)) {
      throw new Error('Project Telegram credentials must contain a numeric apiId and a 32-character apiHash.');
    }

    // The compiler reads the credentials from a private temporary file. They
    // are not written into the checkout, command arguments, or build logs.
    overlayDir = mkdtempSync(path.join(tmpdir(), 'tg-nestfetch-build-'));
    const defaults = path.join(overlayDir, 'telegram_defaults.go');
    const overlay = path.join(overlayDir, 'overlay.json');
    writeFileSync(defaults, 'package main\nvar releaseTelegramConfig = APIConfig{APIID: ' +
      apiId + ', APIHash: ' + JSON.stringify(apiHash.toLowerCase()) + '}\n', { mode: 0o600 });
    writeFileSync(overlay, JSON.stringify({ Replace: { [path.join(engine, 'telegram_defaults.go')]: defaults } }), { mode: 0o600 });
    args.push('-overlay', overlay);
  }
  execFileSync('go', [...args, '.'], {
    cwd: engine,
    env: { ...process.env, CGO_ENABLED: '0', GOOS: goOS === 'win32' ? 'windows' : goOS, GOARCH: goArch },
    stdio: 'inherit',
  });
} finally {
  if (overlayDir) rmSync(overlayDir, { recursive: true, force: true });
}

if (goOS === 'darwin') {
  const resources = path.join(tauri, 'resources');
  mkdirSync(resources, { recursive: true });
  execFileSync('xcrun', ['swiftc', '-O', '-target', arch + '-apple-macosx13.0',
    path.join(root, 'scripts', 'video-frame.swift'), '-o', path.join(resources, 'tg-nestfetch-frame')], {
    cwd: root,
    stdio: 'inherit',
  });
}
