package httpx

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

const (
	msgHTTPMessageEcho    uint32 = 4101
	msgHTTPMessageCreated uint32 = 4102
	msgHTTPMessageBlock   uint32 = 4103
	msgHTTPMessageFast    uint32 = 4104
	msgHTTPMessageMissing uint32 = 4105
	msgHTTPMessageMany    uint32 = 4106
	msgHTTPMessageQueue   uint32 = 4107
	msgHTTPMessageAsync   uint32 = 4108
	msgHTTPMessageClosed  uint32 = 4109
)

func TestMessageEndpointDispatchesToCoreRouter(t *testing.T) {
	endpoint := NewMessage(Config{})
	endpoint.Use(func(next core.Handler) core.Handler {
		return func(ctx core.Context) error {
			ctx.Set("middleware", "hit")
			return next(ctx)
		}
	})
	endpoint.Post("/echo", msgHTTPMessageEcho).
		HandleSync(msgHTTPMessageEcho, func(ctx core.Context) error {
			if ctx.Transport() != core.TransportHTTP {
				t.Fatalf("transport = %s, want %s", ctx.Transport(), core.TransportHTTP)
			}
			if ctx.RemoteAddr() == "" {
				t.Fatal("remote addr is empty")
			}
			if value, ok := ctx.Get("middleware"); !ok || value != "hit" {
				t.Fatalf("middleware value = %v, ok=%v", value, ok)
			}
			msg := ctx.Message()
			if msg.Headers[MessageHeaderMethod] != http.MethodPost {
				t.Fatalf("method header = %q", msg.Headers[MessageHeaderMethod])
			}
			if msg.Headers[MessageHeaderPath] != "/echo" {
				t.Fatalf("path header = %q", msg.Headers[MessageHeaderPath])
			}
			if msg.Meta[MessageMetaPath] != "/echo" {
				t.Fatalf("path meta = %v", msg.Meta[MessageMetaPath])
			}
			return ctx.Reply(msgHTTPMessageEcho, append([]byte("echo:"), msg.Data...))
		})

	resp := executeMessageRequest(endpoint, http.MethodPost, "/echo", "hello")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := resp.Body.String(); got != "echo:hello" {
		t.Fatalf("body = %q", got)
	}
}

func TestMessageEndpointCanSetHTTPStatusAndHeaders(t *testing.T) {
	endpoint := NewMessage(Config{})
	endpoint.Post("/created", msgHTTPMessageCreated).
		HandleSync(msgHTTPMessageCreated, func(ctx core.Context) error {
			return ctx.Session().Send(core.Message{
				ID:   msgHTTPMessageCreated,
				Seq:  ctx.Message().Seq,
				Data: []byte("created"),
				Headers: map[string]string{
					MessageHeaderStatus: "201",
					"Content-Type":      "text/plain",
					"X-Trace":           "abc",
				},
			})
		})

	resp := executeMessageRequest(endpoint, http.MethodPost, "/created", "")

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusCreated)
	}
	if got := resp.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("content type = %q", got)
	}
	if got := resp.Header().Get("X-Trace"); got != "abc" {
		t.Fatalf("x-trace = %q", got)
	}
	if got := resp.Body.String(); got != "created" {
		t.Fatalf("body = %q", got)
	}
}

func TestMessageEndpointReturnsCoreNoHandlerError(t *testing.T) {
	endpoint := NewMessage(Config{})
	endpoint.Post("/missing", msgHTTPMessageMissing)

	resp := executeMessageRequest(endpoint, http.MethodPost, "/missing", "")

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(resp.Body.String(), core.ErrNoHandler.Error()) {
		t.Fatalf("body = %q, want no handler error", resp.Body.String())
	}
}

