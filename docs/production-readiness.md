# Production Readiness Checklist

This checklist separates framework capability from application and platform work. Completing it records evidence for a particular service and environment; it is not a KPro production certification or an SLA.

Status terms used below:

- **Built in**: the repository contains the mechanism and tests for it.
- **Configure and verify**: KPro exposes controls, but the application must choose values and validate them.
- **External**: the application or deployment platform must provide the integration.

## Service lifecycle

- [ ] **Built in:** Use `server.NewChecked` so invalid server configuration is returned during construction; `server.New` remains the permissive compatibility constructor.
- [ ] **Built in:** `Server.Start(ctx)` claims one generation synchronously, uses an application context as the process lifetime, and aggregates `transport.Validator` failures from all mounted components before starting any serving goroutine. `Server.Run(ctx)` remains the blocking form.
- [ ] **Built in:** `RunHandle.WaitReady(ctx)` waits for every readiness-aware runner to publish its listener and returns startup failure without a retry-generation race; `Server.Ready()` and `Server.WaitReady()` remain compatibility views of the current generation.
- [ ] **Configure and verify:** Require custom listener-owning runners to implement `transport.ReadinessProvider`; the legacy fallback only proves that `ListenAndServe` was entered.
- [ ] **Configure and verify:** Make custom `Validate` methods side-effect free and verify TLS, addresses, paths, limits, and mutually dependent options before deployment.
- [ ] **Built in:** Use one `server.Server` to own mounted runners and context-aware managed resources.
- [ ] **Built in:** Server and built-in transport instances are one-shot;
  repeated starts fail with `core.ErrAlreadyStarted`, and shutdown prevents
  late listener, connection, or session publication.
- [ ] **Configure and verify:** Handle `SIGINT` and `SIGTERM`, call `Server.Shutdown` with a deadline, and test drain behavior with real clients.
- [ ] **Configure and verify:** Make the process shutdown budget longer than the slowest accepted request, stream drain, and resource close path.
- [ ] **Built in:** A Server-mounted message `Endpoint` first calls the
  optional `transport.Quiescer`, then stops Router admission and drains
  accepted sync/async handlers, and finally invokes terminal transport
  shutdown. Explicit `Endpoint.Shutdown(ctx)` passes the caller context to
  all applicable phases; serve-context cancellation is intercepted until
  Router drain completes. An earlier phase error does not skip terminal
  shutdown, and errors are retained together.
- [ ] **Built in:** TCP, KCP, QUIC, UDP, and WebSocket implement quiesce while
  retaining existing outbound writers. KCP keeps its shared listener/session
  UDP socket open, accepts and closes new raw sessions, and closes the listener
  only in terminal shutdown. QUIC closes new connection admission but accepts
  and cancels new streams on retained connections until terminal shutdown.
- [ ] **Configure and verify:** Require a custom message transport to implement
  `transport.Quiescer` when new reads/dispatch must stop before Router drain.
  The compatible fallback skips that phase and cannot provide the same
  transport-side cutover.
- [ ] **Built in:** `Router.Shutdown` drains accepted sync/async work even
  when a session closes during graceful drain. `Router.Close` is an immediate
  best-effort sync-queue cancellation path that does not wait; accepted async
  work and already-ready sync work can still start.
- [ ] **Configure and verify:** Always give endpoint/server shutdown an explicit
  total deadline; a Router may apply its own drain timeout when the caller has
  none, but that does not replace a process-level stop bound.
- [ ] **Built in:** HTTP/3 shutdown sends GOAWAY and waits for active requests
  until the shutdown deadline; deadline expiry force-closes remaining
  connections and is reported to the caller.
- [ ] **Configure and verify:** Treat message-transport shutdown as a bounded
  stop with at-most-once outbound replies, not a delivery barrier. Terminal
  transport shutdown does not wait for queued or writer-owned bytes to flush,
  and KPro currently has no separate outbound-drain phase. Add message IDs,
  acknowledgements, bounded retry, and idempotency, or own an explicit
  application-level drain where delivery must be recoverable.
- [ ] **Built in:** SSE stream contexts cancel when endpoint shutdown begins;
  handlers must observe `Context.Done()` and return rather than detach work.
