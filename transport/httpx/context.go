package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// Context is backed by net/http. Body caches the request body on first read so
// handlers can call Body and CopyBody in either order.
type Context struct {
	w           http.ResponseWriter
	r           *http.Request
	header      http.Header
	body        []byte
	bodyRead    bool
	bodyErr     error
	maxBody     int64
	bodyLimited bool
	status      int
	wroteHeader bool
}

// Context returns the request cancellation and deadline context.
func (c *Context) Context() context.Context {
	if c == nil || c.r == nil {
		return context.Background()
	}
	return c.r.Context()
}

// Request exposes the underlying request for protocol-specific integrations.
// It must not be retained after the handler returns.
func (c *Context) Request() *http.Request {
	if c == nil {
		return nil
	}
	return c.r
}

// ResponseWriter exposes the underlying writer for protocol-specific features
// such as streaming and HTTP connection control.
func (c *Context) ResponseWriter() http.ResponseWriter {
	if c == nil {
		return nil
	}
	return c.w
}

func (c *Context) Header() http.Header {
	if c.header == nil {
		c.header = c.w.Header()
	}
	return c.header
}

func (c *Context) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	// Unspecified response types must not be inferred from untrusted bytes.
	// Applications serving HTML must select that type and escape their input.
	header := c.w.Header()
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/octet-stream")
	}
	if header.Get("X-Content-Type-Options") != "nosniff" {
		header.Set("X-Content-Type-Options", "nosniff")
	}
	c.wroteHeader = true
	c.status = status
	c.w.WriteHeader(status)
}

func (c *Context) Write(body []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return c.w.Write(body)
}

func (c *Context) setContentType(contentType string) {
	if contentType == "" {
		return
	}
	c.w.Header().Set("Content-Type", contentType)
}

func (c *Context) Body() ([]byte, error) {
	if c.bodyRead {
		return c.body, c.bodyErr
	}
	c.bodyRead = true
	if c.r.Body == nil {
		return nil, nil
	}
	if c.maxBody > 0 && !c.bodyLimited {
		// Install the limiting reader on the request itself so every internal
		// body consumer shares one budget. Body also caches terminal errors,
		// preventing repeated calls from applying a fresh budget to the unread
		// remainder of an oversized or chunked request.
		c.r.Body = http.MaxBytesReader(c.w, c.r.Body, c.maxBody)
		c.bodyLimited = true
	}
	body, err := io.ReadAll(c.r.Body)
	if err != nil {
		c.bodyErr = err
		return nil, c.bodyErr
	}
	c.body = body
	return c.body, nil
}

func (c *Context) Method() string {
	return c.r.Method
}

func (c *Context) MethodBytes() []byte {
	return []byte(c.r.Method)
}

func (c *Context) Path() string {
	if c.r.URL == nil {
		return ""
	}
	return c.r.URL.Path
}

func (c *Context) PathBytes() []byte {
	return []byte(c.Path())
}

func (c *Context) RawQuery() string {
	if c.r.URL == nil {
		return ""
	}
	return c.r.URL.RawQuery
}

func (c *Context) Host() string {
	return c.r.Host
}

func (c *Context) ContentLength() int {
	if c.r.ContentLength > 0 {
		return int(c.r.ContentLength)
	}
	body, err := c.Body()
	if err != nil {
		return 0
	}
	return len(body)
}

func (c *Context) Bytes(status int, contentType string, body []byte) error {
	c.setContentType(contentType)
	c.WriteHeader(status)
	if len(body) == 0 {
		return nil
	}
	_, err := c.w.Write(body)
	return err
}

func (c *Context) Stream(status int, contentType string, reader io.Reader) error {
	c.setContentType(contentType)
	c.WriteHeader(status)
	if reader == nil {
		return nil
	}
	_, err := io.Copy(c.w, reader)
	return err
}

// CopyBody copies the current request body into the response.
// Use Body directly only when the handler consumes the data before returning.
func (c *Context) CopyBody(status int, contentType string) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	c.setContentType(contentType)
	c.WriteHeader(status)
	if len(body) == 0 {
		return nil
	}
	_, err = c.w.Write(body)
	return err
}

func (c *Context) Text(status int, text string) error {
	c.setContentType("text/plain; charset=utf-8")
	c.WriteHeader(status)
	_, err := io.WriteString(c.w, text)
	return err
}

func (c *Context) JSON(status int, value interface{}) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.setContentType("application/json; charset=utf-8")
	c.WriteHeader(status)
	_, err = c.w.Write(body)
	return err
}

// JSONBody decodes the cached request body into dest.
func (c *Context) JSONBody(dest any) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dest)
}

func (c *Context) Query(key string) string {
	if c.r.URL == nil {
		return ""
	}
	return c.r.URL.Query().Get(key)
}

// Param returns a wildcard captured by a registered ServeMux route pattern,
// for example "id" from "/users/{id}".
func (c *Context) Param(name string) string {
	if c == nil || c.r == nil {
		return ""
	}
	return c.r.PathValue(name)
}
