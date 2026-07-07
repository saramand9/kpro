package qdrantstore

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidConfig  = errors.New("invalid qdrant config")
	ErrInvalidRequest = errors.New("invalid qdrant request")
)

// ResponseError reports a non-2xx Qdrant response.
type ResponseError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("qdrant response %d %s: %s", e.StatusCode, e.Status, e.Body)
}
