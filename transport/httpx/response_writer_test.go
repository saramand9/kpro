package httpx

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeResponseFollowedByHandlerError(t *testing.T) {
	late := errors.New("late handler error")
	endpoint := New(Config{})
	endpoint.Get("/{operation}", func(ctx *Context) error {
		w := ctx.ResponseWriter()
		_, canHijack := w.(http.Hijacker)
		if canHijack != (ctx.Request().ProtoMajor == 1) {
			t.Error("native writer changed hijacking availability")
		}
		switch ctx.Param("operation") {
		case "accepted":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "accepted")
		case "implicit":
			_, _ = w.Write([]byte("already sent"))
		case "header":
			w.Header().Set("X-Application", "ready")
		case "hints":
			w.WriteHeader(http.StatusEarlyHints)
		case "flush":
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("flush: %v", err)
			}
		case "copy", "empty":
			body := ""
			if ctx.Param("operation") == "copy" {
				body = "copied"
			}
			// Expose only Read so io.Copy can exercise the writer's ReadFrom.
			_, err := io.Copy(w, struct{ io.Reader }{strings.NewReader(body)})
			if err != nil {
				t.Errorf("copy: %v", err)
			}
		case "mixed":
			w.WriteHeader(http.StatusCreated)
			_ = ctx.Text(http.StatusAccepted, "created")
		}
		return late
	})
	cases := []struct {
		path string
		code int
		body string
	}{
		{"accepted", 202, "accepted"}, {"implicit", 200, "already sent"},
		{"header", 500, "internal error"}, {"hints", 500, "internal error"},
		{"flush", 200, ""}, {"copy", 200, "copied"}, {"empty", 500, "internal error"},
		{"mixed", 201, "created"},
	}
	for _, version := range []string{"http1", "http2"} {
		t.Run(version, func(t *testing.T) {
			srv := httptest.NewUnstartedServer(endpoint)
			srv.EnableHTTP2 = version == "http2"
			srv.StartTLS()
			defer srv.Close()
			for _, tc := range cases {
				t.Run(tc.path, func(t *testing.T) {
					resp, err := srv.Client().Get(srv.URL + "/" + tc.path)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					b, err := io.ReadAll(resp.Body)
					if (resp.ProtoMajor == 2) != srv.EnableHTTP2 || err != nil || resp.StatusCode != tc.code || string(b) != tc.body {
						t.Fatalf("%s returned (%d, %q, %v), want (%d, %q)", resp.Proto, resp.StatusCode, b, err, tc.code, tc.body)
					}
				})
			}
		})
	}
}

type hijackRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
	err  error
}

func (w hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, nil, w.err
}

func TestNativeHijackTransfersResponseOnlyOnSuccess(t *testing.T) {
	for _, cause := range []error{nil, errors.New("takeover unavailable")} {
		conn, peer := net.Pipe()
		w := hijackRecorder{httptest.NewRecorder(), conn, cause}
		serveRoute(w, httptest.NewRequest(http.MethodGet, "/", nil), 0, func(ctx *Context) error {
			got, _, err := http.NewResponseController(ctx.ResponseWriter()).Hijack()
			if got != conn || !errors.Is(err, cause) {
				t.Errorf("hijack = (%v, %v)", got, err)
			}
			return errors.New("handler failed")
		})
		_ = conn.Close()
		_ = peer.Close()
		if cause == nil && w.Body.Len() != 0 || cause != nil && (w.Code != 500 || w.Body.String() != "internal error") {
			t.Fatalf("hijack error %v produced status %d and body %q", cause, w.Code, w.Body.String())
		}
	}
}
