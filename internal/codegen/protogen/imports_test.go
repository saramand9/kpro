package protogen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseImportsRecognizesOnlyTopLevelStatementsAndSupportsModifiers(t *testing.T) {
	input := []byte(`syntax = "proto3";
// import "ignored/line.proto";
/* import "ignored/block.proto"; */
package example.import.v1;
option (example.import).label = "import \"ignored/option.proto\";";
import "api/first.proto";
import public "api/second.proto";
import weak 'api/third.proto';
// import option is exercised at the parser layer because the optional local
// real-protoc smoke test may use a compiler older than Edition 2024.
import option "api/options.proto";
message Request {
  string import = 1;
  option (example.aggregate) = { import: "ignored/aggregate.proto" };
}
import "api/after-message.proto";
`)
	got, err := ParseImports(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"api/first.proto",
		"api/second.proto",
		"api/third.proto",
		"api/options.proto",
		"api/after-message.proto",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imports = %v, want %v", got, want)
	}
}

func TestParseImportsConcatenatesFragmentsAndDecodesEscapes(t *testing.T) {
	input := []byte(`import "api/" /* between fragments */ "ser" 'vice.proto';
import "\141pi\57" "\x74ypes-\u00E9-\U0001F680.proto";
import "api/it\'s.proto";
import "api\\nested\\file.proto";
`)
	got, err := ParseImports(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"api/service.proto",
		"api/types-é-🚀.proto",
		"api/it's.proto",
		"api/nested/file.proto",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imports = %v, want %v", got, want)
	}
}

func TestImportScannerDecodesOfficialStringEscapes(t *testing.T) {
	scanner := importScanner{input: []byte(`"\a\b\f\n\r\t\v\\\'\"\?\x4Z\x41\101\u00E9\U0001F680"`)}
	token, err := scanner.next()
	if err != nil {
		t.Fatal(err)
	}
	want := "\a\b\f\n\r\t\v\\'\"?\x04ZAAé🚀"
	if token.kind != importTokenString || token.value != want {
		t.Fatalf("decoded string = kind %v value %q, want %q", token.kind, token.value, want)
	}
}

func TestParseImportsRejectsUnsafeAndMalformedImports(t *testing.T) {
	for name, input := range map[string]string{
		"unsafe":               `import ".." "/outside.proto";`,
		"missing path":         `import public;`,
		"missing semicolon":    `import "api/" "shared.proto"`,
		"unterminated string":  `import "api/shared.proto;`,
		"unterminated comment": `/* import "api/shared.proto";`,
		"unknown escape":       `import "api/\q.proto";`,
		"empty hex escape":     `import "api/\x.proto";`,
		"short unicode escape": `import "api/\u123.proto";`,
		"surrogate escape":     `import "api/\uD800.proto";`,
		"unicode out of range": `import "api/\U00110000.proto";`,
		"octal out of range":   `import "api/\400.proto";`,
		"unmatched close brace": `}
import "api/shared.proto";`,
		"unclosed declaration": `message Request { string value = 1;`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseImports([]byte(input)); err == nil {
				t.Fatalf("ParseImports(%q) unexpectedly succeeded", input)
			}
		})
	}
}

func TestValidateImportClosureHandlesRecursionCyclesAndModifiers(t *testing.T) {
	root := t.TempDir()
	writeImportTestFile(t, root, "api/root.proto", `syntax = "proto3";
import public "shared/a.proto";
import weak "shared/b.proto";
`)
	writeImportTestFile(t, root, "shared/a.proto", `import "shared/c.proto";`)
	writeImportTestFile(t, root, "shared/b.proto", `import "api/root.proto";`)
	writeImportTestFile(t, root, "shared/c.proto", `syntax = "proto3";`)

	resolved, err := resolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateImportClosure(resolved, "api/root.proto"); err != nil {
		t.Fatalf("validate recursive import closure: %v", err)
	}
}

