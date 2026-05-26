# KPro examples

Each protocol example has a standalone server and client. Start the server in one terminal, then run the matching client in another terminal.

## TCP

```powershell
go run .\examples\tcp\server -addr :8088
go run .\examples\tcp\client -addr 127.0.0.1:8088 -text "hello tcp"
```

## UDP

```powershell
go run .\examples\udp\server -addr :8089
go run .\examples\udp\client -addr 127.0.0.1:8089 -text "hello udp"
```

## WebSocket

```powershell
go run .\examples\websocket\server -addr :8091 -path /ws
go run .\examples\websocket\client -url ws://127.0.0.1:8091/ws -text "hello websocket"
```

## HTTP

```powershell
go run .\examples\http\server -addr :8080
go run .\examples\http\client -base http://127.0.0.1:8080 -text "hello http"
```

## HTTPS

The HTTPS server generates a local self-signed certificate at startup. The client skips certificate verification because this is a local example.

```powershell
go run .\examples\https\server -addr :8443
go run .\examples\https\client -base https://127.0.0.1:8443 -text "hello https"
```

## SSE

The SSE client reads a few events and exits.

```powershell
go run .\examples\sse\server -addr :8081 -path /events
go run .\examples\sse\client -url http://127.0.0.1:8081/events -events 3
```

## Modular Route Examples

These demos keep startup code, route assembly, and feature modules separate. Each protocol has its own standalone modular server.
Message-protocol modular examples use `HandleSync` by default; use `HandleAsync` for routes that can run concurrently and return out of order.

### HTTP Modular

```powershell
go run .\examples\http-modular\server -addr :8080
go run .\examples\http\client -base http://127.0.0.1:8080 -text "hello modular http"
Invoke-RestMethod -Uri http://127.0.0.1:8080/users
Invoke-RestMethod -Uri http://127.0.0.1:8080/users/active
```

### HTTPS Modular

```powershell
go run .\examples\https-modular\server -addr :8443
go run .\examples\https\client -base https://127.0.0.1:8443 -text "hello modular https"
```

### TCP Modular

```powershell
go run .\examples\tcp-modular\server -addr :8088
go run .\examples\tcp\client -addr 127.0.0.1:8088 -text "hello modular tcp"
```

### UDP Modular

```powershell
go run .\examples\udp-modular\server -addr :8089
go run .\examples\udp\client -addr 127.0.0.1:8089 -text "hello modular udp"
```

### WebSocket Modular

```powershell
go run .\examples\websocket-modular\server -addr :8091 -path /ws
go run .\examples\websocket\client -url ws://127.0.0.1:8091/ws -text "hello modular websocket"
```

### SSE Modular

```powershell
go run .\examples\sse-modular\server -addr :8081 -path /events
go run .\examples\sse\client -url http://127.0.0.1:8081/events -events 3
```

### Multi-Protocol Modular

This demo uses one modular route assembly layer for HTTP, HTTPS, TCP, UDP, WebSocket and SSE. The `echo` module registers both HTTP and message-protocol routes.

```powershell
go run .\examples\modular\server
go run .\examples\http\client -base http://127.0.0.1:8080 -text "hello modular http"
go run .\examples\tcp\client -addr 127.0.0.1:8088 -text "hello modular tcp"
go run .\examples\udp\client -addr 127.0.0.1:8089 -text "hello modular udp"
go run .\examples\websocket\client -url ws://127.0.0.1:8091/ws -text "hello modular websocket"
go run .\examples\sse\client -url http://127.0.0.1:8081/events -events 3
Invoke-RestMethod -Uri http://127.0.0.1:8080/users
```

Enable HTTPS on the same HTTP route modules:

```powershell
go run .\examples\modular\server -https :8443
go run .\examples\https\client -base https://127.0.0.1:8443 -text "hello modular https"
```

## Compile All Examples

```powershell
go test .\examples\...
```
