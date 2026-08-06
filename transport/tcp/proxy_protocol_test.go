package tcp

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/proxyproto"
)

func TestProxyProtocolAdvertisesClientAddressToMessageHandler(t *testing.T) {
	type observation struct {
		remote string
		info   proxyproto.Info
		ok     bool
	}
	observed := make(chan observation, 1)
	_, addr, stop := startTCPTestServer(t, Config{
		Addr: freeTCPTestAddr(t),
		ProxyProtocol: proxyproto.Config{
			Enabled:        true,
			TrustedProxies: []string{"127.0.0.1"},
		},
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}, func(ctx core.Context) error {
		info, ok := proxyproto.InfoFrom(ctx.Session())
		observed <- observation{remote: ctx.RemoteAddr(), info: info, ok: ok}
		message := ctx.Message()
		return ctx.Reply(message.ID, message.Data)
	})
	t.Cleanup(stop)

	connection, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("PROXY TCP4 203.0.113.10 198.51.100.20 45678 8088\r\n")); err != nil {
		t.Fatalf("write PROXY header: %v", err)
	}
	writeTCPTestMessage(t, connection, core.Message{ID: 100, Seq: 7, Data: []byte("hello")})
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	response, err := codec.KProV1().Read(connection, DefaultMaxFrameSize)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.Message.ID != 100 || response.Message.Seq != 7 || string(response.Message.Data) != "hello" {
		t.Fatalf("response = %+v", response.Message)
	}

	select {
	case got := <-observed:
		if got.remote != "203.0.113.10:45678" {
			t.Fatalf("handler remote = %q", got.remote)
		}
		if !got.ok || got.info.Header.Version != 1 || got.info.Header.Protocol != "TCP4" {
			t.Fatalf("handler proxy info = %+v, %v", got.info, got.ok)
		}
		if !strings.HasPrefix(got.info.DirectRemoteAddr, "127.0.0.1:") {
			t.Fatalf("direct remote = %q", got.info.DirectRemoteAddr)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not observe the proxied connection")
	}
}
