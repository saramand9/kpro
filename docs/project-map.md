# Project Map

This map explains how the repository is organized and where to start when
changing a runtime behavior. It is intentionally higher level than package
documentation and lower level than the README.

## System Shape

KPro separates application assembly from protocol implementation:

```text
server.Server
  -> endpoint or runner mounting
  -> transport implementation
  -> codec or native protocol bridge
  -> core.Router or protocol-native handler
  -> core.Invocation middleware
  -> application handler
```

Message-based transports use `core.Message` and `core.Context`. HTTP, SSE, and
gRPC keep protocol-native handler surfaces where that is the clearer contract
for application code.

## Top-Level Packages

| Path | Primary role | Key files |
| --- | --- | --- |
| `core/` | Transport-neutral runtime primitives, message dispatch, and optional session capabilities. | `router.go`, `router_config.go`, `context.go`, `session_lifecycle.go`, `outbound_state.go`, `outbound_buffer.go`, `invocation.go`, `backpressure.go`, `lifecycle.go` |
| `server/` | Dependency-neutral process owner for mounted transport contracts, middleware, readiness, component error provenance, and managed resources. | `server.go`, `mount.go`, `component_error.go`, `lifecycle.go` |
| `transport/` | Shared transport contracts, optional quiesce/readiness capabilities, handler-error policy, public extension helpers, capability descriptors, validation, built-in protocol packages, and their private dispatch/outbound controls. | `transport.go`, `handler_error.go`, `transportkit/`, `capability.go`, `validation.go`, `internal/dispatchgate/`, `internal/outbound/` |
| `codec/` | Binary protocol implementation and optional segmented-frame contract for KPro message traffic. | `kpro.go`, `kpro_segmented.go`, `segmented.go`, `codec.go` |
| `middleware/` | Reusable invocation middleware. | `recover.go`, `ratelimit.go`, `limit.go`, `metrics.go` |
| `observability/` | Optional integrations for health, Prometheus, OTel, and Zap. | `healthx/`, `prometheusx/`, `otelx/`, `zapx/` |
| `eventing/` | Event envelopes, delivery store contracts, GORM outbox, projector runner. | `events/`, `delivery/`, `projector/` |
| `database/` | Database adapters and lifecycle/resource helpers. | `gorm/`, `redis/`, `es/`, `qdrant/`, `mongo/`, `clickhouse/`, `influx/`, `neo4j/`, `common/` |
| `cmd/kpro/` | CLI for minimal/unified service scaffolding, versioned local overlays, read-only contract catalogs, transactional API additions, generation, and version/environment diagnostics. | `main.go`, `scaffold.go`, `scaffold_template.go`, `command_catalog.go`, `command_add.go`, `command_codegen.go`, `service_project.go`, `templates/profiles/service/`, `templates/service-project/` |
| `internal/codegen/` | Safe generation planning, manifests, content-addressed locks, compare-and-swap file transactions, descriptor-only protobuf inspection, and OpenAPI, protobuf, Message, and plugin backends. | `transaction.go`, `manifest/`, `openapi/`, `protogen/`, `messagegen/`, `pluginhost/` |
| `cmd/benchguard/` | Multi-sample benchmark budget checker used by CI; latency uses a median while memory limits apply to every sample. | `main.go` |
| `examples/` | Protocol and modular application examples. | `quickstart/`, protocol-specific directories |
| `benchmark/` | Deterministic hot-path and protocol benchmarks, budget config, and comparison fixtures. | `budgets.json`, `codec_benchmark_test.go`, `protocol_benchmark_test.go`, `compare/` |
| `docs/` | Guides for adoption, operation, extension, internal flows, and performance evidence. | `README.md`, `quickstart.md`, `golden-path.md`, `operations.md`, `performance-fast-path.md` |

The compile-time dependency rules for these packages are documented in
[Dependency Boundaries](dependency-boundaries.md).

## Runtime Flow

Server startup follows this path:

```text
application main
  -> server.NewChecked
  -> construct selected concrete transports
  -> app.Use / app.Mount / app.MountRunner / app.Manage
  -> app.Start(parent context)
  -> claim one-shot lifecycle
  -> validate mounted components
  -> start mounted runners
  -> await transport.ReadinessProvider signals
  -> RunHandle.WaitReady returns success or startup failure
```

Shutdown follows this path:

```text
run-context cancellation, explicit Shutdown, or runner failure
  -> request bounded runner shutdown
  -> stop accepting new sessions/listeners
  -> cancel endpoint contexts
  -> perform protocol-specific graceful work where supported
  -> wait boundedly for serving goroutines to stop
  -> close managed resources after successful runner stop
```

A message `server.Endpoint` has an additional ordered shutdown boundary:

```text
Endpoint.Shutdown(ctx)
  -> optional transport.Quiescer stops new transport dispatch
     while preserving established outbound writers
  -> Router stops new admission
  -> Router drains already accepted sync and async handlers within its bound
  -> terminal message transport Shutdown(ctx)
```

Each later phase is still attempted if quiesce or Router drain returns an
error. TCP, KCP, QUIC, UDP, and WebSocket implement the optional first phase.
A third-party transport without it skips directly to Router drain, so its read
loop may remain active during that compatibility fallback. KCP cannot close
its listener during quiesce because established sessions share that UDP socket;
it accepts and closes new raw sessions and defers listener close to terminal
shutdown. QUIC closes its connection listener but accepts and cancels new
streams on retained connections until terminal shutdown.

