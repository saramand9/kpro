# Operations and Capacity Planning

This guide describes the runtime behavior present in KPro and a measurement process for operating an application built on it. It deliberately provides no universal request-rate, connection-count, latency, or availability claim.

## Failure and shutdown model

Construct new applications with `server.NewChecked` so invalid server config is
reported directly. Before launching any serving goroutine, `Run` and
`ListenAndServeContext`
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

`server.Server.Start(ctx)` synchronously claims one lifecycle generation, then
starts every mounted runner concurrently and uses `ctx`
as the process lifetime. The first runner to exit, or cancellation of `ctx`,
causes the server to cancel the shared context, call `Shutdown` on all runners,
and wait for their serving goroutines to stop within the configured bound.
Managed resources close only after runner shutdown and waiting both succeed.
KPro does not restart a failed runner inside the process; return the joined
error and let the process supervisor apply the deployment's restart policy.
`ListenAndServe` and `ListenAndServeContext` remain available for compatibility,
but new process entry points should prefer `Start`; `Run` remains the blocking
form.

Use the returned `RunHandle` and bound startup with `handle.WaitReady(ctx)`. The ready
channel closes after every runner implementing `transport.ReadinessProvider`
reports that its listener is bound. A legacy custom runner without that
capability is considered ready when its serving method is entered. Startup
failure also closes the channel; only `WaitReady` returns the failure, so do not
treat `<-handle.Ready()` alone as proof of successful startup.

The handle is tied to exactly one start attempt. This matters after a validation
or empty-server failure: `Server.WaitReady` is retained for compatibility, but a
caller that launches `Run` in a goroutine can otherwise observe the prior
generation before that goroutine claims the retry.

Serving and shutdown failures are wrapped in `*server.ComponentError`, which
records `Component` and `Phase` while retaining the original cause. Joined
errors can contain more than one component failure. Use `errors.As` to extract
component provenance and `errors.Is` for the underlying operational category.

Server and built-in transport instances are one-shot. A concurrent or repeated
direct transport start returns `core.ErrAlreadyStarted`; a start attempted
after shutdown returns the transport's closed/not-accepting error. Construct a
new instance for a restart. Passing a nil or Background context is supported
for direct transport use, but then the caller owns shutdown; the internal
cancellation watcher is not retained after serving returns.

Automatic cleanup uses `server.Config.ShutdownTimeout`. An explicit
`Server.Shutdown(ctx)` uses the caller's deadline. Managed resources are closed
in reverse registration order only after runner shutdown and bounded waiting
both succeed. A resource whose shutdown succeeds is not closed again; a failed
resource shutdown may be retried by a later call. If runner stop or waiting
times out, resources remain open to avoid closing them underneath active
handlers.

HTTP and HTTPS attempt graceful `net/http` shutdown first. If that drain fails
or reaches its deadline, the endpoint closes remaining non-hijacked HTTP
connections and returns the joined graceful/force-close error; active request
contexts are canceled and blocked network I/O is released by that hard close.
SSE cancels stream request contexts before attempting the same bounded drain and
force-close fallback. Go cannot terminate user code that permanently ignores
its context, including a gRPC handler that ignores its RPC context. A
connection explicitly hijacked through `net/http` is no longer owned by the
server and must be tracked and closed by the hijacking component.

For a message transport mounted as `server.Endpoint`, endpoint shutdown has
three ordered phases:

1. call `Quiesce(ctx)` when the transport implements the optional
   `transport.Quiescer`;
2. call `Router.Shutdown(ctx)` to close Router admission and drain already
   accepted sync and async handlers;
3. call the transport's terminal `Shutdown(ctx)`.

TCP, KCP, QUIC, UDP, and WebSocket implement `transport.Quiescer`. Their
quiesce phase stops new handler dispatch and message reads while preserving
established sessions and their outbound writer paths, so accepted handlers can
attempt a final reply during Router drain. Quiesce, Router-drain, and transport
errors do not skip later phases and are joined for the caller. A third-party
transport without `Quiescer` uses the compatibility fallback of Router drain
followed by transport shutdown; that fallback cannot prevent the transport
from continuing to read while the Router is draining, so implement the
optional phase when a hard dispatch cutover is required.