- [ ] **Built in:** Serving and shutdown errors use `*server.ComponentError` to retain component name, lifecycle phase, and the original `errors.Is`/`errors.As` chain.
- [ ] **Configure and verify:** Decide whether a runner failure should terminate the whole process. That is the current `Server.Run` behavior: the first runner exit cancels and shuts down the others.
- [ ] **External:** Configure the orchestrator's termination grace period, readiness removal, restart policy, and rollout strategy around the measured drain time.

## Limits and overload

- [ ] **Built in:** Message routing provides bounded sync/async queues,
  global/per-session in-flight caps, and selectable backpressure behavior
  through `core.RouterConfig`. `BackpressureWait` separately caps blocked
  dispatches at 1024 globally and 16 per session by default, with one ticket
  retained across all admission stages of one dispatch.
- [ ] **Configure and verify:** Set router queue, worker, in-flight, waiting,
  idle, and shutdown values from a measured workload rather than accepting
  defaults without review. Alert separately on global and per-session wait
  rejection.
- [ ] **Built in:** TCP, KCP, QUIC message streams, UDP, and WebSocket enforce
  both per-session pending-write count (`SendQueueSize`) and accounted-byte
  (`MaxPendingBytes`) limits. They reserve estimated capacity before encoding
  and reconcile it with the actual encoded size before queue publication.
- [ ] **Configure and verify:** Size both outbound limits from encoded payload
  percentiles and slow-consumer tests. Treat `MaxPendingBytes` as retained
  outbound accounting, not a whole-process heap bound.
- [ ] **Built in:** `core.SessionOutboundState` reports optional
  point-in-time pending/max write and byte counts, current/max waiting writers,
  and high/low-watermark hysteresis for built-in message sessions.
- [ ] **Built in:** `core.SessionWaitOutboundWritable` discovers optional,
  context-cancelable advisory waiting. Built-in sessions bound waiters per
  session to `SendQueueSize` by default; a successful wait reserves no
  capacity for the next send.
- [ ] **Configure and verify:** Use `OutboundState.Writable` only as a
  telemetry or proactive-throttling hint. Always handle the subsequent
  `Send`/`Reply` result as authoritative, including
  `core.ErrSendQueueFull`. TCP, KCP, QUIC, and WebSocket close the session
  on hard outbound-full, while UDP leaves its virtual peer open.
- [ ] **Configure and verify:** Set `RateLimitConfig.IdleTimeout` and `MaxBuckets` for connection/IP limiters, and ensure custom native invocations publish a stable per-connection `ConnDone` signal rather than a request-scoped cancellation channel.
- [ ] **Configure and verify:** Set transport connection/session/stream caps,
  message or frame limits, both outbound queue limits, and read/write/idle
  timeouts.
