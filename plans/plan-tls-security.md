# Feature: TLS / mTLS Security

## 1. Purpose

GoQuorum currently operates with all gRPC and HTTP traffic unencrypted and unauthenticated. In any environment beyond a local development machine, this is unacceptable: both inter-node replication traffic and client-facing APIs should be encrypted and authenticated. This feature adds TLS for all gRPC connections and mutual TLS (mTLS) for inter-node communication, ensuring both confidentiality and identity verification. This feature is not yet implemented.

## 2. Responsibilities

- **Server TLS**: Serve gRPC and HTTP with TLS using a configurable certificate and private key
- **Client TLS**: Configure the Go client library and internal RPC pool to connect with TLS, verifying server certificates against a CA
- **mTLS for inter-node**: Require client certificates on the internal gRPC service so nodes verify each other's identity before replicating or exchanging heartbeats
- **Certificate loading**: Load certificates from files at startup; support hot-reload on SIGHUP to rotate certificates without restart
- **CA bundle**: Accept a custom CA certificate bundle for self-signed or private CA deployments
- **TLS configuration**: Expose TLS version (minimum TLS 1.2), cipher suite selection, and SNI settings

## 3. Non-Responsibilities

- Does not implement a certificate authority or certificate issuance (operator-managed PKI)
- Does not implement RBAC or authorization (identity authentication only)
- Does not implement API key or token-based auth (separate feature if needed)
- Does not implement HTTPS redirect for the HTTP gateway (operator sets up a reverse proxy for that)

## 4. Architecture Design

```
Client (pkg/client):
  grpc.Dial with credentials.NewTLS(tlsCfg)
  tlsCfg = &tls.Config{
    RootCAs:    loadCA(cfg.CACert),
    ServerName: cfg.ServerName,
  }

Server (gRPC + HTTP):
  grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLSCfg)))
  serverTLSCfg = &tls.Config{
    Certificates: []tls.Certificate{loadCert(cfg.Cert, cfg.Key)},
    ClientAuth:   tls.NoClientCert  // for public client API
  }

Internal gRPC (mTLS):
  serverTLSCfg.ClientAuth = tls.RequireAndVerifyClientCert
  serverTLSCfg.ClientCAs  = loadCA(cfg.CACert)

  internalClientTLSCfg = &tls.Config{
    Certificates: []tls.Certificate{loadCert(cfg.NodeCert, cfg.NodeKey)},
    RootCAs:      loadCA(cfg.CACert),
  }
```

## 5. Core Data Structures (Go)

```go
package security

import (
    "crypto/tls"
    "crypto/x509"
    "os"
    "sync"
)

// TLSConfig holds all TLS-related configuration for a GoQuorum node.
type TLSConfig struct {
    // Enabled controls whether TLS is used at all.
    Enabled bool `yaml:"enabled" default:"false"`

    // CACert is the path to the PEM-encoded CA certificate (or bundle).
    CACert string `yaml:"ca_cert"`

    // Server certificate and key for the public-facing gRPC/HTTP listener.
    ServerCert string `yaml:"server_cert"`
    ServerKey  string `yaml:"server_key"`

    // NodeCert and NodeKey are used for mTLS on the internal gRPC service.
    // If empty, the same server cert/key are used.
    NodeCert string `yaml:"node_cert"`
    NodeKey  string `yaml:"node_key"`

    // MutualTLS requires client certificates on the internal gRPC service.
    MutualTLS bool `yaml:"mutual_tls" default:"true"`

    // MinVersion is the minimum TLS version. Default: "1.2".
    MinVersion string `yaml:"min_version" default:"1.2"`
}

// CertLoader loads and caches TLS certificates, supporting hot-reload.
type CertLoader struct {
    mu       sync.RWMutex
    certFile string
    keyFile  string
    cert     *tls.Certificate
    caPool   *x509.CertPool
    caFile   string
}
```

## 6. Public Interfaces

```go
package security

// NewCertLoader creates a CertLoader and loads the initial certificate.
func NewCertLoader(certFile, keyFile, caFile string) (*CertLoader, error)

// GetCertificate implements the tls.Config.GetCertificate callback,
// returning the current certificate (allows hot-reload).
func (cl *CertLoader) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)

// GetClientCertificate implements tls.Config.GetClientCertificate for mTLS clients.
func (cl *CertLoader) GetClientCertificate(info *tls.CertificateRequestInfo) (*tls.Certificate, error)

// Reload re-reads the certificate files from disk. Safe to call from SIGHUP handler.
func (cl *CertLoader) Reload() error

// CAPool returns the x509.CertPool for CA verification.
func (cl *CertLoader) CAPool() *x509.CertPool

// BuildServerTLSConfig constructs a *tls.Config for the gRPC/HTTP server.
func BuildServerTLSConfig(cfg TLSConfig, loader *CertLoader) (*tls.Config, error)

// BuildInternalServerTLSConfig constructs a *tls.Config for the internal gRPC server with mTLS.
func BuildInternalServerTLSConfig(cfg TLSConfig, loader *CertLoader) (*tls.Config, error)

// BuildClientTLSConfig constructs a *tls.Config for outbound gRPC connections.
func BuildClientTLSConfig(cfg TLSConfig, loader *CertLoader) (*tls.Config, error)
```