KCP has one protocol-specific boundary: established sessions share the
listener's UDP socket. Its quiesce phase therefore keeps that listener open,
continuously accepts and closes newly arriving raw KCP sessions, and blocks
their registration/dispatch. The terminal shutdown phase closes the shared
listener after accepted application work drains.

QUIC closes its top-level connection listener during quiesce but retains
established connections and streams for outbound writes. For those connections
it continues accepting and immediately canceling newly opened streams until
terminal shutdown, without publishing them as sessions or dispatching a
handler.

An explicit `Endpoint.Shutdown(ctx)` passes its context to all applicable
phases. Cancellation of the context supplied to
`Endpoint.ListenAndServe` is intercepted by the endpoint so it does not reach
the transport before Router drain; that path uses the Router's configured
`ShutdownTimeout` when applying its internal drain bound. Pass an explicit
deadline to `Server.Shutdown` when the total process stop must be bounded.

`Router.Shutdown` executes accepted per-session sync work as well as accepted
async work, including sync tasks whose session closes during the graceful
drain. `Router.Close` is deliberately the immediate path: it closes admission
and asks session executors to release queued sync contexts, but it does not
wait. A sync task already ready in a scheduler race can still begin, and
accepted async queue work may still execute. Do not use `Close` as proof that
no accepted handler started; use `Shutdown` when accepted work must be
drained.

That ordering is a bounded handler-drain mechanism, not a reply-delivery
barrier. A handler completing, or even a successful `Send` placing its reply in
the outbound queue, does not confirm that the queued or in-flight bytes reached
the peer. The built-in terminal transport `Shutdown` closes sessions and
outbound queues; it does not wait for queued or writer-owned items to flush to
the network. KPro does not currently expose a separate outbound-drain phase.
Message sends remain at-most-once. Applications that require evidence of
network/application receipt should add message identifiers, idempotency,
acknowledgements, and bounded retry, or own an explicit application-level
outbound drain before calling terminal shutdown.

HTTP/3 has a different protocol-specific path: endpoint shutdown stops new
work, sends GOAWAY, and waits for active requests/connections to finish until
the shutdown context expires. On expiry, remaining connections are forced
closed and the context error is returned. Test this with the deployed client
implementations; a client must react to GOAWAY correctly for a clean drain.

When serving-context cancellation initiates shutdown, HTTP/3, gRPC, and
WebSocket return any graceful-shutdown failure joined with the serving result.
Callers can therefore use `errors.Is`/`errors.As` to distinguish a clean stop
from a drain deadline or another shutdown failure.

Operational consequences:

- make shutdown idempotent in custom transports and resources;
- make custom transport startup one-shot or otherwise define concurrent-start
  ownership explicitly;
- remove the instance from readiness before sending termination;
- set the platform termination grace period beyond the application's measured
  worst-case bounded stop plus any protocol-specific graceful drain;
- log every component error in the joined return value rather than assuming the first error is the only cause;
- treat an unexpected clean runner return as a process-ending event too.

HTTP invocation contexts follow the request deadline and disconnect behavior of
`net/http`. SSE request contexts additionally cancel when the endpoint begins
shutdown, so long-lived handlers should select on `Context.Done()` and return.
SSE `WriteTimeout` covers the initial stream flush and is renewed around each
`Context.Send` write and flush rather than limiting the lifetime of the HTTP
response. Its zero value disables those per-write deadlines; direct writes to
the embedded response writer are outside the `Send` deadline contract.

Message invocations can also follow a session's optional
`core.SessionLifecycle.Done` signal. Message handlers read the
middleware-updated cancellation, deadline, trace, and values from
`core.ContextOf(ctx)`. It reads the optional `core.ContextProvider` capability;
`core.BaseContext` implements it, while legacy custom contexts fall back to
`context.Background()`. A custom context that needs request propagation should
implement `core.ContextProvider`. A pooled `core.Context` or request-scoped
`core.Invocation` must not escape its handler; call `core.CloneContext` before
intentional message background work and arrange an application-owned
cancellation/deadline.

