package limiter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestCheck_AllowWithinLimit(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: getRedisAddr(),
	})
	defer rdb.Close()

	ctx := context.Background()
	key := "test:allow:" + t.Name()
	defer rdb.Del(ctx, key)

	engine := New(rdb)
	limit := 5
	window := 10 * time.Second

	for i := 0; i < limit; i++ {
		result, err := engine.Check(ctx, key, limit, window)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !result.Allowed {
			t.Fatalf("Request %d should be allowed", i+1)
		}
		expectedRemaining := int64(limit - i - 1)
		if result.Remaining != expectedRemaining {
			t.Errorf("Expected remaining %d, got %d", expectedRemaining, result.Remaining)
		}
	}
}

func TestCheck_DenyOverLimit(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: getRedisAddr(),
	})
	defer rdb.Close()

	ctx := context.Background()
	key := "test:deny:" + t.Name()
	defer rdb.Del(ctx, key)

	engine := New(rdb)
	limit := 3
	window := 10 * time.Second

	// Fill up the limit
	for i := 0; i < limit; i++ {
		_, err := engine.Check(ctx, key, limit, window)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
	}

	// Next request should be denied
	result, err := engine.Check(ctx, key, limit, window)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.Allowed {
		t.Fatal("Request over limit should be denied")
	}
	if result.Remaining != 0 {
		t.Errorf("Expected remaining 0, got %d", result.Remaining)
	}
	if result.RetryAfter <= 0 {
		t.Errorf("Expected positive RetryAfter, got %v", result.RetryAfter)
	}
}

func TestCheck_ConcurrentSafety(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: getRedisAddr(),
	})
	defer rdb.Close()

	ctx := context.Background()
	key := "test:concurrent:" + t.Name()
	defer rdb.Del(ctx, key)

	engine := New(rdb)
	limit := 100
	window := 60 * time.Second

	concurrency := 200
	results := make(chan bool, concurrency)
	var wg sync.WaitGroup

	// Fire concurrent requests
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			result, err := engine.Check(ctx, key, limit, window)
			if err != nil {
				t.Errorf("Concurrent check failed: %v", err)
				results <- false
				return
			}
			results <- result.Allowed
		}()
	}

	// Wait for all goroutines then close results channel
	go func() {
		wg.Wait()
		close(results)
	}()

	// Count allowed requests
	allowed := 0
	for allowedResult := range results {
		if allowedResult {
			allowed++
		}
	}

	if allowed > limit {
		t.Errorf("Race condition! Allowed %d requests but limit is %d", allowed, limit)
	}
	if allowed != limit {
		t.Logf("Allowed %d out of %d concurrent requests (limit: %d)", allowed, concurrency, limit)
	}
}

func TestCheck_WindowExpiry(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: getRedisAddr(),
	})
	defer rdb.Close()

	ctx := context.Background()
	key := "test:expiry:" + t.Name()
	defer rdb.Del(ctx, key)

	engine := New(rdb)
	limit := 2
	window := 1 * time.Second

	// Fill the limit
	for i := 0; i < limit; i++ {
		_, err := engine.Check(ctx, key, limit, window)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
	}

	// Should be denied
	result, err := engine.Check(ctx, key, limit, window)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.Allowed {
		t.Fatal("Should be denied")
	}

	// Wait for window to expire
	time.Sleep(window + 200*time.Millisecond)

	// Should be allowed again
	result, err = engine.Check(ctx, key, limit, window)
	if err != nil {
		t.Fatalf("Check after expiry failed: %v", err)
	}
	if !result.Allowed {
		t.Fatal("Should be allowed after window expiry")
	}
}

func getRedisAddr() string {
	return "localhost:6379"
}