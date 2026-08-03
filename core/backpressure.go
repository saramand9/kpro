// File: core/backpressure.go
// Purpose: Defines Router admission policies and lightweight runtime statistics.

package core

const (
	// DefaultMaxInflight caps accepted work across all sessions.
	DefaultMaxInflight = 64 * 1024
	// DefaultMaxWaitingDispatches bounds callers blocked by BackpressureWait
	// across the Router.
	DefaultMaxWaitingDispatches = 1024
	// DefaultMaxWaitingDispatchesPerSession prevents one hot session from
	// monopolizing all Router wait capacity.
	DefaultMaxWaitingDispatchesPerSession = 16
)

// BackpressurePolicy controls what DispatchContext does when an admission or
// queue limit is reached.
type BackpressurePolicy uint8

const (
	// BackpressureReject preserves the historical non-blocking behavior.
	BackpressureReject BackpressurePolicy = iota
	// BackpressureWait waits for capacity until the supplied context, Router,
	// or Session is canceled.
	BackpressureWait
)

// RouterStats is a point-in-time Router snapshot. Counters are monotonic except
// for Inflight and queue depths. Snapshot performs no per-message allocation;
// counting live sessions is proportional to the current session count.
type RouterStats struct {
	State        LifecycleState
	Backpressure BackpressurePolicy

	Inflight              int64
	MaxInflight           int
	MaxInflightPerSession int
	// WaitingDispatches is the number of callers currently blocked waiting for
	// global, per-session, sync-queue, or async-queue capacity.
	WaitingDispatches              int
	MaxWaitingDispatches           int
	MaxWaitingDispatchesPerSession int
	Sessions                       int

	// SyncQueueDepth is the sum of queued sync work across all live sessions.
	SyncQueueDepth int64
	// SyncQueueCapacityPerSession is the configured capacity of each session's
	// independent sync queue; it is not a global denominator for SyncQueueDepth.
	SyncQueueCapacityPerSession int
	AsyncQueueDepth             int64
	AsyncQueueCapacity          int

	Accepted  uint64
	Completed uint64
	Dropped   uint64
	Rejected  uint64
	Waited    uint64
	// GlobalWaitRejected counts dispatches rejected because all Router-wide
	// waiting-dispatch capacity was already in use.
	GlobalWaitRejected uint64
	// SessionWaitRejected counts dispatches rejected because the originating
	// session already held its waiting-dispatch allowance.
	SessionWaitRejected uint64
}
