package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
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

func TestSubcommandHelpSucceeds(t *testing.T) {
	for _, command := range []string{"new", "add", "validate", "generate", "list", "describe", "version", "doctor"} {
		t.Run(command, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if err := execute([]string{command, "--help"}, &stdout, &stderr); err != nil {
				t.Fatalf("execute(%s --help) error = %v", command, err)
			}
			if !strings.Contains(stderr.String(), "Usage of "+command+":") {
				t.Fatalf("%s --help output does not contain command usage:\n%s", command, stderr.String())
			}
			if strings.Contains(stderr.String(), "flag: help requested") {
				t.Fatalf("%s --help reported help as an error:\n%s", command, stderr.String())
			}
		})
	}
}

func TestDoctorReportsAvailableProtoGenerationTools(t *testing.T) {
	paths := map[string]string{
		"protoc":             filepath.Join("tools", "protoc"),
		"protoc-gen-go":      filepath.Join("tools", "protoc-gen-go"),
		"protoc-gen-go-grpc": filepath.Join("tools", "protoc-gen-go-grpc"),
	}
	var output bytes.Buffer
	err := runDoctorWithLookPath(nil, &output, &output, func(name string) (string, error) {
		path, ok := paths[name]
		if !ok {
			t.Fatalf("unexpected tool lookup %q", name)
		}
		return path, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := output.String()
	if !strings.Contains(contents, "protobuf tools (protoc inspects contracts; protoc plus both plugins generate Go sources):") {
		t.Fatalf("doctor does not scope tools to proto generation:\n%s", contents)
	}
	for name, path := range paths {
		if !strings.Contains(contents, name+": available: "+path) {
			t.Fatalf("doctor does not report %s as available:\n%s", name, contents)
		}
	}
	if !strings.HasSuffix(contents, "status: ok\n") {
		t.Fatalf("doctor did not retain successful status:\n%s", contents)
	}
}

func TestDoctorTreatsMissingProtoGenerationToolsAsOptional(t *testing.T) {
	var lookedUp []string
	var output bytes.Buffer
	err := runDoctorWithLookPath(nil, &output, &output, func(name string) (string, error) {
		lookedUp = append(lookedUp, name)
		return "", os.ErrNotExist
	})
	if err != nil {
		t.Fatalf("optional tools made doctor fail: %v", err)
	}
	wantTools := []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"}
	if len(lookedUp) != len(wantTools) {
		t.Fatalf("looked up tools = %v, want %v", lookedUp, wantTools)
	}
	for index, want := range wantTools {
		if lookedUp[index] != want {
			t.Fatalf("looked up tools = %v, want %v", lookedUp, wantTools)
		}
	}
	contents := output.String()
	for _, name := range wantTools {
		if !strings.Contains(contents, name+": optional missing") {
			t.Fatalf("doctor does not report %s as optionally missing:\n%s", name, contents)
		}
	}
	if !strings.HasSuffix(contents, "status: ok\n") {
		t.Fatalf("doctor did not retain successful status:\n%s", contents)
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

func TestNewAcceptsTargetMixedBetweenFlags(t *testing.T) {
	target := filepath.Join(t.TempDir(), "mixed-flags")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/mixed",
		target,
		"--name", "mixed",
		"--profile", "minimal",
	}, &output, &output); err != nil {
		t.Fatalf("mixed target and flags: %v\n%s", err, output.String())
	}
	assertProjectPath(t, target, "go.mod")
	assertProjectPath(t, target, "main.go")
}

func TestNewServiceHTTPAPICreatesModularParseableProject(t *testing.T) {
	target := filepath.Join(t.TempDir(), "orders-api")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/orders",
		"--name", "orders-api",
		"--profile", "service",
		"--api", "http",
		target,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"go.mod",
		"api/http/openapi.yaml",
		"kpro.gen.yaml",
		"kpro.service.yaml",
		"cmd/orders-api/main.go",
		"internal/httpapi/types/types.gen.go",
		"internal/httpapi/contract/contract.gen.go",
		"internal/httpapi/kprohttp/routes.gen.go",
		"internal/httpapi/kprohttpclient/client.gen.go",
		"internal/httpapi/service/api.go",
		"internal/kproapp/endpoints.gen.go",
		"internal/kproapp/http.go",
		codegen.DefaultLockFile,
	} {
		assertProjectPath(t, target, name)
	}
	assertServiceMainUsesGeneratedAssembly(t, target, "orders-api")
	assertServiceAssemblyEntrypoint(t, target, []string{"mountHTTP(app)"}, []string{"mountGRPC(app)", "mountMessage(app)"})
	assertServiceProjectSelections(t, target, []string{"http"}, nil)
	httpMount, err := os.ReadFile(filepath.Join(target, "internal", "kproapp", "http.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"server.MountNativeChecked",
		"kprohttp.Register",
		"ReadHeaderTimeout: 5 * time.Second",
		"MaxHeaderBytes:    1 << 20",
		"MaxBodyBytes:      1 << 20",
	} {
		if !strings.Contains(string(httpMount), expected) {
			t.Fatalf("service HTTP mount does not contain %q:\n%s", expected, httpMount)
		}
	}
	assertProjectGoFilesParse(t, target)

	manifest, err := os.ReadFile(filepath.Join(target, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifestContents := string(manifest)
	for _, expected := range []string{
		`service: "orders_api"`,
		"kind: openapi",
		"path: api/http/openapi.yaml",
		"package: internal/httpapi",
	} {
		if !strings.Contains(manifestContents, expected) {
			t.Fatalf("generated HTTP manifest does not contain %q:\n%s", expected, manifest)
		}
	}
	output.Reset()
	if err := execute([]string{"generate", "--root", target, "--check"}, &output, &output); err != nil {
		t.Fatalf("generated profile is not current: %v", err)
	}
	service, err := os.ReadFile(filepath.Join(target, "internal", "httpapi", "service", "api.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(service), `Status: "alive"`) {
		t.Fatalf("service HTTP starter was replaced:\n%s", service)
	}
}

func TestNewServiceHTTPAPINormalizesKeywordServiceName(t *testing.T) {
	target := filepath.Join(t.TempDir(), "keyword-service")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/keyword",
		"--name", "type",
		"--profile", "service",
		"--api", "http",
		target,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(target, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(value), `service: "type_service"`) {
		t.Fatalf("keyword service identifier was not normalized:\n%s", value)
	}
}

func TestNewServiceGenerationFailureLeavesNoTargetOrStage(t *testing.T) {
	generator, registered := sourceGenerators[manifest.KindOpenAPI]
	delete(sourceGenerators, manifest.KindOpenAPI)
	defer func() {
		if registered {
			sourceGenerators[manifest.KindOpenAPI] = generator
			return
		}
		delete(sourceGenerators, manifest.KindOpenAPI)
	}()

	parent := t.TempDir()
	target := filepath.Join(parent, "atomic-service")
	var output bytes.Buffer
	err := execute([]string{
		"new",
		"--module", "example.com/atomic-service",
		"--profile", "service",
		"--api", "http",
		target,
	}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), `source "http-api": unsupported source kind "openapi"`) {
		t.Fatalf("generation failure = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("failed generation left target %q: %v", target, statErr)
	}
	assertNoScaffoldStages(t, parent)
}

func TestNewServiceRejectsExistingEmptyDirectory(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "existing-empty")
	wantMode := os.FileMode(0o711)
	if err := os.Mkdir(target, wantMode); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, wantMode); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	err := execute([]string{
		"new",
		"--module", "example.com/existing-empty",
		"--profile", "service",
		"--api", "http",
		target,
	}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "requires a missing target") {
		t.Fatalf("existing empty target error = %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("existing empty target was modified: %v", entries)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Fatalf("published target permissions = %#o, want %#o", got, wantMode)
		}
	}
	assertNoScaffoldStages(t, parent)
}

func TestNewPublishesMissingTargetWithPortablePermissions(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "missing-target")
	var output bytes.Buffer
	if err := execute([]string{"new", "--module", "example.com/missing-target", target}, &output, &output); err != nil {
		t.Fatal(err)
	}
	assertProjectPath(t, target, "main.go")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("new target permissions = %#o, want 0755", got)
		}
	}
	assertNoScaffoldStages(t, parent)
}

