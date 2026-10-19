package messagegen

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
)

func TestBuildPlanIsDeterministicAndOwnershipAware(t *testing.T) {
	document, err := Parse([]byte(validMessageSpec))
	if err != nil {
		t.Fatal(err)
	}
	options := Options{
		Module: "example.com/orders",
		Output: "internal/messages",
	}
	first, err := BuildPlan(document, options)
	if err != nil {
		t.Fatalf("first BuildPlan() error = %v", err)
	}
	second, err := BuildPlan(document, options)
	if err != nil {
		t.Fatalf("second BuildPlan() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("BuildPlan() changed output for identical input")
	}

	files, err := first.OrderedFiles()
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		path      string
		ownership codegen.Ownership
	}{
		{"internal/messages/contract/service_orders.gen.go", codegen.Generated},
		{"internal/messages/kpromessage/routes_orders.gen.go", codegen.Generated},
		{"internal/messages/service/orders.go", codegen.Scaffold},
		{"internal/messages/types/messages_orders.gen.go", codegen.Generated},
	}
	if len(files) != len(want) {
		t.Fatalf("plan files = %d, want %d: %#v", len(files), len(want), files)
	}
	for index, expected := range want {
		if files[index].Path != expected.path || files[index].Ownership != expected.ownership {
			t.Errorf(
				"file[%d] = %q/%q, want %q/%q",
				index,
				files[index].Path,
				files[index].Ownership,
				expected.path,
				expected.ownership,
			)
		}
	}

	adapter := fileContent(t, files, "internal/messages/kpromessage/routes_orders.gen.go")
	for _, required := range []string{
		"type OrdersRegistrar interface",
		"HandleSyncChecked(uint32, core.Handler) error",
		"HandleAsyncChecked(uint32, core.Handler) error",
		"func RegisterOrders(registrar OrdersRegistrar, service contract.OrdersService) error",
		"registrar.HandleSyncChecked(types.CreateOrderMessageID",
		"registrar.HandleAsyncChecked(types.OrderNotifyMessageID",
		`fmt.Errorf("message registrar is nil")`,
		"type OrdersBindingError struct",
		"var rawFields map[string]json.RawMessage",
		"ctx.Reply(types.CreateOrderMessageID, payload)",
	} {
		if !strings.Contains(adapter, required) {
			t.Errorf("adapter is missing %q:\n%s", required, adapter)
		}
	}
	for _, unrelated := range []string{
		"transport/httpx",
		"google.golang.org/grpc",
		"grpc.ServiceRegistrar",
		"github.com/saramand9/kpro/server",
	} {
		if strings.Contains(adapter, unrelated) {
			t.Errorf("message adapter contains unrelated native binding %q", unrelated)
		}
	}

	types := fileContent(t, files, "internal/messages/types/messages_orders.gen.go")
	for _, required := range []string{
		"CreateOrderMessageID uint32 = 100",
		"OrderNotifyMessageID uint32 = 101",
		"OrderId  string",
		"Quantity *int32",
		"Receipt  *[]byte",
	} {
		if !strings.Contains(types, required) {
			t.Errorf("types output is missing %q:\n%s", required, types)
		}
	}
}

func TestBuildPlanCanonicalizesMessageAndFieldOrder(t *testing.T) {
	first, err := Parse([]byte(validMessageSpec))
	if err != nil {
		t.Fatal(err)
	}
	second := cloneDocument(first)
	second.Messages[0], second.Messages[1] = second.Messages[1], second.Messages[0]
	fields := second.Messages[1].Request.Fields
	fields[0], fields[1] = fields[1], fields[0]
	responseFields := second.Messages[1].Response.Fields
	responseFields[0], responseFields[1] = responseFields[1], responseFields[0]

	options := Options{Module: "example.com/orders", Output: "internal/messages"}
	firstPlan, err := BuildPlan(first, options)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := BuildPlan(second, options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstPlan, secondPlan) {
		t.Fatal("semantic reordering changed generated output")
	}
}

