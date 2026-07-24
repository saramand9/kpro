// File: test/protocol_smoke_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"bytes"
	"testing"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func TestKProV1RoundTrip(t *testing.T) {
	want := core.Message{ID: testMsgEcho, Seq: 42, Data: []byte("hello")}
	frame, err := codec.KProV1().Encode(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := codec.KProV1().Read(bytes.NewReader(frame), 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Message.ID != want.ID || got.Message.Seq != want.Seq || string(got.Message.Data) != string(want.Data) {
		t.Fatalf("got %+v, want %+v", got.Message, want)
	}
}
