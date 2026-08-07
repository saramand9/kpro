# Transport Fault Injection And Soak Testing

This guide defines the repository-owned resilience tests for KPro's canonical
TCP message transport. It separates fast deterministic fault injection from
explicit real-socket stress and soak runs. Passing these tests is framework
evidence; it is not an application capacity result, an SLA, or proof that a
particular kernel, proxy, TLS configuration, and deployment will behave the
same way.

## Test Layers

### Deterministic fault matrix

`transport/tcp/fault_injection_test.go` and the default fanout case in
`transport/tcp/stress_test.go` run as part of the ordinary repository suite.
They cover:

| Scenario | Required invariant |
| --- | --- |
| Gathered short write without an error | Return `io.ErrShortWrite`, complete every affected receipt with that error, and release all queue accounting. |
| Partial progress followed by a concrete write error | Preserve the concrete error for every affected receipt and release every frame/buffer. |
| Slow peer | A caller-bounded drain returns its context error without falsely completing the receipt; reading later can still allow the already accepted write to finish. |
| Write deadline before drain deadline | The receipt and drain expose the transport timeout and retain no pending capacity. |
| Drain deadline before write deadline | Drain returns the caller context error; later socket failure still completes the receipt and releases capacity. |
| TCP half-close | A response whose receipt completed before the read-side EOF remains readable by the peer. |
| TCP reset (`SO_LINGER=0`) | The server session closes, its connection slot is released, later sends return `core.ErrClosed`, and outbound accounting reaches zero. |
| 128 idle real connections | Shutdown closes the listener and every session within a bound, leaving the active-session count at zero. |

Run only this fast matrix with:

```sh
go test -count=1 -timeout=2m -v ./transport/tcp -run '^TestTCPFault'
go test -race -count=1 -timeout=3m ./transport/tcp -run '^TestTCPFault'
```

The injected writer deliberately exercises both invalid short writes
(`n < len(p), err == nil`) and valid partial progress with a non-nil error. Go's
writer contract does not permit KPro to treat the former as success.

### Thousand-connection shutdown

The real-socket thousand-connection case is opt-in so ordinary unit and race
runs do not consume thousands of descriptors and goroutines:

```powershell
$env:KPRO_TCP_STRESS_CONNECTIONS='1000'
go test -count=1 -timeout=3m -v ./transport/tcp -run '^TestTCPStressShutdownThousandsOfConnections$'
```

```sh
KPRO_TCP_STRESS_CONNECTIONS=1000 \
  go test -count=1 -timeout=3m -v ./transport/tcp \
  -run '^TestTCPStressShutdownThousandsOfConnections$'
```

The client uses bounded connection retries because a burst can temporarily
fill an operating system's listen queue. The test does not pass until all
configured connections are active at the same time, shutdown succeeds, all
client sockets observe closure, and the server reports zero active sessions.

### Configurable real-socket soak

The soak case keeps multiple real TCP request/reply streams active, verifies
every decoded response, performs bounded shutdown, forces garbage collection,
and records:

- completed round trips and observed message throughput;
- heap allocation and heap-object counts before and after the exercise; and
- goroutine counts before startup and after shutdown.

It also applies deliberately generous post-GC heap/object guards and requires
the goroutine count to converge. Those guards catch large retained-state leaks;
they are not a replacement for examining a heap/goroutine profile during a
long deployment-specific run.

Example 30-minute run:

```powershell
$env:KPRO_TCP_SOAK_DURATION='30m'
$env:KPRO_TCP_SOAK_CLIENTS='64'
$env:KPRO_TCP_SOAK_PAYLOAD_BYTES='1024'
go test -count=1 -timeout=35m -v ./transport/tcp -run '^TestTCPSoakEchoStability$'
```

```sh
KPRO_TCP_SOAK_DURATION=30m \
KPRO_TCP_SOAK_CLIENTS=64 \
KPRO_TCP_SOAK_PAYLOAD_BYTES=1024 \
  go test -count=1 -timeout=35m -v ./transport/tcp \
  -run '^TestTCPSoakEchoStability$'
```

`KPRO_TCP_SOAK_DURATION` is the enable switch. Client and payload settings
default to 32 and 256 bytes when omitted.

## Manual CI Evidence

The `Transport resilience` workflow in
`.github/workflows/transport-resilience.yml` is intentionally manual. One run
executes:

1. the deterministic TCP matrix under the race detector;
2. shutdown of 1,000 simultaneous real TCP connections; and
3. a two-minute, 64-client real-socket echo soak.

It uploads all three verbose logs as the `transport-resilience` artifact. Use a
longer local or environment-owned run for release workloads whose connection
lifetimes, payloads, TLS, proxies, or handler latency differ materially from
that fixed repository exercise.

## Current Local Smoke Observation

On 2026-08-07, the new tests were exercised on Windows with Go 1.26.5:

| Exercise | Observation |
| --- | --- |
| Deterministic matrix under `-race` | Passed. |
| Real-socket shutdown | 1,000 simultaneous connections; four runs observed the shutdown phase at 26-27 ms. |
| Development soak | 60 seconds, 64 clients, 256-byte payload, 6,823,082 completed round trips, about 113,709 messages/second. |
| Soak retained state | Post-GC heap changed from 2,019,120 to 1,442,320 bytes; heap objects from 3,337 to 3,065; goroutines returned from 2 to 2. |

These numbers are one smoke observation from an otherwise shared development
machine. They establish that the harness ran and its invariants passed; they
must not be used as a stable performance baseline or production capacity
claim.

## Semantics And Limits

- A successful write receipt means the complete transport write call returned
  successfully. It does not prove peer receipt or application processing.
- A drain context timeout stops waiting; it does not cancel or roll back an
  already executing socket write. Terminal shutdown closes that socket after
  the bounded drain phase.
- The half-close case waits for the response receipt inside the accepted
  handler. It does not introduce a new guarantee that arbitrary background
  work may send after a session has observed EOF.
- An RST may race with bytes already accepted by the local kernel. The test
  therefore asserts bounded cleanup and closed-state semantics, not a specific
  application-delivery result.
- KCP, QUIC, UDP, and WebSocket already share the outbound accounting and
  receipt/drain contract tests. Their protocol-specific loss, handshake, and
  interoperability matrices remain separate from this TCP socket-fault suite.
