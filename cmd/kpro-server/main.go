package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/saramand9/bignet/kpro"
)

func main() {
	cfg := kpro.DefaultConfig()
	flag.StringVar(&cfg.Host, "host", cfg.Host, "listen host")
	flag.IntVar(&cfg.Port, "port", cfg.Port, "listen port")
	flag.StringVar(&cfg.MetricsHost, "metrics-host", cfg.MetricsHost, "metrics listen host")
	flag.IntVar(&cfg.MetricsPort, "metrics-port", cfg.MetricsPort, "metrics listen port")
	flag.IntVar(&cfg.MaxConnections, "max-conns", cfg.MaxConnections, "max active connections")
	flag.IntVar(&cfg.MaxConnectionsPerIP, "max-conns-per-ip", cfg.MaxConnectionsPerIP, "max active connections per remote ip")
	flag.IntVar(&cfg.WorkerCount, "workers", cfg.WorkerCount, "worker count; 0 means GOMAXPROCS*2")
	flag.IntVar(&cfg.SendQueueSize, "send-queue", cfg.SendQueueSize, "per connection send queue size")
	maxFrameSize := uint(cfg.MaxFrameSize)
	flag.UintVar(&maxFrameSize, "max-frame", maxFrameSize, "max frame body size")
	flag.Parse()
	cfg.MaxFrameSize = uint32(maxFrameSize)

	s := kpro.NewServer(cfg)
	s.Handle(kpro.MsgEcho, func(ctx kpro.Context) error {
		return ctx.Reply(kpro.MsgEcho, ctx.Message().Data)
	})

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		log.Println("shutdown requested")
		s.Shutdown()
	}()

	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
