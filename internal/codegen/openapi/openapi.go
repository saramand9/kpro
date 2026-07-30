// Package openapi lowers a deliberately small, checked OpenAPI 3.0.3 subset
// into KPro's generator-owned representation.  The exported IR is independent
// of kin-openapi, which is used only while loading a source document.
package openapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"
)

var ErrUnsupported = errors.New("openapi: unsupported feature")

// Document is the generator's stable input model. It intentionally does not
// expose parser-library values.
type Document struct {
	Title      string
	Components []Schema
	Operations []Operation
}

type Schema struct {
	Name       string
	Kind       Kind
	Properties []Field
	Items      *TypeRef
}

type Kind string

const (
	String  Kind = "string"
	Integer Kind = "integer"
	Number  Kind = "number"
	Boolean Kind = "boolean"
	Object  Kind = "object"
	Array   Kind = "array"
)

type TypeRef struct {
	Kind  Kind
	Name  string // component name when Kind is empty
	Items *TypeRef
}

type Field struct {
	Name     string
	JSONName string
	Type     TypeRef
	Required bool
}

type Operation struct {
	ID              string
	Method          string
	Path            string
	Parameters      []Parameter
	Request         *TypeRef
	RequestRequired bool
	Response        *TypeRef
	Status          int
}

type Parameter struct {
	Name     string
	GoName   string
	In       string
	Type     TypeRef
	Required bool
}

// Parse reads an OpenAPI 3.0.3 YAML or JSON document. References to another
// document are disabled by the loader; only local component schema references
// are accepted by the lowering pass.
func Parse(data []byte) (Document, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	spec, err := loader.LoadFromData(data)
	if err != nil {
		if strings.Contains(err.Error(), "disallowed external reference") {
			return Document{}, fmt.Errorf("%w: %v", ErrUnsupported, err)
		}
		return Document{}, fmt.Errorf("openapi: parse document: %w", err)
	}
	return lower(spec)
}

func ParseReader(reader io.Reader) (Document, error) {
	if reader == nil {
		return Document{}, errors.New("openapi: reader is nil")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return Document{}, fmt.Errorf("openapi: read document: %w", err)
	}
	return Parse(data)
}

func Load(filename string) (Document, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Document{}, fmt.Errorf("openapi: read %q: %w", filename, err)
	}
	return Parse(data)
}

func lower(spec *openapi3.T) (Document, error) {
	if spec == nil {
		return Document{}, errors.New("openapi: document is nil")
	}
	if spec.OpenAPI != "3.0.3" {
		return Document{}, fmt.Errorf("%w: OpenAPI version %q (only 3.0.3 is accepted)", ErrUnsupported, spec.OpenAPI)
	}
	if len(spec.Security) != 0 {
		return Document{}, fmt.Errorf("%w: document-level security requirements", ErrUnsupported)
	}
	doc := Document{}
	if spec.Info != nil {
		doc.Title = spec.Info.Title
	}
	if spec.Components != nil {
		names := make([]string, 0, len(spec.Components.Schemas))
		for name := range spec.Components.Schemas {
			names = append(names, name)
		}
		sort.Strings(names)
		seen := map[string]string{}
		for _, name := range names {
			goName, err := exported(name)
			if err != nil {
				return Document{}, fmt.Errorf("openapi: component %q: %w", name, err)
			}
			if previous := seen[goName]; previous != "" {
				return Document{}, fmt.Errorf("openapi: component names %q and %q both become Go identifier %q", previous, name, goName)
			}
			seen[goName] = name
			value := spec.Components.Schemas[name]
			if value == nil || value.Value == nil {
				return Document{}, fmt.Errorf("openapi: component schema %q is empty", name)
			}
			typeRef, properties, items, err := lowerSchema(value, "components.schemas."+name)
			if err != nil {
				return Document{}, err
			}
			if typeRef.Name != "" {
				return Document{}, fmt.Errorf("%w: component schema %q cannot be only a reference", ErrUnsupported, name)
			}
			doc.Components = append(doc.Components, Schema{Name: goName, Kind: typeRef.Kind, Properties: properties, Items: items})
		}
	}
	if spec.Paths == nil {
		return Document{}, errors.New("openapi: paths is required")
	}
	paths := spec.Paths.Map()
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	operationIDs := map[string]struct{}{}
	for _, path := range pathNames {
		item := paths[path]
		if item == nil {
			return Document{}, fmt.Errorf("openapi: path %q is empty", path)
		}
		for _, pair := range operations(item) {
			op, err := lowerOperation(path, pair.method, pair.operation, append(append(openapi3.Parameters(nil), item.Parameters...), pair.operation.Parameters...))
			if err != nil {
				return Document{}, err
			}
			if _, exists := operationIDs[op.ID]; exists {
				return Document{}, fmt.Errorf("openapi: duplicate operationId %q", op.ID)
			}
			operationIDs[op.ID] = struct{}{}
			doc.Operations = append(doc.Operations, op)
		}
	}
	if len(doc.Operations) == 0 {
		return Document{}, fmt.Errorf("openapi: document has no operations")
	}
	return doc, nil
}

type operationPair struct {
	method    string
	operation *openapi3.Operation
}

func operations(item *openapi3.PathItem) []operationPair {
	all := []operationPair{{"GET", item.Get}, {"POST", item.Post}, {"PUT", item.Put}, {"PATCH", item.Patch}, {"DELETE", item.Delete}, {"HEAD", item.Head}, {"OPTIONS", item.Options}, {"TRACE", item.Trace}}
	result := all[:0]
	for _, pair := range all {
		if pair.operation != nil {
			result = append(result, pair)
		}
	}
	return result
}

func pathTemplateNames(path string) (map[string]bool, error) {
	if path == "" || path[0] != '/' {
		return nil, fmt.Errorf("openapi: path %q must begin with /", path)
	}
	names := make(map[string]bool)
	for index := 0; index < len(path); {
		switch path[index] {
		case '{':
			end := strings.IndexByte(path[index+1:], '}')
			if end < 0 {
				return nil, fmt.Errorf("openapi: path %q has an unclosed template", path)
			}
			end += index + 1
			name := path[index+1 : end]
			if !validTemplateName(name) {
				return nil, fmt.Errorf("openapi: path %q has invalid template name %q", path, name)
			}
			if names[name] {
				return nil, fmt.Errorf("openapi: path %q repeats template %q", path, name)
			}
			names[name] = true
			index = end + 1
		case '}':
			return nil, fmt.Errorf("openapi: path %q has an unmatched }", path)
		default:
			index++
		}
	}
	return names, nil
}

func validTemplateName(name string) bool {
	if name == "" {
		return false
	}
	for index, value := range name {
		if !(value == '_' || unicode.IsLetter(value) || (index != 0 && unicode.IsDigit(value))) {
			return false
		}
	}
	return true
}

func lowerOperation(path, method string, source *openapi3.Operation, parameters openapi3.Parameters) (Operation, error) {
	if source == nil {
		return Operation{}, errors.New("openapi: operation is nil")
	}
	if source.Security != nil && len(*source.Security) != 0 {
		return Operation{}, fmt.Errorf("%w: %s %s operation-level security requirements", ErrUnsupported, method, path)
	}
	if len(source.Callbacks) != 0 {
		return Operation{}, fmt.Errorf("%w: %s %s callbacks", ErrUnsupported, method, path)
	}
	templateNames, err := pathTemplateNames(path)
	if err != nil {
		return Operation{}, err
	}
	id, err := exported(source.OperationID)
	if err != nil {
		return Operation{}, fmt.Errorf("openapi: %s %s: operationId: %w", method, path, err)
	}
	op := Operation{ID: id, Method: method, Path: path, Status: 200}
	parameterNames := map[string]struct{}{}
	parameterGoNames := map[string]string{}
	pathParameters := map[string]bool{}
	for _, ref := range parameters {
		if ref == nil || ref.Value == nil {
			return Operation{}, fmt.Errorf("%w: %s %s uses a parameter reference", ErrUnsupported, method, path)
		}
		p := ref.Value
		if ref.Ref != "" {
			return Operation{}, fmt.Errorf("%w: %s %s parameter %q is a reference", ErrUnsupported, method, path, p.Name)
		}
		if p.In != "path" && p.In != "query" && p.In != "header" {
			return Operation{}, fmt.Errorf("%w: %s %s parameter %q in %q", ErrUnsupported, method, path, p.Name, p.In)
		}
		if p.Content != nil {
			return Operation{}, fmt.Errorf("%w: parameter %q uses content", ErrUnsupported, p.Name)
		}
		if p.AllowEmptyValue || p.AllowReserved || p.Style != "" || p.Explode != nil {
			return Operation{}, fmt.Errorf("%w: parameter %q uses allowEmptyValue, allowReserved, style, or explode", ErrUnsupported, p.Name)
		}
		if p.In == "path" {
			if !p.Required {
				return Operation{}, fmt.Errorf("openapi: %s %s path parameter %q must be required", method, path, p.Name)
			}
			if !templateNames[p.Name] {
				return Operation{}, fmt.Errorf("openapi: %s %s path parameter %q has no matching template", method, path, p.Name)
			}
			pathParameters[p.Name] = true
		}
		if p.Schema == nil || p.Schema.Value == nil || p.Schema.Ref != "" {
			return Operation{}, fmt.Errorf("%w: parameter %q must have an inline scalar schema", ErrUnsupported, p.Name)
		}
		typ, _, _, err := lowerSchema(p.Schema, "parameter "+p.Name)
		if err != nil {
			return Operation{}, err
		}
		if typ.Name != "" || !scalar(typ.Kind) {
			return Operation{}, fmt.Errorf("%w: parameter %q must be scalar", ErrUnsupported, p.Name)
		}
		goName, err := exported(p.Name)
		if err != nil {
			return Operation{}, fmt.Errorf("openapi: parameter %q: %w", p.Name, err)
		}
		key := p.In + ":" + p.Name
		if _, exists := parameterNames[key]; exists {
			return Operation{}, fmt.Errorf("openapi: %s %s has duplicate parameter %s", method, path, key)
		}
		if other, exists := parameterGoNames[goName]; exists {
			return Operation{}, fmt.Errorf("openapi: %s %s parameters %q and %q both become Go field %q", method, path, other, p.Name, goName)
		}
		parameterNames[key] = struct{}{}
		parameterGoNames[goName] = p.Name
		op.Parameters = append(op.Parameters, Parameter{Name: p.Name, GoName: goName, In: p.In, Type: typ, Required: p.Required || p.In == "path"})
	}
	for name := range templateNames {
		if !pathParameters[name] {
			return Operation{}, fmt.Errorf("openapi: %s %s template parameter %q has no required path parameter", method, path, name)
		}
	}
	if source.RequestBody != nil {
		if method == "GET" {
			return Operation{}, fmt.Errorf("%w: GET operation %q has a request body", ErrUnsupported, op.ID)
		}
		if source.RequestBody.Ref != "" || source.RequestBody.Value == nil {
			return Operation{}, fmt.Errorf("%w: %s %s requestBody references are not supported", ErrUnsupported, method, path)
		}
		if !source.RequestBody.Value.Required {
			return Operation{}, fmt.Errorf("%w: %s %s optional request body", ErrUnsupported, method, path)
		}
		request, err := requestSchema(source.RequestBody.Value, method+" "+path+" request body")
		if err != nil {
			return Operation{}, err
		}
		op.Request = request
		op.RequestRequired = true
	} else if method == "POST" || method == "PUT" || method == "PATCH" {
		return Operation{}, fmt.Errorf("%w: %s %s requires a request body", ErrUnsupported, method, path)
	}
	if source.Responses == nil {
		return Operation{}, fmt.Errorf("openapi: %s %s has no responses", method, path)
	}
	responses := source.Responses.Map()
	for status := range responses {
		if status == "2XX" {
			return Operation{}, fmt.Errorf("%w: wildcard success response %q", ErrUnsupported, status)
		}
		code, err := strconv.Atoi(status)
		if err == nil && code >= 200 && code < 300 && code != 200 && code != 201 && code != 202 && code != 204 {
			return Operation{}, fmt.Errorf("%w: success response %d", ErrUnsupported, code)
		}
	}
	response204 := responses["204"]
	if response204 != nil && response204.Value != nil && len(response204.Value.Content) != 0 {
		return Operation{}, fmt.Errorf("%w: response 204 must not declare content", ErrUnsupported)
	}
	var selectedResponse *TypeRef
	var selectedStatus int
	for _, status := range []int{200, 201, 202} {
		response := responses[strconv.Itoa(status)]
		if response == nil || response.Value == nil {
			continue
		}
		value, err := mediaSchema(response.Value, fmt.Sprintf("response %d", status))
		if err != nil {
			return Operation{}, err
		}
		if value == nil || value.Name == "" {
			return Operation{}, fmt.Errorf("%w: response %d must reference a local component schema", ErrUnsupported, status)
		}
		if selectedResponse != nil {
			return Operation{}, fmt.Errorf("%w: %s %s declares multiple success responses", ErrUnsupported, method, path)
		}
		selectedStatus, selectedResponse = status, value
	}
	if selectedResponse != nil {
		if response204 != nil && response204.Value != nil {
			return Operation{}, fmt.Errorf("%w: %s %s declares multiple success responses", ErrUnsupported, method, path)
		}
		op.Status, op.Response = selectedStatus, selectedResponse
		return op, nil
	}
	if response204 != nil && response204.Value != nil {
		op.Status = 204
		return op, nil
	}
	return Operation{}, fmt.Errorf("%w: %s %s needs a 200, 201, 202 JSON component response or 204", ErrUnsupported, method, path)
}

