package tcp

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestAcceptConnectionRetriesTemporaryErrors(t *testing.T) {
	firstServer, firstClient := net.Pipe()
	secondServer, secondClient := net.Pipe()
	t.Cleanup(func() {
		_ = firstServer.Close()
		_ = firstClient.Close()
		_ = secondServer.Close()
		_ = secondClient.Close()
	})
	listener := &scriptedAcceptListener{outcomes: []acceptOutcome{
		{err: temporaryAcceptError{}},
		{err: temporaryAcceptError{}},
		{connection: firstServer},
		{err: temporaryAcceptError{}},
		{connection: secondServer},
	}}
	server := New(Config{})

	first, err := server.acceptConnection(context.Background(), listener)
	if err != nil || first != firstServer {
		t.Fatalf("first accept = (%v, %v), want scripted connection", first, err)
	}
	second, err := server.acceptConnection(context.Background(), listener)
	if err != nil || second != secondServer {
		t.Fatalf("second accept = (%v, %v), want scripted connection", second, err)
	}
	if listener.calls != 5 {
		t.Fatalf("Accept calls = %d, want 5", listener.calls)
	}
}

func TestAcceptConnectionReturnsPermanentError(t *testing.T) {
	wantErr := errors.New("permanent listener failure")
	listener := &scriptedAcceptListener{outcomes: []acceptOutcome{{err: wantErr}}}
	server := New(Config{})

	connection, err := server.acceptConnection(context.Background(), listener)
	if connection != nil || !errors.Is(err, wantErr) {
		t.Fatalf("accept = (%v, %v), want (nil, %v)", connection, err, wantErr)
	}
	if listener.calls != 1 {
		t.Fatalf("Accept calls = %d, want 1", listener.calls)
	}
}

func TestAcceptConnectionBackoffStopsWithContextOrAdmission(t *testing.T) {
	tests := []struct {
		name string
		stop func(context.CancelFunc, *Server)
	}{
		{
			name: "context cancellation",
			stop: func(cancel context.CancelFunc, _ *Server) { cancel() },
		},
		{
			name: "admission stop",
			stop: func(_ context.CancelFunc, server *Server) {
				if err := server.stopAdmissionAndReads(); err != nil {
					t.Errorf("stop admission: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener := &scriptedAcceptListener{
				outcomes: []acceptOutcome{{err: temporaryAcceptError{}}},
				called:   make(chan struct{}, 1),
			}
			server := New(Config{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				connection, err := server.acceptConnection(ctx, listener)
				if connection != nil {
					_ = connection.Close()
					result <- errors.New("accept returned an unexpected connection")
					return
				}
				result <- err
			}()

			select {
			case <-listener.called:
			case <-time.After(time.Second):
				t.Fatal("Accept was not called")
			}
			test.stop(cancel, server)
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("accept after stop = %v, want nil", err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("accept backoff did not stop promptly")
			}
		})
	}
}

func TestAcceptRetryDelayIsExponentiallyBounded(t *testing.T) {
	tests := []struct {
		current time.Duration
		want    time.Duration
	}{
		{current: 0, want: initialAcceptRetryDelay},
		{current: initialAcceptRetryDelay, want: 2 * initialAcceptRetryDelay},
		{current: maxAcceptRetryDelay / 2, want: maxAcceptRetryDelay},
		{current: maxAcceptRetryDelay, want: maxAcceptRetryDelay},
	}
	for _, test := range tests {
		if got := nextAcceptRetryDelay(test.current); got != test.want {
			t.Fatalf("nextAcceptRetryDelay(%v) = %v, want %v", test.current, got, test.want)
		}
	}
}

func TestTemporaryAcceptErrorRecognizesDescriptorExhaustion(t *testing.T) {
	err := &net.OpError{
		Op:  "accept",
		Net: "tcp",
		Err: os.NewSyscallError("accept4", syscall.EMFILE),
	}
	if !isTemporaryAcceptError(err) {
		t.Fatalf("EMFILE accept error was not classified as temporary: %v", err)
	}
}

type acceptOutcome struct {
	connection net.Conn
	err        error
}

type scriptedAcceptListener struct {
	outcomes []acceptOutcome
	calls    int
	called   chan struct{}
}

func (l *scriptedAcceptListener) Accept() (net.Conn, error) {
	l.calls++
	if l.called != nil {
		select {
		case l.called <- struct{}{}:
		default:
		}
	}
	if len(l.outcomes) == 0 {
		return nil, net.ErrClosed
	}
	outcome := l.outcomes[0]
	l.outcomes = l.outcomes[1:]
	return outcome.connection, outcome.err
}

func (*scriptedAcceptListener) Close() error   { return nil }
func (*scriptedAcceptListener) Addr() net.Addr { return scriptedAcceptAddr("test") }

type scriptedAcceptAddr string

func (scriptedAcceptAddr) Network() string  { return "tcp" }
func (a scriptedAcceptAddr) String() string { return string(a) }

type temporaryAcceptError struct{}

func (temporaryAcceptError) Error() string   { return "temporary accept failure" }
func (temporaryAcceptError) Timeout() bool   { return false }
func (temporaryAcceptError) Temporary() bool { return true }
