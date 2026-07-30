// Package messagegen parses a small, versioned KPro message contract and
// renders bindings for message transports that dispatch through core.Router.
//
// The package deliberately does not generate HTTP or gRPC bindings. TCP, KCP,
// QUIC, UDP, and WebSocket can share the generated adapter because each of
// those transports exposes the same core.Handler contract.
package messagegen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/saramand9/kpro/core"
	"gopkg.in/yaml.v3"
)

const (
	// APIVersionV1Alpha1 is the first KPro message-spec contract.
	APIVersionV1Alpha1 = "kpro.dev/message/v1alpha1"
	// EncodingJSON is the only payload encoding supported by v1alpha1.
	EncodingJSON Encoding = "json"

	// DispatchSync serializes a message route per session.
	DispatchSync DispatchMode = "sync"
	// DispatchAsync sends a message route through the Router async workers.
	DispatchAsync DispatchMode = "async"

	TypeString  FieldType = "string"
	TypeBool    FieldType = "bool"
	TypeInt32   FieldType = "int32"
	TypeInt64   FieldType = "int64"
	TypeUint32  FieldType = "uint32"
	TypeUint64  FieldType = "uint64"
	TypeFloat32 FieldType = "float32"
	TypeFloat64 FieldType = "float64"
	TypeBytes   FieldType = "bytes"
)

var (
	// ErrInvalidSpec identifies malformed or internally inconsistent contracts.
	ErrInvalidSpec = errors.New("message codegen: invalid spec")
	// ErrUnsupported identifies well-formed features outside the deliberately
	// small v1alpha1 contract.
	ErrUnsupported = errors.New("message codegen: unsupported feature")
)

// Encoding identifies the payload representation used in core.Message.Data.
type Encoding string

// DispatchMode maps a generated route to a checked Router registration method.
type DispatchMode string

// FieldType is one scalar JSON field supported by v1alpha1.
type FieldType string

// Document is the parser-independent message contract used by the renderer.
type Document struct {
	APIVersion string
	Service    string
	Encoding   Encoding
	Messages   []Message
}

// Message describes one routed request. A response, when present, is sent with
// the same message ID and sequence number as the request.
type Message struct {
	ID       uint32
	Name     string
	Dispatch DispatchMode
	Request  Payload
	Response *Payload
}

// Payload contains a flat set of scalar JSON fields.
type Payload struct {
	Fields []Field
}

// Field describes one JSON property. Optional fields are generated as pointers
// so missing/null values remain distinguishable from scalar zero values.
type Field struct {
	Name     string
	Type     FieldType
	Required bool
}

type rawDocument struct {
	APIVersion *string       `yaml:"apiVersion"`
	Service    *string       `yaml:"service"`
	Encoding   *Encoding     `yaml:"encoding"`
	Messages   *[]rawMessage `yaml:"messages"`
}

type rawMessage struct {
	ID       *uint32       `yaml:"id"`
	Name     *string       `yaml:"name"`
	Dispatch *DispatchMode `yaml:"dispatch"`
	Request  *rawPayload   `yaml:"request"`
	Response *rawPayload   `yaml:"response,omitempty"`
}

type rawPayload struct {
	Fields *[]rawField `yaml:"fields"`
}

type rawField struct {
	Name     *string    `yaml:"name"`
	Type     *FieldType `yaml:"type"`
	Required *bool      `yaml:"required"`
}

// Parse strictly decodes one YAML or JSON message-spec document.
func Parse(data []byte) (Document, error) {
	if err := inspectSource(data); err != nil {
		return Document{}, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var raw rawDocument
	if err := decoder.Decode(&raw); err != nil {
		return Document{}, fmt.Errorf("%w: decode document: %v", ErrInvalidSpec, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Document{}, fmt.Errorf("%w: multiple YAML/JSON documents are not supported", ErrInvalidSpec)
		}
		return Document{}, fmt.Errorf("%w: decode trailing document: %v", ErrInvalidSpec, err)
	}

	document, err := lowerRaw(raw)
	if err != nil {
		return Document{}, err
	}
	if err := document.Validate(); err != nil {
		return Document{}, err
	}
	return document, nil
}

// ParseReader reads and parses one message-spec document.
func ParseReader(reader io.Reader) (Document, error) {
	if reader == nil {
		return Document{}, fmt.Errorf("%w: reader is nil", ErrInvalidSpec)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return Document{}, fmt.Errorf("%w: read document: %v", ErrInvalidSpec, err)
	}
	return Parse(data)
}

// Load reads and parses one message-spec file.
func Load(filename string) (Document, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Document{}, fmt.Errorf("message codegen: read %q: %w", filename, err)
	}
	return Parse(data)
}

