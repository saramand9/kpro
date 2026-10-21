package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

// addGRPCProject retains the first observation of each project file. Later
// captures reuse that snapshot so commit can reject concurrent user edits.
type addGRPCProject struct {
	input          codegenInput
	files          map[string]addFileState
	currentOutputs map[string]struct{}
	adding         bool
	upToDate       bool
}

type addGRPCStage struct {
	root     string
	lockFile string
	files    []codegen.File
}

func inspectAddGRPCProject(options addOptions) (*addGRPCProject, error) {
	root, configPath, lockFile, err := resolveAddCodegenPaths(options.root, options.config)
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: %w", err)
	}
	input := codegenInput{
		root:       root,
		config:     filepath.Join(root, filepath.FromSlash(configPath)),
		configPath: configPath,
		lockFile:   lockFile,
	}

	project := &addGRPCProject{input: input, files: make(map[string]addFileState)}
	manifestState, err := project.capture(configPath, true)
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: %w", err)
	}
	currentManifest, err := manifest.Parse(manifestState.content)
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: %w", err)
	}
	project.input.manifest = currentManifest

	serviceState, err := project.capture(defaultServiceProjectFile, true)
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: %s is required; only unified service projects support incremental APIs: %w", defaultServiceProjectFile, err)
	}
	serviceConfig, err := parseServiceProjectConfig(serviceState.content)
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: %w", err)
	}

	hasProjectGRPC := serviceProjectHasSelection(serviceConfig.APIs, "grpc")
	grpcSource, hasManifestGRPC := serviceAssemblySource(currentManifest.Sources, "grpc-api")
	switch {
	case hasProjectGRPC != hasManifestGRPC:
		return nil, errors.New(`add gRPC API: project configuration is inconsistent: "grpc" and manifest source "grpc-api" must either both exist or both be absent`)
	case hasManifestGRPC && !canonicalAddGRPCSource(grpcSource):
		return nil, fmt.Errorf("add gRPC API: existing manifest source %q conflicts with the canonical gRPC source", grpcSource.ID)
	}

	if err := preflightAddGRPCTools(); err != nil {
		return nil, err
	}

	adding := !hasProjectGRPC
	if adding {
		for _, name := range []string{addGRPCProtoPath, addGRPCServicePath} {
			state, err := project.capture(name, false)
			if err != nil {
				return nil, fmt.Errorf("add gRPC API: inspect new path: %w", err)
			}
			if state.exists {
				return nil, fmt.Errorf("add gRPC API: refusing to overwrite unmanaged path %q", name)
			}
		}
	}

	for _, source := range currentManifest.Sources {
		if _, err := project.capture(source.Path, true); err != nil {
			return nil, fmt.Errorf("add gRPC API: snapshot source %q: %w", source.ID, err)
		}
	}
	if err := snapshotProtoImportClosure(currentManifest, project.capture); err != nil {
		return nil, fmt.Errorf("add gRPC API: snapshot proto import closure: %w", err)
	}
	lockState, err := project.capture(project.input.lockFile, false)
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: snapshot generation lock: %w", err)
	}

	currentPlan, err := buildGenerationPlan(project.input.root, currentManifest, false)
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: build current generation plan: %w", err)
	}
	currentResult, err := codegen.Execute(currentPlan, codegen.Options{Root: project.input.root, LockFile: project.input.lockFile, Mode: codegen.DryRun})
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: validate current generated outputs: %w", err)
	}
	// A create-once scaffold edit changes only the lock hash. Treat that as
	// application-owned state and fold the refreshed hash into this transaction;
	// generated creates, updates, and removals must already be applied explicitly.
	if len(currentResult.Created) != 0 || len(currentResult.Updated) != 0 || len(currentResult.Removed) != 0 {
		return nil, fmt.Errorf("add gRPC API: existing generated outputs are not current; run 'kpro generate' first (create=%v update=%v remove=%v)", currentResult.Created, currentResult.Updated, currentResult.Removed)
	}
	if !lockState.exists {
		return nil, errors.New("add gRPC API: generation lock is missing; run 'kpro generate' first")
	}

	currentFiles, err := currentPlan.OrderedFiles()
	if err != nil {
		return nil, fmt.Errorf("add gRPC API: inspect current generation plan: %w", err)
	}
	currentOutputs := make(map[string]struct{}, len(currentFiles))
	for _, file := range currentFiles {
		currentOutputs[file.Path] = struct{}{}
		required := file.Ownership == codegen.Generated
		if _, err := project.capture(file.Path, required); err != nil {
			return nil, fmt.Errorf("add gRPC API: cannot safely snapshot current output %q: %w", file.Path, err)
		}
	}

	project.currentOutputs = currentOutputs
	project.adding = adding
	project.upToDate = !adding && !currentResult.Changed()
	return project, nil
}

