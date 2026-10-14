# Backplane documentation

English Docusaurus handbook, published from version tags at
https://backplane-labs.github.io/backplane/.

```sh
yarn install --frozen-lockfile
yarn start
yarn build
yarn serve
```

Guides live in `docs/`. `scripts/sync-reference.mjs` imports authoritative
repository references and brand assets before start/build; generated reference
files and images are ignored. Edit the original source linked by each page.
Broken links, anchors and duplicate routes fail the production build.

`DOCS_VERSION=vX.Y.Z` labels release builds. The package version must match
the coordinated server/frontend tag. The reusable Documentation workflow builds
PR/branch changes without deployment; Release builds an archive and Pages artifact,
then publishes the site after the GitHub Release. No application tests run here.

The static archive expects `/backplane/`. For another hosting prefix, update
`baseUrl` and rebuild. Local search, fonts and Mermaid are bundled; no external
search or font service is needed.
