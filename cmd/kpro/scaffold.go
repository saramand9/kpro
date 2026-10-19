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
	"os/exec"
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
	kproModulePath              = "github.com/saramand9/kpro"
	developmentModuleVersion    = "v0.0.0"
	messageServiceNameMaxLength = 64
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
	ServiceExported   string
	ServiceFileStem   string
	KProModule        string
	KProVersion       string
	KProReplace       string
	APIList           string
	TransportList     string
	HasHTTP           bool
	HasGRPC           bool
	HasMessage        bool
	HasTCP            bool
	HasKCP            bool
	HasQUIC           bool
	HasUDP            bool
	HasWebSocket      bool
}

type scaffoldTemplate struct {
	source     string
	target     string
	mode       os.FileMode
	content    []byte
	hasContent bool
	literal    bool
}

type repeatedFlag []string

func (values *repeatedFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *repeatedFlag) Set(value string) error {
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return errors.New("selection cannot be empty")
		}
		*values = append(*values, candidate)
	}
	return nil
}

type serviceSelection struct {
	profile    string
	apis       []string
	transports []string
}

var (
	scaffoldLookPath = exec.LookPath
	serviceAPIOrder  = []string{"http", "grpc", "message"}
	transportOrder   = []string{"tcp", "kcp", "quic", "udp", "websocket"}
)

var minimalScaffoldTemplates = []scaffoldTemplate{
	{source: "templates/service/go.mod.tmpl", target: "go.mod", mode: 0o644},
	{source: "templates/service/main.go.tmpl", target: "main.go", mode: 0o644},
	{source: "templates/service/README.md.tmpl", target: "README.md", mode: 0o644},
	{source: "templates/service/gitignore.tmpl", target: ".gitignore", mode: 0o644},
}

var serviceProfileBaseTemplates = []scaffoldTemplate{
	{source: "templates/profiles/service/go.mod.tmpl", target: "go.mod", mode: 0o644},
	{source: "templates/profiles/service/main.go.tmpl", target: "cmd/{{.ServiceName}}/main.go", mode: 0o644},
	{source: "templates/profiles/service/kpro.gen.yaml.tmpl", target: "kpro.gen.yaml", mode: 0o644},
	{source: "templates/profiles/service/kpro.service.yaml.tmpl", target: "kpro.service.yaml", mode: 0o644},
	{source: "templates/profiles/service/README.md.tmpl", target: "README.md", mode: 0o644},
	{source: "templates/profiles/service/gitignore.tmpl", target: ".gitignore", mode: 0o644},
}

var serviceProfileHTTPTemplates = []scaffoldTemplate{
	{source: "templates/profiles/service/openapi.yaml.tmpl", target: "api/http/openapi.yaml", mode: 0o644},
	{source: "templates/profiles/service/http_service.go.tmpl", target: "internal/httpapi/service/api.go", mode: 0o644},
}

var serviceProfileGRPCTemplates = []scaffoldTemplate{
	{source: "templates/profiles/service/service.proto.tmpl", target: "api/grpc/service.proto", mode: 0o644},
	{source: "templates/profiles/service/grpc_service.go.tmpl", target: "internal/grpcapi/kpro_grpc_api_service.go", mode: 0o644},
}

var serviceProfileMessageTemplates = []scaffoldTemplate{
	{source: "templates/profiles/service/messages.yaml.tmpl", target: "api/message/messages.yaml", mode: 0o644},
	{source: "templates/profiles/service/message_service.go.tmpl", target: "internal/messageapi/service/{{.ServiceFileStem}}.go", mode: 0o644},
}

