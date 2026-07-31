package openapi

import (
	"fmt"
	"go/token"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/saramand9/kpro/internal/codegen"
	"golang.org/x/mod/module"
)

const defaultOutput = "internal/openapi"

var reservedHeaderParameters = map[string]struct{}{
	"accept":            {},
	"accept-encoding":   {},
	"connection":        {},
	"content-encoding":  {},
	"content-length":    {},
	"content-type":      {},
	"host":              {},
	"keep-alive":        {},
	"proxy-connection":  {},
	"te":                {},
	"trailer":           {},
	"transfer-encoding": {},
	"upgrade":           {},
}

// Validate checks the exported IR independently of the OpenAPI parser. This
// keeps callers which construct a Document directly from bypassing invariants
// required by the generated Go packages and HTTP routes.
func (doc Document) Validate() error {
	components, err := validateComponents(doc.Components)
	if err != nil {
		return err
	}
	if len(doc.Operations) == 0 {
		return fmt.Errorf("openapi: document has no operations")
	}

	operationIDs := make(map[string]struct{}, len(doc.Operations))
	for index, operation := range doc.Operations {
		location := fmt.Sprintf("operation %d", index)
		if err := validateGeneratedIdentifier(operation.ID); err != nil {
			return fmt.Errorf("openapi: %s ID %q: %w", location, operation.ID, err)
		}
		if _, exists := operationIDs[operation.ID]; exists {
			return fmt.Errorf("openapi: duplicate operation ID %q", operation.ID)
		}
		operationIDs[operation.ID] = struct{}{}
		if err := validateOperation(operation, components); err != nil {
			return err
		}
	}

	if err := validateRequiredReferenceCycles(doc); err != nil {
		return err
	}
	if err := validateRoutePatterns(doc.Operations); err != nil {
		return err
	}
	return nil
}

func validateComponents(schemas []Schema) (map[string]Schema, error) {
	components := make(map[string]Schema, len(schemas))
	for index, schema := range schemas {
		if err := validateGeneratedIdentifier(schema.Name); err != nil {
			return nil, fmt.Errorf("openapi: component %d name %q: %w", index, schema.Name, err)
		}
		if _, exists := components[schema.Name]; exists {
			return nil, fmt.Errorf("openapi: duplicate component %q", schema.Name)
		}
		components[schema.Name] = schema
	}

	for _, schema := range schemas {
		location := "component " + schema.Name
		switch schema.Kind {
		case Object:
			if schema.Items != nil {
				return nil, fmt.Errorf("openapi: %s object must not declare array items", location)
			}
			if err := validateFields(schema, components); err != nil {
				return nil, err
			}
		case Array:
			if len(schema.Properties) != 0 {
				return nil, fmt.Errorf("openapi: %s array must not declare object properties", location)
			}
			if schema.Items == nil {
				return nil, fmt.Errorf("openapi: %s array has no items", location)
			}
			if err := validateTypeRef(schema.Items, components, location+" items", make(map[*TypeRef]bool)); err != nil {
				return nil, err
			}
		case String, Integer, Number, Boolean:
			if len(schema.Properties) != 0 || schema.Items != nil {
				return nil, fmt.Errorf("openapi: %s scalar must not declare properties or array items", location)
			}
		default:
			return nil, fmt.Errorf("%w: %s has schema kind %q", ErrUnsupported, location, schema.Kind)
		}
	}
	return components, nil
}

func validateFields(schema Schema, components map[string]Schema) error {
	fieldNames := make(map[string]struct{}, len(schema.Properties))
	jsonNames := make(map[string]struct{}, len(schema.Properties))
	for index := range schema.Properties {
		field := &schema.Properties[index]
		location := fmt.Sprintf("component %s field %d", schema.Name, index)
		if err := validateGeneratedIdentifier(field.Name); err != nil {
			return fmt.Errorf("openapi: %s name %q: %w", location, field.Name, err)
		}
		if field.Name == "UnmarshalJSON" || field.Name == "ValidateJSON" {
			return fmt.Errorf("openapi: component %s field %q conflicts with generated method", schema.Name, field.Name)
		}
		if _, exists := fieldNames[field.Name]; exists {
			return fmt.Errorf("openapi: component %s has duplicate field %q", schema.Name, field.Name)
		}
		fieldNames[field.Name] = struct{}{}
		if !validJSONTagName(field.JSONName) {
			return fmt.Errorf("%w: %s JSON name %q cannot be represented exactly by encoding/json", ErrUnsupported, location, field.JSONName)
		}
		if _, exists := jsonNames[field.JSONName]; exists {
			return fmt.Errorf("openapi: component %s has duplicate JSON field name %q", schema.Name, field.JSONName)
		}
		jsonNames[field.JSONName] = struct{}{}
		if err := validateTypeRef(&field.Type, components, location+" type", make(map[*TypeRef]bool)); err != nil {
			return err
		}
	}
	return nil
}

