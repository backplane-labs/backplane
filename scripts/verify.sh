#!/usr/bin/env bash
# Run from a configured checkout. Owns only the backplane-verify Compose project.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off
mkdir -p bin/verification
full=(docker compose --profile full -p backplane-verify -f docker-compose.yaml -f docker-compose.observability.yaml)
minimal=(docker compose -p backplane-verify -f docker-compose.yaml -f docker-compose.minimal.yaml)
cleanup() {
  "${full[@]}" logs --no-color >> bin/verification/full-compose.log 2>&1 || true
  "${minimal[@]}" logs --no-color > bin/verification/minimal-compose.log 2>&1 || true
  "${minimal[@]}" down --remove-orphans >/dev/null 2>&1 || true
  "${full[@]}" down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT
make lint gen breaking
git diff --exit-code -- backplanepb internal/store/db web/packages/api/src docs/api-reference.md web/tests/api-examples.generated.ts
(cd web && yarn build && yarn workspace @backplane/embedding build --mode live --outDir dist-live && yarn typecheck && yarn test:unit && yarn test:browser && yarn test:errors-browser && yarn build:storybook && yarn test:storybook && yarn test:packed)
"${full[@]}" up -d --wait
export BACKPLANE_TEST_CONSUL=localhost:8500 BACKPLANE_TEST_NATS=localhost:4222 BACKPLANE_TEST_TEMPORAL=localhost:7233
export BACKPLANE_TEST_VALKEY=localhost:6379 BACKPLANE_TEST_PG='postgres://backplane:backplane@localhost:5433/backplane?sslmode=disable'
export BACKPLANE_TEST_OTLP=http://127.0.0.1:14318 BACKPLANE_TEST_LOGS_URL=http://127.0.0.1:19428
export BACKPLANE_TEST_TRACES_URL=http://127.0.0.1:20428 BACKPLANE_TEST_METRICS_URL=http://127.0.0.1:18428
export BACKPLANE_TEST_BROWSER=1
go vet ./...
go test -race -count=1 -p 1 -timeout 20m -json ./... | tee bin/verification/go-tests.jsonl
"${full[@]}" logs --no-color > bin/verification/full-compose.log 2>&1
"${full[@]}" down
CGO_ENABLED=0 make backplane
export BACKPLANE_NATS_URL= BACKPLANE_TEMPORAL_ADDR= BACKPLANE_MINIMAL_PORT=18081 BACKPLANE_MINIMAL_URL=http://127.0.0.1:18081/backplane/
"${minimal[@]}" up -d --wait
(cd web && node tests/minimal-browser.mjs)
