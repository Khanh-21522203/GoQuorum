package security

import (
	"crypto/tls"

	"goquorum.io/v2/contracts"
)

// TLSConfig holds TLS/mTLS configuration for the client-facing server and
// the inter-node HTTP/JSON transport.
//
// (v1: internal/config/tls.go TLSConfig)
type TLSConfig struct {
	Enabled     bool   `yaml:"enabled"`
	CertFile    string `yaml:"cert_file"`
	KeyFile     string `yaml:"key_file"`
	CAFile      string `yaml:"ca_file"`
	MTLSEnabled bool   `yaml:"mtls_enabled"`
}

// LoadServerTLSConfig builds a server-side *tls.Config from cfg. If
// cfg.MTLSEnabled, client certificates are required and verified against
// cfg.CAFile.
//
// TODO(v2): import crypto/x509, os; load the cert/key pair via
// tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile) and, if cfg.MTLSEnabled,
// load cfg.CAFile into an x509.CertPool and set ClientCAs +
// ClientAuth = tls.RequireAndVerifyClientCert (v1:
// internal/security/tls.go LoadServerTLSConfig).
func LoadServerTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	return nil, contracts.ErrNotImplemented
}

// LoadClientTLSConfig builds a client-side *tls.Config from cfg, verifying
// the peer's certificate against cfg.CAFile. If cfg.MTLSEnabled,
// cfg.CertFile/cfg.KeyFile are also loaded as the client certificate
// presented to the peer.
//
// TODO(v2): import crypto/x509, os; load cfg.CAFile into an x509.CertPool
// for RootCAs and, if cfg.MTLSEnabled, load cfg.CertFile/cfg.KeyFile via
// tls.LoadX509KeyPair and set Certificates (v1: internal/security/tls.go
// LoadClientTLSConfig).
func LoadClientTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	return nil, contracts.ErrNotImplemented
}
