// File: internal/modules/users/handler/message.go
// Purpose: Provides KPro message DTO handlers for the users module.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/saramand9/bignet/kpro/core"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
	"github.com/saramand9/bignet/kpro/server"
)

const (
	// MsgCreateUser creates one user from a JSON CreateRequest payload.
	MsgCreateUser uint32 = 3101
)

// RegisterMessage exposes user routes on message endpoints such as TCP.
func RegisterMessage(endpoint *server.Endpoint, service *userservice.Service) {
	endpoint.HandleSync(MsgCreateUser, messageCreateUser(service))
}

func messageCreateUser(service *userservice.Service) core.Handler {
	return func(ctx core.Context) error {
		var request CreateRequest
		if err := decodeKProMessage(ctx, &request); err != nil {
			return ctx.Error("invalid json")
		}
		created, err := service.Create(context.Background(), request.toInput())
		if err != nil {
			return writeMessageServiceError(ctx, err)
		}
		return replyKProJSON(ctx, MsgCreateUser, created)
	}
}

func decodeKProMessage(ctx core.Context, target any) error {
	data := ctx.Message().Data
	if len(data) == 0 {
		return fmt.Errorf("empty payload")
	}
	return json.Unmarshal(data, target)
}

func replyKProJSON(ctx core.Context, msgID uint32, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return ctx.Reply(msgID, data)
}

func writeMessageServiceError(ctx core.Context, err error) error {
	if errors.Is(err, userservice.ErrInvalidInput) {
		return ctx.Error(err.Error())
	}
	return err
}
