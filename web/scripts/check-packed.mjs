import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { cpSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync, readdirSync, openSync, closeSync, realpathSync, lstatSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, join, basename } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const scratch = mkdtempSync(join(tmpdir(), 'backplane-packed-'));
const logPath = join(scratch, 'acceptance.log'), log = openSync(logPath, 'w');
function run(command, args, cwd, env = {}) { execFileSync(command, args, { cwd, env: { ...process.env, ...env }, stdio: ['ignore', log, log], timeout: 600000 }); }
const resolutions = {}, expected = new Map();
function hashes(directory, prefix = '') {
  return readdirSync(join(directory, prefix), { withFileTypes: true }).flatMap((entry) => {
    const path = join(prefix, entry.name);
    return entry.isDirectory() ? hashes(directory, path) : [[path, createHash('sha256').update(readFileSync(join(directory, path))).digest('hex')]];
  });
}
try {
  mkdirSync(join(scratch, 'packages'));
  for (const name of readdirSync(join(root, 'packages'))) {
    const directory = join(root, 'packages', name), manifest = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'));
    const tar = join(scratch, 'packages', `${name}-${basename(scratch)}.tgz`);
    run('yarn', ['pack', '--ignore-scripts', '--filename', tar], directory);
    resolutions[manifest.name] = `file:${tar}`;
    expected.set(manifest.name, hashes(join(directory, 'dist')));
  }
  console.log(`Packed ${Object.keys(resolutions).length} packages; external consumer: ${scratch}`);
  for (const [name, source] of [['module', 'templates/module'], ['host', 'tests/fixtures/embedding']]) {
    const target = join(scratch, name);
    cpSync(join(root, source), target, { recursive: true, filter: (path) => !['node_modules', 'dist'].includes(basename(path)) });
    const manifestPath = join(target, 'package.json'), manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    manifest.resolutions = resolutions;
    for (const group of ['dependencies', 'devDependencies']) for (const dependency of Object.keys(manifest[group] ?? {})) if (resolutions[dependency]) manifest[group][dependency] = resolutions[dependency];
    // All public exports, including build-only packages, must resolve from tarballs.
    Object.assign(manifest.dependencies, resolutions);
    writeFileSync(manifestPath, JSON.stringify(manifest, null, 2));
    writeFileSync(join(target, '.npmrc'), '@gopherex:registry=https://npm.pkg.github.com\n');
    if (name === 'host') {
      cpSync(join(scratch, 'module/src/gen'), join(target, 'src/gen'), { recursive: true });
      cpSync(join(scratch, 'module/src/fixtures.ts'), join(target, 'src/fixtures.ts'));
      cpSync(join(scratch, 'module/src/standalone.css'), join(target, 'src/standalone.css'));
      const entry = join(target, 'src/main.tsx');
      writeFileSync(entry, readFileSync(entry, 'utf8').replaceAll('../../../../templates/module/src/', './'));
    }
    cpSync(join(root, 'yarn.lock'), join(target, 'yarn.lock'));
    run('yarn', ['install', '--non-interactive'], target);
    for (const dependency of Object.keys(resolutions)) {
      const installed = join(target, 'node_modules', dependency);
      for (const [file, digest] of expected.get(dependency)) {
        if (createHash('sha256').update(readFileSync(join(installed, 'dist', file))).digest('hex') !== digest) throw new Error(`Stale package content: ${dependency}/${file}`);
      }
      if (lstatSync(installed).isSymbolicLink() || realpathSync(installed).startsWith(root)) throw new Error(`Workspace leak: ${dependency}`);
    }
    if (name === 'module') run('yarn', ['typecheck'], target);
    run('yarn', ['build'], target);
    console.log(`${name}: installed from packages and built`);
  }
  run('yarn', ['playwright', 'test', '-c', 'playwright.packed.config.ts'], root, { BACKPLANE_PACKED_DIR: scratch });
  run('yarn', ['playwright', 'test', '-c', 'playwright.packed.config.ts', 'standalone.spec.ts'], root, { BACKPLANE_PACKED_DIR: scratch, BACKPLANE_PACKED_DEV: '1' });
  console.log(`Standalone dev/build and embedded package acceptance passed. Log: ${logPath}`);
} catch (error) {
  console.error(`Package acceptance failed. Inspect ${logPath}`);
  process.exitCode = 1;
} finally { closeSync(log); }