func requestSchema(value *openapi3.RequestBody, location string) (*TypeRef, error) {
	if value == nil || len(value.Content) != 1 {
		return nil, fmt.Errorf("%w: %s must contain only application/json", ErrUnsupported, location)
	}
	media := value.Content["application/json"]
	if media == nil || media.Schema == nil {
		return nil, fmt.Errorf("%w: %s must use application/json with a schema", ErrUnsupported, location)
	}
	typeRef, _, _, err := lowerSchema(media.Schema, location)
	if err != nil {
		return nil, err
	}
	if typeRef.Name == "" {
		return nil, fmt.Errorf("%w: %s must reference a local component schema", ErrUnsupported, location)
	}
	return &typeRef, nil
}

func mediaSchema(value interface{}, location string) (*TypeRef, error) {
	var content openapi3.Content
	switch value := value.(type) {
	case *openapi3.RequestBody:
		content = value.Content
	case *openapi3.Response:
		content = value.Content
	default:
		return nil, fmt.Errorf("openapi: internal: unexpected %T", value)
	}
	if len(content) != 1 {
		return nil, fmt.Errorf("%w: %s must contain only application/json", ErrUnsupported, location)
	}
	media := content["application/json"]
	if media == nil || media.Schema == nil {
		return nil, fmt.Errorf("%w: %s must use application/json with a schema", ErrUnsupported, location)
	}
	typeRef, _, _, err := lowerSchema(media.Schema, location)
	if err != nil {
		return nil, err
	}
	return &typeRef, nil
}

