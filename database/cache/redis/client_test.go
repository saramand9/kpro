package redisstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/saramand9/bignet/kpro/database/contracts/lifecycle"
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
