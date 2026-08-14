package config

import (
	"time"

	"goquorum.io/v2/infra/security"
)

// ConnectionConfig configures the peer connection pool used by the
// inter-node HTTP/JSON transport.
//
// v1 left every field on this struct untagged, so it silently failed to
// deserialize from YAML; every field here carries an explicit tag.
//
// (v1: internal/config/connection.go ConnectionConfig)
type ConnectionConfig struct {
	PoolSize    int           `yaml:"pool_size"`
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	MaxLifetime time.Duration `yaml:"max_lifetime"`
	DialTimeout time.Duration `yaml:"dial_timeout"`

	ReconnectBase        time.Duration `yaml:"reconnect_base"`
	ReconnectMax         time.Duration `yaml:"reconnect_max"`
	ReconnectFactor      float64       `yaml:"reconnect_factor"`
	MaxReconnectAttempts int           `yaml:"max_reconnect_attempts"`

	// TLS configuration for inter-node HTTP connections.
	TLS security.TLSConfig `yaml:"tls"`
}
