# KPro Docs

This directory contains architecture notes, flow guides, testing requirements, database adapter documentation, and eventing flow documentation.

| Document | Purpose |
| --- | --- |
| [app-server.md](app-server.md) | How the current `cmd/app` multi-transport app server is assembled and run. |
| [call-flow.md](call-flow.md) | End-to-end call flow across TCP, UDP, WebSocket, HTTP, SSE, and gRPC. |
| [tcp-internal-flow.md](tcp-internal-flow.md) | Detailed TCP path from bytes to handler and response writes. |
| [invocation-middleware-design.md](invocation-middleware-design.md) | Unified `core.Invocation` middleware design. |
| [network-test-requirements.md](network-test-requirements.md) | Required correctness rules for message-based transport tests. |
| [database-architecture.md](database-architecture.md) | Storage and eventing architecture for source-of-truth writes, delivery contracts, projectors, read models, and adapters. |
| [database-adapters.md](database-adapters.md) | Implemented SQL, Redis, Elasticsearch, Qdrant, MongoDB, ClickHouse, and InfluxDB adapters. |
| [database-redis-flow.md](database-redis-flow.md) | Detailed Redis startup, projector write, cache read, lock, stream, and sorted-set flow. |
| [gorm-http-request-flow.md](gorm-http-request-flow.md) | Detailed HTTP request to module service, GORM store, native GORM, and database insert flow. |
| [database-production.md](database-production.md) | Production storage, GORM usage, projector, and integration test guidance. |

Diagram assets live under [assets/](assets/).
