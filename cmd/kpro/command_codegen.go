package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
	openapigen "github.com/saramand9/kpro/internal/codegen/openapi"
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
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: kpro generate [--config kpro.gen.yaml] [--root dir] [--check] [--dry-run]")
	}
	if *check && *dryRun {
		return errors.New("generate: --check and --dry-run cannot be used together")
	}

	input, err := loadCodegenInput(*root, *config)
	if err != nil {
		return err
	}
	plan, err := buildGenerationPlan(input.root, input.manifest)
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

func buildGenerationPlan(root string, value manifest.Manifest) (codegen.Plan, error) {
	files := make([]codegen.File, 0)
	for _, source := range value.Sources {
		generator, ok := sourceGenerators[source.Kind]
		if !ok || generator == nil {
			return codegen.Plan{}, fmt.Errorf("generate source %q: unsupported source kind %q (no generator backend registered)", source.ID, source.Kind)
		}
		sourcePlan, err := generator.Plan(root, value, source)
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

func planOpenAPI(root string, value manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
	filename := filepath.Join(root, filepath.FromSlash(source.Path))
	document, err := openapigen.Load(filename)
	if err != nil {
		return codegen.Plan{}, err
	}
	return openapigen.BuildPlan(document, openapigen.Options{
		Module: value.Module,
		Output: source.Package,
	})
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