func scaffoldNew(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	flags.SetOutput(stderr)
	modulePath := flags.String("module", "", "Go module path (default example.com/<directory>)")
	serviceName := flags.String("name", "", "service name (default directory name)")
	kproReplace := flags.String("kpro-replace", "", "local KPro checkout (auto-detected from the current directory)")
	profile := flags.String("profile", "", "project profile (supported: minimal, service)")
	templatePath := flags.String("template", "", "local versioned scaffold template overlay directory")
	var apiValues repeatedFlag
	var transportValues repeatedFlag
	flags.Var(&apiValues, "api", "service API: http, grpc, or message (repeatable)")
	flags.Var(&transportValues, "transport", "message transport: tcp, kcp, quic, udp, or websocket (repeatable)")
	parseArgs := normalizeScaffoldNewArgs(args)
	if err := flags.Parse(parseArgs); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: kpro new <directory> [--module example.com/service] [--name service] [--kpro-replace path] [--profile minimal|service] [--api http|grpc|message] [--transport tcp|kcp|quic|udp|websocket] [--template local-directory]")
	}
	selection, err := validateServiceSelection(*profile, apiValues, transportValues)
	if err != nil {
		return err
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
	if err := validatePortableServicePathSegment(*serviceName); err != nil {
		return err
	}
	serviceIdentifier := goIdentifier(*serviceName)
	serviceExported := exportedServiceIdentifier(serviceIdentifier)
	if err := validateDerivedServiceName(selection, serviceExported); err != nil {
		return err
	}
	if err := validateScaffoldPrerequisites(selection); err != nil {
		return err
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
	quotedReplace := ""
	if replacePath != "" {
		quotedReplace = strconv.Quote(filepath.ToSlash(replacePath))
	}
	data := scaffoldData{
		Module:            *modulePath,
		ServiceName:       *serviceName,
		ServiceIdentifier: serviceIdentifier,
		ServiceExported:   serviceExported,
		ServiceFileStem:   strings.ToLower(serviceExported),
		KProModule:        kproModulePath,
		KProVersion:       kproVersion,
		KProReplace:       quotedReplace,
		APIList:           strings.Join(selection.apis, ", "),
		TransportList:     strings.Join(selection.transports, ", "),
		HasHTTP:           hasSelection(selection.apis, "http"),
		HasGRPC:           hasSelection(selection.apis, "grpc"),
		HasMessage:        hasSelection(selection.apis, "message"),
		HasTCP:            hasSelection(selection.transports, "tcp"),
		HasKCP:            hasSelection(selection.transports, "kcp"),
		HasQUIC:           hasSelection(selection.transports, "quic"),
		HasUDP:            hasSelection(selection.transports, "udp"),
		HasWebSocket:      hasSelection(selection.transports, "websocket"),
	}
	templates := minimalScaffoldTemplates
	if selection.profile == "service" {
		templates = serviceProfileTemplates(data)
	}
	var localTemplate *localScaffoldTemplateSnapshot
	if *templatePath != "" {
		snapshot, err := loadLocalScaffoldTemplate(*templatePath, selection)
		if err != nil {
			return err
		}
		templates, err = applyLocalScaffoldTemplate(templates, snapshot, selection, data)
		if err != nil {
			return err
		}
		localTemplate = &snapshot
	}
	if err := validateScaffoldTemplateTargets(templates, data); err != nil {
		return err
	}
	plannedFiles, err := planScaffoldTemplates(templates, data)
	if err != nil {
		return err
	}
	if err := ensureMissingTarget(target); err != nil {
		return err
	}
	stagedTarget, err := createScaffoldStage(target)
	if err != nil {
		return err
	}
	stagePublished := false
	defer func() {
		if !stagePublished {
			_ = os.RemoveAll(stagedTarget)
		}
	}()
	if err := writeScaffoldFiles(stagedTarget, plannedFiles); err != nil {
		return err
	}
	if selection.profile == "service" {
		if err := runGenerate([]string{"--root", stagedTarget}, io.Discard, io.Discard); err != nil {
			return fmt.Errorf("generate service bindings: %w", err)
		}
	}
	if err := commitScaffoldStage(stagedTarget, target); err != nil {
		return err
	}
	stagePublished = true

	if replacePath != "" {
		fmt.Fprintf(stdout, "created %s\nusing KPro source %s\n", *serviceName, replacePath)
	} else {
		fmt.Fprintf(stdout, "created %s\nusing KPro %s\n", *serviceName, kproVersion)
	}
	if localTemplate != nil {
		fmt.Fprintf(
			stdout,
			"using scaffold template %s %s (sha256:%s)\n",
			localTemplate.manifest.Name,
			localTemplate.manifest.Version,
			localTemplate.digest,
		)
	}
	if selection.profile == "service" {
		fmt.Fprintf(stdout, "APIs: %s\n", data.APIList)
		if data.HasMessage {
			fmt.Fprintf(stdout, "message transports: %s\n", data.TransportList)
		}
	}
	runTarget := "."
	if selection.profile == "service" {
		runTarget = "./cmd/" + *serviceName
	}
	fmt.Fprintf(stdout, "\nnext:\n  cd %s\n  go mod tidy\n  go run %s\n", target, runTarget)
	return nil
}

func normalizeScaffoldNewArgs(args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		if argument == "-" || !strings.HasPrefix(argument, "-") {
			positionals = append(positionals, argument)
			continue
		}
		flags = append(flags, argument)
		if argument == "-h" || argument == "--help" || strings.Contains(argument, "=") {
			continue
		}
		if index+1 < len(args) {
			index++
			flags = append(flags, args[index])
		}
	}
	return append(flags, positionals...)
}

