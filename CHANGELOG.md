# Changelog

All notable KPro changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versioned releases
will follow Semantic Versioning as described in
[docs/api-stability.md](docs/api-stability.md).

KPro follows pre-1.0 semantic versioning. See the API stability policy before
upgrading between minor versions.

## Unreleased

### Fixed

- Elasticsearch bulk helpers reject malformed response JSON and missing, null,
  or incorrectly typed `errors` flags instead of reporting success. JSON decoding
  errors retain their original cause for `errors.As`.
- Server runs preserve the caller's cancellation cause when a component exits
  cleanly at the same time, including during startup. Cancellation is no longer
  reported as success or as an unrelated early component exit.

### Changed

- Message transports share writable-wait error mapping and seal-all-before-wait
  drain logic while retaining direct outbound-state mapping, protocol-specific
  session ownership, shutdown behavior, and drain statistics.
- CLI gRPC additions and generation execution separate project inspection,
  isolated generation, mutation planning, and transaction application while
  preserving generated-file ownership and concurrent-edit protection.

## [0.2.1] - 2026-10-19

### Security

- `httpx.Context` raw responses default an unspecified Content-Type to
  `application/octet-stream` and set `X-Content-Type-Options: nosniff` before
  committing headers. This prevents echoed HTML bytes from selecting an HTML
  response through automatic sniffing. Explicit response types and bytes remain
  unchanged; applications intentionally serving HTML must escape untrusted input.
- Real HTTP regression tests cover `Write`, `Bytes`, `Stream`, and `CopyBody`,
  plus preservation of explicit types, status codes and response bytes.

## [0.2.0] - 2026-10-19

### Added

- Open-source release preparation checklist, a pull request template, and
  migration notes for `v0.2.0`.

- A production-v1 roadmap records staged acceptance gates, current evidence
  limits, and the next implementation task. Repository agent instructions now
  require reading that roadmap at the start of each new session.
- Message sessions can optionally expose post-admission transport results
  through `core.SessionSendWithReceipt` and terminal per-session drain through
  `core.SessionDrainOutbound`. TCP, KCP, QUIC, UDP, and WebSocket implement
  both capabilities. `transport.OutboundDrainer` adds the corresponding
  Endpoint phase after Router drain and before terminal transport shutdown.
- `transport.OutboundSnapshotter` exposes low-cardinality, process-lifetime
  aggregate queue, write-result, byte, saturation, waiter, and drain statistics
  for the five built-in message transports. `prometheusx.OutboundCollector`
  exports them with only stable endpoint and transport labels. Hot counters
  are assigned to per-queue shards and aggregated only on snapshot collection
  to avoid a single cross-session atomic contention point.
- `transport/transporttest` provides a reusable live message-transport
  contract suite covering one-shot lifecycle, readiness, descriptors, message
  fields, session lifecycle and flow control, write receipts, quiesce/drain,
  and outbound snapshots. TCP, KCP, QUIC, UDP, and WebSocket run the same suite
  through protocol-native test peers.
- Protocol conformance coverage now cross-checks all KProV1 decode paths at
  every stream split boundary and under fuzzing. WebSocket coverage adds the
  RFC 6455 accept-key vector, reserved-opcode and canonical-length rejection,
  the 64-bit length high-bit rule, and bounded frame-parser fuzzing. SSE
  coverage includes CR, LF, and CRLF data-line handling.
- TCP resilience coverage now injects slow writes, gathered short writes,
  partial progress with concrete errors, transport-write versus drain deadline
  ordering, real half-close and RST, and bounded connection-fanout shutdown.
  Environment-enabled tests exercise at least 1,000 simultaneous real
  connections and configurable request/reply soak stability; a manual workflow
  preserves verbose race, stress, and soak logs as build artifacts.
- `transport/proxyproto` provides opt-in HAProxy PROXY protocol v1/v2 parsing
  with a mandatory trusted-proxy IP/CIDR allowlist, bounded header reads, pre-TLS listener
  wrapping, effective/direct address visibility, and connection-level reject
  observation. TCP, HTTP/HTTPS/HTTP2, fasthttp, WebSocket, SSE, and gRPC expose
  the integration while leaving their zero-value listener paths unchanged.
