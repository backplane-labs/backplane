# syntax=docker/dockerfile:1
# Source build. The package token is a BuildKit secret, never a build argument.
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS web
WORKDIR /src/web
COPY web/ ./
RUN --mount=type=secret,id=github_token,required=true \
    --mount=type=cache,target=/usr/local/share/.cache/yarn \
    printf '\n//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}\n' >> .npmrc && \
    export NODE_AUTH_TOKEN="$(cat /run/secrets/github_token)" && \
    yarn install --frozen-lockfile && \
    yarn build:packages && \
    yarn workspace @backplane/console build

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS go
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0
ARG COMMIT=unknown
ARG BUILD_TIME
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOWORK=off GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/backplane-labs/backplane/pkg/backplane/build.Service=backplane -X github.com/backplane-labs/backplane/pkg/backplane/build.Version=$VERSION -X github.com/backplane-labs/backplane/pkg/backplane/build.Commit=$COMMIT -X github.com/backplane-labs/backplane/pkg/backplane/build.Date=$BUILD_TIME" \
    -o /out/backplane ./cmd/backplane

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=go /out/backplane /app/bin/backplane
COPY --from=web /src/web/apps/console/dist /app/console
COPY LICENSE /app/LICENSE
ENV BACKPLANE_CONSOLE_ASSETS_DIR=/app/console BACKPLANE_CONSOLE_PREFIX=/backplane
EXPOSE 8081 9400
USER nonroot:nonroot
ENTRYPOINT ["/app/bin/backplane"]