## Overload recovery

Message routing has two admission policies:

- `core.BackpressureReject` returns immediately when bounded capacity is exhausted;
- `core.BackpressureWait` waits for capacity until the router or session closes; callers that own a `core.Router` should use `DispatchContext` to add a request deadline.

`BackpressureWait` also bounds blocked callers independently from accepted
in-flight work. `MaxWaitingDispatches` defaults to 1024 Router-wide and
`MaxWaitingDispatchesPerSession` defaults to 16; normalization caps those
defaults by the corresponding global/per-session in-flight limits. A dispatch
acquires a ticket only when it first has to block, retains that same ticket
while it crosses global, per-session, and sync/async queue admission stages,
and releases it when `DispatchContext` returns. It never consumes one waiter
per stage. This bounds wait-state retention and prevents one session from
occupying every Router-wide waiter slot under the defaults. If both values are
set explicitly, keep the per-session limit below the global limit to preserve
that isolation.

Expected saturation errors include `core.ErrDispatchQueueFull`,
`core.ErrSendQueueFull`, `core.ErrTooManyInflight`, and
`core.ErrGlobalInflightLimit`. Wait-specific saturation can additionally
return `core.ErrGlobalWaitingDispatchLimit`,
`core.ErrSessionWaitingDispatchLimit`, or
`core.ErrTooManyOutboundWaiters`. They retain their specific `errors.Is`
identity and classify as `core.ErrorKindOverloaded`. Use
`core.WithErrorKind(kind, err)` for application errors that need the same
protocol-neutral mapping, and `core.PublicErrorMessage(err)` when implementing
a custom response boundary. Do not send `err.Error()` to an untrusted peer.

TCP, KCP, QUIC message streams, UDP peers, and WebSocket sessions use two
per-session outbound hard limits:

- `SendQueueSize` caps pending writes by count;
- `MaxPendingBytes` caps accounted bytes across encoding reservations,
  queued writes, and the write currently owned by the transport writer.

`Send` reserves one write and an estimated payload-plus-frame-header byte
count before invoking the codec, then reconciles that reservation against the
actual encoded size before publishing the write. Encoding failure, rejected
commit, and completed/failed writes release their matching capacity. Queue
close releases queued items; an in-progress encoder reservation or
writer-owned item remains accounted until its owner cancels, commits, or calls
completion. Zero `MaxPendingBytes` derives a transport-specific default from
`SendQueueSize` and the configured maximum frame, packet, or message size;
TCP/KCP/QUIC use `SendQueueSize * (MaxFrameSize + FrameHeaderSize)`,
UDP uses `SendQueueSize * MaxPacketSize`, and WebSocket uses
`SendQueueSize * MaxMessage`. Set both limits explicitly from workload
evidence. The byte budget accounts outbound frame reservations and retained
frames; it is not a process heap limit. In particular, an
application-supplied codec can allocate temporary working memory or return a
frame larger than the pre-encode estimate before the actual-size commit rejects
it.

The queue also maintains byte-watermark hysteresis. Built-ins expose its
point-in-time view through the optional `core.OutboundStateProvider`; use
`core.SessionOutboundState` without widening the base `core.Session`
interface:

```go
state, ok := core.SessionOutboundState(ctx.Session())
if ok && !state.Writable {
	// Advisory only: shed or defer optional work and record bounded telemetry.
}
if err := ctx.Reply(replyID, payload); err != nil {
	// This result is authoritative; state may have changed after the snapshot.
	return err
}
```

