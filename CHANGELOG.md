# Changelog

All notable KPro changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versioned releases
will follow Semantic Versioning as described in
[docs/api-stability.md](docs/api-stability.md).

KPro follows pre-1.0 semantic versioning. See the API stability policy before
upgrading between minor versions.

## Unreleased

### Added

- `kpro new --profile service` composes repeatable HTTP, gRPC, and Message APIs
  in one generated application; Message scaffolds support explicit TCP, KCP,
  QUIC, UDP, and WebSocket transport selections.
- `kpro add --api grpc` extends a current unified `service` project through a
  dry-run-capable, compare-and-swap transaction. It adds the protobuf contract,
  manifest source, endpoint assembly, generated bindings, and generation lock
  without rewriting the developer-owned process entry point.
- A repeatable framework quality assessment records the current technical
  scores, evidence, limitations, and reassessment rules.
- `server.MountNative` and `server.MountNativeChecked` preserve concrete native
  runner types while mounting them through the dependency-neutral lifecycle.
- A source-level dependency boundary test prevents `server` production code
  from importing concrete transport implementations.
- `core.RouterConfig.ErrorObserver` receives payload-free handler, panic, and
  error-response diagnostics while preserving original error chains.

### Changed

- The unified `service` scaffold replaces the pre-release `http-api` profile.
  Generation now runs in a sibling staging directory, publishes only after all
  bindings succeed, and preflights the required protobuf tools for gRPC.
- Unified service scaffolds record API and Message-transport selections in
  `kpro.service.yaml`; a stable `cmd/<service>/main.go` delegates endpoint
  assembly to `internal/kproapp`, so later supported additions do not patch
  application startup code.
- `server` no longer imports every built-in protocol. Applications now create
  selected transports in their own packages and attach message transports with
  `Server.Mount` or native runners with `MountNative`/`MountRunner`. This is a
  pre-1.0 source change: replace `app.HTTP(...)`, `app.TCP(...)`, and the other
  protocol convenience methods with explicit protocol construction and
  mounting. Runtime lifecycle, middleware, readiness, validation, and shutdown
  semantics are unchanged.
- The reference app now exposes distinct `/livez` and `/readyz` probes, keeps
  `/health` as a readiness alias, and applies conservative driver-specific SQL
  connection-pool defaults with command-line overrides.
- Generated services expose `/livez`, `/readyz`, and a `/healthz` readiness
  alias instead of presenting one ambiguous static health response.

### Fixed

- HTTP and HTTP/3 now install `MaxBodyBytes` on the native request before
  middleware and handlers run, so reading `Request().Body` cannot bypass the
  endpoint limit and all consumers share one request budget.
- Cancellation-triggered shutdown failures from HTTP/3, gRPC, and WebSocket
  serving are now returned to the caller and remain discoverable through
  `errors.Is`/`errors.As` instead of being silently discarded.
- SSE `WriteTimeout` now bounds the initial stream flush and each
  `Context.Send` write and flush, renewing the deadline per event instead of
  expiring the whole long-lived response.
- The reference app applies recovery and metrics middleware to every mounted
  transport and reports readiness only while the whole server is running and
  its configured resources are healthy.
- Router handler and panic paths no longer discard failures while sending the
  safe `MsgError` response; observers receive the response failure and rejected
  dispatches retain both errors through `errors.Is`/`errors.As`.
- HTTP, HTTPS, and SSE force-close remaining non-hijacked HTTP connections when
  graceful shutdown fails or reaches its deadline, while preserving the
  shutdown error for the caller.
- The reference app no longer reports readiness while an enabled SQL resource
  is unhealthy, and it no longer leaves the SQL pool unbounded by default.

### Security

- Upgraded `google.golang.org/grpc` to v1.82.1 to address GO-2026-6061; the
  required protobuf, genproto, and OpenTelemetry dependency versions move with
  that security update.

## [0.1.0] - 2026-07-24

