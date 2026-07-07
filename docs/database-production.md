# Database Production Guide

This guide describes the production contract for storage adapters.

## Supported Runtime Paths

The production SQL path is:

```text
module service -> module store port -> module GORM repository -> database/relational/gorm -> gorm.DB -> MySQL
```

`database/relational/gorm` is only a lifecycle wrapper around native `gorm.DB`. Use GORM directly in module repositories and use GORM's `AutoMigrate` or your deployment migration tool for schema management.

`database/contracts/readmodels` contains contracts for cache, search, vector, graph,
analytics, and metrics stores. The implemented non-SQL adapters are:

| Package | Resource kind | Main use |
| --- | --- | --- |
| `database/cache/redis` | `redis` | JSON cache, lock, stream, sorted set |
| `database/search/elasticsearch` | `search` | Typed index operations, raw search, bulk requests |
| `database/vector/qdrant` | `vector` | Collection lifecycle, vector upsert/delete, raw search |
| `database/document/mongodb` | `document` | MongoDB client/database/typed collection helpers |
| `database/analytics/clickhouse` | `analytics` | ClickHouse pool lifecycle, SQL/Tx, analytical table writer |
| `database/metrics/influxdb` | `metrics` | InfluxDB health, point/line writes, Flux query, measurement writer |

Keep MySQL/GORM as the source of truth for transactional data. Use the non-SQL adapters
as read models, analytical sinks, document stores, or cache/projector targets behind module-owned ports.
Concrete projectors belong in business modules; database packages only provide storage capabilities.

## Schema Management

For the built-in example modules, schema setup uses GORM `AutoMigrate` from the module model package.
Production applications can either keep using GORM migrations or run explicit SQL migrations outside KPro.

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