func validateTypeRef(ref *TypeRef, components map[string]Schema, location string, visiting map[*TypeRef]bool) error {
	if ref == nil {
		return fmt.Errorf("openapi: %s is missing", location)
	}
	if visiting[ref] {
		return fmt.Errorf("openapi: %s contains a recursive inline type", location)
	}
	visiting[ref] = true
	defer delete(visiting, ref)

	if ref.Name != "" {
		if ref.Kind != "" || ref.Items != nil {
			return fmt.Errorf("openapi: %s component reference %q must not declare a kind or items", location, ref.Name)
		}
		if _, exists := components[ref.Name]; !exists {
			return fmt.Errorf("openapi: %s references missing component %q", location, ref.Name)
		}
		return nil
	}

	switch ref.Kind {
	case String, Integer, Number, Boolean:
		if ref.Items != nil {
			return fmt.Errorf("openapi: %s scalar type %q must not declare items", location, ref.Kind)
		}
		return nil
	case Array:
		if ref.Items == nil {
			return fmt.Errorf("openapi: %s array type has no items", location)
		}
		return validateTypeRef(ref.Items, components, location+" items", visiting)
	case Object:
		return fmt.Errorf("%w: %s inline object must use a component reference", ErrUnsupported, location)
	default:
		return fmt.Errorf("%w: %s has type kind %q", ErrUnsupported, location, ref.Kind)
	}
}

func validateOperation(operation Operation, components map[string]Schema) error {
	location := operation.Method + " " + operation.Path
	if err := validateCanonicalRoute(operation.Path); err != nil {
		return fmt.Errorf("openapi: operation %q: %w", operation.ID, err)
	}
	templates, err := pathTemplateNames(operation.Path)
	if err != nil {
		return err
	}

	switch operation.Method {
	case "GET":
		if operation.Request != nil || operation.RequestRequired {
			return fmt.Errorf("%w: GET operation %q has a request body", ErrUnsupported, operation.ID)
		}
	case "POST", "PUT", "PATCH":
		if operation.Request == nil {
			return fmt.Errorf("%w: operation %q requires a request body", ErrUnsupported, operation.ID)
		}
	case "DELETE":
	default:
		return fmt.Errorf("%w: HTTP adapter for %s %s", ErrUnsupported, operation.Method, operation.Path)
	}

	if operation.Request == nil {
		if operation.RequestRequired {
			return fmt.Errorf("openapi: operation %q requires a missing request type", operation.ID)
		}
	} else {
		if !operation.RequestRequired {
			return fmt.Errorf("%w: operation %q has an optional request body", ErrUnsupported, operation.ID)
		}
		if operation.Request.Name == "" {
			return fmt.Errorf("%w: operation %q request must reference a component", ErrUnsupported, operation.ID)
		}
		if err := validateTypeRef(operation.Request, components, location+" request", make(map[*TypeRef]bool)); err != nil {
			return err
		}
	}

	if operation.Response == nil {
		if operation.Status != 204 {
			return fmt.Errorf("openapi: operation %q without a response body must use status 204, got %d", operation.ID, operation.Status)
		}
	} else {
		if operation.Response.Name == "" {
			return fmt.Errorf("%w: operation %q response must reference a component", ErrUnsupported, operation.ID)
		}
		if operation.Status != 200 && operation.Status != 201 && operation.Status != 202 {
			return fmt.Errorf("%w: operation %q with a response body uses status %d", ErrUnsupported, operation.ID, operation.Status)
		}
		if err := validateTypeRef(operation.Response, components, location+" response", make(map[*TypeRef]bool)); err != nil {
			return err
		}
	}

	return validateParameters(operation, templates, components)
}

