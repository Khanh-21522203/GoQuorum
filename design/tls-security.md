---
name: TLS Security
description: TLS and mutual TLS configuration for gRPC and inter-node HTTP connections
type: project
---

# TLS Security

## Overview

`internal/security/tls.go` provides two functions for building `*tls.Config` objects: one for the server side and one for clients (including inter-node RPC). TLS is optional — if `TLSConfig.Enabled = false`, connections use plain text.

## Configuration (`internal/config/tls.go`)

```go
type TLSConfig struct {
    Enabled      bool
    CertFile     string   // PEM certificate file
    KeyFile      string   // PEM private key file
    CAFile       string   // PEM CA certificate (for verification)
    MTLSEnabled  bool     // Require client certificates
}
```

Set in `config.yaml` under `server.tls` (for the server listener) and `connection.tls` (for outbound RPC calls from `RPCClient`).

## Server TLS (`LoadServerTLSConfig`)

```go
func LoadServerTLSConfig(cfg TLSConfig) (*tls.Config, error)
```

1. Loads X.509 cert/key pair from `cfg.CertFile` / `cfg.KeyFile` via `tls.LoadX509KeyPair`.
2. If `cfg.MTLSEnabled`:
   - Loads CA from `cfg.CAFile` into an `x509.CertPool` via `loadCertPool`.
   - Sets `ClientAuth = tls.RequireAndVerifyClientCert`.
   - Sets `ClientCAs = certPool`.
3. Returns `*tls.Config` with the server certificate.

Used when registering the gRPC listener in `main.go`.

## Client TLS (`LoadClientTLSConfig`)

```go
func LoadClientTLSConfig(cfg TLSConfig) (*tls.Config, error)
```

1. Loads CA from `cfg.CAFile` into `RootCAs` (verifies the server's certificate against this CA).
2. If `cfg.MTLSEnabled`:
   - Loads client cert/key pair and adds to `Certificates` (presents to server for mutual auth).
3. Returns `*tls.Config` for use in `http.Transport` or gRPC dial options.

Used by `GRPCClient.NewGRPCClient` when constructing the inter-node HTTP client:
```go
if cfg.TLS.Enabled {
    tlsCfg, _ := security.LoadClientTLSConfig(cfg.TLS)
    transport.TLSClientConfig = tlsCfg
}
```

## Certificate Loading Helper

`loadCertPool(caFile string) (*x509.CertPool, error)`:
- Reads PEM file.
- Calls `pool.AppendCertsFromPEM`.
- Returns error if no certificates were parsed (empty or invalid PEM).

## mTLS Flow

```
Node A (client) ──────────────────────────────► Node B (server)
  presents: clientCert signed by CA               verifies: clientCert against ClientCAs
  verifies: serverCert against RootCAs            presents: serverCert signed by CA
```

Both nodes must have certificates signed by the same CA (`CAFile`). Each node has its own `CertFile`/`KeyFile` pair.

## What Is Not Covered

- Certificate rotation at runtime (requires restart).
- ACME/Let's Encrypt integration (not implemented).
- Per-client certificate authorization (any valid cert is accepted in mTLS mode).

Changes:

