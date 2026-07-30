package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
	"github.com/saramand9/kpro/internal/codegen/messagegen"
	openapigen "github.com/saramand9/kpro/internal/codegen/openapi"
	"github.com/saramand9/kpro/internal/codegen/pluginhost"
	"github.com/saramand9/kpro/internal/codegen/protogen"
)

const defaultCodegenConfig = "kpro.gen.yaml"

// sourceGenerator is the seam used by source-specific generators. Backends are
// deliberately registered by the package that implements them; until one is
// present, generate reports the unsupported source instead of succeeding with
// an empty plan.
type sourceGenerator interface {
	Plan(string, manifest.Manifest, manifest.Source) (codegen.Plan, error)
}

type sourceGeneratorFunc func(string, manifest.Manifest, manifest.Source) (codegen.Plan, error)

func (f sourceGeneratorFunc) Plan(root string, value manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
	return f(root, value, source)
}

var sourceGenerators = map[manifest.SourceKind]sourceGenerator{
	manifest.KindOpenAPI: sourceGeneratorFunc(planOpenAPI),
	manifest.KindProto:   sourceGeneratorFunc(planProto),
	manifest.KindMessage: sourceGeneratorFunc(planMessage),
}

type pluginPlanner interface {
	Plan(context.Context, manifest.Manifest, manifest.Source, []byte) (codegen.Plan, error)
}

var newPluginHost = func(path string, args []string) pluginPlanner {
	return pluginhost.Host{Path: path, Args: append([]string(nil), args...)}
}

type codegenInput struct {
	root     string
	config   string
	manifest manifest.Manifest
	lockFile string
}

func runValidate(args []string, stdout, stderr io.Writer) error {
	input, err := parseCodegenInput("validate", args, stderr)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "validated manifest: %s\n", input.config)
	for _, source := range input.manifest.Sources {
		fmt.Fprintf(stdout, "source %s: %s %s -> %s\n", source.ID, source.Kind, source.Path, source.Package)
	}
	return nil
}

func runGenerate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("config", defaultCodegenConfig, "generation manifest path")
	root := flags.String("root", ".", "project root")
	check := flags.Bool("check", false, "report whether generated outputs are current")
	dryRun := flags.Bool("dry-run", false, "report generated changes without writing")
	allowPlugins := flags.Bool("allow-plugins", false, "allow manifest-configured external generator processes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: kpro generate [--config kpro.gen.yaml] [--root dir] [--check] [--dry-run] [--allow-plugins]")
	}
	if *check && *dryRun {
		return errors.New("generate: --check and --dry-run cannot be used together")
	}

	input, err := loadCodegenInput(*root, *config)
	if err != nil {
		return err
	}
	plan, err := buildGenerationPlan(input.root, input.manifest, *allowPlugins)
	if err != nil {
		return err
	}

	mode := codegen.Apply
	if *check {
		mode = codegen.Check
	}
	if *dryRun {
		mode = codegen.DryRun
	}
	result, err := codegen.Execute(plan, codegen.Options{Root: input.root, LockFile: input.lockFile, Mode: mode})
	if err != nil {
		return err
	}
	printGenerationResult(stdout, input.lockFile, result)
	return nil
}

func parseCodegenInput(command string, args []string, stderr io.Writer) (codegenInput, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("config", defaultCodegenConfig, "generation manifest path")
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return codegenInput{}, err
	}
	if flags.NArg() != 0 {
		return codegenInput{}, fmt.Errorf("usage: kpro %s [--config kpro.gen.yaml] [--root dir]", command)
	}
	return loadCodegenInput(*root, *config)
}