func (project *addGRPCProject) capture(name string, required bool) (addFileState, error) {
	name, err := codegen.SafeRelativePath(name)
	if err != nil {
		return addFileState{}, err
	}
	if state, exists := project.files[name]; exists {
		if required && !state.exists {
			return addFileState{}, fmt.Errorf("required project file %q is missing", name)
		}
		return state, nil
	}
	state, err := inspectAddProjectFile(project.input.root, name)
	if err != nil {
		return addFileState{}, err
	}
	if required && !state.exists {
		return addFileState{}, fmt.Errorf("required project file %q is missing", name)
	}
	project.files[name] = state
	return state, nil
}

func (project *addGRPCProject) prepareStage(stage string) (addGRPCStage, error) {
	for name, state := range project.files {
		if !state.exists {
			continue
		}
		if err := writeAddStageFile(stage, name, state.content, state.mode.Perm(), true); err != nil {
			return addGRPCStage{}, fmt.Errorf("add gRPC API: copy %q to isolated stage: %w", name, err)
		}
	}

	if project.adding {
		updatedService, err := addServiceAPIYAML(project.files[defaultServiceProjectFile].content, "grpc")
		if err != nil {
			return addGRPCStage{}, fmt.Errorf("add gRPC API: update %s: %w", defaultServiceProjectFile, err)
		}
		if parsed, err := parseServiceProjectConfig(updatedService); err != nil {
			return addGRPCStage{}, fmt.Errorf("add gRPC API: validate updated %s: %w", defaultServiceProjectFile, err)
		} else if !serviceProjectHasSelection(parsed.APIs, "grpc") {
			return addGRPCStage{}, fmt.Errorf("add gRPC API: updated %s does not select gRPC", defaultServiceProjectFile)
		}
		if err := writeAddStageFile(stage, defaultServiceProjectFile, updatedService, project.files[defaultServiceProjectFile].mode.Perm(), false); err != nil {
			return addGRPCStage{}, err
		}

		updatedManifest, err := appendAddGRPCManifestSource(project.files[project.input.configPath].content)
		if err != nil {
			return addGRPCStage{}, fmt.Errorf("add gRPC API: update generation manifest: %w", err)
		}
		if err := writeAddStageFile(stage, project.input.configPath, updatedManifest, project.files[project.input.configPath].mode.Perm(), false); err != nil {
			return addGRPCStage{}, err
		}

		data := scaffoldData{Module: project.input.manifest.Module, ServiceIdentifier: project.input.manifest.Service, ServiceExported: serviceAssemblyExportedIdentifier(project.input.manifest.Service)}
		if err := renderTemplate(stage, "templates/profiles/service/service.proto.tmpl", addGRPCProtoPath, 0o644, data); err != nil {
			return addGRPCStage{}, fmt.Errorf("add gRPC API: render gRPC contract: %w", err)
		}
		if err := renderTemplate(stage, "templates/profiles/service/grpc_service.go.tmpl", addGRPCServicePath, 0o644, data); err != nil {
			return addGRPCStage{}, fmt.Errorf("add gRPC API: render ready gRPC service: %w", err)
		}
	}

	stageInput, err := loadCodegenInput(stage, project.input.configPath)
	if err != nil {
		return addGRPCStage{}, fmt.Errorf("add gRPC API: load isolated generation input: %w", err)
	}
	stagePlan, err := buildGenerationPlan(stage, stageInput.manifest, false)
	if err != nil {
		return addGRPCStage{}, fmt.Errorf("add gRPC API: build isolated generation plan: %w", err)
	}
	stageResult, err := codegen.Execute(stagePlan, codegen.Options{Root: stage, LockFile: stageInput.lockFile, Mode: codegen.Apply})
	if err != nil {
		return addGRPCStage{}, fmt.Errorf("add gRPC API: execute isolated generation plan: %w", err)
	}
	if len(stageResult.Removed) != 0 {
		return addGRPCStage{}, fmt.Errorf("add gRPC API: isolated generation unexpectedly removes files: %v", stageResult.Removed)
	}

	stageFiles, err := stagePlan.OrderedFiles()
	if err != nil {
		return addGRPCStage{}, fmt.Errorf("add gRPC API: inspect isolated generation plan: %w", err)
	}
	return addGRPCStage{root: stage, lockFile: stageInput.lockFile, files: stageFiles}, nil
}

