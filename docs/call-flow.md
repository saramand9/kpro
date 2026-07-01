# KPro call flow

This document shows the runtime call paths for the current KPro universal server implementation.

## Startup

```mermaid
flowchart TD
    Main[cmd/kpro-universal-server/main.go] --> New[server.New]
    New --> App[server.Server]

    Main --> TCP[app.TCP]
    Main --> UDP[app.UDP]
    Main --> WS[app.WebSocket]
    Main --> HTTP[app.HTTP]
    Main --> HTTPMSG[app.HTTPMessage]
    Main --> SSE[app.SSE]

    TCP --> MountTCP[server.Mount]
    UDP --> MountUDP[server.Mount]
    WS --> MountWS[server.Mount]
    HTTP --> MountHTTP[server.MountRunner]
    HTTPMSG --> MountHTTPMSG[server.MountRunner]
    SSE --> MountSSE[server.MountRunner]

    MountTCP --> TCPEndpoint[server.Endpoint + core.Router]
    MountUDP --> UDPEndpoint[server.Endpoint + core.Router]
    MountWS --> WSEndpoint[server.Endpoint + core.Router]
    MountHTTP --> HTTPEndpoint[httpx.Endpoint]
    MountHTTPMSG --> HTTPMessageEndpoint[httpx.MessageEndpoint + core.Router]
    MountSSE --> SSEEndpoint[sse.Endpoint]

    Main --> Listen[app.ListenAndServe]
    Listen --> RunTCP[tcp.ListenAndServe]
    Listen --> RunUDP[udp.ListenAndServe]
    Listen --> RunWS[websocket.ListenAndServe]
    Listen --> RunHTTP[httpx.ListenAndServe]
    Listen --> RunHTTPMSG[httpx.MessageEndpoint.ListenAndServe]
    Listen --> RunSSE[sse.ListenAndServe]
```

## Message-Based Transports

TCP, UDP, WebSocket and the optional HTTP message endpoint share the same business model:

```text
transport input
  -> protocol decode
  -> core.NewContext
  -> core.Router.Dispatch
  -> middleware chain
  -> business handler
  -> ctx.Reply
  -> protocol encode
  -> transport output
```

## TCP

```mermaid
flowchart TD
    Client[Client TCP] --> Accept[tcp.Server.ListenAndServe]
    Accept --> Session[tcp.session]
    Session --> ReadLoop[session.readLoop]
    ReadLoop --> ProtocolRead[StreamProtocol.Read]
    ProtocolRead --> Message[core.Message]
    Message --> Context[core.NewContext]
    Context --> Dispatch[core.Router.Dispatch]
    Dispatch --> Middleware[core.Middleware]
    Middleware --> Handler[Business Handler]
    Handler --> Reply[ctx.Reply]
    Reply --> Send[session.Send]
    Send --> Encode[StreamProtocol.Encode]
    Encode --> Queue[sendQueue]
    Queue --> WriteLoop[session.writeLoop]
    WriteLoop --> Client
```

Key files:

```text
transport/tcp/server.go
protocol/kpro.go
core/context.go
core/router.go
```

## UDP

```mermaid
flowchart TD
    Client[Client UDP] --> ReadFromUDP[udp.Server.ListenAndServe]
    ReadFromUDP --> DecodePacket[PacketProtocol.DecodePacket]
    DecodePacket --> Message[core.Message]
    Message --> PacketSession[getOrCreate udp.packetSession by remote IP:port]
    PacketSession --> Context[core.NewContext]
    Context --> Dispatch[core.Router.Dispatch]
    Dispatch --> Middleware[core.Middleware]
    Middleware --> Handler[Business Handler]
    Handler --> Reply[ctx.Reply]
    Reply --> Send[packetSession.Send]
    Send --> EncodePacket[PacketProtocol.EncodePacket]
    EncodePacket --> Queue[per-remote sendQueue]
    Queue --> WriteLoop[packetSession.writeLoop]
    WriteLoop --> WriteToUDP[UDP WriteToUDP]
    WriteToUDP --> Client
```

UDP has no real connection. KPro maps each remote IP:port to a virtual session. Each virtual session owns its own bounded send queue and writer goroutine, and idle sessions are cleaned up by timeout.

