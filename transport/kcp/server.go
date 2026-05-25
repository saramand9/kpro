package kcp

import (
	"context"
	"errors"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

var ErrNotImplemented = errors.New("kcp transport is not implemented")

type Config struct {
	Addr     string
	Protocol protocol.PacketProtocol
}

type Server struct {
	config Config
}

func New(config Config) *Server {
	if config.Protocol == nil {
		config.Protocol = protocol.KProV1()
	}
	return &Server{config: config}
}

func NewServer(config Config) *Server {
	return New(config)
}

func (s *Server) Name() string {
	return string(core.TransportKCP)
}

func (s *Server) ListenAndServe(context.Context, core.Handler) error {
	return ErrNotImplemented
}

func (s *Server) Shutdown(context.Context) error {
	return nil
}