`PendingWrites` and `PendingBytes` include encoding reservations, queued
writes, and a writer-owned write. `Writable` is false when the queue is
closed, a hard count/byte cap is reached, or the high-watermark state is
active. That high-watermark state clears only after bytes drain to the low
watermark, avoiding rapid state flapping. It is an observation/throttling
hint, not a reservation: `Send` can still succeed above the high watermark
until a hard count or byte cap is reached, or fail after a writable snapshot
because another sender consumed capacity. `Send`/`Reply` remains the
authoritative admission result. TCP, KCP, QUIC, and WebSocket close the
affected session on a hard outbound-full result; UDP returns
`core.ErrSendQueueFull` without closing the virtual peer.

Code that explicitly prefers bounded waiting over shedding can use the
optional capability without changing `core.Session`:

```go
supported, err := core.SessionWaitOutboundWritable(ctx, session)
if supported && err != nil {
	return err
}
return session.Send(message)
```

The five built-in message sessions support this helper. Waiting is
context-cancelable and bounded per session; the default maximum number of
waiting writers equals that session's `SendQueueSize`. Exceeding it returns
`core.ErrTooManyOutboundWaiters`. `OutboundState.WaitingWriters` and
`MaxWaitingWriters` expose current and configured counts.

A successful wait is advisory: it reports a writable point in the
high/low-watermark state and does not reserve a frame or byte capacity for the
caller's next `Send`. Another sender may win the race, so always handle the
`Send` result. This distinction matters for TCP, KCP, QUIC, and WebSocket:
their hard outbound-full result closes the affected session. If an application
chooses to wait instead of shedding, call the helper before `Send`; do not
interpret `ErrSendQueueFull` as a retry signal on the same stream session.
UDP returns `ErrSendQueueFull` without closing its virtual peer, but the wait
is still advisory and any retry policy needs its own deadline and idempotency
decision.

Built-in HTTP maps rate limiting to 429, payload limits to 413, overload to 503,
and unknown/recovered failures to a generic 500. SSE uses the same mapping
before a stream starts and emits a safe `error` event after streaming begins.
gRPC maps the first three to `ResourceExhausted` and unknown/recovered failures
to `Internal`. Count the
original internal errors separately from the stable client response, and ensure
clients use bounded exponential backoff with jitter. Waiting is not free
capacity: it shifts memory and latency to blocked callers.

TCP, KCP, QUIC, UDP, and WebSocket configs also expose
`HandlerErrorPolicy` and `OnHandlerError`. The zero-value policy closes only the
affected session when the transport's supplied handler returns an error or
panics. Built-in direct handler invocation contains the panic and returns an
error wrapping `core.ErrRecovered`; an error-valued panic also retains its
original `errors.Is`/`errors.As` chain. The transport calls `OnHandlerError`
before applying the policy. `HandlerErrorContinue` keeps the session open after
a returned error or recovered panic; choose it only when subsequent messages
remain safe. Recovered panics always follow this explicit policy and are not
treated as ordinary Router admission rejections.

With a transport mounted through `server.Server`, the supplied handler is the
Router dispatch/admission boundary. Configure
`core.RouterConfig.ErrorObserver` for errors returned later by registered
Router handlers, recovered route panics, and failures to send the safe
`MsgError` response. Its payload-free event contains transport, connection,
remote address, message ID, sequence, the original error, optional response
error, and a panic stack. The direct transport `HandlerErrorEvent` contains the
same routing identifiers and original error but does not include a stack. A nil
Router observer preserves the standard-log compatibility behavior; a nil
direct `OnHandlerError` simply disables that callback. Observer panics are
isolated, but observers run synchronously and may run concurrently, so they
must remain fast, concurrency-safe, non-blocking, and subject to diagnostic
redaction policy.

Transport limits stop different failure modes. Connection/session/stream caps bound peer state; frame, packet, message, header, and body limits bound parsing allocations; send queues bound slow consumers; read/write/idle timeouts bound resource residence. Set all applicable layers so an upstream permissive limit cannot defeat a downstream safety limit.

