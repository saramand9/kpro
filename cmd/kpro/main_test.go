package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCreatesParseableServiceWithoutOverwriting(t *testing.T) {
	target := filepath.Join(t.TempDir(), "orders")
	var output bytes.Buffer
	if err := execute([]string{"new", "--module", "example.com/orders", target}, &output, &output); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(target, "main.go")
	if _, err := parser.ParseFile(token.NewFileSet(), mainPath, nil, parser.AllErrors); err != nil {
		t.Fatalf("parse generated main.go: %v", err)
	}
	goMod, err := os.ReadFile(filepath.Join(target, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "module example.com/orders") {
		t.Fatalf("generated go.mod:\n%s", goMod)
	}
	if !strings.Contains(string(goMod), "replace github.com/saramand9/bignet/kpro => ") {
		t.Fatalf("generated go.mod has no reproducible local KPro source:\n%s", goMod)
	}
	if err := execute([]string{"new", target}, &output, &output); err == nil {
		t.Fatal("second scaffold unexpectedly overwrote a non-empty target")
	}
}

func TestNewRejectsInvalidKProReplacement(t *testing.T) {
	var output bytes.Buffer
	err := execute([]string{
		"new",
		"--kpro-replace", t.TempDir(),
		filepath.Join(t.TempDir(), "invalid-kpro"),
	}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "read go.mod") {
		t.Fatalf("invalid KPro replacement error = %v", err)
	}
}

func TestNewRejectsInvalidModulePath(t *testing.T) {
	var output bytes.Buffer
	err := execute([]string{"new", "--module", "not a module path", filepath.Join(t.TempDir(), "bad")}, &output, &output)
	if err == nil {
		t.Fatal("invalid module path was accepted")
	}
}

func TestNewAcceptsDirectoryBeforeFlags(t *testing.T) {
	target := filepath.Join(t.TempDir(), "billing")
	var output bytes.Buffer
	if err := execute([]string{"new", target, "--module", "example.com/billing"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "main.go")); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsServiceNameThatCannotBeSafelyRendered(t *testing.T) {
	var output bytes.Buffer
	err := execute([]string{
		"new",
		"--name", `bad"name`,
		filepath.Join(t.TempDir(), "bad-name"),
	}, &output, &output)
	if err == nil {
		t.Fatal("unsafe service name was accepted")
	}
}
