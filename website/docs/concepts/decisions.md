# Platform design decisions

These rules define the current product and extension boundaries. The detailed
[frontend integration contract](../reference/frontend-contract.md) is published
from its canonical source next to the code.

## One frontend stack

Embedded modules use React, TypeScript and the platform runtime contract. The host owns the router, session, shared client, theme and translations. Modules define any number of pages beneath their service route and contribute navigation leaves. This keeps provider identity, styling and navigation consistent across the installation.

The visual system uses HyperDX-inspired colors and CSS tokens, shared shadcn-style primitives and wrapped Grafana components where useful. Consumers import platform packages rather than styling Grafana directly. Both light and dark themes are supported. Translation infrastructure is present; English is the only shipped locale.

An author can run a module with `yarn dev` or serve its standalone build from a container without starting Backplane. Fixtures and local adapters belong to that module's development setup. Deployment decides how its built remote is served and embedded. There is no separate external-UI bridge SDK.

## Domain behavior stays in services

The platform provides generic API, telemetry and UI capabilities. An application can build error grouping, incident management or another domain product using them. Those domain models do not become platform requirements. The built-in Errors view reads OTel exception records; it does not imply an issue tracker or an Errotel deployment.

## Infrastructure clients, not application contracts

Applications choose required or optional native clients through dependency providers. They own the protocols, consumers, workflows and schedules they implement. Backplane never chooses a business payload codec or provisions an application queue on a service's behalf. PostgreSQL and Consul are required by the platform server. Its NATS and Temporal connections are optional health checks.

## OpenTelemetry remains independent

Applications send ordinary OTLP to deployment-managed collectors, directly or through the bounded platform proxy. The platform reads independent stores. Ingest transformations, retention, tenants and backend credentials are deployment responsibilities, with no corresponding deployment editor in the console.

Telemetry declarations are optional. Stored sources remain discoverable even when no module declares them. A wrapper and an unmodified third-party service can both be visible through ordinary OTel resource attributes. Resource namespaces organize data; they do not create a tenant isolation boundary.

## Audit is durable and export is optional

Control audit is stored in PostgreSQL. Platform mutations share a transaction with their audit record where possible. Optional export uses OTel Logs, independently of NATS. Trusted application logs marked `backplane.audit=true` are also persisted in PostgreSQL by a separate Collector pipeline.

## Delivery and releases

Public frontend packages live in GitHub Packages under `@backplane-labs`. The Go module uses repository tags. A coordinated release contains Linux binaries with the console, multi-platform GHCR images, frontend package tarballs and the documentation archive. `make release` creates a tag; CI performs packaging and publication. Expensive full-stack verification is explicit, while ordinary pushes select checks by changed paths.

## Current boundaries

The current installation is an operator console, not a multi-user/RBAC product or an identity-provider login integration. It does not implement deployment orchestration, a general service mesh, per-service Temporal authorization, Kafka delivery, alerting, a dashboard-definition editor, GitOps configuration delivery or automatic module-template upgrades. Encryption at rest belongs to the deployed storage infrastructure. The generated metric overview is a read view, not a saved-dashboard system.

These boundaries are not hidden configuration switches. Applications and deployment tooling can add functionality around the platform, but should not assume these capabilities exist in its current API.
