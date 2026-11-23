import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, closeSync, copyFileSync, existsSync, mkdirSync, mkdtempSync,
  openSync, readFileSync, renameSync, rmSync,
} from 'node:fs';
import { homedir } from 'node:os';
import path from 'node:path';

const version = '0.20.4';
const commit = '9d7d49eef2bcb04da720c26e33598c49c68b9ddd';
const artifacts = {
  'aarch64-apple-darwin': ['MacOS_arm64.tar.gz', '7ea79e68c3f46a5dccdbd0356220a3a689cc00d715fbce9dcabde7f18bb99365'],
  'x86_64-apple-darwin': ['MacOS_64bit.tar.gz', 'e9c41d70a706e301666e8973cf08c9cb711ba9c11ed0711c8a113835e2fc4e41'],
  'aarch64-pc-windows-msvc': ['Windows_arm64.zip', '70d933fb511ffc96f22ee0f896e64e2d1921ba108c527fdf5f4a82c66c4b8ec7'],
  'x86_64-pc-windows-msvc': ['Windows_64bit.zip', '3f219779c07a4be628c34491b9910c18a3b0a0ca0b0aa4f283bb83ab33b007c8'],
  'aarch64-unknown-linux-gnu': ['Linux_arm64.tar.gz', 'd51f9f5d018a5dde6e208dbf27f61439aec30fc61b349e1363af5a3a576ec357'],
  'x86_64-unknown-linux-gnu': ['Linux_64bit.tar.gz', 'be251adca3ff96c14fd0417f179e9ff119b8073ed3b3c0ac6fe43ec05ebbda77'],
};

function sha256(file) {
  return createHash('sha256').update(readFileSync(file)).digest('hex');
}

function cachedArchive(url, name, digest) {
  const cacheRoot = process.platform === 'win32' ? process.env.LOCALAPPDATA :
    process.platform === 'darwin' ? path.join(homedir(), 'Library', 'Caches') : process.env.XDG_CACHE_HOME;
  const cache = path.join(cacheRoot || path.join(homedir(), '.cache'), 'tg-nestfetch', 'tdl', version);
  mkdirSync(cache, { recursive: true });
  const cached = path.join(cache, name);
  if (existsSync(cached) && sha256(cached) === digest) return cached;
  const temp = mkdtempSync(path.join(cache, 'download-'));
  try {
    const file = path.join(temp, name);
    execFileSync('curl', ['--fail', '--location', '--proto', '=https', '--proto-redir', '=https',
      '--silent', '--show-error', '--retry', '2', '--connect-timeout', '15', '--max-time', '180',
      '--output', file, url], { stdio: 'inherit' });
    if (sha256(file) !== digest) throw new Error('Unexpected SHA-256 for ' + name);
    renameSync(file, cached);
    return cached;
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
}

export function prepareTdl(tauri, target) {
  const artifact = artifacts[target];
  if (!artifact) throw new Error('No upstream tdl release for ' + target);
  const name = 'tdl_' + artifact[0];
  const archive = cachedArchive('https://github.com/iyear/tdl/releases/download/v' + version + '/' + name, name, artifact[1]);
  const sourceName = 'tdl-v' + version + '-source.tar.gz';
  const source = cachedArchive('https://codeload.github.com/iyear/tdl/tar.gz/' + commit,
    sourceName, '072039b13548f6b6e7cc38cc11ddfaeddd310d5797f6af3c44714cb460489b62');
  copyFileSync(source, path.join(tauri, 'resources', 'tdl', sourceName));

  const windows = target.endsWith('windows-msvc');
  const member = windows ? 'tdl.exe' : 'tdl';
  const binaries = path.join(tauri, 'binaries');
  const temp = mkdtempSync(path.join(binaries, 'extract-'));
  try {
    const file = path.join(temp, member);
    const fd = openSync(file, 'wx', 0o755);
    try {
      // Windows ships bsdtar, which also accepts zip archives. GNU tar does not.
      const useUnzip = windows && process.platform !== 'win32';
      execFileSync(useUnzip ? 'unzip' : 'tar', useUnzip ? ['-p', archive, member] : ['-xOf', archive, member],
        { stdio: ['ignore', fd, 'inherit'] });
    } finally {
      closeSync(fd);
    }
    chmodSync(file, 0o755);
    renameSync(file, path.join(binaries, 'tdl-' + target + (windows ? '.exe' : '')));
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
}