// Validate checks the full public IR, including programmatically constructed
// documents that did not pass through Parse.
func (document Document) Validate() error {
	if document.APIVersion != APIVersionV1Alpha1 {
		return fmt.Errorf(
			"%w: unsupported apiVersion %q (want %q)",
			ErrUnsupported,
			document.APIVersion,
			APIVersionV1Alpha1,
		)
	}
	if _, err := exportedIdentifier(document.Service); err != nil {
		return fmt.Errorf("%w: service %q: %v", ErrInvalidSpec, document.Service, err)
	}
	if document.Encoding != EncodingJSON {
		return fmt.Errorf(
			"%w: encoding %q (only %q is accepted)",
			ErrUnsupported,
			document.Encoding,
			EncodingJSON,
		)
	}
	if len(document.Messages) == 0 {
		return fmt.Errorf("%w: at least one message is required", ErrInvalidSpec)
	}

	ids := make(map[uint32]string, len(document.Messages))
	goNames := make(map[string]string, len(document.Messages))
	for index, message := range document.Messages {
		location := fmt.Sprintf("messages[%d]", index)
		if reservedMessageID(message.ID) {
			return fmt.Errorf("%w: %s id %d is reserved", ErrInvalidSpec, location, message.ID)
		}
		if previous, exists := ids[message.ID]; exists {
			return fmt.Errorf(
				"%w: %s id %d duplicates message %q",
				ErrInvalidSpec,
				location,
				message.ID,
				previous,
			)
		}
		ids[message.ID] = message.Name

		goName, err := exportedIdentifier(message.Name)
		if err != nil {
			return fmt.Errorf("%w: %s name %q: %v", ErrInvalidSpec, location, message.Name, err)
		}
		if previous, exists := goNames[goName]; exists {
			return fmt.Errorf(
				"%w: message names %q and %q both become Go identifier %q",
				ErrInvalidSpec,
				previous,
				message.Name,
				goName,
			)
		}
		goNames[goName] = message.Name

		switch message.Dispatch {
		case DispatchSync, DispatchAsync:
		default:
			return fmt.Errorf(
				"%w: %s dispatch %q (want %q or %q)",
				ErrUnsupported,
				location,
				message.Dispatch,
				DispatchSync,
				DispatchAsync,
			)
		}
		if err := validatePayload(location+".request", message.Request); err != nil {
			return err
		}
		if message.Response != nil {
			if err := validatePayload(location+".response", *message.Response); err != nil {
				return err
			}
		}
	}
	return nil
}

func inspectSource(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return fmt.Errorf("%w: document is empty", ErrInvalidSpec)
		}
		return fmt.Errorf("%w: decode syntax: %v", ErrInvalidSpec, err)
	}
	if len(document.Content) == 0 {
		return fmt.Errorf("%w: document is empty", ErrInvalidSpec)
	}
	if err := inspectNode(&document); err != nil {
		return err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: multiple YAML/JSON documents are not supported", ErrInvalidSpec)
		}
		return fmt.Errorf("%w: decode trailing document: %v", ErrInvalidSpec, err)
	}
	return nil
}

func inspectNode(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode {
		return fmt.Errorf("%w: YAML anchors and aliases", ErrUnsupported)
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return fmt.Errorf("%w: explicit null values are not accepted", ErrInvalidSpec)
	}
	if strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!") {
		return fmt.Errorf("%w: custom YAML tag %q", ErrUnsupported, node.Tag)
	}
	if node.Kind == yaml.MappingNode {
		for index := 0; index+1 < len(node.Content); index += 2 {
			if node.Content[index].Value == "<<" {
				return fmt.Errorf("%w: YAML merge keys", ErrUnsupported)
			}
		}
	}
	for _, child := range node.Content {
		if err := inspectNode(child); err != nil {
			return err
		}
	}
	return nil
}

func lowerRaw(raw rawDocument) (Document, error) {
	if raw.APIVersion == nil {
		return Document{}, missing("apiVersion")
	}
	if raw.Service == nil {
		return Document{}, missing("service")
	}
	if raw.Encoding == nil {
		return Document{}, missing("encoding")
	}
	if raw.Messages == nil {
		return Document{}, missing("messages")
	}

	document := Document{
		APIVersion: *raw.APIVersion,
		Service:    *raw.Service,
		Encoding:   *raw.Encoding,
		Messages:   make([]Message, 0, len(*raw.Messages)),
	}
	for messageIndex, rawMessage := range *raw.Messages {
		location := fmt.Sprintf("messages[%d]", messageIndex)
		if rawMessage.ID == nil {
			return Document{}, missing(location + ".id")
		}
		if rawMessage.Name == nil {
			return Document{}, missing(location + ".name")
		}
		if rawMessage.Dispatch == nil {
			return Document{}, missing(location + ".dispatch")
		}
		if rawMessage.Request == nil {
			return Document{}, missing(location + ".request")
		}
		request, err := lowerPayload(location+".request", *rawMessage.Request)
		if err != nil {
			return Document{}, err
		}
		message := Message{
			ID:       *rawMessage.ID,
			Name:     *rawMessage.Name,
			Dispatch: *rawMessage.Dispatch,
			Request:  request,
		}
		if rawMessage.Response != nil {
			response, err := lowerPayload(location+".response", *rawMessage.Response)
			if err != nil {
				return Document{}, err
			}
			message.Response = &response
		}
		document.Messages = append(document.Messages, message)
	}
	return document, nil
}

