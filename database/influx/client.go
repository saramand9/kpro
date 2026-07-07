package influxstore

import (
	"context"
	"fmt"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
)

// Client owns an InfluxDB client and exposes time-series helpers.
type Client struct {
	name   string
	influx influxdb2.Client
	owns   bool
	org    string
	bucket string
}

var _ lifecycle.Resource = (*Client)(nil)

// Open creates an InfluxDB adapter client and verifies it with Health unless skipped.
func Open(ctx context.Context, config Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	influxClient := config.Client
	owns := false
	if influxClient == nil {
		if config.Options != nil {
			influxClient = influxdb2.NewClientWithOptions(config.URL, config.Token, config.Options)
		} else {
			influxClient = influxdb2.NewClient(config.URL, config.Token)
		}
		owns = true
	}
	client := &Client{
		name:   config.Name,
		influx: influxClient,
		owns:   owns,
		org:    config.Org,
		bucket: config.Bucket,
	}
	if !config.SkipHealthCheck {
		healthCtx, cancel := context.WithTimeout(ctx, config.HealthTimeout)
		defer cancel()
		if err := client.Ping(healthCtx); err != nil {
			if owns {
				influxClient.Close()
			}
			return nil, err
		}
	}
	return client, nil
}

// Influx returns the underlying official InfluxDB client.
func (c *Client) Influx() influxdb2.Client {
	if c == nil {
		return nil
	}
	return c.influx
}

// Org returns the default organization configured for this adapter.
func (c *Client) Org() string {
	if c == nil {
		return ""
	}
	return c.org
}

// Bucket returns the default bucket configured for this adapter.
func (c *Client) Bucket() string {
	if c == nil {
		return ""
	}
	return c.bucket
}

// Writer returns a metric writer using the default org and bucket unless overridden.
func (c *Client) Writer(bucket ...string) *Writer {
	selectedBucket := c.Bucket()
	if len(bucket) > 0 {
		selectedBucket = bucket[0]
	}
	return NewWriter(c, c.Org(), selectedBucket)
}

// Query returns a Flux query helper using the default org unless overridden.
func (c *Client) Query(org ...string) *Query {
	selectedOrg := c.Org()
	if len(org) > 0 {
		selectedOrg = org[0]
	}
	return NewQuery(c, selectedOrg)
}

// Measurement returns a typed point measurement using the default org and bucket.
func (c *Client) Measurement(name string, bucket ...string) *Measurement[Point] {
	selectedBucket := c.Bucket()
	if len(bucket) > 0 {
		selectedBucket = bucket[0]
	}
	return NewMeasurement[Point](c, c.Org(), selectedBucket, name, func(point Point) (*writePoint, error) {
		if point.Measurement == "" {
			point.Measurement = name
		}
		return point.InfluxPoint()
	})
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
	return lifecycle.KindMetrics
}

// Ping implements lifecycle.Resource.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.influx == nil {
		return fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	_, err := c.influx.Health(contextOrBackground(ctx))
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

// Shutdown releases the InfluxDB client when this adapter opened it.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil || c.influx == nil || !c.owns {
		return nil
	}
	c.influx.Close()
	return nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
