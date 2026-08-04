# KPro and Netty Performance Baseline

This document records a source-based performance comparison between KPro and
Netty. It is an engineering hypothesis and optimization guide, not a benchmark
result, capacity guarantee, or claim of Netty parity.

## Baseline and evidence boundary

| Field | Baseline |
| --- | --- |
| Assessment date | 2026-08-04 |
| KPro source | The repository revision containing this document and the segmented TCP/WebSocket FastPath work |
| Netty source | Local Netty 4.2 checkout at `b022b4786f2b8258ea75d5b613957a01f2246c5b` |
| KPro measured evidence | [FastPath regression baseline](../benchmark/RESULTS.md#2026-08-03-fastpath-regression-baseline), plus the 2026-08-04 loopback matrix below |
| Comparison acceptance contract | [Performance Fast Paths](performance-fast-path.md#kpro-versus-netty-baseline-contract) |

The implementation comparison comes from the two source trees. The public
concurrency references below come from Netty's documentation site. The
loopback matrix below is a controlled engineering measurement, not a capacity
guarantee or a production certification.

## What Netty's concurrency reputation means

Netty does not publish a universal maximum connection count. Its official
[user guide](https://netty.io/wiki/user-guide-for-4.x.html) describes a
high-performance, high-scalability framework, not a `maxConnections` service
level guarantee.

Netty's official [related-articles
index](https://netty.io/wiki/related-articles.html) links historical third-party
reports of more than 100,000 concurrent connections and 512,000 concurrent
WebSockets. Its [adopters page](https://netty.io/wiki/adopters.html) records a
Livefyre deployment serving more than one million concurrent users. These are
useful evidence that the architecture has operated at large scale, but they do
not establish one repeatable single-machine limit: the machine count, traffic
rate, protocol behavior, business state, and success criteria differ.

The commonly repeated phrase "Netty supports a million concurrent
connections" should therefore be read as an architecture and deployment
capability, usually for carefully tuned, mostly idle or low-frequency long
connections. It must not be translated into one million simultaneously active
requests.

| Term | What must be measured |
| --- | --- |
| Established connections | Open sockets and protocol/session state, including idle peers |
| Active connections | Connections exchanging data during the measurement interval |
| Requests in flight | Application operations accepted but not completed |
| Throughput | Successfully peer-validated messages or bytes per second |
| Sustainable capacity | The highest offered load that also satisfies declared correctness, latency, error, and memory-growth limits |

For example, one million clients each sending one 1 KiB request and receiving
one 1 KiB response every second already implies roughly two million messages
and about 2 GiB/s of application payload traffic before protocol overhead.
Connection count alone says almost nothing about whether a server can sustain
that workload.

## Current implementation comparison

| Dimension | Current KPro implementation | Netty 4.2 implementation | Theoretical position |
| --- | --- | --- | --- |
| Outbound framing | Inline fixed header, pooled payload, optional transport-owned buffer, and gathered regions | Direct/reference-counted buffers, outbound buffer, gathered writes, and mature allocator integration | KPro has closed much of the narrow framing/admission gap |
| TCP receive | Fixed header `ReadFull`, payload allocation, then payload `ReadFull` for each non-empty frame | Adaptive socket reads, cumulative decoding, and multiple frames from one read buffer | Netty has a large small-message advantage |
| Execution topology | Typical synchronous message path crosses reader, per-session Router executor, outbound queue, and writer goroutines | Decode, handler, and write can remain on one EventLoop when no executor boundary is crossed | Netty has fewer hops for non-blocking I/O work |
| Write formation | Fixed, bounded, opportunistic FIFO drain using `net.Buffers` | Separate write/flush, flush consolidation, adaptive gather limits, write-spin, and partial-progress state | Netty is more adaptive under mixed and partial writes |
| Buffer allocation | Simple server-wide bounded Go slice pool with a hard idle-retention cap | Adaptive/pooled direct-buffer allocators, caches, arenas, and reference-counted ownership | Netty favors throughput/locality; KPro favors simplicity and explicit bounds |
| Backpressure | Hard frame/byte admission limits, watermarks, bounded waiters, and close-on-hard-full behavior | High/low writability watermarks, queued-byte accounting, and read-interest control | KPro is strongly bounded; Netty provides finer gradual flow control |
| Large immutable payloads | No file-region or kernel copy-avoidance API | `FileRegion`/transfer support and native transports | Netty has a large advantage for eligible file transfer |
| Platform backends | Go runtime network poller and standard `net` APIs | NIO plus Linux epoll and io_uring implementations | Netty exposes more specialized Linux tuning |
| Blocking handler tolerance | Goroutine isolation makes ordinary blocking application code easier to contain | Blocking an EventLoop delays every channel assigned to it unless work is offloaded | KPro is easier to use safely for conventional blocking business logic |

The relevant KPro paths are
[`codec/kpro.go`](../codec/kpro.go),
[`core/router.go`](../core/router.go), and
[`transport/tcp/server.go`](../transport/tcp/server.go). The pinned Netty
counterparts include
[`AbstractNioByteChannel`](https://github.com/netty/netty/blob/b022b4786f2b8258ea75d5b613957a01f2246c5b/transport/src/main/java/io/netty/channel/nio/AbstractNioByteChannel.java),
[`ByteToMessageDecoder`](https://github.com/netty/netty/blob/b022b4786f2b8258ea75d5b613957a01f2246c5b/codec-base/src/main/java/io/netty/handler/codec/ByteToMessageDecoder.java),
[`NioSocketChannel`](https://github.com/netty/netty/blob/b022b4786f2b8258ea75d5b613957a01f2246c5b/transport/src/main/java/io/netty/channel/socket/nio/NioSocketChannel.java), and
[`ChannelOutboundBuffer`](https://github.com/netty/netty/blob/b022b4786f2b8258ea75d5b613957a01f2246c5b/transport/src/main/java/io/netty/channel/ChannelOutboundBuffer.java).

## 2026-08-04 receive-path change and loopback matrix

This iteration made two targeted changes without adding a user-visible Fast
Path mode or changing the normal Router/Context/Middleware architecture:

- TCP read deadlines are set when a session starts and refreshed only after a
  time window, instead of issuing `SetReadDeadline` for every frame. Idle
  connections still expire normally, while handler execution is not counted
  as read-idle time.
- `codec.KProV1Codec` implements `ReusableStreamCodec`. TCP sessions retain a
  bounded scratch payload backing and reuse it for synchronous handler reads,
  removing the per-frame payload allocation. This is user-space buffer reuse,
  not kernel `MSG_ZEROCOPY`.

The same raw Go client, KProV1 frame format, Windows loopback host, and test
durations were used for both servers. Netty used the temporary NIO echo server
at commit `b022b4786f2b8258ea75d5b613957a01f2246c5b`. Values are messages per
second; each cell is one run and should be repeated for confidence intervals.

| Case | Previous KPro | Current KPro | KPro change | Previous Netty | Current Netty |
| --- | ---: | ---: | ---: | ---: | ---: |
| T01: 1 conn, 64 B | 11,480 | 15,735 | +37% | 16,577 | 20,317 |
| T02: 64 conn, 64 B | 75,526 | 108,009 | +43% | 100,332 | 132,946 |
| T03: 64 conn, 1 KiB | 56,039 | 103,474 | +85% | 107,193 | 131,016 |
| T04: 64 conn, 16 KiB | 36,380 | 53,337 | +47% | 47,078 | 79,893 |
| T05: 1,000 conn, 64 B | 80,555 | 87,151 | +8% | 96,805 | 102,310 |
| T06: 1,000 conn, 1 KiB | 78,206 | 84,398 | +8% | 93,162 | 100,181 |
| T07: 64 conn, 64 B, batch 16 | 300,160 | 796,795 | +165% | 508,581 | 1,582,528 |
| T08: 256 conn, 1 KiB, batch 16 | 483,029 | 700,677 | +45% | 1,328,293 | 1,160,523 |
| T08b: 1,000 conn, 1 KiB, batch 16 | 588,930 | 708,752 | +20% | 1,055,744 | 1,121,125 |
| T09: 1,000 conn, 1% slow | 68,704 | 85,283 | +24% | 16,155 | 98,834 |

All current cells completed with zero client-reported errors. The previous
T09 Netty cell had errors and is retained only as historical context. The
current KPro/Netty ratio ranges from about 1.16x to 1.99x, with the largest
remaining gap in batched writes. The p50 values from the temporary client can
quantize to zero for sub-microsecond samples; p99 and throughput are the more
useful signals in this matrix.

The result supports a receive-path improvement, not a claim of Netty parity.
It is Windows NIO loopback data, not Linux epoll/io_uring or a multi-host
network result.

## Largest current gap: inbound processing

The normal KProV1 TCP receive path currently performs:

```text
ReadFull(20-byte header)
-> validate payload length
-> reuse session scratch payload
-> ReadFull(payload)
-> Router dispatch
```

The per-frame payload allocation is now removed for the built-in KProV1 TCP
path. The codec still has no persistent cumulative receive buffer from which
one socket read can decode several complete frames, so that remains the main
receive-side gap versus Netty.

Netty reads available bytes into an adaptively sized buffer and its cumulative
decoder can preserve partial frames and emit several messages from the same
input buffer. That difference is expected to dominate small-packet, high-QPS
echo or gateway workloads. Optimizing another few nanoseconds from outbound
header construction is unlikely to compensate for it.

## Scheduling and batching trade-offs

A typical synchronous KPro message follows this shape:

```text
TCP reader goroutine
-> Router admission and per-session executor
-> handler
-> outbound queue
-> TCP writer goroutine
```

This design preserves per-session ordering and isolates blocking work, but its
channels, locks, queue accounting, and goroutine wakeups add CPU cost. Netty's
same-EventLoop fast path avoids most cross-thread scheduling for a non-blocking
handler. A fair comparison must either keep both handlers non-blocking or
offload blocking Netty work; deliberately blocking Netty's EventLoop is not an
equivalent configuration.

KPro's writer takes the first queued frame and immediately drains only the
frames already available, subject to fixed frame and byte limits. That is
bounded and low complexity, but it is opportunistic. Netty can intentionally
separate `write` from `flush`, consolidate flushes, retain partial write
progress, and adjust the write quantum. Those mechanisms should improve
syscall amortization and tail behavior when traffic is bursty or sockets accept
only part of a submission.

## What the current FastPath evidence proves

The recorded Windows in-memory regression run observed:

| Case | Median | Memory |
| --- | ---: | ---: |
| Contiguous TCP send admission, 1 KiB | 343.2 ns/op | 1152 B/op, 1 allocation |
| Segmented TCP send admission, 1 KiB | 210.4 ns/op | 0 B/op, 0 allocations |
| Owned-buffer commit, 1 KiB | 236.3 ns/op | 64 B/op, 1 lease allocation |
| TCP batch loop, 16 x 1 KiB | 1692 ns/op | 0 B/op, 0 allocations |
| WebSocket batch loop, 16 x 1 KiB | 1724 ns/op | 0 B/op, 0 allocations |

The segmented admission result was about 39% lower than KPro's previous
contiguous in-memory baseline and removed its warmed per-operation allocation.
The benchmark does not contain a real socket, kernel scheduling, peer, Router
round trip, or Netty process. It proves a KPro regression improvement only; it
does not prove a 39% service-throughput improvement or a lead over Netty.

## Expected result by workload

| Workload | Source-based expectation before measurement |
| --- | --- |
| Up to roughly 1,000 users, with database/RPC-dominated handlers | Framework I/O difference is unlikely to be the primary bottleneck; KPro is technically suitable when limits are configured and validated |
| 64 B to 1 KiB pure echo at high QPS | Netty is likely to lead clearly because of receive cumulation and fewer scheduling hops |
| Several frames available in each socket read | Netty is likely to lead clearly until KPro adds multi-frame receive decoding |
| Bursty writers, partial writes, and slow consumers | Netty is likely to lead in efficiency and tail stability; KPro still provides explicit hard resource bounds |
| Blocking database or filesystem handlers | KPro is easier to use safely; a correctly configured Netty application must offload the work |
| Large file or immutable-region transfer | Netty has a large structural advantage until KPro adds a file-region/sendfile path |
| Idle process footprint and startup | KPro is structurally simpler and likely lighter, but this requires an equal-semantics RSS/startup measurement before making a numerical claim |

For the stated basic-application target of no more than about 1,000 users, this
comparison does not identify a framework-level capacity blocker. It also does
not turn user count into a capacity result: connection multiplicity, requests
per second, payload distribution, TLS, slow clients, and downstream latency
still belong in the application load model.

## Optimization order

The next work should target measured architectural gaps in this order:

1. Add a bounded cumulative TCP receive buffer that preserves partial frames
   and decodes multiple frames per socket read.
2. Measure and then add bounded flush consolidation, adaptive write quanta, and
   explicit partial-write progress where the measurements justify them.
3. Refine read-side backpressure so sustained outbound pressure can pause and
   resume reads before the hard close boundary.
4. Shard buffer reuse only after contention profiles show the server-wide pool
   is material.
5. Add a separate file-region/sendfile capability for eligible large immutable
   payloads; consider Linux-specific copy avoidance only after that path has a
   reproducible CPU-per-GiB requirement.

Every step must retain current correctness properties: bounded memory,
per-session ordering, caller payload ownership, deterministic close behavior,
and complete cleanup on errors. The real-machine matrix and acceptance gates
in [Performance Fast Paths](performance-fast-path.md#kpro-versus-netty-baseline-contract)
remain the authority for any future performance ratio or parity claim.
