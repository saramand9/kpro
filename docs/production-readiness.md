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
- [ ] **Built in:** HTTP/3 shutdown sends GOAWAY and waits for active requests
  until the shutdown deadline; deadline expiry force-closes remaining
  connections and is reported to the caller.
- [ ] **Configure and verify:** Treat message-transport shutdown as a bounded
  stop with at-most-once outbound replies, not a delivery barrier. Add message
  IDs, acknowledgements, retry, and idempotency where delivery must be
  recoverable.
- [ ] **Built in:** SSE stream contexts cancel when endpoint shutdown begins;
  handlers must observe `Context.Done()` and return rather than detach work.
- [ ] **Built in:** Serving and shutdown errors use `*server.ComponentError` to retain component name, lifecycle phase, and the original `errors.Is`/`errors.As` chain.
- [ ] **Configure and verify:** Decide whether a runner failure should terminate the whole process. That is the current `Server.Run` behavior: the first runner exit cancels and shuts down the others.
- [ ] **External:** Configure the orchestrator's termination grace period, readiness removal, restart policy, and rollout strategy around the measured drain time.

## Limits and overload

- [ ] **Built in:** Message routing provides bounded sync and async queues, global and per-session in-flight caps, and selectable backpressure behavior through `core.RouterConfig`.
- [ ] **Configure and verify:** Set router queue, worker, in-flight, idle, and shutdown values from a measured workload rather than accepting defaults without review.
- [ ] **Configure and verify:** Set `RateLimitConfig.IdleTimeout` and `MaxBuckets` for connection/IP limiters, and ensure custom native invocations publish a stable per-connection `ConnDone` signal rather than a request-scoped cancellation channel.
- [ ] **Configure and verify:** Set transport connection/session/stream caps, message or frame limits, outbound queue sizes, and read/write/idle timeouts.
- [ ] **Configure and verify:** Set HTTP `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `ShutdownTimeout`, and `MaxBodyBytes` explicitly. HTTP read/write/idle defaults are not an operational policy.
- [ ] **Configure and verify:** Set HTTP/3 `MaxConnections`, `MaxBodyBytes`, `MaxHeaderBytes`, handshake/read/write/idle/shutdown timeouts, and QUIC/TLS policy explicitly.
- [ ] **Configure and verify:** Keep HTTP/3 `Allow0RTT` disabled unless every accepted route is idempotent or protected against replay. A nested `QUICConfig.Allow0RTT` does not override the safe top-level default.
- [ ] **Configure and verify:** Define what clients observe when a queue, body, frame, connection, or deadline limit is reached; test retry storms and slow consumers.
- [ ] **Built in:** Protocol-neutral error kinds map rate limiting, payload limits, overload, internal failures, and recovered panics to stable safe HTTP/gRPC responses without exposing raw error text.
- [ ] **Configure and verify:** Select and test each message transport's `HandlerErrorPolicy`; the zero value closes the affected session when the transport-supplied handler returns an error, while continue mode requires an explicit safety rationale and a non-blocking, redacted observer. For Server-mounted endpoints, distinguish Router admission errors from accepted route-handler failures handled inside the Router.
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
- [ ] **Built in:** Additional `prometheusx` collectors expose bounded server/Router lifecycle and admission snapshots, and explicit eventing backlog/projector names.
- [ ] **Configure and verify:** Keep operation names bounded by registered routes/methods; never substitute raw URLs, peers, connection IDs, tenant IDs, or arbitrary metadata as metric labels.
- [ ] **Built in:** `zapx` emits structured completion/error/panic/slow-call logs, and `otelx` starts server spans and extracts propagation with a supplied or global provider/propagator.
- [ ] **External:** The application must configure, own, flush, and shut down its OpenTelemetry SDK, sampler, and exporter. KPro does not select or ship a production exporter configuration.
- [ ] **Configure and verify:** Review peer/connection trace attributes and `zapx.Config.IncludeRemote` against privacy, redaction, retention, and telemetry-cost policy.
- [ ] **Configure and verify:** Include transport, operation, error category, saturation, queue rejection, and dependency latency signals without unbounded labels.
- [ ] **External:** Connect Prometheus exposition, structured logs, traces, and alerts to the application's selected telemetry stack. KPro does not provide a hosted backend.
- [ ] **External:** Define service-level indicators, objectives, paging thresholds, dashboards, log retention, and trace sampling.
- [ ] **Configure and verify:** Correlate a request or message across handler, database transaction, event delivery, and projector without logging credentials or sensitive payloads.

## Testing and release evidence

- [ ] Use a supported Go 1.25 or 1.26 patch release; repository CI currently verifies Go 1.25.12 and 1.26.5.
- [ ] Run `go mod verify` and `govulncheck ./...`; triage every reachable finding before release and retain the report. A clean call-graph scan is not a complete image or deployment scan.
- [ ] Run unit, race, protocol integration, and database integration tests for the exact build.
- [ ] Generate `coverage.out`, inspect package-level gaps, and meet the repository's 55% aggregate framework floor without treating the percentage as proof of scenario coverage.
- [ ] Run the repository `cmd/benchguard` command against `benchmark/budgets.json`; investigate rather than blindly widening a failed hot-path budget.
- [ ] Treat repository performance budgets as regression thresholds for their CI workload, not as application latency or capacity objectives.
- [ ] Run load, spike, soak, slow-client, reconnect, and graceful-drain scenarios at expected and overload traffic.
- [ ] Inject runner, network, database, projector, and telemetry failures; verify bounded recovery and actionable diagnostics.
- [ ] Record CPU, memory, goroutines, connections, queues, database pools, latency percentiles, rejection rate, and drain time.
- [ ] Pin a reviewed KPro commit or release and review its public API changes before upgrading; see [API Stability](api-stability.md).
- [ ] Run `kpro doctor` and `kpro version` (or their `go run ./cmd/kpro` forms) in build diagnostics so toolchain, module, platform, version, and commit evidence are recoverable.
- [ ] For a `v*` tag, verify the release workflow published the expected Linux, Windows, and macOS archives only after validating `checksums.txt`, per-archive SBOMs, the artifact matrix, and native CLI version metadata.
- [ ] Rehearse canary, rollback, configuration rollback, and data compatibility procedures.
- [ ] Assign owners to the runbooks in [Operations and Capacity Planning](operations.md).

## Exit criterion

A service is ready for its target environment only when every applicable item has an owner and evidence, all omitted items have a documented reason, and the measured capacity envelope leaves the margin required by that service's objectives.
