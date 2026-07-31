package protogen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/saramand9/kpro/internal/codegen"
)

// ParseImports returns the portable project-relative paths declared by
// protobuf import statements. It recognizes imports only at top-level
// statement boundaries, ignores comments and other string literals, accepts
// public, weak, and option modifiers, concatenates adjacent string fragments,
// and rejects unsafe paths before callers use them for file access.
func ParseImports(input []byte) ([]string, error) {
	scanner := importScanner{input: input}
	var imports []string
	statementBoundary := true
	statementEndsAtBrace := false
	braceDepth := 0
	for {
		token, err := scanner.next()
		if err != nil {
			return nil, err
		}
		if token.kind == importTokenEOF {
			if braceDepth != 0 {
				return nil, errors.New("protobuf source ends inside a brace-delimited declaration")
			}
			return imports, nil
		}
		if token.kind == importTokenRightBrace && braceDepth == 0 {
			return nil, errors.New("protobuf source contains an unmatched closing brace")
		}

		if statementBoundary && braceDepth == 0 {
			if token.kind == importTokenSemicolon {
				continue
			}
			statementBoundary = false
			statementEndsAtBrace = startsBraceTerminatedStatement(token)
			if token.kind == importTokenIdentifier && token.value == "import" {
				portable, err := parseImportDeclaration(&scanner)
				if err != nil {
					return nil, err
				}
				imports = append(imports, portable)
				statementBoundary = true
				statementEndsAtBrace = false
				continue
			}
		}

		switch token.kind {
		case importTokenLeftBrace:
			braceDepth++
		case importTokenRightBrace:
			if braceDepth == 0 {
				continue
			}
			braceDepth--
			if braceDepth == 0 && statementEndsAtBrace {
				statementBoundary = true
				statementEndsAtBrace = false
			}
		case importTokenSemicolon:
			if braceDepth == 0 {
				statementBoundary = true
				statementEndsAtBrace = false
			}
		}
	}
}

func parseImportDeclaration(scanner *importScanner) (string, error) {
	token, err := scanner.next()
	if err != nil {
		return "", err
	}
	if token.kind == importTokenIdentifier &&
		(token.value == "public" || token.value == "weak" || token.value == "option") {
		token, err = scanner.next()
		if err != nil {
			return "", err
		}
	}
	if token.kind != importTokenString {
		return "", errors.New("import declaration must contain a quoted path")
	}

	var pathValue strings.Builder
	for token.kind == importTokenString {
		pathValue.WriteString(token.value)
		token, err = scanner.next()
		if err != nil {
			return "", err
		}
	}
	portable, err := codegen.SafeRelativePath(pathValue.String())
	if err != nil {
		return "", fmt.Errorf("unsafe import path %q: %w", pathValue.String(), err)
	}
	if token.kind != importTokenSemicolon {
		return "", fmt.Errorf("import %q is missing its terminating semicolon", portable)
	}
	return portable, nil
}

func startsBraceTerminatedStatement(token importToken) bool {
	if token.kind != importTokenIdentifier {
		return false
	}
	switch token.value {
	case "message", "enum", "extend", "service", "export", "local":
		return true
	default:
		return false
	}
}

type importClosureSnapshot struct {
	sourcePath string
	files      []importSnapshotFile
}

type importSnapshotFile struct {
	path    string
	content []byte
}

type resolvedImportFile struct {
	portable  string
	candidate string
	resolved  string
	info      os.FileInfo
}

func validateImportClosure(root, sourcePath string) error {
	_, err := captureImportClosure(root, sourcePath)
	return err
}

