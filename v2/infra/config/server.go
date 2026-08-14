package config

import "goquorum.io/v2/infra/security"

// RateLimitConfig controls token-bucket rate limiting on the client-facing
// server.
//
// (v1: internal/config/config.go RateLimitConfig)
type RateLimitConfig struct {
	GlobalRPS   float64 `yaml:"global_rps"`   // 0 = disabled.
	PerIPRPS    float64 `yaml:"per_ip_rps"`   // 0 = no per-IP limit.
	BurstFactor float64 `yaml:"burst_factor"` // Default: 1.0.
}

// ServerConfig defines the client-facing server's network settings.
//
// (v1: internal/config/config.go ServerConfig)
type ServerConfig struct {
	GRPCAddr  string             `yaml:"grpc_addr"`
	HTTPAddr  string             `yaml:"http_addr"`
	TLS       security.TLSConfig `yaml:"tls"`
	RateLimit RateLimitConfig    `yaml:"rate_limit"`
}
