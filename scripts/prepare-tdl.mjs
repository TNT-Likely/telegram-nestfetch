import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync,
} from 'node:fs';
import { homedir } from 'node:os';
import path from 'node:path';

const version = '0.20.4';
const commit = '9d7d49eef2bcb04da720c26e33598c49c68b9ddd';
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

export function prepareTdl(tauri, target, goOS, goArch) {
  const name = 'tdl-v' + version + '-source.tar.gz';
  const archive = cachedArchive('https://codeload.github.com/iyear/tdl/tar.gz/' + commit,
    name, '072039b13548f6b6e7cc38cc11ddfaeddd310d5797f6af3c44714cb460489b62');
  const resources = path.join(tauri, 'resources', 'tdl');
  copyFileSync(archive, path.join(resources, name));

  // Extract outside the project so the upstream Go workspace and git patch
  // are independent of the application's checkout and modules.
  const source = mkdtempSync(path.join(path.dirname(archive), 'source-'));
  try {
    execFileSync('tar', ['-xzf', archive, '--strip-components', '1', '-C', source]);
    execFileSync('git', ['apply', '--unidiff-zero', path.join(resources, 'qr-login.patch')], { cwd: source });
    execFileSync('git', ['apply', ...['photo-sizes.patch', 'channel-attachments.patch', 'private-preview.patch']
      .map(name => path.join(resources, name))], { cwd: source });
    const stream = path.join(tauri, '..', 'engine', 'preview', 'stream.go');
    const streamPackage = path.join(source, 'internal', 'nestfetchpreview');
    mkdirSync(streamPackage, { recursive: true });
    copyFileSync(stream, path.join(streamPackage, 'stream.go'));
    copyFileSync(stream, path.join(resources, 'nestfetch-preview-stream.go'));
    const flags = '-s -w -X github.com/iyear/tdl/pkg/consts.Version=' + version +
      '-nestfetch.3 -X github.com/iyear/tdl/pkg/consts.Commit=' + commit.slice(0, 7);
    const output = path.join(tauri, 'binaries', 'tdl-' + target + (goOS === 'windows' ? '.exe' : ''));
    execFileSync('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags', flags, '-o', output, '.'], {
      cwd: source, stdio: 'inherit',
      env: { ...process.env, CGO_ENABLED: '0', GOOS: goOS, GOARCH: goArch, GOWORK: path.join(source, 'go.work') },
    });
  } finally {
    rmSync(source, { recursive: true, force: true });
  }
}
