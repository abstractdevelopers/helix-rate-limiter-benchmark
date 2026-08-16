package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/admin"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/config"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/limiter"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/middleware"
)

func getTestRedis() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
}

func getTestConfig(t *testing.T) *config.Manager {
	t.Helper()
	// Use a temp file so tests don't depend on working directory
	cfg := &config.Config{
		DefaultLimit: config.Limit{Requests: 100, WindowSeconds: 60},
		Clients: map[string]*config.Limit{
			"basic":   {Requests: 100, WindowSeconds: 60},
			"premium": {Requests: 1000, WindowSeconds: 60},
		},
	}
	// Write to a temp file
	f, err := os.CreateTemp("", "ratelimit-config-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp config: %v", err)
	}
	defer f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	data, _ := json.MarshalIndent(cfg, "", "  ")
	_, _ = f.Write(data)
	_ = f.Close()

	mgr, err := config.NewManager(f.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	return mgr
}

func TestAdminListLimits(t *testing.T) {
	rdb := getTestRedis()
	defer rdb.Close()

	cfg := getTestConfig(t)
	engine := limiter.New(rdb)

	r := chi.NewRouter()
	adminAPI := admin.New(cfg, engine)
	adminAPI.Register(r)

	req := httptest.NewRequest(http.MethodGet, "/admin/limits", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	var limits map[string]config.Limit
	if err := json.NewDecoder(rec.Body).Decode(&limits); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if _, ok := limits["default"]; !ok {
		t.Error("Expected default limit to be present")
	}
	if _, ok := limits["premium"]; !ok {
		t.Error("Expected premium limit to be present")
	}
}

func TestAdminCreateAndUpdateLimit(t *testing.T) {
	rdb := getTestRedis()
	defer rdb.Close()

	cfg := getTestConfig(t)
	engine := limiter.New(rdb)

	r := chi.NewRouter()
	adminAPI := admin.New(cfg, engine)
	adminAPI.Register(r)

	// Create a new limit with a proper JSON body
	body := strings.NewReader(`{"requests":50,"window_seconds":30}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/limits/test-tier", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the new limit exists
	req = httptest.NewRequest(http.MethodGet, "/admin/limits/test-tier", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	var limit config.Limit
	if err := json.NewDecoder(rec.Body).Decode(&limit); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if limit.Requests != 50 {
		t.Errorf("Expected 50 requests, got %d", limit.Requests)
	}

	// Update the limit
	body = strings.NewReader(`{"requests":75,"window_seconds":45}`)
	req = httptest.NewRequest(http.MethodPut, "/admin/limits/test-tier", body)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200 for update, got %d", rec.Code)
	}

	// Clean up
	req = httptest.NewRequest(http.MethodDelete, "/admin/limits/test-tier", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200 for delete, got %d", rec.Code)
	}
}

func TestAdminHealth(t *testing.T) {
	rdb := getTestRedis()
	defer rdb.Close()

	cfg := getTestConfig(t)
	engine := limiter.New(rdb)

	r := chi.NewRouter()
	adminAPI := admin.New(cfg, engine)
	adminAPI.Register(r)

	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("Expected status 'ok', got '%s'", resp["status"])
	}
}

func TestRateLimitMiddleware_EnforcesLimit(t *testing.T) {
	rdb := getTestRedis()
	defer rdb.Close()

	ctx := context.Background()
	// Clean up key
	key := "ratelimit:test-client:basic"
	_ = rdb.Del(ctx, key)

	cfg := getTestConfig(t)
	engine := limiter.New(rdb)

	// Create a handler that returns 200
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	rateLimiter := middleware.NewRateLimiterMiddleware(engine, cfg, "ratelimit")
	wrapped := rateLimiter.Wrap(handler)

	// Basic tier has 100 req/min limit
	// Fire 100 requests - all should succeed
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
		req.Header.Set("X-Client-ID", "test-client")
		req.Header.Set("X-Client-Tier", "basic")
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Request %d: Expected status 200, got %d", i+1, rec.Code)
		}
	}

	// 101st request should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.Header.Set("X-Client-ID", "test-client")
	req.Header.Set("X-Client-Tier", "basic")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("Expected status 429, got %d", rec.Code)
	}

	if rec.Header().Get("Retry-After") == "" {
		t.Error("Expected Retry-After header")
	}

	if rec.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Errorf("Expected X-RateLimit-Remaining to be 0, got %s", rec.Header().Get("X-RateLimit-Remaining"))
	}
}

func TestRateLimitMiddleware_DifferentTiers(t *testing.T) {
	rdb := getTestRedis()
	defer rdb.Close()

	ctx := context.Background()
	_ = rdb.Del(ctx, "ratelimit:premium-client:premium")
	_ = rdb.Del(ctx, "ratelimit:basic-client:basic")

	cfg := getTestConfig(t)
	engine := limiter.New(rdb)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rateLimiter := middleware.NewRateLimiterMiddleware(engine, cfg, "ratelimiter")
	wrapped := rateLimiter.Wrap(handler)

	// Premium client (1000 req/min) - fire 101 requests, all should succeed
	premiumOK := 0
	for i := 0; i < 101; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
		req.Header.Set("X-Client-ID", "premium-client")
		req.Header.Set("X-Client-Tier", "premium")
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			premiumOK++
		}
	}
	if premiumOK != 101 {
		t.Errorf("Premium client: Expected 101 OK, got %d", premiumOK)
	}

	// Basic client (100 req/min) - fire 101 requests, 100 should succeed
	basicOK := 0
	for i := 0; i < 101; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
		req.Header.Set("X-Client-ID", "basic-client")
		req.Header.Set("X-Client-Tier", "basic")
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			basicOK++
		}
	}
	if basicOK != 100 {
		t.Errorf("Basic client: Expected 100 OK, got %d", basicOK)
	}
}

func TestConcurrentStress(t *testing.T) {
	rdb := getTestRedis()
	defer rdb.Close()

	ctx := context.Background()
	key := "ratelimit:stress-client:basic"
	_ = rdb.Del(ctx, key)

	cfg := getTestConfig(t)
	engine := limiter.New(rdb)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rateLimiter := middleware.NewRateLimiterMiddleware(engine, cfg, "ratelimit")
	wrapped := rateLimiter.Wrap(handler)

	limit := 100 // basic tier limit
	concurrency := 200

	var (
		mu          sync.Mutex
		allowed     = 0
		denied      = 0
	)

	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
			req.Header.Set("X-Client-ID", "stress-client")
			req.Header.Set("X-Client-Tier", "basic")
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)

			mu.Lock()
			if rec.Code == http.StatusOK {
				allowed++
			} else if rec.Code == http.StatusTooManyRequests {
				denied++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	t.Logf("Concurrent stress test: %d allowed, %d denied (limit: %d, concurrency: %d)", allowed, denied, limit, concurrency)

	if allowed > limit {
		t.Errorf("RACE CONDITION: %d requests allowed but limit is %d (overrun by %d)", allowed, limit, allowed-limit)
	}
	if allowed+denied != concurrency {
		t.Errorf("Unexpected response codes: %d allowed + %d denied != %d total", allowed, denied, concurrency)
	}
}

func TestConcurrentStress_HighLoad(t *testing.T) {
	rdb := getTestRedis()
	defer rdb.Close()

	ctx := context.Background()
	key := "ratelimit:highload-client:premium"
	_ = rdb.Del(ctx, key)

	cfg := getTestConfig(t)
	engine := limiter.New(rdb)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rateLimiter := middleware.NewRateLimiterMiddleware(engine, cfg, "ratelimit")
	wrapped := rateLimiter.Wrap(handler)

	limit := 1000 // premium tier limit
	concurrency := 1500

	var (
		mu      sync.Mutex
		allowed = 0
	)

	var wg sync.WaitGroup
	wg.Add(concurrency)

	start := time.Now()
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
			req.Header.Set("X-Client-ID", "highload-client")
			req.Header.Set("X-Client-Tier", "premium")
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)

			mu.Lock()
			if rec.Code == http.StatusOK {
				allowed++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	t.Logf("High load stress test: %d allowed out of %d concurrent (limit: %d) in %v", allowed, concurrency, limit, elapsed)

	if allowed > limit {
		t.Errorf("RACE CONDITION: %d requests allowed but limit is %d (overrun by %d)", allowed, limit, allowed-limit)
	}
}

func TestConfigPersistence(t *testing.T) {
	rdb := getTestRedis()
	defer rdb.Close()

	cfg := getTestConfig(t)
	engine := limiter.New(rdb)

	r := chi.NewRouter()
	adminAPI := admin.New(cfg, engine)
	adminAPI.Register(r)

	// Create a new tier with a proper JSON body
	body := strings.NewReader(`{"requests":200,"window_seconds":60}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/limits/persistence-test", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Reload config from disk
	if err := cfg.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Verify the tier persists
	limit := cfg.GetLimit("persistence-test")
	if limit == nil {
		t.Fatal("Expected persistence-test tier to exist after reload")
	}
	if limit.Requests != 200 {
		t.Errorf("Expected 200 requests, got %d", limit.Requests)
	}

	// Clean up
	_ = cfg.RemoveLimit("persistence-test")
}