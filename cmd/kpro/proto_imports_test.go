package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen/protogen"
)

func TestParseProtoImportsIgnoresCommentsAndSupportsModifiers(t *testing.T) {
	input := []byte(`syntax = "proto3";
// import "ignored/line.proto";
/* import "ignored/block.proto"; */
import "api/first.proto";
import public "api/second.proto";
import weak 'api/third.proto';
option java_package = "text containing import \"ignored/string.proto\";";
`)
	got, err := protogen.ParseImports(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"api/first.proto", "api/second.proto", "api/third.proto"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imports = %v, want %v", got, want)
	}
}

func TestParseProtoImportsRejectsUnsafeDependency(t *testing.T) {
	_, err := protogen.ParseImports([]byte(`import "../outside.proto";`))
	if err == nil || !strings.Contains(err.Error(), "unsafe import path") {
		t.Fatalf("unsafe proto import error = %v", err)
	}
}
