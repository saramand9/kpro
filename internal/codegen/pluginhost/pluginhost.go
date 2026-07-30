// Package pluginhost runs external code generators through a small, versioned
// JSON protocol and converts their virtual outputs into a validated codegen
// Plan.
//
// The protocol does not give a plugin a project directory or a file-writing
// callback. The plugin receives source bytes on stdin, runs with an empty
// temporary directory as its working directory, and returns virtual files on
// stdout. Only the caller may later pass the returned Plan to codegen.Execute.
//
// This is protocol and write-path isolation, not a process sandbox. A plugin is
// executable code with the operating-system permissions of the current
// process, and must therefore still be trusted accordingly.
package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

const (
	// ProtocolVersionV1Alpha1 is the first external-generator wire contract.
	ProtocolVersionV1Alpha1 = "kpro.dev/codegen-plugin/v1alpha1"

	// Defaults bound a plugin that does not provide explicit Host limits.
	DefaultTimeout        = 30 * time.Second
	DefaultMaxStdoutBytes = int64(8 << 20)
	DefaultMaxStderrBytes = int64(1 << 20)

	maxDiagnosticBytes = 4 << 10
)

var (
	// ErrProtocol identifies an invalid or unsupported plugin response.
	ErrProtocol = errors.New("plugin host: invalid protocol response")
	// ErrOutputLimit identifies stdout or stderr exceeding the configured cap.
	ErrOutputLimit = errors.New("plugin host: output limit exceeded")
)

// ManifestMetadata is the manifest context needed by a source generator.
// Runtime configuration, credentials, project roots, and unrelated sources are
// intentionally absent.
type ManifestMetadata struct {
	APIVersion string `json:"apiVersion"`
	Module     string `json:"module"`
	Service    string `json:"service"`
}

// SourceInput carries one source's normalized metadata and already-read bytes.
// Content is encoded as base64 by encoding/json.
type SourceInput struct {
	ID      string              `json:"id"`
	Kind    manifest.SourceKind `json:"kind"`
	Path    string              `json:"path"`
	Package string              `json:"package"`
	Content []byte              `json:"content"`
}

// Request is written as exactly one JSON value to plugin stdin.
type Request struct {
	ProtocolVersion string           `json:"protocolVersion"`
	Manifest        ManifestMetadata `json:"manifest"`
	Source          SourceInput      `json:"source"`
}

// Response is read as exactly one JSON value from plugin stdout.
type Response struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Files           []ResponseFile `json:"files"`
}

// ResponseFile is one virtual output returned by a plugin. Mode is a Unix
// permission value such as 0644 expressed as its JSON number (420); zero asks
// codegen to use its default.
type ResponseFile struct {
	Path      string            `json:"path"`
	Content   []byte            `json:"content"`
	Ownership codegen.Ownership `json:"ownership"`
	Mode      uint32            `json:"mode"`
}

// Command is a direct process invocation. Dir is an empty temporary directory,
// and Stdin contains the encoded Request.
type Command struct {
	Path        string
	Args        []string
	Dir         string
	Stdin       []byte
	StdoutLimit int64
	StderrLimit int64
}

// RunResult contains bounded process output. A Runner must set the corresponding
// exceeded flag if it discarded bytes beyond a limit.
type RunResult struct {
	Stdout         []byte
	Stderr         []byte
	StdoutExceeded bool
	StderrExceeded bool
}

// Runner is the process-execution seam used by Host.
type Runner interface {
	Run(context.Context, Command) (RunResult, error)
}

// ExecRunner invokes plugins directly with exec.CommandContext. It never uses
// a command shell.
type ExecRunner struct{}

// Run executes one bounded command.
func (ExecRunner) Run(ctx context.Context, command Command) (RunResult, error) {
	if ctx == nil {
		return RunResult{}, errors.New("plugin host: context is nil")
	}
	if command.Path == "" {
		return RunResult{}, errors.New("plugin host: plugin path is empty")
	}
	if command.StdoutLimit <= 0 || command.StderrLimit <= 0 {
		return RunResult{}, errors.New("plugin host: process output limits must be positive")
	}

	stdout := newCappedBuffer(command.StdoutLimit)
	stderr := newCappedBuffer(command.StderrLimit)
	process := exec.CommandContext(ctx, command.Path, command.Args...)
	process.Dir = command.Dir
	process.Stdin = bytes.NewReader(command.Stdin)
	process.Stdout = stdout
	process.Stderr = stderr
	err := process.Run()
	return RunResult{
		Stdout:         stdout.Bytes(),
		Stderr:         stderr.Bytes(),
		StdoutExceeded: stdout.Exceeded(),
		StderrExceeded: stderr.Exceeded(),
	}, err
}

// Host configures one external generator executable.
type Host struct {
	Path string
	Args []string

	// Zero values select the package defaults.
	Timeout        time.Duration
	MaxStdoutBytes int64
	MaxStderrBytes int64

	// Runner defaults to ExecRunner.
	Runner Runner
}

