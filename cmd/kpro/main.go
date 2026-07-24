// Command kpro contains developer tooling for KPro projects.
package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"text/template"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

//go:embed templates/service/*
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
	Module      string
	ServiceName string
	KProModule  string
	KProVersion string
	KProReplace string
}

var scaffoldTemplates = []struct {
	source string
	target string
	mode   os.FileMode
}{
	{source: "templates/service/go.mod.tmpl", target: "go.mod", mode: 0o644},
	{source: "templates/service/main.go.tmpl", target: "main.go", mode: 0o644},
	{source: "templates/service/README.md.tmpl", target: "README.md", mode: 0o644},
	{source: "templates/service/gitignore.tmpl", target: ".gitignore", mode: 0o644},
}

func main() {
	if err := execute(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "kpro:", err)
		os.Exit(1)
	}
}

func execute(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "new":
		return runNew(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run 'kpro help'", args[0])
	}
}

func runVersion(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: kpro version")
	}
	resolvedVersion, resolvedCommit, resolvedDate := resolvedBuildMetadata()
	fmt.Fprintf(stdout, "kpro %s (commit %s, built %s, %s/%s, %s)\n", resolvedVersion, resolvedCommit, resolvedDate, runtime.GOOS, runtime.GOARCH, runtime.Version())
	return nil
}

func runDoctor(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: kpro doctor")
	}
	fmt.Fprintf(stdout, "go: %s\nplatform: %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if info, ok := debug.ReadBuildInfo(); ok {
		moduleVersion := info.Main.Version
		if moduleVersion == "" {
			moduleVersion = "(devel)"
		}
		fmt.Fprintf(stdout, "module: %s %s\n", info.Main.Path, moduleVersion)
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				fmt.Fprintf(stdout, "revision: %s\n", setting.Value)
				break
			}
		}
	}
	fmt.Fprintln(stdout, "status: ok")
	return nil
}

func runNew(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	flags.SetOutput(stderr)
	modulePath := flags.String("module", "", "Go module path (default example.com/<directory>)")
	serviceName := flags.String("name", "", "service name (default directory name)")
	kproReplace := flags.String("kpro-replace", "", "local KPro checkout (auto-detected from the current directory)")
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
		return errors.New("usage: kpro new <directory> [--module example.com/service] [--name service] [--kpro-replace path]")
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
		Module:      *modulePath,
		ServiceName: *serviceName,
		KProModule:  kproModulePath,
		KProVersion: kproVersion,
		KProReplace: quotedReplace,
	}
	for _, file := range scaffoldTemplates {
		if err := renderTemplate(target, file.source, file.target, file.mode, data); err != nil {
			return err
		}
	}
	if replacePath != "" {
		fmt.Fprintf(stdout, "created %s\nusing KPro source %s\n", *serviceName, replacePath)
	} else {
		fmt.Fprintf(stdout, "created %s\nusing KPro %s\n", *serviceName, kproVersion)
	}
	fmt.Fprintf(stdout, "\nnext:\n  cd %s\n  go mod tidy\n  go run .\n", target)
	return nil
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
	path := filepath.Join(root, target)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	executeErr := parsed.Execute(file, data)
	closeErr := file.Close()
	if executeErr != nil {
		return fmt.Errorf("render %s: %w", target, executeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", target, closeErr)
	}
	return nil
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "KPro developer tools")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  kpro new <directory> [--module example.com/service] [--name service] [--kpro-replace path]")
	fmt.Fprintln(output, "  kpro version")
	fmt.Fprintln(output, "  kpro doctor")
}
