package tcp

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func TestSendSegmentedFastPathPreservesCallerCopySemantics(t *testing.T) {
	server := New(Config{MaxFrameSize: 1024, SendQueueSize: 2})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	payload := []byte("hello")
	if err := session.Send(core.Message{ID: 7, Seq: 9, Data: payload}); err != nil {
		t.Fatal(err)
	}
	payload[0] = 'X'
	item := <-session.sendQueue.Items()
	message := decodeOutboundFrame(t, item.Value)
	if got := string(message.Data); got != "hello" {
		t.Fatalf("queued payload = %q, want hello", got)
	}
	item.Done()
}

func TestReserveOutboundBufferCommitsWithoutPayloadCopy(t *testing.T) {
	server := New(Config{
		MaxFrameSize:           1024,
		SendQueueSize:          2,
		MaxRetainedBufferBytes: 1024,
	})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	buffer, supported, err := core.SessionReserveOutboundBuffer(session, 5)
	if err != nil || !supported {
		t.Fatalf("SessionReserveOutboundBuffer() = (supported=%v, err=%v)", supported, err)
	}
	if payload := buffer.Bytes(); len(payload) != 5 || cap(payload) != 5 {
		t.Fatalf("Bytes() shape = len %d cap %d, want 5/5", len(payload), cap(payload))
	}
	copy(buffer.Bytes(), "hello")
	if err := buffer.Commit(core.Message{Flags: 3, ID: 7, Seq: 9, Data: []byte("ignored")}); err != nil {
		t.Fatal(err)
	}
	if buffer.Bytes() != nil {
		t.Fatal("committed buffer still exposes mutable bytes")
	}
	owned := buffer.(*sessionOutboundBuffer)
	if owned.session != nil {
		t.Fatal("committed buffer retained its session")
	}
	item := <-session.sendQueue.Items()
	message := decodeOutboundFrame(t, item.Value)
	if message.Flags != 3 || message.ID != 7 || message.Seq != 9 || string(message.Data) != "hello" {
		t.Fatalf("decoded committed message = %+v", message)
	}
	item.Done()
	if got := server.payloadPool.RetainedBytes(); got != 16 {
		t.Fatalf("retained bytes after write completion = %d, want 16", got)
	}
}

func TestReserveOutboundBufferReleaseAndRepeatedUse(t *testing.T) {
	server := New(Config{MaxFrameSize: 1024, SendQueueSize: 1, MaxRetainedBufferBytes: 1024})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	buffer, err := session.ReserveOutboundBuffer(32)
	if err != nil {
		t.Fatal(err)
	}
	buffer.Release()
	owned := buffer.(*sessionOutboundBuffer)
	if owned.session != nil {
		t.Fatal("released buffer retained its session")
	}
	buffer.Release()
	if err := buffer.Commit(core.Message{}); !errors.Is(err, core.ErrInvalidOutboundBuffer) {
		t.Fatalf("Commit() after Release error = %v", err)
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after Release = %+v", snapshot)
	}
	if got := server.payloadPool.RetainedBytes(); got != 32 {
		t.Fatalf("retained bytes after Release = %d, want 32", got)
	}
}

func TestReserveOutboundBufferCleansUpWhenSegmentedCodecPanics(t *testing.T) {
	server := New(Config{
		Codec:                  panickingSegmentedCodec{StreamCodec: codec.KProV1()},
		SendQueueSize:          1,
		MaxRetainedBufferBytes: 1024,
	})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	buffer, err := session.ReserveOutboundBuffer(32)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Commit() did not propagate codec panic")
			}
		}()
		_ = buffer.Commit(core.Message{})
	}()
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after codec panic = %+v", snapshot)
	}
	if got := server.payloadPool.RetainedBytes(); got != 32 {
		t.Fatalf("retained bytes after codec panic = %d, want 32", got)
	}
	owned := buffer.(*sessionOutboundBuffer)
	if owned.session != nil {
		t.Fatal("panicked buffer retained its session")
	}
}

