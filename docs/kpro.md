# KPro TCP framework

KPro is a production-oriented TCP entry layer example for this repository. It is designed for validating about 10k long-lived connections with bounded queues, timeout protection, worker isolation, and HTTP metrics.

## Architecture

```text
accept loop
  -> per-connection reader goroutine
  -> bounded global worker queue
  -> worker pool
  -> bounded per-connection send queue
  -> per-connection writer goroutine
```

Go's runtime netpoller provides the underlying non-blocking I/O multiplexing. KPro keeps the application code simple while adding the resource boundaries required for user-facing connections.

## Protocol

Protocol parsing is now a configurable package boundary:

```go
cfg := kpro.DefaultConfig()
cfg.Protocol = protocol.KProV1()
server := kpro.NewServer(cfg)
```

The TCP transport only owns connections, deadlines and queues. `kpro/protocol`
owns wire format parsing and encoding, and `kpro/core.Message` is the business
packet passed to handlers.

Frame header is 20 bytes, big endian:

```text
uint16 magic      0x4b50
uint8  version    1
uint8  flags
uint32 message id
uint64 sequence
uint32 body length
bytes  body
```

Built-in message ids:

```text
1          heartbeat
2          pong
100        echo
0xffffffff error
```

## Handler API

```go
s := kpro.NewServer(kpro.DefaultConfig())

s.Use(func(next kpro.InvocationHandler) kpro.InvocationHandler {
	return func(inv kpro.Invocation) error {
		// auth, rate limit, trace, etc.
		return next(inv)
	}
})

s.Handle(kpro.MsgEcho, func(ctx kpro.Context) error {
	return ctx.Reply(kpro.MsgEcho, ctx.Message().Data)
})
```

`Use` middlewares are snapshotted when each message is dispatched, so new middleware applies to later requests without re-registering handlers.

## Package Direction

```text
kpro/core      Message and shared business concepts
kpro/protocol  StreamProtocol and PacketProtocol implementations
kpro           current TCP server, context, router and metrics
```

UDP and WebSocket transports reuse `core.Message` and protocol
interfaces instead of duplicating packet definitions.

## Run server

```powershell
go run .\cmd\kpro-server -host 0.0.0.0 -port 8088 -max-conns 10000 -workers 16
```

`-max-conns-per-ip` defaults to `0`, which means no per-IP connection limit. Set it to a positive value for public-facing deployments.

Metrics:

```text
http://127.0.0.1:9090/metrics
http://127.0.0.1:9090/healthz
http://127.0.0.1:9090/debug/pprof/
```

## Run client

```powershell
go run .\cmd\kpro-client -addr 127.0.0.1:8088 -conns 10000 -rate 1 -duration 60s -ramp 30s -drain 5s -payload 64
```

Important metrics to watch:

```text
active_connections
goroutines
heap_alloc_bytes
frames_in / frames_out
worker_queue_full
send_queue_full
read_errors / write_errors
handler_errors
```

## Safety boundaries

KPro closes connections when:

```text
frame is invalid
frame body exceeds MaxFrameSize
per-connection inflight limit is exceeded
worker queue is full
send queue is full
read/write deadline expires
idle timeout expires
per-IP connection limit is exceeded
```
