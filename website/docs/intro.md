---
slug: /
title: Backplane
description: Build, connect and operate independent services with a shared Go SDK and React console.
---

![Backplane](/img/banner.svg)

Backplane connects independently developed services into an operable installation. Operators inspect instances, change live configuration and investigate telemetry. The console combines platform administration with pages supplied by those services.

![The Backplane service catalog](/img/console.png)

## Choose a starting point

| Your goal | Start here |
| --- | --- |
| See a working installation | [Run hello and formatter](quickstart.md) |
| Build a Go service | [Service SDK](sdk/go.md), then [components](sdk/components.md) |
| Build a module's console pages | [Frontend packages](sdk/frontend.md) and [standalone module development](sdk/modules.md) |
| Deploy only the infrastructure you need | [Deployment modes](deploying/overview.md) |
| Call the platform programmatically | [Generated clients](reference/client.md) and [RPC reference](reference/api.md) |
| Investigate an incident | [Telemetry](guides/telemetry.md), [audit](guides/audit.md), [errors](guides/errors.md) |

## What the platform provides

- A Go lifecycle and dependency tree, typed configuration, optional infrastructure drivers, managed listeners and an internal service API.
- Catalog and instance status, revisioned configuration and authenticated module calls.
- A React component system, schema forms, editors, charts, observability views and a module contract with standalone development.
- Durable PostgreSQL audit, bounded OTLP forwarding and read access to independently operated telemetry stores.
- A cookie-authenticated console and generated TypeScript clients for every `backplane.console.v1` service.

Backplane does not deploy services or replace a scheduler such as Kubernetes. It does not own the OpenTelemetry Collector or telemetry storage. Application-specific products can use its APIs and UI packages without moving their domain model into the platform.

## Documentation contract

Guides describe the current implementation. The reference section is synchronized from the repository's authoritative API and package documentation on every site build. The site is published from a release tag; its version appears in the header. [Release archives](deploying/releases.md) include the same static site for independent hosting.
