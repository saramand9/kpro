# Extending KPro

KPro's extension points are small interfaces rather than a general plugin registry. Keep custom implementations in an application-owned package until their lifecycle, limits, and compatibility have been proven.

## Add a message transport

A message transport implements `transport.MessageServer`
(`transport.Server` remains a compatibility alias):

```go
type Server interface {
	Name() string
	ListenAndServe(context.Context, core.Handler) error
	Shutdown(context.Context) error
}
```

Its live peer object implements `core.Session`:

```go
type Session interface {
	ID() uint64
	Transport() core.Transport
	RemoteAddr() net.Addr
	Send(core.Message) error
	Close() error
}
```

Also implement the optional `core.SessionLifecycle` capability by returning a `Done() <-chan struct{}` that closes exactly once. Router cancellation and per-session cleanup are more precise when this signal exists.

Message handlers obtain middleware-updated cancellation, deadlines, tracing,
and values with `core.ContextOf(messageCtx)`. The helper reads the optional
`core.ContextProvider` capability implemented by `core.BaseContext` and
middleware-decorated contexts. A custom `core.Context` may implement
`core.ContextProvider` when it needs request propagation; legacy implementations
remain compatible and receive `context.Background()` from `core.ContextOf`.

Advertise the transport through the optional, dependency-free capability API:

```go
func (*Server) Descriptor() transport.Descriptor {
	return transport.Descriptor{
		Name:         "example-stream",
		Protocol:     core.Transport("example-stream"),
		Mode:         transport.ModeMessage,
		Capabilities: transport.CapabilitySessions |
			transport.CapabilityStream |
			transport.CapabilityRequestResponse |
			transport.CapabilityMiddleware,
	}
}

func (s *Server) Validate() error {
	if s == nil {
		return errors.New("example stream server is nil")
	}
	return errors.Join(s.Descriptor().Validate(), validateConfig(s.config))
}
```

`transport.Describe(component)` returns an advertised descriptor and provides
a conservative fallback for legacy components. A descriptor is metadata for
diagnostics, validation, and tooling; it does not perform protocol negotiation,
authorize a client, or change the component's startup interface. Call
`Descriptor.Validate` in tests and from the component's optional
`transport.Validator` implementation.

`server.Server.Run` and `ListenAndServeContext` aggregate mounted
`transport.Validator` errors before starting serving goroutines. Keep
validation deterministic and side-effect-free: inspect configuration and
cross-field invariants, but do not bind ports,
start goroutines, mutate global state, or perform an unbounded network probe.
Return independent problems with `errors.Join` so one run gives the operator a
useful configuration report.

Implement the optional readiness capability when the component owns a listener:

```go
type ReadinessProvider interface {
	Ready() <-chan struct{}
}
```

Create the channel during construction and close it exactly once after the
listener has been bound and published. Do not close it on bind failure or
shutdown-before-start: the serving result is the failure signal, and closing
`Ready` would race that result and could publish false readiness.
Without this capability, orchestration uses the legacy fallback of considering
the component ready when `ListenAndServe` is entered.

Third-party transports may use the public `transport/transportkit` package for
the same focused helpers as built-ins: listen-address and scalar validation,
TLS/ALPN checks, cancellation-driven shutdown watchers, and full stream writes.
Do not import `transport/internal/...`; those packages are not an extension
contract. `transportkit` helpers do not provide a complete lifecycle—your
component still owns listener publication, one-shot state, capacity counters,
error policy, and idempotent shutdown.

The receive path should:

1. accept or identify a peer and allocate a stable, collision-free session ID;
2. apply connection, frame/message, read deadline, and idle limits before allocation grows without bound;
3. decode exactly one `core.Message` with a `codec.StreamCodec` or `codec.PacketCodec`;
4. create the dispatch value with `core.AcquireContext(session, message)` and call the supplied handler;
5. observe and classify the returned error according to the protocol contract.

For message protocols, the shared `transport.HandlerErrorPolicy`,
`HandlerErrorObserver`, and `ContinueAfterHandlerError` helper provide the same
behavior used by built-ins. The zero value closes the affected session;
`HandlerErrorContinue` keeps it open after notifying the observer. The event is
a stable snapshot and preserves the original error for internal diagnostics.
When the transport is mounted through `server.Server`, the supplied handler is
Router dispatch, so the policy observes dispatch/admission errors; errors from
an accepted registered route are handled and safely reported inside the Router.
Never block the read loop on slow telemetry, and do not rely on an observer
panic to alter the session policy.