`middleware.ConnRateLimit` uses transport, local connection ID, peer, and the
stable connection-lifetime channel as its bucket identity, so independently
numbered transports/endpoints do not share live buckets. Built-in invocation
bridges preserve that lifetime through tracing/deadline context wrappers.
`RateLimitConfig.IdleTimeout` removes inactive buckets when no lifetime signal
is available, and `MaxBuckets` bounds connection/IP limiter cardinality; a new
key is rejected as overload when the bound remains full after pruning. Custom
invocations should expose one stable `ConnDone` channel for every request on a
connection and a distinct channel for each connection.

## Dependency recovery

KPro database adapters expose `Ping` and `Health`, while several expose native pool statistics. The framework does not impose a cross-database retry, circuit-breaker, or failover policy. Configure driver timeouts and pools, classify retryable errors in the application, retry only idempotent operations or transactions designed for retry, and bound every attempt by the request deadline.

For source-of-truth failure:

1. fail readiness only when the service cannot safely serve its contract;
2. reject new dependent work promptly instead of accumulating an unbounded queue;
3. preserve the original error category for alerting;
4. verify reconnection without a process restart and also verify supervisor-driven restart;
5. reconcile any ambiguous writes before replay.

Outbox delivery is at least once. Lease expiry permits recovery after a worker crash, so projector handlers must be idempotent. Monitor pending age, claim/lease expiry, retry count, and dead-letter state; document who can replay and how a poison event is corrected. See [Database Production](database-production.md).

`database/redis.Lock` is a single-acquisition, token-owned Redis lease. A
successful Lock object is never reusable; construct a new one after release or
expiry. Compare-and-delete/refresh prevents a stale object from modifying a
new owner's lease, but the random token is not a monotonic fencing token. A
paused holder can resume after its TTL while a new holder is active. Restrict
the helper to short, idempotent or compensatable coordination, or enforce a
monotonic fence/transaction at the protected resource. Validate TTL, refresh,
partition, failover, and process-pause behavior against the target Redis
topology. A transport error from `Acquire` is not proof that `SET NX` failed:
the lease may exist even though its reply was lost. Reconcile the protected
state or wait for that attempt's TTL before allowing an alternate owner to run
non-idempotent work.

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
2. Compare active request, stream, and message-handler age with the configured application and platform deadlines.
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
- slow-client fraction, stream lifetime, termination/stop window, and any
  protocol-specific drain window.

Use production-like encryption, codecs, middleware, schemas, indexes, logging, and telemetry in the test. A loopback echo benchmark isolates framework overhead but does not establish application capacity.

The repository also provides the opt-in
[`TestTCPConnectionLoad`](../test/tcp_connection_load_test.go), which defaults
to 1000 concurrent connections and 10 request/response messages per connection:

```powershell
$env:KPRO_TCP_CONNECTION_LOAD_TEST = "1"
go test ./test -run TestTCPConnectionLoad -count=1 -timeout=10m
```

It verifies connection lifecycle, configured round trips, shutdown, and reuse
of connection slots by a second full wave. Elapsed time, heap, and goroutine
counts are recorded as observations for that run. This is a loopback framework
check, not application capacity or SLA evidence; change the dimensions and run
production-like scenarios before setting limits. See the
[integration test guide](../test/README.md) for its environment controls.

## Controls to size

