package test

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/rand"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/tcp"
)

const msgTCPOrderStress uint32 = 910

func TestTCPMultiClientRandomOrderStrict100K(t *testing.T) {
	if os.Getenv("KPRO_TCP_LONG_ORDER_TEST") != "1" {
		t.Skip("set KPRO_TCP_LONG_ORDER_TEST=1 to run the 10-client x 100000-message TCP order stress test")
	}

	clients := envInt("KPRO_TCP_LONG_CLIENTS", 10)
	messagesPerClient := envInt("KPRO_TCP_LONG_MESSAGES", 100000)
	if clients <= 0 || messagesPerClient <= 0 {
		t.Fatalf("invalid stress dimensions: clients=%d messages=%d", clients, messagesPerClient)
	}

	logs := newTCPOrderStressLogs(clients, messagesPerClient)
	addr := "127.0.0.1:" + freeTCPPort(t)
	app := server.New(server.Config{
		Name: "tcp-order-stress",
		Router: core.RouterConfig{
			SyncQueueSize:         messagesPerClient + 16,
			AsyncWorkers:          4,
			AsyncQueueSize:        1024,
			MaxInflightPerSession: messagesPerClient + 16,
			SessionIdleTimeout:    2 * time.Minute,
			ShutdownTimeout:       30 * time.Second,
		},
	})
	app.TCP(addr, tcp.Config{
		Protocol:      protocol.KProV1(),
		SendQueueSize: messagesPerClient + 16,
		ReadTimeout:   2 * time.Minute,
		WriteTimeout:  2 * time.Minute,
	}).HandleSync(msgTCPOrderStress, func(ctx core.Context) error {
		clientID, token, err := decodeTCPOrderPayload(ctx.Message().Data)
		if err != nil {
			return err
		}
		if clientID < 0 || clientID >= clients {
			return fmt.Errorf("client id out of range: %d", clientID)
		}
		logs.serverReceived[clientID] = append(logs.serverReceived[clientID], token)
		msg := ctx.Message()
		if err := ctx.Session().Send(core.Message{ID: msg.ID, Seq: msg.Seq, Data: msg.Data}); err != nil {
			return err
		}
		logs.serverSent[clientID] = append(logs.serverSent[clientID], token)
		return nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
		select {
		case <-errCh:
		case <-time.After(30 * time.Second):
			t.Fatal("TCP order stress server did not stop")
		}
	}()

	deadline := time.Now().Add(10 * time.Minute)
	var wg sync.WaitGroup
	clientErrs := make(chan error, clients)
	started := time.Now()
	for clientID := 0; clientID < clients; clientID++ {
		clientID := clientID
		wg.Add(1)
		go func() {
			defer wg.Done()
			sent, received, err := runTCPOrderStressClient(addr, clientID, messagesPerClient, deadline)
			if err != nil {
				clientErrs <- err
				return
			}
			logs.clientSent[clientID] = sent
			logs.clientReceived[clientID] = received
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Until(deadline)):
		t.Fatalf("TCP order stress timed out after %s", time.Since(started).Truncate(time.Second))
	}
	close(clientErrs)
	for err := range clientErrs {
		if err != nil {
			t.Fatal(err)
		}
	}

	for clientID := 0; clientID < clients; clientID++ {
		assertTCPOrderEqual(t, clientID, "client_sent", logs.clientSent[clientID], "server_received", logs.serverReceived[clientID], messagesPerClient)
		assertTCPOrderEqual(t, clientID, "server_received", logs.serverReceived[clientID], "server_sent", logs.serverSent[clientID], messagesPerClient)
		assertTCPOrderEqual(t, clientID, "server_sent", logs.serverSent[clientID], "client_received", logs.clientReceived[clientID], messagesPerClient)
	}
	t.Logf("TCP order stress passed: clients=%d messages_per_client=%d total=%d elapsed=%s", clients, messagesPerClient, clients*messagesPerClient, time.Since(started).Truncate(time.Millisecond))
}

type tcpOrderStressLogs struct {
	clientSent     [][]uint64
	serverReceived [][]uint64
	serverSent     [][]uint64
	clientReceived [][]uint64
}

func newTCPOrderStressLogs(clients, messagesPerClient int) *tcpOrderStressLogs {
	logs := &tcpOrderStressLogs{
		clientSent:     make([][]uint64, clients),
		serverReceived: make([][]uint64, clients),
		serverSent:     make([][]uint64, clients),
		clientReceived: make([][]uint64, clients),
	}
	for i := 0; i < clients; i++ {
		logs.serverReceived[i] = make([]uint64, 0, messagesPerClient)
		logs.serverSent[i] = make([]uint64, 0, messagesPerClient)
	}
	return logs
}

func runTCPOrderStressClient(addr string, clientID, messagesPerClient int, deadline time.Time) ([]uint64, []uint64, error) {
	conn, err := dialTCPOrderStress(addr, deadline)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, nil, fmt.Errorf("client %d set deadline: %w", clientID, err)
	}

	receivedCh := make(chan tcpOrderClientResult, 1)
	go func() {
		received := make([]uint64, 0, messagesPerClient)
		for i := 0; i < messagesPerClient; i++ {
			result, err := protocol.KProV1().Read(conn, 64*1024)
			if err != nil {
				receivedCh <- tcpOrderClientResult{err: fmt.Errorf("client %d read response %d/%d: %w", clientID, i+1, messagesPerClient, err)}
				return
			}
			msg := result.Message
			if msg.ID != msgTCPOrderStress {
				receivedCh <- tcpOrderClientResult{err: fmt.Errorf("client %d response %d id=%d, want %d", clientID, i+1, msg.ID, msgTCPOrderStress)}
				return
			}
			payloadClientID, token, err := decodeTCPOrderPayload(msg.Data)
			if err != nil {
				receivedCh <- tcpOrderClientResult{err: fmt.Errorf("client %d response %d payload: %w", clientID, i+1, err)}
				return
			}
			if payloadClientID != clientID {
				receivedCh <- tcpOrderClientResult{err: fmt.Errorf("client %d response %d belongs to client %d", clientID, i+1, payloadClientID)}
				return
			}
			if msg.Seq != token {
				receivedCh <- tcpOrderClientResult{err: fmt.Errorf("client %d response %d seq=%d token=%d", clientID, i+1, msg.Seq, token)}
				return
			}
			received = append(received, token)
		}
		receivedCh <- tcpOrderClientResult{tokens: received}
	}()

	sent := make([]uint64, 0, messagesPerClient)
	rng := rand.New(rand.NewSource(int64(0x5eed_0000 + clientID)))
	order := rng.Perm(messagesPerClient)
	for writeIndex, randomValue := range order {
		token := tcpOrderToken(clientID, randomValue)
		payload := encodeTCPOrderPayload(clientID, writeIndex, token)
		frame, err := protocol.KProV1().Encode(core.Message{ID: msgTCPOrderStress, Seq: token, Data: payload})
		if err != nil {
			return nil, nil, fmt.Errorf("client %d encode %d: %w", clientID, writeIndex, err)
		}
		if err := writeAllTCPOrderStress(conn, frame); err != nil {
			return nil, nil, fmt.Errorf("client %d write %d/%d: %w", clientID, writeIndex+1, messagesPerClient, err)
		}
		sent = append(sent, token)
	}

	result := <-receivedCh
	if result.err != nil {
		return nil, nil, result.err
	}
	return sent, result.tokens, nil
}

