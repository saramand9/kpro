package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

type runnerFunc func(context.Context, Command) (RunResult, error)

func (f runnerFunc) Run(ctx context.Context, command Command) (RunResult, error) {
	return f(ctx, command)
}

func TestHostSendsMinimalInputAndBuildsDeterministicPlan(t *testing.T) {
	value, source := testManifest()
	sourceContent := []byte("source contract")
	var workingDirectory string
	host := Host{
		Path: "test-plugin",
		Runner: runnerFunc(func(_ context.Context, command Command) (RunResult, error) {
			workingDirectory = command.Dir
			info, err := os.Stat(command.Dir)
			if err != nil {
				t.Fatalf("temporary working directory: %v", err)
			}
			if !info.IsDir() {
				t.Fatalf("working directory is not a directory: %s", command.Dir)
			}
			if command.StdoutLimit != DefaultMaxStdoutBytes || command.StderrLimit != DefaultMaxStderrBytes {
				t.Fatalf("unexpected defaults: stdout=%d stderr=%d", command.StdoutLimit, command.StderrLimit)
			}

			var request Request
			decoder := json.NewDecoder(bytes.NewReader(command.Stdin))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			want := Request{
				ProtocolVersion: ProtocolVersionV1Alpha1,
				Manifest: ManifestMetadata{
					APIVersion: value.APIVersion,
					Module:     value.Module,
					Service:    value.Service,
				},
				Source: SourceInput{
					ID:      source.ID,
					Kind:    source.Kind,
					Path:    source.Path,
					Package: source.Package,
					Content: sourceContent,
				},
			}
			if !reflect.DeepEqual(request, want) {
				t.Fatalf("request mismatch:\n got: %#v\nwant: %#v", request, want)
			}
			if bytes.Contains(command.Stdin, []byte(`"root"`)) ||
				bytes.Contains(command.Stdin, []byte(`"sources"`)) {
				t.Fatalf("request leaked project or unrelated-source metadata: %s", command.Stdin)
			}

			return encodedResult(t, Response{
				ProtocolVersion: ProtocolVersionV1Alpha1,
				Files: []ResponseFile{
					{
						Path:      "z/readme.txt",
						Content:   []byte("starter"),
						Ownership: codegen.Scaffold,
						Mode:      0o644,
					},
					{
						Path:      "a/types.gen.go",
						Content:   []byte("package a\n"),
						Ownership: codegen.Generated,
						Mode:      0o600,
					},
				},
			}), nil
		}),
	}

	plan, err := host.Plan(context.Background(), value, source, sourceContent)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	files, err := plan.OrderedFiles()
	if err != nil {
		t.Fatalf("OrderedFiles: %v", err)
	}
	if got, want := []string{files[0].Path, files[1].Path}, []string{"a/types.gen.go", "z/readme.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	if files[0].Mode != 0o600 || files[1].Ownership != codegen.Scaffold {
		t.Fatalf("response metadata was not preserved: %#v", files)
	}
	if _, err := os.Stat(workingDirectory); !os.IsNotExist(err) {
		t.Fatalf("temporary working directory remains after Plan: %v", err)
	}
}

func TestHostRejectsInvalidProtocolResponses(t *testing.T) {
	version := ProtocolVersionV1Alpha1
	cases := []struct {
		name   string
		stdout string
		match  string
	}{
		{
			name:   "malformed JSON",
			stdout: `{`,
			match:  "decode JSON",
		},
		{
			name:   "multiple JSON values",
			stdout: fmt.Sprintf(`{"protocolVersion":%q,"files":[]} {}`, version),
			match:  "multiple JSON values",
		},
		{
			name:   "unknown field",
			stdout: fmt.Sprintf(`{"protocolVersion":%q,"files":[],"projectRoot":"secret"}`, version),
			match:  `unknown field "projectRoot"`,
		},
		{
			name:   "unsupported version",
			stdout: `{"protocolVersion":"kpro.dev/codegen-plugin/v9","files":[]}`,
			match:  "unsupported protocolVersion",
		},
		{
			name:   "empty files",
			stdout: fmt.Sprintf(`{"protocolVersion":%q,"files":[]}`, version),
			match:  "response contains no files",
		},
		{
			name:   "malformed content",
			stdout: fmt.Sprintf(`{"protocolVersion":%q,"files":[{"path":"x","content":"***","ownership":"generated","mode":420}]}`, version),
			match:  "illegal base64",
		},
		{
			name: "duplicate path",
			stdout: fmt.Sprintf(
				`{"protocolVersion":%q,"files":[`+
					`{"path":"same.go","content":"","ownership":"generated","mode":420},`+
					`{"path":"same.go","content":"","ownership":"generated","mode":420}]}`,
				version,
			),
			match: "duplicate output path",
		},
		{
			name:   "escaping path",
			stdout: fmt.Sprintf(`{"protocolVersion":%q,"files":[{"path":"../escape","content":"","ownership":"generated","mode":420}]}`, version),
			match:  "unsafe relative path",
		},
		{
			name:   "invalid ownership",
			stdout: fmt.Sprintf(`{"protocolVersion":%q,"files":[{"path":"x","content":"","ownership":"plugin","mode":420}]}`, version),
			match:  `invalid ownership "plugin"`,
		},
		{
			name:   "invalid mode",
			stdout: fmt.Sprintf(`{"protocolVersion":%q,"files":[{"path":"x","content":"","ownership":"generated","mode":4096}]}`, version),
			match:  "invalid mode",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			host := Host{
				Path: "test-plugin",
				Runner: runnerFunc(func(context.Context, Command) (RunResult, error) {
					return RunResult{Stdout: []byte(test.stdout)}, nil
				}),
			}
			value, source := testManifest()
			_, err := host.Plan(context.Background(), value, source, nil)
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", err)
			}
			if !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %q, want substring %q", err, test.match)
			}
		})
	}
}

