package projector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/saramand9/bignet/kpro/eventing/delivery"
)

// RetryPolicy chooses the delay after a failed runner pass. ConsecutiveErrors
// starts at one and resets after any successful pass.
type RetryPolicy interface {
	RetryDelay(consecutiveErrors int, err error) time.Duration
}

// RetryPolicyFunc adapts a function to RetryPolicy.
type RetryPolicyFunc func(consecutiveErrors int, err error) time.Duration

// RetryDelay implements RetryPolicy.
func (f RetryPolicyFunc) RetryDelay(consecutiveErrors int, err error) time.Duration {
	if f == nil {
		return 0
	}
	return f(consecutiveErrors, err)
}

// Snapshot is a race-safe point-in-time view of runner activity. LastError is
// cleared by the next successful pass; TotalErrors remains cumulative.
type Snapshot struct {
	Running           bool
	StartedAt         time.Time
	StoppedAt         time.Time
	LastPassAt        time.Time
	LastSuccessAt     time.Time
	LastErrorAt       time.Time
	LastError         string
	ConsecutiveErrors int
	TotalPasses       uint64
	TotalProcessed    uint64
	TotalErrors       uint64
}

// ErrUnhealthy means the runner's most recent pass failed.
var ErrUnhealthy = errors.New("projector runner unhealthy")

const (
	defaultIdleDelay  = 100 * time.Millisecond
	defaultErrorDelay = time.Second
)

// Runner pulls events from a delivery store and applies registered projectors.
type Runner struct {
	store       delivery.Store
	projectors  []Projector
	batchSize   int
	idleDelay   time.Duration
	errorDelay  time.Duration
	retryPolicy RetryPolicy
	onError     func(error)

	stateMu sync.RWMutex
	state   Snapshot
	runs    int
}

// NewRunner creates a projector runner.
func NewRunner(store delivery.Store, projectors ...Projector) *Runner {
	return &Runner{
		store:      store,
		projectors: append([]Projector(nil), projectors...),
		batchSize:  100,
		idleDelay:  defaultIdleDelay,
		errorDelay: defaultErrorDelay,
	}
}

// WithBatchSize sets the max records fetched per projector pass.
func (r *Runner) WithBatchSize(batchSize int) *Runner {
	if batchSize > 0 {
		r.batchSize = batchSize
	}
	return r
}

// WithBackoff configures delays after an empty pass and a failed pass.
func (r *Runner) WithBackoff(idleDelay, errorDelay time.Duration) *Runner {
	if idleDelay > 0 {
		r.idleDelay = idleDelay
	}
	if errorDelay > 0 {
		r.errorDelay = errorDelay
	}
	return r
}

// WithErrorHandler observes errors encountered by Run before it retries.
func (r *Runner) WithErrorHandler(handler func(error)) *Runner {
	r.onError = handler
	return r
}

// WithRetryPolicy configures delays after failed passes. A nil policy restores
// the fixed error delay configured by WithBackoff.
func (r *Runner) WithRetryPolicy(policy RetryPolicy) *Runner {
	r.retryPolicy = policy
	return r
}

// Snapshot returns a race-safe copy of runner health and progress counters.
func (r *Runner) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.state
}

// Health returns nil when configuration is valid and the latest pass
// succeeded. A stopped or not-yet-started runner is not unhealthy by itself;
// callers can distinguish those states with Snapshot().Running.
func (r *Runner) Health() error {
	if err := r.validate(); err != nil {
		return err
	}
	snapshot := r.Snapshot()
	if snapshot.ConsecutiveErrors > 0 {
		return fmt.Errorf("%w: %s", ErrUnhealthy, snapshot.LastError)
	}
	return nil
}

// RunOnce processes one batch for each projector.
func (r *Runner) RunOnce(ctx context.Context) error {
	r.beginRun()
	defer r.endRun()
	processed, err := r.runOnce(contextOrBackground(ctx))
	r.recordPass(processed, err)
	return err
}

