package elasticstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/saramand9/bignet/kpro/database/contracts/readmodels"
)

// Index implements a typed Elasticsearch search index.
type Index[T any] struct {
	client *Client
	index  string
}

var _ readmodels.SearchIndex[any] = (*Index[any])(nil)

// Index implements readmodels.SearchIndex.
func (i *Index[T]) Index(ctx context.Context, id string, document T) error {
	if err := i.validate(id); err != nil {
		return err
	}
	reader, err := encodeBody(document)
	if err != nil {
		return err
	}
	resp, err := i.client.elastic.Index(
		i.index,
		reader,
		i.client.elastic.Index.WithContext(contextOrBackground(ctx)),
		i.client.elastic.Index.WithDocumentID(id),
	)
	if err != nil {
		return err
	}
	return checkResponse(resp)
}

// Delete implements readmodels.SearchIndex.
func (i *Index[T]) Delete(ctx context.Context, id string) error {
	if err := i.validate(id); err != nil {
		return err
	}
	resp, err := i.client.elastic.Delete(
		i.index,
		id,
		i.client.elastic.Delete.WithContext(contextOrBackground(ctx)),
	)
	if err != nil {
		return err
	}
	return checkResponse(resp)
}

// GetRaw returns a document source as raw JSON.
func (i *Index[T]) GetRaw(ctx context.Context, id string) (json.RawMessage, bool, error) {
	if err := i.validate(id); err != nil {
		return nil, false, err
	}
	resp, err := i.client.elastic.Get(
		i.index,
		id,
		i.client.elastic.Get.WithContext(contextOrBackground(ctx)),
	)
	if err != nil {
		return nil, false, err
	}
	body, err := readResponse(resp)
	if responseErr, ok := err.(*ResponseError); ok && responseErr.StatusCode == 404 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var doc struct {
		Found  bool            `json:"found"`
		Source json.RawMessage `json:"_source"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, false, err
	}
	return doc.Source, doc.Found, nil
}

// SearchRaw executes a native Elasticsearch search request.
func (i *Index[T]) SearchRaw(ctx context.Context, query any) (json.RawMessage, error) {
	if i == nil || i.client == nil || i.client.elastic == nil {
		return nil, sql.ErrConnDone
	}
	reader, err := encodeBody(query)
	if err != nil {
		return nil, err
	}
	resp, err := i.client.elastic.Search(
		i.client.elastic.Search.WithContext(contextOrBackground(ctx)),
		i.client.elastic.Search.WithIndex(i.index),
		i.client.elastic.Search.WithBody(reader),
	)
	if err != nil {
		return nil, err
	}
	body, err := readResponse(resp)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func (i *Index[T]) validate(id string) error {
	if i == nil || i.client == nil || i.client.elastic == nil {
		return sql.ErrConnDone
	}
	if i.index == "" {
		return fmt.Errorf("%w: index is required", ErrInvalidRequest)
	}
	if id == "" {
		return fmt.Errorf("%w: document id is required", ErrInvalidRequest)
	}
	return nil
}
