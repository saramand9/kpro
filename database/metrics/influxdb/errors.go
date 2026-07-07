// Package influxstore adapts InfluxDB as a time-series resource.
package influxstore

import "errors"

var (
	// ErrInvalidConfig reports invalid InfluxDB adapter configuration.
	ErrInvalidConfig = errors.New("invalid influx config")
	// ErrInvalidPoint reports an invalid time-series point.
	ErrInvalidPoint = errors.New("invalid influx point")
)
