// File: examples/modular/server/internal/modules/events/handler.go
// Purpose: Provides runnable KPro example code.

package events

import (
	"time"

	"github.com/saramand9/bignet/kpro/transport/sse"
)

func timeStream(ctx *sse.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case t := <-ticker.C:
			if err := ctx.Send("time", []byte(t.Format(time.RFC3339))); err != nil {
				return err
			}
		}
	}
}
