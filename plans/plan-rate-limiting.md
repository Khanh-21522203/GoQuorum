# Feature: Rate Limiting

## 1. Purpose

GoQuorum's API is currently open to any client with network access — there is no throttling. A single misbehaving or runaway client can saturate the coordinator's goroutine pool, exhaust storage I/O, or trigger cascading write quorum timeouts that affect all cluster users. Rate limiting enforces per-client and per-node request quotas at the gRPC server layer using a token-bucket algorithm, shedding excess load with a standard `ResourceExhausted` error before it reaches the coordinator. This feature is not yet implemented.

## 2. Responsibilities

- **Per-IP rate limiting**: Track request rates by client IP address (extracted from gRPC peer info) and reject requests that exceed the configured per-client rate
- **Global rate limiting**: Enforce a cluster-wide requests-per-second ceiling on each node regardless of client identity
- **Operation-specific limits**: Apply separate rate limits to reads, writes, and admin operations (writes are more expensive and should have tighter limits)
- **Token bucket algorithm**: Use a token bucket with configurable rate (tokens/sec) and burst capacity (maximum instantaneous burst)
- **gRPC interceptor**: Implement as a `grpc.UnaryServerInterceptor` so it applies transparently to all gRPC methods without modifying handler code
- **Graceful rejection**: Return `codes.ResourceExhausted` with a `Retry-After` hint when a client is throttled
- **Internal service exemption**: Do not rate-limit inter-node internal RPC calls (only the client-facing and admin services)

## 3. Non-Responsibilities

- Does not implement distributed rate limiting across nodes (each node enforces its own limits independently)
- Does not implement user authentication or per-user quotas (rate limits are per-IP or global)
- Does not implement adaptive rate limiting (limits are static configuration values)
- Does not implement request prioritization or queuing (requests are either admitted or rejected)

## 4. Architecture Design

```
gRPC server interceptor chain:
  [logging interceptor]
  → [rate limiting interceptor]   ← new
      |
      check global limiter: allow?
      check per-IP limiter: allow?
      if throttled: return ResourceExhausted
      |
      v
  [handler: ClientAPI / AdminAPI]

Rate limiters:
  globalLimiter: rate.Limiter{rate: GlobalRPS, burst: GlobalBurst}
  perIPLimiters: sync.Map[string → *rate.Limiter]  (lazy creation)
```

### Token Bucket Behavior
```
Burst=100, Rate=50/s:
  Tokens refill at 50 tokens/second
  Up to 100 tokens can accumulate
  Each request consumes 1 token
  Request when tokens=0 → ResourceExhausted (no blocking)
```

## 5. Core Data Structures (Go)

```go
package server

import (
    "context"
    "sync"
    "time"
    "golang.org/x/time/rate"
    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/peer"
    "google.golang.org/grpc/status"
)

// RateLimiter applies token-bucket rate limiting as a gRPC interceptor.
type RateLimiter struct {
    config       RateLimitConfig
    globalLimiter *rate.Limiter
    perIPLimiters sync.Map // string (IP) → *rate.Limiter
    lastSeen      sync.Map // string (IP) → time.Time (for eviction)
    evictTicker   *time.Ticker
    stopCh        chan struct{}
    logger        *slog.Logger
    metrics       *RateLimitMetrics
}

// RateLimitConfig holds all rate limiting parameters.
type RateLimitConfig struct {
    // Enabled controls whether rate limiting is applied.
    Enabled bool `yaml:"enabled" default:"false"`

    // Global limits apply to all requests on this node combined.
    GlobalRPS   float64 `yaml:"global_rps" default:"10000"`
    GlobalBurst int     `yaml:"global_burst" default:"1000"`

    // PerIP limits apply per unique client IP address.
    PerIPRPS   float64 `yaml:"per_ip_rps" default:"1000"`
    PerIPBurst int     `yaml:"per_ip_burst" default:"100"`

    // Operation-specific multipliers (relative to global/per-IP limits).
    // Values < 1.0 tighten the limit for that operation class.
    WriteRPSFactor float64 `yaml:"write_rps_factor" default:"0.5"`
    AdminRPSFactor float64 `yaml:"admin_rps_factor" default:"0.1"`

    // EvictInterval is how often idle per-IP limiters are evicted.
    EvictInterval time.Duration `yaml:"evict_interval" default:"5m"`
    // EvictIdleAfter evicts per-IP limiters idle for this duration.
    EvictIdleAfter time.Duration `yaml:"evict_idle_after" default:"10m"`
}
```

## 6. Public Interfaces

```go
package server

// NewRateLimiter creates a RateLimiter and starts the eviction goroutine.
func NewRateLimiter(cfg RateLimitConfig, logger *slog.Logger) *RateLimiter

// UnaryInterceptor returns a gRPC UnaryServerInterceptor that enforces rate limits.
// Should be applied only to the client and admin gRPC services, NOT the internal service.
func (rl *RateLimiter) UnaryInterceptor() grpc.UnaryServerInterceptor

// Stop shuts down the eviction goroutine.
func (rl *RateLimiter) Stop()
```

## 7. Internal Algorithms

