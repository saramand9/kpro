// File: examples/http-modular/server/internal/modules/users/handler.go
// Purpose: Provides runnable KPro example code.

package users

import "github.com/saramand9/bignet/kpro/transport/httpx"

type User struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

var allUsers = []User{
	{ID: 1, Name: "Ada", Active: true},
	{ID: 2, Name: "Linus", Active: true},
	{ID: 3, Name: "Grace", Active: false},
}

func list(ctx *httpx.Context) error {
	return ctx.JSON(200, allUsers)
}

func active(ctx *httpx.Context) error {
	activeUsers := make([]User, 0, len(allUsers))
	for _, user := range allUsers {
		if user.Active {
			activeUsers = append(activeUsers, user)
		}
	}
	return ctx.JSON(200, activeUsers)
}
