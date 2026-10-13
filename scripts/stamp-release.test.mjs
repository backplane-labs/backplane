import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm, copyFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { execFileSync } from 'node:child_process';
import { stampRelease } from './stamp-release.mjs';

async function fixture(t) {
  const root = await mkdtemp(resolve(tmpdir(), 'backplane-release-version-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  async function file(path, content) {
    path = resolve(root, path);
    await mkdir(resolve(path, '..'), { recursive: true });
    await writeFile(path, typeof content === 'string' ? content : JSON.stringify(content, null, 2) + '\n');
  }
  await file('web/package.json', { private: true, workspaces: ['packages/*', 'apps/*', 'templates/*', 'tests/fixtures/embedding'] });
  const api = '@gopherex/backplane-api', client = '@gopherex/backplane-client';
  await file('web/packages/api/package.json', { name: api, version: '0.1.1', publishConfig: { registry: 'https://npm.pkg.github.com' }, files: ['dist'] });
  await file('web/packages/client/package.json', { name: client, version: '0.1.1', dependencies: { [api]: '0.1.1', other: '3.0.0' }, peerDependencies: { [api]: '0.1.1' }, publishConfig: { registry: 'https://npm.pkg.github.com' }, files: ['dist'] });
  for (const path of ['apps/console', 'templates/module', 'tests/fixtures/embedding']) {
    await file(`web/${path}/package.json`, { private: true, version: '0.1.1', dependencies: { [client]: '0.1.1' }, devDependencies: { [api]: '0.1.1' }, optionalDependencies: { [api]: '0.1.1' } });
  }
  await file('website/package.json', { private: true, version: '0.1.1', dependencies: { docusaurus: '3.9.2' } });
  await file('web/yarn.lock', '# unchanged lockfile\n');
  await file('docs/releases.md', 'Install @gopherex/backplane-api@0.1.1; historical 0.1.10\n');
  await file('website/docs/sdk/go.md', 'go get github.com/gopherex/backplane@v0.1.1\n');
  return root;
}
const json = async (root, path) => JSON.parse(await readFile(resolve(root, path), 'utf8'));

test('tag version coordinates packages, consumers and documentation without changing external dependencies or lockfiles', async (t) => {
  const root = await fixture(t);
  assert.equal(await stampRelease(root, '0.2.0'), 2);
  const client = await json(root, 'web/packages/client/package.json');
  assert.equal(client.version, '0.2.0');
  assert.equal(client.dependencies['@gopherex/backplane-api'], '0.2.0');
  assert.equal(client.peerDependencies['@gopherex/backplane-api'], '0.2.0');
  assert.equal(client.dependencies.other, '3.0.0');
  for (const path of ['apps/console', 'templates/module', 'tests/fixtures/embedding']) {
    const manifest = await json(root, `web/${path}/package.json`);
    assert.equal(manifest.dependencies['@gopherex/backplane-client'], '0.2.0');
    assert.equal(manifest.devDependencies['@gopherex/backplane-api'], '0.2.0');
    assert.equal(manifest.optionalDependencies['@gopherex/backplane-api'], '0.2.0');
  }
  assert.equal((await json(root, 'website/package.json')).version, '0.2.0');
  assert.match(await readFile(resolve(root, 'website/docs/sdk/go.md'), 'utf8'), /@v0\.2\.0/);
  assert.match(await readFile(resolve(root, 'docs/releases.md'), 'utf8'), /@0\.2\.0; historical 0\.1\.10/);
  assert.equal(await readFile(resolve(root, 'web/yarn.lock'), 'utf8'), '# unchanged lockfile\n');
  const before = await readFile(resolve(root, 'web/packages/client/package.json'), 'utf8');
  await stampRelease(root, '0.2.0');
  assert.equal(await readFile(resolve(root, 'web/packages/client/package.json'), 'utf8'), before);
  // Exercise the actual publication validator in a fresh checkout too.
  await mkdir(resolve(root, 'web/scripts'));
  await copyFile(new URL('../web/scripts/release-packages.mjs', import.meta.url), resolve(root, 'web/scripts/release-packages.mjs'));
  const output = execFileSync(process.execPath, [resolve(root, 'web/scripts/release-packages.mjs'), '0.2.0', resolve(root, 'output'), '--check'], { encoding: 'utf8' });
  assert.match(output, /2 coordinated packages match 0\.2\.0/);
});

test('invalid tag versions fail before modifying the checkout', async (t) => {
  const root = await fixture(t);
  const before = await readFile(resolve(root, 'website/package.json'), 'utf8');
  for (const version of [undefined, 'v0.2.0', '0.2.0-rc.1', '2.0.0', '../0.2.0']) {
    await assert.rejects(stampRelease(root, version), /Expected X.Y.Z/);
    assert.equal(await readFile(resolve(root, 'website/package.json'), 'utf8'), before);
  }
});
