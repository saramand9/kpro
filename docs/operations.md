# Operations and Capacity Planning

This guide describes the runtime behavior present in KPro and a measurement process for operating an application built on it. It deliberately provides no universal request-rate, connection-count, latency, or availability claim.

## Failure and shutdown model

Construct new applications with `server.NewChecked` so invalid server config is
reported directly. Before launching any serving goroutine, `ListenAndServe`
collects failures from mounted components implementing `transport.Validator`.
If validation fails, correct the cause before starting again; do not turn a
certificate, address, path, or limit error into a restart loop. Built-in scalar
configuration cannot be changed through the component after construction, so
rebuild the Server for a bad value. Treat referenced codecs, loggers, TLS
callbacks, and other supplied objects as immutable after construction. The
same pre-start Server may be retried when an
external prerequisite at an unchanged path was repaired. Component validation
must be deterministic, non-mutating, and must not bind a listener or start
goroutines.

Built-in address preflight checks `host:port` syntax only; it does not resolve
DNS, verify that a named service is registered, or prove that a port is free.
Static TLS preflight checks that the files are readable and contain a parseable,
matching certificate/key pair. It does not prove validity dates, SANs, or a
trust chain. For dynamic `tls.Config`, preflight can only confirm that a static
certificate or selection callback exists, not that a callback will succeed.

`server.Server.ListenAndServe` starts every mounted runner concurrently. The first runner to exit causes the server to cancel the shared context, call `Shutdown` on all runners, wait for them to drain, and then close managed resources. KPro does not restart a failed runner inside the process; return the joined error and let the process supervisor apply the deployment's restart policy.

Server and built-in transport instances are one-shot. A concurrent or repeated
direct transport start returns `core.ErrAlreadyStarted`; a start attempted
after shutdown returns the transport's closed/not-accepting error. Construct a
new instance for a restart. Passing a nil or Background context is supported
for direct transport use, but then the caller owns shutdown; the internal
cancellation watcher is not retained after serving returns.

Automatic cleanup uses `server.Config.ShutdownTimeout`. An explicit `Server.Shutdown(ctx)` uses the caller's deadline. Managed resources are closed only after runners have drained, in reverse registration order. A resource whose shutdown succeeds is not closed again; a failed resource shutdown may be retried by a later call. If runner drain times out, resources remain open to avoid closing them underneath active handlers.

Operational consequences:

- make shutdown idempotent in custom transports and resources;
- make custom transport startup one-shot or otherwise define concurrent-start
  ownership explicitly;
- remove the instance from readiness before sending termination;
- set the platform termination grace period beyond the application's measured worst-case drain;
- log every component error in the joined return value rather than assuming the first error is the only cause;
- treat an unexpected clean runner return as a process-ending event too.

HTTP invocation contexts follow the request deadline and disconnect behavior of
`net/http`. SSE request contexts additionally cancel when the endpoint begins
shutdown, so long-lived handlers should select on `Context.Done()` and return.
Message invocations can also follow a session's optional
`core.SessionLifecycle.Done` signal. A pooled `core.Context` or request-scoped
`core.Invocation` must not escape its handler; call `core.CloneContext` before
intentional message background work and arrange an application-owned
cancellation/deadline.

## Overload recovery

Message routing has two admission policies:

- `core.BackpressureReject` returns immediately when bounded capacity is exhausted;
- `core.BackpressureWait` waits for capacity until the router or session closes; callers that own a `core.Router` should use `DispatchContext` to add a request deadline.

Expected saturation errors include `core.ErrDispatchQueueFull`, `core.ErrSendQueueFull`, `core.ErrTooManyInflight`, and `core.ErrGlobalInflightLimit`. Translate them into a stable protocol/application response where possible, count them separately from defects, and ensure clients use bounded exponential backoff with jitter. Waiting is not free capacity: it shifts memory and latency to blocked callers.

Transport limits stop different failure modes. Connection/session/stream caps bound peer state; frame, packet, message, header, and body limits bound parsing allocations; send queues bound slow consumers; read/write/idle timeouts bound resource residence. Set all applicable layers so an upstream permissive limit cannot defeat a downstream safety limit.

## Dependency recovery

KPro database adapters expose `Ping` and `Health`, while several expose native pool statistics. The framework does not impose a cross-database retry, circuit-breaker, or failover policy. Configure driver timeouts and pools, classify retryable errors in the application, retry only idempotent operations or transactions designed for retry, and bound every attempt by the request deadline.

For source-of-truth failure:

