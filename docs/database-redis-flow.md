# Redis Data Flow

This guide uses Redis to show how data moves through KPro's database
architecture. The adapter under `database/redis` is implemented, but an
application module still owns the projector, repository, or service code that
decides when Redis is appropriate.

![Redis database flow](assets/database-redis-flow.svg)

## Core Model

Redis is normally a read model rather than the transactional source of truth:

```text
source-of-truth write succeeds
-> event is stored in the application's delivery store
-> projector consumes the event
-> Redis cache / stream / sorted set is updated
-> read requests may prefer Redis
```

A service may also call the Redis adapter directly when it explicitly needs a
short-lived cache, lock, stream, or sorted set.

## Flow Overview

| Flow | Purpose | Problem addressed | Out of scope |
| --- | --- | --- | --- |
| Startup | Turn Redis configuration into a health-checked, closable resource. | Detect invalid configuration or an unreachable server before serving traffic. | Business data design and cache operations. |
| Projector write | Project committed events asynchronously into a Redis read model. | Avoid an unsafe direct dual write to GORM and Redis; make failures retryable. | Making Redis the transactional source or requiring Redis before the primary request completes. |
| Cache read | Prefer Redis for eligible queries and reduce source-of-truth load on a hit. | Lower read latency and SQL load. | Strong freshness guarantees; the service owns fallback behavior on misses and errors. |
| Other Redis capabilities | Reuse one client for locks, streams, and sorted sets. | Centralize key construction and command semantics. | Hiding every Redis command behind a generic CRUD abstraction. |
| Module projector example | Show how a business event becomes a Redis cache value. | Clarify the boundary between an adapter and a business projector. | Claiming that the sample projector is an implemented application feature. |

## Startup Flow

Startup turns configuration into a stable runtime resource. It validates the
configuration, creates or wraps a client, pings Redis, and exposes a common
`lifecycle.Resource` for health checks and shutdown. It answers whether the
application can reliably connect to Redis; it does not define a cache,
leaderboard, or stream schema.

| Step | File | Call | Input | Output | Responsibility |
| --- | --- | --- | --- | --- | --- |
| 1 | Application assembly, such as `internal/.../app` | Build `redisstore.Config` | `Name`, `Addr`, `DB`, `KeyPrefix`, optional `*redis.Client` | Configuration values | Select the Redis deployment used by this application. |
| 2 | `database/redis/config.go` | `normalizeConfig(config)` | Raw configuration | Normalized configuration or error | Apply the default name, clean the key prefix, require an address or client, and set the default ping timeout. |
| 3 | `database/redis/client.go` | `Open(ctx, config)` | Normalized configuration | `*redisstore.Client` | Create or wrap a go-redis client. |
| 4 | `database/redis/client.go` | `redis.NewClient(redisOptions(config))` | Address, credentials, and database | `*redis.Client` | Create the native client when the caller did not inject one. |
| 5 | `database/redis/client.go` | `client.Ping(ctx)` | Ping context | Error or nil | Confirm reachability before startup completes. |
| 6 | `database/common/lifecycle/resource.go` | `lifecycle.Resource` methods | `Client` | `Name`, `KindRedis`, `Health`, `Close` | Participate in the same health and shutdown lifecycle as other database resources. |

Example assembly:

```go
redisClient, err := redisstore.Open(ctx, redisstore.Config{
	Name:      "redis-cache",
	Addr:      "127.0.0.1:6379",
	DB:        0,
	KeyPrefix: "kpro:",
})
userCache := redisstore.Cache[UserCache](redisClient, "users:")
```

Result:

```text
redisClient.Name() = "redis-cache"
redisClient.Kind() = lifecycle.KindRedis
userCache key prefix = "kpro:" + "users:"
```

## Projector Write Flow

This flow reliably projects a committed source-of-truth change into a Redis
read model. The important property is not that Redis is updated immediately;
it is that the event survives the primary write and a failed Redis update can
be retried.

When GORM SQL owns transactional state and Redis owns a cache view, the primary
request only needs to commit the SQL state and append an event to the
application's delivery store. A background projector can bring Redis up to date.

