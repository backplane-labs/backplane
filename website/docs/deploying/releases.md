# Releases and documentation delivery

A coordinated release uses one `vX.Y.Z` tag for the Go server, all thirteen frontend packages and this documentation site. The release workflow builds from the tagged commit.

## Artifacts

- Linux amd64/arm64 archives containing server, production console, license and launch instructions.
- Multi-platform `ghcr.io/backplane-labs/backplane:X.Y.Z` image and immutable digest.
- Thirteen GitHub Packages releases and downloadable `.tgz` files with an integrity inventory.
- `backplane_X.Y.Z_docs.tar.gz`, the complete static documentation site.
- `SHA256SUMS` covering downloadable archives and metadata.

The documentation is deployed to **https://backplane-labs.github.io/backplane/** from release CI. Ordinary branch/PR changes build and validate it without publishing an unreleased site. The header identifies the published release. Older release archives preserve their matching site.

## Maintain documentation

Handwritten guides live in `website/docs`. Authoritative package/API documents remain beside the code and are synchronized into generated reference pages by `website/scripts/sync-reference.mjs`. Edit those sources, not the generated reference directory. API service pages are split from the generated console RPC reference.

```bash
cd website
yarn install --frozen-lockfile
yarn start
yarn build
yarn serve
```

The build checks internal links, document links and anchors. It includes English local search, Mermaid diagrams, shared brand assets and both themes. There is no external search service or analytics credential to configure.

## Publish

Update coordinated versions, commit and push the branch, then run `make release` and choose the patch/minor/major version as appropriate. The command only creates and pushes the tag after clean/upstream checks. It does not run tests or builds locally.

Release CI requires successful commit CI, builds artifacts and documentation, publishes packages/image/Release, then deploys Pages. A manual Release workflow run builds a rehearsal without publication. If publication partially fails, inspect the failed job before retry; an existing frontend version is reusable only when its integrity matches.

See the [release workflow reference](../reference/releases.md) for exact commands and permissions.
