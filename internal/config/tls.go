package config

// TLSConfig holds TLS/mTLS configuration for gRPC and HTTP servers.
type TLSConfig struct {
	Enabled     bool   `yaml:"enabled"`
	CertFile    string `yaml:"cert_file"`
	KeyFile     string `yaml:"key_file"`
	CAFile      string `yaml:"ca_file"`
	MTLSEnabled bool   `yaml:"mtls_enabled"`
}
