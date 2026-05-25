package core

import "sync"

type Router struct {
	mu          sync.RWMutex
	handlers    map[uint32]Handler
	middlewares []Middleware
}

func NewRouter() *Router {
	return &Router{
		handlers: make(map[uint32]Handler),
	}
}

func (r *Router) Use(middlewares ...Middleware) {
	r.mu.Lock()
	r.middlewares = append(r.middlewares, middlewares...)
	r.mu.Unlock()
}

func (r *Router) Handle(msgID uint32, h Handler) {
	r.mu.Lock()
	r.handlers[msgID] = h
	r.mu.Unlock()
}

func (r *Router) Dispatch(ctx Context) error {
	msg := ctx.Message()

	r.mu.RLock()
	h := r.handlers[msg.ID]
	middlewares := append([]Middleware(nil), r.middlewares...)
	r.mu.RUnlock()

	if h == nil {
		_ = ctx.Error(ErrNoHandler.Error())
		return ErrNoHandler
	}
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	if err := h(ctx); err != nil {
		_ = ctx.Error(err.Error())
		return err
	}
	return nil
}
