import { readdir, readFile, mkdir, writeFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';

const web = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const registry = 'https://npm.pkg.github.com';
const [version, destination, mode] = process.argv.slice(2);
if (!/^(0|1)\.\d+\.\d+$/.test(version ?? '') || !destination || ![undefined, '--check', '--publish'].includes(mode)) {
  throw new Error('Usage: release-packages.mjs X.Y.Z OUTPUT [--check|--publish]');
}
const output = resolve(destination);
const packages = await Promise.all((await readdir(resolve(web, 'packages'))).sort().map(async (dir) => {
  const path = resolve(web, 'packages', dir);
  return { path, manifest: JSON.parse(await readFile(resolve(path, 'package.json'), 'utf8')) };
}));
const names = new Set(packages.map(({ manifest }) => manifest.name));
for (const { manifest } of packages) {
  if (manifest.private || !manifest.name.startsWith('@backplane-labs/backplane-') || manifest.version !== version || manifest.publishConfig?.registry !== registry) {
    throw new Error(`${manifest.name}: public package version and registry must match release ${version} on GitHub Packages`);
  }
  for (const group of ['dependencies', 'devDependencies', 'peerDependencies']) {
    for (const [name, requirement] of Object.entries(manifest[group] ?? {})) {
      if (names.has(name) && requirement !== version) throw new Error(`${manifest.name}: ${name} must use coordinated version ${version}`);
    }
  }
}
if (mode === '--check') {
  console.log(`${packages.length} coordinated packages match ${version}`);
} else if (mode === '--publish') {
  const inventory = JSON.parse(await readFile(resolve(output, 'frontend-packages.json'), 'utf8'));
  if (inventory.version !== version || inventory.packages.length !== packages.length) throw new Error('Release package inventory mismatch');
  if (new Set(inventory.packages.map((entry) => entry.name)).size !== names.size) throw new Error('Duplicate release packages');
  for (const entry of inventory.packages) {
    if (!names.has(entry.name) || !/^[a-z0-9.-]+\.tgz$/.test(entry.filename)) throw new Error('Invalid package inventory');
    const integrity = 'sha512-' + createHash('sha512').update(await readFile(resolve(output, entry.filename))).digest('base64');
    if (integrity !== entry.integrity) throw new Error(`${entry.name}: archive integrity mismatch`);
  }
  // Never silently replace an immutable package when a tag is recreated.
  // A retry can reuse an existing version only if its archive is identical.
  for (const entry of inventory.packages) {
    let existing;
    try {
      existing = JSON.parse(execFileSync('npm', ['view', `${entry.name}@${version}`, 'dist.integrity', '--json', '--registry', registry], { cwd: web, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }));
    } catch (error) {
      let problem; try { problem = JSON.parse(error.stdout); } catch { throw error; }
      if (problem.error?.code !== 'E404') throw error;
    }
    if (existing) {
      if (existing !== entry.integrity) throw new Error(`${entry.name}@${version} already exists with different contents; choose a new version`);
      console.log(`${entry.name}@${version}: identical version already published`);
      continue;
    }
    execFileSync('npm', ['publish', resolve(output, entry.filename), '--ignore-scripts', '--registry', registry], { cwd: web, stdio: 'inherit' });
  }
} else {
  await mkdir(output, { recursive: true });
  const inventory = [];
  for (const { path, manifest } of packages) {
    const [packed] = JSON.parse(execFileSync('npm', ['pack', '--ignore-scripts', '--json', '--pack-destination', output], { cwd: path, encoding: 'utf8' }));
    if (!packed.files.some((file) => file.path.startsWith('dist/'))) throw new Error(`${manifest.name}: built output is missing`);
    inventory.push({ name: manifest.name, filename: packed.filename, integrity: packed.integrity });
  }
  await writeFile(resolve(output, 'frontend-packages.json'), JSON.stringify({ version, registry, packages: inventory }, null, 2) + '\n');
  console.log(`Packed ${inventory.length} packages for ${version}`);
}
