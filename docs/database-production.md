# Database Production Guide

This guide describes the production contract for storage adapters.

## Supported Runtime Paths

The production SQL path is:

```text
module service -> module store port -> module GORM repository -> database/gorm -> gorm.DB -> MySQL
```

`database/gorm` is primarily a lifecycle wrapper around native `gorm.DB`. Use GORM directly in module repositories for CRUD and transactions. Schema changes should go through versioned migrations, either through `database/gorm.ApplyMigrations` or an external deployment migration tool.

`database/common/readmodels` contains shared interfaces for cache, search, vector, graph,
analytics, and metrics stores. The implemented non-SQL adapters are:

| Package | Resource kind | Main use |
| --- | --- | --- |
| `database/redis` | `redis` | JSON cache, lock, stream, sorted set |
| `database/es` | `search` | Typed index operations, raw search, bulk requests |
| `database/qdrant` | `vector` | Collection lifecycle, vector upsert/delete, raw search |
| `database/mongo` | `document` | MongoDB client/database/typed collection helpers |
| `database/clickhouse` | `analytics` | ClickHouse pool lifecycle, SQL/Tx, analytical table writer |
| `database/influx` | `metrics` | InfluxDB health, point/line writes, Flux query, measurement writer |

Keep MySQL/GORM as the source of truth for transactional data. Use the non-SQL adapters
as read models, analytical sinks, document stores, or cache/projector targets behind module-owned ports.
Concrete projectors belong in business modules; database packages only provide storage capabilities.

## Schema Management

For the built-in example modules, schema setup uses explicit SQL migrations declared in module store packages and recorded in `schema_migrations`.

Recommended production rules:

- Use monotonically increasing migration versions.
- Keep SQL statements explicit and reviewable.
- Provide dialect-specific SQL when SQLite/MySQL/Postgres syntax differs.
- Run migrations once during deployment or startup, then fail fast on errors.
- Use external migration tooling when your deployment process already owns database schema changes.

## Event Delivery

`eventing/delivery.Store` defines a reliable event delivery contract. If a service needs durable event delivery, implement that contract in the application or module using the storage strategy it actually needs.

Projector handlers must be idempotent when retries are possible.

## Tests

Default tests do not require external databases:

```sh
go test ./...
```

Adapter unit tests use local fakes where possible:

- Redis uses `miniredis`.
- Elasticsearch, Qdrant, and InfluxDB use `httptest` protocol servers.
- ClickHouse uses a fake `database/sql` driver.
- MongoDB tests validate configuration and construction without requiring a live server.

Real Redis, Elasticsearch, Qdrant, MongoDB, ClickHouse, InfluxDB, and MySQL end-to-end tests should be added as opt-in integration suites when those services are part of the deployment topology.
