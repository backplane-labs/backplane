// @ts-check
const { themes } = require('prism-react-renderer');
const version = process.env.DOCS_VERSION || `v${require('./package.json').version}`;
/** @type {import('@docusaurus/types').Config} */
module.exports = {
  title: 'Backplane',
  tagline: 'Connect services. Build your platform.',
  favicon: 'img/favicon.svg',
  url: 'https://backplane-labs.github.io',
  baseUrl: '/backplane/',
  organizationName: 'backplane-labs',
  projectName: 'backplane',
  trailingSlash: true,
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  onDuplicateRoutes: 'throw',
  i18n: { defaultLocale: 'en', locales: ['en'] },
  markdown: { format: 'md', mermaid: true, hooks: { onBrokenMarkdownLinks: 'throw' } },
  themes: ['@docusaurus/theme-mermaid', ['@easyops-cn/docusaurus-search-local', {
    hashed: 'filename', language: ['en'], docsRouteBasePath: '/', indexBlog: false,
    highlightSearchTermsOnTargetPage: true,
  }]],
  presets: [['classic', {
    docs: { routeBasePath: '/', sidebarPath: require.resolve('./sidebars.js'), editUrl: 'https://github.com/backplane-labs/backplane/edit/master/website/' },
    blog: false,
    theme: { customCss: require.resolve('./src/css/custom.css') },
    sitemap: { changefreq: 'weekly', priority: 0.5 },
  }]],
  themeConfig: {
    image: 'img/banner.svg',
    metadata: [{ name: 'theme-color', content: '#0f1011' }],
    colorMode: { defaultMode: 'dark', respectPrefersColorScheme: true },
    navbar: {
      title: 'Backplane', logo: { alt: 'Backplane', src: 'img/favicon.svg' },
      items: [
        { type: 'docSidebar', sidebarId: 'docs', label: 'Docs', position: 'left' },
        { to: '/sdk/go/', label: 'SDK', position: 'left' },
        { to: '/reference/api/', label: 'API', position: 'left' },
        { href: `https://github.com/backplane-labs/backplane/releases/tag/${version}`, label: version, position: 'right' },
        { href: 'https://github.com/backplane-labs/backplane', label: 'GitHub', position: 'right' },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        { title: 'Start', items: [{ label: 'Quickstart', to: '/quickstart/' }, { label: 'Architecture', to: '/concepts/architecture/' }, { label: 'Console guide', to: '/guides/console/' }] },
        { title: 'Build', items: [{ label: 'Go SDK', to: '/sdk/go/' }, { label: 'UI modules', to: '/sdk/modules/' }, { label: 'UI kit', to: '/reference/ui-kit/' }] },
        { title: 'Operate', items: [{ label: 'Deployment', to: '/deploying/overview/' }, { label: 'Configuration', to: '/reference/configuration/' }, { label: 'Troubleshooting', to: '/guides/troubleshooting/' }] },
      ],
      copyright: `Backplane ${version} · Copyright © ${new Date().getFullYear()} Gopher-EX · MIT`,
    },
    prism: { theme: themes.github, darkTheme: themes.vsDark, additionalLanguages: ['bash', 'json', 'go', 'yaml', 'protobuf', 'sql', 'docker'] },
    tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 3 },
    mermaid: { theme: { light: 'neutral', dark: 'dark' } },
  },
};