func validateServiceSelection(profile string, rawAPIs, rawTransports []string) (serviceSelection, error) {
	if profile == "" {
		profile = "minimal"
	}
	if profile != "minimal" && profile != "service" {
		return serviceSelection{}, fmt.Errorf("unsupported project profile %q (supported: minimal, service)", profile)
	}
	if profile == "minimal" {
		if len(rawAPIs) > 0 || len(rawTransports) > 0 {
			return serviceSelection{}, errors.New("minimal profile does not accept --api or --transport")
		}
		return serviceSelection{profile: profile}, nil
	}
	apis, err := canonicalSelections("API", rawAPIs, serviceAPIOrder)
	if err != nil {
		return serviceSelection{}, err
	}
	if len(apis) == 0 {
		return serviceSelection{}, errors.New("service profile requires at least one --api")
	}
	transports, err := canonicalSelections("transport", rawTransports, transportOrder)
	if err != nil {
		return serviceSelection{}, err
	}
	hasMessage := hasSelection(apis, "message")
	if !hasMessage && len(transports) > 0 {
		return serviceSelection{}, errors.New("--transport requires --api message")
	}
	if hasMessage && len(transports) == 0 {
		return serviceSelection{}, errors.New("--api message requires at least one --transport")
	}
	return serviceSelection{profile: profile, apis: apis, transports: transports}, nil
}

func canonicalSelections(label string, values, allowed []string) ([]string, error) {
	selected := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if !hasSelection(allowed, value) {
			return nil, fmt.Errorf("unsupported %s %q (supported: %s)", label, raw, strings.Join(allowed, ", "))
		}
		if _, exists := selected[value]; exists {
			return nil, fmt.Errorf("duplicate %s %q", label, value)
		}
		selected[value] = struct{}{}
	}
	ordered := make([]string, 0, len(selected))
	for _, value := range allowed {
		if _, exists := selected[value]; exists {
			ordered = append(ordered, value)
		}
	}
	return ordered, nil
}

func hasSelection(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateScaffoldPrerequisites(selection serviceSelection) error {
	if !hasSelection(selection.apis, "grpc") {
		return nil
	}
	for _, tool := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
		if _, err := scaffoldLookPath(tool); err != nil {
			return fmt.Errorf("service profile with gRPC requires %s on PATH; run 'kpro doctor': %w", tool, err)
		}
	}
	return nil
}

