package elasticstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/saramand9/kpro/database/common/lifecycle"
)

type closeIdleTransport interface {
	CloseIdleConnections()
}

// Client owns an Elasticsearch client and exposes search helpers.
type Client struct {
	name          string
	elastic       *elasticsearch.Client
	ownsTransport bool
	transport     http.RoundTripper
}

var _ lifecycle.Resource = (*Client)(nil)

// Open creates an Elasticsearch adapter client.
func Open(ctx context.Context, config Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	elasticClient := config.Client
	var ownedTransport http.RoundTripper
	if elasticClient == nil {
		clientConfig := elasticConfig(config)
		ownedTransport, err = prepareOwnedTransport(&clientConfig)
		if err != nil {
			return nil, err
		}
		elasticClient, err = elasticsearch.NewClient(clientConfig)
		if err != nil {
			if closer, ok := ownedTransport.(closeIdleTransport); ok {
				closer.CloseIdleConnections()
			}
			return nil, err
		}
	}
	client := &Client{
		name:          config.Name,
		elastic:       elasticClient,
		ownsTransport: ownedTransport != nil,
		transport:     ownedTransport,
	}
	if !config.SkipHealthCheck {
		healthCtx, cancel := context.WithTimeout(ctx, config.HealthTimeout)
		defer cancel()
		if err := client.Ping(healthCtx); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	return client, nil
}

// Elastic returns the underlying official Elasticsearch client.
func (c *Client) Elastic() *elasticsearch.Client {
	if c == nil {
		return nil
	}
	return c.elastic
}

// SearchIndex returns a typed search index view.
func SearchIndex[T any](client *Client, index string) *Index[T] {
	return &Index[T]{client: client, index: index}
}

// SearchIndex returns an untyped search index view.
func (c *Client) SearchIndex(index string) *Index[any] {
	return SearchIndex[any](c, index)
}

// Bulk returns a bulk request helper scoped to index when provided.
func (c *Client) Bulk(index ...string) *BulkService {
	selectedIndex := ""
	if len(index) > 0 {
		selectedIndex = index[0]
	}
	return &BulkService{client: c, index: selectedIndex}
}

// Name implements lifecycle.Resource.
func (c *Client) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// Kind implements lifecycle.Resource.
func (c *Client) Kind() lifecycle.Kind {
	return lifecycle.KindSearch
}

// Ping implements lifecycle.Resource.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.elastic == nil {
		return fmt.Errorf("%w: elastic client is nil", lifecycle.ErrUnavailable)
	}
	resp, err := c.elastic.Info(c.elastic.Info.WithContext(contextOrBackground(ctx)))
	if err != nil {
		return err
	}
	return checkResponse(resp)
}

// Health implements lifecycle.Resource.
func (c *Client) Health(ctx context.Context) lifecycle.Health {
	return lifecycle.CheckHealth(ctx, c)
}

// Close implements lifecycle.Resource.
func (c *Client) Close() error {
	return c.Shutdown(context.Background())
}

// Shutdown releases transport resources when this adapter opened the client.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil || !c.ownsTransport || c.transport == nil {
		return nil
	}
	if closer, ok := c.transport.(closeIdleTransport); ok {
		closer.CloseIdleConnections()
	}
	return nil
}

// prepareOwnedTransport installs a private clone when the caller did not
// provide a transport. This makes idle-connection ownership explicit instead
// of closing transports supplied through Config or ClientConfig.
func prepareOwnedTransport(config *elasticsearch.Config) (http.RoundTripper, error) {
	if config == nil || config.Transport != nil {
		return nil, nil
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("%w: default HTTP transport is not cloneable", ErrInvalidConfig)
	}
	transport := defaultTransport.Clone()
	if len(config.CACert) > 0 {
		tlsConfig := transport.TLSClientConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			tlsConfig = tlsConfig.Clone()
		}
		tlsConfig.RootCAs = x509.NewCertPool()
		if ok := tlsConfig.RootCAs.AppendCertsFromPEM(config.CACert); !ok {
			return nil, fmt.Errorf("%w: cannot parse CA certificate", ErrInvalidConfig)
		}
		transport.TLSClientConfig = tlsConfig
		config.CACert = nil
	}
	config.Transport = transport
	return transport, nil
}

func encodeBody(body any) (io.Reader, error) {
	if body == nil {
		return nil, nil
	}
	switch value := body.(type) {
	case io.Reader:
		return value, nil
	case []byte:
		return bytes.NewReader(value), nil
	case json.RawMessage:
		return bytes.NewReader(value), nil
	case string:
		return strings.NewReader(value), nil
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return bytes.NewReader(data), nil
	}
}

func checkResponse(response *esapi.Response) error {
	if response == nil {
		return ErrInvalidRequest
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode <= 299 {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	body, _ := io.ReadAll(response.Body)
	return &ResponseError{StatusCode: response.StatusCode, Status: response.Status(), Body: strings.TrimSpace(string(body))}
}

func readResponse(response *esapi.Response) ([]byte, error) {
	if response == nil {
		return nil, ErrInvalidRequest
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 200 && response.StatusCode <= 299 {
		return body, nil
	}
	return nil, &ResponseError{StatusCode: response.StatusCode, Status: response.Status(), Body: strings.TrimSpace(string(body))}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
