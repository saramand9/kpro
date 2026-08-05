# Performance fast paths

This document describes the performance mechanisms currently implemented in
the TCP receive/write paths and WebSocket write path. It is an implementation guide and a
measurement contract, not a throughput, latency, or Netty-parity claim.

The important distinction is ownership. KPro can avoid constructing some
intermediate contiguous frames, reuse transport-owned payload storage, and
submit several byte regions together. Normal socket writes still copy dynamic
application bytes from user space into the kernel unless a future transport
uses and verifies a kernel copy-avoidance facility.

## Implemented TCP path

### Segmented KProV1 frames

The TCP transport recognizes only the exact built-in `codec.KProV1Codec`
dynamic type for its default fixed-header fast path. Its outbound
representation is:

```text
20-byte KProV1 header | unchanged payload bytes
```

The TCP transport stores the fixed header inline in its outbound frame and
keeps the payload as a separate slice. `codec.WriteKProV1Header` fills that
header without growing a slice. The writer later supplies the header and
payload as separate regions instead of allocating one contiguous wire frame.

`Session.Send` retains its safe caller-ownership contract. It copies
`Message.Data` into transport-owned storage before returning, so the caller may
reuse or mutate its original slice. Consequently, the normal segmented path:

- replaces the unconditional contiguous frame allocation with an inline
  header and bounded reusable payload storage;
- can be allocation-free for eligible pool hits, including the warmed 1 KiB
  regression benchmark;
- still performs exactly one application-payload ownership copy; and
- still performs the ordinary user-to-kernel socket copy.

A custom stream codec can explicitly opt into the same layout by implementing
`codec.StreamFrameCodec` with a positive header size no greater than the TCP
transport's 20-byte inline header. Other `codec.StreamCodec` implementations
remain compatible and use their existing contiguous `Encode` result.

Exact dynamic-type recognition is intentional. A wrapper that anonymously
embeds `KProV1Codec` and overrides `Encode` must continue through its override;
it must not acquire segmented wire behavior accidentally through Go method
promotion.

### Bounded payload reuse

Each TCP server owns sharded concurrent, power-of-two size-class payload
pools. The shards share one deliberately bounded retention budget:

- `MaxRetainedBufferBytes` limits total idle memory retained across every shard;
- the default retained-byte limit is 4 MiB;
- the current pool retains buffers up to 64 KiB and discards larger buffers;
- `MaxPendingBytes` and `SendQueueSize` continue to bound reserved, queued, and
  writer-owned logical wire bytes and frame count per session; and
- queue completion and queue close both release transport-owned payloads
  exactly once.

The retained-byte limit and the per-session pending-byte/Router inflight limits
cover different states. The first is a hard limit on reusable idle inbound and
outbound buffer capacity. The others limit admitted logical wire bytes and
active work, including a reservation
made before encoding. Actual active allocation capacity is rounded up to a
power-of-two class from 16 bytes through 64 KiB; it can therefore exceed the
logical payload count (by less than 2x at 16 bytes and above, and up to 16x for
the smallest non-empty payload). `SendQueueSize` still bounds the number of
such active buffers. Neither setting is a whole-process memory limit;
deployment sizing must include class capacity, connection count, writer/queue
state, codec allocations, and application-owned memory.

Application-visible reserved buffers are cleared before reuse. The internal
`Session.Send` copy path may request uncleared scratch storage because it
overwrites every visible byte before the buffer can be sent.

### Owned reusable receive buffers

TCP uses `codec.ReusableStreamCodec` when the configured codec implements it.
The default KProV1 codec reads its header and payload sequentially into the
same scratch allocation. The transport transfers that allocation to an owned
message Context, so it is returned to the session's pool only after both the
TCP handler call and any accepted Router task release their Context references.
The next socket read therefore cannot overwrite a payload still queued in the
Router.

The adaptive per-connection reader starts at 4 KiB and grows, only after a
completely full lower-level read, to `MaxReadBufferSize` (32 KiB by default).
This amortizes receive syscalls under coalesced traffic without allocating the
maximum buffer for idle connections.

