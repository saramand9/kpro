package middleware

import (
	"strings"
	"testing"

	"github.com/saramand9/kpro/core"
)

func TestRecoverConvertsPanicToError(t *testing.T) {
	ctx := core.NewInvocation(core.InvocationOptions{Operation: core.MessageOperation(testMsgEcho)})
	var loggedRecovered interface{}
	var loggedStack string
	previousLogger := logPanicStack
	logPanicStack = func(recovered interface{}, stack []byte) {
		loggedRecovered = recovered
		loggedStack = string(stack)
	}
	defer func() {
		logPanicStack = previousLogger
	}()

	handler := Recover()(func(core.Invocation) error {
		panic("simulated crash")
	})

	err := handler(ctx)
	if err == nil || err.Error() != "handler panic: simulated crash" {
		t.Fatalf("err = %v, want handler panic error", err)
	}
	if loggedRecovered != "simulated crash" {
		t.Fatalf("logged recovered = %v, want simulated crash", loggedRecovered)
	}
	if !strings.Contains(loggedStack, "runtime/debug.Stack") ||
		!strings.Contains(loggedStack, "TestRecoverConvertsPanicToError") {
		t.Fatalf("logged stack does not include expected frames:\n%s", loggedStack)
	}
	t.Logf("handler returned error: %v", err)
	t.Logf("server logged panic: %v", loggedRecovered)
	t.Logf("server stack starts with: %q", firstLine(loggedStack))
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
