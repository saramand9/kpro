package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
	"gopkg.in/yaml.v3"
)

const (
	addGRPCProtoPath   = "api/grpc/service.proto"
	addGRPCPackage     = "internal/grpcapi"
	addGRPCServicePath = "internal/grpcapi/kpro_grpc_api_service.go"
)

var (
	addLookPath              = exec.LookPath
	addCommitFileTransaction = codegen.CommitFileTransaction
)

type addOptions struct {
	root   string
	config string
	dryRun bool
}

type addFileState struct {
	content []byte
	mode    fs.FileMode
	exists  bool
}

func runAdd(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	api := flags.String("api", "", "API to add (supported: grpc)")
	root := flags.String("root", ".", "project root")
	config := flags.String("config", defaultCodegenConfig, "generation manifest path")
	dryRun := flags.Bool("dry-run", false, "report changes without writing the project")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *api == "" {
		return errors.New("usage: kpro add --api grpc [--root dir] [--config kpro.gen.yaml] [--dry-run]")
	}
	if *api != "grpc" {
		return fmt.Errorf("add: unsupported API %q (supported: grpc)", *api)
	}
	return addGRPC(addOptions{root: *root, config: *config, dryRun: *dryRun}, stdout, stderr)
}

func addGRPC(options addOptions, output, warningOutput io.Writer) error {
	project, err := inspectAddGRPCProject(options)
	if err != nil {
		return err
	}
	if project.upToDate {
		fmt.Fprintln(output, "gRPC API is already configured and generated outputs are current")
		return nil
	}

	stageRoot, err := createAddStage(project.input.root)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageRoot)
	stage, err := project.prepareStage(stageRoot)
	if err != nil {
		return err
	}
	mutations, err := project.planMutations(stage)
	if err != nil {
		return err
	}
	if options.dryRun {
		printAddMutationPreview(output, mutations, project.files)
		return nil
	}
	result, err := addCommitFileTransaction(mutations, codegen.FileTransactionOptions{Root: project.input.root})
	if err != nil {
		return fmt.Errorf("add gRPC API: commit project transaction: %w", err)
	}
	for _, warning := range result.CleanupWarnings {
		fmt.Fprintf(warningOutput, "warning: %s\n", warning)
	}
	for _, name := range result.Created {
		fmt.Fprintf(output, "create %s\n", name)
	}
	for _, name := range result.Updated {
		fmt.Fprintf(output, "update %s\n", name)
	}
	if !result.Changed() {
		fmt.Fprintln(output, "gRPC API is already configured and generated outputs are current")
		return nil
	}
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "next:")
	fmt.Fprintf(output, "  cd %s\n", project.input.root)
	fmt.Fprintln(output, "  go mod tidy")
	fmt.Fprintln(output, "  go test ./...")
	if project.input.configPath == defaultCodegenConfig {
		fmt.Fprintln(output, "  kpro generate --check")
	} else {
		fmt.Fprintf(output, "  kpro generate --config %s --check\n", project.input.configPath)
	}
	return nil
}

func canonicalAddGRPCSource(source manifest.Source) bool {
	return source.ID == "grpc-api" && source.Kind == manifest.KindProto && source.Path == addGRPCProtoPath && source.Package == addGRPCPackage && source.Plugin == nil
}

func preflightAddGRPCTools() error {
	for _, tool := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
		resolved, err := addLookPath(tool)
		if err != nil {
			return fmt.Errorf("add gRPC API: required tool %q was not found; install it and run 'kpro doctor': %w", tool, err)
		}
		if strings.TrimSpace(resolved) == "" {
			return fmt.Errorf("add gRPC API: required tool %q resolved to an empty path", tool)
		}
	}
	return nil
}

func resolveAddCodegenPaths(root, config string) (string, string, string, error) {
	if root == "" {
		return "", "", "", errors.New("codegen root cannot be empty")
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve codegen root: %w", err)
	}
	rootInfo, err := os.Lstat(resolvedRoot)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect codegen root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", "", "", fmt.Errorf("codegen root %q is not a regular directory", root)
	}
	if config == "" {
		return "", "", "", errors.New("generation manifest path cannot be empty")
	}
	resolvedConfig := config
	if !filepath.IsAbs(resolvedConfig) {
		resolvedConfig = filepath.Join(resolvedRoot, resolvedConfig)
	}
	resolvedConfig, err = filepath.Abs(resolvedConfig)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve generation manifest: %w", err)
	}
	relativeConfig, err := filepath.Rel(resolvedRoot, resolvedConfig)
	if err != nil {
		return "", "", "", fmt.Errorf("locate generation manifest under root: %w", err)
	}
	portableConfig, err := codegen.SafeRelativePath(filepath.ToSlash(relativeConfig))
	if err != nil {
		return "", "", "", fmt.Errorf("generation manifest must be inside root: %w", err)
	}
	lockFile, _ := generationLockPaths(portableConfig)
	if err := validateGenerationLockPath(portableConfig, lockFile); err != nil {
		return "", "", "", err
	}
	return resolvedRoot, portableConfig, lockFile, nil
}