- `transport/fasthttpx` provides an opt-in fasthttp-native HTTP/1.1 runner with
  method/path routing, unified invocation middleware, checked limits,
  readiness, TLS restricted to HTTP/1.1, and bounded graceful shutdown. The
  existing `httpx` endpoint remains the standard-library and HTTP/2 path.
- `BackpressureWait` now bounds blocked dispatch callers independently from
  in-flight work. The defaults allow 1024 waiters Router-wide and 16 per
  session; one dispatch holds one ticket while crossing global, per-session,
  and queue admission stages. Snapshot/Prometheus counters distinguish global
  and per-session wait-limit rejection.
- Message sessions can optionally implement context-cancelable, bounded
  outbound-writability waiting through `core.SessionWaitOutboundWritable`.
  Built-in sessions cap waiting writers at `SendQueueSize` and expose the
  current/max waiter counts in `core.OutboundState`.
- Message transports can implement the optional `transport.Quiescer` shutdown
  phase. TCP, KCP, QUIC, UDP, and WebSocket stop new dispatch before accepted
  Router work drains while retaining established outbound writer paths.
- TCP, KCP, QUIC message streams, UDP peers, and WebSocket sessions now expose
  configurable `MaxPendingBytes` accounting alongside `SendQueueSize`, plus the
  optional `core.SessionOutboundState` high/low-watermark snapshot.
- Performance CI now requires three samples for Router, HTTP, outbound-queue,
  transport dispatch-gate, codec, and TCP send-admission hot paths; explicit
  zero budgets enforce allocation-free outbound accounting, batch drain,
  dispatch-gate, segmented KProV1 encoding, default KProV1 TCP send admission,
  and the in-memory TCP/WebSocket batch writer loops.
- `codec.StreamFrameCodec` lets fixed-header stream codecs expose a segmented
  header-plus-payload representation, while `core.SessionReserveOutboundBuffer`
  discovers an optional TCP ownership-transfer path for applications that can
  encode directly into transport-owned payload storage.
- A source-based KPro/Netty performance baseline separates connection count
  from active load, records the current architectural gaps, and orders future
  optimization work without asserting unmeasured cross-framework ratios.
- OpenAPI generation now emits a bounded, typed HTTP client implementing the
  generated application contract, with checked URL/parameter binding, exact
  success statuses, response media-type validation, and structured
  `ResponseError` values.
- `kpro list api` and `kpro describe service` provide deterministic text or
  versioned JSON catalogs of declared OpenAPI, gRPC, and Message contracts
  without starting the application or executing external generator plugins.
- `kpro new --template` applies a strict, versioned local scaffold overlay with
  additive/replacement rules, selection conditions, portable path checks,
  source snapshots, and content-addressed provenance.
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
- `transport.InvokeHandler` gives built-in and third-party Message transports
  one public handler boundary that manages the pooled transport context,
  contains panics as `core.ErrRecovered`, and preserves error-valued panic
  chains.
- CI rejects any `replace` or `exclude` directive in the root
  module and compiles the complete module plus the pure-Go SQLite paths with
  CGO disabled.
- An opt-in TCP connection lifecycle check defaults to 1,000 concurrent
  connections, verifies request/response round trips and connection-slot
  reuse, and records elapsed time, heap, and goroutine observations without
  treating them as an application capacity or SLA result.

### Changed

- Built-in message transport shutdown now seals outbound admission, waits for
  encoding reservations, queued writes, and writer-owned writes, and reports
  drain errors before closing sessions or shared sockets. This is a complete
  transport-write boundary, not peer acknowledgement or durable delivery.
- TCP and WebSocket writers non-blockingly drain bounded FIFO batches and use
  `net.Buffers` gather submissions subject to `MaxWriteBatchFrames` and
  `MaxWriteBatchBytes`. TCP additionally bounds reusable idle payload storage
  with `MaxRetainedBufferBytes`. These new exported config fields are a
  pre-1.0 source change for positional struct literals; applications should use
  keyed config literals.
- Router message invocation state is pooled and route operation names are
  precomputed, reducing the guarded dispatch path from 200 B/3 allocations to
  24 B/1 allocation per operation on the recorded baseline.
