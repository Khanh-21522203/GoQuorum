package server

import (
	"context"
	"net"
	"sync"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"GoQuorum/internal/config"
)

// RateLimiter enforces a global token-bucket limit and an optional per-IP limit.
type RateLimiter struct {
	global *rate.Limiter
	perIP  sync.Map // map[string]*rate.Limiter
	cfg    config.RateLimitConfig
}

// NewRateLimiter creates a RateLimiter from cfg. Call only when GlobalRPS > 0.
func NewRateLimiter(cfg config.RateLimitConfig) *RateLimiter {
	if cfg.BurstFactor <= 0 {
		cfg.BurstFactor = 1.0
	}
	burst := int(cfg.GlobalRPS * cfg.BurstFactor)
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{
		global: rate.NewLimiter(rate.Limit(cfg.GlobalRPS), burst),
		cfg:    cfg,
	}
}

// UnaryInterceptor returns a gRPC interceptor that enforces rate limits.
func (rl *RateLimiter) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if !rl.global.Allow() {
			return nil, status.Error(codes.ResourceExhausted, "global rate limit exceeded")
		}

		if rl.cfg.PerIPRPS > 0 {
			ip := peerIP(ctx)
			limiter := rl.getOrCreateIPLimiter(ip)
			if !limiter.Allow() {
				return nil, status.Error(codes.ResourceExhausted, "per-IP rate limit exceeded")
			}
		}

		return handler(ctx, req)
	}
}

func (rl *RateLimiter) getOrCreateIPLimiter(ip string) *rate.Limiter {
	burst := int(rl.cfg.PerIPRPS * rl.cfg.BurstFactor)
	if burst < 1 {
		burst = 1
	}
	v, _ := rl.perIP.LoadOrStore(ip, rate.NewLimiter(rate.Limit(rl.cfg.PerIPRPS), burst))
	return v.(*rate.Limiter)
}

// peerIP extracts the source IP from the gRPC context, stripping the port.
func peerIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return p.Addr.String()
	}
	return host
}
