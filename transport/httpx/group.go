package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/saramand9/kpro/core"
)

// RouteGroup registers routes below a shared path prefix and middleware set.
// Middleware added with Use applies to routes registered after that call.
type RouteGroup struct {
	endpoint *Endpoint
	prefix   string

	mu          sync.RWMutex
	middlewares []core.Middleware
}

// Group creates a route group. Invalid prefixes or nil middleware panic; use
// GroupChecked when the values come from configuration or plugins.
func (e *Endpoint) Group(prefix string, middlewares ...core.Middleware) *RouteGroup {
	group, err := e.GroupChecked(prefix, middlewares...)
	if err != nil {
		panic(err)
	}
	return group
}

// GroupChecked creates a route group without mutating the endpoint.
func (e *Endpoint) GroupChecked(prefix string, middlewares ...core.Middleware) (*RouteGroup, error) {
	if e == nil {
		return nil, errors.New("http endpoint is nil")
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

// Use appends middleware for routes subsequently registered through the group.
func (g *RouteGroup) Use(middlewares ...core.Middleware) *RouteGroup {
	if err := validateRouteMiddlewares(middlewares); err != nil {
		panic(err)
	}
	g.mu.Lock()
	g.middlewares = append(g.middlewares, middlewares...)
	g.mu.Unlock()
	return g
}

// Group creates a nested route group and snapshots the parent's middleware.
func (g *RouteGroup) Group(prefix string, middlewares ...core.Middleware) *RouteGroup {
	if g == nil || g.endpoint == nil {
		panic("http route group is nil")
	}
	normalized, err := normalizeGroupPrefix(prefix)
	if err != nil {
		panic(err)
	}
	if err := validateRouteMiddlewares(middlewares); err != nil {
		panic(err)
	}
	combined := g.middlewareSnapshot(middlewares)
	return &RouteGroup{
		endpoint:    g.endpoint,
		prefix:      joinRoutePath(g.prefix, normalized),
		middlewares: combined,
	}
}

// Handle registers a method and path below the group prefix.
func (g *RouteGroup) Handle(method, path string, h Handler, middlewares ...core.Middleware) *RouteGroup {
	if err := g.HandleChecked(method, path, h, middlewares...); err != nil {
		panic(err)
	}
	return g
}

// HandleChecked registers a grouped route and reports invalid registrations.
func (g *RouteGroup) HandleChecked(method, path string, h Handler, middlewares ...core.Middleware) error {
	if g == nil || g.endpoint == nil {
		return errors.New("http route group is nil")
	}
	if err := validateRouteMiddlewares(middlewares); err != nil {
		return err
	}
	return g.endpoint.HandleChecked(
		method,
		joinRoutePath(g.prefix, path),
		h,
		g.middlewareSnapshot(middlewares)...,
	)
}

// Get registers a GET route below the group prefix.
func (g *RouteGroup) Get(path string, h Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(http.MethodGet, path, h, middlewares...)
}

// Post registers a POST route below the group prefix.
func (g *RouteGroup) Post(path string, h Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(http.MethodPost, path, h, middlewares...)
}

// Put registers a PUT route below the group prefix.
func (g *RouteGroup) Put(path string, h Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(http.MethodPut, path, h, middlewares...)
}

// Delete registers a DELETE route below the group prefix.
func (g *RouteGroup) Delete(path string, h Handler, middlewares ...core.Middleware) *RouteGroup {
	return g.Handle(http.MethodDelete, path, h, middlewares...)
}

func (g *RouteGroup) middlewareSnapshot(routeMiddlewares []core.Middleware) []core.Middleware {
	g.mu.RLock()
	middlewares := make([]core.Middleware, 0, len(g.middlewares)+len(routeMiddlewares))
	middlewares = append(middlewares, g.middlewares...)
	g.mu.RUnlock()
	middlewares = append(middlewares, routeMiddlewares...)
	return middlewares
}

func validateRouteMiddlewares(middlewares []core.Middleware) error {
	for index, middleware := range middlewares {
		if middleware == nil {
			return fmt.Errorf("http route middleware %d is nil", index)
		}
	}
	return nil
}

func normalizeGroupPrefix(prefix string) (string, error) {
	if prefix == "" || prefix == "/" {
		return "", nil
	}
	if !strings.HasPrefix(prefix, "/") {
		return "", fmt.Errorf("http route group prefix %q must start with /", prefix)
	}
	if strings.ContainsAny(prefix, "{}?#") {
		return "", fmt.Errorf("http route group prefix %q must be a literal path", prefix)
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
