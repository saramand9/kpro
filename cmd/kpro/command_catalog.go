package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/saramand9/kpro/internal/codegen/manifest"
	"github.com/saramand9/kpro/internal/codegen/messagegen"
	openapigen "github.com/saramand9/kpro/internal/codegen/openapi"
	"github.com/saramand9/kpro/internal/codegen/protogen"
)

const catalogAPIVersion = "kpro.dev/catalog/v1alpha1"

var inspectCatalogProto = func(
	ctx context.Context,
	root string,
	value manifest.Manifest,
	source manifest.Source,
) (protogen.Contract, error) {
	return (protogen.Inspector{Root: root}).InspectContext(ctx, value, source)
}

type catalogCommandOptions struct {
	root   string
	config string
	format string
}

type catalogDocument struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Module     string           `json:"module"`
	Service    string           `json:"service"`
	Manifest   string           `json:"manifest"`
	Assembly   *catalogAssembly `json:"assembly"`
	Sources    []catalogSource  `json:"sources"`
	APIs       []catalogAPI     `json:"apis"`
}

type catalogAssembly struct {
	APIs       []string `json:"apis"`
	Transports []string `json:"transports"`
}

type catalogSource struct {
	ID         string              `json:"id"`
	Kind       manifest.SourceKind `json:"kind"`
	Path       string              `json:"path"`
	Package    string              `json:"package"`
	Groups     int                 `json:"groups"`
	Operations int                 `json:"operations"`
}

type catalogAPI struct {
	SourceID  string                 `json:"sourceId"`
	Kind      string                 `json:"kind"`
	API       string                 `json:"api"`
	Operation string                 `json:"operation"`
	HTTP      *catalogHTTPBinding    `json:"http,omitempty"`
	GRPC      *catalogGRPCBinding    `json:"grpc,omitempty"`
	Message   *catalogMessageBinding `json:"message,omitempty"`
}

type catalogHTTPBinding struct {
	Method     string                 `json:"method"`
	Path       string                 `json:"path"`
	Status     int                    `json:"status"`
	Parameters []catalogHTTPParameter `json:"parameters"`
	Request    *catalogType           `json:"request"`
	Response   *catalogType           `json:"response"`
}

type catalogHTTPParameter struct {
	Name     string      `json:"name"`
	In       string      `json:"in"`
	Required bool        `json:"required"`
	Type     catalogType `json:"type"`
}

type catalogType struct {
	Kind  string       `json:"kind,omitempty"`
	Name  string       `json:"name,omitempty"`
	Items *catalogType `json:"items,omitempty"`
}

type catalogGRPCBinding struct {
	Input           string `json:"input"`
	Output          string `json:"output"`
	ClientStreaming bool   `json:"clientStreaming"`
	ServerStreaming bool   `json:"serverStreaming"`
}

type catalogMessageBinding struct {
	ID       uint32                 `json:"id"`
	Dispatch string                 `json:"dispatch"`
	Encoding string                 `json:"encoding"`
	Request  catalogMessagePayload  `json:"request"`
	Response *catalogMessagePayload `json:"response"`
}

type catalogMessagePayload struct {
	Fields []catalogMessageField `json:"fields"`
}

type catalogMessageField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type catalogAPIList struct {
	APIVersion string       `json:"apiVersion"`
	Kind       string       `json:"kind"`
	Module     string       `json:"module"`
	Service    string       `json:"service"`
	Manifest   string       `json:"manifest"`
	APIs       []catalogAPI `json:"apis"`
}

func runList(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: kpro list api [--root dir] [--config kpro.gen.yaml] [--format text|json]")
	}
	switch args[0] {
	case "help", "-h", "--help":
		printListUsage(stderr)
		return flag.ErrHelp
	case "api":
		return runListAPI(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown list target %q; run 'kpro list --help'", args[0])
	}
}

func runDescribe(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: kpro describe service [--root dir] [--config kpro.gen.yaml] [--format text|json]")
	}
	switch args[0] {
	case "help", "-h", "--help":
		printDescribeUsage(stderr)
		return flag.ErrHelp
	case "service":
		return runDescribeService(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown describe target %q; run 'kpro describe --help'", args[0])
	}
}

func printListUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage of list:")
	fmt.Fprintln(output, "  kpro list api [--root dir] [--config kpro.gen.yaml] [--format text|json]")
}

func printDescribeUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage of describe:")
	fmt.Fprintln(output, "  kpro describe service [--root dir] [--config kpro.gen.yaml] [--format text|json]")
}

func runListAPI(args []string, stdout, stderr io.Writer) error {
	options, err := parseCatalogOptions("list api", args, stderr)
	if err != nil {
		return err
	}
	document, err := buildCatalog(context.Background(), options, false)
	if err != nil {
		return err
	}

	var rendered []byte
	switch options.format {
	case "text":
		rendered, err = renderAPIListText(document)
	case "json":
		rendered, err = renderCatalogJSON(catalogAPIList{
			APIVersion: document.APIVersion,
			Kind:       "APIList",
			Module:     document.Module,
			Service:    document.Service,
			Manifest:   document.Manifest,
			APIs:       document.APIs,
		})
	}
	if err != nil {
		return fmt.Errorf("render API catalog: %w", err)
	}
	return writeCatalogOutput(stdout, rendered)
}

func runDescribeService(args []string, stdout, stderr io.Writer) error {
	options, err := parseCatalogOptions("describe service", args, stderr)
	if err != nil {
		return err
	}
	document, err := buildCatalog(context.Background(), options, true)
	if err != nil {
		return err
	}

	var rendered []byte
	switch options.format {
	case "text":
		rendered, err = renderServiceDescriptionText(document)
	case "json":
		rendered, err = renderCatalogJSON(document)
	}
	if err != nil {
		return fmt.Errorf("render service description: %w", err)
	}
	return writeCatalogOutput(stdout, rendered)
}

func parseCatalogOptions(command string, args []string, stderr io.Writer) (catalogCommandOptions, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	config := flags.String("config", defaultCodegenConfig, "generation manifest path")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return catalogCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return catalogCommandOptions{}, fmt.Errorf(
			"usage: kpro %s [--root dir] [--config kpro.gen.yaml] [--format text|json]",
			command,
		)
	}
	resolvedFormat := strings.ToLower(strings.TrimSpace(*format))
	if resolvedFormat != "text" && resolvedFormat != "json" {
		return catalogCommandOptions{}, fmt.Errorf(
			"%s: unsupported format %q (supported: text, json)",
			command,
			*format,
		)
	}
	return catalogCommandOptions{
		root:   *root,
		config: *config,
		format: resolvedFormat,
	}, nil
}