func captureImportClosure(root, sourcePath string) (importClosureSnapshot, error) {
	if err := validateProtoSourceArgumentPath(sourcePath); err != nil {
		return importClosureSnapshot{}, err
	}
	sourcePortable, err := codegen.SafeRelativePath(sourcePath)
	if err != nil {
		return importClosureSnapshot{}, fmt.Errorf("protobuf dependency %q: unsafe path: %w", sourcePath, err)
	}

	snapshot := importClosureSnapshot{sourcePath: sourcePortable}
	queue := []string{sourcePortable}
	visited := make(map[string]string)
	for len(queue) != 0 {
		name := queue[0]
		queue = queue[1:]
		portable, err := codegen.SafeRelativePath(name)
		if err != nil {
			return importClosureSnapshot{}, fmt.Errorf("protobuf dependency %q: unsafe path: %w", name, err)
		}
		key, err := codegen.PortablePathKey(portable)
		if err != nil {
			return importClosureSnapshot{}, fmt.Errorf(
				"protobuf dependency %q: compute portable path identity: %w",
				portable,
				err,
			)
		}
		if previous, exists := visited[key]; exists {
			if previous != portable {
				return importClosureSnapshot{}, fmt.Errorf(
					"protobuf dependency %q aliases %q on supported filesystems",
					portable,
					previous,
				)
			}
			continue
		}

		resolved, err := resolveImportFile(root, name)
		if err != nil {
			return importClosureSnapshot{}, fmt.Errorf("protobuf dependency %q: %w", name, err)
		}
		visited[key] = resolved.portable

		content, err := readResolvedImportFile(root, resolved)
		if err != nil {
			return importClosureSnapshot{}, fmt.Errorf("read protobuf dependency %q: %w", portable, err)
		}
		imports, err := ParseImports(content)
		if err != nil {
			return importClosureSnapshot{}, fmt.Errorf(
				"parse imports in protobuf dependency %q: %w",
				portable,
				err,
			)
		}
		snapshot.files = append(snapshot.files, importSnapshotFile{path: resolved.portable, content: content})
		queue = append(queue, imports...)
	}
	return snapshot, nil
}

func validateProtoSourceArgumentPath(sourcePath string) error {
	if strings.HasPrefix(sourcePath, "-") || strings.HasPrefix(sourcePath, "@") {
		return fmt.Errorf("protobuf source path %q must not begin with '-' or '@'", sourcePath)
	}
	if strings.ContainsAny(sourcePath, ",=") {
		return fmt.Errorf("protobuf source path %q must not contain ',' or '='", sourcePath)
	}
	return nil
}

func resolveImportFile(root, name string) (resolvedImportFile, error) {
	portable, err := codegen.SafeRelativePath(name)
	if err != nil {
		return resolvedImportFile{}, fmt.Errorf("unsafe path: %w", err)
	}
	candidate := filepath.Join(root, filepath.FromSlash(portable))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return resolvedImportFile{}, fmt.Errorf("resolve path: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return resolvedImportFile{}, fmt.Errorf("resolve absolute path: %w", err)
	}
	if err := validateResolvedImportPath(root, resolved); err != nil {
		return resolvedImportFile{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return resolvedImportFile{}, fmt.Errorf("inspect path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return resolvedImportFile{}, errors.New("path is not a regular file")
	}
	return resolvedImportFile{
		portable:  portable,
		candidate: candidate,
		resolved:  resolved,
		info:      info,
	}, nil
}

func readResolvedImportFile(root string, resolved resolvedImportFile) ([]byte, error) {
	file, err := os.Open(resolved.resolved)
	if err != nil {
		return nil, fmt.Errorf("open validated path: %w", err)
	}
	defer file.Close()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened path: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(resolved.info, openedInfo) {
		return nil, errors.New("path changed after validation")
	}
	currentResolved, err := filepath.EvalSymlinks(resolved.candidate)
	if err != nil {
		return nil, fmt.Errorf("revalidate path: %w", err)
	}
	currentResolved, err = filepath.Abs(currentResolved)
	if err != nil {
		return nil, fmt.Errorf("resolve revalidated absolute path: %w", err)
	}
	if err := validateResolvedImportPath(root, currentResolved); err != nil {
		return nil, fmt.Errorf("revalidate path: %w", err)
	}
	currentInfo, err := os.Stat(currentResolved)
	if err != nil {
		return nil, fmt.Errorf("inspect revalidated path: %w", err)
	}
	if !os.SameFile(openedInfo, currentInfo) {
		return nil, errors.New("path changed after validation")
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read opened path: %w", err)
	}
	return content, nil
}

func validateResolvedImportPath(root, resolved string) error {
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return fmt.Errorf("locate path under project root: %w", err)
	}
	if relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("path resolves outside the project root")
	}
	return nil
}

