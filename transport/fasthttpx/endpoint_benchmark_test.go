package fasthttpx

import (
	"net"
	"testing"

	"github.com/valyala/fasthttp"

	"github.com/saramand9/kpro/core"
)

func BenchmarkEndpointServeFastHTTP(b *testing.B) {
	benchmarks := []struct {
		name       string
		middleware core.Middleware
	}{
		{name: "direct"},
		{
			name: "middleware",
			middleware: func(next core.InvocationHandler) core.InvocationHandler {
				return func(invocation core.Invocation) error {
					return next(invocation)
				}
			},
		},
		{
			name: "middleware_headers_meta",
			middleware: func(next core.InvocationHandler) core.InvocationHandler {
				return func(invocation core.Invocation) error {
					_ = invocation.Headers()
					_ = invocation.Meta()
					return next(invocation)
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
				return ctx.Text(fasthttp.StatusOK, "ok")
			})
			var request fasthttp.Request
			request.Header.SetMethod(fasthttp.MethodGet)
			request.SetRequestURI("/benchmark")
			request.Header.SetHost("example.com")
			var native fasthttp.RequestCtx
			native.Init(&request, &net.TCPAddr{}, nil)

			b.ReportAllocs()
			b.ResetTimer()
			for requestIndex := 0; requestIndex < b.N; requestIndex++ {
				native.Response.Reset()
				endpoint.ServeFastHTTP(&native)
			}
		})
	}
}

func BenchmarkEndpointServeFastHTTPEcho1KiB(b *testing.B) {
	endpoint := New(Config{})
	endpoint.Post("/echo", func(ctx *Context) error {
		return ctx.CopyBody(fasthttp.StatusOK, "application/octet-stream")
	})
	var request fasthttp.Request
	request.Header.SetMethod(fasthttp.MethodPost)
	request.SetRequestURI("/echo")
	request.SetBodyRaw(make([]byte, 1024))
	var native fasthttp.RequestCtx
	native.Init(&request, &net.TCPAddr{}, nil)

	b.ReportAllocs()
	b.SetBytes(1024)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		native.Response.Reset()
		endpoint.ServeFastHTTP(&native)
	}
}