// Plan runs the plugin using sourceContent as its only contract data and
// returns a fully validated virtual plan. It does not write into the project.
func (h Host) Plan(
	ctx context.Context,
	value manifest.Manifest,
	source manifest.Source,
	sourceContent []byte,
) (codegen.Plan, error) {
	if ctx == nil {
		return codegen.Plan{}, errors.New("plugin host: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return codegen.Plan{}, fmt.Errorf("plugin host: canceled before start: %w", err)
	}
	if h.Path == "" {
		return codegen.Plan{}, errors.New("plugin host: plugin path is empty")
	}
	if err := value.Validate(); err != nil {
		return codegen.Plan{}, fmt.Errorf("plugin host: invalid manifest: %w", err)
	}
	if !manifestContains(value, source) {
		return codegen.Plan{}, fmt.Errorf("plugin host: source %q is not present in the manifest", source.ID)
	}

	timeout, stdoutLimit, stderrLimit, err := h.limits()
	if err != nil {
		return codegen.Plan{}, err
	}
	request := Request{
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
			Content: append([]byte(nil), sourceContent...),
		},
	}
	input, err := json.Marshal(request)
	if err != nil {
		return codegen.Plan{}, fmt.Errorf("plugin host: encode request: %w", err)
	}

	workingDirectory, err := os.MkdirTemp("", "kpro-codegen-plugin-*")
	if err != nil {
		return codegen.Plan{}, fmt.Errorf("plugin host: create temporary working directory: %w", err)
	}
	defer os.RemoveAll(workingDirectory)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runner := h.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	result, runErr := runner.Run(runCtx, Command{
		Path:        h.Path,
		Args:        append([]string(nil), h.Args...),
		Dir:         workingDirectory,
		Stdin:       input,
		StdoutLimit: stdoutLimit,
		StderrLimit: stderrLimit,
	})
	if err := checkOutputLimits(result, stdoutLimit, stderrLimit); err != nil {
		return codegen.Plan{}, err
	}
	if runErr != nil {
		if err := runCtx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return codegen.Plan{}, fmt.Errorf("plugin host: plugin timed out after %s: %w", timeout, err)
			}
			return codegen.Plan{}, fmt.Errorf("plugin host: plugin canceled: %w", err)
		}
		detail := diagnostic(result.Stderr)
		if detail == "" {
			return codegen.Plan{}, fmt.Errorf("plugin host: plugin process failed: %w", runErr)
		}
		return codegen.Plan{}, fmt.Errorf("plugin host: plugin process failed: %w: %s", runErr, detail)
	}

	response, err := decodeResponse(result.Stdout)
	if err != nil {
		return codegen.Plan{}, err
	}
	files := make([]codegen.File, 0, len(response.Files))
	for i, file := range response.Files {
		if file.Mode&^uint32(0o777) != 0 {
			return codegen.Plan{}, fmt.Errorf(
				"%w: file[%d] %q has invalid mode %#o",
				ErrProtocol, i, file.Path, file.Mode,
			)
		}
		files = append(files, codegen.File{
			Path:      file.Path,
			Content:   append([]byte(nil), file.Content...),
			Ownership: file.Ownership,
			Mode:      fs.FileMode(file.Mode),
		})
	}
	plan, err := codegen.BuildPlan(files...)
	if err != nil {
		return codegen.Plan{}, fmt.Errorf("%w: validate files: %w", ErrProtocol, err)
	}
	return plan, nil
}

func (h Host) limits() (time.Duration, int64, int64, error) {
	timeout := h.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	stdout := h.MaxStdoutBytes
	if stdout == 0 {
		stdout = DefaultMaxStdoutBytes
	}
	stderr := h.MaxStderrBytes
	if stderr == 0 {
		stderr = DefaultMaxStderrBytes
	}
	if timeout < 0 {
		return 0, 0, 0, errors.New("plugin host: timeout must not be negative")
	}
	if stdout < 0 || stderr < 0 {
		return 0, 0, 0, errors.New("plugin host: output limits must not be negative")
	}
	return timeout, stdout, stderr, nil
}

func manifestContains(value manifest.Manifest, source manifest.Source) bool {
	for _, candidate := range value.Sources {
		if reflect.DeepEqual(candidate, source) {
			return true
		}
	}
	return false
}

func checkOutputLimits(result RunResult, stdoutLimit, stderrLimit int64) error {
	if result.StdoutExceeded || int64(len(result.Stdout)) > stdoutLimit {
		return fmt.Errorf("%w: stdout exceeds %d bytes", ErrOutputLimit, stdoutLimit)
	}
	if result.StderrExceeded || int64(len(result.Stderr)) > stderrLimit {
		return fmt.Errorf("%w: stderr exceeds %d bytes", ErrOutputLimit, stderrLimit)
	}
	return nil
}

func decodeResponse(data []byte) (Response, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("%w: decode JSON: %v", ErrProtocol, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Response{}, fmt.Errorf("%w: multiple JSON values", ErrProtocol)
		}
		return Response{}, fmt.Errorf("%w: trailing JSON data: %v", ErrProtocol, err)
	}
	if response.ProtocolVersion != ProtocolVersionV1Alpha1 {
		return Response{}, fmt.Errorf(
			"%w: unsupported protocolVersion %q",
			ErrProtocol, response.ProtocolVersion,
		)
	}
	if len(response.Files) == 0 {
		return Response{}, fmt.Errorf("%w: response contains no files", ErrProtocol)
	}
	return response, nil
}

func diagnostic(data []byte) string {
	data = bytes.TrimSpace(data)
	if len(data) > maxDiagnosticBytes {
		data = append(append([]byte(nil), data[:maxDiagnosticBytes]...), []byte("...")...)
	}
	return strings.TrimSpace(string(data))
}

type cappedBuffer struct {
	limit    int64
	data     []byte
	exceeded bool
}

func newCappedBuffer(limit int64) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	inputLength := len(data)
	remaining := b.limit - int64(len(b.data))
	if remaining > 0 {
		keep := int64(len(data))
		if keep > remaining {
			keep = remaining
		}
		b.data = append(b.data, data[:int(keep)]...)
	}
	if int64(inputLength) > remaining {
		b.exceeded = true
	}
	return inputLength, nil
}

func (b *cappedBuffer) Bytes() []byte {
	return append([]byte(nil), b.data...)
}

func (b *cappedBuffer) Exceeded() bool {
	return b.exceeded
}
