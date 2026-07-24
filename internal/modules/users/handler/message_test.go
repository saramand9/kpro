// File: internal/modules/users/handler/message_test.go
// Purpose: Verifies KPro message handlers can persist through the GORM store.

package handler

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/saramand9/kpro/core"
	userservice "github.com/saramand9/kpro/internal/modules/users/service"
	userstore "github.com/saramand9/kpro/internal/modules/users/store"
)

func TestMessageCreateUserPersistsWithGORM(t *testing.T) {
	database := openHTTPTestDatabase(t)
	defer closeHTTPTestDatabase(t, database)

	service := userservice.NewWithStore(userstore.NewGORM(database))
	payload, err := json.Marshal(CreateRequest{Name: "Ada", Age: 37})
	if err != nil {
		t.Fatal(err)
	}

	session := &messageTestSession{}
	ctx := core.NewContext(session, core.Message{ID: MsgCreateUser, Seq: 77, Data: payload})
	if err := messageCreateUser(service)(ctx); err != nil {
		t.Fatal(err)
	}

	reply := session.last()
	if reply.ID != MsgCreateUser || reply.Seq != 77 {
		t.Fatalf("reply = %#v", reply)
	}
	var created userResponse
	if err := json.Unmarshal(reply.Data, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Name != "Ada" || created.Age != 37 || created.CreatedAt.IsZero() {
		t.Fatalf("created = %#v", created)
	}
	stored, err := service.Find(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != created.ID || stored.Name != created.Name || stored.Age != created.Age {
		t.Fatalf("stored = %#v, created = %#v", stored, created)
	}
}

func TestMessageCreateUserRejectsInvalidJSON(t *testing.T) {
	service := userservice.NewWithStore(userstore.NewGORM(nil))
	session := &messageTestSession{}
	ctx := core.NewContext(session, core.Message{ID: MsgCreateUser, Seq: 78, Data: []byte("{")})
	if err := messageCreateUser(service)(ctx); err != nil {
		t.Fatal(err)
	}

	reply := session.last()
	if reply.ID != core.MsgError || reply.Seq != 78 || string(reply.Data) != "invalid json" {
		t.Fatalf("error reply = %#v", reply)
	}
}

type messageTestSession struct {
	sent []core.Message
}

func (s *messageTestSession) ID() uint64 { return 1 }

func (s *messageTestSession) Transport() core.Transport { return core.TransportTCP }

func (s *messageTestSession) RemoteAddr() net.Addr { return nil }

func (s *messageTestSession) Send(msg core.Message) error {
	s.sent = append(s.sent, msg)
	return nil
}

func (s *messageTestSession) Close() error { return nil }

func (s *messageTestSession) last() core.Message {
	if len(s.sent) == 0 {
		return core.Message{}
	}
	return s.sent[len(s.sent)-1]
}