1. fail readiness only when the service cannot safely serve its contract;
2. reject new dependent work promptly instead of accumulating an unbounded queue;
3. preserve the original error category for alerting;
4. verify reconnection without a process restart and also verify supervisor-driven restart;
5. reconcile any ambiguous writes before replay.

Outbox delivery is at least once. Lease expiry permits recovery after a worker crash, so projector handlers must be idempotent. Monitor pending age, claim/lease expiry, retry count, and dead-letter state; document who can replay and how a poison event is corrected. See [Database Production](database-production.md).

## Minimum runbooks

### Startup validation fails

1. Read every joined component error; validation reports more than one problem when possible.
2. Identify the process with `Server.Name()` and inspect
   `Server.Descriptors()` when protocol mode or capabilities are unclear.
3. Correct configuration without starting a competing diagnostic listener or bypassing `Validate`.
4. Run configuration and certificate checks in the deployment environment, then retry startup.
5. Treat a repeatedly invalid immutable deployment as a rollout failure, not a transient runtime outage.

### Shutdown exceeds its deadline

1. Confirm readiness was removed and identify active runners.
2. Compare active request/stream age with the configured application and platform deadlines.
3. Inspect slow handlers, blocked sends, database calls, and resource shutdown errors.
4. Preserve diagnostics before the supervisor forces termination.
5. Reproduce with the same traffic shape and fix cancellation or budget alignment; do not only increase every timeout.

### Queue or in-flight rejection rises

1. Separate global, per-session, dispatch-queue, and send-queue saturation.
2. Identify whether load, service time, payload size, or slow consumers changed.
3. Shed or throttle traffic and protect healthy tenants/sessions.
4. Scale only after checking the saturated resource and downstream capacity.
5. Retest the new limit; a larger queue can increase memory and tail latency without increasing throughput.

### Database becomes unavailable

1. Confirm with a bounded health check and native pool/server telemetry.
2. Stop retry amplification and fail operations within their deadlines.
3. Determine whether writes are known failed, known committed, or ambiguous.
4. Restore connectivity/fail over according to the database runbook.
5. Reconcile ambiguous work and drain outbox/projector backlog while watching downstream saturation.

### Projector backlog grows

1. Compare incoming event rate, completion rate, oldest pending age, retries, and dead letters.
2. Identify dependency latency, poison events, lease churn, or insufficient workers.
3. Correct poison data or dependency health before increasing concurrency.
4. Increase workers in steps bounded by source and target database capacity.
5. Verify idempotency during replay and record time to recover the backlog.

## Capacity inputs

Capture at least these workload dimensions before choosing limits:

- concurrent connections, virtual sessions, streams, and reconnect rate;
- request/message arrival rate, burst duration, and per-session skew;
- encoded and decoded payload percentiles, headers, and response fan-out;
- handler CPU time, allocation rate, external dependency latency, and timeout rate;
- synchronous versus asynchronous route mix and ordering requirements;
- slow-client fraction, stream lifetime, and deployment drain window.

Use production-like encryption, codecs, middleware, schemas, indexes, logging, and telemetry in the test. A loopback echo benchmark isolates framework overhead but does not establish application capacity.

## Controls to size

