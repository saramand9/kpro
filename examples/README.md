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

## Compile All Examples

```powershell
go test .\examples\...
```