Initial developer release of the KPro application framework.

### Added


- Explicit server/router lifecycle states and bounded shutdown behavior.
- Global and per-session router admission limits with reject/wait policies and
  runtime snapshots.
- Transport descriptors, startup validation, and capability discovery.
- Process diagnostic identity through `Server.Name` and mounted transport
  inventory through `Server.Descriptors`.
- OpenTelemetry tracing, Prometheus metrics, zap structured logging, and
  bounded health probes as optional packages.
- Transactional outbox append, fenced lease settlement, batch renewal,
  dead-letter replay/audit, backlog statistics, and projector health snapshots.
- `kpro new` project scaffolding with an explicit/auto-detected local KPro
  replacement, plus CI-enforced hot-path performance budgets.
- Quick Start, Golden Path, production-readiness, operations, API stability,
  and extension documentation.
- Runnable KCP and QUIC message server/client examples, including local-only
  in-memory TLS setup for QUIC.
- Race-free asynchronous startup through `Server.Start` and per-generation
  `RunHandle` readiness/completion waits, plus component/phase lifecycle errors.
- Optional message `ContextProvider` and `core.ContextOf`, preserving custom
  `core.Context` compatibility while propagating deadlines, tracing, and
  cancellation into handlers.
- HTTP and HTTP/3 `ServeMux` patterns, path parameters, route groups,
  route-local middleware, and checked route registration.
- Public `transport/transportkit` extension helpers and package-level API docs
  for the core framework surfaces.
- `kpro version` and `kpro doctor`, dependency-update automation, security
  policy, coverage/vulnerability gates, and verified checksum/SBOM release
  automation.

### Changed


- `Server` is now one-shot. Configuration freezes when startup or shutdown
  claims the lifecycle; late `Mount`, `MountRunner`, `Manage`, and `Use` calls
  panic with `ErrConfigurationLocked`. Code that may race startup should migrate
  to `MountChecked`, `MountRunnerChecked`, `ManageChecked`, and `UseChecked` and
  handle the returned error.
- Checked middleware/mount operations are serialized; a concurrent or
  reentrant mutation returns `ErrConfigurationBusy` and can be retried.
- Transport contracts now expose `Component`, `MessageServer`,
  `NativeRunner`, and `MiddlewareTarget`; the former `Server` and `Runner`
  names remain source-compatible aliases.
- Built-in transports validate address syntax, negative limits/timeouts, and
  TLS certificate selection before binding; direct `ListenAndServe` calls run
  the same preflight as server orchestration.
- Built-in transport instances are one-shot: concurrent/repeated starts return
  `core.ErrAlreadyStarted`, and shutdown closes the registration gate before
  listeners or sessions are swept. Context-cancellation watchers now exit when
  serving returns, including Background-context/manual-shutdown usage.
- WebSocket now has a default and configurable `MaxConnections` ceiling;
  excess upgrade requests receive HTTP 503 before hijacking, and
  `HandshakeTimeout` bounds incomplete HTTP upgrade headers.
- SSE now has a default and configurable `MaxStreams` ceiling plus bounded
  header-read and idle keep-alive defaults. Active SSE request contexts are
  cancelled before graceful shutdown so the recommended `ctx.Done()` handler
  pattern drains normally.
- HTTP/3 now creates and owns its QUIC listener explicitly so startup
  cancellation and concurrent shutdown cannot strand the UDP socket.
- HTTP and HTTP/3 now apply safe zero-value header/body/time limits and redact
  unclassified handler failures; HTTP/3 0-RTT is disabled unless explicitly
  enabled and both HTTP/3 and QUIC enforce physical connection ceilings.
- TCP, KCP, QUIC, UDP, and WebSocket now define handler-error policy and
  observation hooks, default to closing the affected session, report full send
  queues consistently, and publish readiness only after successful binding.