func TestCustomSegmentedCodecMayReturnSeparateHeaderSlice(t *testing.T) {
	server := New(Config{Codec: separateHeaderCodec{StreamCodec: codec.KProV1()}, SendQueueSize: 1})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	if err := session.Send(core.Message{ID: 7, Seq: 9, Data: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	item := <-session.sendQueue.Items()
	message := decodeOutboundFrame(t, item.Value)
	item.Done()
	if message.ID != 7 || message.Seq != 9 || string(message.Data) != "hello" {
		t.Fatalf("decoded custom segmented message = %+v", message)
	}
}

func TestEmbeddedKProCodecOverrideDoesNotAccidentallyUseSegmentedPath(t *testing.T) {
	server := New(Config{Codec: embeddedKProOverrideCodec{}, SendQueueSize: 1})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	if err := session.Send(core.Message{Data: []byte("ignored")}); err != nil {
		t.Fatal(err)
	}
	item := <-session.sendQueue.Items()
	wire := bytes.Join(item.Value.appendBuffers(nil), nil)
	item.Done()
	if got, want := string(wire), "custom-wire"; got != want {
		t.Fatalf("wire = %q, want %q", got, want)
	}
}

func TestCustomSegmentedHeaderSizeControlsAdmissionEstimate(t *testing.T) {
	server := New(Config{
		Codec:           oneByteHeaderCodec{StreamCodec: codec.KProV1()},
		SendQueueSize:   1,
		MaxPendingBytes: 21,
	})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	if err := session.Send(core.Message{Data: make([]byte, 20)}); err != nil {
		t.Fatal(err)
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 1 || snapshot.PendingBytes != 21 {
		t.Fatalf("snapshot after custom header send = %+v", snapshot)
	}
	item := <-session.sendQueue.Items()
	if got := item.Value.wireBytes(); got != 21 {
		t.Fatalf("custom frame wire bytes = %d, want 21", got)
	}
	item.Done()
}

func TestReserveOutboundBufferValidatesSizeAndCodec(t *testing.T) {
	server := New(Config{MaxFrameSize: 4})
	testSession := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	if _, err := testSession.ReserveOutboundBuffer(-1); !errors.Is(err, core.ErrInvalidOutboundBuffer) {
		t.Fatalf("negative size error = %v", err)
	}
	buffer, err := testSession.ReserveOutboundBuffer(5)
	if err != nil {
		t.Fatalf("outbound size above inbound MaxFrameSize error = %v", err)
	}
	buffer.Release()

	server = New(Config{Codec: unsegmentedStreamCodec{StreamCodec: codec.KProV1()}})
	testSession = &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	if _, err := testSession.ReserveOutboundBuffer(1); !errors.Is(err, core.ErrOutboundBufferUnsupported) {
		t.Fatalf("unsegmented codec error = %v", err)
	}
}

func TestReserveOutboundBufferRejectsKProV1ProtocolOverflowBeforeReservation(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("an int cannot represent a payload above the KPro V1 uint32 limit")
	}
	oversized64 := uint64(^uint32(0)) + 1
	server := New(Config{SendQueueSize: 1, MaxPendingBytes: math.MaxInt64})
	testSession := &session{
		server:    server,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	if _, err := testSession.ReserveOutboundBuffer(int(oversized64)); !errors.Is(err, codec.ErrFrameTooLarge) {
		t.Fatalf("oversized KPro V1 reservation error = %v, want %v", err, codec.ErrFrameTooLarge)
	}
	if snapshot := testSession.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after protocol overflow = %+v", snapshot)
	}
}

type unsegmentedStreamCodec struct {
	codec.StreamCodec
}

type panickingSegmentedCodec struct {
	codec.StreamCodec
}

func (panickingSegmentedCodec) HeaderSize() int { return codec.FrameHeaderSize }
func (panickingSegmentedCodec) AppendHeader([]byte, core.Message) ([]byte, error) {
	panic("append header failed")
}

type separateHeaderCodec struct {
	codec.StreamCodec
}

func (separateHeaderCodec) HeaderSize() int { return codec.FrameHeaderSize }
func (separateHeaderCodec) AppendHeader(_ []byte, message core.Message) ([]byte, error) {
	header := make([]byte, codec.FrameHeaderSize)
	return header, codec.WriteKProV1Header(header, message)
}

type embeddedKProOverrideCodec struct {
	codec.KProV1Codec
}

type oneByteHeaderCodec struct {
	codec.StreamCodec
}

func (oneByteHeaderCodec) HeaderSize() int { return 1 }
func (oneByteHeaderCodec) AppendHeader(dst []byte, _ core.Message) ([]byte, error) {
	return append(dst, 0x01), nil
}

func (embeddedKProOverrideCodec) Encode(core.Message) ([]byte, error) {
	return []byte("custom-wire"), nil
}

func decodeOutboundFrame(t testing.TB, frame outboundFrame) core.Message {
	t.Helper()
	buffers := frame.appendBuffers(nil)
	wire := bytes.Join(buffers, nil)
	message, err := codec.KProV1().DecodePacket(wire)
	if err != nil {
		t.Fatal(err)
	}
	return message
}
