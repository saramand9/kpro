package qdrantstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/saramand9/bignet/kpro/database/contracts/lifecycle"
)

// Client owns Qdrant HTTP API access.
type Client struct {
	name   string
	url    string
	apiKey string
	header http.Header
	http   *http.Client
}

var _ lifecycle.Resource = (*Client)(nil)

// Open creates a Qdrant adapter and verifies it unless skipped.
func Open(ctx context.Context, config Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	client := &Client{
		name:   config.Name,
		url:    config.URL,
		apiKey: config.APIKey,
		header: cloneHeader(config.Header),
		http:   config.HTTPClient,
	}
	if !config.SkipHealthCheck {
		healthCtx, cancel := context.WithTimeout(ctx, config.HealthTimeout)
		defer cancel()
		if err := client.Ping(healthCtx); err != nil {
			return nil, err
		}
	}
	return client, nil
}

// Collection returns a typed vector collection view.
func Collection[T any](client *Client, name string) *CollectionView[T] {
	return &CollectionView[T]{client: client, name: name}
}

// Collection returns an untyped vector collection view.
func (c *Client) Collection(name string) *CollectionView[any] {
	return Collection[any](c, name)
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
	return lifecycle.KindVector
}

// Ping implements lifecycle.Resource.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/", nil, nil)
	return err
}

// Health implements lifecycle.Resource.
func (c *Client) Health(ctx context.Context) lifecycle.Health {
	return lifecycle.CheckHealth(ctx, c)
}

// Close implements lifecycle.Resource.
func (c *Client) Close() error {
	return c.Shutdown(context.Background())
}

// Shutdown releases adapter resources. The underlying HTTP client is caller-owned.
func (c *Client) Shutdown(ctx context.Context) error {
	return nil
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values, body any) ([]byte, error) {
	if c == nil || c.http == nil || c.url == "" {
		return nil, ErrInvalidConfig
	}
	reader, err := encodeBody(body)
	if err != nil {
		return nil, err
	}
	requestURL := c.url + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(contextOrBackground(ctx), method, requestURL, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		request.Header.Set("api-key", c.apiKey)
	}
	for key, values := range c.header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	return readResponse(response)
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

func readResponse(response *http.Response) ([]byte, error) {
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
	return nil, &ResponseError{StatusCode: response.StatusCode, Status: response.Status, Body: strings.TrimSpace(string(body))}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func cloneHeader(header http.Header) http.Header {
	if header == nil {
		return nil
	}
	return header.Clone()
}

func invalidRequest(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, message)
}
