package core

import "testing"

func TestReplyCurrentPayloadFallsBackToOrdinaryReply(t *testing.T) {
	session := newFakeSession(7)
	ctx := NewContext(session, Message{ID: 1, Seq: 9, Data: []byte("payload")})
	if err := ReplyCurrentPayload(ctx, 11); err != nil {
		t.Fatal(err)
	}
	message := session.waitForMessage(t)
	if message.ID != 11 || message.Seq != 9 || string(message.Data) != "payload" {
		t.Fatalf("fallback reply = %+v", message)
	}
}