| Step | File | Call | Input | Output | Responsibility |
| --- | --- | --- | --- | --- | --- |
| 1 | Module handler | `CreateUser(...)` entry point | HTTP, gRPC, or Message request | Command DTO | Decode transport data into a business command. |
| 2 | Module service | Service method | Command DTO | Domain model and event | Enforce business rules and decide what changed. |
| 3 | `eventing/events/codec.go` | `JSONCodec.Encode(event)` | Typed event | JSON bytes | Encode the event payload. |
| 4 | `eventing/events/event.go` | `NewEnvelope(id, event, payload, occurredAt)` | Event ID, typed event, payload | `events.Envelope` | Add type, aggregate ID, schema version, and timestamp metadata. |
| 5 | Module GORM repository | `db.Transaction(...)` | Domain model and event | SQL row and outbox row | Persist domain state and call `GORMStore.AppendTx` in one transaction. |
| 6 | `eventing/delivery.GORMStore` | `AppendTx(ctx, tx, envelope)` | GORM transaction and envelope | Durable event record | Let the caller commit or roll back both rows together. |
| 7 | `eventing/projector/runner.go` | `Runner.Run(ctx)` | Delivery store and projectors | Lease renewal and fenced settlement until cancellation | Fetch deliverable events, renew leases before and during long work, and process continuously. `RunOnce` remains available for tests and external schedulers. |
| 8 | `eventing/delivery/store.go` | `Store.Fetch(ctx, consumer, limit)` | Projector name and batch size | `[]delivery.Record` | Create delivery rows and claim eligible events as leased `inflight` work that can be recovered after a crash. |
| 9 | Module projector, such as `UserCacheProjector` | `Handle(ctx, envelope)` | `delivery.Record.Envelope` | Redis cache command | Decode the payload and map the event to a Redis read model. |
| 10 | `database/redis/cache.go` | `CacheView.Set(ctx, key, value, ttl)` | Logical key, typed value, TTL | Error or nil | Marshal the value as JSON and issue Redis `SET`. |
| 11 | `database/redis/client.go` | `Client.key(key, prefix)` | Key `u_1001` and prefix `users:` | `kpro:users:u_1001` | Apply the application and cache-specific prefixes. |
| 12 | go-redis | `SET key json ttl` | Final Redis key and JSON bytes | Redis reply | Send the Redis protocol command. |
| 13 | `eventing/projector/runner.go` | `AckAttempt(...)` or `FailAttempt(...)` | Projector, event ID, and attempt | Updated delivery state | Prevent a stale worker from settling a newer attempt; retry failures or move them to dead-letter state. |

Concrete data types:

```go
type UserCreated struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (e UserCreated) EventType() string   { return "user.created" }
func (e UserCreated) AggregateID() string { return e.ID }

type UserCache struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
```

At the event boundary:

```text
typed event input:
UserCreated{ID:"u_1001", Name:"Ada", Email:"ada@example.com"}

payload after events.JSONCodec.Encode:
{"id":"u_1001","name":"Ada","email":"ada@example.com"}

envelope after events.NewEnvelope:
ID: evt_9001
Type: user.created
AggregateID: u_1001
SchemaVersion: 1
Payload: the JSON bytes above
```

At the delivery boundary:

```text
input:
events.Envelope{ID:"evt_9001", Type:"user.created", AggregateID:"u_1001", ...}

output delivery-store record:
id=evt_9001
type=user.created
aggregate_id=u_1001
payload={"id":"u_1001","name":"Ada","email":"ada@example.com"}

Fetch then creates or claims a delivery record:
consumer=redis.user-cache
event_id=evt_9001
status=inflight
```

At the projector boundary:

```text
input:
delivery.Record{
  Consumer: "redis.user-cache",
  Status: inflight,
  Envelope: evt_9001/user.created/u_1001
}

work:
Decode Envelope.Payload -> UserCreated
Map UserCreated -> UserCache
Call cache.Set(ctx, "u_1001", UserCache{...}, 30m)

output:
Redis write request; on failure, Runner calls FailAttempt for this attempt.
```

At the Redis adapter boundary:

```text
CacheView.Set input:
key = "u_1001"
value = UserCache{ID:"u_1001", Name:"Ada", Email:"ada@example.com"}
ttl = 30m

cache.go:
json.Marshal(value)

client.go key construction:
KeyPrefix "kpro:" + cache prefix "users:" + key "u_1001"

Redis command:
SET kpro:users:u_1001 {"id":"u_1001","name":"Ada","email":"ada@example.com"} EX 1800
```

On success:

```text
Runner calls delivery.AttemptStore.AckAttempt(
  ctx, "redis.user-cache", "evt_9001", attempt,
)
delivery status = delivered
```

If Redis is unavailable or JSON marshaling fails:

```text
CacheView.Set returns an error
Runner calls delivery.AttemptStore.FailAttempt(
  ctx, "redis.user-cache", "evt_9001", attempt, err,
)
status = failed, available_at = now + RetryDelay
the next claim increments attempts
status = dead after MaxAttempts
```

The built-in `Runner` renews a lease before calling a projector and continues
renewing during long processing. If another worker has reclaimed the lease,
renewal or fenced settlement returns `delivery.ErrLeaseLost`, so the stale
worker cannot overwrite the newer attempt. Direct `Fetch` callers should also
use `AckAttempt` and `FailAttempt`. `GORMStore.Ack` and `GORMStore.Fail` only
retain their compatibility semantics for the first delivery attempt.

## Redis Cache Read Flow

Eligible reads may prefer a Redis view when a small amount of projection lag is
acceptable. The business service decides whether a miss may fall back to the
source of truth, whether a Redis error should degrade gracefully, and whether a
query requires strong consistency.

