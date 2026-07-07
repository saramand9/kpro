package truth

// Aggregate identifies state that owns business invariants.
type Aggregate interface {
	AggregateID() string
	AggregateVersion() int64
}

// VersionConflict reports an optimistic concurrency mismatch.
type VersionConflict struct {
	AggregateID string
	Expected    int64
	Actual      int64
}

// Error implements error.
func (e VersionConflict) Error() string {
	return "aggregate version conflict"
}