func lowerSchema(ref *openapi3.SchemaRef, location string) (TypeRef, []Field, *TypeRef, error) {
	if ref == nil || ref.Value == nil {
		return TypeRef{}, nil, nil, fmt.Errorf("openapi: %s schema is empty", location)
	}
	if ref.Ref != "" {
		if !strings.HasPrefix(ref.Ref, "#/components/schemas/") {
			return TypeRef{}, nil, nil, fmt.Errorf("%w: %s uses non-local schema reference %q", ErrUnsupported, location, ref.Ref)
		}
		name, err := exported(strings.TrimPrefix(ref.Ref, "#/components/schemas/"))
		if err != nil {
			return TypeRef{}, nil, nil, err
		}
		return TypeRef{Name: name}, nil, nil, nil
	}
	schema := ref.Value
	if schema.Nullable || len(schema.OneOf) != 0 || len(schema.AnyOf) != 0 || len(schema.AllOf) != 0 || schema.Not != nil || len(schema.Enum) != 0 || schema.AdditionalProperties.Has != nil || schema.AdditionalProperties.Schema != nil {
		return TypeRef{}, nil, nil, fmt.Errorf("%w: %s uses a composition, enum, nullable, or additionalProperties schema", ErrUnsupported, location)
	}
	if schema.Type == nil || len(schema.Type.Slice()) != 1 {
		return TypeRef{}, nil, nil, fmt.Errorf("%w: %s schema type is required", ErrUnsupported, location)
	}
	kind := Kind(schema.Type.Slice()[0])
	switch kind {
	case String, Integer, Number, Boolean:
		return TypeRef{Kind: kind}, nil, nil, nil
	case Array:
		item, _, _, err := lowerSchema(schema.Items, location+".items")
		if err != nil {
			return TypeRef{}, nil, nil, err
		}
		if item.Name == "" && item.Kind == Object {
			return TypeRef{}, nil, nil, fmt.Errorf("%w: %s array items must use a component object schema", ErrUnsupported, location)
		}
		return TypeRef{Kind: Array, Items: &item}, nil, &item, nil
	case Object:
		required := map[string]bool{}
		for _, name := range schema.Required {
			required[name] = true
		}
		names := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		fields := make([]Field, 0, len(names))
		used := map[string]string{}
		for _, name := range names {
			goName, err := exported(name)
			if err != nil {
				return TypeRef{}, nil, nil, err
			}
			if other := used[goName]; other != "" {
				return TypeRef{}, nil, nil, fmt.Errorf("openapi: %s properties %q and %q both become %q", location, other, name, goName)
			}
			used[goName] = name
			fieldType, _, _, err := lowerSchema(schema.Properties[name], location+".properties."+name)
			if err != nil {
				return TypeRef{}, nil, nil, err
			}
			if fieldType.Name == "" && fieldType.Kind == Object {
				return TypeRef{}, nil, nil, fmt.Errorf("%w: %s property %q must use a component object schema", ErrUnsupported, location, name)
			}
			fields = append(fields, Field{Name: goName, JSONName: name, Type: fieldType, Required: required[name]})
		}
		return TypeRef{Kind: Object}, fields, nil, nil
	default:
		return TypeRef{}, nil, nil, fmt.Errorf("%w: %s schema type %q", ErrUnsupported, location, kind)
	}
}

func scalar(kind Kind) bool {
	return kind == String || kind == Integer || kind == Number || kind == Boolean
}
func exported(value string) (string, error) {
	words := strings.FieldsFunc(value, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
	if len(words) == 0 {
		return "", errors.New("must contain a letter or digit")
	}
	var out bytes.Buffer
	for _, word := range words {
		for index, r := range word {
			if index == 0 {
				out.WriteRune(unicode.ToUpper(r))
			} else {
				out.WriteRune(r)
			}
		}
	}
	name := out.String()
	if name == "" || unicode.IsDigit(rune(name[0])) {
		return "", fmt.Errorf("%q is not a Go identifier", value)
	}
	if name == "Context" || name == "API" || name == "Register" {
		return "", fmt.Errorf("%q conflicts with generated name", value)
	}
	return name, nil
}
