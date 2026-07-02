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
	maxBody     int64
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
		return c.body, nil
	}
	if c.r.Body == nil {
		c.bodyRead = true
		return nil, nil
	}
	reader := c.r.Body
	if c.maxBody > 0 {
		reader = http.MaxBytesReader(c.w, c.r.Body, c.maxBody)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	c.body = body
	c.bodyRead = true
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
	copied := make([]byte, len(body))
	copy(copied, body)
	_, err = c.w.Write(copied)
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

func (c *Context) Query(key string) string {
	if c.r.URL == nil {
		return ""
	}
	return c.r.URL.Query().Get(key)
}
