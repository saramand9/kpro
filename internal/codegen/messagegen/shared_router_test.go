package messagegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedAdapterDispatchesEverySharedRouterTransport(t *testing.T) {
	document, err := Parse([]byte(validMessageSpec))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(document, Options{
		Module: "example.com/shared",
		Output: "internal/messages",
	})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	writePlan(t, root, plan)
	goMod := "module example.com/shared\n\ngo 1.25.0\n\n" +
		"require github.com/saramand9/kpro v0.0.0\n\n" +
		"replace github.com/saramand9/kpro => " +
		filepath.ToSlash(repositoryRoot(t)) +
		"\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(
		root,
		"internal",
		"messages",
		"kpromessage",
		"shared_router_test.go",
	)
	if err := os.WriteFile(filename, []byte(sharedRouterGeneratedTest), 0o644); err != nil {
		t.Fatal(err)
	}
	runGoTest(t, root)
}

const sharedRouterGeneratedTest = `package kpromessage

import (
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	types "example.com/shared/internal/messages/types"
)

type sharedService struct {
	seen chan core.Transport
}

func (service *sharedService) CreateOrder(
	ctx core.Context,
	request types.CreateOrderRequest,
) (types.CreateOrderResponse, error) {
	service.seen <- ctx.Transport()
	return types.CreateOrderResponse{Accepted: request.OrderId != ""}, nil
}

func (*sharedService) OrderNotify(core.Context, types.OrderNotifyRequest) error {
	return nil
}

type transportSession struct {
	id uint64
	transport core.Transport
	sent chan core.Message
	done chan struct{}
}

func (session *transportSession) ID() uint64 { return session.id }
func (session *transportSession) Transport() core.Transport { return session.transport }
func (session *transportSession) RemoteAddr() net.Addr { return nil }
func (session *transportSession) Send(message core.Message) error {
	session.sent <- message
	return nil
}
func (session *transportSession) Close() error {
	select {
	case <-session.done:
	default:
		close(session.done)
	}
	return nil
}
func (session *transportSession) Done() <-chan struct{} { return session.done }

func TestOneRouterAdapterServesEveryMessageTransport(t *testing.T) {
	service := &sharedService{seen: make(chan core.Transport, 1)}
	router := core.NewRouter()
	defer router.Close()
	if err := RegisterOrders(router, service); err != nil {
		t.Fatal(err)
	}

	transports := []core.Transport{
		core.TransportTCP,
		core.TransportKCP,
		core.TransportQUIC,
		core.TransportUDP,
		core.TransportWebSocket,
	}
	for index, transport := range transports {
		session := &transportSession{
			id: uint64(index + 1),
			transport: transport,
			sent: make(chan core.Message, 1),
			done: make(chan struct{}),
		}
		ctx := core.NewContext(session, core.Message{
			ID: types.CreateOrderMessageID,
			Seq: uint64(index + 10),
			Data: []byte("{\"orderId\":\"A-1\"}"),
		})
		if err := router.Dispatch(ctx); err != nil {
			t.Fatalf("%s dispatch: %v", transport, err)
		}
		select {
		case seen := <-service.seen:
			if seen != transport {
				t.Fatalf("service transport = %q, want %q", seen, transport)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s handler timed out", transport)
		}
		select {
		case response := <-session.sent:
			if response.ID != types.CreateOrderMessageID {
				t.Fatalf("%s response id = %d", transport, response.ID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s response timed out", transport)
		}
	}
}
`
