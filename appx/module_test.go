// File: appx/module_test.go
// Purpose: Tests lightweight application module contracts.

package appx

import (
	"context"
	"testing"

	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

func TestRegisterInstallsModulesInOrder(t *testing.T) {
	app := server.New()
	var order []string

	err := Register(context.Background(), app,
		testModule{name: "one", order: &order},
		testModule{name: "two", order: &order},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := order; len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("order = %v", got)
	}
}

func TestRegisterHTTPInstallsModulesInOrder(t *testing.T) {
	endpoint := httpx.New(httpx.Config{})
	var order []string

	RegisterHTTP(endpoint,
		testHTTPModule{name: "one", order: &order},
		testHTTPModule{name: "two", order: &order},
	)
	if got := order; len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("order = %v", got)
	}
}

type testModule struct {
	name  string
	order *[]string
}

func (m testModule) Register(context.Context, *server.Server) error {
	*m.order = append(*m.order, m.name)
	return nil
}

type testHTTPModule struct {
	name  string
	order *[]string
}

func (m testHTTPModule) RegisterHTTP(*httpx.Endpoint) {
	*m.order = append(*m.order, m.name)
}
