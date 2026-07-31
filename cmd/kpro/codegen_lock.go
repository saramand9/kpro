package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/saramand9/kpro/internal/codegen"
)

func generationLockPaths(config string) (current, legacy string) {
	directory, filename := path.Split(config)
	if filename == defaultCodegenConfig {
		return path.Join(directory, codegen.DefaultLockFile), ""
	}
	current = config + ".lock.json"
	legacy = strings.TrimSuffix(config, path.Ext(config)) + ".lock.json"
	if current == legacy {
		legacy = ""
	}
	return current, legacy
}

func validateGenerationLockPath(config, lockFile string) error {
	directory, filename := path.Split(config)
	reserved := path.Join(directory, codegen.DefaultLockFile)
	if filename != defaultCodegenConfig && strings.EqualFold(lockFile, reserved) {
		return fmt.Errorf(
			"generation manifest path %q maps to reserved default lock %q; rename the manifest",
			config,
			reserved,
		)
	}
	return nil
}

func prepareGenerationLock(input codegenInput, mode codegen.Mode) (string, []string, error) {
	if input.legacyLockFile == "" {
		return input.lockFile, nil, nil
	}
	current, err := inspectAddProjectFile(input.root, input.lockFile)
	if err != nil {
		return "", nil, fmt.Errorf("inspect generation lock %q: %w", input.lockFile, err)
	}
	if current.exists {
		return input.lockFile, nil, nil
	}
	legacy, err := inspectAddProjectFile(input.root, input.legacyLockFile)
	if err != nil {
		return "", nil, fmt.Errorf("inspect legacy generation lock %q: %w", input.legacyLockFile, err)
	}
	if !legacy.exists {
		return input.lockFile, nil, nil
	}
	if err := rejectAmbiguousLegacyGenerationLock(input); err != nil {
		return "", nil, err
	}
	if mode != codegen.Apply {
		return input.legacyLockFile, nil, nil
	}
	warnings, err := migrateLegacyGenerationLock(input.root, input.legacyLockFile, input.lockFile, legacy)
	if err != nil {
		return "", nil, err
	}
	return input.lockFile, warnings, nil
}

func rejectAmbiguousLegacyGenerationLock(input codegenInput) error {
	configRelative := input.configPath
	configDirectory, configFilename := path.Split(configRelative)
	stem := strings.TrimSuffix(configFilename, path.Ext(configFilename))
	entries, err := os.ReadDir(filepath.Join(input.root, filepath.FromSlash(configDirectory)))
	if err != nil {
		return fmt.Errorf("inspect generation manifest directory for legacy lock migration: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.EqualFold(entry.Name(), configFilename) {
			continue
		}
		extension := strings.ToLower(path.Ext(entry.Name()))
		if extension != ".yaml" && extension != ".yml" && extension != ".json" {
			continue
		}
		if !strings.EqualFold(strings.TrimSuffix(entry.Name(), path.Ext(entry.Name())), stem) {
			continue
		}
		return fmt.Errorf(
			"generation lock %q is ambiguous between manifests %q and %q; move or remove the legacy lock after reviewing its generated outputs",
			input.legacyLockFile,
			configRelative,
			path.Join(configDirectory, entry.Name()),
		)
	}
	return nil
}

func migrateLegacyGenerationLock(root, legacyPath, currentPath string, legacy addFileState) ([]string, error) {
	mutations := []codegen.FileMutation{
		{
			Path:         legacyPath,
			Precondition: codegen.ExpectFileContent(legacy.content),
			Remove:       true,
		},
		{
			Path:         currentPath,
			Content:      legacy.content,
			Mode:         legacy.mode.Perm(),
			Precondition: codegen.ExpectFileMissing(),
		},
	}
	result, err := codegen.CommitFileTransaction(mutations, codegen.FileTransactionOptions{Root: root})
	if err != nil {
		return nil, fmt.Errorf("migrate generation lock %q to %q: %w", legacyPath, currentPath, err)
	}
	return result.CleanupWarnings, nil
}