func TestNewRejectsSymlinkTargetWithoutReplacingIt(t *testing.T) {
	parent := t.TempDir()
	realTarget := filepath.Join(parent, "real-target")
	if err := os.Mkdir(realTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "link-target")
	if err := os.Symlink(realTarget, target); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	var output bytes.Buffer
	err := execute([]string{
		"new",
		"--module", "example.com/link-target",
		"--profile", "service",
		"--api", "http",
		target,
	}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink target error = %v", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target symlink was replaced: mode=%v", info.Mode())
	}
	entries, err := os.ReadDir(realTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink destination was modified: %v", entries)
	}
	assertNoScaffoldStages(t, parent)
}

func TestCommitScaffoldStageRejectsSymlinkTarget(t *testing.T) {
	parent := t.TempDir()
	stage := filepath.Join(parent, "stage")
	if err := os.Mkdir(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "go.mod"), []byte("module example.com/stage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	realTarget := filepath.Join(parent, "real-target")
	if err := os.Mkdir(realTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "link-target")
	if err := os.Symlink(realTarget, target); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	err := commitScaffoldStage(stage, target)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("commit symlink target error = %v", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("commit replaced target symlink: mode=%v", info.Mode())
	}
	if _, err := os.Stat(filepath.Join(stage, "go.mod")); err != nil {
		t.Fatalf("commit consumed stage after rejection: %v", err)
	}
}

func TestCommitScaffoldStageRejectsExistingTarget(t *testing.T) {
	parent := t.TempDir()
	stage := filepath.Join(parent, "stage")
	if err := os.Mkdir(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "go.mod"), []byte("module example.com/stage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "appeared")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	err := commitScaffoldStage(stage, target)
	if err == nil || !strings.Contains(err.Error(), "appeared while the scaffold was generated") {
		t.Fatalf("commit existing target error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "go.mod")); err != nil {
		t.Fatalf("commit consumed stage after rejection: %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("commit modified existing target: %v", entries)
	}
}

func TestNewRejectsNonPortableServicePathSegments(t *testing.T) {
	for _, name := range []string{
		"CON",
		"prn.log",
		"Aux",
		"NUL.txt",
		"COM1",
		"com9.data",
		"LPT1",
		"lpt9.out",
		"service.",
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "generated")
			var output bytes.Buffer
			err := execute([]string{
				"new",
				"--module", "example.com/portable",
				"--name", name,
				"--profile", "service",
				"--api", "http",
				target,
			}, &output, &output)
			if err == nil || !strings.Contains(err.Error(), "invalid service name") {
				t.Fatalf("non-portable service name %q error = %v", name, err)
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("invalid service name created target %q: %v", target, statErr)
			}
			assertNoScaffoldStages(t, parent)
		})
	}
}

func TestPortableServicePathSegmentAllowsSimilarNames(t *testing.T) {
	for _, name := range []string{"console", "com0", "com10", "lpt0", "lpt10", "service.name"} {
		if err := validatePortableServicePathSegment(name); err != nil {
			t.Errorf("validatePortableServicePathSegment(%q) error = %v", name, err)
		}
	}
}

func TestNewMessageRejectsOverlongDerivedServiceNameBeforeStaging(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "long-message")
	serviceName := "1" + strings.Repeat("a", 63)
	var output bytes.Buffer
	err := execute([]string{
		"new",
		"--module", "example.com/long-message",
		"--name", serviceName,
		"--profile", "service",
		"--api", "message",
		"--transport", "tcp",
		target,
	}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "derived name") || !strings.Contains(err.Error(), "maximum is 64") {
		t.Fatalf("overlong derived Message service error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("overlong service name created target %q: %v", target, statErr)
	}
	assertNoScaffoldStages(t, parent)
}

func TestNewMessageRejectsReservedDerivedFileNameBeforeStaging(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "derived-reserved")
	var output bytes.Buffer
	err := execute([]string{
		"new",
		"--module", "example.com/derived-reserved",
		"--name", "c-o-n",
		"--profile", "service",
		"--api", "message",
		"--transport", "tcp",
		target,
	}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "invalid derived scaffold path") || !strings.Contains(strings.ToLower(err.Error()), "con.go") {
		t.Fatalf("reserved derived file error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("reserved derived file created target %q: %v", target, statErr)
	}
	assertNoScaffoldStages(t, parent)
}

func TestNewServiceSupportsYAMLScalarLikeModuleAndName(t *testing.T) {
	target := filepath.Join(t.TempDir(), "scalar-service")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "1.0",
		"--name", "null",
		"--profile", "service",
		"--api", "http",
		"--api", "message",
		"--transport", "tcp",
		target,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(target, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`module: "1.0"`, `service: "null"`} {
		if !strings.Contains(string(manifest), expected) {
			t.Fatalf("scalar-safe manifest does not contain %q:\n%s", expected, manifest)
		}
	}
	messageSpec, err := os.ReadFile(filepath.Join(target, "api", "message", "messages.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(messageSpec), `service: "Null"`) {
		t.Fatalf("scalar-safe Message contract is not quoted:\n%s", messageSpec)
	}
	assertProjectGoFilesParse(t, target)
	output.Reset()
	if err := execute([]string{"generate", "--root", target, "--check"}, &output, &output); err != nil {
		t.Fatalf("scalar-safe generated profile is not current: %v", err)
	}
}

func TestNewServiceGRPCPreflightFailureLeavesNoPartialTarget(t *testing.T) {
	tools := []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"}
	for missingIndex, missing := range tools {
		t.Run(missing, func(t *testing.T) {
			previousLookPath := scaffoldLookPath
			var lookedUp []string
			scaffoldLookPath = func(name string) (string, error) {
				lookedUp = append(lookedUp, name)
				if name == missing {
					return "", os.ErrNotExist
				}
				return filepath.Join("tools", name), nil
			}
			defer func() { scaffoldLookPath = previousLookPath }()

			target := filepath.Join(t.TempDir(), "grpc-service")
			var output bytes.Buffer
			err := execute([]string{
				"new",
				"--module", "example.com/grpc-service",
				"--profile", "service",
				"--api", "grpc",
				target,
			}, &output, &output)
			if err == nil || !strings.Contains(err.Error(), "requires "+missing+" on PATH") || !strings.Contains(err.Error(), "kpro doctor") {
				t.Fatalf("missing %s error = %v", missing, err)
			}
			wantLookups := tools[:missingIndex+1]
			if strings.Join(lookedUp, ",") != strings.Join(wantLookups, ",") {
				t.Fatalf("tool lookups = %v, want %v", lookedUp, wantLookups)
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("failed gRPC preflight left target %q: %v", target, statErr)
			}
		})
	}
}

func TestNewServiceGRPCAPIWithInstalledToolchain(t *testing.T) {
	for _, tool := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("optional gRPC scaffold integration: %s is unavailable: %v", tool, err)
		}
	}
	previousLookPath := scaffoldLookPath
	scaffoldLookPath = exec.LookPath
	t.Cleanup(func() { scaffoldLookPath = previousLookPath })

	target := filepath.Join(t.TempDir(), "greeter")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/greeter",
		"--name", "greeter",
		"--profile", "service",
		"--api", "grpc",
		target,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"go.mod",
		"api/grpc/service.proto",
		"kpro.gen.yaml",
		"kpro.service.yaml",
		"cmd/greeter/main.go",
		"internal/grpcapi/service.pb.go",
		"internal/grpcapi/service_grpc.pb.go",
		"internal/grpcapi/kpro_grpc_api_grpc.gen.go",
		"internal/grpcapi/kpro_grpc_api_service.go",
		"internal/kproapp/endpoints.gen.go",
		"internal/kproapp/grpc.go",
		codegen.DefaultLockFile,
	} {
		assertProjectPath(t, target, name)
	}
	manifest, err := os.ReadFile(filepath.Join(target, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"kind: proto",
		"path: api/grpc/service.proto",
		"package: internal/grpcapi",
	} {
		if !strings.Contains(string(manifest), expected) {
			t.Fatalf("generated gRPC manifest does not contain %q:\n%s", expected, manifest)
		}
	}
	assertServiceMainUsesGeneratedAssembly(t, target, "greeter")
	assertServiceAssemblyEntrypoint(t, target, []string{"mountGRPC(app)"}, []string{"mountHTTP(app)", "mountMessage(app)"})
	assertServiceProjectSelections(t, target, []string{"grpc"}, nil)
	grpcMount, err := os.ReadFile(filepath.Join(target, "internal", "kproapp", "grpc.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"grpcx.NewServer",
		"KProGrpcApiGRPCServices",
		"KProGrpcApiGreeterService",
		"RegisterGRPC",
	} {
		if !strings.Contains(string(grpcMount), expected) {
			t.Fatalf("service gRPC mount does not contain %q:\n%s", expected, grpcMount)
		}
	}
	service, err := os.ReadFile(filepath.Join(target, "internal", "grpcapi", "kpro_grpc_api_service.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(service), `Status: "ready"`) {
		t.Fatalf("service gRPC starter was replaced:\n%s", service)
	}
	assertProjectGoFilesParse(t, target)
	output.Reset()
	if err := execute([]string{"generate", "--root", target, "--check"}, &output, &output); err != nil {
		t.Fatalf("generated gRPC profile is not current: %v", err)
	}
}
func TestNewServiceMessageAPISupportsMultipleTransports(t *testing.T) {
	target := filepath.Join(t.TempDir(), "message-service")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/message-service",
		"--name", "message-service",
		"--profile", "service",
		"--api", "message",
		"--transport", "tcp",
		"--transport", "kcp",
		"--transport", "quic",
		"--transport", "udp",
		"--transport", "websocket",
		target,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"go.mod",
		"api/message/messages.yaml",
		"kpro.gen.yaml",
		"kpro.service.yaml",
		"cmd/message-service/main.go",
		"internal/messageapi/types",
		"internal/messageapi/contract",
		"internal/messageapi/kpromessage",
		"internal/messageapi/service",
		"internal/kproapp/endpoints.gen.go",
		"internal/kproapp/message.go",
		codegen.DefaultLockFile,
	} {
		assertProjectPath(t, target, name)
	}
	assertServiceMainUsesGeneratedAssembly(t, target, "message-service")
	assertServiceAssemblyEntrypoint(t, target, []string{"mountMessage(app)"}, []string{"mountHTTP(app)", "mountGRPC(app)"})
	assertServiceProjectSelections(
		t,
		target,
		[]string{"message"},
		[]string{"tcp", "kcp", "quic", "udp", "websocket"},
	)
	messageMount, err := os.ReadFile(filepath.Join(target, "internal", "kproapp", "message.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"github.com/saramand9/kpro/transport/tcp"`,
		`"github.com/saramand9/kpro/transport/kcp"`,
		`"github.com/saramand9/kpro/transport/quic"`,
		`"github.com/saramand9/kpro/transport/udp"`,
		`"github.com/saramand9/kpro/transport/websocket"`,
		"kpromessage.",
	} {
		if !strings.Contains(string(messageMount), expected) {
			t.Fatalf("service Message mount does not contain %q:\n%s", expected, messageMount)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(target, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"kind: message",
		"path: api/message/messages.yaml",
		"package: internal/messageapi",
	} {
		if !strings.Contains(string(manifest), expected) {
			t.Fatalf("generated Message manifest does not contain %q:\n%s", expected, manifest)
		}
	}
	assertProjectGoFilesParse(t, target)
	output.Reset()
	if err := execute([]string{"generate", "--root", target, "--check"}, &output, &output); err != nil {
		t.Fatalf("generated Message profile is not current: %v", err)
	}
}

func TestNewServiceCombinesHTTPAndMessageAPIs(t *testing.T) {
	target := filepath.Join(t.TempDir(), "combined-service")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/combined",
		"--name", "combined-service",
		"--profile", "service",
		"--api", "message",
		"--transport", "tcp",
		"--api", "http",
		target,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"api/http/openapi.yaml",
		"api/message/messages.yaml",
		"kpro.service.yaml",
		"internal/httpapi/kprohttp/routes.gen.go",
		"internal/httpapi/kprohttpclient/client.gen.go",
		"internal/messageapi/kpromessage",
		"internal/kproapp/endpoints.gen.go",
		"internal/kproapp/http.go",
		"internal/kproapp/message.go",
		"cmd/combined-service/main.go",
		codegen.DefaultLockFile,
	} {
		assertProjectPath(t, target, name)
	}
	manifest, err := os.ReadFile(filepath.Join(target, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(manifest)
	httpIndex := strings.Index(contents, "kind: openapi")
	messageIndex := strings.Index(contents, "kind: message")
	if httpIndex < 0 || messageIndex < 0 || httpIndex >= messageIndex {
		t.Fatalf("combined manifest is not in canonical HTTP-then-Message order:\n%s", contents)
	}
	assertServiceMainUsesGeneratedAssembly(t, target, "combined-service")
	assertServiceAssemblyEntrypoint(t, target, []string{"mountHTTP(app)", "mountMessage(app)"}, []string{"mountGRPC(app)"})
	assertServiceProjectSelections(t, target, []string{"http", "message"}, []string{"tcp"})
	httpMount, err := os.ReadFile(filepath.Join(target, "internal", "kproapp", "http.go"))
	if err != nil {
		t.Fatal(err)
	}
	messageMount, err := os.ReadFile(filepath.Join(target, "internal", "kproapp", "message.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(httpMount), "kprohttp.Register") {
		t.Fatalf("combined HTTP mount has no generated registration:\n%s", httpMount)
	}
	for _, expected := range []string{"kpromessage.", `"github.com/saramand9/kpro/transport/tcp"`} {
		if !strings.Contains(string(messageMount), expected) {
			t.Fatalf("combined Message mount does not contain %q:\n%s", expected, messageMount)
		}
	}
	assertProjectGoFilesParse(t, target)
	output.Reset()
	if err := execute([]string{"generate", "--root", target, "--check"}, &output, &output); err != nil {
		t.Fatalf("combined generated profile is not current: %v", err)
	}
}

func TestNewServiceRejectsInvalidAPIAndTransportSelections(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing API", args: []string{"--profile", "service"}, want: "api"},
		{name: "unknown API", args: []string{"--profile", "service", "--api", "graphql"}, want: "api"},
		{name: "duplicate API", args: []string{"--profile", "service", "--api", "http", "--api", "http"}, want: "api"},
		{name: "message missing transport", args: []string{"--profile", "service", "--api", "message"}, want: "transport"},
		{name: "transport without message", args: []string{"--profile", "service", "--api", "http", "--transport", "tcp"}, want: "transport"},
		{name: "unknown transport", args: []string{"--profile", "service", "--api", "message", "--transport", "serial"}, want: "transport"},
		{name: "duplicate transport", args: []string{"--profile", "service", "--api", "message", "--transport", "tcp", "--transport", "tcp"}, want: "transport"},
		{name: "API with minimal", args: []string{"--profile", "minimal", "--api", "http"}, want: "api"},
		{name: "API with default profile", args: []string{"--api", "http"}, want: "api"},
		{name: "transport with minimal", args: []string{"--profile", "minimal", "--transport", "tcp"}, want: "transport"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "invalid")
			args := append([]string{"new"}, test.args...)
			args = append(args, target)
			var output bytes.Buffer
			err := execute(args, &output, &output)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("execute() error = %v, want containing %q", err, test.want)
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("invalid selection created target %q: %v", target, statErr)
			}
		})
	}
}

func TestNewRejectsRemovedHTTPAPIProfile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "removed")
	var output bytes.Buffer
	err := execute([]string{"new", "--profile", "http-api", target}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "unsupported project profile") {
		t.Fatalf("removed http-api profile error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("removed profile created target %q: %v", target, statErr)
	}
}

func TestNewServiceOutputIsIndependentOfOptionOrder(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/deterministic",
		"--name", "deterministic",
		"--profile", "service",
		"--api", "http",
		"--api", "message",
		"--transport", "tcp",
		"--transport", "websocket",
		first,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := execute([]string{
		"new",
		second,
		"--transport", "websocket",
		"--api", "message",
		"--profile", "service",
		"--transport", "tcp",
		"--api", "http",
		"--name", "deterministic",
		"--module", "example.com/deterministic",
	}, &output, &output); err != nil {
		t.Fatal(err)
	}

	firstFiles := readProjectTree(t, first)
	secondFiles := readProjectTree(t, second)
	if len(firstFiles) != len(secondFiles) {
		t.Fatalf("generated file counts differ: first=%d second=%d", len(firstFiles), len(secondFiles))
	}
	for name, want := range firstFiles {
		got, ok := secondFiles[name]
		if !ok {
			t.Errorf("second project is missing %s", name)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("option order changed %s", name)
		}
	}
}

func assertServiceMainUsesGeneratedAssembly(t *testing.T, root, service string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, "cmd", service, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"server.NewChecked", "kproapp.Mount(app)", "/internal/kproapp"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("service main.go does not contain %q:\n%s", expected, contents)
		}
	}
	for _, forbidden := range []string{
		"server.MountNativeChecked",
		".MountChecked(",
		"/transport/",
		"kprohttp.",
		"grpcapi.",
		"kpromessage.",
	} {
		if strings.Contains(string(contents), forbidden) {
			t.Fatalf("service main.go contains protocol assembly %q:\n%s", forbidden, contents)
		}
	}
}