### UnaryInterceptor
```
UnaryInterceptor():
  return func(ctx, req, info, handler):
    if !rl.config.Enabled:
      return handler(ctx, req)

    // Determine operation cost
    cost = 1.0
    if isWriteOperation(info.FullMethod): cost *= (1.0 / config.WriteRPSFactor)
    if isAdminOperation(info.FullMethod): cost *= (1.0 / config.AdminRPSFactor)

    // Check global limiter
    if !rl.globalLimiter.AllowN(now(), int(cost)):
      rl.metrics.GlobalThrottled.Inc()
      return status.Errorf(codes.ResourceExhausted, "global rate limit exceeded, retry after 1s")

    // Extract client IP
    ip = extractIP(ctx)  // from grpc/peer metadata
    if ip != "":
      limiter = rl.getOrCreateIPLimiter(ip)
      rl.lastSeen.Store(ip, now())
      if !limiter.AllowN(now(), int(cost)):
        rl.metrics.PerIPThrottled.WithLabelValues(ip).Inc()
        return status.Errorf(codes.ResourceExhausted, "per-client rate limit exceeded, retry after 1s")

    return handler(ctx, req)
```

### getOrCreateIPLimiter
```
getOrCreateIPLimiter(ip):
  if v, ok = rl.perIPLimiters.Load(ip); ok:
    return v.(*rate.Limiter)
  limiter = rate.NewLimiter(rate.Limit(config.PerIPRPS), config.PerIPBurst)
  actual, _ = rl.perIPLimiters.LoadOrStore(ip, limiter)
  return actual.(*rate.Limiter)
```

### Eviction Loop (prevents unbounded memory growth)
```
evictLoop():
  ticker = time.NewTicker(config.EvictInterval)
  for:
    select:
    case <-ticker.C:
      cutoff = now().Add(-config.EvictIdleAfter)
      rl.lastSeen.Range(func(ip, ts):
        if ts.(time.Time).Before(cutoff):
          rl.perIPLimiters.Delete(ip)
          rl.lastSeen.Delete(ip)
      )
    case <-rl.stopCh:
      return
```

### extractIP
```
extractIP(ctx):
  p, ok = peer.FromContext(ctx)
  if !ok: return ""
  addr = p.Addr.String()  // "1.2.3.4:5678"
  host, _, err = net.SplitHostPort(addr)
  if err != nil: return addr
  return host
```

## 8. Persistence Model

Not applicable. Rate limiting state is purely in-memory. Limiters reset on restart. The token bucket state is never persisted.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| `rate.Limiter` (atomic ops) | `golang.org/x/time/rate.Limiter` is goroutine-safe; `Allow()` uses atomic operations |
| `sync.Map` for per-IP limiters | Lock-free reads for existing IPs; rare concurrent creation handled by `LoadOrStore` |
| `sync.Map` for lastSeen | Tracks last access time per IP for eviction |
| Single eviction goroutine | Periodically scans and removes idle entries |

## 10. Configuration

```go
type RateLimitConfig struct {
    Enabled        bool          `yaml:"enabled" default:"false"`
    GlobalRPS      float64       `yaml:"global_rps" default:"10000"`
    GlobalBurst    int           `yaml:"global_burst" default:"1000"`
    PerIPRPS       float64       `yaml:"per_ip_rps" default:"1000"`
    PerIPBurst     int           `yaml:"per_ip_burst" default:"100"`
    WriteRPSFactor float64       `yaml:"write_rps_factor" default:"0.5"`
    AdminRPSFactor float64       `yaml:"admin_rps_factor" default:"0.1"`
    EvictInterval  time.Duration `yaml:"evict_interval" default:"5m"`
    EvictIdleAfter time.Duration `yaml:"evict_idle_after" default:"10m"`
}
```

Rate limiting is disabled by default to preserve backward compatibility. Enable in production environments.

## 11. Observability

- `goquorum_rate_limit_throttled_total{type="global|per_ip",method}` — Counter of throttled requests
- `goquorum_rate_limit_admitted_total{method}` — Counter of admitted requests (for admission rate tracking)
- `goquorum_rate_limit_ip_limiters_count` — Gauge of active per-IP limiters (for memory monitoring)
- Log at DEBUG when a request is throttled, including client IP and method
- Log at WARN when global throttling is sustained for >10 seconds (cluster overload signal)

## 12. Testing Strategy

- **Unit tests**:
  - `TestGlobalLimiterThrottles`: set GlobalRPS=10, send 20 requests instantly, assert ~10 admitted and rest throttled
  - `TestPerIPLimiterThrottles`: two IPs, each at PerIPRPS=5, assert each throttled independently
  - `TestWriteOperationHigherCost`: WriteRPSFactor=0.5, assert writes consume 2 tokens each
  - `TestAdminOperationHighestCost`: AdminRPSFactor=0.1, assert admin calls consume 10 tokens each
  - `TestDisabledRateLimiterPassesAll`: Enabled=false, assert all requests pass through
  - `TestIPLimiterEviction`: create limiter for IP, wait EvictIdleAfter, trigger eviction, assert limiter removed
  - `TestExtractIPFromContext`: peer context with "1.2.3.4:5678", assert extracted IP = "1.2.3.4"
  - `TestResourceExhaustedGRPCStatus`: throttled request returns codes.ResourceExhausted
  - `TestConcurrentRequestsDifferentIPs`: 100 goroutines from 100 different IPs, assert no data race
- **Integration tests**:
  - `TestRateLimitingInterceptorApplied`: start server with RPS=10, send 100 requests fast, assert ~10 succeed and rest return ResourceExhausted
  - `TestInternalServiceNotRateLimited`: assert Replicate and Heartbeat RPCs bypass rate limiter

## 13. Open Questions

- Should we implement sliding window counters instead of token buckets for more predictable throughput?
- Should we support per-key rate limiting for hot-key protection (a specific key receiving too many requests)?
