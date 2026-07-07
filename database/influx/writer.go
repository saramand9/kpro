package influxstore

import (
	"context"
	"fmt"

	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

// Writer writes points and line-protocol records to InfluxDB.
type Writer struct {
	client *Client
	org    string
	bucket string
}

// NewWriter creates a writer for org and bucket.
func NewWriter(client *Client, org string, bucket string) *Writer {
	return &Writer{
		client: client,
		org:    org,
		bucket: bucket,
	}
}

// WritePoint writes a single metric point.
func (w *Writer) WritePoint(ctx context.Context, point Point) error {
	return w.WritePoints(ctx, point)
}

// WritePoints writes one or more metric points.
func (w *Writer) WritePoints(ctx context.Context, points ...Point) error {
	if len(points) == 0 {
		return nil
	}
	influxPoints := make([]*write.Point, 0, len(points))
	for _, point := range points {
		influxPoint, err := point.InfluxPoint()
		if err != nil {
			return err
		}
		influxPoints = append(influxPoints, influxPoint)
	}
	return w.writeAPI().WritePoint(contextOrBackground(ctx), influxPoints...)
}

// WriteRecord writes one or more line-protocol records.
func (w *Writer) WriteRecord(ctx context.Context, lines ...string) error {
	if len(lines) == 0 {
		return nil
	}
	return w.writeAPI().WriteRecord(contextOrBackground(ctx), lines...)
}

func (w *Writer) writeAPI() interface {
	WritePoint(context.Context, ...*write.Point) error
	WriteRecord(context.Context, ...string) error
} {
	if w == nil || w.client == nil || w.client.influx == nil {
		return failedWriteAPI{err: fmt.Errorf("%w: nil client", ErrInvalidConfig)}
	}
	if w.org == "" {
		return failedWriteAPI{err: fmt.Errorf("%w: missing org", ErrInvalidConfig)}
	}
	if w.bucket == "" {
		return failedWriteAPI{err: fmt.Errorf("%w: missing bucket", ErrInvalidConfig)}
	}
	return w.client.influx.WriteAPIBlocking(w.org, w.bucket)
}

type failedWriteAPI struct {
	err error
}

func (f failedWriteAPI) WritePoint(context.Context, ...*write.Point) error {
	return f.err
}

func (f failedWriteAPI) WriteRecord(context.Context, ...string) error {
	return f.err
}