- TCP, KCP, QUIC, and WebSocket sends now deterministically return
  `core.ErrClosed` after session close instead of occasionally queueing an
  undeliverable frame.
- The Router now applies a default global in-flight ceiling. `BackpressureWait`
  reentrant dispatch must use a bounded `DispatchContext`.
- Middleware chains are compiled per route/configuration snapshot instead of
  being rebuilt for every invocation.
- Message contexts are pooled; background work must retain a `CloneContext`
  snapshot rather than the request-scoped context.
- Connection-aware rate limiting now keys physical connection lifetime,
  bounds bucket cardinality, and evicts idle state without collapsing distinct
  transports or peers into one bucket.
- Framework error responses now use stable protocol-neutral classifications;
  internal, recovered, and unknown details are never copied to HTTP, SSE,
  gRPC, or KPro wire responses.
- Packet decoding rejects trailing bytes instead of silently accepting more
  than one frame.
- Projector registration and execution validate inputs, isolate panics, and
  publish registry snapshots atomically. Delivery scans materialize bounded
  batches and include supporting migration indexes.
- Caller-supplied GORM and Elasticsearch transports remain caller-owned;
  ClickHouse transactions roll back after panics; Redis locks synchronize TTL
  and key prefix use, and stream reads/trimming expose explicit bounds.
- SQL outbox lease decisions use the database UTC clock.
- Dead-letter replay keeps lease attempt receipts monotonic across generations,
  while `last_dead_attempts` restarts the retry budget for each explicit replay.
- The module baseline is Go 1.25.12. quic-go, gRPC, pgx, ClickHouse, Redis,
  `x/net`, and `x/text` were upgraded to patched versions; quic-go integrations
  use the v0.59 connection, stream, listener, and HTTP/3 transport APIs.
- Passing more than one configuration to `server.New` or `core.NewRouter`, and
  invalid unchecked route registration, now panics instead of silently ignoring
  extra/invalid input. Checked constructors and registration methods return the
  corresponding error. `core.ErrClosed` now uses component-neutral wording.

### Deprecated

- `delivery.GORMStore.Ack` and `Fail` are compatibility-only for the first
  attempt. Lease-aware consumers should use `AckAttempt` and `FailAttempt`.

### Fixed

- gRPC always applies the safe error boundary, including calls without
  middleware, and does not expose details from wrapped status/context errors.
- HTTP/3 closes the QUIC listener before gracefully draining accepted
  connections, preventing new handshakes during shutdown.
- WebSocket validates request method/version, singleton handshake headers,
  origins, close payloads, and close replies at the protocol boundary.
- UDP rejects oversized datagrams instead of dispatching a truncated prefix.
- Duplicate HTTP/message routes and nil middleware are rejected without
  replacing or corrupting existing configuration.
- Redis lock objects use a cryptographically random immutable token for one
  successful acquisition, so stale owners cannot release a replacement lease.

### Known limitations

- TCP, KCP, QUIC, UDP, and WebSocket shutdown is a bounded stop, not a reply
  delivery barrier. Outbound replies are at-most-once; applications requiring
  delivery confirmation need acknowledgements, retries, and idempotency.
- `database/redis.Lock` is a single-node TTL lease, not a fencing or quorum
  lock. An `Acquire` transport failure can leave the `SET NX` outcome unknown.
- WebSocket can close immediately for some malformed frames rather than first
  sending close code 1002, and server shutdown does not perform a full peer
  close-handshake exchange.
- Repository tests and microbenchmarks are not workload-specific production
  certification; deployments must run their own load, failure, and recovery
  exercises.

### Validation

- Default, race, vet, Linux/Windows scaffold smoke, real MySQL/PostgreSQL
  build-tag, benchmark comparison, and performance-budget suites are
  configured in CI.
- Repository tests are evidence of implemented behavior, not a claim of
  workload-specific production certification.

[Unreleased]: https://github.com/saramand9/kpro/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/saramand9/kpro/releases/tag/v0.1.0
