# Helix Rate Limiter Benchmark

Production-ready API rate limiter built with Go and Redis. Designed for correctness under high concurrency, with zero race-condition overruns.

## Architecture

### Concurrency Model
- **Atomic Lua scripting** on Redis ensures rate limit checks are race-free. The entire check-and-increment operation runs as a single atomic script via `EVAL`, so even under thousands of concurrent requests, the limit is never exceeded.
- Go goroutines are used for the HTTP server (standard `net/http`) and config hot-reload. All shared state (config) is protected by `sync.RWMutex`.

### State Store
- **Redis** is the sole state store. It provides:
  - Sorted sets for sliding window rate limiting
  - Atomic Lua execution for race-free counter increments
  - Persistence via RDB/AOF (configured at the Redis level)
  - Multi-instance support: all instances share the same Redis backend

### Config-Update Consistency
- Rate limit rules are stored in `api/config.json` and loaded at startup.
- The admin API allows creating, updating, and removing limits at runtime without restart.
- Changes are immediately persisted to disk and to an in-memory `sync.RWMutex`-protected map.
- `POST /admin/config/reload` re-reads the config file from disk.
- No stale state: the limiter reads the current config on every request via `GetLimit(tier)`.

### Multi-Instance Scaling
- Stateless HTTP server instances share a single Redis backend.
- Lua scripts run atomically on Redis, so multiple instances coordinate correctly.
- Horizontal scaling: add more instances, point them at the same Redis URL (`REDIS_ADDR`).

## Quick Start

### Prerequisites
- Go 1.21+
- Docker & Docker Compose (for Redis)

### Run with Docker Compose
```bash
docker compose up -d
```

This starts Redis on port 6379 and the rate limiter server on port 8080.

### Run locally
```bash
# Start Redis
redis-server --daemonize yes

# Run the server
go run ./cmd/server
```

## API Endpoints

### Protected API (rate-limited)
| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/data` | Returns protected data |
| POST | `/api/submit` | Accepts submissions |
| GET | `/api/status` | Status endpoint |

### Rate Limit Headers
- `X-RateLimit-Limit`: Maximum requests allowed in the window
- `X-RateLimit-Window`: Window size in seconds
- `X-RateLimit-Remaining`: Remaining requests in the current window
- `Retry-After`: Seconds to wait before retrying (on 429 responses)

### Client Identification
- `X-Client-ID`: Unique client identifier (defaults to remote address)
- `X-Client-Tier`: Rate limit tier name (defaults to `default`)

### Admin API (not rate-limited)
| Method | Path | Description |
|--------|------|-------------|
| GET | `/admin/health` | Health check (verifies Redis connectivity) |
| GET | `/admin/limits` | List all rate limit tiers |
| GET | `/admin/limits/{tier}` | Get a specific tier's limit |
| POST | `/admin/limits/{tier}` | Create a new tier |
| PUT | `/admin/limits/{tier}` | Update an existing tier |
| DELETE | `/admin/limits/{tier}` | Remove a tier |
| POST | `/admin/config/reload` | Reload config from disk |

### Example: Create a new tier
```bash
curl -X POST http://localhost:8080/admin/limits/enterprise \
  -H "Content-Type: application/json" \
  -d '{"requests":10000,"window_seconds":60}'
```

### Example: Hit a protected endpoint
```bash
curl -H "X-Client-ID: user-123" -H "X-Client-Tier: premium" \
  http://localhost:8080/api/data
```

## Configuration

Default config is in `api/config.json`:
```json
{
  "default_limit": {
    "requests": 100,
    "window_seconds": 60
  },
  "clients": {
    "premium": {
      "requests": 1000,
      "window_seconds": 60
    },
    "basic": {
      "requests": 100,
      "window_seconds": 60
    }
  }
}
```

Environment variables:
| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | `localhost:6379` | Redis connection address |
| `CONFIG_PATH` | `api/config.json` | Path to the config file |
| `PORT` | `8080` | HTTP server port |

## Testing

```bash
# Start Redis first
redis-server --daemonize yes

# Run all tests
go test -count=1 ./...

# Run with verbose output
go test -v -count=1 ./...
```

Tests include:
- **Unit tests**: Core limiter engine correctness (allow/deny, window expiry)
- **Concurrent stress tests**: 200 and 1500 concurrent goroutines verifying zero overruns
- **Integration tests**: Full HTTP middleware, admin API, config persistence

## Project Structure

```
├── api/
│   └── config.json          # Rate limit configuration
├── cmd/
│   └── server/
│       └── main.go          # HTTP server entry point
├── internal/
│   ├── admin/
│   │   └── api.go           # Admin API handlers
│   ├── config/
│   │   └── config.go        # Config manager with hot-reload
│   ├── limiter/
│   │   ├── engine.go        # Redis-based rate limiter engine
│   │   └── engine_test.go   # Limiter unit tests
│   └── middleware/
│       └── rate_limit.go    # HTTP rate limiting middleware
├── tests/
│   └── integration_test.go  # Integration & stress tests
├── docker-compose.yml       # Redis + server setup
├── Dockerfile               # Server container image
├── go.mod
└── README.md
```