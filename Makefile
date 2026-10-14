# backplane — platform, Go SDK, reference module `hello`, console.
#
# Conventions (shared with schemapb):
#   - ALL tools live in ./bin (version-pinned below); nothing is installed
#     globally. `git clone` + `make configure` = fully working environment.
#   - `make check` explicitly runs full verification; `make release` only tags.

SHELL := /bin/bash
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

BIN := $(CURDIR)/bin

# ---- pinned tool versions (bumps are explicit commits) ----------------------
EASYP_VERSION              := v0.16.6
PROTOC_GEN_GO_VERSION      := v1.36.11
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2
GOLANGCI_LINT_VERSION      := v2.11.3
SQLD_VERSION               := v1.1.8

# Scratch database `make db-init` realizes schemas in: sqld drops every
# non-system schema in it, so never point it at a real database.
DB_DEV_URL ?= postgres://backplane:backplane@localhost:5433/backplane_scratch?sslmode=disable

# easyp resolves protoc-gen-* plugins from PATH: put ./bin first.
EASYP := PATH="$(CURDIR)/web/node_modules/.bin:$(BIN):$$PATH" "$(BIN)/easyp"

# v2+ requires semantic import versioning (/v2 in the module path) — not
# supported yet; releases stay on v0/v1.
MAX_MAJOR := 1

.PHONY: help
help: ## List all targets with explanations
	@grep -hE '^[a-zA-Z0-9_.-]+:.*## ' $(MAKEFILE_LIST) | \
	  awk -F':.*## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: configure
configure: configure-web configure-lint configure-gen ## Bring environment to working state: fetch pinned Go and frontend tools
	@echo "✓ configure done — tools in $(BIN)"

.PHONY: configure-gen
configure-gen: ## Install pinned code generators and fetch proto dependencies
	mkdir -p "$(BIN)"
	echo "--- easyp $(EASYP_VERSION)"
	GOBIN="$(BIN)" go install github.com/easyp-tech/easyp/cmd/easyp@$(EASYP_VERSION)
	echo "--- protoc-gen-go $(PROTOC_GEN_GO_VERSION)"
	GOBIN="$(BIN)" go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	echo "--- protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION)"
	GOBIN="$(BIN)" go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	echo "--- sqld, sqld-gen-go $(SQLD_VERSION)"
	GOBIN="$(BIN)" go install github.com/gopherex/sqld/cmd/sqld@$(SQLD_VERSION)
	GOBIN="$(BIN)" go install github.com/gopherex/sqld/cmd/sqld-gen-go@$(SQLD_VERSION)
	echo "--- proto deps"
	$(EASYP) mod update

.PHONY: configure-lint
configure-lint: ## Install only the pinned Go linter
	mkdir -p "$(BIN)"
	GOBIN="$(BIN)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: gen
gen: db ## Generate Go/TS APIs, the hello example, and the store's queries
	$(EASYP) mod download
	$(EASYP) generate
	$(EASYP) --cfg examples/hello/easyp.yaml generate
	node web/scripts/api-index.mjs
	node web/scripts/api-reference.mjs

.PHONY: db
db: ## Generate the store's typed queries (internal/store/db) from schema and queries
	"$(BIN)/sqld" generate -c sqld.yaml
	"$(BIN)/sqld" migrate validate -c sqld.yaml