func TestHostRejectsRunnerOutputBeyondLimits(t *testing.T) {
	value, source := testManifest()
	cases := []struct {
		name   string
		result RunResult
		match  string
	}{
		{
			name:   "reported stdout truncation",
			result: RunResult{StdoutExceeded: true},
			match:  "stdout exceeds 16 bytes",
		},
		{
			name:   "unreported stdout overflow",
			result: RunResult{Stdout: bytes.Repeat([]byte("x"), 17)},
			match:  "stdout exceeds 16 bytes",
		},
		{
			name:   "reported stderr truncation",
			result: RunResult{StderrExceeded: true},
			match:  "stderr exceeds 12 bytes",
		},
		{
			name:   "unreported stderr overflow",
			result: RunResult{Stderr: bytes.Repeat([]byte("x"), 13)},
			match:  "stderr exceeds 12 bytes",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			host := Host{
				Path:           "test-plugin",
				MaxStdoutBytes: 16,
				MaxStderrBytes: 12,
				Runner: runnerFunc(func(context.Context, Command) (RunResult, error) {
					return test.result, nil
				}),
			}
			_, err := host.Plan(context.Background(), value, source, nil)
			if !errors.Is(err, ErrOutputLimit) {
				t.Fatalf("error = %v, want ErrOutputLimit", err)
			}
			if !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %q, want substring %q", err, test.match)
			}
		})
	}
}

