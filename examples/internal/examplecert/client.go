package examplecert

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Client uses system trust by default, or the explicitly selected PEM CA file.
// insecure is an explicit opt-in for local demonstrations with self-signed TLS.
func Client(caFile string, insecure bool) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		data, err := os.ReadFile(caFile) // #nosec G304 -- CA file is explicitly selected by the local CLI operator.
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("CA file contains no valid PEM certificates")
		}
		config.RootCAs = roots
	}
	if insecure {
		config.InsecureSkipVerify = true // #nosec G402 -- Explicit CLI opt-in for a local self-signed demonstration; disabled by default.
	}
	return config, nil
}