type tcpOrderClientResult struct {
	tokens []uint64
	err    error
}

func tcpOrderToken(clientID, randomValue int) uint64 {
	return uint64(uint16(clientID))<<48 | uint64(uint64(randomValue)&0x0000ffffffffffff)
}

func encodeTCPOrderPayload(clientID, writeIndex int, token uint64) []byte {
	payload := make([]byte, 18)
	binary.BigEndian.PutUint16(payload[0:2], uint16(clientID))
	binary.BigEndian.PutUint32(payload[2:6], uint32(writeIndex))
	binary.BigEndian.PutUint64(payload[6:14], token)
	binary.BigEndian.PutUint32(payload[14:18], crc32.ChecksumIEEE(payload[:14]))
	return payload
}

func decodeTCPOrderPayload(payload []byte) (int, uint64, error) {
	if len(payload) != 18 {
		return 0, 0, fmt.Errorf("payload length=%d, want 18", len(payload))
	}
	wantCRC := binary.BigEndian.Uint32(payload[14:18])
	if gotCRC := crc32.ChecksumIEEE(payload[:14]); gotCRC != wantCRC {
		return 0, 0, fmt.Errorf("payload crc=%08x, want %08x", gotCRC, wantCRC)
	}
	clientID := int(binary.BigEndian.Uint16(payload[0:2]))
	token := binary.BigEndian.Uint64(payload[6:14])
	return clientID, token, nil
}

func dialTCPOrderStress(addr string, deadline time.Time) (net.Conn, error) {
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("dial %s: %w", addr, lastErr)
}

func writeAllTCPOrderStress(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func assertTCPOrderEqual(t *testing.T, clientID int, leftName string, left []uint64, rightName string, right []uint64, wantLen int) {
	t.Helper()
	if len(left) != wantLen || len(right) != wantLen {
		t.Fatalf("client %d %s/%s length: %d/%d, want %d", clientID, leftName, rightName, len(left), len(right), wantLen)
	}
	for i := 0; i < wantLen; i++ {
		if left[i] != right[i] {
			t.Fatalf("client %d order mismatch at index %d: %s=%d %s=%d", clientID, i, leftName, left[i], rightName, right[i])
		}
	}
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