func materializeImportSnapshot(snapshot importClosureSnapshot) (string, error) {
	root, err := os.MkdirTemp("", "kpro-protoc-input-*")
	if err != nil {
		return "", fmt.Errorf("create isolated input directory: %w", err)
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(root)
		}
	}()

	for _, input := range snapshot.files {
		filename := filepath.Join(root, filepath.FromSlash(input.path))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			return "", fmt.Errorf("create snapshot directory for %q: %w", input.path, err)
		}
		if err := os.WriteFile(filename, input.content, 0o600); err != nil {
			return "", fmt.Errorf("write snapshot dependency %q: %w", input.path, err)
		}
	}
	remove = false
	return root, nil
}

type importTokenKind uint8

const (
	importTokenEOF importTokenKind = iota
	importTokenIdentifier
	importTokenString
	importTokenSemicolon
	importTokenLeftBrace
	importTokenRightBrace
	importTokenOther
)

type importToken struct {
	kind  importTokenKind
	value string
}

type importScanner struct {
	input  []byte
	offset int
}

func (scanner *importScanner) next() (importToken, error) {
	for scanner.offset < len(scanner.input) {
		current := scanner.input[scanner.offset]
		if unicode.IsSpace(rune(current)) {
			scanner.offset++
			continue
		}
		if current == '/' && scanner.offset+1 < len(scanner.input) {
			switch scanner.input[scanner.offset+1] {
			case '/':
				scanner.offset += 2
				for scanner.offset < len(scanner.input) && scanner.input[scanner.offset] != '\n' {
					scanner.offset++
				}
				continue
			case '*':
				scanner.offset += 2
				for scanner.offset+1 < len(scanner.input) &&
					!(scanner.input[scanner.offset] == '*' && scanner.input[scanner.offset+1] == '/') {
					scanner.offset++
				}
				if scanner.offset+1 >= len(scanner.input) {
					return importToken{}, errors.New("unterminated block comment")
				}
				scanner.offset += 2
				continue
			}
		}
		if isImportIdentifierStart(current) {
			start := scanner.offset
			scanner.offset++
			for scanner.offset < len(scanner.input) && isImportIdentifierPart(scanner.input[scanner.offset]) {
				scanner.offset++
			}
			return importToken{kind: importTokenIdentifier, value: string(scanner.input[start:scanner.offset])}, nil
		}
		if current == '"' || current == '\'' {
			value, err := scanner.scanString(current)
			if err != nil {
				return importToken{}, err
			}
			return importToken{kind: importTokenString, value: value}, nil
		}
		scanner.offset++
		switch current {
		case ';':
			return importToken{kind: importTokenSemicolon, value: ";"}, nil
		case '{':
			return importToken{kind: importTokenLeftBrace, value: "{"}, nil
		case '}':
			return importToken{kind: importTokenRightBrace, value: "}"}, nil
		}
		return importToken{kind: importTokenOther, value: string(current)}, nil
	}
	return importToken{kind: importTokenEOF}, nil
}