func buildCatalog(
	ctx context.Context,
	options catalogCommandOptions,
	includeAssembly bool,
) (catalogDocument, error) {
	if ctx == nil {
		return catalogDocument{}, errors.New("build API catalog: context is nil")
	}
	input, err := loadCodegenInput(options.root, options.config)
	if err != nil {
		return catalogDocument{}, err
	}
	for _, source := range input.manifest.Sources {
		if source.Plugin != nil {
			return catalogDocument{}, fmt.Errorf(
				"inspect source %q: external plugin catalogs are not supported; list and describe never execute plugins",
				source.ID,
			)
		}
	}

	document := catalogDocument{
		APIVersion: catalogAPIVersion,
		Kind:       "ServiceDescription",
		Module:     input.manifest.Module,
		Service:    input.manifest.Service,
		Manifest:   input.configPath,
		Sources:    make([]catalogSource, 0, len(input.manifest.Sources)),
		APIs:       make([]catalogAPI, 0),
	}
	if includeAssembly {
		project, configured, err := loadConfiguredServiceProject(input.root)
		if err != nil {
			return catalogDocument{}, err
		}
		if configured {
			if _, err := buildServiceAssemblyPlan(project, input.manifest); err != nil {
				return catalogDocument{}, err
			}
			document.Assembly = &catalogAssembly{
				APIs:       append([]string{}, project.APIs...),
				Transports: append([]string{}, project.Transports...),
			}
		}
	}

	for _, source := range input.manifest.Sources {
		summary := catalogSource{
			ID:      source.ID,
			Kind:    source.Kind,
			Path:    source.Path,
			Package: source.Package,
		}
		switch source.Kind {
		case manifest.KindOpenAPI:
			filename, err := resolveProjectFile(input.root, source.Path)
			if err != nil {
				return catalogDocument{}, fmt.Errorf("inspect source %q: %w", source.ID, err)
			}
			contract, err := openapigen.Load(filename)
			if err != nil {
				return catalogDocument{}, fmt.Errorf("inspect source %q: %w", source.ID, err)
			}
			if _, err := openapigen.BuildPlan(contract, openapigen.Options{
				Module: input.manifest.Module,
				Output: source.Package,
			}); err != nil {
				return catalogDocument{}, fmt.Errorf("inspect source %q: %w", source.ID, err)
			}
			entries := catalogOpenAPI(source, contract)
			summary.Groups = 1
			summary.Operations = len(entries)
			document.APIs = append(document.APIs, entries...)
		case manifest.KindProto:
			contract, err := inspectCatalogProto(ctx, input.root, input.manifest, source)
			if err != nil {
				return catalogDocument{}, fmt.Errorf("inspect source %q: %w", source.ID, err)
			}
			entries := catalogProto(source, contract)
			summary.Groups = len(contract.Services)
			summary.Operations = len(entries)
			document.APIs = append(document.APIs, entries...)
		case manifest.KindMessage:
			filename, err := resolveProjectFile(input.root, source.Path)
			if err != nil {
				return catalogDocument{}, fmt.Errorf("inspect source %q: %w", source.ID, err)
			}
			contract, err := messagegen.Load(filename)
			if err != nil {
				return catalogDocument{}, fmt.Errorf("inspect source %q: %w", source.ID, err)
			}
			if _, err := messagegen.BuildPlan(contract, messagegen.Options{
				Module: input.manifest.Module,
				Output: source.Package,
			}); err != nil {
				return catalogDocument{}, fmt.Errorf("inspect source %q: %w", source.ID, err)
			}
			entries := catalogMessages(source, contract)
			summary.Groups = 1
			summary.Operations = len(entries)
			document.APIs = append(document.APIs, entries...)
		default:
			return catalogDocument{}, fmt.Errorf("inspect source %q: unsupported source kind %q", source.ID, source.Kind)
		}
		document.Sources = append(document.Sources, summary)
	}
	sort.Slice(document.Sources, func(left, right int) bool {
		return document.Sources[left].ID < document.Sources[right].ID
	})
	sort.Slice(document.APIs, func(left, right int) bool {
		return catalogAPILess(document.APIs[left], document.APIs[right])
	})
	return document, nil
}