func TestValidateImportClosureRejectsPortablePathAliases(t *testing.T) {
	root := t.TempDir()
	writeImportTestFile(t, root, "api/root.proto", `import "shared/Foo.proto";
import "shared/foo.proto";
`)
	writeImportTestFile(t, root, "shared/Foo.proto", `syntax = "proto3";`)
	resolved, err := resolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	err = validateImportClosure(resolved, "api/root.proto")
	if err == nil || !strings.Contains(err.Error(), "shared/Foo.proto") ||
		!strings.Contains(err.Error(), "shared/foo.proto") || !strings.Contains(err.Error(), "aliases") {
		t.Fatalf("portable alias error = %v", err)
	}
}

func TestValidateImportClosureRejectsMissingImport(t *testing.T) {
	root := t.TempDir()
	writeImportTestFile(t, root, "api/root.proto", `import "shared/missing.proto";`)
	resolved, err := resolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	err = validateImportClosure(resolved, "api/root.proto")
	if err == nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "shared/missing.proto") {
		t.Fatalf("missing import error = %v", err)
	}
}

func TestValidateImportClosureRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.proto")
	if err := os.WriteFile(outside, []byte(`syntax = "proto3";`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeImportTestFile(t, root, "api/root.proto", `import "shared/outside.proto";`)
	if err := os.MkdirAll(filepath.Join(root, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "shared", "outside.proto")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	resolved, err := resolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	err = validateImportClosure(resolved, "api/root.proto")
	if err == nil || !strings.Contains(err.Error(), "shared/outside.proto") || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func TestBackendsRejectImportClosureBeforeToolLookupOrExecution(t *testing.T) {
	for _, backend := range []string{"generator", "inspector"} {
		t.Run(backend, func(t *testing.T) {
			root, value, source := descriptorTestProject(t)
			writeImportTestFile(t, root, source.Path, `syntax = "proto3";
import "shared/missing.proto";
package example.orders.v1;
service Orders {}
`)
			lookupCalled := false
			runnerCalled := false
			lookup := func(string) (string, error) {
				lookupCalled = true
				return filepath.Join(root, "tools", "unexpected.exe"), nil
			}
			runner := runnerFunc(func(context.Context, Command) ([]byte, error) {
				runnerCalled = true
				return nil, errors.New("unexpected runner call")
			})

			var err error
			switch backend {
			case "generator":
				_, err = (Generator{Root: root, LookupTool: lookup, Runner: runner}).Plan(value, source)
			case "inspector":
				_, err = (Inspector{Root: root, LookupTool: lookup, Runner: runner}).Inspect(value, source)
			}
			if err == nil || !strings.Contains(err.Error(), "validate import closure") || !strings.Contains(err.Error(), "shared/missing.proto") {
				t.Fatalf("%s error = %v", backend, err)
			}
			if lookupCalled || runnerCalled {
				t.Fatalf("%s called lookup=%v runner=%v after import failure", backend, lookupCalled, runnerCalled)
			}
		})
	}
}

func TestBackendsRejectArgumentLikeSourceBeforeToolLookupOrExecution(t *testing.T) {
	for _, test := range []struct {
		sourcePath string
		want       string
	}{
		{sourcePath: "-entry.proto", want: "must not begin"},
		{sourcePath: "@response.proto", want: "must not begin"},
		{sourcePath: "entry,name.proto", want: "must not contain"},
		{sourcePath: "entry=name.proto", want: "must not contain"},
	} {
		for _, backend := range []string{"generator", "inspector"} {
			t.Run(test.sourcePath+"/"+backend, func(t *testing.T) {
				root, value, source := descriptorTestProject(t)
				source.Path = test.sourcePath
				value.Sources[0] = source
				lookupCalled := false
				runnerCalled := false
				lookup := func(string) (string, error) {
					lookupCalled = true
					return filepath.Join(root, "tools", "unexpected.exe"), nil
				}
				runner := runnerFunc(func(context.Context, Command) ([]byte, error) {
					runnerCalled = true
					return nil, errors.New("unexpected runner call")
				})

				var err error
				switch backend {
				case "generator":
					_, err = (Generator{Root: root, LookupTool: lookup, Runner: runner}).Plan(value, source)
				case "inspector":
					_, err = (Inspector{Root: root, LookupTool: lookup, Runner: runner}).Inspect(value, source)
				}
				if err == nil || !strings.Contains(err.Error(), test.sourcePath) ||
					!strings.Contains(err.Error(), test.want) {
					t.Fatalf("%s error = %v", backend, err)
				}
				if lookupCalled || runnerCalled {
					t.Fatalf("%s called lookup=%v runner=%v after source path failure", backend, lookupCalled, runnerCalled)
				}
			})
		}
	}
}

func TestBackendsUseSnapshotCapturedBeforeToolLookupMutation(t *testing.T) {
	for _, backend := range []string{"generator", "inspector"} {
		t.Run(backend, func(t *testing.T) {
			root, value, source := descriptorTestProject(t)
			originalSource := `syntax = "proto3";
import "shared/types.proto";
package example.orders.v1;
service Orders {}
`
			originalDependency := `syntax = "proto3";
package example.shared.v1;
message Original {}
`
			writeImportTestFile(t, root, source.Path, originalSource)
			writeImportTestFile(t, root, "shared/types.proto", originalDependency)
			resolvedRoot, err := resolveRoot(root)
			if err != nil {
				t.Fatal(err)
			}

			toolRoot := t.TempDir()
			mutated := false
			lookup := func(name string) (string, error) {
				if !mutated {
					mutated = true
					writeImportTestFile(t, root, source.Path, `syntax = "proto3"; package changed;`)
					writeImportTestFile(t, root, "shared/types.proto", `syntax = "proto3"; message Changed {}`)
				}
				return filepath.Join(toolRoot, name+".exe"), nil
			}

			var inputRoot string
			var outputRoot string
			runner := runnerFunc(func(_ context.Context, command Command) ([]byte, error) {
				inputRoot = commandProtoPath(t, command)
				outputRoot = command.Dir
				if filepath.Clean(inputRoot) == filepath.Clean(resolvedRoot) ||
					filepath.Clean(inputRoot) == filepath.Clean(outputRoot) {
					t.Fatalf("protoc roots input=%q output=%q live=%q", inputRoot, outputRoot, resolvedRoot)
				}
				liveRootKey := strings.ToLower(filepath.Clean(resolvedRoot))
				for _, argument := range append([]string{command.Path, command.Dir}, command.Args...) {
					if strings.Contains(strings.ToLower(argument), liveRootKey) {
						t.Fatalf("protoc command exposes live project root in %q", argument)
					}
				}
				for name, want := range map[string]string{
					source.Path:          originalSource,
					"shared/types.proto": originalDependency,
				} {
					content, err := os.ReadFile(filepath.Join(inputRoot, filepath.FromSlash(name)))
					if err != nil {
						t.Fatalf("read snapshot %q: %v", name, err)
					}
					if string(content) != want {
						t.Fatalf("snapshot %q = %q, want pre-lookup content %q", name, content, want)
					}
				}
				switch backend {
				case "generator":
					writeFakeProtocOutput(t, command.Dir, source.Path, source.Package)
				case "inspector":
					writeDescriptorContract(t, command.Dir, source.Path)
				}
				return nil, nil
			})

			switch backend {
			case "generator":
				_, err = (Generator{Root: root, LookupTool: lookup, Runner: runner}).Plan(value, source)
			case "inspector":
				_, err = (Inspector{Root: root, LookupTool: lookup, Runner: runner}).Inspect(value, source)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !mutated || inputRoot == "" || outputRoot == "" {
				t.Fatalf("snapshot seam mutated=%v input=%q output=%q", mutated, inputRoot, outputRoot)
			}
			for _, temporary := range []string{inputRoot, outputRoot} {
				if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("temporary directory %q remains: %v", temporary, err)
				}
			}
		})
	}
}

func commandProtoPath(t *testing.T, command Command) string {
	t.Helper()
	if len(command.Args) == 0 || !strings.HasPrefix(command.Args[0], "--proto_path=") {
		t.Fatalf("protoc args = %v, want --proto_path first", command.Args)
	}
	return strings.TrimPrefix(command.Args[0], "--proto_path=")
}

func writeImportTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
