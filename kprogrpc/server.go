package kprogrpc

import (
	"errors"
	"net"
	"sync"

	"google.golang.org/grpc"
)

type Option func(*Server)

type Server struct {
	addr string
	grpc *grpc.Server

	mu sync.Mutex
	ln net.Listener
}

func NewServer(addr string, opts ...Option) *Server {
	s := &Server{addr: addr}
	for _, opt := range opts {
		opt(s)
	}
	if s.grpc == nil {
		s.grpc = grpc.NewServer()
	}
	return s
}

func WithServerOptions(opts ...grpc.ServerOption) Option {
	return func(s *Server) {
		s.grpc = grpc.NewServer(opts...)
	}
}

func (s *Server) RegisterService(desc *grpc.ServiceDesc, impl any) {
	s.grpc.RegisterService(desc, impl)
}

func (s *Server) Serve() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	return s.ServeListener(ln)
}

func (s *Server) ServeListener(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	err := s.grpc.Serve(ln)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

func (s *Server) GRPCServer() *grpc.Server {
	return s.grpc
}

func (s *Server) GracefulStop() {
	s.grpc.GracefulStop()
}

func (s *Server) Stop() {
	s.grpc.Stop()
}

var _ grpc.ServiceRegistrar = (*Server)(nil)
