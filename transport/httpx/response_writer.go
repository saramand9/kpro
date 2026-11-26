package httpx

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/felixge/httpsnoop"
)

// A protocol adapter may restore additional native capabilities on the tracked
// writer. Taking over a protocol stream transfers ownership of the response.
type nativeResponseExtensions interface {
	WithResponseTracking(tracked http.ResponseWriter, takeover func()) http.ResponseWriter
}

func (c *Context) trackResponseWriter() http.ResponseWriter {
	commit := func(status int) {
		if !c.wroteHeader && (status == http.StatusSwitchingProtocols || status >= 200) {
			c.wroteHeader, c.status = true, status
		}
	}
	takeover := func() { c.wroteHeader = true }
	w := httpsnoop.Wrap(c.w, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				next(code)
				commit(code)
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(b []byte) (int, error) {
				n, err := next(b)
				commit(http.StatusOK)
				return n, err
			}
		},
		WriteString: func(next httpsnoop.WriteStringFunc) httpsnoop.WriteStringFunc {
			return func(s string) (int, error) {
				n, err := next(s)
				commit(http.StatusOK)
				return n, err
			}
		},
		ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
			return func(r io.Reader) (int64, error) {
				n, err := next(r)
				if n > 0 {
					commit(http.StatusOK)
				}
				return n, err
			}
		},
		Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
			return func() {
				next()
				commit(http.StatusOK)
			}
		},
		FlushError: func(next httpsnoop.FlushErrorFunc) httpsnoop.FlushErrorFunc {
			return func() error {
				err := next()
				if !errors.Is(err, http.ErrNotSupported) {
					commit(http.StatusOK)
				}
				return err
			}
		},
		Hijack: func(next httpsnoop.HijackFunc) httpsnoop.HijackFunc {
			return func() (net.Conn, *bufio.ReadWriter, error) {
				conn, rw, err := next()
				if err == nil {
					takeover()
				}
				return conn, rw, err
			}
		},
	})
	if native, ok := c.w.(nativeResponseExtensions); ok {
		return native.WithResponseTracking(w, takeover)
	}
	return w
}
