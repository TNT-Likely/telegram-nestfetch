import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { copyFile, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, join } from 'node:path';

const [inputs, output, repository, tag] = process.argv.slice(2);
const config = JSON.parse(await readFile(new URL('../src-tauri/tauri.conf.json', import.meta.url), 'utf8'));
const { version } = config;
if (!inputs || !output || !/^[\w.-]+\/[\w.-]+$/.test(repository ?? '') || tag !== `v${version}`) {
  throw new Error('Expected an artifact directory, output directory, repository, and the configured version tag.');
}

const name = `TG NestFetch_${version}`;
const downloads = `https://github.com/${repository}/releases/download/${tag}`;
const targets = [
  ['aarch64-apple-darwin', 'darwin-aarch64', 'TG NestFetch.app.tar.gz', `${name}_aarch64.app.tar.gz`, [`${name}_aarch64.dmg`]],
  ['x86_64-apple-darwin', 'darwin-x86_64', 'TG NestFetch.app.tar.gz', `${name}_x64.app.tar.gz`, [`${name}_x64.dmg`]],
  ['x86_64-pc-windows-msvc', 'windows-x86_64', `${name}_x64-setup.exe`, `${name}_x64-setup.exe`, []],
  ['x86_64-unknown-linux-gnu', 'linux-x86_64', `${name}_amd64.AppImage`, `${name}_amd64.AppImage`, [`${name}_amd64.deb`]],
];

async function artifactFile(directory, filenames, name) {
  const matches = filenames.filter(file => basename(file) === name);
  if (matches.length !== 1) throw new Error(`Expected one ${name} in ${directory}.`);
  const file = join(directory, matches[0]);
  const info = await stat(file);
  if (!info.isFile() || info.size === 0) throw new Error(`Empty or invalid artifact: ${file}`);
  return file;
}

function releaseName(name) {
  return name.replace(/^TG NestFetch/, 'TG-NestFetch');
}

await mkdir(output);
const verification = await mkdtemp(join(tmpdir(), 'nestfetch-release-'));
const keyFile = join(verification, 'updater.pub');
const signatureFile = join(verification, 'artifact.minisig');
const platforms = {};
try {
  await writeFile(keyFile, Buffer.from(config.plugins.updater.pubkey, 'base64'));
  for (const [target, platform, bundle, updateName, installers] of targets) {
    const directory = join(inputs, `TG-NestFetch-${target}`);
    const filenames = await readdir(directory, { recursive: true });
    const update = await artifactFile(directory, filenames, bundle);
    const signature = (await readFile(await artifactFile(directory, filenames, `${bundle}.sig`), 'utf8')).trim();
    const decoded = Buffer.from(signature, 'base64').toString('utf8');
    if (!decoded.split(/\r?\n/).some(line => line.startsWith('trusted comment: ') && line.split('\t').at(-1) === `version:${version}`)) {
      throw new Error(`The signature for ${target} is missing the trusted release version.`);
    }
    await writeFile(signatureFile, decoded);
    execFileSync('minisign', ['-q', '-Vm', update, '-p', keyFile, '-x', signatureFile], { stdio: 'inherit' });

    const asset = releaseName(updateName);
    await copyFile(update, join(output, asset));
    await writeFile(join(output, `${asset}.sig`), signature + '\n');
    platforms[platform] = { signature, url: `${downloads}/${asset}` };
    for (const installer of installers) {
      await copyFile(await artifactFile(directory, filenames, installer), join(output, releaseName(installer)));
    }
  }
} finally {
  await rm(verification, { recursive: true, force: true });
}

const manifest = { version, pub_date: new Date().toISOString(), platforms };
await writeFile(join(output, 'latest.json'), JSON.stringify(manifest, null, 2) + '\n');

const checksums = [];
for (const asset of (await readdir(output)).sort()) {
  const digest = createHash('sha256');
  for await (const chunk of createReadStream(join(output, asset))) digest.update(chunk);
  checksums.push(`${digest.digest('hex')}  ${asset}`);
}
await writeFile(join(output, 'SHA256SUMS.txt'), checksums.join('\n') + '\n');