func (project *addGRPCProject) planMutations(stage addGRPCStage) ([]codegen.FileMutation, error) {
	readSet := make(map[string]struct{}, len(project.files)+len(stage.files)+2)
	for name, state := range project.files {
		if state.exists {
			readSet[name] = struct{}{}
		}
	}
	readSet[project.input.configPath] = struct{}{}
	readSet[defaultServiceProjectFile] = struct{}{}
	readSet[stage.lockFile] = struct{}{}
	if project.adding {
		readSet[addGRPCProtoPath] = struct{}{}
		readSet[addGRPCServicePath] = struct{}{}
	}
	for _, file := range stage.files {
		staged, err := inspectAddProjectFile(stage.root, file.Path)
		if err != nil {
			return nil, fmt.Errorf("add gRPC API: inspect isolated output %q: %w", file.Path, err)
		}
		if !staged.exists {
			state, snapshotted := project.files[file.Path]
			_, wasCurrent := project.currentOutputs[file.Path]
			if file.Ownership == codegen.Generated || !snapshotted || state.exists || !wasCurrent {
				return nil, fmt.Errorf("add gRPC API: isolated generation did not produce required output %q", file.Path)
			}
			// Execute intentionally preserves a user's deletion of a previously
			// locked create-once file. Keep that absence out of the write set.
			continue
		}
		readSet[file.Path] = struct{}{}
		if _, exists := project.files[file.Path]; exists {
			continue
		}
		state, err := project.capture(file.Path, false)
		if err != nil {
			return nil, fmt.Errorf("add gRPC API: inspect new output %q: %w", file.Path, err)
		}
		if state.exists {
			if _, managed := project.currentOutputs[file.Path]; !managed {
				return nil, fmt.Errorf("add gRPC API: refusing to overwrite unmanaged path %q", file.Path)
			}
		}
	}

	names := make([]string, 0, len(readSet))
	for name := range readSet {
		names = append(names, name)
	}
	sort.Strings(names)
	mutations := make([]codegen.FileMutation, 0, len(names))
	for _, name := range names {
		state, exists := project.files[name]
		if !exists {
			return nil, fmt.Errorf("add gRPC API: missing snapshot for %q", name)
		}
		staged, err := inspectAddProjectFile(stage.root, name)
		if err != nil {
			return nil, fmt.Errorf("add gRPC API: inspect staged %q: %w", name, err)
		}
		if !staged.exists {
			return nil, fmt.Errorf("add gRPC API: isolated generation did not retain required read-set file %q", name)
		}
		precondition := codegen.ExpectFileMissing()
		mode := staged.mode.Perm()
		if state.exists {
			precondition = codegen.ExpectFileContent(state.content)
			mode = 0
		}
		mutations = append(mutations, codegen.FileMutation{Path: name, Content: staged.content, Mode: mode, Precondition: precondition})
	}

	return mutations, nil
}
