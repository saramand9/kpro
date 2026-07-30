package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
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

func TestNewMinimalProfileMatchesDefaultScaffold(t *testing.T) {
	root := t.TempDir()
	defaultTarget := filepath.Join(root, "default")
	minimalTarget := filepath.Join(root, "minimal")
	var output bytes.Buffer
	if err := execute([]string{"new", "--module", "example.com/orders", "--name", "orders", defaultTarget}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if err := execute([]string{"new", "--module", "example.com/orders", "--name", "orders", "--profile", "minimal", minimalTarget}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "main.go", "README.md", ".gitignore"} {
		want, err := os.ReadFile(filepath.Join(defaultTarget, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(minimalTarget, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("minimal profile changed %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(defaultTarget, "cmd")); !os.IsNotExist(err) {
		t.Fatalf("default scaffold unexpectedly created cmd: %v", err)
	}
}

func TestNewHTTPAPIProfileCreatesModularParseableProject(t *testing.T) {
	target := filepath.Join(t.TempDir(), "orders-api")
	var output bytes.Buffer
	if err := execute([]string{"new", "--module", "example.com/orders", "--name", "orders-api", "--profile", "http-api", target}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"go.mod",
		"api/openapi.yaml",
		"kpro.gen.yaml",
		"cmd/orders-api/main.go",
		"internal/api/types/types.gen.go",
		"internal/api/contract/contract.gen.go",
		"internal/api/kprohttp/routes.gen.go",
		"internal/api/service/api.go",
		codegen.DefaultLockFile,
	} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Fatalf("http-api scaffold missing %s: %v", name, err)
		}
	}
	mainContents, err := os.ReadFile(filepath.Join(target, "cmd", "orders-api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"server.NewChecked", "server.MountNativeChecked", "kprohttp.Register", "ReadHeaderTimeout: 5 * time.Second", "MaxHeaderBytes:    1 << 20", "MaxBodyBytes:      1 << 20"} {
		if !strings.Contains(string(mainContents), expected) {
			t.Fatalf("http-api main.go does not contain %q", expected)
		}
	}
	err = filepath.Walk(target, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || filepath.Ext(path) != ".go" {
			return walkErr
		}
		_, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.AllErrors)
		return err
	})
	if err != nil {
		t.Fatalf("parse generated Go project: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(target, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "service: orders_api") {
		t.Fatalf("generated manifest has unsafe service identifier:\n%s", manifest)
	}
	output.Reset()
	if err := execute([]string{"generate", "--root", target, "--check"}, &output, &output); err != nil {
		t.Fatalf("generated profile is not current: %v", err)
	}
	service, err := os.ReadFile(filepath.Join(target, "internal", "api", "service", "api.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(service), `Status: "alive"`) {
		t.Fatalf("http-api service starter was replaced:\n%s", service)
	}
}

func TestNewHTTPAPIProfileNormalizesKeywordServiceName(t *testing.T) {
	target := filepath.Join(t.TempDir(), "keyword-service")
	var output bytes.Buffer
	if err := execute([]string{"new", "--module", "example.com/keyword", "--name", "type", "--profile", "http-api", target}, &output, &output); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(target, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(value), "service: type_service") {
		t.Fatalf("keyword service identifier was not normalized:\n%s", value)
	}
}

func TestNewRejectsUnknownProfile(t *testing.T) {
	var output bytes.Buffer
	err := execute([]string{"new", "--profile", "grpc-api", filepath.Join(t.TempDir(), "unknown")}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "unsupported project profile") {
		t.Fatalf("unknown profile error = %v", err)
	}
}
