# KPro Examples

Each protocol has a small server and client pair:

```text
example/tcp
example/udp
example/websocket
example/http
example/sse
```

Run a server in one terminal and its client in another.

## TCP

```powershell
go run .\example\tcp\server
go run .\example\tcp\client -text "hello tcp"
```

## UDP

```powershell
go run .\example\udp\server
go run .\example\udp\client -text "hello udp"
```

## WebSocket

```powershell
go run .\example\websocket\server
go run .\example\websocket\client -text "hello websocket"
```

## HTTP

```powershell
go run .\example\http\server
go run .\example\http\client -text "hello http"
```

## SSE

```powershell
go run .\example\sse\server
go run .\example\sse\client -events 3
```

KCP is not included here because the current KCP transport is a placeholder and returns `kcp transport is not implemented`.
