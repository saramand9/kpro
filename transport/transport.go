package transport

import (
	"context"

	"github.com/saramand9/bignet/kpro/core"
)

type Server interface {
	Name() string
	ListenAndServe(context.Context, core.Handler) error
	Shutdown(context.Context) error
}

type Runner interface {
	Name() string
	ListenAndServe(context.Context) error
	Shutdown(context.Context) error
}
