package messagegen

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const validMessageSpec = `apiVersion: kpro.dev/message/v1alpha1
service: orders
encoding: json
messages:
  - id: 100
    name: createOrder
    dispatch: sync
    request:
      fields:
        - name: orderId
          type: string
          required: true
        - name: quantity
          type: int32
          required: false
        - name: fields
          type: string
          required: false
    response:
      fields:
        - name: accepted
          type: bool
          required: true
        - name: receipt
          type: bytes
          required: false
  - id: 101
    name: order-notify
    dispatch: async
    request:
      fields: []
`

func TestParseAcceptsStrictYAMLAndJSON(t *testing.T) {
	yamlDocument, err := Parse([]byte(validMessageSpec))
	if err != nil {
		t.Fatalf("Parse(YAML) error = %v", err)
	}
	jsonDocument, err := Parse([]byte(`{
		"apiVersion": "kpro.dev/message/v1alpha1",
		"service": "orders",
		"encoding": "json",
		"messages": [
			{
				"id": 100,
				"name": "createOrder",
				"dispatch": "sync",
				"request": {
					"fields": [
						{"name": "orderId", "type": "string", "required": true},
						{"name": "quantity", "type": "int32", "required": false},
						{"name": "fields", "type": "string", "required": false}
					]
				},
				"response": {
					"fields": [
						{"name": "accepted", "type": "bool", "required": true},
						{"name": "receipt", "type": "bytes", "required": false}
					]
				}
			},
			{
				"id": 101,
				"name": "order-notify",
				"dispatch": "async",
				"request": {"fields": []}
			}
		]
	}`))
	if err != nil {
		t.Fatalf("Parse(JSON) error = %v", err)
	}
	if !reflect.DeepEqual(yamlDocument, jsonDocument) {
		t.Fatalf("YAML and JSON lower differently:\nYAML: %#v\nJSON: %#v", yamlDocument, jsonDocument)
	}
	if yamlDocument.Messages[0].Response == nil ||
		yamlDocument.Messages[1].Response != nil {
		t.Fatalf("response presence was not retained: %#v", yamlDocument.Messages)
	}
}

func TestParseRejectsMissingAndUnknownInput(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		contain string
	}{
		{
			name: "unknown root field",
			spec: strings.Replace(
				validMessageSpec,
				"service: orders",
				"service: orders\nunknown: true",
				1,
			),
			contain: "field unknown not found",
		},
		{
			name:    "missing dispatch",
			spec:    strings.Replace(validMessageSpec, "    dispatch: sync\n", "", 1),
			contain: `required field "messages[0].dispatch" is missing`,
		},
		{
			name:    "missing explicit required flag",
			spec:    strings.Replace(validMessageSpec, "          required: true\n", "", 1),
			contain: `required field "messages[0].request.fields[0].required" is missing`,
		},
		{
			name:    "missing explicit fields",
			spec:    strings.Replace(validMessageSpec, "      fields: []\n", "", 1),
			contain: "explicit null values",
		},
		{
			name:    "multiple documents",
			spec:    validMessageSpec + "---\n{}\n",
			contain: "multiple YAML/JSON documents",
		},
		{
			name: "explicit null",
			spec: strings.Replace(
				validMessageSpec,
				"    response:\n",
				"    response: null\n    ignored_response:\n",
				1,
			),
			contain: "explicit null values",
		},
		{
			name: "alias",
			spec: strings.Replace(
				validMessageSpec,
				"service: orders",
				"service: &service orders",
				1,
			),
			contain: "YAML anchors and aliases",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.spec))
			if err == nil ||
				!errors.Is(err, ErrInvalidSpec) && !errors.Is(err, ErrUnsupported) ||
				!strings.Contains(err.Error(), test.contain) {
				t.Fatalf("Parse() error = %v, want %q", err, test.contain)
			}
		})
	}
}

