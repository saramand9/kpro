// Scaffold support for the kpro new command.
package main

import (
	"bytes"
	"embed"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"text/template"

	"github.com/saramand9/kpro/internal/codegen"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

//go:embed templates/service/* templates/profiles/*
var templateFiles embed.FS

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const (
	kproModulePath           = "github.com/saramand9/kpro"
	developmentModuleVersion = "v0.0.0"
)

var (
	version = "devel"
	commit  = "unknown"
	date    = "unknown"
)

type scaffoldData struct {
	Module            string
	ServiceName       string
	ServiceIdentifier string
	KProModule        string
	KProVersion       string
	KProReplace       string
}

type scaffoldTemplate struct {
	source string
	target string
	mode   os.FileMode
}

var serviceScaffoldTemplates = []scaffoldTemplate{
	{source: "templates/service/go.mod.tmpl", target: "go.mod", mode: 0o644},
	{source: "templates/service/main.go.tmpl", target: "main.go", mode: 0o644},
	{source: "templates/service/README.md.tmpl", target: "README.md", mode: 0o644},
	{source: "templates/service/gitignore.tmpl", target: ".gitignore", mode: 0o644},
}

var httpAPIScaffoldTemplates = []scaffoldTemplate{
	{source: "templates/profiles/http-api/go.mod.tmpl", target: "go.mod", mode: 0o644},
	{source: "templates/profiles/http-api/main.go.tmpl", target: "cmd/{{.ServiceName}}/main.go", mode: 0o644},
	{source: "templates/profiles/http-api/service.go.tmpl", target: "internal/api/service/api.go", mode: 0o644},
	{source: "templates/profiles/http-api/openapi.yaml.tmpl", target: "api/openapi.yaml", mode: 0o644},
	{source: "templates/profiles/http-api/kpro.gen.yaml.tmpl", target: "kpro.gen.yaml", mode: 0o644},
	{source: "templates/profiles/http-api/README.md.tmpl", target: "README.md", mode: 0o644},
	{source: "templates/profiles/http-api/gitignore.tmpl", target: ".gitignore", mode: 0o644},
}

func scaffoldNew(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	flags.SetOutput(stderr)
	modulePath := flags.String("module", "", "Go module path (default example.com/<directory>)")
	serviceName := flags.String("name", "", "service name (default directory name)")
	kproReplace := flags.String("kpro-replace", "", "local KPro checkout (auto-detected from the current directory)")
	profile := flags.String("profile", "", "project profile (supported: minimal, http-api)")
	parseArgs := args
	// Accept both the natural `new <directory> --module ...` form and Go's
	// conventional `new --module ... <directory>` form.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		parseArgs = append(append([]string(nil), args[1:]...), args[0])
	}
	if err := flags.Parse(parseArgs); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: kpro new <directory> [--module example.com/service] [--name service] [--kpro-replace path] [--profile minimal|http-api]")
	}
	var templates []scaffoldTemplate
	switch *profile {
	case "", "minimal":
		templates = serviceScaffoldTemplates
	case "http-api":
		templates = httpAPIScaffoldTemplates
	default:
		return fmt.Errorf("unsupported project profile %q (supported: minimal, http-api)", *profile)
	}
	target, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	base := filepath.Base(filepath.Clean(target))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return errors.New("target directory must have a service name")
	}
	if *modulePath == "" {
		*modulePath = "example.com/" + strings.ToLower(base)
	}
	if err := module.CheckPath(*modulePath); err != nil {
		return fmt.Errorf("invalid module path %q: %w", *modulePath, err)
	}
	if *serviceName == "" {
		*serviceName = base
	}
	if !serviceNamePattern.MatchString(*serviceName) {
		return fmt.Errorf("invalid service name %q: use 1-64 letters, digits, '.', '_' or '-'", *serviceName)
	}
	replacePath, err := resolveKProReplace(*kproReplace)
	if err != nil {
		return err
	}
	kproVersion := developmentModuleVersion
	if replacePath == "" {
		var ok bool
		kproVersion, ok = resolvedReleaseVersion()
		if !ok {
			return errors.New("KPro release version is unavailable; run from a KPro checkout or pass --kpro-replace <checkout>")
		}
	}
	if err := ensureEmptyTarget(target); err != nil {
		return err
	}
	quotedReplace := ""
	if replacePath != "" {
		quotedReplace = strconv.Quote(filepath.ToSlash(replacePath))
	}
	data := scaffoldData{
		Module:            *modulePath,
		ServiceName:       *serviceName,
		ServiceIdentifier: goIdentifier(*serviceName),
		KProModule:        kproModulePath,
		KProVersion:       kproVersion,
		KProReplace:       quotedReplace,
	}
	for _, file := range templates {
		if err := renderTemplate(target, file.source, file.target, file.mode, data); err != nil {
			return err
		}
	}
	if *profile == "http-api" {
		if err := runGenerate([]string{"--root", target}, io.Discard, io.Discard); err != nil {
			return fmt.Errorf("generate http-api bindings: %w", err)
		}
	}
	if replacePath != "" {
		fmt.Fprintf(stdout, "created %s\nusing KPro source %s\n", *serviceName, replacePath)
	} else {
		fmt.Fprintf(stdout, "created %s\nusing KPro %s\n", *serviceName, kproVersion)
	}
	runTarget := "."
	if *profile == "http-api" {
		runTarget = "./cmd/" + *serviceName
	}
	fmt.Fprintf(stdout, "\nnext:\n  cd %s\n  go mod tidy\n  go run %s\n", target, runTarget)
	return nil
}