func assertServiceAssemblyEntrypoint(t *testing.T, root string, selected, absent []string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, "internal", "kproapp", "endpoints.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, expected := range selected {
		index := strings.Index(string(contents), expected)
		if index < 0 {
			t.Fatalf("generated assembly does not contain %q:\n%s", expected, contents)
		}
		if index <= previous {
			t.Fatalf("generated assembly is not in canonical order at %q:\n%s", expected, contents)
		}
		previous = index
	}
	for _, forbidden := range absent {
		if strings.Contains(string(contents), forbidden) {
			t.Fatalf("generated assembly unexpectedly contains %q:\n%s", forbidden, contents)
		}
	}
}

func assertServiceProjectSelections(t *testing.T, root string, apis, transports []string) {
	t.Helper()
	project, err := loadServiceProjectConfig(filepath.Join(root, defaultServiceProjectFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(project.APIs, ",") != strings.Join(apis, ",") {
		t.Fatalf("service project APIs = %v, want %v", project.APIs, apis)
	}
	if strings.Join(project.Transports, ",") != strings.Join(transports, ",") {
		t.Fatalf("service project transports = %v, want %v", project.Transports, transports)
	}
}
func assertNoScaffoldStages(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".kpro-") {
			t.Fatalf("scaffold staging directory was not cleaned: %s", entry.Name())
		}
	}
}

func assertProjectPath(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
		t.Fatalf("generated project missing %s: %v", name, err)
	}
}

func assertProjectGoFilesParse(t *testing.T, root string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || filepath.Ext(path) != ".go" {
			return walkErr
		}
		_, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.AllErrors)
		return err
	})
	if err != nil {
		t.Fatalf("parse generated Go project: %v", err)
	}
}

func readProjectTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.Walk(root, func(filename string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = content
		return nil
	})
	if err != nil {
		t.Fatalf("read generated project: %v", err)
	}
	return files
}
