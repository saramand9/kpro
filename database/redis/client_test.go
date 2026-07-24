package redisstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/saramand9/kpro/database/common/lifecycle"
)

type cacheUser struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestCacheSetGetDelete(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	cache := NewCache[cacheUser](client, "users:")
	if err := cache.Set(ctx, "ada", cacheUser{Name: "Ada", Age: 37}, time.Minute); err != nil {
		t.Fatal(err)
	}
	user, ok, err := cache.Get(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || user.Name != "Ada" || user.Age != 37 {
		t.Fatalf("user=%#v ok=%v", user, ok)
	}
	if err := cache.Delete(ctx, "ada"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = cache.Get(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestLockAcquireRefreshRelease(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	lock := NewLock(client, "locks:job", time.Minute)
	if lock.Key() != "test:locks:job" {
		t.Fatalf("lock key=%q, want prefixed key", lock.Key())
	}
	if ok, err := lock.Acquire(ctx); err != nil || !ok {
		t.Fatalf("acquire ok=%v err=%v", ok, err)
	}
	other := NewLock(client, "locks:job", time.Minute)
	if _, err := other.Acquire(ctx); !errors.Is(err, ErrLockNotAcquired) {
		t.Fatalf("second acquire err=%v, want ErrLockNotAcquired", err)
	}
	if ok, err := lock.Refresh(ctx, 2*time.Minute); err != nil || !ok {
		t.Fatalf("refresh ok=%v err=%v", ok, err)
	}
	if ok, err := lock.Release(ctx); err != nil || !ok {
		t.Fatalf("release ok=%v err=%v", ok, err)
	}
}

func TestLockCannotAcquireAgainAfterRelease(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	lock := NewLock(client, "locks:single-acquisition", time.Minute)
	if got := lock.Token(); got != "" {
		t.Fatalf("token before acquisition=%q, want empty", got)
	}
	if ok, err := lock.Acquire(ctx); err != nil || !ok {
		t.Fatalf("acquire ok=%v err=%v", ok, err)
	}
	token := lock.Token()
	if token == "" {
		t.Fatal("acquisition token is empty")
	}
	if ok, err := lock.Release(ctx); err != nil || !ok {
		t.Fatalf("release ok=%v err=%v", ok, err)
	}

	if ok, err := lock.Acquire(ctx); ok || !errors.Is(err, ErrLockAlreadyAcquired) {
		t.Fatalf("reacquire ok=%v err=%v, want ErrLockAlreadyAcquired", ok, err)
	}
	if got := lock.Token(); got != token {
		t.Fatalf("token after reacquire=%q, want immutable token %q", got, token)
	}
}

func TestExpiredLockCanOnlyBeReacquiredByNewObject(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	const ttl = time.Second
	expired := NewLock(client, "locks:expired", ttl)
	if ok, err := expired.Acquire(ctx); err != nil || !ok {
		t.Fatalf("acquire expired lock ok=%v err=%v", ok, err)
	}
	server.FastForward(ttl + time.Millisecond)

	if ok, err := expired.Acquire(ctx); ok || !errors.Is(err, ErrLockAlreadyAcquired) {
		t.Fatalf("expired object reacquire ok=%v err=%v, want ErrLockAlreadyAcquired", ok, err)
	}
	replacement := NewLock(client, "locks:expired", time.Minute)
	if ok, err := replacement.Acquire(ctx); err != nil || !ok {
		t.Fatalf("replacement acquire ok=%v err=%v", ok, err)
	}
	if replacement.Token() == expired.Token() {
		t.Fatalf("replacement reused expired acquisition token %q", replacement.Token())
	}
}

func TestDelayedReleaseDoesNotDeleteReplacementLease(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	const ttl = time.Second
	expired := NewLock(client, "locks:replacement", ttl)
	if ok, err := expired.Acquire(ctx); err != nil || !ok {
		t.Fatalf("acquire expired lock ok=%v err=%v", ok, err)
	}
	server.FastForward(ttl + time.Millisecond)

	replacement := NewLock(client, "locks:replacement", time.Minute)
	if ok, err := replacement.Acquire(ctx); err != nil || !ok {
		t.Fatalf("replacement acquire ok=%v err=%v", ok, err)
	}
	if ok, err := expired.Release(ctx); ok || !errors.Is(err, ErrLockTokenMismatch) {
		t.Fatalf("delayed release ok=%v err=%v, want ErrLockTokenMismatch", ok, err)
	}
	storedToken, err := client.Redis().Get(ctx, replacement.Key()).Result()
	if err != nil {
		t.Fatal(err)
	}
	if storedToken != replacement.Token() {
		t.Fatalf("stored token=%q, want replacement token %q", storedToken, replacement.Token())
	}
}

func TestLockConcurrentRefreshIsRaceSafe(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	lock := client.Lock("locks:concurrent", time.Minute)
	if ok, err := lock.Acquire(ctx); err != nil || !ok {
		t.Fatalf("acquire ok=%v err=%v", ok, err)
	}

	const workers = 8
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			_, err := lock.Refresh(ctx, time.Duration(worker+1)*time.Minute)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestResourceHealth(t *testing.T) {
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	health := client.Health(context.Background())
	if health.Status != lifecycle.StatusOK {
		t.Fatalf("health status=%s", health.Status)
	}
}

func openTestRedis(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client, err := Open(context.Background(), Config{
		Addr:      server.Addr(),
		KeyPrefix: "test:",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}
