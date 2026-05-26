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
    Main --> SSE[app.SSE]

    TCP --> MountTCP[server.Mount]
    UDP --> MountUDP[server.Mount]
    WS --> MountWS[server.Mount]
    HTTP --> MountHTTP[server.MountRunner]
    SSE --> MountSSE[server.MountRunner]

    MountTCP --> TCPEndpoint[server.Endpoint + core.Router]
    MountUDP --> UDPEndpoint[server.Endpoint + core.Router]
    MountWS --> WSEndpoint[server.Endpoint + core.Router]
    MountHTTP --> HTTPEndpoint[httpx.Endpoint]
    MountSSE --> SSEEndpoint[sse.Endpoint]

    Main --> Listen[app.ListenAndServe]
    Listen --> RunTCP[tcp.ListenAndServe]
    Listen --> RunUDP[udp.ListenAndServe]
    Listen --> RunWS[websocket.ListenAndServe]
    Listen --> RunHTTP[httpx.ListenAndServe]
    Listen --> RunSSE[sse.ListenAndServe]
```

## Message-Based Transports

TCP, UDP and WebSocket share the same business model:

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
    Message --> PacketSession[udp.packetSession]
    PacketSession --> Context[core.NewContext]
    Context --> Dispatch[core.Router.Dispatch]
    Dispatch --> Middleware[core.Middleware]
    Middleware --> Handler[Business Handler]
    Handler --> Reply[ctx.Reply]
    Reply --> Send[packetSession.Send]
    Send --> EncodePacket[PacketProtocol.EncodePacket]
    EncodePacket --> WriteToUDP[UDP WriteToUDP]
    WriteToUDP --> Client
```

UDP has no real connection. KPro maps each remote address to a lightweight virtual session ID.

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

```mermaid
flowchart TD
    Client[HTTP Client] --> Listen[httpx.Endpoint.ListenAndServe]
    Listen --> ServePath[Endpoint.servePath]
    ServePath --> Route[routes path method]
    Route --> Middleware[httpx.Middleware]
    Middleware --> Handler[HTTP Handler]
    Handler --> Response[ctx.Text / ctx.JSON]
    Response --> Client
```

HTTP and HTTPS use method + path routing rather than `core.Message.ID`.

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
