package projector

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/eventing/delivery"
	"github.com/saramand9/bignet/kpro/eventing/events"
)

type fakeDeliveryStore struct {
	records []delivery.Record
	acked   []string
	failed  []string
}

func (s *fakeDeliveryStore) Append(context.Context, ...events.Envelope) error {
	return nil
}

func (s *fakeDeliveryStore) Fetch(_ context.Context, consumer string, _ int) ([]delivery.Record, error) {
	for i := range s.records {
		s.records[i].Consumer = consumer
	}
	return s.records, nil
}

func (s *fakeDeliveryStore) Ack(_ context.Context, _ string, eventID string) error {
	s.acked = append(s.acked, eventID)
	return nil
}

func (s *fakeDeliveryStore) Fail(_ context.Context, _ string, eventID string, _ error) error {
	s.failed = append(s.failed, eventID)
	return nil
}

type testProjection struct {
	name string
	err  error
}

func (p testProjection) Name() string { return p.name }

func (p testProjection) Handle(context.Context, events.Envelope) error {
	return p.err
}

func TestRunnerAcksHandledEvents(t *testing.T) {
	store := &fakeDeliveryStore{
		records: []delivery.Record{{Envelope: events.Envelope{ID: "f1"}}},
	}
	runner := NewRunner(store, testProjection{name: "cache"})

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.acked) != 1 || store.acked[0] != "f1" {
		t.Fatalf("expected f1 acked, got %#v", store.acked)
	}
	if len(store.failed) != 0 {
		t.Fatalf("expected no failed events, got %#v", store.failed)
	}
}

func TestRunnerFailsUnhandledEvents(t *testing.T) {
	store := &fakeDeliveryStore{
		records: []delivery.Record{{Envelope: events.Envelope{ID: "f1"}}},
	}
	runner := NewRunner(store, testProjection{name: "cache", err: errors.New("boom")})

	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected runner error")
	}
	if len(store.failed) != 1 || store.failed[0] != "f1" {
		t.Fatalf("expected f1 failed, got %#v", store.failed)
	}
}

type fencedDeliveryStore struct {
	fakeDeliveryStore
	ackAttempts  []int
	failAttempts []int
}

type renewableDeliveryStore struct {
	fencedDeliveryStore
	renewals    atomic.Int32
	renewed     chan struct{}
	renewErr    error
	leaseLength time.Duration
}

type batchRenewableDeliveryStore struct {
	renewableDeliveryStore
	batchRenewals atomic.Int32
	batchCalls    chan int
}

func (s *batchRenewableDeliveryStore) RenewAttempts(_ context.Context, attempts []delivery.LeaseAttempt) ([]delivery.LeaseRenewal, error) {
	s.batchRenewals.Add(1)
	if s.batchCalls != nil {
		select {
		case s.batchCalls <- len(attempts):
		default:
		}
	}
	results := make([]delivery.LeaseRenewal, len(attempts))
	for i, attempt := range attempts {
		results[i] = delivery.LeaseRenewal{LeaseAttempt: attempt, Remaining: s.leaseLength}
	}
	return results, nil
}

func (s *renewableDeliveryStore) RenewAttempt(context.Context, string, string, int) (time.Duration, error) {
	count := s.renewals.Add(1)
	if s.renewErr != nil {
		return 0, s.renewErr
	}
	if count >= 2 && s.renewed != nil {
		select {
		case s.renewed <- struct{}{}:
		default:
		}
	}
	return s.leaseLength, nil
}

func (s *fencedDeliveryStore) AckAttempt(_ context.Context, _ string, _ string, attempt int) error {
	s.ackAttempts = append(s.ackAttempts, attempt)
	return nil
}

func (s *fencedDeliveryStore) FailAttempt(_ context.Context, _ string, _ string, attempt int, _ error) error {
	s.failAttempts = append(s.failAttempts, attempt)
	return nil
}