// Run continuously processes projector batches until ctx is cancelled. It
// polls at idleDelay when no events are available and retries failed passes
// after errorDelay. Errors can be observed with WithErrorHandler.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.validate(); err != nil {
		return err
	}
	r.beginRun()
	defer r.endRun()
	ctx = contextOrBackground(ctx)
	consecutiveErrors := 0
	for {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		processed, err := r.runOnce(ctx)
		if err != nil {
			if contextErr := context.Cause(ctx); contextErr != nil {
				return contextErr
			}
		}
		r.recordPass(processed, err)
		if err != nil {
			if r.onError != nil {
				r.onError(err)
			}
			consecutiveErrors++
			delay := r.errorDelay
			if r.retryPolicy != nil {
				delay = r.retryPolicy.RetryDelay(consecutiveErrors, err)
				if delay < 0 {
					delay = 0
				}
			}
			if err := wait(ctx, delay); err != nil {
				return err
			}
			continue
		}
		consecutiveErrors = 0
		if processed == 0 {
			if err := wait(ctx, r.idleDelay); err != nil {
				return err
			}
		}
	}
}

func (r *Runner) runOnce(ctx context.Context) (int, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	processed := 0
	var errs []error
	attemptStore, fenced := r.store.(delivery.AttemptStore)
	renewableStore, renewable := r.store.(delivery.RenewableAttemptStore)
	for _, projector := range r.projectors {
		if err := context.Cause(ctx); err != nil {
			return processed, errors.Join(append(errs, err)...)
		}
		if projector == nil {
			continue
		}
		name := projector.Name()
		records, err := r.store.Fetch(ctx, name, r.batchSize)
		if err != nil {
			errs = append(errs, fmt.Errorf("fetch projector %q: %w", name, err))
			continue
		}
		processed += len(records)
		var batchLeaseDeadline time.Time
		_, batchRenewable := renewableStore.(delivery.BatchRenewableAttemptStore)
		for i, record := range records {
			var handleErr error
			var renewErr error
			if renewable {
				deadline := time.Time{}
				if batchRenewable {
					deadline = batchLeaseDeadline
				}
				handleErr, renewErr, deadline = handleWithLeaseRenewal(ctx, renewableStore, projector, record, records[i:], deadline)
				if batchRenewable {
					batchLeaseDeadline = deadline
				}
			} else {
				handleErr = projector.Handle(ctx, record.Envelope)
			}
			if handleErr != nil {
				errs = append(errs, fmt.Errorf("handle projector %q event %q: %w", name, record.Envelope.ID, handleErr))
			}
			if renewErr != nil {
				errs = append(errs, fmt.Errorf("renew projector %q event %q: %w", name, record.Envelope.ID, renewErr))
				continue
			}
			if handleErr != nil {
				var failErr error
				if fenced {
					failErr = attemptStore.FailAttempt(ctx, name, record.Envelope.ID, record.Attempts, handleErr)
				} else {
					failErr = r.store.Fail(ctx, name, record.Envelope.ID, handleErr)
				}
				if failErr != nil {
					errs = append(errs, fmt.Errorf("fail projector %q event %q: %w", name, record.Envelope.ID, failErr))
				}
				continue
			}
			var ackErr error
			if fenced {
				ackErr = attemptStore.AckAttempt(ctx, name, record.Envelope.ID, record.Attempts)
			} else {
				ackErr = r.store.Ack(ctx, name, record.Envelope.ID)
			}
			if ackErr != nil {
				errs = append(errs, fmt.Errorf("ack projector %q event %q: %w", name, record.Envelope.ID, ackErr))
			}
		}
	}
	return processed, errors.Join(errs...)
}

func handleWithLeaseRenewal(
	ctx context.Context,
	store delivery.RenewableAttemptStore,
	projector Projector,
	record delivery.Record,
	pending []delivery.Record,
	leaseDeadline time.Time,
) (handleErr, renewErr error, nextLeaseDeadline time.Time) {
	leaseDuration := time.Until(leaseDeadline)
	if leaseDeadline.IsZero() || leaseDuration <= 2*time.Millisecond {
		var err error
		leaseDuration, err = renewLeases(ctx, store, record, pending)
		if err != nil {
			return nil, err, leaseDeadline
		}
		if leaseDuration <= 0 {
			return nil, fmt.Errorf("renewed lease duration must be positive"), leaseDeadline
		}
		leaseDeadline = time.Now().Add(leaseDuration)
	}
	nextLeaseDeadline = leaseDeadline
	leaseDuration = time.Until(leaseDeadline)
	if leaseDuration <= 0 {
		return nil, fmt.Errorf("renewed lease expired before handling began"), leaseDeadline
	}

	handleCtx, cancel := context.WithCancelCause(ctx)
	type maintenanceResult struct {
		deadline time.Time
		err      error
	}
	renewDone := make(chan maintenanceResult, 1)
	go func() {
		deadline, err := maintainLease(handleCtx, cancel, store, record, pending, leaseDuration)
		renewDone <- maintenanceResult{deadline: deadline, err: err}
	}()
	defer func() {
		cancel(context.Canceled)
		result := <-renewDone
		renewErr = result.err
		nextLeaseDeadline = result.deadline
	}()
	handleErr = projector.Handle(handleCtx, record.Envelope)
	return handleErr, nil, nextLeaseDeadline
}

