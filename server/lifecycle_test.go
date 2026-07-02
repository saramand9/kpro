// File: server/lifecycle_test.go
// Purpose: Implements or tests the high-level KPro server orchestration layer.

package server

import (
	"context"
	"reflect"
	"testing"
)

func TestServerManageShutsResourcesAfterRunners(t *testing.T) {
	var order []string
	app := New()
	app.MountRunner(&managedTestRunner{shutdown: func() {
		order = append(order, "runner")
	}})
	app.Manage(&managedTestResource{shutdown: func() {
		order = append(order, "resource")
	}})

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{"runner", "resource", "runner"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
}

type managedTestRunner struct {
	shutdown func()
}

func (r *managedTestRunner) Name() string {
	return "managed-test"
}

func (r *managedTestRunner) ListenAndServe(context.Context) error {
	return nil
}

func (r *managedTestRunner) Shutdown(context.Context) error {
	if r.shutdown != nil {
		r.shutdown()
	}
	return nil
}

type managedTestResource struct {
	shutdown func()
}

func (r *managedTestResource) Shutdown(context.Context) error {
	if r.shutdown != nil {
		r.shutdown()
	}
	return nil
}