## WebSocket

```mermaid
flowchart TD
    Client[WebSocket Client] --> Upgrade[websocket.serveWS]
    Upgrade --> Hijack[HTTP Hijack TCP connection]
    Hijack --> WSSession[websocket.session]
    WSSession --> ReadLoop[session.readLoop]
    ReadLoop --> ReadFrame[readFrame]
    ReadFrame --> DecodePacket[PacketProtocol.DecodePacket]
    DecodePacket --> Message[core.Message]
    Message --> Context[core.NewContext]
    Context --> Dispatch[core.Router.Dispatch]
    Dispatch --> Middleware[core.Middleware]
    Middleware --> Handler[Business Handler]
    Handler --> Reply[ctx.Reply]
    Reply --> Send[session.Send]
    Send --> EncodePacket[PacketProtocol.EncodePacket]
    EncodePacket --> WriteFrame[writeFrame]
    WriteFrame --> Client
```

WebSocket currently treats one WebSocket message as one protocol packet.

## HTTP / HTTPS

HTTP and HTTPS now have two explicit modes.

The native HTTP mode keeps method + path routing and is appropriate for REST-style APIs:

```mermaid
flowchart TD
    Client[HTTP Client] --> Listen[httpx.Endpoint.ListenAndServe]
    Listen --> NetHTTP[net/http Server]
    NetHTTP --> Serve[Endpoint.serve]
    Serve --> Path{path exists}
    Path -->|no| NotFound[404 not found]
    Path -->|yes| Method{method exists}
    Method -->|no| MethodNotAllowed[405 method not allowed]
    Method -->|yes| Middleware[httpx.Middleware]
    Middleware --> Handler[HTTP Handler]
    Handler --> Response[ctx.Text / ctx.JSON / ctx.Bytes / ctx.Stream]
    Handler -->|error| ServerError[500 err.Error]
    Response --> Client
```

The message HTTP mode maps method + path routes to `core.Message.ID`, then reuses the same `core.Router`, middleware and sync/async dispatch model as TCP:

```mermaid
flowchart TD
    Client[HTTP Client] --> Listen[httpx.MessageEndpoint.ListenAndServe]
    Listen --> NetHTTP[net/http Server]
    NetHTTP --> Serve[Endpoint.serve]
    Serve --> Route[method + path -> message id]
    Route --> RequestBody[read request body]
    RequestBody --> Message[core.Message]
    Message --> Session[per-request HTTP session]
    Session --> Context[core.NewContext]
    Context --> Dispatch[core.Router.Dispatch]
    Dispatch --> Middleware[core.Middleware]
    Middleware --> Handler[Business Handler]
    Handler --> Reply[ctx.Reply / session.Send]
    Reply --> Response[HTTP status, headers, body]
    Response --> Client
```

`HTTPMessage` and `HTTPSMessage` are the TCP-like entry points. They preserve HTTP protocol semantics at the edge, but business code runs against `core.Context`.

## SSE

```mermaid
flowchart TD
    Client[SSE Client] --> Listen[sse.Endpoint.ListenAndServe]
    Listen --> Serve[sse.Endpoint.serve]
    Serve --> Context[sse.Context]
    Context --> Handler[SSE Handler]
    Handler --> Send[ctx.Send event data]
    Send --> Stream[text/event-stream]
    Stream --> Client
```

SSE is a one-way server push transport. It is intentionally not modeled as request/reply.

## Shutdown

```mermaid
flowchart TD
    Signal[Shutdown signal] --> AppShutdown[server.Server.Shutdown]
    AppShutdown --> Cancel[context cancel]
    Cancel --> TCPShutdown[tcp.Shutdown]
    Cancel --> UDPShutdown[udp.Shutdown]
    Cancel --> WSShutdown[websocket.Shutdown]
    Cancel --> HTTPShutdown[httpx.Shutdown]
    Cancel --> SSEShutdown[sse.Shutdown]
    TCPShutdown --> Done[wait goroutines]
    UDPShutdown --> Done
    WSShutdown --> Done
    HTTPShutdown --> Done
    SSEShutdown --> Done
```