func lowerPayload(location string, raw rawPayload) (Payload, error) {
	if raw.Fields == nil {
		return Payload{}, missing(location + ".fields")
	}
	payload := Payload{Fields: make([]Field, 0, len(*raw.Fields))}
	for fieldIndex, rawField := range *raw.Fields {
		fieldLocation := fmt.Sprintf("%s.fields[%d]", location, fieldIndex)
		if rawField.Name == nil {
			return Payload{}, missing(fieldLocation + ".name")
		}
		if rawField.Type == nil {
			return Payload{}, missing(fieldLocation + ".type")
		}
		if rawField.Required == nil {
			return Payload{}, missing(fieldLocation + ".required")
		}
		payload.Fields = append(payload.Fields, Field{
			Name:     *rawField.Name,
			Type:     *rawField.Type,
			Required: *rawField.Required,
		})
	}
	return payload, nil
}

func validatePayload(location string, payload Payload) error {
	rawNames := make(map[string]struct{}, len(payload.Fields))
	goNames := make(map[string]string, len(payload.Fields))
	for index, field := range payload.Fields {
		fieldLocation := fmt.Sprintf("%s.fields[%d]", location, index)
		goName, err := exportedIdentifier(field.Name)
		if err != nil {
			return fmt.Errorf("%w: %s name %q: %v", ErrInvalidSpec, fieldLocation, field.Name, err)
		}
		if _, exists := rawNames[field.Name]; exists {
			return fmt.Errorf("%w: %s duplicates JSON field %q", ErrInvalidSpec, fieldLocation, field.Name)
		}
		rawNames[field.Name] = struct{}{}
		if previous, exists := goNames[goName]; exists {
			return fmt.Errorf(
				"%w: %s fields %q and %q both become Go identifier %q",
				ErrInvalidSpec,
				location,
				previous,
				field.Name,
				goName,
			)
		}
		goNames[goName] = field.Name
		if !supportedFieldType(field.Type) {
			return fmt.Errorf("%w: %s type %q", ErrUnsupported, fieldLocation, field.Type)
		}
	}
	return nil
}

func supportedFieldType(fieldType FieldType) bool {
	switch fieldType {
	case TypeString,
		TypeBool,
		TypeInt32,
		TypeInt64,
		TypeUint32,
		TypeUint64,
		TypeFloat32,
		TypeFloat64,
		TypeBytes:
		return true
	default:
		return false
	}
}

func reservedMessageID(id uint32) bool {
	return id == 0 || id == core.MsgHeartbeat || id == core.MsgPong || id == core.MsgError
}

func missing(name string) error {
	return fmt.Errorf("%w: required field %q is missing", ErrInvalidSpec, name)
}

func exportedIdentifier(value string) (string, error) {
	if err := validateSchemaName(value); err != nil {
		return "", err
	}
	var result strings.Builder
	result.Grow(len(value))
	upperNext := true
	for _, character := range value {
		switch character {
		case '-', '_':
			upperNext = true
			continue
		}
		if upperNext {
			result.WriteRune(unicode.ToUpper(character))
			upperNext = false
			continue
		}
		result.WriteRune(character)
	}
	return result.String(), nil
}

func validateSchemaName(value string) error {
	if len(value) == 0 || len(value) > 64 {
		return errors.New("must contain between 1 and 64 characters")
	}
	if !asciiLetter(value[0]) {
		return errors.New("must begin with an ASCII letter")
	}
	separator := false
	for index := 1; index < len(value); index++ {
		character := value[index]
		switch {
		case asciiLetter(character) || asciiDigit(character):
			separator = false
		case character == '-' || character == '_':
			if separator || index == len(value)-1 {
				return errors.New("separators must occur singly between letters or digits")
			}
			separator = true
		default:
			return errors.New("may contain only ASCII letters, digits, '-' and '_'")
		}
	}
	return nil
}

func asciiLetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func asciiDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func orderedMessages(messages []Message) []Message {
	ordered := append([]Message(nil), messages...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].ID == ordered[right].ID {
			return ordered[left].Name < ordered[right].Name
		}
		return ordered[left].ID < ordered[right].ID
	})
	return ordered
}