func catalogAPILess(left, right catalogAPI) bool {
	if left.SourceID != right.SourceID {
		return left.SourceID < right.SourceID
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	switch {
	case left.HTTP != nil && right.HTTP != nil:
		if left.HTTP.Path != right.HTTP.Path {
			return left.HTTP.Path < right.HTTP.Path
		}
		if left.HTTP.Method != right.HTTP.Method {
			return left.HTTP.Method < right.HTTP.Method
		}
	case left.GRPC != nil && right.GRPC != nil:
		if left.API != right.API {
			return left.API < right.API
		}
	case left.Message != nil && right.Message != nil:
		if left.Message.ID != right.Message.ID {
			return left.Message.ID < right.Message.ID
		}
	}
	if left.API != right.API {
		return left.API < right.API
	}
	return left.Operation < right.Operation
}

func catalogOpenAPI(source manifest.Source, document openapigen.Document) []catalogAPI {
	operations := append([]openapigen.Operation(nil), document.Operations...)
	sort.Slice(operations, func(left, right int) bool {
		if operations[left].Path != operations[right].Path {
			return operations[left].Path < operations[right].Path
		}
		if operations[left].Method != operations[right].Method {
			return operations[left].Method < operations[right].Method
		}
		return operations[left].ID < operations[right].ID
	})
	apiName := document.Title
	if strings.TrimSpace(apiName) == "" {
		apiName = source.ID
	}
	entries := make([]catalogAPI, 0, len(operations))
	for _, operation := range operations {
		parameters := append([]openapigen.Parameter(nil), operation.Parameters...)
		sort.Slice(parameters, func(left, right int) bool {
			if parameters[left].In != parameters[right].In {
				return parameters[left].In < parameters[right].In
			}
			return parameters[left].Name < parameters[right].Name
		})
		binding := &catalogHTTPBinding{
			Method:     operation.Method,
			Path:       operation.Path,
			Status:     operation.Status,
			Parameters: make([]catalogHTTPParameter, 0, len(parameters)),
			Request:    catalogOpenAPIType(operation.Request),
			Response:   catalogOpenAPIType(operation.Response),
		}
		for _, parameter := range parameters {
			binding.Parameters = append(binding.Parameters, catalogHTTPParameter{
				Name:     parameter.Name,
				In:       parameter.In,
				Required: parameter.Required,
				Type:     *catalogOpenAPIType(&parameter.Type),
			})
		}
		entries = append(entries, catalogAPI{
			SourceID:  source.ID,
			Kind:      "http",
			API:       apiName,
			Operation: operation.ID,
			HTTP:      binding,
		})
	}
	return entries
}

func catalogOpenAPIType(reference *openapigen.TypeRef) *catalogType {
	if reference == nil {
		return nil
	}
	return &catalogType{
		Kind:  string(reference.Kind),
		Name:  reference.Name,
		Items: catalogOpenAPIType(reference.Items),
	}
}

func catalogProto(source manifest.Source, contract protogen.Contract) []catalogAPI {
	services := append([]protogen.Service(nil), contract.Services...)
	sort.Slice(services, func(left, right int) bool {
		return services[left].FullName < services[right].FullName
	})
	var entries []catalogAPI
	for _, service := range services {
		methods := append([]protogen.Method(nil), service.Methods...)
		sort.Slice(methods, func(left, right int) bool {
			return methods[left].Name < methods[right].Name
		})
		for _, method := range methods {
			entries = append(entries, catalogAPI{
				SourceID:  source.ID,
				Kind:      "grpc",
				API:       service.FullName,
				Operation: method.Name,
				GRPC: &catalogGRPCBinding{
					Input:           method.Input,
					Output:          method.Output,
					ClientStreaming: method.ClientStreaming,
					ServerStreaming: method.ServerStreaming,
				},
			})
		}
	}
	return entries
}

func catalogMessages(source manifest.Source, document messagegen.Document) []catalogAPI {
	messages := append([]messagegen.Message(nil), document.Messages...)
	sort.Slice(messages, func(left, right int) bool {
		if messages[left].ID != messages[right].ID {
			return messages[left].ID < messages[right].ID
		}
		return messages[left].Name < messages[right].Name
	})
	entries := make([]catalogAPI, 0, len(messages))
	for _, message := range messages {
		binding := &catalogMessageBinding{
			ID:       message.ID,
			Dispatch: string(message.Dispatch),
			Encoding: string(document.Encoding),
			Request:  catalogMessagePayloadOf(message.Request),
		}
		if message.Response != nil {
			response := catalogMessagePayloadOf(*message.Response)
			binding.Response = &response
		}
		entries = append(entries, catalogAPI{
			SourceID:  source.ID,
			Kind:      "message",
			API:       document.Service,
			Operation: message.Name,
			Message:   binding,
		})
	}
	return entries
}

func catalogMessagePayloadOf(payload messagegen.Payload) catalogMessagePayload {
	fields := append([]messagegen.Field(nil), payload.Fields...)
	sort.Slice(fields, func(left, right int) bool {
		return fields[left].Name < fields[right].Name
	})
	result := catalogMessagePayload{Fields: make([]catalogMessageField, 0, len(fields))}
	for _, field := range fields {
		result.Fields = append(result.Fields, catalogMessageField{
			Name:     field.Name,
			Type:     string(field.Type),
			Required: field.Required,
		})
	}
	return result
}

func renderAPIListText(document catalogDocument) ([]byte, error) {
	var output bytes.Buffer
	table := tabwriter.NewWriter(&output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "KIND\tSOURCE\tAPI\tOPERATION\tBINDING"); err != nil {
		return nil, err
	}
	for _, entry := range document.APIs {
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%s\n",
			catalogCell(entry.Kind),
			catalogCell(entry.SourceID),
			catalogCell(entry.API),
			catalogCell(entry.Operation),
			catalogCell(catalogBinding(entry)),
		); err != nil {
			return nil, err
		}
	}
	if err := table.Flush(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func renderServiceDescriptionText(document catalogDocument) ([]byte, error) {
	var output bytes.Buffer
	fmt.Fprintf(&output, "Service: %s\n", catalogCell(document.Service))
	fmt.Fprintf(&output, "Module: %s\n", catalogCell(document.Module))
	fmt.Fprintf(&output, "Manifest: %s\n", catalogCell(document.Manifest))
	if document.Assembly == nil {
		fmt.Fprintln(&output, "Assembly: not configured")
	} else {
		fmt.Fprintf(&output, "Assembly APIs: %s\n", catalogSelection(document.Assembly.APIs))
		fmt.Fprintf(&output, "Message transports: %s\n", catalogSelection(document.Assembly.Transports))
	}
	fmt.Fprintln(&output, "Scope: declared contracts (not runtime registrations)")
	fmt.Fprintln(&output)

	table := tabwriter.NewWriter(&output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SOURCE\tKIND\tCONTRACT\tPACKAGE\tCONTENTS"); err != nil {
		return nil, err
	}
	for _, source := range document.Sources {
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%s\n",
			catalogCell(source.ID),
			catalogCell(string(source.Kind)),
			catalogCell(source.Path),
			catalogCell(source.Package),
			catalogSourceContents(source),
		); err != nil {
			return nil, err
		}
	}
	if err := table.Flush(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func renderCatalogJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeCatalogOutput(output io.Writer, content []byte) error {
	if _, err := io.Copy(output, bytes.NewReader(content)); err != nil {
		return fmt.Errorf("write catalog output: %w", err)
	}
	return nil
}

func catalogBinding(entry catalogAPI) string {
	switch {
	case entry.HTTP != nil:
		return fmt.Sprintf("%s %s -> %d", entry.HTTP.Method, entry.HTTP.Path, entry.HTTP.Status)
	case entry.GRPC != nil:
		switch {
		case entry.GRPC.ClientStreaming && entry.GRPC.ServerStreaming:
			return "bidi-streaming"
		case entry.GRPC.ClientStreaming:
			return "client-streaming"
		case entry.GRPC.ServerStreaming:
			return "server-streaming"
		default:
			return "unary"
		}
	case entry.Message != nil:
		return fmt.Sprintf("id=%d %s", entry.Message.ID, entry.Message.Dispatch)
	default:
		return "unknown"
	}
}

func catalogCell(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		if !catalogTextControl(character) {
			escaped.WriteRune(character)
			continue
		}
		if character <= 0xffff {
			fmt.Fprintf(&escaped, "\\u%04X", character)
			continue
		}
		fmt.Fprintf(&escaped, "\\U%08X", character)
	}
	return strings.Join(strings.Fields(escaped.String()), " ")
}

func catalogTextControl(character rune) bool {
	return character <= 0x1f ||
		character >= 0x7f && character <= 0x9f ||
		unicode.Is(unicode.Cf, character)
}

func catalogSelection(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func catalogSourceContents(source catalogSource) string {
	switch source.Kind {
	case manifest.KindOpenAPI:
		return catalogCount(source.Groups, "API", "APIs") + ", " +
			catalogCount(source.Operations, "operation", "operations")
	case manifest.KindProto:
		return catalogCount(source.Groups, "service", "services") + ", " +
			catalogCount(source.Operations, "method", "methods")
	case manifest.KindMessage:
		return catalogCount(source.Groups, "service", "services") + ", " +
			catalogCount(source.Operations, "message", "messages")
	default:
		return catalogCount(source.Groups, "group", "groups") + ", " +
			catalogCount(source.Operations, "operation", "operations")
	}
}

func catalogCount(value int, singular, plural string) string {
	label := plural
	if value == 1 {
		label = singular
	}
	return fmt.Sprintf("%d %s", value, label)
}
