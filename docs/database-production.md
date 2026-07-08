# Database Production Guide

This guide describes the production contract for storage adapters.

## Supported Runtime Paths

The production SQL path is:

```text
module service -> module store port -> module GORM repository -> database/gorm -> gorm.DB -> MySQL
```

`database/gorm` is primarily a lifecycle wrapper around native `gorm.DB`. Use GORM directly in module repositories for CRUD and transactions. Schema changes should go through versioned migrations, either through `database/gorm.ApplyMigrations` or an external deployment migration tool.

The app process selects the SQL driver explicitly:

```powershell
go run .\cmd\app -gorm-driver sqlite -gorm-dsn file:kpro-app.db
go run .\cmd\app -gorm-driver mysql -gorm-dsn "root:pwd@tcp(127.0.0.1:3306)/kpro?parseTime=true"
go run .\cmd\app -gorm-driver postgres -gorm-dsn "host=127.0.0.1 user=kpro password=pwd dbname=kpro sslmode=disable"
```

`database/common/readmodels` contains shared interfaces for cache, search, vector, graph, analytics, and metrics stores. The implemented non-SQL adapters are:

| Package | Resource kind | Main use |
| --- | --- | --- |
| `database/redis` | `redis` | JSON cache, lock, stream, sorted set |
| `database/es` | `search` | Typed index operations, raw search, bulk requests |
| `database/qdrant` | `vector` | Collection lifecycle, vector upsert/delete, raw search |
| `database/neo4j` | `graph` | Graph node/edge upsert, batch writes, raw Cypher, managed write transaction |
| `database/mongo` | `document` | MongoDB client/database/typed collection helpers |
| `database/clickhouse` | `analytics` | ClickHouse pool lifecycle, SQL/Tx, analytical table writer |
| `database/influx` | `metrics` | InfluxDB health, point/line writes, Flux query, measurement writer |

Keep MySQL/GORM as the source of truth for transactional data. Use the non-SQL adapters as read models, analytical sinks, document stores, or cache/projector targets behind module-owned ports. Concrete projectors belong in business modules; database packages only provide storage capabilities.

## Schema Management

Built-in modules use explicit SQL migrations declared in module store packages and recorded in `schema_migrations`.

Production rules:

- Use monotonically increasing migration versions, preferably timestamp-based: `YYYYMMDDHHMM` plus a sequence if needed.
- Keep SQL statements explicit and reviewable.
- Provide dialect-specific SQL when SQLite/MySQL/Postgres syntax differs.
- Do not modify an already-applied migration; checksum validation will reject it.
- Dirty or failed migrations stop startup and require operator review.
- MySQL/Postgres migrations acquire an advisory lock before running to reduce multi-instance startup races.
- Run migrations once during deployment or startup, then fail fast on errors.
- Use external migration tooling when your deployment process already owns database schema changes.

## Event Delivery

`eventing/delivery.Store` defines a reliable event delivery contract. The built-in `delivery.GORMStore` implements this as an outbox:

```text
service writes event envelope
-> delivery.GORMStore.Append
-> event_outbox_events
-> projector.Runner.Fetch(consumer)
-> event_outbox_deliveries status=inflight
-> projector.Handle
-> Ack / Fail
```

Projector handlers must be idempotent because delivery can retry. Failed records are scheduled for retry until `MaxAttempts`, then move to `dead`.

## Tests

Default tests do not require external databases:

```sh
go test ./...
```

Adapter unit tests use local fakes where possible:

- Redis uses `miniredis`.
- Elasticsearch, Qdrant, and InfluxDB use `httptest` protocol servers.
- ClickHouse uses a fake `database/sql` driver.
- MongoDB and Neo4j tests validate configuration and construction without requiring a live server.

Real Redis, Elasticsearch, Qdrant, MongoDB, Neo4j, ClickHouse, InfluxDB, MySQL, and Postgres end-to-end tests should be added as opt-in integration suites when those services are part of the deployment topology.