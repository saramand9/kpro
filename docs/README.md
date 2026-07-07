# KPro Docs

This directory contains architecture notes, flow guides, testing requirements, and database module documentation.

| Document | Purpose |
| --- | --- |
| [app-server.md](app-server.md) | How the current `cmd/app` multi-transport app server is assembled and run. |
| [call-flow.md](call-flow.md) | End-to-end call flow across TCP, UDP, WebSocket, HTTP, SSE, and gRPC. |
| [tcp-internal-flow.md](tcp-internal-flow.md) | Detailed TCP path from bytes to handler and response writes. |
| [invocation-middleware-design.md](invocation-middleware-design.md) | Unified `core.Invocation` middleware design. |
| [network-test-requirements.md](network-test-requirements.md) | Required correctness rules for message-based transport tests. |
| [dbx.md](dbx.md) | DBX database module, Store CRUD, async writes, SQL dialects, and routing examples. |
| [database-architecture.md](database-architecture.md) | Fact-centric database architecture for SQL source-of-truth, outbox, projections, and adapters. |
| [database-adapters.md](database-adapters.md) | Implemented SQL, Redis, Elasticsearch, Qdrant, MongoDB, ClickHouse, and InfluxDB adapters. |
| [database-production.md](database-production.md) | Production database migration, outbox, and SQL integration test guidance. |

Diagram assets live under [assets/](assets/).
