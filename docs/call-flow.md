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
    Main --> GRPC[app.GRPC]

    TCP --> MountTCP[server.Mount]
    UDP --> MountUDP[server.Mount]
    WS --> MountWS[server.Mount]
    HTTP --> MountHTTP[server.MountRunner]
    SSE --> MountSSE[server.MountRunner]
    GRPC --> MountGRPC[server.MountRunner]

    MountTCP --> TCPEndpoint[server.Endpoint + core.Router]
    MountUDP --> UDPEndpoint[server.Endpoint + core.Router]
    MountWS --> WSEndpoint[server.Endpoint + core.Router]
    MountHTTP --> HTTPEndpoint[httpx.Endpoint]
    MountSSE --> SSEEndpoint[sse.Endpoint]
    MountGRPC --> GRPCEndpoint[grpcx.Server]

    Main --> Listen[app.ListenAndServe]
    Listen --> RunTCP[tcp.ListenAndServe]
    Listen --> RunUDP[udp.ListenAndServe]
    Listen --> RunWS[websocket.ListenAndServe]
    Listen --> RunHTTP[httpx.ListenAndServe]
    Listen --> RunSSE[sse.ListenAndServe]
    Listen --> RunGRPC[grpcx.ListenAndServe]
```

## Message-Based Transports

TCP, UDP and WebSocket share the same message business model:

```text
transport input
  -> protocol decode
  -> core.NewContext
  -> core.Router.Dispatch
  -> queue / scheduling
  -> core.Invocation
  -> core.Middleware chain
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
    Dispatch --> Invocation[core.Invocation]
    Invocation --> Middleware[core.Middleware]
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
    Dispatch --> Invocation[core.Invocation]
    Invocation --> Middleware[core.Middleware]
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
    Dispatch --> Invocation[core.Invocation]
    Invocation --> Middleware[core.Middleware]
    Middleware --> Handler[Business Handler]
    Handler --> Reply[ctx.Reply]
    Reply --> Send[session.Send]
    Send --> EncodePacket[PacketProtocol.EncodePacket]
    EncodePacket --> WriteFrame[writeFrame]
    WriteFrame --> Client
```

WebSocket currently treats one WebSocket message as one protocol packet.

## HTTP / HTTPS

HTTP and HTTPS keep protocol-native method + path routing.

This is appropriate for REST-style APIs:

```mermaid
flowchart TD
    Client[HTTP Client] --> Listen[httpx.Endpoint.ListenAndServe]
    Listen --> NetHTTP[net/http Server]
    NetHTTP --> Serve[Endpoint.serve]
    Serve --> Path{path exists}
    Path -->|no| NotFound[404 not found]
    Path -->|yes| Method{method exists}
    Method -->|no| MethodNotAllowed[405 method not allowed]
    Method -->|yes| Invocation[core.Invocation]
    Invocation --> Middleware[core.Middleware]
    Middleware --> Handler[HTTP Handler]
    Handler --> Response[ctx.Text / ctx.JSON / ctx.Bytes / ctx.Stream]
    Handler -->|error| ServerError[500 err.Error]
    Response --> Client
```


## SSE

```mermaid
flowchart TD
    Client[SSE Client] --> Listen[sse.Endpoint.ListenAndServe]
    Listen --> Serve[sse.Endpoint.serve]
    Serve --> Invocation[core.Invocation]
    Invocation --> Middleware[core.Middleware]
    Middleware --> Context[sse.Context]
    Context --> Handler[SSE Handler]
    Handler --> Send[ctx.Send event data]
    Send --> Stream[text/event-stream]
    Stream --> Client
```

SSE is a one-way server push transport. It is intentionally not modeled as request/reply. The stream setup can still enter unified `core.Middleware`, using operation keys such as `GET /events`.

## gRPC

gRPC keeps native service and method routing. KPro does not convert gRPC requests into `core.Message`; instead unary and stream RPCs enter the shared `core.Middleware` layer before the service handler runs.

```mermaid
flowchart TD
    Client[gRPC Client] --> Listen[grpcx.Server.ListenAndServe]
    Listen --> GRPCServer[google.golang.org/grpc.Server]
    GRPCServer --> UnaryInterceptor[unary middleware interceptor]
    GRPCServer --> StreamInterceptor[stream middleware interceptor]
    UnaryInterceptor --> UnaryContext[core.Invocation with request payload]
    StreamInterceptor --> StreamContext[core.Invocation with stream metadata]
    UnaryContext --> ServiceHandler[gRPC unary handler]
    StreamContext --> StreamHandler[gRPC stream handler]
    ServiceHandler --> Response[gRPC response / status]
    StreamHandler --> StreamResponse[gRPC stream messages / status]
    Response --> Client
    StreamResponse --> Client
```

The operation key is the full gRPC method, for example `/helloworld.v1.Greeter/SayHello`. Unary payload is the decoded request object. Stream payload is nil at stream setup; stream direction is exposed in metadata.

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
    Cancel --> GRPCShutdown[grpcx.Shutdown]
    TCPShutdown --> Done[wait goroutines]
    UDPShutdown --> Done
    WSShutdown --> Done
    HTTPShutdown --> Done
    SSEShutdown --> Done
    GRPCShutdown --> Done
```