- A mounted message Endpoint intercepts serve-context cancellation, optionally
  quiesces the transport, drains already accepted Router handlers while
  sessions remain writable, invokes optional outbound drain, and only then
  shuts down its transport. Custom transports use the compatible lifecycle
  phases they expose. This remains a transport-write boundary, not a
  peer-delivery guarantee.
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
- The root module now uses the published `github.com/mattn/go-sqlite3`
  dependency instead of replacing it with a repository-local shim. Reference
  and test SQLite paths continue to select `modernc.org/sqlite` explicitly when
  a pure-Go driver is required.

### Fixed

- KProV1 stream reads and contiguous encoding reject frame sizes that cannot
  fit an `int` including the header on 32-bit builds.

- TCP queue drains honor `MaxWriteBatchFrames` as a hard limit under backlog
  instead of implicitly doubling it. The performance guide documents the
  default, coalescing polling, and write-deadline refresh behavior.
- SSE event serialization now treats CR, LF, and CRLF as line boundaries and
  rejects line breaks in event names, preventing unprefixed or injected
  event-stream fields.
- Batched TCP/WebSocket writes reject silent short writes, release every
  drained item on failure, clear reusable vector references after completion,
  and prevent WebSocket data from being written after a close frame. Reused
  application-visible payload buffers clear their full capacity before handoff.
- Message transport sends can no longer commit after concurrent session close;
  outbound count/byte reservations are also released on encode errors, codec
  panics, failed writes, and close-time queue drain.
- Graceful Router shutdown now executes accepted per-session sync work instead
  of dropping queued tasks when the session closes during drain. Immediate
  `Router.Close` remains best-effort sync-queue cancellation without waiting;
  accepted async work and already-ready sync work can still start.
- Default outbound high-watermark calculation no longer overflows when an
  internal queue is configured near the maximum `int64` byte limit.
- Protobuf generation and descriptor inspection now parse the complete import
  grammar used by supported compilers, capture the project-local closure before
  tool lookup, and run `protoc` against a private input snapshot. Unsafe paths,
  missing/non-regular dependencies, path aliases, symlink escapes, and
  option/plugin-argument-like entry names are rejected before execution.
- Generated OpenAPI clients preserve unexpected-response metadata and partial
  bounded bodies when reads fail, reject redirects by default, safely encode dot
  path values, reject header control characters and non-finite numbers, handle
  typed-nil response bodies, and no longer let external parameter names alter
  generated error format strings. Direct IR and route validation now enforce the
  same generation invariants, reject JSON's special ignored field name, and
  prevent non-importable Go `vendor` output paths.
- Scaffolding now requires a missing target so its staged project is published
  with one directory rename instead of removing an existing empty directory;
  declared file modes are applied explicitly rather than weakened by `umask`.
- Human-readable API catalogs visibly escape terminal control and Unicode format
  characters while JSON catalogs retain the original encoded values.
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
- Direct TCP, KCP, QUIC, UDP, and WebSocket handlers now contain panics at the
  transport boundary, preserve `core.ErrRecovered` and error chains, notify
  configured `OnHandlerError` callbacks, and apply the session error policy.
- `kpro <command> --help` now prints that subcommand's usage and exits
  successfully instead of reporting `flag: help requested` as a command
  failure.

### Security

- TLS example clients verify server certificates by default and accept explicit
  `-ca` or local-only `-insecure` flags. Framework-created TLS configurations
  require TLS 1.2, or TLS 1.3 for QUIC/HTTP3.
- Release archive verification and extraction enforce entry and decompressed
  stream limits. Extracted binaries remain owner-only in private directories.
- Security CI uses the locally verified Gitleaks version and manual Go CodeQL
  builds. Intentional local CLI filesystem/tool access and RFC 6455 SHA-1 use
  carry narrow, documented scanner exceptions.

- Require Go 1.26.9 and upgrade x/net to v0.60.0, x/text to v0.42.0, and
  gRPC to v1.83.2, with their required transitive dependencies, to address
  reachable vulnerability reports. This includes gRPC GO-2026-6348 and
  GO-2026-6443. Go 1.25 support is dropped in this minor release; scaffolding
  templates use the same patched baseline.

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