func loadCodegenInput(root, config string) (codegenInput, error) {
	if root == "" {
		return codegenInput{}, errors.New("codegen root cannot be empty")
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return codegenInput{}, fmt.Errorf("resolve codegen root: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return codegenInput{}, fmt.Errorf("inspect codegen root: %w", err)
	}
	if !info.IsDir() {
		return codegenInput{}, fmt.Errorf("codegen root %q is not a directory", root)
	}

	resolvedConfig := config
	if !filepath.IsAbs(resolvedConfig) {
		resolvedConfig = filepath.Join(resolvedRoot, resolvedConfig)
	}
	resolvedConfig, err = filepath.Abs(resolvedConfig)
	if err != nil {
		return codegenInput{}, fmt.Errorf("resolve codegen config: %w", err)
	}
	relativeConfig, err := filepath.Rel(resolvedRoot, resolvedConfig)
	if err != nil {
		return codegenInput{}, fmt.Errorf("locate codegen config under root: %w", err)
	}
	portableConfig, err := codegen.SafeRelativePath(filepath.ToSlash(relativeConfig))
	if err != nil {
		return codegenInput{}, fmt.Errorf("codegen config must be inside root: %w", err)
	}
	lockFile := strings.TrimSuffix(portableConfig, filepath.Ext(portableConfig)) + ".lock.json"
	loaded, err := manifest.Load(resolvedConfig)
	if err != nil {
		return codegenInput{}, err
	}
	return codegenInput{root: resolvedRoot, config: resolvedConfig, manifest: loaded, lockFile: lockFile}, nil
}

func buildGenerationPlan(root string, value manifest.Manifest, allowPlugins bool) (codegen.Plan, error) {
	if !allowPlugins {
		for _, source := range value.Sources {
			if source.Plugin != nil {
				return codegen.Plan{}, fmt.Errorf(
					"generate source %q: external plugin %q requires explicit --allow-plugins authorization",
					source.ID,
					source.Plugin.Command,
				)
			}
		}
	}

	files := make([]codegen.File, 0)
	for _, source := range value.Sources {
		var (
			sourcePlan codegen.Plan
			err        error
		)
		if source.Plugin != nil {
			sourcePlan, err = planPlugin(root, value, source)
		} else {
			generator, ok := sourceGenerators[source.Kind]
			if !ok || generator == nil {
				return codegen.Plan{}, fmt.Errorf("generate source %q: unsupported source kind %q (no generator backend registered)", source.ID, source.Kind)
			}
			sourcePlan, err = generator.Plan(root, value, source)
		}
		if err != nil {
			return codegen.Plan{}, fmt.Errorf("generate source %q: %w", source.ID, err)
		}
		outputs, err := sourcePlan.OrderedFiles()
		if err != nil {
			return codegen.Plan{}, fmt.Errorf("generate source %q: %w", source.ID, err)
		}
		files = append(files, outputs...)
	}
	plan, err := codegen.BuildPlan(files...)
	if err != nil {
		return codegen.Plan{}, fmt.Errorf("build generation plan: %w", err)
	}
	return plan, nil
}

func planPlugin(root string, value manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
	if source.Plugin == nil {
		return codegen.Plan{}, errors.New("plugin configuration is missing")
	}
	command, err := resolvePluginCommand(root, source.Plugin.Command)
	if err != nil {
		return codegen.Plan{}, err
	}
	sourceFilename, err := resolveProjectFile(root, source.Path)
	if err != nil {
		return codegen.Plan{}, fmt.Errorf("resolve plugin source %q: %w", source.Path, err)
	}
	sourceContent, err := os.ReadFile(sourceFilename)
	if err != nil {
		return codegen.Plan{}, fmt.Errorf("read plugin source %q: %w", source.Path, err)
	}
	host := newPluginHost(command, source.Plugin.Args)
	if host == nil {
		return codegen.Plan{}, errors.New("plugin host factory returned nil")
	}
	return host.Plan(context.Background(), value, source, sourceContent)
}

func resolvePluginCommand(root, command string) (string, error) {
	if command == "" || strings.IndexByte(command, 0) >= 0 {
		return "", errors.New("plugin command cannot be empty or contain NUL")
	}
	if _, err := codegen.SafeRelativePath(command); err != nil {
		return "", fmt.Errorf("unsafe plugin command %q: %w", command, err)
	}
	if strings.ContainsAny(command, `/\`) {
		resolved, err := resolveProjectFile(root, command)
		if err != nil {
			return "", fmt.Errorf("resolve project-relative plugin command %q: %w", command, err)
		}
		return resolved, nil
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("find plugin command %q on PATH: %w", command, err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve plugin command %q: %w", command, err)
	}
	return absolute, nil
}

func resolveProjectFile(root, relative string) (string, error) {
	portable, err := codegen.SafeRelativePath(relative)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return "", fmt.Errorf("resolve absolute project root: %w", err)
	}
	candidate := filepath.Join(resolvedRoot, filepath.FromSlash(portable))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve project file: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve absolute project file: %w", err)
	}
	relativeToRoot, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil {
		return "", fmt.Errorf("locate project file under root: %w", err)
	}
	if _, err := codegen.SafeRelativePath(filepath.ToSlash(relativeToRoot)); err != nil {
		return "", fmt.Errorf("project file resolves outside root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect project file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("project file is not a regular file")
	}
	return resolved, nil
}

func planOpenAPI(root string, value manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
	filename, err := resolveProjectFile(root, source.Path)
	if err != nil {
		return codegen.Plan{}, fmt.Errorf("resolve OpenAPI source %q: %w", source.Path, err)
	}
	document, err := openapigen.Load(filename)
	if err != nil {
		return codegen.Plan{}, err
	}
	return openapigen.BuildPlan(document, openapigen.Options{
		Module: value.Module,
		Output: source.Package,
	})
}

func planMessage(root string, value manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
	files, err := (messagegen.Generator{Root: root}).Plan(value, source)
	if err != nil {
		return codegen.Plan{}, err
	}
	return codegen.BuildPlan(files...)
}

func planProto(root string, value manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
	files, err := (protogen.Generator{Root: root}).Plan(value, source)
	if err != nil {
		return codegen.Plan{}, err
	}
	return codegen.BuildPlan(files...)
}
func printGenerationResult(output io.Writer, lockFile string, result codegen.Result) {
	for _, path := range result.Created {
		fmt.Fprintf(output, "create %s\n", path)
	}
	for _, path := range result.Updated {
		fmt.Fprintf(output, "update %s\n", path)
	}
	for _, path := range result.Removed {
		fmt.Fprintf(output, "remove %s\n", path)
	}
	for _, path := range result.Unchanged {
		fmt.Fprintf(output, "unchanged %s\n", path)
	}
	if result.LockChanged {
		fmt.Fprintf(output, "update %s\n", lockFile)
	}
}