## 7. Internal Algorithms

### BuildServerTLSConfig
```
BuildServerTLSConfig(cfg, loader):
  tlsCfg = &tls.Config{
    GetCertificate: loader.GetCertificate,
    ClientCAs:      loader.CAPool(),
    ClientAuth:     tls.NoClientCert,
    MinVersion:     parseTLSVersion(cfg.MinVersion),
  }
  return tlsCfg
```

### BuildInternalServerTLSConfig (mTLS)
```
BuildInternalServerTLSConfig(cfg, loader):
  tlsCfg = BuildServerTLSConfig(cfg, loader)
  if cfg.MutualTLS:
    tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
  return tlsCfg
```

### BuildClientTLSConfig (outbound RPCs)
```
BuildClientTLSConfig(cfg, loader):
  tlsCfg = &tls.Config{
    RootCAs:               loader.CAPool(),
    GetClientCertificate:  loader.GetClientCertificate,
    MinVersion:            parseTLSVersion(cfg.MinVersion),
  }
  return tlsCfg
```

### Reload (SIGHUP-triggered)
```
Reload():
  cert, err = tls.LoadX509KeyPair(cl.certFile, cl.keyFile)
  if err != nil:
    log.Error("cert reload failed", err=err)
    return err  // keep serving with old cert

  caPool, err = loadCAPool(cl.caFile)
  if err != nil:
    log.Error("CA pool reload failed", err=err)
    return err

  cl.mu.Lock()
  cl.cert = &cert
  cl.caPool = caPool
  cl.mu.Unlock()

  log.Info("TLS certificates reloaded")
  return nil
```

### SIGHUP Handler (in main.go)
```
sigCh = make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGHUP)
go func():
  for range sigCh:
    if err = certLoader.Reload(); err != nil:
      log.Error("reload failed", err=err)
```

## 8. Persistence Model

Not applicable. TLS certificates are read from the filesystem. The `CertLoader` caches the parsed certificate in memory for performance. The filesystem is the source of truth; `Reload()` re-reads from disk.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| `sync.RWMutex` in CertLoader | RLock for `GetCertificate`/`GetClientCertificate` (hot path); Lock for `Reload` |
| `tls.Config.GetCertificate` callback | Called per TLS handshake; safe for concurrent use via RLock |
| SIGHUP goroutine | Single goroutine handles reload signals; safe due to CertLoader mutex |

## 10. Configuration

```go
type TLSConfig struct {
    Enabled    bool   `yaml:"enabled" default:"false"`
    CACert     string `yaml:"ca_cert"`
    ServerCert string `yaml:"server_cert"`
    ServerKey  string `yaml:"server_key"`
    NodeCert   string `yaml:"node_cert"`   // optional; falls back to ServerCert
    NodeKey    string `yaml:"node_key"`    // optional; falls back to ServerKey
    MutualTLS  bool   `yaml:"mutual_tls" default:"true"`
    MinVersion string `yaml:"min_version" default:"1.2"` // "1.2" or "1.3"
}
```

### Example: Self-signed 3-node cluster (config.yaml)
```yaml
tls:
  enabled: true
  ca_cert: /etc/goquorum/ca.crt
  server_cert: /etc/goquorum/node1.crt
  server_key: /etc/goquorum/node1.key
  mutual_tls: true
  min_version: "1.2"
```

## 11. Observability

- Log at INFO at startup: "TLS enabled, cert=..., CA=..., mTLS=..."
- Log at INFO on successful certificate reload
- Log at ERROR on failed certificate reload (with old cert continuing in service)
- `goquorum_tls_handshake_errors_total` — Counter of TLS handshake failures (via gRPC stats handler)
- `goquorum_cert_expiry_seconds` — Gauge of seconds until the current certificate expires (alert on low values)

## 12. Testing Strategy

- **Unit tests**:
  - `TestBuildServerTLSConfig`: cert and CA loaded, assert tls.Config has expected fields
  - `TestBuildInternalServerTLSConfigMTLS`: MutualTLS=true, assert ClientAuth=RequireAndVerifyClientCert
  - `TestBuildClientTLSConfig`: assert RootCAs set and GetClientCertificate set
  - `TestCertLoaderReload`: load cert, replace cert file with new cert, call Reload(), assert new cert returned
  - `TestCertLoaderReloadFailsGracefully`: reload with invalid cert file, assert old cert still returned
  - `TestConcurrentGetCertificate`: 100 goroutines calling GetCertificate concurrently while Reload runs, assert no race
- **Integration tests**:
  - `TestGRPCWithTLS`: start server with TLS, client connects with CA cert, assert Put/Get succeed
  - `TestMTLSInterNodeReplication`: two nodes with mTLS, assert Replicate RPC succeeds
  - `TestMTLSRejectsUnknownClient`: client without valid cert, assert connection rejected

## 13. Open Questions

- Should we support SPIFFE/SVID for certificate issuance in cloud-native deployments (e.g., via cert-manager)?
- Should the HTTP gateway port support TLS separately from the gRPC port, or should they share the same cert?
