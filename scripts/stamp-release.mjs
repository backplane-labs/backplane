// Release tags own artifact versions; source manifests retain development defaults.
import { readdir, readFile, writeFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export async function stampRelease(root, version) {
  if (!/^[01]\.\d+\.\d+$/.test(version)) throw new Error('Expected X.Y.Z (major 0 or 1)');
  const web = resolve(root, 'web');
  const rootManifest = JSON.parse(await readFile(resolve(web, 'package.json'), 'utf8'));
  const paths = [resolve(web, 'package.json'), resolve(root, 'website/package.json')];
  for (const workspace of rootManifest.workspaces) {
    if (workspace.endsWith('/*')) {
      const parent = resolve(web, workspace.slice(0, -2));
      for (const item of await readdir(parent, { withFileTypes: true })) {
        if (item.isDirectory()) paths.push(resolve(parent, item.name, 'package.json'));
      }
    } else {
      paths.push(resolve(web, workspace, 'package.json'));
    }
  }
  const manifests = await Promise.all(paths.map(async (path) => ({
    path, manifest: JSON.parse(await readFile(path, 'utf8')),
  })));
  const publicNames = new Set(manifests.filter(({ path, manifest }) =>
    path.startsWith(resolve(web, 'packages') + '/') && !manifest.private,
  ).map(({ manifest }) => manifest.name));
  if (publicNames.size === 0) throw new Error('No public frontend packages');
  const edits = new Map();
  for (const { path, manifest } of manifests) {
    manifest.version = version;
    for (const group of ['dependencies', 'devDependencies', 'peerDependencies', 'optionalDependencies']) {
      for (const name of Object.keys(manifest[group] ?? {})) {
        if (publicNames.has(name)) manifest[group][name] = version;
      }
    }
    edits.set(path, JSON.stringify(manifest, null, 2) + '\n');
  }
  // Update published documentation examples from the development version.
  // Only release checkouts are stamped; no version-bump commit is required.
  const previous = JSON.parse(await readFile(resolve(root, 'website/package.json'), 'utf8')).version;
  const versionPattern = new RegExp(`(?<![\\d.])${previous.replaceAll('.', '\\.')}(?![\\d.])`, 'g');
  async function docs(directory) {
    for (const item of await readdir(directory, { withFileTypes: true })) {
      const path = resolve(directory, item.name);
      if (item.isDirectory()) await docs(path);
      else if (item.name.endsWith('.md')) {
        const text = await readFile(path, 'utf8');
        const stamped = text.replace(versionPattern, version);
        if (stamped !== text) edits.set(path, stamped);
      }
    }
  }
  await docs(resolve(root, 'docs'));
  await docs(resolve(root, 'website/docs'));
  for (const [path, content] of edits) await writeFile(path, content);
  return publicNames.size;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
  const count = await stampRelease(root, process.argv[2]);
  console.log(`Stamped ${count} frontend packages, workspace consumers and documentation as ${process.argv[2]}`);
}