func TestBuildPlanRejectsInvalidTargetBeforeReturningFiles(t *testing.T) {
	document, err := Parse([]byte(validMessageSpec))
	if err != nil {
		t.Fatal(err)
	}
	tests := []Options{
		{Module: "", Output: "internal/messages"},
		{Module: "example.com/orders ", Output: "internal/messages"},
		{Module: "example.com/orders", Output: "../outside"},
		{Module: "example.com/orders", Output: "internal/type"},
		{Module: "example.com/orders", Output: "internal/message-gen"},
	}
	for _, options := range tests {
		if plan, err := BuildPlan(document, options); err == nil || len(plan.Files) != 0 {
			t.Errorf("BuildPlan(%#v) = %#v, %v; want empty plan and error", options, plan, err)
		}
	}
}

func TestGeneratedPackagesCompileAndExerciseBinding(t *testing.T) {
	document, err := Parse([]byte(validMessageSpec))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(document, Options{
		Module: "example.com/orders",
		Output: "internal/messages",
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}

	root := t.TempDir()
	writePlan(t, root, plan)
	repository := repositoryRoot(t)
	goMod := "module example.com/orders\n\ngo 1.26.9\n\n" +
		"require github.com/saramand9/kpro v0.0.0\n\n" +
		"replace github.com/saramand9/kpro => " + filepath.ToSlash(repository) + "\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}

	testPath := filepath.Join(root, "internal", "messages", "kpromessage", "routes_behavior_test.go")
	if err := os.WriteFile(testPath, []byte(generatedBehaviorTest), 0o644); err != nil {
		t.Fatal(err)
	}
	runGoTest(t, root)
}

func fileContent(t *testing.T, files []codegen.File, name string) string {
	t.Helper()
	for _, file := range files {
		if file.Path == name {
			return string(file.Content)
		}
	}
	t.Fatalf("plan does not contain %q", name)
	return ""
}

func writePlan(t *testing.T, root string, plan codegen.Plan) {
	t.Helper()
	files, err := plan.OrderedFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		filename := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, file.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report the test filename")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}

const generatedBehaviorTest = `package kpromessage

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/server"
	types "example.com/orders/internal/messages/types"
)

type ordersImplementation struct{}

func (*ordersImplementation) CreateOrder(
	_ core.Context,
	request types.CreateOrderRequest,
) (types.CreateOrderResponse, error) {
	return types.CreateOrderResponse{Accepted: request.OrderId == "A-1"}, nil
}

func (*ordersImplementation) OrderNotify(core.Context, types.OrderNotifyRequest) error {
	return nil
}

type testSession struct {
	sent chan core.Message
	done chan struct{}
}

func (session *testSession) ID() uint64 { return 7 }
func (session *testSession) Transport() core.Transport { return core.TransportTCP }
func (session *testSession) RemoteAddr() net.Addr { return nil }
func (session *testSession) Send(message core.Message) error {
	session.sent <- message
	return nil
}
func (session *testSession) Close() error {
	select {
	case <-session.done:
	default:
		close(session.done)
	}
	return nil
}
func (session *testSession) Done() <-chan struct{} { return session.done }

func TestGeneratedRouterBinding(t *testing.T) {
	router := core.NewRouter()
	defer router.Close()
	if err := RegisterOrders(router, &ordersImplementation{}); err != nil {
		t.Fatal(err)
	}
	session := &testSession{sent: make(chan core.Message, 1), done: make(chan struct{})}
	ctx := core.NewContext(session, core.Message{
		ID: types.CreateOrderMessageID,
		Seq: 19,
		Data: []byte("{\"orderId\":\"A-1\"}"),
	})
	if err := router.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-session.sent:
		if response.ID != types.CreateOrderMessageID || response.Seq != 19 {
			t.Fatalf("response = %#v", response)
		}
		if string(response.Data) != "{\"accepted\":true}" {
			t.Fatalf("response data = %s", response.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for generated handler response")
	}
}

func TestGeneratedBindingErrorsAreTypedAndSpecific(t *testing.T) {
	_, err := decodeCreateOrderRequest([]byte("{\"unknown\":true}"))
	var binding *OrdersBindingError
	if !errors.As(err, &binding) || binding.MessageID != types.CreateOrderMessageID {
		t.Fatalf("unknown field error = %#v", err)
	}
	if binding.Err == nil {
		t.Fatal("binding error did not retain its cause")
	}

	_, err = decodeCreateOrderRequest([]byte("{\"ORDERID\":\"A-1\"}"))
	if !errors.As(err, &binding) ||
		binding.Error() != "bind message \"createOrder\" (100): unknown field \"ORDERID\"" {
		t.Fatalf("case-mismatched field error = %v", err)
	}
	_, err = decodeCreateOrderRequest([]byte("{\"zUnknown\":true,\"aUnknown\":true}"))
	if !errors.As(err, &binding) ||
		binding.Error() != "bind message \"createOrder\" (100): unknown field \"aUnknown\"" {
		t.Fatalf("deterministic unknown field error = %v", err)
	}

	_, err = decodeCreateOrderRequest([]byte("{\"orderId\":null}"))
	if !errors.As(err, &binding) ||
		binding.Error() != "bind message \"createOrder\" (100): required field orderId is missing or null" {
		t.Fatalf("required field error = %v", err)
	}

	_, err = decodeCreateOrderRequest([]byte("{} {}"))
	if !errors.As(err, &binding) {
		t.Fatalf("trailing value error = %v", err)
	}
}

func TestGeneratedRegistrationRejectsTypedNilService(t *testing.T) {
	router := core.NewRouter()
	defer router.Close()
	var service *ordersImplementation
	if err := RegisterOrders(router, service); err == nil {
		t.Fatal("RegisterOrders accepted a typed nil service")
	}
}

func TestGeneratedRegistrationRejectsTypedNilRegistrar(t *testing.T) {
	var router *core.Router
	if err := RegisterOrders(router, &ordersImplementation{}); err == nil {
		t.Fatal("RegisterOrders accepted a typed nil registrar")
	}
}

type fakeMessageServer struct {
	handler core.Handler
}

func (*fakeMessageServer) Name() string { return "fake" }
func (transport *fakeMessageServer) ListenAndServe(_ context.Context, handler core.Handler) error {
	transport.handler = handler
	return nil
}
func (*fakeMessageServer) Shutdown(context.Context) error { return nil }

func TestGeneratedRegistrationAcceptsServerEndpoint(t *testing.T) {
	app := server.New()
	transport := &fakeMessageServer{}
	endpoint := app.Mount(transport)
	defer endpoint.Shutdown(context.Background())
	if err := RegisterOrders(endpoint, &ordersImplementation{}); err != nil {
		t.Fatalf("RegisterOrders(*server.Endpoint): %v", err)
	}
	if err := endpoint.ListenAndServe(context.Background()); err != nil {
		t.Fatalf("endpoint.ListenAndServe(): %v", err)
	}
	if transport.handler == nil {
		t.Fatal("message transport did not receive the endpoint handler")
	}

	session := &testSession{sent: make(chan core.Message, 1), done: make(chan struct{})}
	ctx := core.NewContext(session, core.Message{
		ID: types.CreateOrderMessageID,
		Seq: 23,
		Data: []byte("{\"orderId\":\"A-1\"}"),
	})
	if err := transport.handler(ctx); err != nil {
		t.Fatalf("endpoint handler: %v", err)
	}
	select {
	case response := <-session.sent:
		if response.ID != types.CreateOrderMessageID || response.Seq != 23 {
			t.Fatalf("endpoint response = %#v", response)
		}
		if string(response.Data) != "{\"accepted\":true}" {
			t.Fatalf("endpoint response data = %s", response.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for endpoint handler response")
	}

	if err := RegisterOrders(endpoint, &ordersImplementation{}); !errors.Is(err, core.ErrHandlerAlreadyRegistered) {
		t.Fatalf("duplicate registration error = %v, want ErrHandlerAlreadyRegistered", err)
	}
}

var _ OrdersRegistrar = (*core.Router)(nil)
var _ OrdersRegistrar = (*server.Endpoint)(nil)
`
