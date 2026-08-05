package fasthttpx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/valyala/fasthttp"
)

// Context is the request-scoped KPro view of a fasthttp RequestCtx. It and all
// borrowed byte slices are valid only until the Handler returns.
type Context struct {
	native         *fasthttp.RequestCtx
	endpoint       *Endpoint
	requestContext context.Context
	values         map[string]any
	maxBody        int64
	status         int
	wroteHeader    bool
}

func (c *Context) initialize(endpoint *Endpoint, native *fasthttp.RequestCtx) {
	c.native = native
	c.endpoint = endpoint
	c.requestContext = endpoint.shutdownCtx
	c.maxBody = endpoint.config.MaxBodyBytes
	c.status = 0
	c.wroteHeader = false
}

func (c *Context) reset() {
	c.native = nil
	c.endpoint = nil
	c.requestContext = nil
	c.maxBody = 0
	c.status = 0
	c.wroteHeader = false
	for key := range c.values {
		delete(c.values, key)
	}
}

// Context returns the middleware-adjustable request context. Its base
// cancellation signal closes on endpoint shutdown, not peer disconnect.
func (c *Context) Context() context.Context {
	if c == nil || c.requestContext == nil {
		return context.Background()
	}
	return c.requestContext
}

// Native exposes fasthttp's request context for protocol-specific features.
// It must not be retained after the handler returns.
func (c *Context) Native() *fasthttp.RequestCtx {
	if c == nil {
		return nil
	}
	return c.native
}

// RequestHeader returns a copied request-header value as a string.
func (c *Context) RequestHeader(name string) string {
	if c == nil || c.native == nil {
		return ""
	}
	return string(c.native.Request.Header.Peek(name))
}

// RequestHeaderBytes returns a borrowed request-header value.
func (c *Context) RequestHeaderBytes(name string) []byte {
	if c == nil || c.native == nil {
		return nil
	}
	return c.native.Request.Header.Peek(name)
}

// SetResponseHeader replaces one response-header value.
func (c *Context) SetResponseHeader(name, value string) {
	if c == nil || c.native == nil {
		return
	}
	c.native.Response.Header.Set(name, value)
}

// AddResponseHeader appends one response-header value.
func (c *Context) AddResponseHeader(name, value string) {
	if c == nil || c.native == nil {
		return
	}
	c.native.Response.Header.Add(name, value)
}

// WriteHeader sets the response status. Repeated calls are ignored.
func (c *Context) WriteHeader(status int) {
	if c == nil || c.native == nil || c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.status = status
	c.native.SetStatusCode(status)
}

// Write appends response bytes.
func (c *Context) Write(body []byte) (int, error) {
	if c == nil || c.native == nil {
		return 0, nil
	}
	if !c.wroteHeader {
		c.WriteHeader(fasthttp.StatusOK)
	}
	return c.native.Write(body)
}

func (c *Context) setContentType(contentType string) {
	if c == nil || c.native == nil || contentType == "" {
		return
	}
	c.native.SetContentType(contentType)
}

// Body returns the borrowed request body. Copy it before retaining it beyond
// the handler.
func (c *Context) Body() ([]byte, error) {
	if c == nil || c.native == nil {
		return nil, nil
	}
	body := c.native.PostBody()
	if c.maxBody > 0 && int64(len(body)) > c.maxBody {
		return nil, &BodyTooLargeError{Size: int64(len(body)), Limit: c.maxBody}
	}
	return body, nil
}

func (c *Context) Method() string {
	if c == nil || c.native == nil {
		return ""
	}
	return string(c.native.Method())
}

// MethodBytes returns a borrowed request method.
func (c *Context) MethodBytes() []byte {
	if c == nil || c.native == nil {
		return nil
	}
	return c.native.Method()
}

func (c *Context) Path() string {
	if c == nil || c.native == nil {
		return ""
	}
	return string(c.native.Path())
}

// PathBytes returns a borrowed normalized request path.
func (c *Context) PathBytes() []byte {
	if c == nil || c.native == nil {
		return nil
	}
	return c.native.Path()
}

func (c *Context) RawQuery() string {
	if c == nil || c.native == nil {
		return ""
	}
	return string(c.native.URI().QueryString())
}

func (c *Context) Host() string {
	if c == nil || c.native == nil {
		return ""
	}
	return string(c.native.Host())
}

func (c *Context) ContentLength() int {
	if c == nil || c.native == nil {
		return 0
	}
	return len(c.native.PostBody())
}

func (c *Context) Bytes(status int, contentType string, body []byte) error {
	if c == nil || c.native == nil {
		return nil
	}
	c.setContentType(contentType)
	c.WriteHeader(status)
	c.native.SetBody(body)
	return nil
}

// Stream schedules reader as the response body. fasthttp consumes it after
// the handler returns, so the reader must remain valid and own its data.
func (c *Context) Stream(status int, contentType string, reader io.Reader) error {
	if c == nil || c.native == nil {
		return nil
	}
	c.setContentType(contentType)
	c.WriteHeader(status)
	if reader != nil {
		c.native.Response.SetBodyStream(reader, -1)
	}
	return nil
}

// CopyBody copies the request body into the response.
func (c *Context) CopyBody(status int, contentType string) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	return c.Bytes(status, contentType, body)
}

func (c *Context) Text(status int, value string) error {
	if c == nil || c.native == nil {
		return nil
	}
	c.setContentType("text/plain; charset=utf-8")
	c.WriteHeader(status)
	c.native.SetBodyString(value)
	return nil
}

func (c *Context) JSON(status int, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if c == nil || c.native == nil {
		return nil
	}
	c.setContentType("application/json; charset=utf-8")
	c.WriteHeader(status)
	// json.Marshal returns an owned buffer. Hand it to fasthttp without a
	// second copy; the caller has no reference that can mutate it.
	c.native.Response.SetBodyRaw(body)
	return nil
}

func (c *Context) JSONBody(destination any) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	return json.Unmarshal(body, destination)
}

func (c *Context) Query(name string) string {
	if c == nil || c.native == nil {
		return ""
	}
	return string(c.native.QueryArgs().Peek(name))
}

// QueryBytes returns a borrowed query-parameter value.
func (c *Context) QueryBytes(name string) []byte {
	if c == nil || c.native == nil {
		return nil
	}
	return c.native.QueryArgs().Peek(name)
}

// Param returns a path parameter captured from a {name} route segment.
func (c *Context) Param(name string) string {
	if c == nil || c.native == nil {
		return ""
	}
	value := c.native.UserValue(name)
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

// Set stores a request-scoped value for downstream middleware and handlers.
func (c *Context) Set(key string, value any) {
	if c == nil {
		return
	}
	if c.values == nil {
		c.values = make(map[string]any)
	}
	c.values[key] = value
}

// Get retrieves a request-scoped middleware value.
func (c *Context) Get(key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.values[key]
	return value, ok
}
