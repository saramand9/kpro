package core

import (
	"errors"
	"testing"
)

func TestErrorKindsClassifyFrameworkSentinels(t *testing.T) {
	tests := []struct {
		err  error
		kind ErrorKind
	}{
		{ErrRateLimited, ErrorKindRateLimited},
		{ErrPayloadTooLarge, ErrorKindPayloadTooLarge},
		{ErrOverloaded, ErrorKindOverloaded},
		{ErrInternal, ErrorKindInternal},
		{ErrRecovered, ErrorKindRecovered},
		{ErrDispatchQueueFull, ErrorKindOverloaded},
		{ErrSendQueueFull, ErrorKindOverloaded},
		{ErrTooManyInflight, ErrorKindOverloaded},
		{ErrGlobalInflightLimit, ErrorKindOverloaded},
	}
	for _, test := range tests {
		if got := ErrorKindOf(test.err); got != test.kind {
			t.Errorf("ErrorKindOf(%v) = %q, want %q", test.err, got, test.kind)
		}
	}
}

func TestOverloadErrorsRetainSpecificIdentity(t *testing.T) {
	if !errors.Is(ErrDispatchQueueFull, ErrOverloaded) {
		t.Fatal("dispatch queue error did not unwrap to ErrOverloaded")
	}
	if errors.Is(ErrDispatchQueueFull, ErrSendQueueFull) {
		t.Fatal("distinct overload errors became indistinguishable")
	}
}

func TestWithErrorKindRetainsCause(t *testing.T) {
	cause := errors.New("database detail")
	err := WithErrorKind(ErrorKindInternal, cause)
	if ErrorKindOf(err) != ErrorKindInternal {
		t.Fatalf("error kind = %q, want internal", ErrorKindOf(err))
	}
	if !errors.Is(err, cause) {
		t.Fatal("classified error did not retain its cause")
	}
	if !errors.Is(err, ErrInternal) {
		t.Fatal("classified error did not unwrap to its kind sentinel")
	}
	if WithErrorKind(ErrorKindInternal, nil) != nil {
		t.Fatal("classifying nil did not return nil")
	}
}

func TestPublicErrorMessageUsesStableSafeText(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrRateLimited, "rate limit exceeded"},
		{ErrPayloadTooLarge, "payload too large"},
		{ErrDispatchQueueFull, "service overloaded"},
		{ErrInternal, "internal error"},
		{ErrRecovered, "internal error"},
		{errors.New("database password=secret"), "internal error"},
	}
	for _, test := range tests {
		if got := PublicErrorMessage(test.err); got != test.want {
			t.Errorf("PublicErrorMessage(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestIsDispatchRejection(t *testing.T) {
	if !IsDispatchRejection(ErrNoHandler) || !IsDispatchRejection(ErrDispatchQueueFull) {
		t.Fatal("Router rejection was not classified as handled")
	}
	if IsDispatchRejection(errors.New("handler failed")) {
		t.Fatal("ordinary handler error classified as Router rejection")
	}
}
