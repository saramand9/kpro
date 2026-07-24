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
	generatedMain, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	mainContents := string(generatedMain)
	for _, expected := range []string{
		`server.MountNative(app, httpx.New(httpx.Config{`,
		`Addr:            ":8080"`,
		`httpEndpoint.Get("/livez", liveness)`,
		`httpEndpoint.Get("/readyz", readiness)`,
		`httpEndpoint.Get("/healthz", readiness)`,
		`"dependencies": "none"`,
	} {
		if !strings.Contains(mainContents, expected) {
			t.Fatalf("generated main.go does not contain %q:\n%s", expected, mainContents)
		}
	}
	if strings.Contains(mainContents, `app.HTTP(`) {
		t.Fatalf("generated main.go still uses the removed HTTP convenience method:\n%s", mainContents)
	}
	goMod, err := os.ReadFile(filepath.Join(target, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "module example.com/orders") {
		t.Fatalf("generated go.mod:\n%s", goMod)
	}
	if !strings.Contains(string(goMod), "replace github.com/saramand9/kpro => ") {
		t.Fatalf("generated go.mod has no reproducible local KPro source:\n%s", goMod)
	}
	if !strings.Contains(string(goMod), "go 1.25.12") {
		t.Fatalf("generated go.mod has wrong Go baseline:\n%s", goMod)
	}
	if err := execute([]string{"new", target}, &output, &output); err == nil {
		t.Fatal("second scaffold unexpectedly overwrote a non-empty target")
	}
}

func TestReleasedCLIUsesVersionedModuleWithoutLocalReplace(t *testing.T) {
	previousVersion, previousCommit, previousDate := version, commit, date
	version, commit, date = "v0.1.0", "release-commit", "2026-07-24"
	t.Cleanup(func() {
		version, commit, date = previousVersion, previousCommit, previousDate
	})
	t.Chdir(t.TempDir())
	target := filepath.Join(t.TempDir(), "released")
	var output bytes.Buffer
	if err := execute([]string{"new", "--module", "example.com/released", target}, &output, &output); err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join(target, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(goMod)
	if !strings.Contains(contents, "require "+kproModulePath+" v0.1.0") {
		t.Fatalf("generated go.mod does not pin the release:\n%s", contents)
	}
	if strings.Contains(contents, "replace ") {
		t.Fatalf("released scaffold unexpectedly contains a local replace:\n%s", contents)
	}
}

func TestNormalizeReleaseVersion(t *testing.T) {
	for input, want := range map[string]string{
		"v0.1.0": "v0.1.0",
		"0.1.0":  "v0.1.0",
	} {
		if got, ok := normalizeReleaseVersion(input); !ok || got != want {
			t.Fatalf("normalizeReleaseVersion(%q) = %q/%v, want %q/true", input, got, ok, want)
		}
	}
	for _, input := range []string{"", "devel", "(devel)", "not-a-version"} {
		if got, ok := normalizeReleaseVersion(input); ok {
			t.Fatalf("normalizeReleaseVersion(%q) = %q/true, want invalid", input, got)
		}
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

func TestVersionAndDoctorCommands(t *testing.T) {
	for _, command := range []string{"version", "doctor"} {
		t.Run(command, func(t *testing.T) {
			var output bytes.Buffer
			if err := execute([]string{command}, &output, &output); err != nil {
				t.Fatalf("%s: %v", command, err)
			}
			if output.Len() == 0 {
				t.Fatalf("%s produced no output", command)
			}
		})
	}
}
