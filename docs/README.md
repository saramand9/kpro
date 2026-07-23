# KPro Documentation

Start with the quickstart and golden path. The remaining guides separate implemented framework behavior from the application and infrastructure validation required for a production service.

## Start here

| Document | Purpose |
| --- | --- |
| [Analysis Index](analysis-index.md) | Reopen the repository quickly, find critical code paths, and reuse validation commands. |
| [Project Map](project-map.md) | Understand package ownership, runtime flow, extension points, and evidence files. |
| [10-Minute Quick Start](quickstart.md) | Run a checked HTTP example or scaffold a starter with middleware and graceful shutdown. |
| [Recommended Golden Path](golden-path.md) | Choose a protocol, structure an application, validate startup, own observability/lifecycle, and grow safely. |
| [Observability Example](../examples/observability/main.go) | Run bounded health and Prometheus endpoints with Zap, OTel wiring, and a custom runner descriptor. |
| [Examples](../examples/README.md) | Run standalone and modular examples for each implemented protocol. |

## Ship and operate

| Document | Purpose |
| --- | --- |
| [Production Readiness Checklist](production-readiness.md) | Distinguish built-in mechanisms, required configuration, and external production work. |
| [Operations and Capacity Planning](operations.md) | Failure recovery, runbooks, overload behavior, performance budgets, sizing controls, and load-test evidence. |
| [API Stability, Versioning, and Deprecation](api-stability.md) | Current pre-1.0 status, SemVer policy, compatibility surface, and upgrade guidance. |
| [App Server](app-server.md) | How the current `cmd/app` multi-transport application is assembled and run. |

## Extend the framework

| Document | Purpose |
| --- | --- |
| [Extending KPro](extending-kpro.md) | Implement descriptors/validation, transports, runners, codecs, middleware, health checks, and database adapters. |
| [Invocation Middleware Design](invocation-middleware-design.md) | Unified `core.Invocation` middleware model and protocol bridges. |

## Data and eventing

| Document | Purpose |
| --- | --- |
| [Database Architecture](database-architecture.md) | Source-of-truth writes, delivery contracts, projectors, read models, and adapter boundaries. |
| [Database Adapters](database-adapters.md) | Implemented SQL, Redis, Elasticsearch, Qdrant, MongoDB, ClickHouse, InfluxDB, and Neo4j adapters. |
| [Database Production](database-production.md) | GORM usage, migrations, outbox/projector operation, and real integration-test scope. |
| [GORM HTTP Request Flow](gorm-http-request-flow.md) | HTTP request through module service, GORM store, native GORM, and database insert. |
| [Redis Flow](database-redis-flow.md) | Redis startup, projector writes, cache reads, locks, streams, and sorted sets. |

## Runtime internals and testing

| Document | Purpose |
| --- | --- |
| [Call Flow](call-flow.md) | End-to-end flow across TCP, UDP, WebSocket, HTTP, SSE, and gRPC. |
| [TCP Internal Flow](tcp-internal-flow.md) | Detailed TCP path from bytes to handler and response writes. |
| [Network Test Requirements](network-test-requirements.md) | Correctness rules for message-transport tests. |

Diagram assets live under [assets/](assets/).

Project-wide contribution and change records live in
[CONTRIBUTING.md](../CONTRIBUTING.md) and [CHANGELOG.md](../CHANGELOG.md).

Protocol or adapter implementation means the relevant code path exists and is tested at the scope described by its guide. It does not imply production certification, an availability guarantee, or validation against every deployment topology.