func maintainLease(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	store delivery.RenewableAttemptStore,
	record delivery.Record,
	pending []delivery.Record,
	leaseDuration time.Duration,
) (time.Time, error) {
	leaseDeadline := time.Now().Add(leaseDuration)
	for {
		delay := leaseDuration / 2
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return leaseDeadline, nil
		case <-timer.C:
		}

		renewedDuration, err := renewLeases(ctx, store, record, pending)
		if err != nil {
			if context.Cause(ctx) != nil {
				return leaseDeadline, nil
			}
			cancel(err)
			return leaseDeadline, err
		}
		if renewedDuration <= 0 {
			err := fmt.Errorf("renewed lease duration must be positive")
			cancel(err)
			return leaseDeadline, err
		}
		leaseDuration = renewedDuration
		leaseDeadline = time.Now().Add(renewedDuration)
	}
}

func renewLeases(ctx context.Context, store delivery.RenewableAttemptStore, record delivery.Record, pending []delivery.Record) (time.Duration, error) {
	if batchStore, ok := store.(delivery.BatchRenewableAttemptStore); ok {
		attempts := make([]delivery.LeaseAttempt, 0, len(pending))
		for _, pendingRecord := range pending {
			attempts = append(attempts, delivery.LeaseAttempt{
				Consumer: pendingRecord.Consumer,
				EventID:  pendingRecord.Envelope.ID,
				Attempt:  pendingRecord.Attempts,
			})
		}
		results, err := batchStore.RenewAttempts(ctx, attempts)
		if err != nil {
			return 0, err
		}
		if len(results) != len(attempts) {
			return 0, fmt.Errorf("batch lease renewal returned %d results, want %d", len(results), len(attempts))
		}
		currentFound := false
		remaining := time.Duration(0)
		for i, attempt := range attempts {
			if results[i].LeaseAttempt != attempt {
				return 0, fmt.Errorf("batch lease renewal result %d does not match its request", i)
			}
			if results[i].Lost {
				return 0, fmt.Errorf("%w: consumer %q event %q attempt %d", delivery.ErrLeaseLost, attempt.Consumer, attempt.EventID, attempt.Attempt)
			}
			if results[i].Remaining <= 0 {
				return 0, fmt.Errorf("renewed lease duration must be positive")
			}
			if remaining == 0 || results[i].Remaining < remaining {
				remaining = results[i].Remaining
			}
			if attempt.Consumer == record.Consumer && attempt.EventID == record.Envelope.ID && attempt.Attempt == record.Attempts {
				currentFound = true
			}
		}
		if !currentFound {
			return 0, fmt.Errorf("current delivery is missing from batch lease renewal")
		}
		return remaining, nil
	}
	return store.RenewAttempt(ctx, record.Consumer, record.Envelope.ID, record.Attempts)
}

func (r *Runner) beginRun() {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.runs++
	r.state.Running = true
	if r.state.StartedAt.IsZero() {
		r.state.StartedAt = now
	}
	r.state.StoppedAt = time.Time{}
}

func (r *Runner) endRun() {
	if r == nil {
		return
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.runs > 0 {
		r.runs--
	}
	if r.runs == 0 {
		r.state.Running = false
		r.state.StoppedAt = time.Now().UTC()
	}
}

func (r *Runner) recordPass(processed int, err error) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.state.LastPassAt = now
	r.state.TotalPasses++
	if processed > 0 {
		r.state.TotalProcessed += uint64(processed)
	}
	if err != nil {
		r.state.LastErrorAt = now
		r.state.LastError = err.Error()
		r.state.ConsecutiveErrors++
		r.state.TotalErrors++
		return
	}
	r.state.LastSuccessAt = now
	r.state.LastError = ""
	r.state.ConsecutiveErrors = 0
}

func (r *Runner) validate() error {
	if r == nil || r.store == nil {
		return fmt.Errorf("projector runner requires a delivery store")
	}
	return nil
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