| Layer | Primary controls | What they bound |
| --- | --- | --- |
| Message router | `SyncQueueSize`, `AsyncWorkers`, `AsyncQueueSize`, `MaxInflightPerSession`, `MaxInflight`, `MaxWaitingDispatches`, `MaxWaitingDispatchesPerSession`, `Backpressure`, `SessionIdleTimeout`, `ShutdownTimeout` | retained and blocked work, concurrency, per-session wait fairness, and bounded handler completion during Router shutdown; not transport reply delivery |
| TCP | `MaxConnections`, `MaxFrameSize`, `SendQueueSize`, `MaxPendingBytes`, `ReadTimeout`, `WriteTimeout` | connection state, parsing, pending write count/bytes, slow readers/writers |
| KCP | TCP-like controls including `SendQueueSize` and `MaxPendingBytes`, plus `SendWindow`, `ReceiveWindow`, `MTU`, shard and congestion settings | sessions, pending writes, buffers, loss/retransmission behavior |
| QUIC message streams | `MaxConnections`, `MaxStreams`, `MaxFrameSize`, `SendQueueSize`, `MaxPendingBytes`, read/write, handshake, and idle timeouts | physical connection state, message streams, framing, pending write count/bytes, handshakes, idle peers |
| UDP | `MaxSessions`, `MaxPacketSize`, `SendQueueSize`, `MaxPendingBytes`, `SessionIdleTimeout`, `CleanupInterval` | virtual-peer state, datagrams, pending write count/bytes, idle retention |
| WebSocket | `MaxConnections`, `MaxMessage`, `SendQueueSize`, `MaxPendingBytes`, `HandshakeTimeout`, `ReadTimeout`, `WriteTimeout`, `CheckOrigin` | upgraded sessions, HTTP upgrade/origin policy, reassembled messages, pending write count/bytes, and slow peers |
| HTTP/HTTPS | `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `ShutdownTimeout`, `MaxBodyBytes` | requests, bodies, keep-alive residence, drain |
| HTTP/3 | `MaxConnections`, `MaxBodyBytes`, `MaxHeaderBytes`, handshake/read/write/idle/shutdown timeouts, TLS and QUIC settings | physical connections, request bodies, streams, headers, drain; `Allow0RTT` is a replay-sensitive opt-in, not a capacity control |
| SSE | `MaxStreams`, `HandshakeTimeout`, `IdleTimeout`, `WriteTimeout`, `ShutdownTimeout` | active streams, request headers, keep-alive residence, initial stream flush, per-event `Send` writes and flushes, and drain |
| gRPC | `WithShutdownTimeout` and selected `grpc.ServerOption` limits | RPC/message/connection behavior and drain |
| SQL pools | max open/idle connections, connection lifetime/idle time | database concurrency and connection churn |

Defaults are guardrails for development, not a statement that every row has been sized for your service.

`RouterStats.SyncQueueDepth` is a total across all live sessions, while
`SyncQueueCapacityPerSession` is the limit for each independent session queue;
do not divide one by the other as a global saturation ratio. Under
`BackpressureWait`, handler-initiated reentrant work must call
`DispatchContext` with a deadline: the current handler can already own the
global or per-session capacity that the nested dispatch is waiting for.
`RouterStats.WaitingDispatches` is the current Router-wide ticket count;
`MaxWaitingDispatches` and `MaxWaitingDispatchesPerSession` expose its two
limits, while `GlobalWaitRejected` and `SessionWaitRejected` distinguish
which bound rejected new waiting work.

## Estimation and measurement

Use Little's Law as a first consistency check, not a benchmark result:

```text
expected active work ≈ arrival rate × observed end-to-end service time
expected DB concurrency ≈ DB-operation rate × observed DB hold time
```

Add a burst and uncertainty margin supported by tests. If required concurrency exceeds CPU, downstream pool, or latency objectives, reduce service time or scale capacity before raising in-flight limits.

A useful capacity-inventory estimate for message-path memory is:

```text
session state
+ sessions × MaxPendingBytes of accounted outbound reservations/writes
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
5. terminate instances under load and measure bounded stop, protocol-specific
   drain, reconnect, and recovery; validate the application's at-most-once
   message-delivery assumptions.

Record throughput, latency percentiles, error/rejection category, CPU, heap, allocation rate, goroutines, connections/sessions, queue depth, in-flight work, database pool waits, and oldest event age. Repeat after changing any limit; tune one bottleneck at a time.

## Performance regression budgets

Repository CI runs selected Router, HTTP handler, outbound-queue, in-memory
codec, TCP send-admission, and TCP/WebSocket batch-loop microbenchmarks and
checks their `ns/op`, `B/op`, and `allocs/op` against
[`benchmark/budgets.json`](../benchmark/budgets.json). Reproduce that guard with:

