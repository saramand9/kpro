package transport

import (
	"context"
	"testing"
)

type quiescerTestComponent struct{}

func (quiescerTestComponent) Name() string                   { return "quiescer-test" }
func (quiescerTestComponent) Shutdown(context.Context) error { return nil }
func (quiescerTestComponent) Quiesce(context.Context) error  { return nil }

func TestQuiescerIsAnOptionalComponentCapability(t *testing.T) {
	var component Component = quiescerTestComponent{}
	quiescer, ok := component.(Quiescer)
	if !ok {
		t.Fatal("component implementing Quiesce was not recognized as Quiescer")
	}
	if err := quiescer.Quiesce(context.Background()); err != nil {
		t.Fatalf("Quiesce() error = %v", err)
	}
}