func (scanner *importScanner) scanString(quote byte) (string, error) {
	scanner.offset++
	var decoded []byte
	for scanner.offset < len(scanner.input) {
		current := scanner.input[scanner.offset]
		switch current {
		case quote:
			scanner.offset++
			return string(decoded), nil
		case '\n', '\r':
			return "", errors.New("unterminated protobuf string literal")
		case 0:
			return "", errors.New("protobuf string literal contains an unescaped NUL byte")
		case '\\':
			scanner.offset++
			escaped, err := scanner.scanEscape()
			if err != nil {
				return "", err
			}
			decoded = append(decoded, escaped...)
		default:
			decoded = append(decoded, current)
			scanner.offset++
		}
	}
	return "", errors.New("unterminated protobuf string literal")
}

func (scanner *importScanner) scanEscape() ([]byte, error) {
	if scanner.offset >= len(scanner.input) {
		return nil, errors.New("unterminated protobuf string escape")
	}
	escape := scanner.input[scanner.offset]
	scanner.offset++
	switch escape {
	case 'a':
		return []byte{'\a'}, nil
	case 'b':
		return []byte{'\b'}, nil
	case 'f':
		return []byte{'\f'}, nil
	case 'n':
		return []byte{'\n'}, nil
	case 'r':
		return []byte{'\r'}, nil
	case 't':
		return []byte{'\t'}, nil
	case 'v':
		return []byte{'\v'}, nil
	case '\\', '\'', '"', '?':
		return []byte{escape}, nil
	case 'x', 'X':
		return scanner.scanByteEscape(16, 2, "hexadecimal")
	case 'u':
		return scanner.scanUnicodeEscape(4)
	case 'U':
		return scanner.scanUnicodeEscape(8)
	default:
		if escape >= '0' && escape <= '7' {
			scanner.offset--
			return scanner.scanByteEscape(8, 3, "octal")
		}
		return nil, fmt.Errorf("unsupported protobuf string escape \\%c", escape)
	}
}

func (scanner *importScanner) scanByteEscape(base, limit int, label string) ([]byte, error) {
	value := 0
	digits := 0
	for digits < limit && scanner.offset < len(scanner.input) {
		digit, valid := importDigitValue(scanner.input[scanner.offset], base)
		if !valid {
			break
		}
		value = value*base + digit
		digits++
		scanner.offset++
	}
	if digits == 0 {
		return nil, fmt.Errorf("protobuf %s escape requires at least one digit", label)
	}
	if value > 0xff {
		return nil, fmt.Errorf("protobuf %s escape value %#x exceeds one byte", label, value)
	}
	return []byte{byte(value)}, nil
}

func (scanner *importScanner) scanUnicodeEscape(width int) ([]byte, error) {
	value := 0
	for index := 0; index < width; index++ {
		if scanner.offset >= len(scanner.input) {
			return nil, fmt.Errorf("protobuf Unicode escape requires exactly %d hexadecimal digits", width)
		}
		digit, valid := importDigitValue(scanner.input[scanner.offset], 16)
		if !valid {
			return nil, fmt.Errorf("protobuf Unicode escape requires exactly %d hexadecimal digits", width)
		}
		value = value*16 + digit
		scanner.offset++
	}
	if value > unicode.MaxRune || value >= 0xd800 && value <= 0xdfff {
		return nil, fmt.Errorf("protobuf Unicode escape value U+%X is not a Unicode scalar value", value)
	}
	encoded := make([]byte, utf8.UTFMax)
	length := utf8.EncodeRune(encoded, rune(value))
	return encoded[:length], nil
}

func importDigitValue(value byte, base int) (int, bool) {
	var digit int
	switch {
	case value >= '0' && value <= '9':
		digit = int(value - '0')
	case value >= 'a' && value <= 'f':
		digit = int(value-'a') + 10
	case value >= 'A' && value <= 'F':
		digit = int(value-'A') + 10
	default:
		return 0, false
	}
	return digit, digit < base
}

func isImportIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isImportIdentifierPart(value byte) bool {
	return isImportIdentifierStart(value) || value >= '0' && value <= '9'
}