func TestMessageEndpointSyncRequestsKeepSessionOrder(t *testing.T) {
	endpoint := NewMessage(Config{}, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	})

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseFirst)
		})
	}
	defer release()
	defer shutdownMessageEndpoint(t, endpoint)

	endpoint.Post("/ordered", msgHTTPMessageQueue).
		HandleSync(msgHTTPMessageQueue, func(ctx core.Context) error {
			switch string(ctx.Message().Data) {
			case "first":
				close(firstStarted)
				<-releaseFirst
			case "second":
				close(secondStarted)
			}
			return ctx.Reply(msgHTTPMessageQueue, ctx.Message().Data)
		})

	firstRespCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstRespCh <- executeMessageRequest(endpoint, http.MethodPost, "/ordered", "first")
	}()
	waitForClosed(t, firstStarted, "first HTTPMessage sync handler did not start")

	secondRespCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondRespCh <- executeMessageRequest(endpoint, http.MethodPost, "/ordered", "second")
	}()

	select {
	case <-secondStarted:
		t.Fatal("second sync handler started before first completed")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	assertMessageResponse(t, <-firstRespCh, http.StatusOK, []byte("first"))
	select {
	case secondResp := <-secondRespCh:
		assertMessageResponse(t, secondResp, http.StatusOK, []byte("second"))
	case <-time.After(time.Second):
		t.Fatal("second sync response did not complete")
	}
}

