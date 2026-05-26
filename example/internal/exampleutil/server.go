package exampleutil

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/server"
)

func Echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}

func Run(app *server.Server) error {
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch

		log.Println("shutdown requested")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	}()

	return app.ListenAndServe()
}
