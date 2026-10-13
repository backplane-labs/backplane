// Authoritative references remain next to the code. The site consumes them on build.
import { readFile, writeFile, mkdir, rm, copyFile } from 'node:fs/promises';
import { dirname, resolve, relative, posix } from 'node:path';
import { fileURLToPath } from 'node:url';
const website = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const root = resolve(website, '..');
const sources = {
  'docs/frontend-contract.md': 'frontend-contract',
  'docs/api-client.md': 'client', 'docs/audit-api.md': 'audit',
  'docs/obs-api.md': 'observability-api', 'docs/errors.md': 'errors',
  'docs/telemetry-admission.md': 'otlp', 'deployments/README.md': 'configuration',
  'docs/ui-kit.md': 'ui-kit', 'docs/ui-components.md': 'components',
  'docs/schema-forms.md': 'schema-forms', 'docs/editors.md': 'editors',
  'docs/charts.md': 'charts', 'docs/observability-ui.md': 'observability-ui',
  'docs/platform-ui.md': 'platform-ui', 'docs/development.md': 'development',
  'docs/releases.md': 'releases', 'examples/README.md': 'examples',
  'web/templates/module/README.md': 'module-template', 'web/README.md': 'frontend-workspace',
  'web/packages/errors/README.md': 'browser-errors', 'docs/api-reference.md': 'api',
};
const routes = Object.fromEntries(Object.entries(sources).map(([source, id]) => [source, `reference/${id}.md`]));
const repository = 'https://github.com/gopherex/backplane/blob/master/';
function links(text, source, target) {
  return text.replace(/\]\(([^)]+)\)/g, (match, href) => {
    if (/^(?:[a-z]+:|#|\/)/i.test(href)) return match;
    const [path, anchor] = href.split('#');
    const file = posix.normalize(posix.join(posix.dirname(source), path));
    const mapped = routes[file] ?? (file.startsWith('website/docs/') ? file.slice('website/docs/'.length) : undefined);
    const fragment = anchor ? `#${anchor}` : '';
    return `](${mapped ? relative(dirname(target), mapped) : repository + file}${fragment})`;
  });
}
const reference = resolve(website, 'docs/reference');
await rm(reference, { recursive: true, force: true });
await mkdir(resolve(reference, 'services'), { recursive: true });
for (const [source, id] of Object.entries(sources)) {
  if (id === 'api') continue;
  const text = await readFile(resolve(root, source), 'utf8');
  const target = `reference/${id}.md`;
  await writeFile(resolve(website, 'docs', target), `---\ncustom_edit_url: ${repository.replace('/blob/', '/edit/') + source}\n---\n\n` + links(text, source, target));
}
const api = await readFile(resolve(root, 'docs/api-reference.md'), 'utf8');
const [intro, ...sections] = api.split(/\n(?=## \w+Service\n)/);
let index = links(intro, 'docs/api-reference.md', 'reference/api.md') + '\n## Services\n\n';
for (const section of sections) {
  const name = section.match(/^## (\w+)/)[1];
  const id = name.replace(/[A-Z]/g, (letter, i) => `${i ? '-' : ''}${letter.toLowerCase()}`);
  const target = `reference/services/${id}.md`;
  const methods = (section.match(/^### /gm) ?? []).length;
  index += `- [${name}](services/${id}.md) — ${methods} RPCs.\n`;
  await writeFile(resolve(website, 'docs', target), `---\ncustom_edit_url: ${repository.replace('/blob/', '/edit/')}docs/api-reference.md\n---\n\n` + links(section.replace(/^## /, '# '), 'docs/api-reference.md', target));
}
await writeFile(resolve(reference, 'api.md'), index);
await mkdir(resolve(website, 'static/img'), { recursive: true });
for (const [source, target] of [
  ['web/apps/console/public/favicon.svg', 'favicon.svg'],
  ['.github/assets/banner.svg', 'banner.svg'], ['.github/assets/console.png', 'console.png'],
]) await copyFile(resolve(root, source), resolve(website, 'static/img', target));
console.log(`Synced ${Object.keys(sources).length} references, ${sections.length} API services and brand assets`);
