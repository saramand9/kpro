# Fast HTTP/1.1 Endpoint

`transport/fasthttpx` is an opt-in, protocol-native HTTP/1.1 runner backed by
`github.com/valyala/fasthttp`. It participates in the same KPro server
lifecycle, readiness, validation, capability, and `core.Middleware` contracts
as the standard HTTP endpoint.

## Choose the endpoint deliberately

| Requirement | Endpoint |
| --- | --- |
| `net/http` handlers, middleware, request cancellation, or HTTP/2 | `transport/httpx` |
| Measured HTTP/1.1 throughput or allocation pressure justifies a native API | `transport/fasthttpx` |
| HTTP semantics over QUIC | `transport/http3` |

`fasthttpx` is not a drop-in replacement for `httpx`. It does not expose
`http.Handler`, `http.ResponseWriter`, or `*http.Request`, and its TLS listener
advertises only the `http/1.1` ALPN protocol. Shared business services should
sit behind both handler adapters when an application needs both APIs.

## Mount and route

```go
app, err := server.NewChecked(server.Config{
	Name:            "api",
	ShutdownTimeout: 5 * time.Second,
})
if err != nil {
	return err
}

endpoint, err := server.MountNativeChecked(app, fasthttpx.New(fasthttpx.Config{
	Addr:            ":8082",
	ReadTimeout:     10 * time.Second,
	WriteTimeout:    10 * time.Second,
	IdleTimeout:     30 * time.Second,
	ShutdownTimeout: 5 * time.Second,
	MaxBodyBytes:    1 << 20,
}))
if err != nil {
	return err
}

endpoint.Get("/users/{id}", func(ctx *fasthttpx.Context) error {
	return ctx.JSON(200, map[string]string{"id": ctx.Param("id")})
})
```

Routes use `{name}` parameters through `fasthttp/router`. Endpoint, group, and
route-local middleware run through `core.Invocation`; middleware operation names
remain method plus registered path, such as `GET /users/{id}`.

## Ownership and cancellation

`fasthttpx.Context`, `Native()`, request bodies, byte-valued headers, query
values, and path values are borrowed until the handler returns. Copy anything
that must outlive the handler. `Body`, `CopyBody`, and `JSONBody` enforce the
configured endpoint or middleware payload limit.

The base `Context()` is canceled when endpoint shutdown begins. fasthttp does
not provide the same peer-disconnect request cancellation contract as
`net/http`; handlers that need shorter work bounds must add an application
deadline through middleware or service code.

## Capacity and shutdown

Set `Concurrency`, `MaxConnsPerIP`, `MaxRequestsPerConn`, `ReadBufferSize`,
`WriteBufferSize`, `MaxBodyBytes`, and all timeouts from measured deployment
requirements. `ReadBufferSize` also bounds the request line and headers.

Shutdown first stops admission and cancels endpoint contexts, then waits for
fasthttp's graceful drain. If the supplied deadline expires, KPro closes the
tracked native connections and returns the shutdown error. An endpoint is
one-shot; construct a new instance to restart it.

Run the example with:

```sh
go run ./examples/fasthttp/server -addr :8082
go run ./examples/http/client -base http://127.0.0.1:8082 -text "hello fasthttp"
```

Compare `httpx` and `fasthttpx` with the same routes, response bytes,
connection reuse, client concurrency, payload sizes, middleware, and timeout
policy. A microbenchmark result alone is not a production capacity claim.
