package mongostore

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CollectionView wraps a MongoDB collection with common document helpers.
type CollectionView[T any] struct {
	client     *Client
	collection *mongo.Collection
	name       string
}

// NewCollection creates a collection helper for name.
func NewCollection[T any](client *Client, name string) *CollectionView[T] {
	collection := (*mongo.Collection)(nil)
	if client != nil && client.database != nil && name != "" {
		collection = client.database.Collection(name)
	}
	return &CollectionView[T]{client: client, collection: collection, name: name}
}

// Mongo returns the underlying MongoDB collection.
func (c *CollectionView[T]) Mongo() *mongo.Collection {
	if c == nil {
		return nil
	}
	return c.collection
}

// Name returns the MongoDB collection name.
func (c *CollectionView[T]) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// InsertOne inserts one document.
func (c *CollectionView[T]) InsertOne(ctx context.Context, document T, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	if err := c.requireCollection(); err != nil {
		return nil, err
	}
	return c.collection.InsertOne(contextOrBackground(ctx), document, opts...)
}

// InsertMany inserts multiple documents.
func (c *CollectionView[T]) InsertMany(ctx context.Context, documents []T, opts ...*options.InsertManyOptions) (*mongo.InsertManyResult, error) {
	if err := c.requireCollection(); err != nil {
		return nil, err
	}
	values := make([]any, 0, len(documents))
	for _, document := range documents {
		values = append(values, document)
	}
	return c.collection.InsertMany(contextOrBackground(ctx), values, opts...)
}

// FindByID decodes a document by _id.
func (c *CollectionView[T]) FindByID(ctx context.Context, id any, opts ...*options.FindOneOptions) (T, bool, error) {
	return c.FindOne(ctx, bson.M{"_id": id}, opts...)
}

// FindOne decodes one matching document.
func (c *CollectionView[T]) FindOne(ctx context.Context, filter any, opts ...*options.FindOneOptions) (T, bool, error) {
	var value T
	if err := c.requireCollection(); err != nil {
		return value, false, err
	}
	err := c.collection.FindOne(contextOrBackground(ctx), filter, opts...).Decode(&value)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	return value, true, nil
}

// Find returns a MongoDB cursor for matching documents.
func (c *CollectionView[T]) Find(ctx context.Context, filter any, opts ...*options.FindOptions) (*mongo.Cursor, error) {
	if err := c.requireCollection(); err != nil {
		return nil, err
	}
	return c.collection.Find(contextOrBackground(ctx), filter, opts...)
}

// FindAll returns all documents matching filter.
func (c *CollectionView[T]) FindAll(ctx context.Context, filter any, opts ...*options.FindOptions) ([]T, error) {
	cursor, err := c.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(contextOrBackground(ctx))
	values := make([]T, 0)
	if err := cursor.All(contextOrBackground(ctx), &values); err != nil {
		return nil, err
	}
	return values, nil
}

// UpdateByID updates one document by _id.
func (c *CollectionView[T]) UpdateByID(ctx context.Context, id any, update any, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	if err := c.requireCollection(); err != nil {
		return nil, err
	}
	return c.collection.UpdateOne(contextOrBackground(ctx), bson.M{"_id": id}, update, opts...)
}

// DeleteByID deletes one document by _id.
func (c *CollectionView[T]) DeleteByID(ctx context.Context, id any, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	if err := c.requireCollection(); err != nil {
		return nil, err
	}
	return c.collection.DeleteOne(contextOrBackground(ctx), bson.M{"_id": id}, opts...)
}

// Count counts matching documents.
func (c *CollectionView[T]) Count(ctx context.Context, filter any, opts ...*options.CountOptions) (int64, error) {
	if err := c.requireCollection(); err != nil {
		return 0, err
	}
	return c.collection.CountDocuments(contextOrBackground(ctx), filter, opts...)
}

func (c *CollectionView[T]) requireCollection() error {
	if c == nil || c.client == nil || c.client.database == nil {
		return fmt.Errorf("%w: nil collection", ErrInvalidConfig)
	}
	if c.name == "" {
		return fmt.Errorf("%w: missing collection", ErrInvalidRequest)
	}
	if c.collection == nil {
		return fmt.Errorf("%w: nil collection", ErrInvalidConfig)
	}
	return nil
}