.PHONY: db-init
db-init: ## Regenerate the single init migration from schema.sql (pre-release: no history), then reset the local database
	docker compose exec -T postgres dropdb -U backplane --if-exists backplane_scratch
	docker compose exec -T postgres createdb -U backplane backplane_scratch
	rm -f internal/store/migrations/*.sql
	"$(BIN)/sqld" migrate generate init -c sqld.yaml --dev-url "$(DB_DEV_URL)"
	@# The store creates the schema before migrating (sqld_migrations lives in it).
	sed -i -e 's|^CREATE SCHEMA "backplane";|CREATE SCHEMA IF NOT EXISTS "backplane";|' \
		-e '/^DROP SCHEMA "backplane";$$/d' internal/store/migrations/*_init.sql
	"$(BIN)/sqld" migrate validate -c sqld.yaml
	docker compose exec -T postgres psql -U backplane -d backplane -qc 'DROP SCHEMA IF EXISTS backplane CASCADE'

.PHONY: lint
lint: ## Lint proto files and Go code
	$(EASYP) lint -p backplanepb
	$(EASYP) --cfg examples/hello/easyp.yaml lint -p proto
	"$(BIN)/golangci-lint" run ./...

.PHONY: fmt
fmt: ## Format Go code (gofumpt, gci)
	"$(BIN)/golangci-lint" fmt ./...

.PHONY: breaking
breaking: ## Check proto files for breaking changes against master
	$(EASYP) breaking -p backplanepb

.PHONY: test
test: ## Unit tests with the race detector (integration tests skip without the stack)
	go vet ./...
	go test -race ./...

.PHONY: conformance
conformance: ## SDK contract and integration tests against platform-in-a-box (make up)
	BACKPLANE_TEST_CONSUL=localhost:8500 BACKPLANE_TEST_NATS=localhost:4222 BACKPLANE_TEST_TEMPORAL=localhost:7233 \
	BACKPLANE_TEST_PG="postgres://backplane:backplane@localhost:5433/backplane?sslmode=disable" \
		go test -race -count=1 ./...

.PHONY: test-m1
test-m1: ## M1 end to end: bin backplane + hello on direct listeners — config revisions, reconciler, console, relay (make up)
	BACKPLANE_TEST_CONSUL=localhost:8500 \
	BACKPLANE_TEST_PG="postgres://backplane:backplane@localhost:5433/backplane?sslmode=disable" \
		GOWORK=off go test -race -count=1 -run '^TestM1$$' -v ./conformance/

.PHONY: test-m2
test-m2: ## M2 end to end: bin backplane + hello with NATS and Temporal on direct listeners — bindings, rules, runs, one trace (make up)
	BACKPLANE_TEST_CONSUL=localhost:8500 \
	BACKPLANE_TEST_NATS=localhost:4222 BACKPLANE_TEST_TEMPORAL=localhost:7233 \
	BACKPLANE_TEST_PG="postgres://backplane:backplane@localhost:5433/backplane?sslmode=disable" \
		GOWORK=off go test -race -count=1 -timeout 20m -run '^TestM2$$' -v ./conformance/

.PHONY: test-direct
test-direct: ## Direct runtime end to end: bin backplane + hello with NATS and no Temporal — hooks over gRPC, bindings and rules in-process, recorded runs (make up)
	BACKPLANE_TEST_CONSUL=localhost:8500 BACKPLANE_TEST_NATS=localhost:4222 \
	BACKPLANE_TEST_PG="postgres://backplane:backplane@localhost:5433/backplane?sslmode=disable" \
		GOWORK=off go test -race -count=1 -timeout 20m -run '^TestDirect$$' -v ./conformance/

.PHONY: up down
up: ## Start platform-in-a-box (docker compose)
	docker compose --profile full up -d --wait

down: ## Stop platform-in-a-box
	docker compose down

# ---- service build: stamp identity at link time --------------------------------
BUILD_PKG := github.com/backplane-labs/backplane/pkg/backplane/build
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0)
COMMIT    ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE      ?= $(shell date -u +%FT%TZ)
ldflags    = -X $(BUILD_PKG).Service=$(1) -X $(BUILD_PKG).Version=$(VERSION) -X $(BUILD_PKG).Commit=$(COMMIT) -X $(BUILD_PKG).Date=$(DATE)

.PHONY: hello
hello: ## Build examples/hello into ./bin/hello
	go build -trimpath -ldflags "$(call ldflags,hello)" -o "$(BIN)/hello" ./examples/hello/cmd/hello

.PHONY: run-hello
run-hello: hello ## Run hello against the local stack
	BACKPLANE_CONSUL_ADDR=localhost:8500 BACKPLANE_NATS_URL=localhost:4222 \
	BACKPLANE_TEMPORAL_ADDR=localhost:7233 "$(BIN)/hello"

.PHONY: backplane
backplane: ## Build the backplane server into ./bin/backplane
	go build -trimpath -ldflags "$(call ldflags,backplane)" -o "$(BIN)/backplane" ./cmd/backplane

.PHONY: run-backplane
# Console admin token of `make run-backplane` (log in with it at the
# console): a development value; a deployment sets its own secret.
DEV_ADMIN_TOKEN ?= dev-admin-token-change-me

run-backplane: backplane ## Run backplane against the local stack (console token: DEV_ADMIN_TOKEN)
	BACKPLANE_CONSUL_ADDR=localhost:8500 BACKPLANE_NATS_URL=localhost:4222 \
	BACKPLANE_TEMPORAL_ADDR=localhost:7233 \
	BACKPLANE_ADMIN_TOKEN="$(DEV_ADMIN_TOKEN)" \
	BACKPLANE_PG_DSN="postgres://backplane:backplane@localhost:5433/backplane?sslmode=disable" \
	BACKPLANE_INTERNAL_PORT=9410 BACKPLANE_PUBLIC_PORT=8090 \
	BACKPLANE_CONSOLE_INSECURE_COOKIE=true \
		"$(BIN)/backplane"

.PHONY: check
check: verify ## Full verification on explicit request; same as manual full CI

.PHONY: configure-web web-check dev-ui dev-module
configure-web: ## Install locked frontend dependencies (Node >=22.12, Yarn 1.22.22)
	cd web
	yarn install --frozen-lockfile

web-check: ## Build packages and fixtures, typecheck, unit and browser tests
	cd web
	yarn build
	yarn typecheck
	yarn test:unit
	yarn test:browser
	yarn test:errors-browser
	yarn build:storybook
	yarn test:storybook
	cd ..
	GOWORK=off BACKPLANE_TEST_BROWSER=1 go test -race -count=1 -run '^TestBrowserConsole$$' ./internal/console

dev-ui: ## Run the component compatibility fixture
	cd web
	yarn dev

dev-module: ## Run the module template independently of the platform
	cd web
	yarn dev:module

.PHONY: release

.PHONY: test-otlp
test-otlp: ## OTLP admission and errors -> isolated Collector -> Victoria storage (start observability compose first)
	GOWORK=off BACKPLANE_TEST_OTLP=http://127.0.0.1:14318 \
	BACKPLANE_TEST_LOGS_URL=http://127.0.0.1:19428 BACKPLANE_TEST_TRACES_URL=http://127.0.0.1:20428 \
	BACKPLANE_TEST_METRICS_URL=http://127.0.0.1:18428 \
		go test -race -count=1 -run '^(TestStoredSignals|TestErrorsLive)$$' -v ./internal/otlp ./internal/obs

release: ## Create and push a version tag; no local builds or tests
	cd "$$(git rev-parse --show-toplevel)"
	if [ -n "$$(git status --porcelain)" ]; then
	  echo "✗ Working tree is not clean — commit or stash first:"
	  git status --short
	  exit 1
	fi
	if ! upstream="$$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null)"; then
	  echo "✗ Current branch has no upstream — push it first."
	  exit 1
	fi
	git fetch --quiet --prune --tags
	read -r behind ahead <<< "$$(git rev-list --left-right --count "$$upstream...HEAD")"
	if [ "$$ahead" -gt 0 ] || [ "$$behind" -gt 0 ]; then
	  echo "✗ Branch differs from $$upstream ($$ahead ahead, $$behind behind) — synchronize first."
	  exit 1
	fi

	cur="$$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)"
	cur="$${cur:-0.0.0}"
	head="$$(git rev-parse --short HEAD)"
	echo "Latest release: v$$cur    HEAD: $$head"
	echo
	echo "  1) bump version"
	echo "  2) recreate last tag (v$$cur) on HEAD   [force]"
	echo "  3) cancel"
	read -r -p "> " action

	case "$$action" in
	1)
	  IFS=. read -r MA MI PA <<< "$$cur"
	  echo
	  echo "  1) major  -> v$$((MA+1)).0.0"
	  echo "  2) minor  -> v$$MA.$$((MI+1)).0"
	  echo "  3) patch  -> v$$MA.$$MI.$$((PA+1))"
	  read -r -p "> " comp
	  case "$$comp" in
	    1) MA=$$((MA+1)); MI=0; PA=0 ;;
	    2) MI=$$((MI+1)); PA=0 ;;
	    3) PA=$$((PA+1)) ;;
	    *) echo "Aborted."; exit 0 ;;
	  esac
	  if [ "$$MA" -gt "$(MAX_MAJOR)" ]; then
	    echo "✗ v$$MA requires semantic import versioning (/v$$MA in the module path)."
	    echo "  Not supported yet — stay on v0/v1."
	    exit 1
	  fi
	  new="$$MA.$$MI.$$PA"
	  echo
	  echo "Release v$$new — will create tag v$$new on $$head and push."
	  read -r -p "Type 'yes' to proceed: " ok
	  [ "$$ok" = "yes" ] || { echo "Aborted."; exit 0; }
	  git tag -a "v$$new" -m "v$$new"
	  git push origin "v$$new"
	  echo "✓ Released v$$new."
	  ;;
	2)
	  if ! git tag -l "v$$cur" | grep -q .; then
	    echo "✗ No release tag to recreate."; exit 1
	  fi
	  echo
	  echo "Will DELETE and recreate tag v$$cur on $$head, then force-push."
	  read -r -p "Type 'yes' to proceed: " ok
	  [ "$$ok" = "yes" ] || { echo "Aborted."; exit 0; }
	  git tag -d "v$$cur"
	  git push origin ":refs/tags/v$$cur" 2>/dev/null || true
	  git tag -a "v$$cur" -m "v$$cur"
	  git push origin --force "v$$cur"
	  echo "✓ Recreated v$$cur on $$head."
	  ;;
	*) echo "Cancelled." ;;
	esac

.PHONY: clean
clean: ## Remove tools, vendored protos and generated code
	rm -rf "$(BIN)" easyp_vendor backplanepb/v1/*.pb.go

.PHONY: formatter run-formatter setup-example test-replicas
formatter: ## Build the independent formatter companion service
	go build -trimpath -ldflags "$(call ldflags,formatter)" -o "$(BIN)/formatter" ./examples/formatter/cmd/formatter

run-formatter: formatter ## Run formatter against the local stack on :8082 / :9420
	BACKPLANE_CONSUL_ADDR=localhost:8500 BACKPLANE_NATS_URL=localhost:4222 \
	BACKPLANE_TEMPORAL_ADDR=localhost:7233 BACKPLANE_PUBLIC_PORT=8082 \
	BACKPLANE_INTERNAL_PORT=9420 "$(BIN)/formatter"

setup-example: ## Save hello -> formatter binding and event rule through the console API
	BACKPLANE_ADMIN_TOKEN="$${BACKPLANE_ADMIN_TOKEN:-$(DEV_ADMIN_TOKEN)}" \
		go run ./examples/demo/cmd/setup

test-replicas: ## Two backplanes + hello + formatter: crash/rejoin, config, rules, Nexus retirement (make up)
	BACKPLANE_TEST_CONSUL=localhost:8500 \
	BACKPLANE_TEST_NATS=localhost:4222 BACKPLANE_TEST_TEMPORAL=localhost:7233 \
	BACKPLANE_TEST_PG="postgres://backplane:backplane@localhost:5433/backplane?sslmode=disable" \
		GOWORK=off go test -race -count=1 -timeout 20m -run '^TestReplicas$$' -v ./conformance/

.PHONY: test-audit
test-audit: ## Durable audit: real PostgreSQL transactions, replica leases, outage recovery and cursor expiry
	GOWORK=off BACKPLANE_TEST_PG="postgres://backplane:backplane@localhost:5433/backplane?sslmode=disable" \
		go test -race -count=1 ./internal/audit ./internal/store

# Development installation with the production console and live example module.
DEV_VERSION ?= 0.0.0+dev.$(shell date -u +%Y%m%d%H%M%S%N)
DEV_COMPOSE = docker compose --profile full -f docker-compose.yaml -f docker-compose.observability.yaml -f docker-compose.dev.yaml
.PHONY: dev dev-down dev-build dev-logs test-dev
dev-build: ## Build the console, example module and static Go binaries
	cd web
	yarn install --frozen-lockfile
	yarn build
	cd ..
	CGO_ENABLED=0 GOWORK=off $(MAKE) VERSION="$(DEV_VERSION)" backplane hello formatter
	CGO_ENABLED=0 GOWORK=off go build -o "$(BIN)/setup-example" ./examples/demo/cmd/setup

dev: dev-build ## Start the complete live installation with a seeded hello/formatter binding and rule
	$(DEV_COMPOSE) up -d --wait
	node web/scripts/dev-ready.mjs
	$(DEV_COMPOSE) run --rm seed
	curl --fail --silent --show-error 'http://127.0.0.1:8080/hello/?name=Developer'
	echo 'Development console: http://127.0.0.1:8081/backplane/ (DEV_ADMIN_TOKEN)'

dev-down: ## Stop the development installation; retain stored data
	$(DEV_COMPOSE) down

dev-logs: ## Follow backplane and reference service logs
	$(DEV_COMPOSE) logs -f backplane hello formatter

test-dev: ## Browser acceptance against the installation started by make dev
	cd web
	node tests/login-browser.mjs
	node tests/dev-browser.mjs
	node tests/console-browser.mjs
	node tests/infrastructure-browser.mjs

.PHONY: dev-minimal
dev-minimal: dev-build ## Start console with PostgreSQL and Consul only
	docker compose -f docker-compose.yaml -f docker-compose.minimal.yaml up -d --wait
	echo 'Minimal console: http://127.0.0.1:8081/backplane/ (DEV_ADMIN_TOKEN)'

.PHONY: verify
verify: ## Full release verification: live race suite, browser schedules, packages and minimal Compose
	bash scripts/verify.sh
