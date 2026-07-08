# Database Adapters

The database layer contains concrete storage adapters. It does not own business projector logic. Event mechanics live in `eventing`, and business modules decide which events update which external stores.

| Package | Kind | Primary helpers |
| --- | --- | --- |
| `database/gorm` | `sql` | GORM lifecycle, native `gorm.DB`, versioned migrations with lock/checksum/dirty state |
| `database/redis` | `redis` | `Cache[T]`, `Lock`, `Stream`, `ZSet` |
| `database/es` | `search` | `Index[T]`, `SearchRaw`, `BulkService` |
| `database/qdrant` | `vector` | `Collection[T]`, create/drop, upsert/delete, `SearchRaw` |
| `database/neo4j` | `graph` | `Graph`, batch node/edge upsert, raw Cypher query/write, managed write transaction |
| `database/mongo` | `document` | Mongo client/database, typed `Collection[T]` CRUD helpers |
| `database/clickhouse` | `analytics` | pool lifecycle, SQL/Tx, `AnalyticsTable[T]` |
| `database/influx` | `metrics` | `Writer`, `Measurement[T]`, Flux `Query` |

## Supporting Packages

| Package | Purpose |
| --- | --- |
| `internal/app/persistence` | App-level database resource composition, SQL driver selection, health, and shutdown ownership. |
| `database/common/lifecycle` | Shared resource lifecycle, health, and observation types. |
| `database/common/readmodels` | Shared cache/search/vector/graph/analytics/metrics read-model interfaces. |
| `eventing/events` | Event envelope, schema, codec, and registry. |
| `eventing/delivery` | Reliable event delivery contract plus GORM outbox store. |
| `eventing/projector` | Projector interface, registry, and runner. |

## Usage Rules

- Keep transactional writes in MySQL/GORM unless the domain explicitly owns a different source of truth.
- Put adapter-specific APIs behind module ports, not directly in handlers.
- Register each adapter as a `lifecycle.Resource` so health checks and shutdown are consistent.
- Keep concrete projectors in business modules, for example `internal/modules/orders/projectors/search.go`.
- Prefer raw official-client access for engine-specific advanced queries instead of growing a lowest-common-denominator abstraction.

## Test Coverage

The default `go test ./eventing/... ./database/...` suite runs without external services. Redis uses `miniredis`; Elasticsearch, Qdrant, and InfluxDB use protocol-level HTTP test servers; ClickHouse uses a fake `database/sql` driver; Mongo and Neo4j validate configuration and construction without connecting to a live server. Neo4j also has an opt-in `neo4jintegration` suite driven by `KPRO_TEST_NEO4J_URI`, `KPRO_TEST_NEO4J_USER`, `KPRO_TEST_NEO4J_PASSWORD`, and `KPRO_TEST_NEO4J_DATABASE`.