When mounted through `app.Mount(customTransport)`, the endpoint router owns the acquired context after handler handoff. Do not retain or mutate it after the handler returns. Code that intentionally continues in the background must call `core.CloneContext` first; the clone preserves the current standard context and copied message/value containers but still shares the live session, so replies can fail after disconnect.

The send and shutdown paths should have bounded outbound queues, explicit
slow-consumer behavior, idempotent close, a context-aware bounded stop, and no
goroutine left waiting forever on an unresponsive peer. Message sends are
at-most-once: shutdown closes acceptance, session, and writer paths and does not
promise that queued or in-flight replies reach the peer. Verify partial frames,
malformed packets, duplicate shutdown calls, remote disconnect, queue
saturation, and cancellation under the race detector.

Mount and route the implementation like a built-in message transport:

```go
endpoint := app.Mount(mytransport.New(config))
endpoint.HandleSync(100, handleRequest)
```

`HandleSync` is the default for ordered per-session work. Use `HandleAsync` only for concurrency-safe operations that may finish out of order.

## Add a native protocol runner

If converting the protocol into `core.Message` would discard useful semantics,
implement `transport.NativeRunner` instead (`transport.Runner` remains a
compatibility alias):

```go
type Runner interface {
	Name() string
	ListenAndServe(context.Context) error
	Shutdown(context.Context) error
}
```

The runner owns its native routing and handler contract. Implement
`ApplyMiddleware(...core.Middleware)` to satisfy
`transport.MiddlewareTarget` when app-level invocation middleware is
meaningful, implement `transport.CapabilityProvider` and
`transport.Validator` as described above, implement
`transport.ReadinessProvider` when the runner owns a listener, and materialize a
`core.Invocation` at actual handler execution time. Mount it with
`app.MountRunner(runner)`.

Cancellation, connection identity, operation naming, headers, metadata, payload, and `Native()` must be stable for the lifetime of one invocation. When multiple invocations share a physical connection, populate `InvocationOptions.ConnDone` or implement `ConnDone() <-chan struct{}` with one stable channel for that connection. `core.InvocationConnDone` deliberately does not fall back to request cancellation, so connection-scoped middleware is not reset after every request. Do not retain `core.Invocation` after its handler returns.

## Add a codec

Use `codec.StreamCodec` for transports that read an unbounded byte stream and `codec.PacketCodec` when the carrier already preserves packet/message boundaries.

Stream codecs must return one complete read result, tolerate fragmented reads, reject declared lengths over the supplied maximum before allocating, and make EOF versus truncated-frame behavior explicit. Packet codecs must reject trailing or malformed data and never assume a datagram is trustworthy because its boundary is intact.

Both codec forms should have deterministic encode/decode tests, maximum-size boundary tests, fuzz tests for decoding, and wire fixtures shared with every non-Go client.

## Add middleware

Middleware wraps a protocol-neutral `core.InvocationHandler`:

```go
func Observe(record func(transport, operation string, elapsed time.Duration, err error)) core.Middleware {
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(inv core.Invocation) error {
			started := time.Now()
			err := next(inv)
			record(string(inv.Transport()), inv.Operation(), time.Since(started), err)
			return err
		}
	}
}
```

The function returned by a middleware may be called concurrently; captured state and recorders must be concurrency-safe. Middleware runs in registration order, so the first item is the outermost wrapper. Always call `next` at most once, return its error unless intentionally translating it, and use `defer` when observation must survive a panic that an outer recovery layer will catch.

`Invocation`, payload views, and native contexts are request-scoped. Do not retain them after return. Prefer `Transport()`, `Operation()`, `Context()`, and capability checks; use `Native()` only when a feature is inherently protocol-specific and guard every type assertion.

Install cross-transport middleware with `app.Use(...)` and endpoint-specific
middleware with the endpoint's `Use(...)` method when available. HTTP and
HTTP/3 routes additionally accept route-local middleware, and `Group` snapshots
group middleware for subsequently registered routes. The execution order is
endpoint, group, route, then handler.

For metrics, treat `Operation()` as a registered, bounded route key. The
`prometheusx` invocation collectors intentionally use only transport,
operation, and result labels. Lifecycle, Router, backlog, and projector
collectors require caller-controlled component/consumer/projector names at
construction. Do not add raw URLs, remote addresses, connection IDs, tenants,
event IDs, error text, or arbitrary metadata to a metric vector. `zapx` may
record optional remote data as a log field, where retention and redaction are
application policy rather than time-series cardinality.

