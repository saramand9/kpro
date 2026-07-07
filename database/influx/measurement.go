package influxstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"github.com/saramand9/bignet/kpro/database/views"
)

type writePoint = write.Point

// PointMapper converts a typed value to an official InfluxDB point.
type PointMapper[T any] func(value T) (*write.Point, error)

// Measurement adapts typed values to views.Measurement.
type Measurement[T any] struct {
	client      *Client
	org         string
	bucket      string
	measurement string
	mapper      PointMapper[T]
}

var _ views.Measurement[any] = (*Measurement[any])(nil)

// NewMeasurement creates a typed measurement view.
func NewMeasurement[T any](client *Client, org string, bucket string, measurement string, mapper PointMapper[T]) *Measurement[T] {
	return &Measurement[T]{
		client:      client,
		org:         org,
		bucket:      bucket,
		measurement: measurement,
		mapper:      mapper,
	}
}

// Name returns the logical measurement name.
func (m *Measurement[T]) Name() string {
	if m == nil {
		return ""
	}
	return m.measurement
}

// WritePoint implements views.Measurement.
func (m *Measurement[T]) WritePoint(ctx context.Context, point T) error {
	return m.WriteBatch(ctx, []T{point})
}

// WriteBatch implements views.Measurement.
func (m *Measurement[T]) WriteBatch(ctx context.Context, points []T) error {
	if len(points) == 0 {
		return nil
	}
	if m == nil || m.client == nil || m.client.influx == nil {
		return sql.ErrConnDone
	}
	if m.org == "" {
		return fmt.Errorf("%w: missing org", ErrInvalidConfig)
	}
	if m.bucket == "" {
		return fmt.Errorf("%w: missing bucket", ErrInvalidConfig)
	}
	if m.mapper == nil {
		return fmt.Errorf("%w: missing point mapper", ErrInvalidConfig)
	}
	influxPoints := make([]*write.Point, 0, len(points))
	for _, point := range points {
		influxPoint, err := m.mapper(point)
		if err != nil {
			return err
		}
		influxPoints = append(influxPoints, influxPoint)
	}
	return m.client.influx.WriteAPIBlocking(m.org, m.bucket).WritePoint(contextOrBackground(ctx), influxPoints...)
}
