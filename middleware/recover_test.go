package middleware

import (
	"net"
	"strings"
	"testing"

	"github.com/saramand9/bignet/kpro/core"
)

type recoverTestSession struct {
	sent []core.Message
}

func (s *recoverTestSession) ID() uint64 {
	return 1
}

func (s *recoverTestSession) Transport() core.Transport {
	return core.TransportTCP
}

func (s *recoverTestSession) RemoteAddr() net.Addr {
	return nil
}

func (s *recoverTestSession) Send(msg core.Message) error {
	s.sent = append(s.sent, msg)
	return nil
}

func (s *recoverTestSession) Close() error {
	return nil
}

func TestRecoverConvertsPanicToErrorMessage(t *testing.T) {
	session := &recoverTestSession{}
	ctx := core.NewContext(session, core.Message{ID: core.MsgEcho, Seq: 42, Data: []byte("request")})
	var loggedRecovered interface{}
	var loggedStack string
	previousLogger := logPanicStack
	logPanicStack = func(recovered interface{}, stack []byte) {
		loggedRecovered = recovered
		loggedStack = string(stack)
	}
	defer func() {
		logPanicStack = previousLogger
	}()

	handler := Recover()(func(core.Context) error {
		panic("simulated crash")
	})

	err := handler(ctx)
	if err == nil || err.Error() != "handler panic: simulated crash" {
		t.Fatalf("err = %v, want handler panic error", err)
	}
	if len(session.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(session.sent))
	}
	msg := session.sent[0]
	if msg.ID != core.MsgError || msg.Seq != 42 || string(msg.Data) != "handler panic" {
		t.Fatalf("sent message = id=%d seq=%d data=%q", msg.ID, msg.Seq, string(msg.Data))
	}
	if loggedRecovered != "simulated crash" {
		t.Fatalf("logged recovered = %v, want simulated crash", loggedRecovered)
	}
	if !strings.Contains(loggedStack, "runtime/debug.Stack") ||
		!strings.Contains(loggedStack, "TestRecoverConvertsPanicToErrorMessage") {
		t.Fatalf("logged stack does not include expected frames:\n%s", loggedStack)
	}
	t.Logf("handler returned error: %v", err)
	t.Logf("client-visible message: id=%d seq=%d data=%q", msg.ID, msg.Seq, string(msg.Data))
	t.Logf("server logged panic: %v", loggedRecovered)
	t.Logf("server stack starts with: %q", firstLine(loggedStack))
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
