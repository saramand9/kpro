package neo4jstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
)

// Client owns a Neo4j driver and exposes graph helpers.
type Client struct {
	name     string
	driver   neo4j.DriverWithContext
	owns     bool
	database string
}

var _ lifecycle.Resource = (*Client)(nil)

// Open creates a Neo4j adapter and verifies connectivity unless skipped.
func Open(ctx context.Context, config Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	driver := config.Driver
	owns := false
	if driver == nil {
		driver, err = neo4j.NewDriverWithContext(config.URI, authToken(config), config.Config...)
		if err != nil {
			return nil, err
		}
		owns = true
	}

	client := &Client{name: config.Name, driver: driver, owns: owns, database: config.Database}
	if !config.SkipVerifyConnection {
		verifyCtx, cancel := context.WithTimeout(ctx, config.VerifyTimeout)
		defer cancel()
		if err := client.Ping(verifyCtx); err != nil {
			if owns {
				_ = driver.Close(context.Background())
			}
			return nil, err
		}
	}
	return client, nil
}

// Driver returns the underlying official Neo4j driver.
func (c *Client) Driver() neo4j.DriverWithContext {
	if c == nil {
		return nil
	}
	return c.driver
}

// Database returns the configured Neo4j database name.
func (c *Client) Database() string {
	if c == nil {
		return ""
	}
	return c.database
}

// Session creates a Neo4j session with the configured default database.
func (c *Client) Session(ctx context.Context, config neo4j.SessionConfig) (neo4j.SessionWithContext, error) {
	if c == nil || c.driver == nil {
		return nil, fmt.Errorf("%w: nil driver", ErrInvalidConfig)
	}
	if config.DatabaseName == "" {
		config.DatabaseName = c.database
	}
	return c.driver.NewSession(contextOrBackground(ctx), config), nil
}

// Execute runs a Cypher query through neo4j.ExecuteQuery.
func (c *Client) Execute(ctx context.Context, cypher string, params map[string]any, options ...neo4j.ExecuteQueryConfigurationOption) (*neo4j.EagerResult, error) {
	if c == nil || c.driver == nil {
		return nil, fmt.Errorf("%w: nil driver", ErrInvalidConfig)
	}
	queryOptions := make([]neo4j.ExecuteQueryConfigurationOption, 0, len(options)+1)
	if c.database != "" {
		queryOptions = append(queryOptions, neo4j.ExecuteQueryWithDatabase(c.database))
	}
	queryOptions = append(queryOptions, options...)
	return neo4j.ExecuteQuery(contextOrBackground(ctx), c.driver, cypher, params, neo4j.EagerResultTransformer, queryOptions...)
}

// QueryRaw runs a caller-owned Cypher query and returns eager records.
func (c *Client) QueryRaw(ctx context.Context, cypher string, params map[string]any, options ...neo4j.ExecuteQueryConfigurationOption) (*neo4j.EagerResult, error) {
	if strings.TrimSpace(cypher) == "" {
		return nil, fmt.Errorf("%w: missing cypher", ErrInvalidRequest)
	}
	return c.Execute(ctx, cypher, params, options...)
}

// WriteTransaction runs multiple graph writes in one Neo4j managed transaction.
func (c *Client) WriteTransaction(ctx context.Context, fn func(*Graph) error) error {
	if c == nil || c.driver == nil {
		return fmt.Errorf("%w: nil driver", ErrInvalidConfig)
	}
	if fn == nil {
		return fmt.Errorf("%w: nil transaction function", ErrInvalidRequest)
	}
	session, err := c.Session(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	if err != nil {
		return err
	}
	defer session.Close(contextOrBackground(ctx))
	_, err = neo4j.ExecuteWrite(contextOrBackground(ctx), session, func(tx neo4j.ManagedTransaction) (struct{}, error) {
		graph := &Graph{executor: txGraphExecutor{ctx: contextOrBackground(ctx), tx: tx}}
		return struct{}{}, fn(graph)
	})
	return err
}

// Graph returns a read-model graph helper.
func (c *Client) Graph() *Graph {
	return &Graph{executor: c}
}

// Name implements lifecycle.Resource.
func (c *Client) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// Kind implements lifecycle.Resource.
func (c *Client) Kind() lifecycle.Kind {
	return lifecycle.KindGraph
}

// Ping implements lifecycle.Resource.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.driver == nil {
		return fmt.Errorf("%w: nil driver", ErrInvalidConfig)
	}
	return c.driver.VerifyConnectivity(contextOrBackground(ctx))
}

// Health implements lifecycle.Resource.
func (c *Client) Health(ctx context.Context) lifecycle.Health {
	health := lifecycle.CheckHealth(ctx, c)
	if c != nil && c.database != "" {
		health.Details = map[string]any{"database": c.database}
	}
	return health
}

// Close implements lifecycle.Resource.
func (c *Client) Close() error {
	return c.Shutdown(context.Background())
}

// Shutdown releases the Neo4j driver when this adapter opened it.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil || c.driver == nil || !c.owns {
		return nil
	}
	return c.driver.Close(contextOrBackground(ctx))
}

func (c *Client) executeWrite(ctx context.Context, cypher string, params map[string]any) error {
	session, err := c.Session(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	if err != nil {
		return err
	}
	defer session.Close(contextOrBackground(ctx))
	_, err = neo4j.ExecuteWrite(contextOrBackground(ctx), session, func(tx neo4j.ManagedTransaction) (struct{}, error) {
		result, err := tx.Run(contextOrBackground(ctx), cypher, params)
		if err != nil {
			return struct{}{}, err
		}
		_, err = result.Consume(contextOrBackground(ctx))
		return struct{}{}, err
	})
	return err
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

type txGraphExecutor struct {
	ctx context.Context
	tx  neo4j.ManagedTransaction
}

func (e txGraphExecutor) executeWrite(ctx context.Context, cypher string, params map[string]any) error {
	if e.tx == nil {
		return fmt.Errorf("%w: nil transaction", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = e.ctx
	}
	result, err := e.tx.Run(contextOrBackground(ctx), cypher, params)
	if err != nil {
		return err
	}
	_, err = result.Consume(contextOrBackground(ctx))
	return err
}
