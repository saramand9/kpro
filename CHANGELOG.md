# Changelog

All notable KPro changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versioned releases
will follow Semantic Versioning as described in
[docs/api-stability.md](docs/api-stability.md).

KPro is currently pre-1.0 and has no published release tag. Entries under
`Unreleased` describe repository behavior, not a released compatibility promise.

## Unreleased

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
- TCP, KCP, QUIC, and WebSocket sends now deterministically return
  `core.ErrClosed` after session close instead of occasionally queueing an
  undeliverable frame.
- The Router now applies a default global in-flight ceiling. `BackpressureWait`
  reentrant dispatch must use a bounded `DispatchContext`.
- Middleware chains are compiled per route/configuration snapshot instead of
  being rebuilt for every invocation.
- Message contexts are pooled; background work must retain a `CloneContext`
  snapshot rather than the request-scoped context.
- Packet decoding rejects trailing bytes instead of silently accepting more
  than one frame.
- SQL outbox lease decisions use the database UTC clock.
- Dead-letter replay keeps lease attempt receipts monotonic across generations,
  while `last_dead_attempts` restarts the retry budget for each explicit replay.

### Deprecated

- `delivery.GORMStore.Ack` and `Fail` are compatibility-only for the first
  attempt. Lease-aware consumers should use `AckAttempt` and `FailAttempt`.

### Validation

- Default, race, vet, Linux/Windows scaffold smoke, real MySQL/PostgreSQL
  build-tag, benchmark comparison, and performance-budget suites are
  configured in CI.
- Repository tests are evidence of implemented behavior, not a claim of
  workload-specific production certification.