func TestRunnerUsesAttemptAwareSettlement(t *testing.T) {
	successStore := &fencedDeliveryStore{fakeDeliveryStore: fakeDeliveryStore{
		records: []delivery.Record{{Envelope: events.Envelope{ID: "fenced-ack"}, Attempts: 3}},
	}}
	if err := NewRunner(successStore, testProjection{name: "cache"}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(successStore.ackAttempts) != 1 || successStore.ackAttempts[0] != 3 || len(successStore.acked) != 0 {
		t.Fatalf("attempt acks=%#v legacy acks=%#v", successStore.ackAttempts, successStore.acked)
	}

	failStore := &fencedDeliveryStore{fakeDeliveryStore: fakeDeliveryStore{
		records: []delivery.Record{{Envelope: events.Envelope{ID: "fenced-fail"}, Attempts: 4}},
	}}
	if err := NewRunner(failStore, testProjection{name: "cache", err: errors.New("boom")}).RunOnce(context.Background()); err == nil {
		t.Fatal("expected handler error")
	}
	if len(failStore.failAttempts) != 1 || failStore.failAttempts[0] != 4 || len(failStore.failed) != 0 {
		t.Fatalf("attempt failures=%#v legacy failures=%#v", failStore.failAttempts, failStore.failed)
	}
}

func TestRunnerRenewsLeaseBeforeHandling(t *testing.T) {
	store := &renewableDeliveryStore{
		fencedDeliveryStore: fencedDeliveryStore{fakeDeliveryStore: fakeDeliveryStore{
			records: []delivery.Record{{
				Envelope: events.Envelope{ID: "renewed"},
				Attempts: 1,
			}},
		}},
		leaseLength: time.Second,
	}
	if err := NewRunner(store, testProjection{name: "cache"}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if renewals := store.renewals.Load(); renewals != 1 {
		t.Fatalf("renewals=%d, want 1", renewals)
	}
	if len(store.ackAttempts) != 1 {
		t.Fatalf("attempt acks=%#v", store.ackAttempts)
	}
}

func TestRunnerPrefersBatchLeaseRenewalCapability(t *testing.T) {
	store := &batchRenewableDeliveryStore{renewableDeliveryStore: renewableDeliveryStore{
		fencedDeliveryStore: fencedDeliveryStore{fakeDeliveryStore: fakeDeliveryStore{
			records: []delivery.Record{{
				Envelope: events.Envelope{ID: "batch-renewed"},
				Attempts: 1,
			}},
		}},
		leaseLength: time.Second,
	}}
	if err := NewRunner(store, testProjection{name: "cache"}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := store.batchRenewals.Load(); calls != 1 {
		t.Fatalf("batch renewals=%d, want 1", calls)
	}
	if calls := store.renewals.Load(); calls != 0 {
		t.Fatalf("scalar renewals=%d, want 0", calls)
	}
}

func TestRunnerBatchRenewalProtectsQueuedRecords(t *testing.T) {
	store := &batchRenewableDeliveryStore{
		renewableDeliveryStore: renewableDeliveryStore{
			fencedDeliveryStore: fencedDeliveryStore{fakeDeliveryStore: fakeDeliveryStore{
				records: []delivery.Record{
					{Envelope: events.Envelope{ID: "slow-first"}, Attempts: 1},
					{Envelope: events.Envelope{ID: "queued-second"}, Attempts: 1},
				},
			}},
			leaseLength: 20 * time.Millisecond,
		},
		batchCalls: make(chan int, 8),
	}
	projection := projectionFunc{name: "cache", handle: func(ctx context.Context, envelope events.Envelope) error {
		if envelope.ID != "slow-first" {
			return nil
		}
		seenInitial := false
		for {
			select {
			case size := <-store.batchCalls:
				if size != 2 {
					continue
				}
				if seenInitial {
					return nil
				}
				seenInitial = true
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(time.Second):
				return errors.New("queued leases were not batch-renewed")
			}
		}
	}}
	if err := NewRunner(store, projection).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := store.batchRenewals.Load(); calls < 2 {
		t.Fatalf("batch renewals=%d, want initial plus maintenance renewal", calls)
	}
	if len(store.ackAttempts) != 2 {
		t.Fatalf("attempt acks=%#v", store.ackAttempts)
	}
}

func TestRunnerReusesBatchLeaseWindowForFastRecords(t *testing.T) {
	records := make([]delivery.Record, 32)
	for i := range records {
		records[i] = delivery.Record{Envelope: events.Envelope{ID: fmt.Sprintf("fast-%d", i)}, Attempts: 1}
	}
	store := &batchRenewableDeliveryStore{renewableDeliveryStore: renewableDeliveryStore{
		fencedDeliveryStore: fencedDeliveryStore{fakeDeliveryStore: fakeDeliveryStore{records: records}},
		leaseLength:         time.Minute,
	}}
	if err := NewRunner(store, testProjection{name: "cache"}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := store.batchRenewals.Load(); calls != 1 {
		t.Fatalf("batch renewals=%d, want one O(N) batch renewal", calls)
	}
	if got := len(store.ackAttempts); got != len(records) {
		t.Fatalf("attempt acks=%d, want %d", got, len(records))
	}
}

func TestRunnerRenewsLeaseWhileHandling(t *testing.T) {
	store := &renewableDeliveryStore{
		fencedDeliveryStore: fencedDeliveryStore{fakeDeliveryStore: fakeDeliveryStore{
			records: []delivery.Record{{
				Envelope: events.Envelope{ID: "long-running"},
				Attempts: 2,
			}},
		}},
		renewed:     make(chan struct{}, 1),
		leaseLength: 20 * time.Millisecond,
	}
	projection := projectionFunc{name: "cache", handle: func(ctx context.Context, _ events.Envelope) error {
		select {
		case <-store.renewed:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(time.Second):
			return errors.New("lease was not renewed while handling")
		}
	}}

	if err := NewRunner(store, projection).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if renewals := store.renewals.Load(); renewals < 2 {
		t.Fatalf("renewals=%d, want at least 2", renewals)
	}
	if len(store.ackAttempts) != 1 || store.ackAttempts[0] != 2 {
		t.Fatalf("attempt acks=%#v", store.ackAttempts)
	}
}

func TestRunnerDoesNotHandleAfterLeaseRenewalFails(t *testing.T) {
	leaseErr := errors.New("renew failed")
	store := &renewableDeliveryStore{
		fencedDeliveryStore: fencedDeliveryStore{fakeDeliveryStore: fakeDeliveryStore{
			records: []delivery.Record{{
				Envelope: events.Envelope{ID: "lost"},
				Attempts: 1,
			}},
		}},
		renewErr: leaseErr,
	}
	handled := 0
	projection := projectionFunc{name: "cache", handle: func(context.Context, events.Envelope) error {
		handled++
		return nil
	}}
	err := NewRunner(store, projection).RunOnce(context.Background())
	if !errors.Is(err, leaseErr) {
		t.Fatalf("RunOnce error=%v, want %v", err, leaseErr)
	}
	if handled != 0 || len(store.ackAttempts) != 0 || len(store.failAttempts) != 0 {
		t.Fatalf("handled=%d acks=%#v fails=%#v", handled, store.ackAttempts, store.failAttempts)
	}
}

type projectionFunc struct {
	name   string
	handle func(context.Context, events.Envelope) error
}

func (p projectionFunc) Name() string { return p.name }

func (p projectionFunc) Handle(ctx context.Context, envelope events.Envelope) error {
	return p.handle(ctx, envelope)
}

type loopingDeliveryStore struct {
	fakeDeliveryStore
	fetches int
	cancel  context.CancelFunc
}

func (s *loopingDeliveryStore) Fetch(_ context.Context, consumer string, _ int) ([]delivery.Record, error) {
	s.fetches++
	switch s.fetches {
	case 1:
		return nil, errors.New("temporary fetch failure")
	case 2:
		return nil, nil
	default:
		return []delivery.Record{{Envelope: events.Envelope{ID: "eventual"}, Consumer: consumer}}, nil
	}
}

func (s *loopingDeliveryStore) Ack(_ context.Context, _ string, eventID string) error {
	s.acked = append(s.acked, eventID)
	s.cancel()
	return nil
}

func TestRunnerRunRetriesBacksOffAndCancels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &loopingDeliveryStore{cancel: cancel}
	var observed []error
	runner := NewRunner(store, testProjection{name: "cache"}).
		WithBackoff(time.Millisecond, time.Millisecond).
		WithErrorHandler(func(err error) { observed = append(observed, err) })

	err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error=%v, want context.Canceled", err)
	}
	if store.fetches != 3 {
		t.Fatalf("fetches=%d, want 3", store.fetches)
	}
	if len(store.acked) != 1 || store.acked[0] != "eventual" {
		t.Fatalf("acked=%#v", store.acked)
	}
	if len(observed) != 1 || observed[0] == nil {
		t.Fatalf("observed errors=%#v", observed)
	}
}

func TestRunnerRetryPolicyAndSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &loopingDeliveryStore{cancel: cancel}
	var attempts []int
	runner := NewRunner(store, testProjection{name: "cache"}).
		WithBackoff(time.Millisecond, time.Hour).
		WithRetryPolicy(RetryPolicyFunc(func(consecutiveErrors int, err error) time.Duration {
			if err == nil {
				t.Fatal("retry policy received nil error")
			}
			attempts = append(attempts, consecutiveErrors)
			return time.Millisecond
		}))

	err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error=%v, want context.Canceled", err)
	}
	if len(attempts) != 1 || attempts[0] != 1 {
		t.Fatalf("retry attempts=%#v", attempts)
	}
	snapshot := runner.Snapshot()
	if snapshot.Running || snapshot.TotalPasses != 3 || snapshot.TotalProcessed != 1 || snapshot.TotalErrors != 1 || snapshot.ConsecutiveErrors != 0 || snapshot.LastSuccessAt.IsZero() || snapshot.StoppedAt.IsZero() {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if err := runner.Health(); err != nil {
		t.Fatalf("health after recovery=%v", err)
	}
}

func TestRunnerHealthReflectsLatestPass(t *testing.T) {
	store := &fakeDeliveryStore{records: []delivery.Record{{Envelope: events.Envelope{ID: "health"}}}}
	failing := atomic.Bool{}
	failing.Store(true)
	projection := projectionFunc{name: "health", handle: func(context.Context, events.Envelope) error {
		if failing.Load() {
			return errors.New("unhealthy projection")
		}
		return nil
	}}
	runner := NewRunner(store, projection)
	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected failed pass")
	}
	if err := runner.Health(); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("health=%v, want ErrUnhealthy", err)
	}
	failing.Store(false)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runner.Health(); err != nil {
		t.Fatalf("recovered health=%v", err)
	}
	snapshot := runner.Snapshot()
	if snapshot.TotalPasses != 2 || snapshot.TotalProcessed != 2 || snapshot.TotalErrors != 1 || snapshot.LastError != "" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}