func TestMessageEndpointAsyncHandlerCanReturnBeforeBlockedSyncHandler(t *testing.T) {
	endpoint := NewMessage(Config{}, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	})

	syncStarted := make(chan struct{})
	releaseSync := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseSync)
		})
	}
	defer release()
	defer shutdownMessageEndpoint(t, endpoint)

	endpoint.Post("/block", msgHTTPMessageBlock).
		Post("/fast", msgHTTPMessageFast).
		HandleSync(msgHTTPMessageBlock, func(ctx core.Context) error {
			close(syncStarted)
			<-releaseSync
			return ctx.Reply(msgHTTPMessageBlock, []byte("sync"))
		}).
		HandleAsync(msgHTTPMessageFast, func(ctx core.Context) error {
			return ctx.Reply(msgHTTPMessageFast, []byte("async"))
		})

	blockRespCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		blockRespCh <- executeMessageRequest(endpoint, http.MethodPost, "/block", "sync")
	}()

	select {
	case <-syncStarted:
	case <-time.After(time.Second):
		t.Fatal("sync handler did not start")
	}

	fastResp := executeMessageRequest(endpoint, http.MethodPost, "/fast", "async")
	if fastResp.Code != http.StatusOK || fastResp.Body.String() != "async" {
		t.Fatalf("fast response status=%d body=%q", fastResp.Code, fastResp.Body.String())
	}

	release()
	select {
	case blockResp := <-blockRespCh:
		if blockResp.Code != http.StatusOK || blockResp.Body.String() != "sync" {
			t.Fatalf("block response status=%d body=%q", blockResp.Code, blockResp.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("blocked response did not complete")
	}
}

func TestMessageEndpointThousandSyncAndAsyncEchoes(t *testing.T) {
	endpoint := NewMessage(Config{}, core.RouterConfig{
		SyncQueueSize:         64,
		AsyncWorkers:          4,
		AsyncQueueSize:        128,
		MaxInflightPerSession: 128,
	})
	defer shutdownMessageEndpoint(t, endpoint)

	echo := func(ctx core.Context) error {
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	}
	endpoint.Post("/sync-many", msgHTTPMessageMany).
		Post("/async-many", msgHTTPMessageAsync).
		HandleSync(msgHTTPMessageMany, echo).
		HandleAsync(msgHTTPMessageAsync, echo)

	for i := 0; i < 1000; i++ {
		body := []byte("sync-" + string(rune(i%26+'a')))
		resp := executeMessageRequestBytes(endpoint, http.MethodPost, "/sync-many", body)
		assertMessageResponse(t, resp, http.StatusOK, body)
	}
	for i := 0; i < 1000; i++ {
		body := []byte("async-" + string(rune(i%26+'a')))
		resp := executeMessageRequestBytes(endpoint, http.MethodPost, "/async-many", body)
		assertMessageResponse(t, resp, http.StatusOK, body)
	}
}

func TestMessageEndpointPayloadBoundaries(t *testing.T) {
	endpoint := NewMessage(Config{})
	defer shutdownMessageEndpoint(t, endpoint)

	endpoint.Post("/payload", msgHTTPMessageMany).
		HandleSync(msgHTTPMessageMany, func(ctx core.Context) error {
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})

	cases := [][]byte{
		nil,
		[]byte{0x00, 0x01, 0x02, 0xfe, 0xff},
		bytes.Repeat([]byte{0x5a}, 64*1024),
	}
	for _, body := range cases {
		resp := executeMessageRequestBytes(endpoint, http.MethodPost, "/payload", body)
		assertMessageResponse(t, resp, http.StatusOK, body)
	}
}

func TestMessageEndpointSyncQueueFullReturnsCoreError(t *testing.T) {
	endpoint := NewMessage(Config{}, core.RouterConfig{
		SyncQueueSize:         1,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 16,
	})
	started := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseFirst)
		})
	}
	defer release()
	defer shutdownMessageEndpoint(t, endpoint)

	endpoint.Post("/queue", msgHTTPMessageQueue).
		HandleSync(msgHTTPMessageQueue, func(ctx core.Context) error {
			if string(ctx.Message().Data) == "first" {
				close(started)
				<-releaseFirst
			}
			return ctx.Reply(msgHTTPMessageQueue, ctx.Message().Data)
		})

	firstRespCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstRespCh <- executeMessageRequest(endpoint, http.MethodPost, "/queue", "first")
	}()
	waitForClosed(t, started, "first HTTPMessage sync handler did not start")

	const extraRequests = 8
	respCh := make(chan *httptest.ResponseRecorder, extraRequests)
	for i := 0; i < extraRequests; i++ {
		go func() {
			respCh <- executeMessageRequest(endpoint, http.MethodPost, "/queue", "extra")
		}()
	}

	sawQueueFull := false
	received := 0
	for i := 0; i < extraRequests; i++ {
		select {
		case resp := <-respCh:
			received++
			if resp.Code == http.StatusInternalServerError &&
				strings.Contains(resp.Body.String(), core.ErrDispatchQueueFull.Error()) {
				sawQueueFull = true
				i = extraRequests
			}
		case <-time.After(time.Second):
			i = extraRequests
		}
	}
	if !sawQueueFull {
		t.Fatal("did not observe sync queue-full error")
	}

	release()
	assertMessageResponse(t, <-firstRespCh, http.StatusOK, []byte("first"))
	for i := received; i < extraRequests; i++ {
		select {
		case <-respCh:
		case <-time.After(time.Second):
			t.Fatal("HTTPMessage sync queue response did not complete")
		}
	}
}

func TestMessageEndpointMaxInflightReturnsCoreError(t *testing.T) {
	endpoint := NewMessage(Config{}, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 1,
	})
	started := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseFirst)
		})
	}
	defer release()
	defer shutdownMessageEndpoint(t, endpoint)

	endpoint.Post("/inflight", msgHTTPMessageQueue).
		HandleSync(msgHTTPMessageQueue, func(ctx core.Context) error {
			close(started)
			<-releaseFirst
			return ctx.Reply(msgHTTPMessageQueue, ctx.Message().Data)
		})

	firstRespCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstRespCh <- executeMessageRequest(endpoint, http.MethodPost, "/inflight", "first")
	}()
	waitForClosed(t, started, "first HTTPMessage inflight handler did not start")

	errorResp := executeMessageRequest(endpoint, http.MethodPost, "/inflight", "second")
	assertMessageErrorContains(t, errorResp, core.ErrTooManyInflight.Error())

	release()
	assertMessageResponse(t, <-firstRespCh, http.StatusOK, []byte("first"))
}