func validateParameters(operation Operation, templates map[string]bool, components map[string]Schema) error {
	parameterKeys := make(map[string]struct{}, len(operation.Parameters))
	goNames := make(map[string]struct{}, len(operation.Parameters))
	headerNames := make(map[string]struct{})
	pathParameters := make(map[string]bool)
	for index := range operation.Parameters {
		parameter := &operation.Parameters[index]
		location := fmt.Sprintf("operation %q parameter %d", operation.ID, index)
		if !validJSONTagName(parameter.Name) {
			return fmt.Errorf("%w: %s name %q cannot be represented exactly by encoding/json", ErrUnsupported, location, parameter.Name)
		}
		if err := validateGeneratedIdentifier(parameter.GoName); err != nil {
			return fmt.Errorf("openapi: %s Go name %q: %w", location, parameter.GoName, err)
		}
		if _, exists := goNames[parameter.GoName]; exists {
			return fmt.Errorf("openapi: operation %q has duplicate parameter Go field %q", operation.ID, parameter.GoName)
		}
		goNames[parameter.GoName] = struct{}{}

		switch parameter.In {
		case "path", "query", "header":
		default:
			return fmt.Errorf("%w: %s has location %q", ErrUnsupported, location, parameter.In)
		}
		key := parameter.In + ":" + parameter.Name
		if _, exists := parameterKeys[key]; exists {
			return fmt.Errorf("openapi: operation %q has duplicate parameter %s", operation.ID, key)
		}
		parameterKeys[key] = struct{}{}

		if err := validateTypeRef(&parameter.Type, components, location+" type", make(map[*TypeRef]bool)); err != nil {
			return err
		}
		if parameter.Type.Name != "" || !scalar(parameter.Type.Kind) {
			return fmt.Errorf("%w: %s must have an inline scalar type", ErrUnsupported, location)
		}

		switch parameter.In {
		case "path":
			if !parameter.Required {
				return fmt.Errorf("openapi: operation %q path parameter %q must be required", operation.ID, parameter.Name)
			}
			if !templates[parameter.Name] {
				return fmt.Errorf("openapi: operation %q path parameter %q has no matching template", operation.ID, parameter.Name)
			}
			pathParameters[parameter.Name] = true
		case "header":
			if !validHTTPToken(parameter.Name) {
				return fmt.Errorf("openapi: operation %q header parameter name %q is not a valid HTTP field name", operation.ID, parameter.Name)
			}
			folded := strings.ToLower(parameter.Name)
			if _, reserved := reservedHeaderParameters[folded]; reserved {
				return fmt.Errorf("%w: operation %q header parameter %q is controlled by HTTP semantics", ErrUnsupported, operation.ID, parameter.Name)
			}
			if _, exists := headerNames[folded]; exists {
				return fmt.Errorf("openapi: operation %q has duplicate case-insensitive header parameter %q", operation.ID, parameter.Name)
			}
			headerNames[folded] = struct{}{}
		}
	}
	for name := range templates {
		if !pathParameters[name] {
			return fmt.Errorf("openapi: operation %q template parameter %q has no required path parameter", operation.ID, name)
		}
	}
	return nil
}

func validateCanonicalRoute(route string) error {
	if !utf8.ValidString(route) {
		return fmt.Errorf("route %q must be valid UTF-8", route)
	}
	if hasUnsafeRouteRune(route) {
		return fmt.Errorf("route %q must not contain control or format characters", route)
	}
	if strings.ContainsAny(route, "?#") {
		return fmt.Errorf("route %q must not contain a raw query or fragment delimiter", route)
	}
	if strings.Contains(route, "//") {
		return fmt.Errorf("route %q must not contain repeated slashes", route)
	}
	for _, segment := range strings.Split(route, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return fmt.Errorf("route %q contains invalid path escaping: %w", route, err)
		}
		if !utf8.ValidString(decoded) {
			return fmt.Errorf("route %q contains escaping that is not valid UTF-8", route)
		}
		if hasUnsafeRouteRune(decoded) {
			return fmt.Errorf("route %q must not contain raw or encoded control or format characters", route)
		}
		if decoded == "." || decoded == ".." {
			return fmt.Errorf("route %q must not contain raw or encoded dot segments", route)
		}
	}
	return nil
}

func hasUnsafeRouteRune(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			return true
		}
	}
	return false
}

func validateGeneratedIdentifier(name string) error {
	if !token.IsIdentifier(name) {
		return fmt.Errorf("must be a valid Go identifier")
	}
	first, _ := utf8.DecodeRuneInString(name)
	if !unicode.IsUpper(first) {
		return fmt.Errorf("must be an exported Go identifier")
	}
	switch name {
	case "API", "Context", "Register":
		return fmt.Errorf("conflicts with generated name")
	default:
		return nil
	}
}

func validHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

// validateOptions returns the canonical relative output directory after
// validating both filesystem and generated-import-path requirements.
func validateOptions(options Options) (string, error) {
	if options.Module == "" {
		return "", fmt.Errorf("openapi: output module is required")
	}
	if err := module.CheckPath(options.Module); err != nil {
		return "", fmt.Errorf("openapi: output module %q is invalid: %w", options.Module, err)
	}
	if hasVendorPathSegment(options.Module) {
		return "", fmt.Errorf("openapi: output module %q must not contain a vendor path segment", options.Module)
	}
	output := options.Output
	if output == "" {
		output = defaultOutput
	}
	normalized, err := codegen.SafeRelativePath(output)
	if err != nil {
		return "", fmt.Errorf("openapi: output directory %q: %w", output, err)
	}
	if hasVendorPathSegment(normalized) {
		return "", fmt.Errorf("openapi: output directory %q must not contain a vendor path segment", output)
	}
	if err := module.CheckImportPath(options.Module + "/" + normalized); err != nil {
		return "", fmt.Errorf("openapi: output import path %q is invalid: %w", options.Module+"/"+normalized, err)
	}
	return normalized, nil
}

func hasVendorPathSegment(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if segment == "vendor" {
			return true
		}
	}
	return false
}
