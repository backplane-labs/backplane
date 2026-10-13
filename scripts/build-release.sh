#!/usr/bin/env bash
# Build deployable archives. Dependencies must already be installed; no tests.
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:?usage: build-release.sh X.Y.Z [OUTPUT]}"
[[ "$version" =~ ^[01]\.[0-9]+\.[0-9]+$ ]] || { echo 'Expected X.Y.Z (major 0 or 1)' >&2; exit 1; }
output="$(realpath -m "${2:-bin/release}")"
node web/scripts/release-packages.mjs "$version" "$output" --check
commit="${COMMIT:-$(git rev-parse HEAD)}"
build_time="${BUILD_TIME:-$(git show -s --format=%cI HEAD)}"
mkdir -p "$output"
(cd web && yarn build:packages && yarn workspace @backplane/console build)
node web/scripts/release-packages.mjs "$version" "$output"
for arch in amd64 arm64; do
  tree="$output/linux_$arch"
  mkdir -p "$tree/bin"
  CGO_ENABLED=0 GOWORK=off GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags="-s -w -X github.com/gopherex/backplane/pkg/backplane/build.Service=backplane -X github.com/gopherex/backplane/pkg/backplane/build.Version=$version -X github.com/gopherex/backplane/pkg/backplane/build.Commit=$commit -X github.com/gopherex/backplane/pkg/backplane/build.Date=$build_time" \
    -o "$tree/bin/backplane" ./cmd/backplane
  mkdir -p "$tree/console"
  cp -R web/apps/console/dist/. "$tree/console/"
  cp LICENSE "$tree/LICENSE"
  cp deployments/README.release.md "$tree/README.md"
  tar -czf "$output/backplane_${version}_linux_${arch}.tar.gz" -C "$tree" .
done
(cd "$output" && sha256sum ./*.tar.gz ./*.tgz frontend-packages.json > SHA256SUMS)
echo "Release $version built in $output"
