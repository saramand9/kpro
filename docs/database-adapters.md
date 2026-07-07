# Database Adapters

The database layer contains concrete storage adapters. It does not own business
projector logic. Event mechanics live in `eventing`, and business modules decide
which events update which external stores.

| Package | Kind | Primary helpers |
| --- | --- | --- |
| `database/relational/gorm` | `sql` | GORM resource lifecycle and native `gorm.DB` access |
| `database/cache/redis` | `redis` | `Cache[T]`, `Lock`, `Stream`, `ZSet` |
| `database/search/elasticsearch` | `search` | `Index[T]`, `SearchRaw`, `BulkService` |
| `database/vector/qdrant` | `vector` | `Collection[T]`, create/drop, upsert/delete, `SearchRaw` |
| `database/document/mongodb` | `document` | Mongo client/database, typed `Collection[T]` CRUD helpers |
| `database/analytics/clickhouse` | `analytics` | pool lifecycle, SQL/Tx, `AnalyticsTable[T]` |
| `database/metrics/influxdb` | `metrics` | `Writer`, `Measurement[T]`, Flux `Query` |

## Supporting Packages

| Package | Purpose |
| --- | --- |
| `database/contracts/lifecycle` | Common resource lifecycle, health, and observation contracts. |
| `database/contracts/readmodels` | Cache/search/vector/graph/analytics/metrics read-model interfaces. |
| `eventing/events` | Event envelope, schema, codec, and registry. |
| `eventing/delivery` | Reliable event delivery contract. |
| `eventing/projector` | Projector interface, registry, and runner. |

## Usage Rules

- Keep transactional writes in MySQL/GORM unless the domain explicitly owns a different source of truth.
- Put adapter-specific APIs behind module ports, not directly in handlers.
- Register each adapter as a `lifecycle.Resource` so health checks and shutdown are consistent.
- Keep concrete projectors in business modules, for example `internal/modules/orders/projectors/search.go`.
- Prefer raw official-client access for engine-specific advanced queries instead of growing a lowest-common-denominator abstraction.

## Test Coverage

The default `go test ./eventing/... ./database/...` suite runs without external
services. Redis uses `miniredis`; Elasticsearch, Qdrant, and InfluxDB use
protocol-level HTTP test servers; ClickHouse uses a fake `database/sql` driver;
Mongo validates configuration and construction without connecting to a live server.