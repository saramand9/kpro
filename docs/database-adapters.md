# Database Adapters

The database layer keeps SQL as the transactional source-of-truth path and uses
adapter packages for cache, search, vectors, documents, analytics, and metrics.
Application modules should depend on their own ports; startup code chooses the
adapter implementation.

| Package | Kind | Primary helpers |
| --- | --- | --- |
| `database/dbx` | `sql` | `Database`, `Store[T]`, async writes, transactions |
| `database/sql` | `sql` | SQL resource, migrator, unit of work, SQL outbox |
| `database/gorm` | `sql` | GORM resource, `Store[T]`, table-name inference, DAO helpers |
| `database/redis` | `redis` | `Cache[T]`, `Lock`, `Stream`, `ZSet` |
| `database/elastic` | `search` | `Index[T]`, `SearchRaw`, `BulkService` |
| `database/qdrant` | `vector` | `Collection[T]`, create/drop, upsert/delete, `SearchRaw` |
| `database/mongo` | `document` | Mongo client/database, typed `Collection[T]` CRUD helpers |
| `database/clickhouse` | `analytics` | pool lifecycle, SQL/Tx, `AnalyticsTable[T]` |
| `database/influx` | `metrics` | `Writer`, `Measurement[T]`, Flux `Query` |

## Usage Rules

- Keep transactional writes in SQL unless the domain explicitly owns a different
  source of truth.
- Put adapter-specific APIs behind module ports, not directly in handlers.
- Register each adapter as a `kernel.Resource` so health checks and shutdown are
  consistent.
- Use projection/outbox delivery for cache, search, vector, analytics, and
  metrics updates that can be replayed.
- Prefer raw official-client access for engine-specific advanced queries instead
  of growing a lowest-common-denominator abstraction.

## Test Coverage

The default `go test ./database/...` suite runs without external services.
Redis uses `miniredis`; Elasticsearch, Qdrant, and InfluxDB use protocol-level
HTTP test servers; ClickHouse uses a fake `database/sql` driver; Mongo validates
configuration and construction without connecting to a live server.

Real service integration tests should be opt-in and environment-driven, like the
current SQL `dbintegration` suite.
