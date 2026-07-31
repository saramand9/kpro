package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/saramand9/kpro/internal/codegen"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const (
	localScaffoldTemplateAPIVersion   = "kpro.dev/scaffold-template/v1alpha1"
	localScaffoldTemplateManifestFile = "kpro.template.yaml"
	scaffoldTemplateLockFile          = "kpro.scaffold.lock.json"
)

var localScaffoldTemplateNamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

type localScaffoldTemplateManifest struct {
	APIVersion string                      `yaml:"apiVersion"`
	Name       string                      `yaml:"name"`
	Version    string                      `yaml:"version"`
	Profile    string                      `yaml:"profile"`
	Files      []localScaffoldTemplateFile `yaml:"files"`
}

type localScaffoldTemplateFile struct {
	Source string                         `yaml:"source"`
	Target string                         `yaml:"target"`
	Action string                         `yaml:"action"`
	Mode   string                         `yaml:"mode,omitempty"`
	When   localScaffoldTemplateCondition `yaml:"when,omitempty"`
}

type localScaffoldTemplateCondition struct {
	APIs       []string `yaml:"apis,omitempty"`
	Transports []string `yaml:"transports,omitempty"`
}

type localScaffoldTemplateSnapshot struct {
	manifest localScaffoldTemplateManifest
	files    map[string][]byte
	digest   string
}

type localTemplateCheckedPath struct {
	path string
	info os.FileInfo
}

type scaffoldTemplateProvenance struct {
	Version  int                                `json:"version"`
	Template scaffoldTemplateProvenanceTemplate `json:"template"`
	KPro     scaffoldTemplateProvenanceKPro     `json:"kpro"`
}

