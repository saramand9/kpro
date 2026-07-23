// Package configcheck contains deterministic, side-effect-free transport
// configuration checks shared by the built-in adapters.
package configcheck

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	pathpkg "path"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// ListenAddress validates the host:port shape used by TCP- and UDP-backed
// listeners without resolving DNS or binding a socket.
func ListenAddress(name, address string) error {
	if strings.TrimSpace(address) == "" {
		return fmt.Errorf("%s is required", name)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%s %q is invalid: %w", name, address, err)
	}
	if host != strings.TrimSpace(host) || strings.ContainsAny(host, " \r\n\t") {
		return fmt.Errorf("%s %q contains whitespace in the host", name, address)
	}
	if port == "" || port != strings.TrimSpace(port) || strings.ContainsAny(port, "\r\n\t ") {
		return fmt.Errorf("%s %q has an invalid port", name, address)
	}
	if isDecimal(port) {
		number, err := strconv.Atoi(port)
		if err != nil || number > 65535 {
			return fmt.Errorf("%s %q has a port outside 0..65535", name, address)
		}
	} else if !isServiceName(port) {
		return fmt.Errorf("%s %q has an invalid service name", name, address)
	}
	return nil
}

func isDecimal(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}

func isServiceName(value string) bool {
	characters := []rune(value)
	if len(characters) == 0 || !isAlphaNumeric(characters[0]) || !isAlphaNumeric(characters[len(characters)-1]) {
		return false
	}
	for _, character := range characters {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' {
			continue
		}
		return false
	}
	return true
}

func isAlphaNumeric(character rune) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}

// NonNegativeInt validates a zero-defaulted integer option.
func NonNegativeInt(name string, value int) error {
	if value < 0 {
		return fmt.Errorf("%s cannot be negative", name)
	}
	return nil
}

// NonNegativeInt64 validates a zero-defaulted int64 option.
func NonNegativeInt64(name string, value int64) error {
	if value < 0 {
		return fmt.Errorf("%s cannot be negative", name)
	}
	return nil
}

// NonNegativeDuration validates a zero-defaulted duration option.
func NonNegativeDuration(name string, value time.Duration) error {
	if value < 0 {
		return fmt.Errorf("%s cannot be negative", name)
	}
	return nil
}

// IntRangeIfSet validates a zero-defaulted integer with an inclusive range.
func IntRangeIfSet(name string, value, minimum, maximum int) error {
	if value == 0 {
		return nil
	}
	if value < minimum || value > maximum {
		return fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return nil
}

// HTTPPath validates the exact path shape accepted by KPro's SSE and
// WebSocket endpoints. ServeMux wildcard patterns are intentionally excluded.
func HTTPPath(name, path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%s must start with '/'", name)
	}
	if strings.ContainsAny(path, " \t\r\n?#{}") {
		return fmt.Errorf("%s %q contains unsupported path characters", name, path)
	}
	cleaned := pathpkg.Clean(path)
	if path != cleaned && !(cleaned != "/" && path == cleaned+"/") {
		return fmt.Errorf("%s %q is not canonical", name, path)
	}
	return nil
}

// TypedNil rejects an interface that is non-nil but contains a nil reference.
func TypedNil(name string, value any) error {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if reflected.IsNil() {
			return fmt.Errorf("%s cannot be typed nil", name)
		}
	}
	return nil
}

// ALPN validates protocol identifiers before a TLS or QUIC listener is bound.
func ALPN(name string, protocols []string) error {
	if len(protocols) == 0 {
		return fmt.Errorf("%s requires at least one protocol", name)
	}
	var validationErr error
	wireLength := 0
	for index, protocol := range protocols {
		length := len(protocol)
		wireLength += 1 + length
		if length == 0 || length > 255 {
			validationErr = errors.Join(validationErr, fmt.Errorf(
				"%s[%d] must contain 1..255 bytes",
				name,
				index,
			))
		}
	}
	if wireLength > 65535 {
		validationErr = errors.Join(validationErr, fmt.Errorf("%s encoded length exceeds 65535 bytes", name))
	}
	return validationErr
}

// TLSKeyPair validates that both certificate paths exist and contain a
// matching, parseable key pair. Callers should skip this when an in-memory TLS
// configuration owns certificate selection.
func TLSKeyPair(name, certFile, keyFile string) error {
	if certFile == "" || keyFile == "" {
		return fmt.Errorf("%s requires both certificate and key files", name)
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return fmt.Errorf("load %s certificate and key: %w", name, err)
	}
	return nil
}

// TLSConfig validates that an in-memory server configuration has at least one
// way to select a certificate during the handshake.
func TLSConfig(name string, config *tls.Config) error {
	if config == nil {
		return fmt.Errorf("%s TLS configuration is required", name)
	}
	hasStaticCertificate := false
	for _, certificate := range config.Certificates {
		if len(certificate.Certificate) > 0 && certificate.PrivateKey != nil {
			hasStaticCertificate = true
			break
		}
	}
	if !hasStaticCertificate && config.GetCertificate == nil && config.GetConfigForClient == nil {
		return fmt.Errorf("%s TLS configuration cannot select a certificate", name)
	}
	return nil
}