func TestExecRunnerHelperSuccessUsesTemporaryWorkingDirectory(t *testing.T) {
	value, source := testManifest()
	host := helperHost("success")
	plan, err := host.Plan(context.Background(), value, source, []byte("contract bytes"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	files, err := plan.OrderedFiles()
	if err != nil {
		t.Fatalf("OrderedFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "generated/helper.txt" {
		t.Fatalf("unexpected files: %#v", files)
	}
	var helper struct {
		WorkingDirectory string `json:"workingDirectory"`
		Content          []byte `json:"content"`
		LiteralArg       string `json:"literalArg"`
	}
	if err := json.Unmarshal(files[0].Content, &helper); err != nil {
		t.Fatalf("decode helper output: %v", err)
	}
	if string(helper.Content) != "contract bytes" {
		t.Fatalf("source content = %q", helper.Content)
	}
	if helper.LiteralArg != `literal && $(not-a-shell) ; value` {
		t.Fatalf("argument was not passed literally: %q", helper.LiteralArg)
	}
	repositoryDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if samePath(helper.WorkingDirectory, repositoryDirectory) {
		t.Fatalf("plugin ran in repository directory %q", repositoryDirectory)
	}
	if _, err := os.Stat(helper.WorkingDirectory); !os.IsNotExist(err) {
		t.Fatalf("plugin temporary directory remains after Plan: %v", err)
	}
}

func TestExecRunnerHelperReportsNonzeroExit(t *testing.T) {
	value, source := testManifest()
	_, err := helperHost("nonzero").Plan(context.Background(), value, source, nil)
	if err == nil {
		t.Fatal("Plan succeeded, want process failure")
	}
	if !strings.Contains(err.Error(), "exit status") ||
		!strings.Contains(err.Error(), "helper diagnostic") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecRunnerHelperEnforcesStdoutAndStderrLimits(t *testing.T) {
	value, source := testManifest()
	for _, test := range []struct {
		scenario    string
		match       string
		stdoutLimit int64
		stderrLimit int64
	}{
		{scenario: "stdout-limit", match: "stdout exceeds 64 bytes", stdoutLimit: 64, stderrLimit: 1024},
		{scenario: "stderr-limit", match: "stderr exceeds 64 bytes", stdoutLimit: 1024, stderrLimit: 64},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			host := helperHost(test.scenario)
			host.MaxStdoutBytes = test.stdoutLimit
			host.MaxStderrBytes = test.stderrLimit
			_, err := host.Plan(context.Background(), value, source, nil)
			if !errors.Is(err, ErrOutputLimit) {
				t.Fatalf("error = %v, want ErrOutputLimit", err)
			}
			if !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %q, want substring %q", err, test.match)
			}
		})
	}
}

func TestExecRunnerHelperTimeoutAndCancellation(t *testing.T) {
	value, source := testManifest()
	t.Run("timeout", func(t *testing.T) {
		host := helperHost("hang")
		host.Timeout = 100 * time.Millisecond
		_, err := host.Plan(context.Background(), value, source, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		host := helperHost("hang")
		host.Timeout = 5 * time.Second
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		_, err := host.Plan(ctx, value, source, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
		if !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestHostRejectsInvalidConfigurationAndSource(t *testing.T) {
	value, source := testManifest()
	cases := []struct {
		name   string
		host   Host
		ctx    context.Context
		source manifest.Source
		match  string
	}{
		{
			name:   "nil context",
			host:   Host{Path: "plugin"},
			source: source,
			match:  "context is nil",
		},
		{
			name:   "empty path",
			host:   Host{},
			ctx:    context.Background(),
			source: source,
			match:  "plugin path is empty",
		},
		{
			name:   "negative timeout",
			host:   Host{Path: "plugin", Timeout: -time.Second},
			ctx:    context.Background(),
			source: source,
			match:  "timeout must not be negative",
		},
		{
			name:   "negative output limit",
			host:   Host{Path: "plugin", MaxStdoutBytes: -1},
			ctx:    context.Background(),
			source: source,
			match:  "output limits must not be negative",
		},
		{
			name: "source not in manifest",
			host: Host{Path: "plugin"},
			ctx:  context.Background(),
			source: manifest.Source{
				ID:      "other",
				Kind:    source.Kind,
				Path:    source.Path,
				Package: source.Package,
			},
			match: "not present in the manifest",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.host.Plan(test.ctx, value, test.source, nil)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %v, want substring %q", err, test.match)
			}
		})
	}
}

func TestHostAcceptsEquivalentPluginSource(t *testing.T) {
	value, source := testManifest()
	source.Plugin = &manifest.Plugin{
		Command: "test-plugin",
		Args:    []string{"--strict"},
	}
	value.Sources[0] = source
	equivalent := source
	equivalent.Plugin = &manifest.Plugin{
		Command: "test-plugin",
		Args:    append([]string(nil), source.Plugin.Args...),
	}
	host := Host{
		Path: "test-plugin",
		Runner: runnerFunc(func(context.Context, Command) (RunResult, error) {
			return encodedResult(t, Response{
				ProtocolVersion: ProtocolVersionV1Alpha1,
				Files: []ResponseFile{{
					Path:      "generated/equivalent.txt",
					Content:   []byte("ok"),
					Ownership: codegen.Generated,
				}},
			}), nil
		}),
	}
	if _, err := host.Plan(context.Background(), value, equivalent, nil); err != nil {
		t.Fatalf("Plan with equivalent source: %v", err)
	}
}

// TestPluginHelperProcess is executed in a child process by helperHost. Keeping
// it as a real Go test binary exercises exec.CommandContext on every platform.
func TestPluginHelperProcess(t *testing.T) {
	index := helperMarkerIndex(os.Args)
	if index < 0 {
		return
	}
	if index+1 >= len(os.Args) {
		fmt.Fprintln(os.Stderr, "missing helper scenario")
		os.Exit(2)
	}
	scenario := os.Args[index+1]
	switch scenario {
	case "success":
		var request Request
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		workingDirectory, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		literal := ""
		if index+2 < len(os.Args) {
			literal = os.Args[index+2]
		}
		content, err := json.Marshal(struct {
			WorkingDirectory string `json:"workingDirectory"`
			Content          []byte `json:"content"`
			LiteralArg       string `json:"literalArg"`
		}{
			WorkingDirectory: workingDirectory,
			Content:          request.Source.Content,
			LiteralArg:       literal,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersionV1Alpha1,
			Files: []ResponseFile{{
				Path:      "generated/helper.txt",
				Content:   content,
				Ownership: codegen.Generated,
				Mode:      0o644,
			}},
		})
	case "nonzero":
		fmt.Fprintln(os.Stderr, "helper diagnostic")
		os.Exit(23)
	case "stdout-limit":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 1024))
	case "stderr-limit":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 1024))
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersionV1Alpha1,
		})
	case "hang":
		time.Sleep(30 * time.Second)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper scenario %q\n", scenario)
		os.Exit(2)
	}
	os.Exit(0)
}

func helperHost(scenario string) Host {
	return Host{
		Path: os.Args[0],
		Args: []string{
			"-test.run=^TestPluginHelperProcess$",
			"--",
			"plugin-helper",
			scenario,
			`literal && $(not-a-shell) ; value`,
		},
	}
}

func helperMarkerIndex(args []string) int {
	for i, arg := range args {
		if arg == "plugin-helper" {
			return i
		}
	}
	return -1
}

func encodedResult(t *testing.T, response Response) RunResult {
	t.Helper()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return RunResult{Stdout: data}
}

func testManifest() (manifest.Manifest, manifest.Source) {
	source := manifest.Source{
		ID:      "messages",
		Kind:    manifest.KindMessage,
		Path:    "api/messages.yaml",
		Package: "internal/messages",
	}
	return manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/service",
		Service:    "service",
		Sources:    []manifest.Source{source},
	}, source
}

func samePath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