func validatePortableServicePathSegment(value string) error {
	if strings.HasSuffix(value, ".") {
		return fmt.Errorf("invalid service name %q: a portable path segment cannot end with '.'", value)
	}
	stem := value
	if extension := strings.IndexByte(stem, '.'); extension >= 0 {
		stem = stem[:extension]
	}
	if windowsReservedDeviceName(stem) {
		return fmt.Errorf("invalid service name %q: %q is a reserved Windows device name", value, stem)
	}
	return nil
}

func windowsReservedDeviceName(value string) bool {
	upper := strings.ToUpper(value)
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(upper) != 4 || upper[3] < '1' || upper[3] > '9' {
		return false
	}
	return upper[:3] == "COM" || upper[:3] == "LPT"
}

func validateDerivedServiceName(selection serviceSelection, serviceExported string) error {
	if !hasSelection(selection.apis, "message") || len(serviceExported) <= messageServiceNameMaxLength {
		return nil
	}
	return fmt.Errorf(
		"invalid service name for Message API: derived name %q is %d characters; maximum is %d",
		serviceExported,
		len(serviceExported),
		messageServiceNameMaxLength,
	)
}

func validateScaffoldTemplateTargets(templates []scaffoldTemplate, data scaffoldData) error {
	for _, candidate := range templates {
		target, err := renderString(candidate.target, data)
		if err != nil {
			return fmt.Errorf("render target %s: %w", candidate.target, err)
		}
		if _, err := portableScaffoldPath(target); err != nil {
			return fmt.Errorf("invalid derived scaffold path %q: %w", target, err)
		}
	}
	return nil
}

func portableScaffoldPath(value string) (string, error) {
	portable, err := codegen.SafeRelativePath(value)
	if err != nil {
		return "", err
	}
	for _, segment := range strings.Split(portable, "/") {
		if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
			return "", fmt.Errorf("path segment %q cannot end with a dot or space", segment)
		}
		stem := segment
		if extension := strings.IndexByte(stem, '.'); extension >= 0 {
			stem = stem[:extension]
		}
		if windowsReservedDeviceName(stem) {
			return "", fmt.Errorf("path segment %q uses reserved Windows device name %q", segment, stem)
		}
	}
	return portable, nil
}

