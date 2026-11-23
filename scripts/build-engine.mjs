import { execFileSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
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
execFileSync('go', ['build', '-mod=readonly', '-trimpath', '-ldflags=-s -w', '-o', output, '.'], {
  cwd: path.join(root, 'engine'),
  env: { ...process.env, CGO_ENABLED: '0', GOOS: goOS === 'win32' ? 'windows' : goOS, GOARCH: goArch },
  stdio: 'inherit',
});

if (goOS === 'darwin') {
  const resources = path.join(tauri, 'resources');
  mkdirSync(resources, { recursive: true });
  execFileSync('xcrun', ['swiftc', '-O', '-target', arch + '-apple-macosx13.0',
    path.join(root, 'scripts', 'video-frame.swift'), '-o', path.join(resources, 'tg-nestfetch-frame')], {
    cwd: root,
    stdio: 'inherit',
  });
}
