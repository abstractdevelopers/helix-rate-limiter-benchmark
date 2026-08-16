package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/config"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/limiter"
)

// RateLimiterMiddleware enforces rate limits on incoming requests.
type RateLimiterMiddleware struct {
	engine *limiter.Engine
	cfg    *config.Manager
	prefix string
}

// NewRateLimiterMiddleware creates a new rate limiting middleware.
func NewRateLimiterMiddleware(engine *limiter.Engine, cfg *config.Manager, prefix string) *RateLimiterMiddleware {
	return &RateLimiterMiddleware{
		engine: engine,
		cfg:    cfg,
		prefix: prefix,
	}
}

// Wrap wraps an http.Handler with rate limiting.
func (m *RateLimiterMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tier := GetClientTier(r)
		clientID := GetClientID(r)
		limit := m.cfg.GetLimit(tier)
		key := m.prefix + ":" + clientID + ":" + tier

		result, err := m.engine.Check(r.Context(), key, limit.Requests, time.Duration(limit.WindowSeconds)*time.Second)
		if err != nil {
			http.Error(w, `{"error":"rate limiter internal error"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit.Requests))
		w.Header().Set("X-RateLimit-Window", strconv.Itoa(limit.WindowSeconds))

		if !result.Allowed {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Retry-After", strconv.Itoa(int(result.RetryAfter.Seconds())))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded","retry_after":` + strconv.Itoa(int(result.RetryAfter.Seconds())) + `}`))
			return
		}

		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))
		next.ServeHTTP(w, r)
	})
}

// GetClientTier extracts the client tier from the request.
func GetClientTier(r *http.Request) string {
	tier := r.Header.Get("X-Client-Tier")
	if strings.TrimSpace(tier) == "" {
		return "default"
	}
	return tier
}

// GetClientID extracts the client identifier from the request.
func GetClientID(r *http.Request) string {
	clientID := r.Header.Get("X-Client-ID")
	if strings.TrimSpace(clientID) == "" {
		return r.RemoteAddr
	}
	return clientID
}