// File: internal/modules/login/handler/udp_test.go
// Purpose: Tests UDP message DTO handlers for the login module.

package handler

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	loginservice "github.com/saramand9/kpro/internal/modules/login/service"
)

func TestUDPLoginHandlerReturnsSession(t *testing.T) {
	service := loginservice.New(loginservice.Config{
		Username: "ada",
		Password: "secret",
		TokenTTL: 2 * time.Minute,
		Now: func() time.Time {
			return time.Unix(100, 0)
		},
	})
	session := &udpTestSession{id: 1}

	payload := mustJSON(t, LoginRequest{Username: "ada", Password: "secret"})
	if err := udpLogin(service)(core.NewContext(session, core.Message{ID: MsgLogin, Seq: 10, Data: payload})); err != nil {
		t.Fatal(err)
	}

	msg := session.last()
	if msg.ID != MsgLogin || msg.Seq != 10 {
		t.Fatalf("reply message = %#v", msg)
	}
	var response loginservice.Session
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Username != "ada" || response.Token == "" || response.ExpiresIn != 120 {
		t.Fatalf("response = %#v", response)
	}
}

func TestUDPLoginHandlerWritesProtocolError(t *testing.T) {
	service := loginservice.New(loginservice.Config{Username: "ada", Password: "secret"})
	session := &udpTestSession{id: 1}

	payload := mustJSON(t, LoginRequest{Username: "ada", Password: "wrong"})
	if err := udpLogin(service)(core.NewContext(session, core.Message{ID: MsgLogin, Seq: 11, Data: payload})); err != nil {
		t.Fatal(err)
	}

	msg := session.last()
	if msg.ID != core.MsgError || msg.Seq != 11 || string(msg.Data) != "invalid credentials" {
		t.Fatalf("error message = %#v", msg)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type udpTestSession struct {
	id   uint64
	sent []core.Message
}

func (s *udpTestSession) ID() uint64                { return s.id }
func (s *udpTestSession) Transport() core.Transport { return core.TransportUDP }
func (s *udpTestSession) RemoteAddr() net.Addr      { return udpTestAddr("127.0.0.1:9000") }
func (s *udpTestSession) Send(msg core.Message) error {
	s.sent = append(s.sent, msg)
	return nil
}
func (s *udpTestSession) Close() error { return nil }
func (s *udpTestSession) last() core.Message {
	if len(s.sent) == 0 {
		return core.Message{}
	}
	return s.sent[len(s.sent)-1]
}

type udpTestAddr string

func (a udpTestAddr) Network() string { return "udp" }
func (a udpTestAddr) String() string  { return string(a) }