func goIdentifier(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	identifier := builder.String()
	if identifier == "" || identifier[0] >= '0' && identifier[0] <= '9' {
		identifier = "service_" + identifier
	}
	if token.Lookup(identifier).IsKeyword() {
		identifier += "_service"
	}
	return identifier
}
func resolveKProReplace(configured string) (string, error) {
	if configured != "" {
		path, err := filepath.Abs(configured)
		if err != nil {
			return "", fmt.Errorf("resolve KPro checkout: %w", err)
		}
		if err := validateKProRoot(path); err != nil {
			return "", err
		}
		return path, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}
	for candidate := cwd; ; candidate = filepath.Dir(candidate) {
		if validateKProRoot(candidate) == nil {
			return candidate, nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	if _, ok := resolvedReleaseVersion(); ok {
		return "", nil
	}
	return "", errors.New("KPro release version is unavailable; run from a KPro checkout or pass --kpro-replace <checkout>")
}

func validateKProRoot(path string) error {
	data, err := os.ReadFile(filepath.Join(path, "go.mod"))
	if err != nil {
		return fmt.Errorf("KPro checkout %q: read go.mod: %w", path, err)
	}
	if got := modfile.ModulePath(data); got != kproModulePath {
		return fmt.Errorf("KPro checkout %q has module %q", path, got)
	}
	return nil
}

func resolvedReleaseVersion() (string, bool) {
	resolvedVersion, _, _ := resolvedBuildMetadata()
	if !strings.HasPrefix(resolvedVersion, "v") {
		resolvedVersion = "v" + resolvedVersion
	}
	if !semver.IsValid(resolvedVersion) || semver.Prerelease(resolvedVersion) == "-devel" {
		return "", false
	}
	return resolvedVersion, true
}

func resolvedBuildMetadata() (resolvedVersion, resolvedCommit, resolvedDate string) {
	resolvedVersion, resolvedCommit, resolvedDate = version, commit, date
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return resolvedVersion, resolvedCommit, resolvedDate
	}
	if _, release := normalizeReleaseVersion(resolvedVersion); !release {
		if buildVersion, valid := normalizeReleaseVersion(info.Main.Version); valid {
			resolvedVersion = buildVersion
		}
	} else if normalized, valid := normalizeReleaseVersion(resolvedVersion); valid {
		resolvedVersion = normalized
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if resolvedCommit == "" || resolvedCommit == "unknown" {
				resolvedCommit = setting.Value
			}
		case "vcs.time":
			if resolvedDate == "" || resolvedDate == "unknown" {
				resolvedDate = setting.Value
			}
		}
	}
	return resolvedVersion, resolvedCommit, resolvedDate
}

func normalizeReleaseVersion(candidate string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || candidate == "devel" || candidate == "(devel)" || candidate == "unknown" {
		return "", false
	}
	if !strings.HasPrefix(candidate, "v") {
		candidate = "v" + candidate
	}
	return candidate, semver.IsValid(candidate)
}

func ensureEmptyTarget(target string) error {
	entries, err := os.ReadDir(target)
	switch {
	case err == nil && len(entries) > 0:
		return fmt.Errorf("target directory %q is not empty", target)
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create target: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("inspect target: %w", err)
	}
}

func renderTemplate(root, source, target string, mode os.FileMode, data scaffoldData) error {
	templateBody, err := templateFiles.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read embedded template %s: %w", source, err)
	}
	parsed, err := template.New(source).Option("missingkey=error").Parse(string(templateBody))
	if err != nil {
		return fmt.Errorf("parse embedded template %s: %w", source, err)
	}
	target, err = renderString(target, data)
	if err != nil {
		return fmt.Errorf("render target %s: %w", target, err)
	}
	target, err = codegen.SafeRelativePath(target)
	if err != nil {
		return fmt.Errorf("unsafe template target %q: %w", target, err)
	}
	path := filepath.Join(root, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", target, err)
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, data); err != nil {
		return fmt.Errorf("render %s: %w", target, err)
	}
	content := rendered.Bytes()
	if strings.EqualFold(filepath.Ext(target), ".go") {
		content, err = format.Source(content)
		if err != nil {
			return fmt.Errorf("gofmt %s: %w", target, err)
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write %s: %w", target, writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close %s: %w", target, closeErr)
	}
	return nil
}

func renderString(value string, data scaffoldData) (string, error) {
	parsed, err := template.New("target").Option("missingkey=error").Parse(value)
	if err != nil {
		return "", err
	}
	var rendered strings.Builder
	if err := parsed.Execute(&rendered, data); err != nil {
		return "", err
	}
	return rendered.String(), nil
}