Even a successful Router drain only means accepted handlers completed while
sessions were available for their final send attempts. Message transports do
not promise reply drain: outbound sends and replies are at-most-once, and
shutdown does not confirm queued or in-flight delivery to the peer.

Message transport dispatch follows this path:

```text
client bytes/datagram/frame
  -> transport session
  -> codec.KProV1 decode
  -> core.Router dispatch
  -> queue and in-flight admission
  -> core.Invocation middleware chain
  -> registered message handler
  -> core.ContextOf(ctx) reads the optional core.ContextProvider capability
  -> ctx.Reply or session.Send
  -> reserve one pending-write slot and estimated encoded bytes
  -> codec encode
  -> reconcile actual encoded bytes and publish to the outbound queue
  -> transport write
```

HTTP-style dispatch follows this path:

```text
HTTP client
  -> httpx or http3 endpoint
  -> method/path route lookup
  -> core.Invocation middleware bridge
  -> httpx.Context handler
  -> core.ErrorKind to safe HTTP status/message
  -> response writer
```

gRPC dispatch follows this path:

```text
gRPC client
  -> grpcx server
  -> unary or stream interceptor bridge
  -> core.Invocation middleware chain
  -> core.ErrorKind to safe gRPC status/message
  -> registered gRPC service handler
```

## Lifecycle And Backpressure Controls

Use `server.NewChecked` for construction-time validation and `Server.Start` to
claim one lifecycle generation and aggregate transport `Validate` failures
before any serving goroutine starts. Use the returned
`RunHandle.WaitReady` before publishing application readiness; `Run` remains the
blocking form. Built-in listeners expose `transport.ReadinessProvider`; legacy
custom runners fall back to serving-method entry. Built-in servers and
transports are one-shot: repeated starts return `core.ErrAlreadyStarted`.

Serving and shutdown failures use `server.ComponentError` to preserve component
and phase alongside the original error chain. Protocol-facing handler failures
use `core.ErrorKind`; internal causes remain available to logs and traces while
HTTP, gRPC, and message responses use stable non-sensitive text.

Use `core.RouterConfig` when message traffic needs explicit worker, queue,
per-session in-flight, global in-flight, waiting-dispatch, idle, and shutdown
behavior. Under `BackpressureWait`, blocked dispatches default to 1024
Router-wide and 16 per session. One dispatch holds one ticket across every
global, per-session, and queue wait stage; Snapshot and Prometheus distinguish
global from per-session ticket rejection. Treat Router shutdown as bounded
handler completion rather than wire-level reply delivery, and treat Router and
transport limits as admission controls, not business validation.

`Router.Shutdown` drains accepted sync and async work, including accepted sync
tasks observed after the session closes during graceful drain.
`Router.Close` remains immediate best-effort sync-queue cancellation and does
not wait; accepted async work and a sync task already ready in a scheduler race
can still start.

TCP, KCP, QUIC message streams, UDP peers, and WebSocket sessions share the
same internal outbound accounting model: `SendQueueSize` caps pending writes
and `MaxPendingBytes` caps estimated/actual bytes across encoding
reservations, queued writes, and the writer-owned write. The internal queue
tracks high/low-watermark hysteresis and the five built-ins expose a public
point-in-time view through `core.SessionOutboundState`, including
current/maximum waiting writers. They also expose the optional,
context-cancelable `core.SessionWaitOutboundWritable` capability, bounded to
`SendQueueSize` waiters per session by default. `Writable` and a successful
wait are advisory and reserve no capacity; `Send` is the authoritative
admission operation.

HTTP-style endpoints require explicit timeout and body-size decisions. The
production checklist records which limits are built in and which remain
application or platform work.

## Extension Points

Add a transport by implementing the common contracts in `transport/`, including
descriptors, validation, and listener readiness. Reuse bounded public helpers
from `transport/transportkit` rather than importing `transport/internal`.
Custom sessions with outbound queues may implement the optional
`core.OutboundStateProvider` and `core.OutboundWritableWaiter`; custom
message transports may implement `transport.Quiescer`. The base
`core.Session` and `transport.MessageServer` contracts remain unchanged.
Follow [extending-kpro.md](extending-kpro.md) and
mirror existing package tests for lifecycle, capacity, closed-session sends,
handler-error policy, readiness ordering, and cancellation.

Add middleware through `core.Middleware` and `core.Invocation`; protocol bridges
should preserve stable operation names and bounded metadata.

Add database behavior behind module-owned ports where possible. The framework
adapters provide lifecycle and infrastructure primitives, but business modules
should avoid depending directly on concrete storage types.

## Evidence Files

- [framework-quality-assessment.md](framework-quality-assessment.md) records
  the current scored quality baseline, its evidence, and reassessment rules.
- [production-readiness.md](production-readiness.md) defines built-in,
  configure-and-verify, and external responsibilities.
- [operations.md](operations.md) records overload, failure, and capacity
  planning guidance.
- [api-stability.md](api-stability.md) defines the current pre-1.0 compatibility
  posture.
- [benchmark/budgets.json](../benchmark/budgets.json) defines hot-path
  regression budgets.
- [CHANGELOG.md](../CHANGELOG.md) records repository-level changes.
- [.github/workflows/ci.yml](../.github/workflows/ci.yml) defines checksum,
  test, race, vulnerability, coverage, benchmark, database, Windows, and Go
  compatibility gates.
- [.github/workflows/release.yml](../.github/workflows/release.yml) and
  [.goreleaser.yml](../.goreleaser.yml) define tag-triggered artifacts,
  checksums, per-archive SBOM generation, verification, and publication.
