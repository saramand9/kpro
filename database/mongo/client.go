package mongostore

import (
	"context"
	"fmt"

	"github.com/saramand9/bignet/kpro/database/kernel"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// Client owns a MongoDB client and exposes document collection helpers.
type Client struct {
	name         string
	mongo        *mongo.Client
	database     *mongo.Database
	owns         bool
	databaseName string
}

var _ kernel.Resource = (*Client)(nil)

// Open creates a MongoDB adapter client and verifies it with Ping unless skipped.
func Open(ctx context.Context, config Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	mongoClient := config.Client
	owns := false
	if mongoClient == nil {
		mongoClient, err = mongo.Connect(ctx, mongoOptions(config))
		if err != nil {
			return nil, err
		}
		owns = true
	}

	client := &Client{
		name:         config.Name,
		mongo:        mongoClient,
		database:     mongoClient.Database(config.Database),
		owns:         owns,
		databaseName: config.Database,
	}
	if !config.SkipPingCheck {
		pingCtx, cancel := context.WithTimeout(ctx, config.PingTimeout)
		defer cancel()
		if err := client.Ping(pingCtx); err != nil {
			if owns {
				_ = mongoClient.Disconnect(context.Background())
			}
			return nil, err
		}
	}
	return client, nil
}

// Mongo returns the underlying official MongoDB client.
func (c *Client) Mongo() *mongo.Client {
	if c == nil {
		return nil
	}
	return c.mongo
}

// Database returns the default MongoDB database handle.
func (c *Client) Database() *mongo.Database {
	if c == nil {
		return nil
	}
	return c.database
}

// DatabaseName returns the configured default database name.
func (c *Client) DatabaseName() string {
	if c == nil {
		return ""
	}
	return c.databaseName
}

// Collection returns a typed helper for collection in the default database.
func Collection[T any](client *Client, name string) *CollectionView[T] {
	return NewCollection[T](client, name)
}

// Collection returns an untyped helper for collection in the default database.
func (c *Client) Collection(name string) *CollectionView[any] {
	return NewCollection[any](c, name)
}

// Name implements kernel.Resource.
func (c *Client) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// Kind implements kernel.Resource.
func (c *Client) Kind() kernel.Kind {
	return kernel.KindDocument
}

// Ping implements kernel.Resource.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.mongo == nil {
		return fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	return c.mongo.Ping(contextOrBackground(ctx), readpref.Primary())
}

// Health implements kernel.Resource.
func (c *Client) Health(ctx context.Context) kernel.Health {
	return kernel.CheckHealth(ctx, c)
}

// Close implements kernel.Resource.
func (c *Client) Close() error {
	return c.Shutdown(context.Background())
}

// Shutdown releases the MongoDB client when this adapter opened it.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil || c.mongo == nil || !c.owns {
		return nil
	}
	return c.mongo.Disconnect(contextOrBackground(ctx))
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
