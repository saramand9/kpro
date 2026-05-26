package test

import (
	"bytes"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

func TestKProV1RoundTrip(t *testing.T) {
	want := core.Message{ID: core.MsgEcho, Seq: 42, Data: []byte("hello")}
	frame, err := protocol.KProV1().Encode(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := protocol.KProV1().Read(bytes.NewReader(frame), 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Message.ID != want.ID || got.Message.Seq != want.Seq || string(got.Message.Data) != string(want.Data) {
		t.Fatalf("got %+v, want %+v", got.Message, want)
	}
}
