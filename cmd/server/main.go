package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	chi "github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/admin"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/config"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/limiter"
	"github.com/abstractdevelopers/helix-rate-limiter-benchmark/internal/middleware"
)

func main() {
	// Redis connection
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis unreachable at %s: %v", redisAddr, err)
	}

	// Config
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "api/config.json"
	}

	cfg, err := config.NewManager(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Rate limiter engine
	engine := limiter.New(rdb)

	// Router
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)

	// Admin API (no rate limiting)
	adminAPI := admin.New(cfg, engine)
	adminAPI.Register(r)

	// Protected API (with rate limiting)
	rateLimiter := middleware.NewRateLimiterMiddleware(engine, cfg, "ratelimit")
	protected := chi.NewRouter()
	protected.Get("/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"protected data","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `"}`))
	})
	protected.Post("/submit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":"submitted","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `"}`))
	})
	protected.Get("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Mount("/api/", rateLimiter.Wrap(protected))

	// Health check (no rate limiting)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := engine.Health(r.Context()); err != nil {
			http.Error(w, "redis down", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	done := make(chan bool, 1)
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint
		log.Println("Shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
		done <- true
	}()

	log.Printf("Rate limiter server starting on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
	<-done
	log.Println("Server stopped")
	fmt.Println("Server stopped gracefully")
}