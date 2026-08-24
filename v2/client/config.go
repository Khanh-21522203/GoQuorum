package client

import "time"

// ClientConfig holds configuration for the GoQuorum client.
//
// (v1: client/client.go ClientConfig)
type ClientConfig struct {
	Addr           string // server address, e.g. "localhost:7070"
	DialTimeout    time.Duration
	RequestTimeout time.Duration
	RetryBaseDelay time.Duration
	MaxRetries     int
}

// DefaultClientConfig returns a ClientConfig with the v1 defaults: a 5s
// dial timeout, a 5s per-request timeout, up to 3 retries, and a 100ms
// base retry delay.
//
// (v1: client/client.go DefaultClientConfig)
func DefaultClientConfig(addr string) ClientConfig {
	return ClientConfig{
		Addr:           addr,
		DialTimeout:    5 * time.Second,
		RequestTimeout: 5 * time.Second,
		RetryBaseDelay: 100 * time.Millisecond,
		MaxRetries:     3,
	}
}