`otelx` accepts a `trace.TracerProvider` and propagator but intentionally does
not construct an SDK, sampler, or exporter. The application owns those pieces
and their flush/shutdown lifecycle. Place OTel before Zap when Zap should read
the active trace and span IDs from `Invocation.Context()`.

Errors crossing a protocol boundary should carry a stable classification while
retaining their original cause:

```go
return core.WithErrorKind(core.ErrorKindOverloaded, err)
```

Available kinds cover rate limiting, payload size, bounded-capacity overload,
internal failure, and recovered panic. `core.ErrorKindOf` reads a category and
`core.PublicErrorMessage` returns safe client text; an unclassified error is
treated as internal. Built-in HTTP and pre-stream SSE map the public categories
to 429, 413, 503, or 500; active SSE uses a safe error event. gRPC maps capacity
categories to `ResourceExhausted` and internal categories to `Internal`. A
custom transport should follow equivalent native semantics and must not
serialize `err.Error()` or panic values to untrusted
clients. Log the retained cause at the internal observation boundary.

## Add a health check

`healthx.CheckFunc` is called concurrently and receives a context bounded by
the probe request and `healthx.Config.Timeout`:

```go
healthx.Check{
	Name: "orders-db",
	Run: func(ctx context.Context) error {
		return database.Ping(ctx)
	},
}
```

Pass that context to every dependency call and select on `ctx.Done()` in custom
waiting loops. The HTTP handler can return 503 at its deadline, but Go cannot
force a check function that ignores cancellation to stop. `healthx` will not
start an overlapping execution of that same stuck check; later probes report it
failed until the original returns. Check functions must still be
concurrency-safe, cheap, and free of repair/migration side effects. Use stable
unique names, and expose raw errors only on an appropriately protected endpoint.

## Add a database adapter

Database adapters share the `database/common/lifecycle.Resource` contract:

```go
type Resource interface {
	Name() string
	Kind() lifecycle.Kind
	Ping(context.Context) error
	Health(context.Context) lifecycle.Health
	Close() error
}
```

Use the shared lifecycle shape and make constructor-specific ownership explicit. Existing adapters differ in how they open, wrap, and close native clients, so verify each adapter before registering it as process-owned:

- `Open` normalizes configuration, creates or accepts a native client, validates connectivity with a bounded context, and cleans up on partial failure;
- `Ping` performs a cheap real dependency check and honors cancellation;
- `Health` uses the shared status vocabulary and includes bounded, non-sensitive diagnostic details;
- `Close` is safe to call after partial initialization; a new adapter that accepts an injected client should not close it unless ownership was explicitly transferred;
- narrowly scoped typed helpers expose useful native capabilities without moving business queries into the adapter package.

`lifecycle.Resource` uses `Close()`, while `server.Server.Manage` requires `Shutdown(context.Context)`. Add an application-owned context-aware wrapper when the process server should own the resource, and document whether a timeout can interrupt the underlying driver's close operation.

Business modules should depend on application-owned repository or read-model ports. Returning a native client is useful for advanced operations, but it should not make the concrete adapter part of every service interface.

Test configuration, ownership, health classification, cancellation, query encoding, retryable/non-retryable error mapping, and idempotent close. Protocol fakes are useful unit tests; production use also requires an opt-in suite against the exact engine version and topology.

## Extension completion checklist

- [ ] Public contract and ownership are documented.
- [ ] Every input, queue, connection, and allocation has a bound or an explicit reason not to.
- [ ] Cancellation and repeated shutdown are tested.
- [ ] `Descriptor().Validate()` and side-effect-free `Validate()` cover configuration invariants.
- [ ] Listener-owning components implement `transport.ReadinessProvider` and test bind failure, shutdown-before-start, and readiness ordering.
- [ ] Third-party code uses public `transport`/`transportkit` APIs and does not import `transport/internal` packages.
- [ ] Health checks honor their supplied context and are safe under concurrent probes.
- [ ] Concurrent access passes `go test -race` on relevant packages.
- [ ] Errors preserve useful causes and avoid secrets or payload leakage.
- [ ] Handler-error close/continue policy and observer behavior are explicit and tested.
- [ ] Metrics and logs have bounded cardinality.
- [ ] Compatibility fixtures exist for wire or storage formats.
- [ ] Real dependency or network integration tests cover the deployment topology.
- [ ] Examples compile against only public APIs.
