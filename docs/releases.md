# Releases and containers

`make release` checks a clean working tree and an upstream-synchronized branch,
then creates and pushes a `vX.Y.Z` tag. It does not build, install dependencies or
run tests locally. The release workflow requires successful push CI for that
commit, then builds the deployable artifacts. It does not rerun the full stack
verification suite.

## Version contract

The `vX.Y.Z` tag is the source of truth for the server, container, all 13 public
`@backplane-labs/backplane-*` packages and documentation. No package-version edit or
version-bump commit is required before `make release`.

CI stamps the tag version into its checkout before dependency installation and
build. It updates package versions and internal workspace dependency references
together, including the module template. Documentation build versions and usage
examples receive the same version. The publication job stamps its fresh checkout
as well, then verifies that the package archives match that release. Source
manifest versions are development defaults; they do not choose the release.

Frontend plugin compatibility is currently SDK major **0**. A 1.x release
requires updating and validating the host/module compatibility contract; a
version bump alone does not establish compatibility.

## What a release publishes

| Destination | Artifact |
| --- | --- |
| GitHub Releases | Linux amd64/arm64 archives: `bin/backplane`, `console`, license and launch instructions |
| GitHub Releases | 13 frontend `.tgz` packages and `frontend-packages.json` inventory |
| GitHub Releases | `backplane_X.Y.Z_docs.tar.gz`: static English documentation site |
| GitHub Releases | `SHA256SUMS` and `image-digest.txt` |
| GitHub Pages | Release documentation at https://backplane-labs.github.io/backplane/ |
| GHCR | `ghcr.io/backplane-labs/backplane:X.Y.Z` and `:latest`, both Linux amd64/arm64 |
| GitHub Packages | All 13 `@backplane-labs/backplane-*` packages at the coordinated version |

The multi-platform image is assembled from the same binary and console trees
as the downloadable archives. It runs as non-root and includes trusted CA
certificates. Its defaults set `BACKPLANE_CONSOLE_ASSETS_DIR=/app/console` and
`BACKPLANE_CONSOLE_PREFIX=/backplane`. Deployment supplies credentials and
infrastructure endpoints; see [configuration](../deployments/README.md).

GitHub Release is created after the documentation build and image/package publication succeed.
Pages deploys that release build afterward. Branch and pull-request documentation
checks never publish an unreleased site. A
failed publication can leave some registry artifacts published; rerun that tag
after correcting access. A retry reuses an existing frontend version only if
its integrity matches the archive. Different contents require a new version;
the workflow never deletes published packages to overwrite them.

## Rehearsal

Run **Release → Run workflow** on the intended branch with version `0.1.1`.
It builds archives, packages, documentation and the multi-platform container without pushing
images, publishing packages, deploying Pages or creating a release. Download `release-files` and `release-docs`
from the workflow's artifacts. A rehearsal proves the builds; registry write
permissions are exercised by an actual tagged release.

Local equivalents, after installing dependencies in a disposable checkout
(the artifact build stamps its package manifests and documentation examples):

```sh
bash scripts/build-release.sh 0.1.1
docker buildx build --platform linux/amd64,linux/arm64 \
  -f deployments/Dockerfile.release bin/release
```

The standalone source Dockerfile builds the frontend and server itself:

```sh
docker build --secret id=github_token,env=NODE_AUTH_TOKEN \
  -f deployments/Dockerfile -t backplane:local .
```

Use a package-read token for `NODE_AUTH_TOKEN`. BuildKit mounts it only during
installation; it is not a Docker build argument or included in the runtime
image. Frontend authentication follows the
[GitHub Packages setup](../web/README.md).

## Workflow access

The standard `GITHUB_TOKEN` needs package-read access to existing Gopherex
dependencies, package-write access for GHCR/frontend publication and
contents-write access for release creation. The Pages deployment job uses
`pages: write` and `id-token: write`; configure the repository Pages source as
GitHub Actions and allow version tags in the `github-pages` environment. Grant this repository Actions
access on dependency package settings if needed. Local builds require a token
with package-read access. Published package visibility and organization policy
are controlled in GitHub's package settings.

No publication goes to npmjs.org. The release workflow explicitly uses
`https://npm.pkg.github.com` and rejects a public package manifest configured
with another registry.

## Documentation archive

The archive contains the complete Docusaurus static build, local search index,
Mermaid runtime, fonts and favicon. Serve it under `/backplane/` with a static
web server; no package installation or Node runtime is required. To host at
another base path, change `website/docusaurus.config.js` and rebuild.

Edit guides in `website/docs`; reference pages are synchronized from source
documents by `website/scripts/sync-reference.mjs`. From `website`, run
`yarn install --frozen-lockfile` and `yarn build` to validate links and anchors,
or `yarn start` for development. Release CI stamps the site and its examples from the coordinated tag before
building.
