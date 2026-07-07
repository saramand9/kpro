package elasticstore

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidConfig  = errors.New("invalid elastic config")
	ErrInvalidRequest = errors.New("invalid elastic request")
)

// ResponseError reports a non-2xx Elasticsearch response.
type ResponseError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("elastic response %d %s: %s", e.StatusCode, e.Status, e.Body)
}