```sh
go test -p 1 ./core ./transport/httpx ./transport/internal/outbound ./transport/internal/dispatchgate ./transport/tcp ./transport/websocket ./benchmark -run '^$' -bench 'Benchmark(RouterDispatchSync|EndpointServeHTTP|OutboundQueueRoundTrip|OutboundQueueDrainAvailable|GateBeginEnd|KProCodec|SessionSendContiguous|SessionSendSegmented|ReserveOutboundBufferCommit|TCPSessionBatchWrite16x1KiB|WebSocketSessionBatchWrite16x1KiB)$' -benchmem -benchtime=250ms -count=3 > benchmark-current.txt
go run ./cmd/benchguard -budget benchmark/budgets.json -input benchmark-current.txt
```

`benchguard` compares median `ns/op` across the repeated samples, while
every `B/op` and `allocs/op` sample must remain within its hard budget.
The outbound queue, codec, and TCP send-admission cases are pure in-memory
checks and do not contain network or disk I/O. Queue round-trip/drain and the
default segmented TCP `Session.Send` path are locked to zero heap allocations;
the explicit owned-buffer path permits one allocation for its public lease
handle in the warmed 1 KiB case, without a new payload backing allocation per
operation. The TCP/WebSocket batch-loop guards use an in-memory discard
connection and do not establish syscall or network performance.

The budgets are intentionally repository-owned regression thresholds for this
specific code path and CI workload. They do not predict end-to-end service
capacity or establish an SLA. On failure, compare benchmark conditions and
profiles before changing a budget; require review and evidence for intentional
threshold changes.

## Repository verification and release evidence

Use a supported Go 1.25 or 1.26 patch release. The current CI matrix verifies
Go 1.25.12 and 1.26.5, with 1.26.5 used for the complete suite. Before proposing
a release, reproduce the security and coverage gates:

```sh
go mod verify
go test ./...
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go test -coverprofile=coverage.out ./core ./server ./middleware ./codec ./transport/... ./observability/... ./eventing/... ./database/... ./cmd/kpro ./cmd/benchguard
go tool cover -func=coverage.out
```

CI rejects reachable known vulnerabilities and aggregate framework coverage
below 55%, and uploads the coverage report as build evidence. A passing
`govulncheck` means no known vulnerability was found on the analyzed call
paths; it does not replace container, operating-system, configuration, or
deployment scanning.

`go run ./cmd/kpro doctor` reports the local toolchain/module environment and
`go run ./cmd/kpro version` reports CLI build metadata. A `v*` tag starts the
release workflow, which reuses the repository CI gates and then uses GoReleaser
to build a draft GitHub release with Linux, Windows, and macOS archives,
checksums, and per-archive SBOMs. The workflow validates the tag, artifact
matrix, checksums, SBOMs, and native CLI metadata before converting the draft
into the published release. Application teams still own their
workload-specific validation records.

## Observability boundary

`middleware.Metrics` supplies an in-process aggregate snapshot of
started/completed invocations, errors, panics, in-flight work, and latency.
`core.Router.Snapshot` and `server.Endpoint.Snapshot` expose Router admission,
queue, in-flight, session, rejection, and lifecycle counters. Database resources
provide health checks, and GORM/ClickHouse expose SQL pool stats.
For a live message session, `core.SessionOutboundState` optionally exposes
pending/max writes and bytes, current/max waiting writers, and
high/low-watermark state. It is a point-in-time advisory signal; the following
`Send` result remains authoritative.

The optional observability packages add concrete integration points:

- `prometheusx` registers invocation collectors with transport, stable
  operation, and result labels, plus bounded lifecycle/Router and explicit
  backlog/projector collectors. Router waiting capacity appears in
  `kpro_router_waiting_dispatches` with `kind="current"`,
  `kind="global_limit"`, or `kind="per_session_limit"` under the default
  namespace; `kpro_router_dispatch_total` adds the bounded outcomes
  `global_wait_rejected` and `session_wait_rejected`;
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
