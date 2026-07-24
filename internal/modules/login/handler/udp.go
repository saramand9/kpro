// File: internal/modules/login/handler/udp.go
// Purpose: Provides UDP message DTO handlers for the login module.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/saramand9/kpro/core"
	loginservice "github.com/saramand9/kpro/internal/modules/login/service"
	"github.com/saramand9/kpro/server"
)

const (
	// MsgLogin authenticates one principal from a JSON LoginRequest payload.
	MsgLogin uint32 = 3001
)

// LoginRequest is the public UDP input for logging in.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (r LoginRequest) toInput() loginservice.LoginInput {
	return loginservice.LoginInput{
		Username: r.Username,
		Password: r.Password,
	}
}

// RegisterUDP exposes login routes on a message endpoint such as UDP.
func RegisterUDP(endpoint *server.Endpoint, service *loginservice.Service) {
	endpoint.HandleSync(MsgLogin, udpLogin(service))
}

func udpLogin(service *loginservice.Service) core.Handler {
	return func(ctx core.Context) error {
		var request LoginRequest
		if err := decodeMessage(ctx, &request); err != nil {
			return ctx.Error("invalid json")
		}
		session, err := service.Login(context.Background(), request.toInput())
		if err != nil {
			return writeUDPServiceError(ctx, err)
		}
		return replyJSON(ctx, MsgLogin, session)
	}
}

func decodeMessage(ctx core.Context, target any) error {
	data := ctx.Message().Data
	if len(data) == 0 {
		return fmt.Errorf("empty payload")
	}
	return json.Unmarshal(data, target)
}

func replyJSON(ctx core.Context, msgID uint32, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return ctx.Reply(msgID, data)
}

func writeUDPServiceError(ctx core.Context, err error) error {
	if errors.Is(err, loginservice.ErrInvalidCredentials) {
		return ctx.Error("invalid credentials")
	}
	return err
}
