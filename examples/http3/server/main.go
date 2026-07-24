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
	http3x "github.com/saramand9/kpro/transport/http3"
)

func main() {
	addr := flag.String("addr", ":8443", "listen address")
	flag.Parse()

	certFile, keyFile, cleanup, err := selfSignedCert()
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	app := server.New(server.Config{Name: "http3-example"})
	httpEndpoint := http3x.New(http3x.Config{
		Addr:     *addr,
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	app.MountRunner(httpEndpoint)
	httpEndpoint.
		Get("/health", func(ctx *http3x.Context) error {
			return ctx.Text(200, "ok\n")
		}).
		Post("/echo", func(ctx *http3x.Context) error {
			body, err := ctx.Body()
			if err != nil {
				return err
			}
			return ctx.JSON(200, map[string]interface{}{"echo": string(body)})
		})

	log.Printf("http3 example server listening on https://127.0.0.1%s", *addr)
	log.Fatal(app.ListenAndServe())
}

func selfSignedCert() (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "kpro-http3-example-*")
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
	if err := writePEM(certFile, 0644, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := writePEM(keyFile, 0600, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return certFile, keyFile, cleanup, nil
}

func writePEM(path string, perm os.FileMode, block *pem.Block) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer file.Close()
	return pem.Encode(file, block)
}
