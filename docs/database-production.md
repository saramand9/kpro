# Database Production Guide

This guide describes the production contract for the database layer.

## Supported Runtime Paths

The production SQL path is:

```text
module service -> module store port -> database/dbx or database/gorm -> SQL database
```

`database/dbx` is the primary `database/sql` implementation. SQLite, MySQL, and
Postgres are covered by generated SQL dialect support and the optional
`dbintegration` test suite. `database/gorm` is a peer adapter and should be wired
explicitly through module store adapters.

`database/views` contains contracts for cache, search, vector, graph, analytics,
and metrics stores. The implemented non-SQL adapters are:

| Package | Resource kind | Main use |
| --- | --- | --- |
| `database/redis` | `redis` | JSON cache, lock, stream, sorted set |
| `database/elastic` | `search` | Typed index operations, raw search, bulk requests |
| `database/qdrant` | `vector` | Collection lifecycle, vector upsert/delete, raw search |
| `database/mongo` | `document` | MongoDB client/database/typed collection helpers |
| `database/clickhouse` | `analytics` | ClickHouse pool lifecycle, SQL/Tx, analytical table writer |
| `database/influx` | `metrics` | InfluxDB health, point/line writes, Flux query, measurement writer |

Keep SQL as the source of truth for transactional data. Use the non-SQL adapters
as read models, analytical sinks, document stores, or cache/projection targets
behind module-owned ports.

## Migrations

Use `database/sql.Migrator` for source-of-truth schema changes. Every migration
must have a stable ID and immutable statements. The migrator records:

```text
id, checksum, status, started_at, applied_at, description, error
```

Production rules:

- Reusing a migration ID with different statements fails with a checksum error.
- Failed migrations are recorded as `failed` and are not retried silently.
- Operators must inspect and repair a failed migration before reapplying.
- MySQL DDL can implicitly commit; keep migrations small and idempotent where the
  database engine does not provide transactional DDL.

## Outbox

`database/sql.OutboxStore` stores facts once and tracks delivery per projection
consumer. `Fetch` claims pending or retryable deliveries as `inflight` before
returning them to a runner.

Production rules:

- `ClaimTimeout` is the inflight lease. If a runner crashes, the fact becomes
  fetchable again after the lease expires.
- `Ack` is idempotent after successful delivery.
- `Fail` only applies to inflight records, increments attempts, and moves a fact
  to `dead` after `MaxAttempts`.
- Projection handlers must be idempotent because retries and lease expiry can
  deliver the same fact more than once.

## Integration Tests

Default tests do not require external databases:

```sh
go test ./...
```

Adapter unit tests use local fakes where possible:

- Redis uses `miniredis`.
- Elasticsearch, Qdrant, and InfluxDB use `httptest` protocol servers.
- ClickHouse uses a fake `database/sql` driver.
- MongoDB tests validate configuration and construction without requiring a
  live server.

Run SQL integration tests with:

```sh
go test -tags dbintegration ./database/...
```

Set these environment variables to include external engines:

```sh
KPRO_TEST_MYSQL_DSN='kpro:kpro@tcp(127.0.0.1:3306)/kpro?parseTime=true'
KPRO_TEST_POSTGRES_DSN='postgres://kpro:kpro@127.0.0.1:5432/kpro?sslmode=disable'
KPRO_REQUIRE_EXTERNAL_DB=1
```

CI runs the integration suite against MySQL and Postgres service containers.

Real Redis, Elasticsearch, Qdrant, MongoDB, ClickHouse, and InfluxDB end-to-end
tests should be added as opt-in integration suites when those services are part
of the deployment topology.