type scaffoldTemplateProvenanceTemplate struct {
	APIVersion string `json:"apiVersion"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	Profile    string `json:"profile"`
}

type scaffoldTemplateProvenanceKPro struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func loadLocalScaffoldTemplate(root string, selection serviceSelection) (localScaffoldTemplateSnapshot, error) {
	if strings.TrimSpace(root) == "" {
		return localScaffoldTemplateSnapshot{}, errors.New("local scaffold template directory cannot be empty")
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return localScaffoldTemplateSnapshot{}, fmt.Errorf("resolve local scaffold template directory: %w", err)
	}
	rootInfo, err := os.Lstat(resolvedRoot)
	if err != nil {
		return localScaffoldTemplateSnapshot{}, fmt.Errorf("inspect local scaffold template directory: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return localScaffoldTemplateSnapshot{}, fmt.Errorf("local scaffold template %q must be a real directory, not a symbolic link", root)
	}

	manifestData, err := readLocalTemplateRegularFile(resolvedRoot, localScaffoldTemplateManifestFile)
	if err != nil {
		return localScaffoldTemplateSnapshot{}, fmt.Errorf("read local scaffold template manifest: %w", err)
	}
	value, err := parseLocalScaffoldTemplateManifest(manifestData)
	if err != nil {
		return localScaffoldTemplateSnapshot{}, err
	}
	if value.Profile != selection.profile {
		return localScaffoldTemplateSnapshot{}, fmt.Errorf(
			"local scaffold template %q requires profile %q, selected %q",
			value.Name,
			value.Profile,
			selection.profile,
		)
	}

	snapshot := localScaffoldTemplateSnapshot{
		manifest: value,
		files:    make(map[string][]byte, len(value.Files)),
	}
	for _, candidate := range value.Files {
		if _, exists := snapshot.files[candidate.Source]; exists {
			continue
		}
		body, err := readLocalTemplateRegularFile(resolvedRoot, candidate.Source)
		if err != nil {
			return localScaffoldTemplateSnapshot{}, fmt.Errorf("read local scaffold template source %q: %w", candidate.Source, err)
		}
		if _, err := template.New(candidate.Source).Option("missingkey=error").Parse(string(body)); err != nil {
			return localScaffoldTemplateSnapshot{}, fmt.Errorf("parse local scaffold template source %q: %w", candidate.Source, err)
		}
		snapshot.files[candidate.Source] = append([]byte(nil), body...)
	}
	snapshot.digest = localScaffoldTemplateDigest(manifestData, snapshot.files)
	return snapshot, nil
}

func parseLocalScaffoldTemplateManifest(data []byte) (localScaffoldTemplateManifest, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var value localScaffoldTemplateManifest
	if err := decoder.Decode(&value); err != nil {
		return localScaffoldTemplateManifest{}, fmt.Errorf("decode local scaffold template manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return localScaffoldTemplateManifest{}, errors.New("decode local scaffold template manifest: multiple YAML documents are not supported")
		}
		return localScaffoldTemplateManifest{}, fmt.Errorf("decode local scaffold template manifest: %w", err)
	}

	value.APIVersion = strings.TrimSpace(value.APIVersion)
	value.Name = strings.TrimSpace(value.Name)
	value.Version = strings.TrimSpace(value.Version)
	value.Profile = strings.TrimSpace(value.Profile)
	if value.APIVersion != localScaffoldTemplateAPIVersion {
		return localScaffoldTemplateManifest{}, fmt.Errorf("validate local scaffold template manifest: unsupported apiVersion %q", value.APIVersion)
	}
	if !localScaffoldTemplateNamePattern.MatchString(value.Name) {
		return localScaffoldTemplateManifest{}, fmt.Errorf("validate local scaffold template manifest: invalid name %q", value.Name)
	}
	if !semver.IsValid(value.Version) {
		return localScaffoldTemplateManifest{}, fmt.Errorf("validate local scaffold template manifest: version %q must be valid Go semantic version syntax such as v1.0.0", value.Version)
	}
	if value.Profile != "minimal" && value.Profile != "service" {
		return localScaffoldTemplateManifest{}, fmt.Errorf("validate local scaffold template manifest: unsupported profile %q", value.Profile)
	}
	if len(value.Files) == 0 {
		return localScaffoldTemplateManifest{}, errors.New("validate local scaffold template manifest: at least one file is required")
	}

	sourceKeys := make(map[string]string, len(value.Files))
	for index := range value.Files {
		candidate := &value.Files[index]
		prefix := fmt.Sprintf("validate local scaffold template manifest: files[%d]", index)
		candidate.Source = strings.TrimSpace(candidate.Source)
		candidate.Target = strings.TrimSpace(candidate.Target)
		candidate.Action = strings.TrimSpace(candidate.Action)
		candidate.Mode = strings.TrimSpace(candidate.Mode)
		source, err := codegen.SafeRelativePath(candidate.Source)
		if err != nil {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s has unsafe source %q: %w", prefix, candidate.Source, err)
		}
		candidate.Source = source
		sourceKey, err := codegen.PortablePathKey(source)
		if err != nil {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s has unsafe source %q: %w", prefix, candidate.Source, err)
		}
		if previous, exists := sourceKeys[sourceKey]; exists {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s source %q aliases or repeats %q", prefix, source, previous)
		}
		sourceKeys[sourceKey] = source
		if candidate.Target == "" {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s target cannot be empty", prefix)
		}
		if _, err := template.New("target").Option("missingkey=error").Parse(candidate.Target); err != nil {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s has invalid target template %q: %w", prefix, candidate.Target, err)
		}
		if candidate.Action != "add" && candidate.Action != "replace" {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s has unsupported action %q (supported: add, replace)", prefix, candidate.Action)
		}
		if candidate.Mode != "" && candidate.Mode != "0644" && candidate.Mode != "0755" {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s has unsupported mode %q (supported: 0644, 0755)", prefix, candidate.Mode)
		}
		apis, err := canonicalSelections("template API condition", candidate.When.APIs, serviceAPIOrder)
		if err != nil {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s: %w", prefix, err)
		}
		transports, err := canonicalSelections("template transport condition", candidate.When.Transports, transportOrder)
		if err != nil {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s: %w", prefix, err)
		}
		if value.Profile == "minimal" && (len(apis) != 0 || len(transports) != 0) {
			return localScaffoldTemplateManifest{}, fmt.Errorf("%s has conditions, but the minimal profile has no API or transport selections", prefix)
		}
		candidate.When.APIs = apis
		candidate.When.Transports = transports
	}
	return value, nil
}

func readLocalTemplateRegularFile(root, relative string) ([]byte, error) {
	portable, err := codegen.SafeRelativePath(relative)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("template root is not a real directory")
	}
	checked := []localTemplateCheckedPath{{path: root, info: rootInfo}}
	current := root
	segments := strings.Split(portable, "/")
	for index, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("path %q contains a symbolic link", portable)
		}
		if index != len(segments)-1 && !info.IsDir() {
			return nil, fmt.Errorf("path %q has a non-directory parent", portable)
		}
		if index == len(segments)-1 && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("path %q is not a regular file", portable)
		}
		checked = append(checked, localTemplateCheckedPath{path: current, info: info})
	}
	if err := recheckLocalTemplatePath(checked, portable); err != nil {
		return nil, err
	}
	file, err := os.Open(current)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, statErr
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("path %q changed while it was read", portable)
	}
	if !os.SameFile(checked[len(checked)-1].info, info) {
		_ = file.Close()
		return nil, fmt.Errorf("path %q changed before it was opened", portable)
	}
	body, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := recheckLocalTemplatePath(checked, portable); err != nil {
		return nil, err
	}
	return body, nil
}

func recheckLocalTemplatePath(checked []localTemplateCheckedPath, relative string) error {
	for _, candidate := range checked {
		current, err := os.Lstat(candidate.path)
		if err != nil {
			return fmt.Errorf("reinspect path %q: %w", relative, err)
		}
		if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(candidate.info, current) {
			return fmt.Errorf("path %q changed or became a symbolic link while it was read", relative)
		}
	}
	return nil
}

func localScaffoldTemplateDigest(manifest []byte, files map[string][]byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("kpro-local-scaffold-template-v1\x00"))
	writeDigestField(hash, localScaffoldTemplateManifestFile, manifest)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writeDigestField(hash, name, files[name])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func writeDigestField(writer digestWriter, name string, value []byte) {
	_, _ = fmt.Fprintf(writer, "%d:%s:%d:", len(name), name, len(value))
	_, _ = writer.Write(value)
	_, _ = writer.Write([]byte{0})
}

func applyLocalScaffoldTemplate(
	base []scaffoldTemplate,
	snapshot localScaffoldTemplateSnapshot,
	selection serviceSelection,
	data scaffoldData,
) ([]scaffoldTemplate, error) {
	merged := append([]scaffoldTemplate(nil), base...)
	baseByKey := make(map[string]int, len(merged))
	baseTargets := make([]string, len(merged))
	for index := range merged {
		target, err := renderString(merged[index].target, data)
		if err != nil {
			return nil, fmt.Errorf("render built-in scaffold target %q: %w", merged[index].target, err)
		}
		target, err = portableScaffoldPath(target)
		if err != nil {
			return nil, fmt.Errorf("validate built-in scaffold target %q: %w", target, err)
		}
		key, err := codegen.PortablePathKey(target)
		if err != nil {
			return nil, err
		}
		if previous, exists := baseByKey[key]; exists {
			return nil, fmt.Errorf("built-in scaffold targets %q and %q collide", baseTargets[previous], target)
		}
		baseByKey[key] = index
		baseTargets[index] = target
		merged[index].target = target
	}

	activeTargets := make(map[string]string, len(merged)+len(snapshot.manifest.Files)+1)
	for index, target := range baseTargets {
		key, _ := codegen.PortablePathKey(target)
		activeTargets[key] = baseTargets[index]
	}
	replaced := make(map[string]struct{})
	for index, candidate := range snapshot.manifest.Files {
		target, err := renderString(candidate.Target, data)
		if err != nil {
			return nil, fmt.Errorf("render local scaffold template files[%d] target %q: %w", index, candidate.Target, err)
		}
		target, err = portableScaffoldPath(target)
		if err != nil {
			return nil, fmt.Errorf("local scaffold template files[%d] has unsafe target %q: %w", index, target, err)
		}
		reserved, err := reservedScaffoldTemplateTarget(target)
		if err != nil {
			return nil, err
		}
		if reserved {
			return nil, fmt.Errorf("local scaffold template files[%d] targets reserved lock file %q", index, target)
		}
		if !localTemplateConditionMatches(candidate.When, selection) {
			continue
		}
		key, err := codegen.PortablePathKey(target)
		if err != nil {
			return nil, err
		}
		mode, modeSet := localScaffoldTemplateMode(candidate.Mode)
		switch candidate.Action {
		case "replace":
			baseIndex, exists := baseByKey[key]
			if !exists {
				return nil, fmt.Errorf("local scaffold template files[%d] cannot replace %q because it is not a selected built-in scaffold target", index, target)
			}
			if _, exists := replaced[key]; exists {
				return nil, fmt.Errorf("local scaffold template files[%d] replaces %q more than once", index, target)
			}
			replaced[key] = struct{}{}
			if !modeSet {
				mode = merged[baseIndex].mode
			}
			merged[baseIndex] = scaffoldTemplate{
				source:     candidate.Source,
				target:     baseTargets[baseIndex],
				mode:       mode,
				content:    append([]byte(nil), snapshot.files[candidate.Source]...),
				hasContent: true,
			}
		case "add":
			if previous, exists := activeTargets[key]; exists {
				return nil, fmt.Errorf("local scaffold template files[%d] cannot add %q because it collides with %q", index, target, previous)
			}
			if !modeSet {
				mode = 0o644
			}
			merged = append(merged, scaffoldTemplate{
				source:     candidate.Source,
				target:     target,
				mode:       mode,
				content:    append([]byte(nil), snapshot.files[candidate.Source]...),
				hasContent: true,
			})
			activeTargets[key] = target
		}
	}

	lockBody, err := scaffoldTemplateLock(snapshot)
	if err != nil {
		return nil, err
	}
	merged = append(merged, scaffoldTemplate{
		source:     scaffoldTemplateLockFile,
		target:     scaffoldTemplateLockFile,
		mode:       0o644,
		content:    lockBody,
		hasContent: true,
		literal:    true,
	})
	return merged, nil
}

func reservedScaffoldTemplateTarget(target string) (bool, error) {
	for _, reserved := range []string{scaffoldTemplateLockFile, codegen.DefaultLockFile} {
		collides, err := codegen.PortablePathsCollide(target, reserved)
		if err != nil {
			return false, err
		}
		if collides {
			return true, nil
		}
	}
	return false, nil
}

func localTemplateConditionMatches(condition localScaffoldTemplateCondition, selection serviceSelection) bool {
	for _, api := range condition.APIs {
		if !hasSelection(selection.apis, api) {
			return false
		}
	}
	for _, transport := range condition.Transports {
		if !hasSelection(selection.transports, transport) {
			return false
		}
	}
	return true
}

func localScaffoldTemplateMode(value string) (os.FileMode, bool) {
	switch value {
	case "0644":
		return 0o644, true
	case "0755":
		return 0o755, true
	default:
		return 0, false
	}
}

func scaffoldTemplateLock(snapshot localScaffoldTemplateSnapshot) ([]byte, error) {
	resolvedVersion, resolvedCommit, _ := resolvedBuildMetadata()
	value := scaffoldTemplateProvenance{
		Version: 1,
		Template: scaffoldTemplateProvenanceTemplate{
			APIVersion: snapshot.manifest.APIVersion,
			Name:       snapshot.manifest.Name,
			Version:    snapshot.manifest.Version,
			SHA256:     snapshot.digest,
			Profile:    snapshot.manifest.Profile,
		},
		KPro: scaffoldTemplateProvenanceKPro{
			Version: resolvedVersion,
			Commit:  resolvedCommit,
		},
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode local scaffold template lock: %w", err)
	}
	return append(body, '\n'), nil
}
