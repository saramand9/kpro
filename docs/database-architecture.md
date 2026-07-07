# Fact-Centric Database Architecture

The database layer treats business facts as the center of the system. Databases
are carriers for facts, projections, and query views.

![Database architecture flow](assets/database-architecture-flow.svg)

```text
modules produce facts
database moves facts reliably
database persists facts and views
```

## Layers

```text
modules/
  domain      aggregates, policies, facts
  app         use cases
  ports       business repositories and view interfaces
  adapters    HTTP, SQL repositories, projections

database/
  dbx         lightweight database/sql toolkit
  sql         dbx-backed SQL resource, unit of work, and SQL outbox
  gorm        GORM as a peer storage framework, not hidden inside dbx
  redis       Redis cache, lock, stream, and sorted-set adapter
  elastic     Elasticsearch index, search, and bulk adapter
  qdrant      Qdrant vector collection adapter
  mongo       MongoDB document adapter
  clickhouse  ClickHouse analytical SQL adapter
  influx      InfluxDB metrics and Flux query adapter
  kernel      lifecycle, health, errors, observation
  truth       unit of work and source-of-truth contracts
  facts       fact envelope, schema, codec, registry
  outbox      reliable fact delivery contracts
  projections projection runtime and registry
  views       cache/search/vector/graph/analytics/metrics interfaces
```

The existing `dbx` package remains the low-level SQL implementation. GORM lives
beside it under `database/gorm` as a separate framework adapter. New code should
place business persistence behind module-specific repository interfaces, then
use `database/dbx`, `database/gorm`, `database/sql`, Redis, Elasticsearch,
Qdrant, MongoDB, ClickHouse, or InfluxDB behind those interfaces.

## Create Flow

```text
HTTP request
-> module HTTP adapter decodes DTO
-> app use case validates command
-> domain aggregate creates a fact
-> truth.UnitOfWork
   -> SQL repository saves source-of-truth state
   -> outbox appends fact in the same transaction
-> response returns
-> projection runner delivers fact to cache/search/vector/graph/analytics views
```

This keeps strong consistency in the SQL source of truth and moves Redis,
Elasticsearch, Qdrant, Neo4j, ClickHouse, and InfluxDB updates into replayable
projections.

## Current Implementation Status

The current codebase has the first executable slice:

```text
database/kernel       Resource and health contracts
database/facts        Fact envelope, JSON codec, registry
database/outbox       Outbox Store contract
database/projections  Projection registry and RunOnce runner
database/views        Cache/Search/Vector/Graph/Analytics/Metrics contracts
database/sql         dbx Database wrapper, UnitOfWork, SQL Migrator, SQL OutboxStore
database/gorm        GORM Database wrapper and generic Store
database/redis       Redis Resource plus Cache/Lock/Stream/ZSet helpers
database/elastic     Elasticsearch Resource plus Index/Search/Bulk helpers
database/qdrant      Qdrant Resource plus Collection helpers
database/mongo       MongoDB Resource plus typed Collection helpers
database/clickhouse  ClickHouse Resource plus AnalyticsTable writer
database/influx      InfluxDB Resource plus Writer/Measurement/Query helpers
```

`database/sql.Migrator` records schema migrations in `schema_migrations` with a
checksum and status so changed, running, or failed migrations are visible.
`database/sql.OutboxStore` stores immutable facts in `outbox_facts` and tracks
delivery independently per projection consumer in `outbox_deliveries`. This
means one projection can ack a fact while another projection still has its own
pending delivery.

The outbox lifecycle is:

```text
Append facts
-> Fetch by projection name
-> claim pending or retryable deliveries as inflight
-> Projection.Handle
-> Ack on success
-> Fail on error
-> retry until MaxAttempts
-> dead-letter when attempts are exhausted
```

`inflight` deliveries use `ClaimTimeout` as a lease. If a projection runner
crashes, another runner can reclaim the delivery after the lease expires.

The existing `dbx.Store[T]` remains available as a low-level SQL helper. Module
services depend on business-shaped store interfaces, as the `users` and
`projects` modules now do. Framework-specific return values such as
`sql.Result` stay inside module store adapters under `internal/modules/*/store`.

GORM is no longer a hidden branch inside `dbx.Store[T]`. Startup code chooses the
implementation explicitly:

```text
NewDBXStores(database)       -> users/projects store ports backed by dbx
NewGORMStores(gormResource)  -> users/projects store ports backed by GORM
RegisterHTTPModules(endpoint, stores)
```