Non-reusable custom codecs keep the original `StreamCodec.Read` behavior.
Reusable custom codecs keep compatibility but must honor the documented
scratch ownership contract. Live receive buffers are bounded by transport and
Router admission, while `MaxRetainedBufferBytes` bounds only idle buffers after
their final Context reference is released.

### Bounded buffered decode and Router batch admission

`codec.BufferedStreamCodec` is an optional receive contract for decoding one
complete frame already present in a contiguous reader buffer. KProV1 implements
it without allocating or copying inside the codec; the decoded payload aliases
the supplied bytes. TCP must therefore copy each additional buffered payload
into transport-owned pool storage before the adaptive reader can discard or
reuse those bytes.

When a TCP server is mounted through `server.Endpoint`, the endpoint detects
the optional `transport.BatchMessageServer` capability. TCP then:

1. reads the first frame through the ordinary owned receive path;
2. probes for up to 15 more complete frames already buffered by the same
   adaptive reader, for a hard maximum group of 16;
3. splits business-message groups at heartbeat frames; and
4. offers each group to the Router as one optional admission operation.

`Router.TryDispatchSyncBatch` accepts only 2 through 32 contexts from the same
session, only when every route is synchronous, the Router uses
`BackpressureReject`, and global, per-session, and queue capacity are all
available. It reserves the whole count and enqueues the group while holding the
session queue lock. Returning `false` guarantees that the Router retained no
Context and accepted no task; TCP then invokes the ordinary handler once per
Context. Wait-mode backpressure, async routes, mixed sessions, missing routes,
closed sessions, and insufficient capacity therefore preserve the existing
per-message path.

Direct `tcp.Server.ListenAndServe` does not install a batch handler and keeps
the original `readFrame` behavior. Custom transports and codecs remain source
compatible because both capabilities are optional. Batch-handler panics are
contained by the transport invocation helper and follow the configured handler
error policy.

This is bounded cumulative probing and admission amortization. It does not make
the receive path zero-copy, remove per-message Router execution, or promise
that one socket read always produces a full group.

### Bounded gather-write batching

The single TCP writer for a session receives the first queued frame, then
non-blockingly drains a FIFO prefix. Draining and submission are bounded by:

| Setting | Default | Meaning |
| --- | ---: | --- |
| `MaxWriteBatchFrames` | 16 | Maximum frames taken by one queue drain; also clipped to `SendQueueSize`. |
| `MaxWriteBatchBytes` | 256 KiB, capped by `MaxPendingBytes` | Maximum bytes in one logical gather submission. |

An already admitted frame larger than the byte setting is still written by
itself so the writer always makes progress. A frame-count drain may be split
into multiple byte-bounded submissions without reordering it.

Each submission is expressed as `net.Buffers.WriteTo`: header and payload
regions from several frames can be submitted without concatenating them.
Depending on the concrete connection and platform, Go may use scatter/gather
I/O or fall back to ordinary writes. The API therefore does not guarantee that
one logical submission is exactly one system call.

`WriteTimeout` applies to each gather submission. All drained queue items are
completed on success or write failure, returning their accounting and owned
buffers. Reusable writer arrays are cleared after a batch so an idle session
does not retain stale payload references outside the queue and pool budgets.

## Optional owned outbound buffer

TCP sessions using a compatible segmented codec implement the optional
`core.OutboundBufferAllocator` capability. Applications that already encode a
response can write directly into transport-owned payload storage:

```go
buffer, supported, err := core.SessionReserveOutboundBuffer(session, payloadSize)
if err != nil {
	return err
}
if !supported {
	payload := make([]byte, payloadSize)
	encodePayload(payload)
	return session.Send(core.Message{ID: responseID, Seq: seq, Data: payload})
}
defer buffer.Release()

encodePayload(buffer.Bytes())
return buffer.Commit(core.Message{ID: responseID, Seq: seq})
```

Reservation first claims queue frame and byte capacity. `Bytes` then exposes a
zeroed slice of the requested size. `Commit` uses those bytes as the payload,
ignores `Message.Data`, and transfers ownership to the session. `Release`
abandons an uncommitted reservation and is safe to defer.

