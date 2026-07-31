package main

import (
	"errors"
	"fmt"
	"strconv"
	"unicode"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

func snapshotProtoImportClosure(
	value manifest.Manifest,
	capture func(string, bool) (addFileState, error),
) error {
	queue := make([]string, 0)
	for _, source := range value.Sources {
		if source.Kind == manifest.KindProto && source.Plugin == nil {
			queue = append(queue, source.Path)
		}
	}
	visited := make(map[string]struct{}, len(queue))
	for len(queue) != 0 {
		name := queue[0]
		queue = queue[1:]
		portable, err := codegen.SafeRelativePath(name)
		if err != nil {
			return fmt.Errorf("unsafe proto dependency %q: %w", name, err)
		}
		if _, exists := visited[portable]; exists {
			continue
		}
		visited[portable] = struct{}{}
		state, err := capture(portable, true)
		if err != nil {
			return fmt.Errorf("capture proto dependency %q: %w", portable, err)
		}
		imports, err := parseProtoImports(state.content)
		if err != nil {
			return fmt.Errorf("parse imports in %q: %w", portable, err)
		}
		queue = append(queue, imports...)
	}
	return nil
}

type protoTokenKind uint8

const (
	protoTokenEOF protoTokenKind = iota
	protoTokenIdentifier
	protoTokenString
	protoTokenSemicolon
	protoTokenOther
)

type protoToken struct {
	kind  protoTokenKind
	value string
}

type protoScanner struct {
	input  []byte
	offset int
}

func parseProtoImports(input []byte) ([]string, error) {
	scanner := protoScanner{input: input}
	var imports []string
	for {
		token, err := scanner.next()
		if err != nil {
			return nil, err
		}
		if token.kind == protoTokenEOF {
			return imports, nil
		}
		if token.kind != protoTokenIdentifier || token.value != "import" {
			continue
		}
		token, err = scanner.next()
		if err != nil {
			return nil, err
		}
		if token.kind == protoTokenIdentifier && (token.value == "public" || token.value == "weak") {
			token, err = scanner.next()
			if err != nil {
				return nil, err
			}
		}
		if token.kind != protoTokenString {
			return nil, errors.New("import declaration must contain a quoted path")
		}
		portable, err := codegen.SafeRelativePath(token.value)
		if err != nil {
			return nil, fmt.Errorf("unsafe import path %q: %w", token.value, err)
		}
		terminator, err := scanner.next()
		if err != nil {
			return nil, err
		}
		if terminator.kind != protoTokenSemicolon {
			return nil, fmt.Errorf("import %q is missing its terminating semicolon", portable)
		}
		imports = append(imports, portable)
	}
}

func (s *protoScanner) next() (protoToken, error) {
	for s.offset < len(s.input) {
		current := s.input[s.offset]
		if unicode.IsSpace(rune(current)) {
			s.offset++
			continue
		}
		if current == '/' && s.offset+1 < len(s.input) {
			switch s.input[s.offset+1] {
			case '/':
				s.offset += 2
				for s.offset < len(s.input) && s.input[s.offset] != '\n' {
					s.offset++
				}
				continue
			case '*':
				s.offset += 2
				for s.offset+1 < len(s.input) && !(s.input[s.offset] == '*' && s.input[s.offset+1] == '/') {
					s.offset++
				}
				if s.offset+1 >= len(s.input) {
					return protoToken{}, errors.New("unterminated block comment")
				}
				s.offset += 2
				continue
			}
		}
		if isProtoIdentifierStart(current) {
			start := s.offset
			s.offset++
			for s.offset < len(s.input) && isProtoIdentifierPart(s.input[s.offset]) {
				s.offset++
			}
			return protoToken{kind: protoTokenIdentifier, value: string(s.input[start:s.offset])}, nil
		}
		if current == '"' || current == '\'' {
			value, err := s.scanString(current)
			if err != nil {
				return protoToken{}, err
			}
			return protoToken{kind: protoTokenString, value: value}, nil
		}
		s.offset++
		if current == ';' {
			return protoToken{kind: protoTokenSemicolon, value: ";"}, nil
		}
		return protoToken{kind: protoTokenOther, value: string(current)}, nil
	}
	return protoToken{kind: protoTokenEOF}, nil
}

func (s *protoScanner) scanString(quote byte) (string, error) {
	start := s.offset
	s.offset++
	for s.offset < len(s.input) {
		current := s.input[s.offset]
		if current == '\n' || current == '\r' {
			return "", errors.New("unterminated import string")
		}
		if current == '\\' {
			s.offset += 2
			if s.offset > len(s.input) {
				return "", errors.New("unterminated import string escape")
			}
			continue
		}
		s.offset++
		if current == quote {
			literal := string(s.input[start:s.offset])
			if quote == '"' {
				value, err := strconv.Unquote(literal)
				if err != nil {
					return "", fmt.Errorf("decode import string: %w", err)
				}
				return value, nil
			}
			return unquoteSingleProtoString(literal[1 : len(literal)-1])
		}
	}
	return "", errors.New("unterminated import string")
}

func unquoteSingleProtoString(value string) (string, error) {
	var result []rune
	for value != "" {
		character, _, tail, err := strconv.UnquoteChar(value, '\'')
		if err != nil {
			return "", fmt.Errorf("decode import string: %w", err)
		}
		result = append(result, character)
		value = tail
	}
	return string(result), nil
}

func isProtoIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isProtoIdentifierPart(value byte) bool {
	return isProtoIdentifierStart(value) || value >= '0' && value <= '9'
}