| Step | File | Call | Input | Output | Responsibility |
| --- | --- | --- | --- | --- | --- |
| 1 | Module handler | `GetUser(id)` entry point | User ID `u_1001` | Query DTO | Decode a path or query parameter. |
| 2 | Module service | Cache-first query | `u_1001` | Cached view or miss | Decide whether this query may trust Redis first. |
| 3 | `database/common/readmodels/views.go` | `Cache[T]` interface | `Get(ctx, key)` contract | Typed value, hit flag, error | Keep the service independent of Redis when an abstraction is useful. |
| 4 | `database/redis/cache.go` | `CacheView.Get(ctx, key)` | Logical key `u_1001` | `UserCache`, hit flag, error | Build the key, call Redis `GET`, and decode JSON. |
| 5 | `database/redis/client.go` | `Client.key(key, prefix)` | Logical key and prefix | Final Redis key | Apply the `kpro:users:` prefix. |
| 6 | go-redis / Redis | `GET kpro:users:u_1001` | Final key | Bytes, `redis.Nil`, or error | Read from Redis. |
| 7 | `database/redis/cache.go` | `json.Unmarshal` | Bytes | Typed `UserCache` | Decode the stored JSON. |
| 8 | Module service | Return or fall back | Hit or miss result | Response DTO or source query | Return a hit; optionally query SQL and refill the cache on a miss. |

The three outcomes are distinct:

```text
hit:
CacheView.Get -> UserCache{ID:"u_1001", Name:"Ada", Email:"ada@example.com"}, true, nil

miss:
Redis returns redis.Nil
CacheView.Get -> zero UserCache, false, nil

Redis or network failure:
CacheView.Get -> zero UserCache, false, err
```

## Other Redis Capabilities

The adapter groups common Redis behavior by semantics so application code does
not scatter raw `SETNX`, `XADD`, and `ZADD` calls. This is intentionally not a
generic Redis CRUD layer.

| Capability | File | Main calls | Data shape | Typical use |
| --- | --- | --- | --- | --- |
| Cache | `database/redis/cache.go` | `Get`, `Set`, `Delete`, `SetNX`, `Exists` | Typed values encoded as JSON | Read-model cache |
| Lock | `database/redis/lock.go` | `Acquire`, `Release`, `Refresh` | Key, random ownership token, TTL | Short-lived distributed coordination |
| Stream | `database/redis/stream.go` | `Add`, `Read`, `CreateGroup`, `ReadGroup`, `Ack` | Stream message as `map[string]any` | Redis Streams queue or event relay |
| Sorted set | `database/redis/zset.go` | `Add`, `Increment`, `Score`, `Range`, `RevRange`, `Remove` | String member and score | Leaderboards, priority views, and ranking |
| Lifecycle | `database/redis/client.go` | `Ping`, `Health`, `Close` | Resource health state | Startup validation and shutdown |

### Lock Correctness Boundary

A `Lock` is a single-acquisition object. Once `Acquire` succeeds, that object
cannot acquire another lease, even after `Release` or TTL expiry. Create the
next lease with `NewLock` or `Client.Lock`. A failed acquisition may be retried
on the same object with an application-owned bounded backoff policy.
`Release` and `Refresh` use Lua compare-and-delete/expire with a random
ownership token, so an old object cannot release or extend a later owner's
lease.

The ownership token is not a fencing token, and this implementation is not a
multi-node quorum or Redlock algorithm. After a process pause, network
partition, or operation that exceeds the TTL, an old owner may continue while
a new owner has already acquired the lease. Use it only for short coordination
where brief duplicate execution is acceptable and operations are idempotent or
compensatable.

For payments, inventory decrements, and other irreversible writes that require
strict exclusion, use a monotonic fencing version, transaction or unique
constraint, or concurrency control native to the protected source of truth.
Redis TTL locks alone cannot establish that guarantee. Validate TTL, renewal,
retry, and process-pause behavior against the target Redis topology.

A transport error from `Acquire` may also mean that the result is unknown:
`SET NX` might have reached Redis before its response was lost. Do not treat
the error as proof that no lease exists or immediately let another owner
perform non-idempotent work. Reconcile the protected state or, at minimum, wait
for the configured TTL to expire before recovery.

## Minimal Module Projector

The business module translates a generic `events.Envelope` into its own Redis
shape. `database/redis` supplies Redis capabilities; the projector understands
what `user.created` means, which key to write, and which TTL is appropriate.
Keep this code beside the module that owns the read model, not in
`database/redis`.

```go
type UserCacheProjector struct {
	cache readmodels.Cache[UserCache]
	codec events.JSONCodec
}

func (p UserCacheProjector) Name() string { return "redis.user-cache" }

func (p UserCacheProjector) Handle(ctx context.Context, envelope events.Envelope) error {
	if envelope.Type != "user.created" {
		return nil
	}
	var event UserCreated
	if err := p.codec.Decode(envelope.Payload, &event); err != nil {
		return err
	}
	value := UserCache{ID: event.ID, Name: event.Name, Email: event.Email}
	return p.cache.Set(ctx, event.ID, value, 30*time.Minute)
}
```

Application assembly:

```go
redisClient, _ := redisstore.Open(ctx, redisstore.Config{
	Addr:      "127.0.0.1:6379",
	KeyPrefix: "kpro:",
})
userCache := redisstore.Cache[UserCache](redisClient, "users:")
userProjector := UserCacheProjector{cache: userCache}
runner := projector.NewRunner(deliveryStore, userProjector)
```