func inspectAddProjectFile(root, name string) (addFileState, error) {
	portable, err := codegen.SafeRelativePath(name)
	if err != nil {
		return addFileState{}, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return addFileState{}, fmt.Errorf("inspect project root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return addFileState{}, fmt.Errorf("project root %q is not a regular directory", root)
	}
	current := root
	segments := strings.Split(portable, "/")
	for index, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return addFileState{}, nil
		}
		if err != nil {
			return addFileState{}, fmt.Errorf("inspect project file %q: %w", portable, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return addFileState{}, fmt.Errorf("project path %q contains a symbolic link", portable)
		}
		if index < len(segments)-1 {
			if !info.IsDir() {
				return addFileState{}, fmt.Errorf("project path parent for %q is not a directory", portable)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return addFileState{}, fmt.Errorf("project file %q is not a regular file", portable)
		}
		content, err := os.ReadFile(current) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
		if err != nil {
			return addFileState{}, fmt.Errorf("read project file %q: %w", portable, err)
		}
		return addFileState{content: content, mode: info.Mode(), exists: true}, nil
	}
	return addFileState{}, nil
}

func createAddStage(root string) (string, error) {
	stage, err := os.MkdirTemp(filepath.Dir(root), "."+filepath.Base(root)+"-kpro-add-*")
	if err != nil {
		return "", fmt.Errorf("add gRPC API: create sibling staging directory: %w", err)
	}
	return stage, nil
}

func writeAddStageFile(root, name string, content []byte, mode fs.FileMode, exclusive bool) error {
	portable, err := codegen.SafeRelativePath(name)
	if err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(portable))
	// #nosec G301 -- Generated source directories intentionally allow read and traversal, without group or other write access.
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent for %q: %w", portable, err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	if mode == 0 {
		mode = 0o644
	}
	file, err := os.OpenFile(target, flags, mode.Perm()) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return fmt.Errorf("open %q: %w", portable, err)
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write %q: %w", portable, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %q: %w", portable, closeErr)
	}
	return nil
}

func appendAddGRPCManifestSource(data []byte) ([]byte, error) {
	document, err := decodeAddYAMLDocument(data)
	if err != nil {
		return nil, err
	}
	sources, err := addYAMLMappingValue(document, "sources")
	if err != nil {
		return nil, err
	}
	if sources.Kind != yaml.SequenceNode {
		return nil, errors.New(`manifest field "sources" must be a sequence`)
	}
	source := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, pair := range []struct{ key, value string }{{"id", "grpc-api"}, {"kind", "proto"}, {"path", addGRPCProtoPath}, {"package", addGRPCPackage}} {
		source.Content = append(source.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: pair.key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: pair.value})
	}
	sources.Content = append(sources.Content, source)
	output, err := encodeAddYAMLDocument(document)
	if err != nil {
		return nil, err
	}
	value, err := manifest.Parse(output)
	if err != nil {
		return nil, err
	}
	added, exists := serviceAssemblySource(value.Sources, "grpc-api")
	if !exists || !canonicalAddGRPCSource(added) {
		return nil, errors.New("updated manifest does not contain the canonical gRPC source")
	}
	return output, nil
}

func addServiceAPIYAML(data []byte, api string) ([]byte, error) {
	document, err := decodeAddYAMLDocument(data)
	if err != nil {
		return nil, err
	}
	apis, err := addYAMLMappingValue(document, "apis")
	if err != nil {
		return nil, err
	}
	if apis.Kind != yaml.SequenceNode {
		return nil, errors.New(`service project field "apis" must be a sequence`)
	}
	targetOrder := addSelectionOrder(api, serviceProjectAPIOrder)
	insertAt := len(apis.Content)
	for index, item := range apis.Content {
		if item.Kind != yaml.ScalarNode {
			return nil, errors.New(`service project field "apis" must contain scalar values`)
		}
		value := strings.ToLower(strings.TrimSpace(item.Value))
		if value == api {
			return nil, fmt.Errorf("service project already selects API %q", api)
		}
		if addSelectionOrder(value, serviceProjectAPIOrder) > targetOrder {
			insertAt = index
			break
		}
	}
	added := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: api}
	apis.Content = append(apis.Content, nil)
	copy(apis.Content[insertAt+1:], apis.Content[insertAt:])
	apis.Content[insertAt] = added
	return encodeAddYAMLDocument(document)
}

func addSelectionOrder(value string, order []string) int {
	for index, candidate := range order {
		if value == candidate {
			return index
		}
	}
	return len(order)
}

func decodeAddYAMLDocument(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple YAML documents are not supported")
		}
		return nil, err
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("YAML document root must be a mapping")
	}
	return &document, nil
}

func addYAMLMappingValue(document *yaml.Node, key string) (*yaml.Node, error) {
	mapping := document.Content[0]
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1], nil
		}
	}
	return nil, fmt.Errorf("YAML field %q is missing", key)
}

func encodeAddYAMLDocument(document *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func printAddMutationPreview(output io.Writer, mutations []codegen.FileMutation, snapshots map[string]addFileState) {
	for _, mutation := range mutations {
		state := snapshots[mutation.Path]
		switch {
		case !state.exists:
			fmt.Fprintf(output, "would create %s\n", mutation.Path)
		case !bytes.Equal(state.content, mutation.Content):
			fmt.Fprintf(output, "would update %s\n", mutation.Path)
		}
	}
}