func TestMessageEndpointAsyncQueueFullReturnsCoreError(t *testing.T) {
	endpoint := NewMessage(Config{}, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        1,
		MaxInflightPerSession: 16,
	})
	started := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseFirst)
		})
	}
	defer release()
	defer shutdownMessageEndpoint(t, endpoint)

	endpoint.Post("/async-queue", msgHTTPMessageAsync).
		HandleAsync(msgHTTPMessageAsync, func(ctx core.Context) error {
			if string(ctx.Message().Data) == "first" {
				close(started)
				<-releaseFirst
			}
			return ctx.Reply(msgHTTPMessageAsync, ctx.Message().Data)
		})

	firstRespCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstRespCh <- executeMessageRequest(endpoint, http.MethodPost, "/async-queue", "first")
	}()
	waitForClosed(t, started, "first HTTPMessage async handler did not start")

	const extraRequests = 8
	respCh := make(chan *httptest.ResponseRecorder, extraRequests)
	for i := 0; i < extraRequests; i++ {
		go func() {
			respCh <- executeMessageRequest(endpoint, http.MethodPost, "/async-queue", "extra")
		}()
	}

	sawQueueFull := false
	received := 0
	for i := 0; i < extraRequests; i++ {
		select {
		case resp := <-respCh:
			received++
			if resp.Code == http.StatusInternalServerError &&
				strings.Contains(resp.Body.String(), core.ErrDispatchQueueFull.Error()) {
				sawQueueFull = true
				i = extraRequests
			}
		case <-time.After(time.Second):
			i = extraRequests
		}
	}
	if !sawQueueFull {
		t.Fatal("did not observe async queue-full error")
	}

	release()
	assertMessageResponse(t, <-firstRespCh, http.StatusOK, []byte("first"))
	for i := received; i < extraRequests; i++ {
		select {
		case <-respCh:
		case <-time.After(time.Second):
			t.Fatal("HTTPMessage async queue response did not complete")
		}
	}
}

func TestMessageEndpointRouterClosedReturnsCoreError(t *testing.T) {
	endpoint := NewMessage(Config{})
	defer shutdownMessageEndpoint(t, endpoint)
	endpoint.Post("/closed", msgHTTPMessageClosed).
		HandleSync(msgHTTPMessageClosed, func(ctx core.Context) error {
			return ctx.Reply(msgHTTPMessageClosed, ctx.Message().Data)
		})
	endpoint.router.Close()

	resp := executeMessageRequest(endpoint, http.MethodPost, "/closed", "closed")
	assertMessageErrorContains(t, resp, core.ErrRouterClosed.Error())
}

func executeMessageRequest(endpoint *MessageEndpoint, method, path, body string) *httptest.ResponseRecorder {
	return executeMessageRequestBytes(endpoint, method, path, []byte(body))
}

func executeMessageRequestBytes(endpoint *MessageEndpoint, method, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	endpoint.endpoint.serve(recorder, req)
	return recorder
}

func assertMessageResponse(t *testing.T, resp *httptest.ResponseRecorder, status int, body []byte) {
	t.Helper()
	if resp.Code != status {
		t.Fatalf("status = %d, want %d; body=%q", resp.Code, status, resp.Body.String())
	}
	if !bytes.Equal(resp.Body.Bytes(), body) {
		t.Fatalf("body = %x, want %x", resp.Body.Bytes(), body)
	}
}

func assertMessageErrorContains(t *testing.T, resp *httptest.ResponseRecorder, text string) {
	t.Helper()
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusInternalServerError, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), text) {
		t.Fatalf("body = %q, want %q", resp.Body.String(), text)
	}
}

func shutdownMessageEndpoint(t *testing.T, endpoint *MessageEndpoint) {
	t.Helper()
	if err := endpoint.Shutdown(nil); err != nil {
		t.Fatalf("shutdown endpoint: %v", err)
	}
}

func waitForClosed(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