- [ ] **Configure and verify:** Set HTTP `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `ShutdownTimeout`, and `MaxBodyBytes` explicitly. HTTP read/write/idle defaults are not an operational policy.
- [ ] **Configure and verify:** Set HTTP/3 `MaxConnections`, `MaxBodyBytes`, `MaxHeaderBytes`, handshake/read/write/idle/shutdown timeouts, and QUIC/TLS policy explicitly.
- [ ] **Configure and verify:** Keep HTTP/3 `Allow0RTT` disabled unless every accepted route is idempotent or protected against replay. A nested `QUICConfig.Allow0RTT` does not override the safe top-level default.
- [ ] **Configure and verify:** Define what clients observe when a queue, body, frame, connection, or deadline limit is reached; test retry storms and slow consumers.
- [ ] **Built in:** Protocol-neutral error kinds map rate limiting, payload limits, overload, internal failures, and recovered panics to stable safe HTTP/gRPC responses without exposing raw error text.
- [ ] **Built in:** Direct TCP, KCP, QUIC, UDP, and WebSocket handler panics are
  contained as errors wrapping `core.ErrRecovered`, reported to
  a configured `OnHandlerError`, and passed through the session policy.
- [ ] **Configure and verify:** Select and test each message transport's `HandlerErrorPolicy`; the zero value closes the affected session when the transport-supplied handler returns an error or panics, while continue mode requires an explicit safety rationale and a non-blocking, redacted observer. For Server-mounted endpoints, distinguish Router admission errors from accepted route-handler failures handled inside the Router.
- [ ] **External:** Enforce upstream rate, connection, and request-size controls where an ingress, load balancer, or API gateway is part of the topology.

## Security

- [ ] **Configure and verify:** Use TLS where the protocol supports it, validate protocol versions and cipher policy, and test certificate reload or restart rotation.
- [ ] **Built in:** WebSocket upgrades with an `Origin` header default to a
  same-host check; non-browser clients without `Origin` remain supported.
- [ ] **Configure and verify:** Set `websocket.Config.CheckOrigin` to an
  explicit allowlist when browser origin and WebSocket host differ. Never use
  an allow-all callback on an exposed browser endpoint, and trust forwarded
  headers only behind a sanitizing proxy.
- [ ] **External:** Provide authentication and authorization in application middleware, service handlers, or a trusted gateway. KPro does not ship an identity or policy system.
- [ ] **External:** Manage secrets, keys, certificates, rotation, revocation, and audit retention with the deployment platform.
- [ ] **Configure and verify:** Validate request, message, metadata, and database input at the module boundary; do not treat a transport limit as business validation.
- [ ] **Configure and verify:** Preserve original classified error causes in protected logs/traces, but expose only `core.PublicErrorMessage` or the built-in protocol mapping to untrusted clients.
- [ ] **External:** Run dependency, container, artifact, and deployment-policy scanning appropriate to the organization.

## Data and migrations

- [ ] **Built in:** Database adapters expose lifecycle and health contracts; the GORM layer provides migrations and event/outbox primitives documented in [Database Production](database-production.md).
- [ ] **Configure and verify:** Establish one source of truth for each aggregate and put other stores behind module ports or read-model projectors.
- [ ] **Configure and verify:** Run migrations before traffic, fail deployment on migration errors, and rehearse forward repair and rollback constraints.
- [ ] **Configure and verify:** Size database pools from measured transaction latency and concurrency; set server-side statement, lock, and idle timeouts.
- [ ] **Configure and verify:** Test backup restoration, point-in-time recovery, replica failover, connection exhaustion, and partial dependency outages.
- [ ] **Configure and verify:** Make outbox/projector consumers idempotent, monitor lease expiry and backlog, and define dead-letter replay ownership.
- [ ] **Configure and verify:** Use each `database/redis.Lock` object for only
  one successful acquisition. Treat it as a token-owned TTL lease, not a
  fencing or quorum lock; require idempotency/compensation or enforce a
  monotonic fence at the protected resource. Treat an `Acquire` transport error
  as an ambiguous `SET NX` outcome and reconcile or wait for TTL before
  failover.

The repository CI requires live MySQL and PostgreSQL for its `dbintegration` suite. Redis uses an in-process test server; Elasticsearch, Qdrant, and InfluxDB use protocol-level HTTP test servers; ClickHouse uses a fake SQL driver; MongoDB and Neo4j default tests cover configuration/construction. Neo4j has a separate opt-in integration suite. Treat every adapter used in a deployment as requiring end-to-end tests against the chosen engine, version, topology, TLS mode, and failure policy.

## Health and observability

- [ ] **Built in:** `middleware.Metrics` aggregates invocation counts, failures, panics, in-flight work, and latency in process; data resources expose `Health`/`Ping` and native client statistics where supported.
- [ ] **Built in:** `healthx` runs named checks concurrently under a bounded request context, prevents overlapping execution of the same stuck check, returns stable JSON, and reports HTTP 503 for failure or timeout.
- [ ] **Configure and verify:** Expose separate liveness and readiness endpoints. Readiness should reflect only dependencies required to serve the endpoint safely, and every `healthx.CheckFunc` must pass its context to I/O and return on cancellation.
- [ ] **Built in:** Use the active `RunHandle.WaitReady` result as the listener-readiness input before allowing an application readiness check to succeed; `Server.WaitReady` remains a compatibility view.
- [ ] **Configure and verify:** Keep `healthx.Config.ExposeErrors` disabled unless disclosing dependency errors on the probe endpoint is acceptable.
- [ ] **Built in:** `prometheusx` exposes invocation count, in-flight, and duration collectors labeled only by transport, stable operation, and result.
- [ ] **Built in:** Additional `prometheusx` collectors expose bounded
  server/Router lifecycle and admission snapshots, including current/global/
  per-session waiting capacity and distinct global/session wait-rejection
  counters, plus explicit eventing backlog/projector names.
- [ ] **Configure and verify:** Keep operation names bounded by registered routes/methods; never substitute raw URLs, peers, connection IDs, tenant IDs, or arbitrary metadata as metric labels.
- [ ] **Built in:** `zapx` emits structured completion/error/panic/slow-call logs, and `otelx` starts server spans and extracts propagation with a supplied or global provider/propagator.
- [ ] **External:** The application must configure, own, flush, and shut down its OpenTelemetry SDK, sampler, and exporter. KPro does not select or ship a production exporter configuration.
- [ ] **Configure and verify:** Review peer/connection trace attributes and `zapx.Config.IncludeRemote` against privacy, redaction, retention, and telemetry-cost policy.
- [ ] **Configure and verify:** Include transport, operation, error category, saturation, queue rejection, and dependency latency signals without unbounded labels.
- [ ] **Configure and verify:** If exporting session outbound state, aggregate
  it without connection IDs or peer addresses as labels; a state snapshot is
  advisory and must not replace send-error counters.
- [ ] **External:** Connect Prometheus exposition, structured logs, traces, and alerts to the application's selected telemetry stack. KPro does not provide a hosted backend.
- [ ] **External:** Define service-level indicators, objectives, paging thresholds, dashboards, log retention, and trace sampling.
- [ ] **Configure and verify:** Correlate a request or message across handler, database transaction, event delivery, and projector without logging credentials or sensitive payloads.

## Testing and release evidence

- [ ] Use a supported Go 1.25 or 1.26 patch release; repository CI currently verifies Go 1.25.12 and 1.26.5.
- [ ] Run `go mod verify` and `govulncheck ./...`; triage every reachable finding before release and retain the report. A clean call-graph scan is not a complete image or deployment scan.
- [ ] Run unit, race, protocol integration, and database integration tests for the exact build.
- [ ] Preserve the repository failure/concurrency cases for global and
  per-session Router waiter exhaustion, cancellation cleanup, quiesce-before-
  drain ordering, accepted sync work after session close, outbound waiter
  exhaustion, concurrent Send/close, and writer-owned accounting.
- [ ] Preserve the deterministic near-`MaxInt64` outbound-queue test. The
  default high watermark is calculated as `MaxBytes - MaxBytes/5` to avoid the
  multiplication overflow of `MaxBytes*4/5`; this arithmetic test is not a
  claim that such a queue size is operationally appropriate.
- [ ] Generate `coverage.out`, inspect package-level gaps, and meet the repository's 55% aggregate framework floor without treating the percentage as proof of scenario coverage.
- [ ] Run the repository `cmd/benchguard` command against `benchmark/budgets.json`; investigate rather than blindly widening a failed hot-path budget.
- [ ] Treat repository performance budgets as regression thresholds for their CI workload, not as application latency or capacity objectives.
- [ ] Run load, spike, soak, slow-client, reconnect, and graceful-drain scenarios at expected and overload traffic.
- [ ] Inject runner, network, database, projector, and telemetry failures; verify bounded recovery and actionable diagnostics.
- [ ] Record CPU, memory, goroutines, connections, queues, database pools, latency percentiles, rejection rate, and drain time.
- [ ] Pin a reviewed KPro commit or release and review its public API changes before upgrading; see [API Stability](api-stability.md).
- [ ] Run `kpro doctor` and `kpro version` (or their `go run ./cmd/kpro` forms) in build diagnostics so toolchain, module, platform, version, and commit evidence are recoverable.
- [ ] Before publishing a KPro release, verify the root `go.mod` contains no
  `replace` or `exclude`, then test a clean external consumer module and
  `go install github.com/saramand9/kpro/cmd/kpro@<tag>` through the module path
  consumers will actually use. Local checkout replacements do not satisfy this
  check.
- [ ] For a `v*` tag, verify the release workflow published the expected Linux, Windows, and macOS archives only after validating `checksums.txt`, per-archive SBOMs, the artifact matrix, and native CLI version metadata.
- [ ] Rehearse canary, rollback, configuration rollback, and data compatibility procedures.
- [ ] Assign owners to the runbooks in [Operations and Capacity Planning](operations.md).

## Exit criterion

A service is ready for its target environment only when every applicable item has an owner and evidence, all omitted items have a documented reason, and the measured capacity envelope leaves the margin required by that service's objectives.
