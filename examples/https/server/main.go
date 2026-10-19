// File: examples/https/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/httpx"
)

func main() {
	addr := flag.String("addr", ":8443", "listen address")
	flag.Parse()

	certFile, keyFile, cleanup, err := selfSignedCert()
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	app := server.New(server.Config{Name: "https-example"})
	httpEndpoint := httpx.New(httpx.Config{
		Addr:     *addr,
		TLS:      true,
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	app.MountRunner(httpEndpoint)
	httpEndpoint.
		Get("/health", func(ctx *httpx.Context) error {
			return ctx.Text(200, "ok\n")
		}).
		Post("/echo", func(ctx *httpx.Context) error {
			body, err := ctx.Body()
			if err != nil {
				return err
			}
			return ctx.JSON(200, map[string]interface{}{"echo": string(body)})
		})

	log.Printf("https example server listening on https://127.0.0.1%s", *addr)
	log.Fatal(app.ListenAndServe())
}

func selfSignedCert() (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "kpro-https-example-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	certOut, err := os.Create(certFile) // #nosec G304 -- Example certificate files are created under an application-owned temporary directory.
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		_ = certOut.Close()
		cleanup()
		return "", "", nil, err
	}
	_ = certOut.Close()

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600) // #nosec G304 -- Example certificate files are created under an application-owned temporary directory.
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		_ = keyOut.Close()
		cleanup()
		return "", "", nil, err
	}
	_ = keyOut.Close()
	return certFile, keyFile, cleanup, nil
}
