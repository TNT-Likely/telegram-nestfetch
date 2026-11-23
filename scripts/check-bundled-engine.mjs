import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

const binaryDir = path.resolve(process.argv[2]);
const suffix = process.platform === 'win32' ? '.exe' : '';
const data = mkdtempSync(path.join(tmpdir(), 'nestfetch-package-'));

try {
  const tdl = execFileSync(path.join(binaryDir, 'tdl' + suffix), ['version'], {
    encoding: 'utf8', timeout: 15_000,
  });
  if (!tdl.includes('-nestfetch.')) throw new Error('Expected the bundled NestFetch build of tdl');

  const response = JSON.parse(execFileSync(path.join(binaryDir, 'nestfetch-engine' + suffix),
    ['--data-dir', data, '--download-dir', data], {
      input: JSON.stringify({ id: 1, method: 'snapshot' }) + '\n', encoding: 'utf8', timeout: 15_000,
    }));
  if (response.id !== 1 || response.result?.account?.backend !== 'tdl' ||
      response.result.account.state !== 'disconnected') {
    throw new Error('The packaged engine could not start with its bundled tdl');
  }
} finally {
  rmSync(data, { recursive: true, force: true });
}
