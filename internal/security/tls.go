package security

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"GoQuorum/internal/config"
)

// LoadServerTLSConfig builds a *tls.Config suitable for use with a gRPC server.
// If cfg.MTLSEnabled is true, client certificates are required and verified
// against the CA specified in cfg.CAFile.
func LoadServerTLSConfig(cfg config.TLSConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	if cfg.MTLSEnabled {
		pool, err := loadCertPool(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("load CA for mTLS: %w", err)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tlsCfg, nil
}

// LoadClientTLSConfig builds a *tls.Config suitable for use with a gRPC client.
// The CA in cfg.CAFile is used to verify the server certificate.
// If cfg.MTLSEnabled is true, cfg.CertFile/cfg.KeyFile are also loaded as the
// client certificate presented to the server.
func LoadClientTLSConfig(cfg config.TLSConfig) (*tls.Config, error) {
	pool, err := loadCertPool(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("load CA: %w", err)
	}

	tlsCfg := &tls.Config{
		RootCAs: pool,
	}

	if cfg.MTLSEnabled {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert/key for mTLS: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}

// loadCertPool reads a PEM-encoded CA certificate file and returns an x509.CertPool.
func loadCertPool(caFile string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file %q: %w", caFile, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no valid certificates found in CA file %q", caFile)
	}

	return pool, nil
}
