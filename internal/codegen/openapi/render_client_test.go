package openapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const generatedClientSpec = `openapi: 3.0.3
info:
  title: Widget Client
  version: 1.0.0
paths:
  /widgets/{slug}:
    post:
      operationId: createWidget
      parameters:
        - name: slug
          in: path
          required: true
          schema: { type: string }
        - name: verbose
          in: query
          schema: { type: boolean }
        - name: filter%
          in: query
          schema: { type: string }
        - name: score
          in: query
          schema: { type: number }
        - name: X-Trace
          in: header
          schema: { type: string }
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/WidgetInput' }
      responses:
        '201':
          description: created widget
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Widget' }
    delete:
      operationId: deleteWidget
      parameters:
        - name: slug
          in: path
          required: true
          schema: { type: string }
      responses:
        '204': { description: deleted }
components:
  schemas:
    WidgetInput:
      type: object
      required: [name]
      properties:
        name: { type: string }
    Widget:
      type: object
      required: [slug, name]
      properties:
        slug: { type: string }
        name: { type: string }
`

func TestGeneratedHTTPClientRuntime(t *testing.T) {
	document, err := Parse([]byte(generatedClientSpec))
	if err != nil {
		t.Fatalf("parse client spec: %v", err)
	}
	plan, err := BuildPlan(document, Options{Module: "example.com/generated", Output: "internal/widgetapi"})
	if err != nil {
		t.Fatalf("build client plan: %v", err)
	}

	directory := t.TempDir()
	for _, file := range plan.Files {
		target := filepath.Join(directory, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, file.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repository, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	module := "module example.com/generated\n\ngo 1.25.12\n\nrequire github.com/saramand9/kpro v0.0.0\n\nreplace github.com/saramand9/kpro => " + filepath.ToSlash(repository) + "\n"
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(directory, "internal", "widgetapi", "kprohttpclient", "client_test.go")
	if err := os.WriteFile(testFile, []byte(generatedHTTPClientTest), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated HTTP client tests failed: %v\n%s", err, output)
	}
}

const generatedHTTPClientTest = `package kprohttpclient

import (
    "context"
    "errors"
    "io"
    "math"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "testing"

    contract "example.com/generated/internal/widgetapi/contract"
    server "example.com/generated/internal/widgetapi/kprohttp"
    types "example.com/generated/internal/widgetapi/types"
    "github.com/saramand9/kpro/transport/httpx"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) {
    return function(request)
}

type pointerDoer struct{}

func (*pointerDoer) Do(*http.Request) (*http.Response, error) { return nil, nil }

type pointerBody struct{}

func (*pointerBody) Read([]byte) (int, error) { panic("typed-nil body was read") }
func (*pointerBody) Close() error            { panic("typed-nil body was closed") }

type trackedBody struct {
    io.Reader
    closed bool
}

type interruptedBody struct {
    data []byte
    cause error
    read bool
    closed bool
}

func (body *interruptedBody) Read(target []byte) (int, error) {
    if !body.read {
        body.read = true
        return copy(target, body.data), nil
    }
    return 0, body.cause
}

func (body *interruptedBody) Close() error {
    body.closed = true
    return nil
}

type finiteAPI struct{ calls int }

func (api *finiteAPI) CreateWidget(_ context.Context, params contract.CreateWidgetParams, request types.WidgetInput) (types.Widget, error) {
    api.calls++
    return types.Widget{Slug: params.Slug, Name: request.Name}, nil
}

func (api *finiteAPI) DeleteWidget(context.Context, contract.DeleteWidgetParams) error {
    api.calls++
    return nil
}

func (body *trackedBody) Close() error {
    body.closed = true
    return nil
}

func TestClientBindsTypedRequestAndResponse(t *testing.T) {
    calls := 0
    executor := doerFunc(func(request *http.Request) (*http.Response, error) {
        calls++
        switch calls {
        case 1:
            if request.Method != http.MethodPost || request.URL.EscapedPath() != "/gateway/widgets/a%2Fb" {
                t.Fatalf("create target = %s %s", request.Method, request.URL.EscapedPath())
            }
            if request.URL.Query().Get("verbose") != "true" || request.URL.Query().Get("score") != "1.5" || request.Header.Get("X-Trace") != "trace-1" {
                t.Fatalf("create bindings = query %q header %q", request.URL.RawQuery, request.Header.Get("X-Trace"))
            }
            if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "application/json" {
                t.Fatalf("create media types = Content-Type %q Accept %q", request.Header.Get("Content-Type"), request.Header.Get("Accept"))
            }
            body, err := io.ReadAll(request.Body)
            if err != nil || string(body) != ` + "`" + `{"name":"Milo"}` + "`" + ` {
                t.Fatalf("create body = %q, %v", body, err)
            }
            return &http.Response{
                StatusCode: http.StatusCreated,
                Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
                Body: io.NopCloser(strings.NewReader(` + "`" + `{"slug":"a/b","name":"Milo"}` + "`" + `)),
            }, nil
        case 2:
            if request.Method != http.MethodDelete || request.URL.EscapedPath() != "/gateway/widgets/a%2Fb" {
                t.Fatalf("delete target = %s %s", request.Method, request.URL.EscapedPath())
            }
            return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
        default:
            t.Fatalf("unexpected request %d", calls)
            return nil, nil
        }
    })
    client, err := New(Config{BaseURL: "https://example.test/gateway/", HTTPClient: executor, MaxResponseBytes: 1024})
    if err != nil { t.Fatal(err) }
    verbose := true
    score := 1.5
    trace := "trace-1"
    response, err := client.CreateWidget(context.Background(), contract.CreateWidgetParams{
        Slug: "a/b", Verbose: &verbose, Score: &score, XTrace: &trace,
    }, types.WidgetInput{Name: "Milo"})
    if err != nil || response.Slug != "a/b" || response.Name != "Milo" {
        t.Fatalf("CreateWidget = %#v, %v", response, err)
    }
    if err := client.DeleteWidget(context.Background(), contract.DeleteWidgetParams{Slug: "a/b"}); err != nil {
        t.Fatalf("DeleteWidget: %v", err)
    }
}

func TestClientBoundsErrorsAndClosesBodies(t *testing.T) {
    errorBody := &trackedBody{Reader: strings.NewReader("secret-value")}
    client, err := New(Config{
        BaseURL: "https://example.test",
        MaxResponseBytes: 6,
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            return &http.Response{
                StatusCode: http.StatusServiceUnavailable,
                Header: http.Header{"X-Request-Id": []string{"request-1"}},
                Body: errorBody,
            }, nil
        }),
    })
    if err != nil { t.Fatal(err) }
    _, err = client.CreateWidget(context.Background(), contract.CreateWidgetParams{Slug: "widget"}, types.WidgetInput{Name: "Milo"})
    var responseError *ResponseError
    if !errors.As(err, &responseError) {
        t.Fatalf("error = %T %v, want ResponseError", err, err)
    }
    if responseError.StatusCode != http.StatusServiceUnavailable || string(responseError.Body) != "secret" || !responseError.Truncated {
        t.Fatalf("ResponseError = %#v", responseError)
    }
    if responseError.Header.Get("X-Request-ID") != "request-1" || strings.Contains(err.Error(), "secret") {
        t.Fatalf("ResponseError metadata/error = %#v / %q", responseError.Header, err)
    }
    if !errorBody.closed { t.Fatal("error response body was not closed") }

    redirectBody := &trackedBody{Reader: strings.NewReader("redirect")}
    sentinel := errors.New("redirect rejected")
    redirectClient, err := New(Config{
        BaseURL: "https://example.test",
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            return &http.Response{StatusCode: http.StatusFound, Body: redirectBody}, sentinel
        }),
    })
    if err != nil { t.Fatal(err) }
    err = redirectClient.DeleteWidget(context.Background(), contract.DeleteWidgetParams{Slug: "widget"})
    if !errors.Is(err, sentinel) || !redirectBody.closed {
        t.Fatalf("response+error = %v, body closed %v", err, redirectBody.closed)
    }
}

func TestDefaultClientRejectsRedirectAndEscapesDotSegments(t *testing.T) {
    finalRequests := make(chan struct{}, 1)
    dotPaths := make(chan string, 1)
    mux := http.NewServeMux()
    mux.HandleFunc("POST /widgets/{slug}", func(writer http.ResponseWriter, request *http.Request) {
        switch request.PathValue("slug") {
        case "redirect":
            http.Redirect(writer, request, "/final", http.StatusFound)
        case ".":
            dotPaths <- request.URL.EscapedPath()
            writer.Header().Set("Content-Type", "application/json")
            writer.WriteHeader(http.StatusCreated)
            _, _ = io.WriteString(writer, "{\"slug\":\".\",\"name\":\"Milo\"}")
        default:
            http.NotFound(writer, request)
        }
    })
    mux.HandleFunc("/final", func(writer http.ResponseWriter, _ *http.Request) {
        finalRequests <- struct{}{}
        writer.WriteHeader(http.StatusCreated)
    })
    testServer := httptest.NewServer(mux)
    defer testServer.Close()

    client, err := New(Config{BaseURL: testServer.URL})
    if err != nil { t.Fatal(err) }
    _, err = client.CreateWidget(context.Background(), contract.CreateWidgetParams{Slug: "redirect"}, types.WidgetInput{Name: "Milo"})
    var responseError *ResponseError
    if !errors.As(err, &responseError) || responseError.StatusCode != http.StatusFound {
        t.Fatalf("redirect error = %T %v", err, err)
    }
    select {
    case <-finalRequests:
        t.Fatal("default HTTP client followed a redirect")
    default:
    }

    response, err := client.CreateWidget(context.Background(), contract.CreateWidgetParams{Slug: "."}, types.WidgetInput{Name: "Milo"})
    if err != nil || response.Slug != "." {
        t.Fatalf("dot segment request = %#v, %v", response, err)
    }
    select {
    case escapedPath := <-dotPaths:
        if escapedPath != "/widgets/%2E" { t.Fatalf("dot path = %q", escapedPath) }
    default:
        t.Fatal("dot segment request did not reach ServeMux route")
    }
}

func TestClientRejectsUnsafeParameterValuesBeforeDoer(t *testing.T) {
    calls := 0
    client, err := New(Config{
        BaseURL: "https://example.test",
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            calls++
            return nil, errors.New("unexpected request")
        }),
    })
    if err != nil { t.Fatal(err) }

    filter := ""
    _, err = client.CreateWidget(context.Background(), contract.CreateWidgetParams{Slug: "widget", Filter: &filter}, types.WidgetInput{Name: "Milo"})
    cause := errors.Unwrap(err)
    if cause == nil || !errors.Is(err, cause) || strings.Contains(err.Error(), "%!") || !strings.Contains(err.Error(), "filter%") {
        t.Fatalf("percent parameter error = %q, cause %v", err, cause)
    }
    if calls != 0 { t.Fatalf("empty percent parameter reached Doer %d times", calls) }

    trace := "safe\r\nX-Injected: true"
    _, err = client.CreateWidget(context.Background(), contract.CreateWidgetParams{Slug: "widget", XTrace: &trace}, types.WidgetInput{Name: "Milo"})
    if err == nil || !strings.Contains(err.Error(), "control character") || calls != 0 {
        t.Fatalf("unsafe header error = %v, calls = %d", err, calls)
    }

    for _, number := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
        number := number
        _, err = client.CreateWidget(context.Background(), contract.CreateWidgetParams{Slug: "widget", Score: &number}, types.WidgetInput{Name: "Milo"})
        if err == nil || !strings.Contains(err.Error(), "finite") || calls != 0 {
            t.Fatalf("non-finite client parameter %v = %v, calls = %d", number, err, calls)
        }
    }
}

func TestUnexpectedStatusPreservesInterruptedBodyError(t *testing.T) {
    sentinel := errors.New("body read interrupted")
    body := &interruptedBody{data: []byte("part"), cause: sentinel}
    client, err := New(Config{
        BaseURL: "https://example.test",
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            return &http.Response{
                StatusCode: http.StatusServiceUnavailable,
                Header: http.Header{"X-Request-Id": []string{"request-2"}},
                Body: body,
            }, nil
        }),
    })
    if err != nil { t.Fatal(err) }
    _, err = client.CreateWidget(context.Background(), contract.CreateWidgetParams{Slug: "widget"}, types.WidgetInput{Name: "Milo"})
    var responseError *ResponseError
    if !errors.As(err, &responseError) || !errors.Is(err, sentinel) {
        t.Fatalf("interrupted response error = %T %v", err, err)
    }
    if responseError.StatusCode != http.StatusServiceUnavailable || string(responseError.Body) != "part" || responseError.Cause != sentinel {
        t.Fatalf("interrupted ResponseError = %#v", responseError)
    }
    if responseError.Header.Get("X-Request-ID") != "request-2" || !body.closed {
        t.Fatalf("interrupted response metadata = %#v, body closed %v", responseError.Header, body.closed)
    }

    var typedNilBody *pointerBody
    bodylessClient, err := New(Config{
        BaseURL: "https://example.test",
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            return &http.Response{StatusCode: http.StatusBadGateway, Body: typedNilBody}, nil
        }),
    })
    if err != nil { t.Fatal(err) }
    err = bodylessClient.DeleteWidget(context.Background(), contract.DeleteWidgetParams{Slug: "widget"})
    responseError = nil
    if !errors.As(err, &responseError) || responseError.StatusCode != http.StatusBadGateway || responseError.Cause == nil || !errors.Is(err, responseError.Cause) {
        t.Fatalf("bodyless unexpected response = %T %#v", err, responseError)
    }

    successClient, err := New(Config{
        BaseURL: "https://example.test",
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            return &http.Response{StatusCode: http.StatusNoContent, Body: typedNilBody}, nil
        }),
    })
    if err != nil { t.Fatal(err) }
    if err := successClient.DeleteWidget(context.Background(), contract.DeleteWidgetParams{Slug: "widget"}); err == nil || !strings.Contains(err.Error(), "body is nil") {
        t.Fatalf("typed-nil successful response body = %v", err)
    }

    responseSentinel := errors.New("typed-nil response and error")
    responseAndErrorClient, err := New(Config{
        BaseURL: "https://example.test",
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            return &http.Response{StatusCode: http.StatusFound, Body: typedNilBody}, responseSentinel
        }),
    })
    if err != nil { t.Fatal(err) }
    if err := responseAndErrorClient.DeleteWidget(context.Background(), contract.DeleteWidgetParams{Slug: "widget"}); !errors.Is(err, responseSentinel) {
        t.Fatalf("typed-nil response+error = %v", err)
    }
}

func TestServerRejectsNonFiniteNumberParameters(t *testing.T) {
    api := &finiteAPI{}
    endpoint := httpx.New(httpx.Config{})
    if err := server.Register(endpoint, api); err != nil { t.Fatal(err) }

    for _, number := range []string{"NaN", "+Inf", "-Inf"} {
        target := "/widgets/widget?score=" + url.QueryEscape(number)
        request := httptest.NewRequest(http.MethodPost, target, strings.NewReader("{\"name\":\"Milo\"}"))
        request.Header.Set("Content-Type", "application/json")
        recorder := httptest.NewRecorder()
        endpoint.ServeHTTP(recorder, request)
        if recorder.Code != http.StatusBadRequest || api.calls != 0 {
            t.Fatalf("server number %q = status %d, calls %d", number, recorder.Code, api.calls)
        }
    }
}

func TestClientRejectsInvalidConfigurationAndResponses(t *testing.T) {
	var zero Client
	if err := zero.DeleteWidget(context.Background(), contract.DeleteWidgetParams{Slug: "widget"}); err == nil {
		t.Fatal("zero-value Client call succeeded")
	}

    for _, config := range []Config{
        {},
        {BaseURL: "relative/path"},
        {BaseURL: "ftp://example.test"},
        {BaseURL: "https://user@example.test"},
        {BaseURL: "https://example.test?query=1"},
        {BaseURL: "https://example.test/gateway//v1"},
        {BaseURL: "https://example.test/gateway/./v1"},
        {BaseURL: "https://example.test/gateway/../v1"},
        {BaseURL: "https://example.test/gateway/%2e/v1"},
        {BaseURL: "https://example.test/gateway/%2E%2E/v1"},
        {BaseURL: "https://example.test/gateway/%2Fv1"},
        {BaseURL: "https://example.test", MaxResponseBytes: -1},
    } {
        if _, err := New(config); err == nil { t.Fatalf("New(%#v) succeeded", config) }
    }
    var typedNil *pointerDoer
    if _, err := New(Config{BaseURL: "https://example.test", HTTPClient: typedNil}); err == nil {
        t.Fatal("New accepted a typed-nil HTTP client")
    }

    oversizedBody := &trackedBody{Reader: strings.NewReader(` + "`" + `{"slug":"widget","name":"Milo"}` + "`" + `)}
    client, err := New(Config{
        BaseURL: "https://example.test",
        MaxResponseBytes: 8,
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            return &http.Response{
                StatusCode: http.StatusCreated,
                Header: http.Header{"Content-Type": []string{"application/json"}},
                Body: oversizedBody,
            }, nil
        }),
    })
    if err != nil { t.Fatal(err) }
    _, err = client.CreateWidget(context.Background(), contract.CreateWidgetParams{Slug: "widget"}, types.WidgetInput{Name: "Milo"})
    if err == nil || !strings.Contains(err.Error(), "exceeds 8 bytes") || !oversizedBody.closed {
        t.Fatalf("oversized response = %v, body closed %v", err, oversizedBody.closed)
    }

    bodylessClient, err := New(Config{
        BaseURL: "https://example.test",
        HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
            return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("x"))}, nil
        }),
    })
    if err != nil { t.Fatal(err) }
    if err := bodylessClient.DeleteWidget(context.Background(), contract.DeleteWidgetParams{Slug: "widget"}); err == nil || !strings.Contains(err.Error(), "must not contain a body") {
        t.Fatalf("bodyless response error = %v", err)
    }
}
`
