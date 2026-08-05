// Package fasthttpx provides an optional HTTP/1.1-native KPro endpoint backed
// by fasthttp.
//
// The package is intentionally separate from httpx. It does not implement
// net/http.Handler, does not expose *http.Request or http.ResponseWriter, and
// does not support HTTP/2. TLS endpoints advertise only the http/1.1 ALPN
// protocol. Keep httpx for standard-library interoperability and HTTP/2, and
// keep http3 for HTTP/3.
//
// Context and byte slices returned from it are borrowed for the duration of
// the handler. Applications must copy data that outlives the handler. The base
// Context cancellation signal closes when the endpoint shuts down; unlike
// net/http, fasthttp does not expose request cancellation on peer disconnect.
package fasthttpx
