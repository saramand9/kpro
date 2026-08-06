# HAProxy PROXY Protocol

KPro supports [HAProxy PROXY protocol v1 and v2](https://github.com/haproxy/haproxy/blob/master/doc/proxy-protocol.txt)
on TCP-based listeners. It lets
an L4 proxy or load balancer pass the original client and destination address
to the application without terminating or modifying the application protocol.

This feature is available on:

- TCP message transport;
- HTTP/1.1, HTTPS, and HTTP/2 through `transport/httpx`;
- fast HTTP/1.1 through `transport/fasthttpx`;
- WebSocket and SSE;
- gRPC, including gRPC with TLS.

It does not apply to KCP, QUIC, UDP, HTTP/3, or the KPro QUIC message
transport. Those protocols do not begin with a TCP byte stream on which this
header can be inserted.

## Security model

The zero value is disabled. When enabled:

1. every accepted connection must begin with a valid v1 or v2 header;
2. the direct socket peer must match an exact IP or CIDR in
   `TrustedProxies`;
3. the header must complete within `HeaderTimeout`, which defaults to five
   seconds; and
4. parsing happens before TLS or application-protocol negotiation.

KPro deliberately does not auto-detect optional headers. Auto-detection on a
public listener would let a direct client claim an arbitrary source address.
An untrusted peer, a missing header, a malformed header, or a header timeout
closes only that connection. `OnReject` can record a bounded error category
and direct peer address; it runs synchronously and must return quickly.

Use the narrowest LB source ranges available and enforce the same boundary
with security groups, firewall rules, or network policy. Do not put public
client ranges in `TrustedProxies`. A trusted source is the machine that opens
the backend TCP connection to KPro.

PROXY protocol is different from HTTP `Forwarded` or `X-Forwarded-For`.
Those are L7 request headers. Do not enable PROXY protocol for an HTTP ingress
that sends only forwarded HTTP headers; the connection will be rejected as
missing the required binary/text connection header.

## Configuration

Create one configuration value and pass it to each listener that is actually
behind an enabled L4 proxy:

```go
package main

import (
	"log"

	"github.com/saramand9/kpro/transport/httpx"
	"github.com/saramand9/kpro/transport/proxyproto"
	"github.com/saramand9/kpro/transport/tcp"
)

func proxyConfig() proxyproto.Config {
	return proxyproto.Config{
		Enabled: true,
		TrustedProxies: []string{
			"10.20.0.0/16",
			"2001:db8:1234::/48",
		},
		OnReject: func(event proxyproto.RejectEvent) {
			log.Printf("rejected PROXY connection from %s: %v", event.DirectRemoteAddr, event.Err)
		},
	}
}

func endpoints() {
	_ = tcp.New(tcp.Config{
		Addr:          ":8088",
		ProxyProtocol: proxyConfig(),
	})

	_ = httpx.New(httpx.Config{
		Addr:          ":8443",
		TLS:           true,
		CertFile:      "server.crt",
		KeyFile:       "server.key",
		ProxyProtocol: proxyConfig(),
	})
}
```

For gRPC, use the server option:

```go
grpcServer := grpcx.NewServer(
	":9090",
	grpcx.WithProxyProtocol(proxyConfig()),
)
```

`websocket.Config`, `fasthttpx.Config`, and `sse.Config` expose the same
`ProxyProtocol` field. Custom TCP runners can wrap a listener directly with
`proxyproto.WrapListener` before applying TLS.

## Address visibility

After a valid `PROXY` command:

- `core.Context.RemoteAddr()`, `core.Invocation.RemoteAddr()`,
  `http.Request.RemoteAddr`, fasthttp's remote address, and gRPC peer metadata
  expose the advertised original client address;
- the original socket peer remains available for auditing; and
- v1 `UNKNOWN` and v2 `LOCAL` retain the direct socket addresses.

Message handlers can retrieve both address views from their session:

```go
func handle(ctx core.Context) error {
	info, ok := proxyproto.InfoFrom(ctx.Session())
	if ok {
		log.Printf("client=%s proxy=%s", ctx.RemoteAddr(), info.DirectRemoteAddr)
	}
	return nil
}
```

HTTP, fasthttp, SSE, WebSocket handshake, and gRPC connection contexts carry
the same immutable information:

```go
info, ok := proxyproto.FromContext(invocation.Context())
```

`Info.Header` contains the version, command, address family, advertised source
and destination, and skipped TLV byte count. KPro validates and skips v2 TLVs
but does not retain or interpret their values.

## Deployment checks

Before enabling the feature in production:

- verify the selected LB actually sends v1 or v2 on the backend target;
- verify its source addresses match `TrustedProxies` in every zone;
- test plain TCP and TLS listeners separately, because the header precedes the
  TLS ClientHello;
- confirm health checks also send the header, or use a separate health-check
  listener;
- test rolling deployment behavior when old and new targets use different
  settings; and
- alert on `ErrUntrustedProxy`, `ErrHeaderRequired`, `ErrHeaderTimeout`,
  `ErrMalformedHeader`, and `ErrUnsupportedHeader` without using peer addresses
  as metric labels.

Enabling this on a listener is a deployment contract change, not a transparent
parser toggle. Direct clients and LBs that do not send the header will stop
working on that listener.