func serviceProfileTemplates(data scaffoldData) []scaffoldTemplate {
	templates := append([]scaffoldTemplate(nil), serviceProfileBaseTemplates...)
	if data.HasHTTP {
		templates = append(templates, serviceProfileHTTPTemplates...)
	}
	if data.HasGRPC {
		templates = append(templates, serviceProfileGRPCTemplates...)
	}
	if data.HasMessage {
		templates = append(templates, serviceProfileMessageTemplates...)
	}
	return templates
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

func exportedServiceIdentifier(value string) string {
	var builder strings.Builder
	upperNext := true
	for _, character := range value {
		if character == '_' {
			upperNext = true
			continue
		}
		if upperNext && character >= 'a' && character <= 'z' {
			character -= 'a' - 'A'
		}
		builder.WriteRune(character)
		upperNext = false
	}
	if builder.Len() == 0 {
		return "Service"
	}
	return builder.String()
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
	data, err := os.ReadFile(filepath.Join(path, "go.mod")) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
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

func ensureMissingTarget(target string) error {
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("inspect target: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("target directory %q cannot be a symbolic link", target)
	default:
		return fmt.Errorf("target path %q already exists; kpro new requires a missing target", target)
	}
}

func createScaffoldStage(target string) (string, error) {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil { // #nosec G301 -- Generated source directories intentionally allow read and traversal, without group or other write access.
		return "", fmt.Errorf("create scaffold parent: %w", err)
	}
	pattern := ".kpro-" + goIdentifier(filepath.Base(target)) + "-*"
	stage, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", fmt.Errorf("create scaffold staging directory: %w", err)
	}
	return stage, nil
}

func commitScaffoldStage(stage, target string) error {
	if err := os.Chmod(stage, 0o755); err != nil { // #nosec G302 -- Published scaffold is a source directory requiring traversal, without group or other write access.
		return fmt.Errorf("set published scaffold permissions: %w", err)
	}
	if current, err := os.Lstat(target); err == nil {
		if current.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target directory %q became a symbolic link while the scaffold was generated", target)
		}
		return fmt.Errorf("target path %q appeared while the scaffold was generated", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reinspect target before publishing scaffold: %w", err)
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("publish scaffold: %w", err)
	}
	return nil
}

func renderTemplate(root, source, target string, mode os.FileMode, data scaffoldData) error {
	files, err := planScaffoldTemplates([]scaffoldTemplate{{
		source: source,
		target: target,
		mode:   mode,
	}}, data)
	if err != nil {
		return err
	}
	return writeScaffoldFiles(root, files)
}

func planScaffoldTemplates(templates []scaffoldTemplate, data scaffoldData) ([]codegen.File, error) {
	files := make([]codegen.File, 0, len(templates))
	for _, candidate := range templates {
		body := candidate.content
		if !candidate.hasContent {
			var err error
			body, err = templateFiles.ReadFile(candidate.source)
			if err != nil {
				return nil, fmt.Errorf("read embedded template %s: %w", candidate.source, err)
			}
		}
		target, err := renderString(candidate.target, data)
		if err != nil {
			return nil, fmt.Errorf("render target %s: %w", candidate.target, err)
		}
		target, err = portableScaffoldPath(target)
		if err != nil {
			return nil, fmt.Errorf("unsafe template target %q: %w", target, err)
		}

		content := append([]byte(nil), body...)
		if !candidate.literal {
			parsed, err := template.New(candidate.source).Option("missingkey=error").Parse(string(body))
			if err != nil {
				return nil, fmt.Errorf("parse template %s: %w", candidate.source, err)
			}
			var rendered bytes.Buffer
			if err := parsed.Execute(&rendered, data); err != nil {
				return nil, fmt.Errorf("render %s: %w", target, err)
			}
			content = normalizeTextLineEndings(rendered.Bytes())
		}
		if strings.EqualFold(filepath.Ext(target), ".go") {
			content, err = format.Source(content)
			if err != nil {
				return nil, fmt.Errorf("gofmt %s: %w", target, err)
			}
		}
		mode := candidate.mode
		if mode == 0 {
			mode = 0o644
		}
		files = append(files, codegen.File{
			Path:      target,
			Content:   content,
			Ownership: codegen.Scaffold,
			Mode:      mode,
		})
	}
	plan, err := codegen.BuildPlan(files...)
	if err != nil {
		return nil, fmt.Errorf("plan scaffold templates: %w", err)
	}
	return plan.OrderedFiles()
}

func writeScaffoldFiles(root string, files []codegen.File) error {
	for _, candidate := range files {
		target, err := portableScaffoldPath(candidate.Path)
		if err != nil {
			return fmt.Errorf("unsafe planned scaffold target %q: %w", candidate.Path, err)
		}
		path := filepath.Join(root, filepath.FromSlash(target))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- Generated source directories intentionally allow read and traversal, without group or other write access.
			return fmt.Errorf("create parent directory for %s: %w", target, err)
		}
		mode := candidate.Mode
		if mode == 0 {
			mode = 0o644
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
		if err != nil {
			return fmt.Errorf("create %s: %w", target, err)
		}
		_, writeErr := file.Write(candidate.Content)
		var chmodErr error
		if writeErr == nil {
			chmodErr = file.Chmod(mode.Perm())
		}
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return fmt.Errorf("write %s: %w", target, writeErr)
		}
		if chmodErr != nil {
			_ = os.Remove(path)
			return fmt.Errorf("set permissions on %s: %w", target, chmodErr)
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return fmt.Errorf("close %s: %w", target, closeErr)
		}
	}
	return nil
}

func normalizeTextLineEndings(content []byte) []byte {
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(content, []byte("\r"), []byte("\n"))
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
