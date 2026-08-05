package fasthttpx

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/valyala/fasthttp"

	"github.com/saramand9/kpro/core"
)

// RouteGroup registers routes below a shared literal prefix. Middleware added
// with Use applies to subsequently registered routes.
type RouteGroup struct {
	endpoint *Endpoint
	prefix   string

	mu          sync.RWMutex
	middlewares []core.Middleware
}

func (e *Endpoint) Group(prefix string, middlewares ...core.Middleware) *RouteGroup {
	group, err := e.GroupChecked(prefix, middlewares...)
	if err != nil {
		panic(err)
	}
	return group
}

func (e *Endpoint) GroupChecked(prefix string, middlewares ...core.Middleware) (*RouteGroup, error) {
	if e == nil {
		return nil, errors.New("fasthttp endpoint is nil")
	}
	normalized, err := normalizeGroupPrefix(prefix)
	if err != nil {
		return nil, err
	}
	if err := validateRouteMiddlewares(middlewares); err != nil {
		return nil, err
	}
	return &RouteGroup{
		endpoint:    e,
		prefix:      normalized,
		middlewares: append([]core.Middleware(nil), middlewares...),
	}, nil
}

func (g *RouteGroup) Use(middlewares ...core.Middleware) *RouteGroup {
	if err := validateRouteMiddlewares(middlewares); err != nil {
		panic(err)
	}
	g.mu.Lock()
	g.middlewares = append(g.middlewares, middlewares...)
	g.mu.Unlock()
	return g
}

func (g *RouteGroup) Group(prefix string, middlewares ...core.Middleware) *RouteGroup {
	if g == nil || g.endpoint == nil {
		panic("fasthttp route group is nil")
	}
	normalized, err := normalizeGroupPrefix(prefix)
	if err != nil {
		panic(err)
	}
	if err := validateRouteMiddlewares(middlewares); err != nil {
		panic(err)
	}
	return &RouteGroup{
		endpoint:    g.endpoint,
		prefix:      joinRoutePath(g.prefix, normalized),
		middlewares: g.middlewareSnapshot(middlewares),
	}
}

func (g *RouteGroup) Handle(method, path string, handler Handler, middlewares ...core.Middleware) *RouteGroup {
	if err := g.HandleChecked(method, path, handler, middlewares...); err != nil {
		panic(err)
	}
	return g
}

func (g *RouteGroup) HandleChecked(method, path string, handler Handler, middlewares ...core.Middleware) error {
	if g == nil || g.endpoint == nil {
		return errors.New("fasthttp route group is nil")
	}
	if err := validateRouteMiddlewares(middlewares); err != nil {
		return err
	}
	return g.endpoint.HandleChecked(
		method,
		joinRoutePath(g.prefix, path),
		handler,
		g.middlewareSnapshot(middlewares)...,
	)
}

func (g *RouteGroup) Get(path string, handler Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(fasthttp.MethodGet, path, handler, middlewares...)
}

func (g *RouteGroup) Head(path string, handler Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(fasthttp.MethodHead, path, handler, middlewares...)
}

func (g *RouteGroup) Post(path string, handler Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(fasthttp.MethodPost, path, handler, middlewares...)
}

func (g *RouteGroup) Put(path string, handler Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(fasthttp.MethodPut, path, handler, middlewares...)
}

func (g *RouteGroup) Patch(path string, handler Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(fasthttp.MethodPatch, path, handler, middlewares...)
}

func (g *RouteGroup) Delete(path string, handler Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(fasthttp.MethodDelete, path, handler, middlewares...)
}

func (g *RouteGroup) Options(path string, handler Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(fasthttp.MethodOptions, path, handler, middlewares...)
}

func (g *RouteGroup) middlewareSnapshot(routeMiddlewares []core.Middleware) []core.Middleware {
	g.mu.RLock()
	middlewares := make([]core.Middleware, 0, len(g.middlewares)+len(routeMiddlewares))
	middlewares = append(middlewares, g.middlewares...)
	g.mu.RUnlock()
	return append(middlewares, routeMiddlewares...)
}

func validateRouteMiddlewares(middlewares []core.Middleware) error {
	for index, middleware := range middlewares {
		if middleware == nil {
			return fmt.Errorf("fasthttp route middleware %d is nil", index)
		}
	}
	return nil
}

func normalizeGroupPrefix(prefix string) (string, error) {
	if prefix == "" || prefix == "/" {
		return "", nil
	}
	if !strings.HasPrefix(prefix, "/") {
		return "", fmt.Errorf("fasthttp route group prefix %q must start with /", prefix)
	}
	if strings.ContainsAny(prefix, "{}?#") {
		return "", fmt.Errorf("fasthttp route group prefix %q must be a literal path", prefix)
	}
	return strings.TrimRight(prefix, "/"), nil
}

func joinRoutePath(prefix, path string) string {
	if prefix == "" {
		if path == "" {
			return "/"
		}
		if strings.HasPrefix(path, "/") {
			return path
		}
		return "/" + path
	}
	if path == "" {
		return prefix
	}
	if strings.HasPrefix(path, "/") {
		return prefix + path
	}
	return prefix + "/" + path
}