func TestDocumentValidationRejectsReservedDuplicateAndCollidingNames(t *testing.T) {
	valid, err := Parse([]byte(validMessageSpec))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		mutate  func(*Document)
		contain string
		kind    error
	}{
		{
			name: "version",
			mutate: func(document *Document) {
				document.APIVersion = "kpro.dev/message/v2"
			},
			contain: "unsupported apiVersion",
			kind:    ErrUnsupported,
		},
		{
			name: "encoding",
			mutate: func(document *Document) {
				document.Encoding = "protobuf"
			},
			contain: "only \"json\" is accepted",
			kind:    ErrUnsupported,
		},
		{
			name: "reserved zero",
			mutate: func(document *Document) {
				document.Messages[0].ID = 0
			},
			contain: "id 0 is reserved",
			kind:    ErrInvalidSpec,
		},
		{
			name: "reserved heartbeat",
			mutate: func(document *Document) {
				document.Messages[0].ID = 1
			},
			contain: "id 1 is reserved",
			kind:    ErrInvalidSpec,
		},
		{
			name: "reserved error",
			mutate: func(document *Document) {
				document.Messages[0].ID = ^uint32(0)
			},
			contain: "is reserved",
			kind:    ErrInvalidSpec,
		},
		{
			name: "duplicate id",
			mutate: func(document *Document) {
				document.Messages[1].ID = document.Messages[0].ID
			},
			contain: "duplicates message",
			kind:    ErrInvalidSpec,
		},
		{
			name: "message Go collision",
			mutate: func(document *Document) {
				document.Messages[1].Name = "create_order"
				document.Messages[0].Name = "create-order"
			},
			contain: `both become Go identifier "CreateOrder"`,
			kind:    ErrInvalidSpec,
		},
		{
			name: "field Go collision",
			mutate: func(document *Document) {
				document.Messages[0].Request.Fields = append(
					document.Messages[0].Request.Fields,
					Field{Name: "order-id", Type: TypeString, Required: false},
				)
				document.Messages[0].Request.Fields[0].Name = "order_id"
			},
			contain: `both become Go identifier "OrderId"`,
			kind:    ErrInvalidSpec,
		},
		{
			name: "unsupported field",
			mutate: func(document *Document) {
				document.Messages[0].Request.Fields[0].Type = "timestamp"
			},
			contain: `type "timestamp"`,
			kind:    ErrUnsupported,
		},
		{
			name: "unsupported dispatch",
			mutate: func(document *Document) {
				document.Messages[0].Dispatch = "parallel"
			},
			contain: `dispatch "parallel"`,
			kind:    ErrUnsupported,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := cloneDocument(valid)
			test.mutate(&document)
			err := document.Validate()
			if err == nil ||
				!errors.Is(err, test.kind) ||
				!strings.Contains(err.Error(), test.contain) {
				t.Fatalf("Validate() error = %v, want %v containing %q", err, test.kind, test.contain)
			}
		})
	}
}

func TestDocumentValidationAcceptsEverySupportedScalar(t *testing.T) {
	types := []FieldType{
		TypeString,
		TypeBool,
		TypeInt32,
		TypeInt64,
		TypeUint32,
		TypeUint64,
		TypeFloat32,
		TypeFloat64,
		TypeBytes,
	}
	fields := make([]Field, 0, len(types))
	for index, fieldType := range types {
		fields = append(fields, Field{
			Name:     "field" + string(rune('A'+index)),
			Type:     fieldType,
			Required: index%2 == 0,
		})
	}
	document := Document{
		APIVersion: APIVersionV1Alpha1,
		Service:    "scalar-service",
		Encoding:   EncodingJSON,
		Messages: []Message{{
			ID:       100,
			Name:     "scalar-message",
			Dispatch: DispatchSync,
			Request:  Payload{Fields: fields},
		}},
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func cloneDocument(source Document) Document {
	cloned := source
	cloned.Messages = append([]Message(nil), source.Messages...)
	for index := range cloned.Messages {
		cloned.Messages[index].Request.Fields = append(
			[]Field(nil),
			source.Messages[index].Request.Fields...,
		)
		if source.Messages[index].Response != nil {
			response := Payload{
				Fields: append([]Field(nil), source.Messages[index].Response.Fields...),
			}
			cloned.Messages[index].Response = &response
		}
	}
	return cloned
}
