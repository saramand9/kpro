// File: database/dbx/async.go
// Purpose: Implements bounded non-blocking database write workers.

package dbx

import (
	"context"
	"database/sql"
	"sync"
)

type asyncOperation func(context.Context) (sql.Result, error)

type asyncJob struct {
	ctx    context.Context
	op     asyncOperation
	future *Future
}

// AsyncWriter runs write operations through a bounded queue and worker pool.
type AsyncWriter struct {
	writer *Writer
	jobs   chan asyncJob

	mu     sync.RWMutex
	closed bool
	wg     sync.WaitGroup
}

// Future represents one queued async write result.
type Future struct {
	done   chan struct{}
	result sql.Result
	err    error
}

// NewAsyncWriter creates and starts an async writer.
func NewAsyncWriter(writer *Writer, config AsyncConfig) *AsyncWriter {
	if config.Workers <= 0 {
		config.Workers = defaultAsyncWorkers
	}
	if config.QueueSize <= 0 {
		config.QueueSize = defaultAsyncQueue
	}
	async := &AsyncWriter{
		writer: writer,
		jobs:   make(chan asyncJob, config.QueueSize),
	}
	for i := 0; i < config.Workers; i++ {
		async.wg.Add(1)
		go async.run()
	}
	return async
}

// Submit queues op without blocking for queue capacity.
func (a *AsyncWriter) Submit(ctx context.Context, op func(context.Context) (sql.Result, error)) (*Future, error) {
	if a == nil {
		return nil, ErrAsyncClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	future := &Future{done: make(chan struct{})}
	job := asyncJob{
		ctx:    ctx,
		op:     op,
		future: future,
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, ErrAsyncClosed
	}
	select {
	case a.jobs <- job:
		return future, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrAsyncQueueFull
	}
}

// Insert queues a generated insert.
func (a *AsyncWriter) Insert(ctx context.Context, table string, record any) (*Future, error) {
	return a.Submit(ctx, func(jobCtx context.Context) (sql.Result, error) {
		return a.writer.Insert(jobCtx, table, record)
	})
}

// Update queues a generated update.
func (a *AsyncWriter) Update(ctx context.Context, table string, record any) (*Future, error) {
	return a.Submit(ctx, func(jobCtx context.Context) (sql.Result, error) {
		return a.writer.Update(jobCtx, table, record)
	})
}

// Upsert queues a generated upsert.
func (a *AsyncWriter) Upsert(ctx context.Context, table string, record any, conflict ConflictConfig) (*Future, error) {
	return a.Submit(ctx, func(jobCtx context.Context) (sql.Result, error) {
		return a.writer.Upsert(jobCtx, table, record, conflict)
	})
}

// BatchInsert queues a generated batch insert.
func (a *AsyncWriter) BatchInsert(ctx context.Context, table string, records any) (*Future, error) {
	return a.Submit(ctx, func(jobCtx context.Context) (sql.Result, error) {
		return a.writer.BatchInsert(jobCtx, table, records)
	})
}

// Shutdown stops accepting jobs and waits for queued jobs to finish or ctx to expire.
func (a *AsyncWriter) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil {
		return nil
	}
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.jobs)
	}
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until the async write finishes or ctx expires.
func (f *Future) Wait(ctx context.Context) (sql.Result, error) {
	if f == nil {
		return nil, ErrAsyncClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-f.done:
		return f.result, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *AsyncWriter) run() {
	defer a.wg.Done()
	for job := range a.jobs {
		job.future.result, job.future.err = job.op(job.ctx)
		close(job.future.done)
	}
}
