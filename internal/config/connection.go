package config

import (
	"errors"
	"time"
)

// ConnectionConfig configures peer connection pool (Section 8.1)
type ConnectionConfig struct {
	PoolSize    int           // Connections per peer (default: 10)
	IdleTimeout time.Duration // Close idle connections (default: 60s)
	MaxLifetime time.Duration // Maximum connection age (default: 1h)
	DialTimeout time.Duration // Connection establishment (default: 5s)

	// Reconnection backoff (Section 8.2)
	ReconnectBase        time.Duration // Base delay (default: 100ms)
	ReconnectMax         time.Duration // Max delay (default: 30s)
	ReconnectFactor      float64       // Backoff factor (default: 2.0)
	MaxReconnectAttempts int           // Max attempts (default: 10)

	// TLS configuration for inter-node HTTP connections
	TLS TLSConfig
}

// DefaultConnectionConfig returns default connection configuration
func DefaultConnectionConfig() ConnectionConfig {
	return ConnectionConfig{
		PoolSize:             10,
		IdleTimeout:          60 * time.Second,
		MaxLifetime:          1 * time.Hour,
		DialTimeout:          5 * time.Second,
		ReconnectBase:        100 * time.Millisecond,
		ReconnectMax:         30 * time.Second,
		ReconnectFactor:      2.0,
		MaxReconnectAttempts: 10,
	}
}

func (c ConnectionConfig) Validate() error {
	if c.PoolSize <= 0 {
		return errors.New("pool_size must be > 0")
	}
	if c.IdleTimeout <= 0 {
		return errors.New("idle_timeout must be > 0")
	}
	if c.MaxLifetime <= 0 {
		return errors.New("max_lifetime must be > 0")
	}
	if c.DialTimeout <= 0 {
		return errors.New("dial_timeout must be > 0")
	}
	if c.ReconnectBase <= 0 {
		return errors.New("reconnect_base must be > 0")
	}
	if c.ReconnectMax < c.ReconnectBase {
		return errors.New("reconnect_max must be >= reconnect_base")
	}
	if c.ReconnectFactor <= 1.0 {
		return errors.New("reconnect_factor must be > 1.0")
	}
	if c.MaxReconnectAttempts <= 0 {
		return errors.New("max_reconnect_attempts must be > 0")
	}
	return nil
}
