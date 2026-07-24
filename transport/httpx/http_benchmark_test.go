package httpx

import (
	"net/http"
	"testing"

	"github.com/saramand9/kpro/core"
)

func BenchmarkEndpointServeHTTP(b *testing.B) {
	benchmarks := []struct {
		name       string
		middleware core.Middleware
	}{
		{name: "direct"},
		{
			name: "middleware",
			middleware: func(next core.InvocationHandler) core.InvocationHandler {
				return func(inv core.Invocation) error {
					return next(inv)
				}
			},
		},
		{
			name: "middleware_headers_meta",
			middleware: func(next core.InvocationHandler) core.InvocationHandler {
				return func(inv core.Invocation) error {
					_ = inv.Headers()
					_ = inv.Meta()
					return next(inv)
				}
			},
		},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			endpoint := New(Config{})
			if benchmark.middleware != nil {
				endpoint.Use(benchmark.middleware)
			}
			endpoint.Get("/benchmark", func(ctx *Context) error {
				return ctx.Text(http.StatusOK, "ok")
			})
			req, err := http.NewRequest(http.MethodGet, "http://example.com/benchmark", nil)
			if err != nil {
				b.Fatal(err)
			}
			writer := &discardHTTPResponseWriter{header: make(http.Header)}

			b.ReportAllocs()
			b.ResetTimer()
			for requestIndex := 0; requestIndex < b.N; requestIndex++ {
				endpoint.ServeHTTP(writer, req)
			}
		})
	}
}

type discardHTTPResponseWriter struct {
	header http.Header
}

func (w *discardHTTPResponseWriter) Header() http.Header {
	return w.header
}

func (w *discardHTTPResponseWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

func (*discardHTTPResponseWriter) WriteHeader(int) {}
