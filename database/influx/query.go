package influxstore

import (
	"context"
	"fmt"
)

// Query runs Flux queries for an InfluxDB organization.
type Query struct {
	client *Client
	org    string
}

// Record is one row returned from a parsed Flux query result.
type Record struct {
	Values map[string]any
}

// NewQuery creates a query helper for org.
func NewQuery(client *Client, org string) *Query {
	return &Query{
		client: client,
		org:    org,
	}
}

// QueryRaw returns the annotated CSV returned by Flux.
func (q *Query) QueryRaw(ctx context.Context, flux string) (string, error) {
	if q == nil || q.client == nil || q.client.influx == nil {
		return "", fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if q.org == "" {
		return "", fmt.Errorf("%w: missing org", ErrInvalidConfig)
	}
	return q.client.influx.QueryAPI(q.org).QueryRaw(contextOrBackground(ctx), flux, nil)
}

// Query returns parsed Flux rows with copied value maps.
func (q *Query) Query(ctx context.Context, flux string) ([]Record, error) {
	if q == nil || q.client == nil || q.client.influx == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if q.org == "" {
		return nil, fmt.Errorf("%w: missing org", ErrInvalidConfig)
	}
	result, err := q.client.influx.QueryAPI(q.org).Query(contextOrBackground(ctx), flux)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	records := make([]Record, 0)
	for result.Next() {
		values := result.Record().Values()
		copied := make(map[string]any, len(values))
		for key, value := range values {
			copied[key] = value
		}
		records = append(records, Record{Values: copied})
	}
	if err := result.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