| Layer | Primary controls | What they bound |
| --- | --- | --- |
| Message router | `SyncQueueSize`, `AsyncWorkers`, `AsyncQueueSize`, `MaxInflightPerSession`, `MaxInflight`, `Backpressure`, `SessionIdleTimeout`, `ShutdownTimeout` | retained work, concurrency, fairness, and drain |
| TCP | `MaxConnections`, `MaxFrameSize`, `SendQueueSize`, `ReadTimeout`, `WriteTimeout` | connection state, parsing, slow readers/writers |
| KCP | TCP-like controls plus `SendWindow`, `ReceiveWindow`, `MTU`, shard and congestion settings | sessions, buffers, loss/retransmission behavior |
| QUIC message streams | `MaxStreams`, `MaxFrameSize`, `SendQueueSize`, read/write, handshake, and idle timeouts | stream state, framing, handshakes, idle peers |
| UDP | `MaxSessions`, `MaxPacketSize`, `SendQueueSize`, `SessionIdleTimeout`, `CleanupInterval` | virtual-peer state, datagrams, idle retention |
| WebSocket | `MaxConnections`, `MaxMessage`, `SendQueueSize`, `HandshakeTimeout`, `ReadTimeout`, `WriteTimeout` | upgraded sessions, HTTP upgrade headers, reassembled messages, and slow peers |
| HTTP/HTTPS | `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `ShutdownTimeout`, `MaxBodyBytes` | requests, bodies, keep-alive residence, drain |
| HTTP/3 | handshake/idle/shutdown timeouts, `MaxHeaderBytes`, TLS and QUIC settings | handshakes, streams, headers, drain |
| SSE | `MaxStreams`, `HandshakeTimeout`, `IdleTimeout`, `WriteTimeout`, `ShutdownTimeout` | active streams, request headers, keep-alive residence, long-lived writers, and drain |
| gRPC | `WithShutdownTimeout` and selected `grpc.ServerOption` limits | RPC/message/connection behavior and drain |
| SQL pools | max open/idle connections, connection lifetime/idle time | database concurrency and connection churn |

Defaults are guardrails for development, not a statement that every row has been sized for your service.

`RouterStats.SyncQueueDepth` is a total across all live sessions, while
`SyncQueueCapacityPerSession` is the limit for each independent session queue;
do not divide one by the other as a global saturation ratio. Under
`BackpressureWait`, handler-initiated reentrant work must call
`DispatchContext` with a deadline: the current handler can already own the
global or per-session capacity that the nested dispatch is waiting for.

## Estimation and measurement

Use Little's Law as a first consistency check, not a benchmark result:

```text
expected active work ≈ arrival rate × observed end-to-end service time
expected DB concurrency ≈ DB-operation rate × observed DB hold time
```

Add a burst and uncertainty margin supported by tests. If required concurrency exceeds CPU, downstream pool, or latency objectives, reduce service time or scale capacity before raising in-flight limits.

A useful upper-bound inventory for message-path memory is:

```text
session state
+ sessions × outbound queue capacity × average encoded response retained
+ active sync sessions × sync queue capacity × average queued task retained
+ async queue capacity × average queued task retained
+ running handlers × average decoded payload and handler working set
```

This is an accounting model, not an allocation guarantee. Measure heap profiles and GC behavior because slice capacity, protocol buffers, TLS state, driver pools, logging, and business objects add overhead.

Run at least five phases:

1. ramp to expected steady traffic;
2. hold long enough to observe pools, GC, idle cleanup, and leaks;
3. spike above admission capacity and verify controlled rejection;
4. inject slow clients and dependency failures;
5. terminate instances under load and measure drain, reconnect, and recovery.

Record throughput, latency percentiles, error/rejection category, CPU, heap, allocation rate, goroutines, connections/sessions, queue depth, in-flight work, database pool waits, and oldest event age. Repeat after changing any limit; tune one bottleneck at a time.

## Performance regression budgets

Repository CI runs selected Router and HTTP handler microbenchmarks and checks
their `ns/op`, `B/op`, and `allocs/op` against
[`benchmark/budgets.json`](../benchmark/budgets.json). Reproduce that guard with:

```sh
go test ./core ./transport/httpx -run '^$' \
  -bench 'Benchmark(RouterDispatchSync|EndpointServeHTTP)$' \
  -benchmem -benchtime=250ms -count=1 > benchmark-current.txt
go run ./cmd/benchguard \
  -budget benchmark/budgets.json \
  -input benchmark-current.txt
```

The budgets are intentionally repository-owned regression thresholds for this
specific code path and CI workload. They do not predict end-to-end service
capacity or establish an SLA. On failure, compare benchmark conditions and
profiles before changing a budget; require review and evidence for intentional
threshold changes.

## Observability boundary

`middleware.Metrics` supplies an in-process aggregate snapshot of
started/completed invocations, errors, panics, in-flight work, and latency.
`core.Router.Snapshot` and `server.Endpoint.Snapshot` expose Router admission,
queue, in-flight, session, rejection, and lifecycle counters. Database resources
provide health checks, and GORM/ClickHouse expose SQL pool stats.

The optional observability packages add concrete integration points:

- `prometheusx` registers invocation collectors with transport, stable
  operation, and result labels, plus bounded lifecycle/Router and explicit
  backlog/projector collectors;
- `zapx` writes structured completion, failure, panic, and slow-call logs using
  an application-supplied logger;
- `otelx` creates server spans and extracts propagation using a supplied/global
  provider and propagator;
- `healthx` evaluates named checks concurrently within a probe deadline.

An OTel SDK, sampler, exporter, collector/backend, provider flush/shutdown,
Prometheus scraping, dashboards, alerts, log retention, and tracing storage are
application/platform responsibilities. Every health check must honor its
context. Validate telemetry during overload, keep metric labels bounded, and do
not expose raw health errors or sensitive payloads without an access policy.
