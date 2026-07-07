package elasticstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// BulkService writes Elasticsearch bulk requests.
type BulkService struct {
	client *Client
	index  string
}

// BulkOperation is one operation in an Elasticsearch bulk request.
type BulkOperation struct {
	Action   string
	Index    string
	ID       string
	Document any
	Routing  string
}

// BulkIndex creates an index operation.
func BulkIndex(id string, document any) BulkOperation {
	return BulkOperation{Action: "index", ID: id, Document: document}
}

// BulkCreate creates a create-only operation.
func BulkCreate(id string, document any) BulkOperation {
	return BulkOperation{Action: "create", ID: id, Document: document}
}

// BulkUpdate creates an update operation using document as the partial doc.
func BulkUpdate(id string, document any) BulkOperation {
	return BulkOperation{Action: "update", ID: id, Document: map[string]any{"doc": document}}
}

// BulkDelete creates a delete operation.
func BulkDelete(id string) BulkOperation {
	return BulkOperation{Action: "delete", ID: id}
}

// Do executes a typed bulk request.
func (s *BulkService) Do(ctx context.Context, operations ...BulkOperation) error {
	if len(operations) == 0 {
		return nil
	}
	body, err := s.bulkBody(operations)
	if err != nil {
		return err
	}
	return s.Raw(ctx, body.String())
}

// Raw executes an already-built newline-delimited bulk body.
func (s *BulkService) Raw(ctx context.Context, body any) error {
	if s == nil || s.client == nil || s.client.elastic == nil {
		return ErrInvalidRequest
	}
	reader, err := encodeBody(body)
	if err != nil {
		return err
	}
	if reader == nil {
		return ErrInvalidRequest
	}
	options := []func(*esapi.BulkRequest){
		s.client.elastic.Bulk.WithContext(contextOrBackground(ctx)),
	}
	if s.index != "" {
		options = append(options, s.client.elastic.Bulk.WithIndex(s.index))
	}
	resp, err := s.client.elastic.Bulk(reader, options...)
	if err != nil {
		return err
	}
	return checkBulkResponse(resp)
}

func (s *BulkService) bulkBody(operations []BulkOperation) (*bytes.Buffer, error) {
	var body bytes.Buffer
	for _, operation := range operations {
		action := operation.Action
		if action == "" {
			action = "index"
		}
		index := operation.Index
		if index == "" && s != nil {
			index = s.index
		}
		if index == "" {
			return nil, fmt.Errorf("%w: missing bulk operation index", ErrInvalidRequest)
		}
		metadata := map[string]map[string]string{
			action: {"_index": index},
		}
		if operation.ID != "" {
			metadata[action]["_id"] = operation.ID
		}
		if operation.Routing != "" {
			metadata[action]["routing"] = operation.Routing
		}
		if err := appendNDJSON(&body, metadata); err != nil {
			return nil, err
		}
		if action != "delete" {
			if operation.Document == nil {
				return nil, fmt.Errorf("%w: missing bulk operation document", ErrInvalidRequest)
			}
			if err := appendNDJSON(&body, operation.Document); err != nil {
				return nil, err
			}
		}
	}
	return &body, nil
}

func appendNDJSON(body *bytes.Buffer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body.Write(data)
	body.WriteByte('\n')
	return nil
}

func checkBulkResponse(resp *esapi.Response) error {
	body, err := readResponse(resp)
	if err != nil {
		return err
	}
	var result struct {
		Errors bool `json:"errors"`
	}
	if err := json.Unmarshal(body, &result); err == nil && result.Errors {
		return &ResponseError{StatusCode: 200, Status: "200 OK", Body: string(body)}
	}
	return nil
}