After either `Commit` or `Release`, the caller must not retain, read, or mutate
the returned slice. Repeated use is rejected. An unsupported transport or codec
returns `supported == false`, preserving the small base `core.Session`
contract and third-party transport compatibility.

This capability avoids the application-to-transport payload ownership copy
when the application produces the payload directly in `Bytes`. It does not
avoid a transport backing allocation on a pool miss or the later socket copy
into the kernel.

## Optional current-payload ownership transfer

An echo or forwarding handler that sends the exact current inbound payload can
explicitly transfer its transport-managed ownership to the TCP writer:

```go
func echo(ctx core.Context) error {
	message := ctx.Message()
	return core.ReplyCurrentPayload(ctx, message.ID)
}
```

After calling `ReplyCurrentPayload`, the handler must return without reading,
retaining, or mutating `Message.Data`. When the Context owns the exact current
transport buffer and the Session implements `core.OwnedPayloadSender`, a
compatible segmented TCP writer retains one Context reference until the frame
is completed, rejected, or discarded. Every path releases that reference
exactly once.

Wrapped or synthetic contexts, a transformed or sliced payload, unsupported
Sessions, and non-segmented codecs fall back to ordinary `Context.Reply` copy
semantics. Normal `Context.Reply` and `Session.Send` are unchanged, so this
optimization is explicit rather than a hidden ownership-contract change.

This transfer can avoid the inbound-to-outbound application ownership copy for
that narrow workload. It does not avoid socket receive/send copies, and it is
not appropriate when the response payload must be transformed or used after
the reply call.

## Implemented WebSocket path

The WebSocket writer now performs the same bounded FIFO queue drain, with
`MaxWriteBatchFrames` and `MaxWriteBatchBytes` defaults of 16 and 256 KiB.
Server frame headers are generated into reusable fixed-size storage and are
gathered with their payloads through `net.Buffers.WriteTo`. Multiple queued
WebSocket frames can therefore share a logical gather submission without
constructing a second contiguous WebSocket header-plus-payload buffer.

Close ordering remains explicit: a batch stops at the first close frame, data
drained after it is discarded, and direct control-frame writes are serialized
with the batch writer. A direct close request is published before it waits for
the writer lock, so a byte-split batch yields after at most its current gather
submission instead of relying on mutex scheduling fairness.

The WebSocket message path still uses `Codec.EncodePacket`, which currently
builds a contiguous encoded application packet. WebSocket batching avoids
constructing another contiguous WebSocket frame and groups regions into
bounded logical gather submissions. Whether that reduces syscalls depends on
the concrete connection and platform and is not established by the current
microbenchmarks. This path does not provide the TCP owned-buffer capability or
remove the application-packet encoding allocation.

## Copy terminology

Use the narrowest accurate term when describing a result:

| Mechanism | What it can avoid | What it does not avoid |
| --- | --- | --- |
| Bounded buffer pool | Repeated backing allocations for eligible sizes when an idle class buffer is available | Pool misses, oversized allocations, payload copying, or socket copying |
| Owned reusable receive buffer | Repeated receive-payload backing allocation after warm-up for reusable codecs | Socket copying, decode work, or Router scheduling |
| Buffered decode plus batch admission | Additional socket reads for complete frames already buffered, plus repeated Router capacity and queue synchronization for an eligible group | The ownership copy out of the shared reader buffer, per-message handler execution, or guaranteed grouping |
| Segmented header and payload | A contiguous header-plus-payload frame allocation and representation | Normal `Session.Send` ownership copy; user-to-kernel copy |
| `core.OutboundBuffer` | A separate application-owned payload allocation and ownership copy when filled directly | A transport allocation on pool miss; user-to-kernel copy |
| `core.ReplyCurrentPayload` | The inbound-to-outbound ownership copy for an explicit exact-payload reply | Socket copies, payload transformation, unsupported contexts/codecs, or use of the payload after replying |
| `net.Buffers.WriteTo` | Intermediate concatenation; it can group regions into one logical submission | A guaranteed syscall reduction; kernel copy of dynamic user memory |
| `sendfile`, `splice`, `MSG_ZEROCOPY`, or `SEND_ZC` | Potential kernel data-copy work on eligible paths | Protocol processing, completion handling, pinning, and fallback costs |

