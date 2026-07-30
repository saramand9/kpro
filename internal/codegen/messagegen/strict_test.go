package messagegen

import (
	"errors"
	"strings"
	"testing"
)

func TestParseRequiresFieldsKeyEvenForEmptyPayload(t *testing.T) {
	spec := strings.Replace(
		validMessageSpec,
		"    request:\n      fields: []\n",
		"    request: {}\n",
		1,
	)
	_, err := Parse([]byte(spec))
	if err == nil ||
		!errors.Is(err, ErrInvalidSpec) ||
		!strings.Contains(err.Error(), `required field "messages[1].request.fields" is missing`) {
		t.Fatalf("Parse() error = %v, want explicit fields diagnostic", err)
	}
}

func TestParseRejectsDuplicateYAMLKeys(t *testing.T) {
	spec := strings.Replace(
		validMessageSpec,
		"service: orders",
		"service: orders\nservice: other",
		1,
	)
	_, err := Parse([]byte(spec))
	if err == nil ||
		!errors.Is(err, ErrInvalidSpec) ||
		!strings.Contains(err.Error(), "mapping key \"service\" already defined") {
		t.Fatalf("Parse() error = %v, want duplicate-key diagnostic", err)
	}
}
