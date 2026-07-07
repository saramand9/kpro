package influxstore

import (
	"fmt"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

// Point is a single InfluxDB time-series data point.
type Point struct {
	Measurement string
	Tags        map[string]string
	Fields      map[string]any
	Time        time.Time
}

// PointOption changes a point during construction.
type PointOption func(*Point)

// NewPoint creates a metric point.
func NewPoint(measurement string, fields map[string]any, options ...PointOption) Point {
	point := Point{
		Measurement: measurement,
		Fields:      fields,
	}
	for _, option := range options {
		if option != nil {
			option(&point)
		}
	}
	return point
}

// WithTags sets point tags.
func WithTags(tags map[string]string) PointOption {
	return func(point *Point) {
		point.Tags = tags
	}
}

// WithTime sets point timestamp.
func WithTime(ts time.Time) PointOption {
	return func(point *Point) {
		point.Time = ts
	}
}

// InfluxPoint converts this point to the official client point type.
func (p Point) InfluxPoint() (*write.Point, error) {
	if p.Measurement == "" {
		return nil, fmt.Errorf("%w: missing measurement", ErrInvalidPoint)
	}
	if len(p.Fields) == 0 {
		return nil, fmt.Errorf("%w: missing fields", ErrInvalidPoint)
	}
	return write.NewPoint(p.Measurement, p.Tags, p.Fields, p.Time), nil
}