The current implementation does **not** use `sendfile`, `splice`,
`MSG_ZEROCOPY`, io_uring `SEND_ZC`, zero-copy receive, kernel TLS, or a
kernel-bypass data path. It must therefore not be described as kernel or
end-to-end zero-copy.

Even a future `MSG_ZEROCOPY` call would be evidence of an attempted
copy-avoidance path, not proof that the kernel skipped the copy. Linux documents
that it is a hint, may complete with `SO_EE_CODE_ZEROCOPY_COPIED`, adds page
pinning and completion-notification costs, is generally useful only for larger
writes, and is not meaningfully validated on loopback. See the
[Linux `MSG_ZEROCOPY` documentation](https://docs.kernel.org/networking/msg_zerocopy.html).

## Current evidence boundary

Unit tests currently establish:

- KProV1 segmented headers match the legacy contiguous encoding;
- normal `Session.Send` preserves caller-copy semantics;
- an owned outbound buffer is consumed exactly once and is returned after
  completion or release;
- a reusable receive buffer remains owned while a Router handler is active and
  is returned only after the final Context reference;
- KProV1 `ReadInto` performs zero allocations with correctly sized scratch;
- KProV1 buffered decoding waits for a complete frame and returns an alias only
  for that complete frame;
- TCP consumes complete coalesced frames already present in the adaptive reader
  without another socket read;
- batch admission retains receive buffers through Router completion and falls
  back without transferring Context ownership or accepting a task for
  unsupported routes, policies, mixed sessions, or insufficient capacity;
- endpoint capability selection and batch-handler panic containment preserve
  ordinary transport behavior;
- `ReplyCurrentPayload` transfers one Context reference to the TCP writer and
  releases it on success, close, and failure, while unsupported contexts retain
  ordinary reply semantics;
- sharded pools preserve their aggregate retained-byte hard limit;
- the idle pool does not exceed its retained-byte setting;
- TCP and WebSocket batching preserves FIFO wire order and queue accounting;
- write failure releases every drained item; and
- a WebSocket close frame prevents later drained data from being written.

The repository benchmark budget also requires the 1 KiB segmented codec,
default TCP `Session.Send`, and 16-frame TCP/WebSocket batch loops to remain at
zero allocations after warm-up. The owned-buffer benchmark permits one
allocation for the public lease handle; in that warmed 1 KiB case it does not
allocate a new payload backing slice per operation. These are correctness and
regression results.
They do not establish service capacity, tail latency, syscall reduction,
kernel copy avoidance, or superiority to Netty.

The isolated 2026-08-05 candidate measurements, including retained and rejected
experiments, are recorded in [Benchmark Results](../benchmark/RESULTS.md#2026-08-05-bounded-receive-batch-candidate).
The corresponding current KPro/Netty loopback diagnostic is recorded in
[KPro and Netty Performance Baseline](netty-performance-baseline.md#2026-08-05-bounded-receive-batch-candidate-and-loopback-retest).

## KPro versus Netty baseline contract

The source-level architecture and concurrency interpretation are recorded in
[KPro and Netty Performance Baseline](netty-performance-baseline.md). The
contract below defines the additional evidence required to replace those
theoretical expectations with comparative performance claims.

Any public comparison must be reproducible from pinned source, toolchain, and
machine manifests. At minimum it must satisfy the following contract.

### Equal semantics

Run two separately reported lanes:

1. a transport floor consisting of the same KProV1 decode, validation, echo,
   encode, and peer byte verification; and
2. a framework lane consisting of KPro Router dispatch and an equivalent Netty
   executor, ordering, queue, byte limit, and overload policy.

Both implementations must use the same wire bytes, socket settings, flush
semantics, queue limits, handler work, response-completion definition, and
full-frame correctness checks. A KPro batch must not be compared only with a
Netty `writeAndFlush` on every individual message. Report both a
semantics-normalized configuration and a best-tuned configuration when their
recommended internal strategies differ.

### Controlled environment

- Use a Linux server and independent load generators; loopback is diagnostic
  only.
- Give each server the same CPU set, NUMA placement, total memory limit, socket
  limits, NIC queues, offloads, MTU, IRQ policy, and kernel settings.
- Pin KPro, Netty, Go, JDK, kernel, and native transport versions. Report Netty
  NIO, epoll, and io_uring results separately where applicable.
- Record `GOMAXPROCS`, `GOGC`, `GOMEMLIMIT`, JVM active processor count, heap,
  direct-memory limit, allocator, and GC. Compare total RSS/PSS, not Go heap
  against Java heap alone.
- Prove that the load generators and network have at least 50% headroom beyond
  the measured server capacity.

### Predeclared primary matrix

The initial raw TCP matrix has 24 workload shapes and 72 offered-load cells:

| Dimension | Values |
| --- | --- |
| Connections | 64, 1,000, 10,000 |
| Payload | 64 B, 1 KiB, 16 KiB, 64 KiB |
| Requests in flight per connection | 1, 16 |
| Offered load | 50%, 75%, and 90% of the lower measured sustainable capacity |

Add one-connection, one-in-flight 64 B and 1 KiB latency-floor cases. Run a
separate overload/slow-consumer case with 1% of peers not reading for 30
seconds. File transfer, TLS, UDP, QUIC, and WebSocket require separate matrices
and separate claims.

Use an open-loop load schedule so a stalled server cannot hide latency through
coordinated omission. Warm each fresh process for at least five minutes,
measure for at least ten minutes, alternate implementation order, and collect
at least seven independent process runs per primary cell.

### Required measurements

Record successful peer-validated messages and payload bytes per second,
p50/p99/p99.9 latency, errors and rejections, CPU-seconds per million messages,
cycles, instructions, context switches, total and peak RSS/PSS, allocation
rate, GC CPU and pause time, queue depth, retained and pinned bytes, and syscall
counts. For batching, include frames and bytes per write/writev/send call. For
overload, include healthy-peer tail latency, memory growth, and fairness.

Define sustainable capacity before running the comparison. A suitable minimum
is ten steady minutes with at least 99.9% offered requests completed, no frame
corruption or silent loss, a predeclared p99 objective, and no continuing
unbounded memory growth.

## Future acceptance gates

Cross-framework claims use ratios paired by machine and run. Confidence
intervals must be computed from independent process runs, not by treating every
request in one run as an independent experiment.

### Netty non-inferiority

KPro is non-inferior for the predeclared TCP matrix only when every primary
cell passes correctness and the one-sided 95% confidence bounds satisfy:

- sustainable throughput `KPro / Netty >= 0.95`;
- p99 latency `KPro / Netty <= 1.05`;
- p99.9 latency `KPro / Netty <= 1.10`;
- CPU per successful message `KPro / Netty <= 1.05`; and
- peak total RSS `KPro / Netty <= 1.10`.

An aggregate geometric mean must not hide a failed primary cell.

### Scoped performance lead

A statement that KPro leads Netty must name the transport, payload range,
connection range, dispatch mode, OS backend, and runtime settings. For that
predeclared scope, require either:

- a throughput confidence lower bound of at least `1.10`; or
- a CPU-per-message confidence upper bound of at most `0.90`;

while the p99 upper bound remains at most `1.05`, correctness has no failures,
and neither total memory nor overload isolation regresses materially. Repeat
the result on at least two server hardware configurations before describing it
as an architectural rather than machine-specific lead.

### Future kernel copy-avoidance path

If a later Linux backend adds `MSG_ZEROCOPY`, `SEND_ZC`, `sendfile`, or another
copy-avoidance mechanism, acceptance additionally requires:

- syscall or kernel trace evidence that the intended path was used;
- explicit copied-fallback and completion accounting;
- correct buffer lifetime through completion, retransmission, error, and close;
- a hard bound on pinned or registered memory;
- peer-side full-byte verification;
- separate small-message fallback and eligible large-message results; and
- at least a 15% reduction in CPU per GiB for the eligible workload without
  more than a 5% p99 latency regression.

Only after these checks may that specific path be called kernel
copy-avoidance. The broader framework should still not be labeled
"zero-copy" because other codecs, transports, TLS configurations, and response
sources may retain ordinary copy paths.
