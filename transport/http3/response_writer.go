package http3

import (
	"net/http"
	"time"

	qh3 "github.com/quic-go/quic-go/http3"
)

type streamResponseWriter interface {
	http.ResponseWriter
	http.Flusher
	FlushError() error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type streamExtensions interface {
	qh3.HTTPStreamer
	qh3.Settingser
}

type nativeStreamWriter struct {
	streamResponseWriter
	streamExtensions
}

func (w nativeStreamWriter) Unwrap() http.ResponseWriter { return w.streamResponseWriter }

func (w nativeStreamWriter) WithResponseTracking(tracked http.ResponseWriter, takeover func()) http.ResponseWriter {
	return nativeStreamWriter{tracked.(streamResponseWriter), trackedStream{w.streamExtensions, takeover}}
}

type trackedStream struct {
	streamExtensions
	takeover func()
}

func (w trackedStream) HTTPStream() *qh3.Stream {
	stream := w.streamExtensions.HTTPStream()
	w.takeover()
	return stream
}
