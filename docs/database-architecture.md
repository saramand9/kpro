# Database And Eventing Architecture

KPro separates storage adapters from event flow mechanics:

```text
database  = concrete storage clients and lifecycle resources
eventing  = events, delivery contracts, and projector scheduling
modules   = business repositories, business events, and concrete projectors
```

Transactional MySQL data should use GORM directly in module repositories. Secondary stores such as
Redis, Elasticsearch, Qdrant, MongoDB, ClickHouse, and InfluxDB stay as concrete adapters.

![Database architecture flow](assets/database-architecture-flow.svg)

## Name Map

| Package | Meaning |
| --- | --- |
| `database/contracts/lifecycle` | Common resource lifecycle, health, and observation contracts. |
| `database/contracts/readmodels` | Query-side capabilities such as cache, search, vector, analytics, and metrics. |
| `eventing/events` | Immutable business events, transport envelopes, codecs, and registry. |
| `eventing/delivery` | Reliable event delivery contract only; concrete storage is chosen by the application. |
| `eventing/projector` | Projector interface, registry, and runner. |

The storage adapters keep their database names:

```text
database/relational/gorm        GORM database resource around native gorm.DB
database/cache/redis            Redis cache, lock, stream, and sorted-set adapter
database/search/elasticsearch   Elasticsearch index, search, and bulk adapter
database/vector/qdrant          Qdrant vector collection adapter
database/document/mongodb       MongoDB document adapter
database/analytics/clickhouse   ClickHouse analytical SQL adapter
database/metrics/influxdb       InfluxDB metrics and Flux query adapter
```

## GORM Write Flow

The default relational write path is intentionally direct:

```text
HTTP/gRPC/TCP handler
-> module service
-> module store port
-> internal/modules/<name>/store
-> database/relational/gorm.Database.GORM()
-> native gorm.DB Create / Save / Delete / First / Find / AutoMigrate
-> MySQL
```

`database/relational/gorm` only opens, wraps, checks, and closes native GORM handles. Module repositories use GORM's own API for CRUD, transactions, and migrations.

## Business Projector Boundary

`eventing/projector` only owns the generic scheduling mechanics:

```text
Projector interface
Registry
Runner
```

Concrete projectors belong to business modules because they know business event semantics and target data shape:

```text
internal/modules/orders/events.go
internal/modules/orders/projectors/search.go     -> writes Elasticsearch
internal/modules/orders/projectors/cache.go      -> writes Redis
internal/modules/orders/projectors/analytics.go  -> writes ClickHouse
```

The database adapters only execute storage operations. They do not decide whether
`order.created` should update Redis, Elasticsearch, ClickHouse, or nothing.

## Adapter Flow Examples

### GORM SQL Path

```text
handler
-> service
-> module store port
-> module GORM repository
-> database/relational/gorm.Database
-> gorm.DB
-> MySQL
```

Typical use:

```text
gormResource := gormstore.Open(...)
stores := NewGORMStores(gormResource)
users service calls users.Store
users.Store calls gorm.DB directly
```

### Redis Path

```text
service or projector
-> database/cache/redis.Cache[T] / Lock / Stream / ZSet
-> go-redis client
-> Redis
```

### Elasticsearch Path

```text
service or projector
-> database/search/elasticsearch.Index[T] / SearchRaw / BulkService
-> go-elasticsearch client
-> Elasticsearch
```

### Qdrant Path

```text
service or projector
-> database/vector/qdrant.Collection[T]
-> Qdrant HTTP API
-> Qdrant collection
```

### MongoDB Path

```text
service or projector
-> database/document/mongodb.Collection[T]
-> mongo-driver
-> MongoDB collection
```

### ClickHouse Path

```text
analytics projector or report service
-> database/analytics/clickhouse.AnalyticsTable[T] or SQL/Tx
-> clickhouse-go / database/sql
-> ClickHouse
```

### InfluxDB Path

```text
metrics service or projector
-> database/metrics/influxdb.Writer / Measurement[T] / Query
-> influxdb-client-go
-> InfluxDB bucket
```

## Runtime Lifecycle

Every database adapter that owns an external connection implements
`database/contracts/lifecycle.Resource` where practical:

```text
Name()
Kind()
Ping(ctx)
Health(ctx)
Close()
```

This gives startup, health checks, and graceful shutdown one common contract without forcing all databases into one CRUD abstraction.
