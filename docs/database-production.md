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
-> domain write + delivery.GORMStore.AppendTx in one GORM transaction
-> event_outbox_events
-> projector.Runner.Run
-> delivery.GORMStore.Fetch(consumer)
-> event_outbox_deliveries status=inflight with a visibility lease
-> RenewAttempts once for the unprocessed batch and periodically near lease expiry
-> projector.Handle
-> fenced AckAttempt / FailAttempt
```

Use `AppendTx` when the domain row and event share a GORM database; the caller
owns commit or rollback. `Append` remains available for event-only writes.
Inflight records become fetchable again after `WithLease` expires, so a process
crash cannot strand them forever. The runner uses attempt-aware settlement when
the store implements `delivery.AttemptStore`, preventing an expired worker from
acknowledging or failing a newer attempt. With `delivery.RenewableAttemptStore`,
the built-in runner renews a record before handling and halfway through each
remaining lease; renewal loss cancels the handler context and the stale attempt
is not settled. When the store also implements
`delivery.BatchRenewableAttemptStore`, one initial renewal protects the current
record and the still-unprocessed tail from one database-clock sample. Fast
records reuse that lease window instead of updating an N-item tail N times; a
long handler renews the remaining tail again near half-life. This prevents a
slow first handler from letting later records expire without O(N²) write
amplification. Configure the lease above normal database latency and scheduling
jitter. Lease decisions use the database server's UTC clock on SQLite, MySQL
and PostgreSQL, so worker host clock skew cannot cause an early reclaim. Keep
projector handlers idempotent because delivery is at least once. Failed or
repeatedly expired records move to `dead` after the configured maximum attempts.

Code that fetches and settles records directly should call `AckAttempt` and
`FailAttempt` with the attempt returned by `Fetch`. `GORMStore.Ack` and `Fail`
remain only as first-attempt compatibility methods and deliberately return
`ErrLeaseLost` after a record has been reclaimed.

Operational tooling can discover optional capabilities without widening the
base `delivery.Store` interface:

- `BacklogStore.Backlog` returns pending, inflight, failed, and dead counts for
  one consumer in a single query. Events whose consumer row has not yet been
  materialized are counted as pending. The query aggregates the narrow
  consumer delivery index and a separate event count instead of a full-history
  `LEFT JOIN`.
- `DeadLetterStore.DeadLetters` returns deterministic dead-letter pages.
- `DeadLetterStore.ReplayDeadLetter` performs an atomic `dead -> pending`
  transition only. It preserves the dead attempts/error in audit columns,
  keeps lease receipts monotonic across replay generations, restarts the
  per-generation retry budget, and returns `ErrNotDead` for an active lease or
  a concurrent replay loser.
- `Runner.Snapshot` and `Runner.Health` expose process-local progress and the
  latest pass health. `WithRetryPolicy` selects bounded application-specific
  retry delay; it does not change delivery attempt fencing.

Register `prometheusx.NewBacklogCollector` and
`prometheusx.NewProjectorCollector` with an application-owned Prometheus
registry when these values should be scraped. Consumer and projector labels
are explicit constructor input; event IDs and error text are never metric
labels. Retention remains an application policy: archive or purge delivered
history only after every required consumer has crossed the corresponding
watermark, and measure the backlog query plan at production cardinality.

## Tests

Default tests do not require external databases:

```sh
go test ./...
```

MySQL and PostgreSQL connection, migration-lock, idempotent-migration, and
transaction-rollback paths run in CI through the opt-in suite:

```sh
KPRO_REQUIRE_EXTERNAL_DB=1 \
KPRO_TEST_MYSQL_DSN='...' \
KPRO_TEST_POSTGRES_DSN='...' \
go test -tags dbintegration ./database/... ./eventing/delivery
```

Adapter unit tests use local fakes where possible:

- Redis uses `miniredis`.
- Elasticsearch, Qdrant, and InfluxDB use `httptest` protocol servers.
- ClickHouse uses a fake `database/sql` driver.
- MongoDB and Neo4j tests validate configuration and construction without requiring a live server.

Real Redis, Elasticsearch, Qdrant, MongoDB, ClickHouse, and InfluxDB end-to-end
tests should be added as opt-in integration suites when those services are part
of the deployment topology. Neo4j already has a separate opt-in suite.
