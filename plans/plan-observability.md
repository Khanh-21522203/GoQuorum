# Feature: Observability & Metrics

## 1. Purpose

The observability subsystem provides structured logging, Prometheus metrics, and health check endpoints across all GoQuorum components. It gives operators real-time visibility into throughput, latency, error rates, replication health, sibling divergence, and storage utilization — all of which are critical for understanding and operating a distributed key-value store.

## 2. Responsibilities

- **Prometheus metrics registration**: Create and register all Prometheus counters, gauges, and histograms at startup
- **Per-component metrics structs**: Provide typed metrics structs to each component (coordinator, storage, anti-entropy, etc.) so they can record observations without knowing about global state
- **Structured logging**: Initialize a `slog.Logger` with configurable log level and format (JSON/text); inject it into all components
- **Health check logic**: Implement liveness and readiness probes that check storage open status, coordinator initialization, and peer connectivity
- **Metrics HTTP endpoint**: Register `/metrics` handler with Prometheus default registry on the HTTP mux

## 3. Non-Responsibilities

- Does not implement distributed tracing (out of scope for MVP)
- Does not implement alerting rules (operator-configured in Prometheus/Alertmanager)
- Does not aggregate metrics across nodes (handled by Prometheus scraping all nodes)
- Does not implement log aggregation (delegated to infrastructure layer)

## 4. Architecture Design

```
+--------------------------------------------------+
|               observability package               |
|                                                   |
|  NewMetrics() → *Metrics                          |
|    registers all Prometheus metrics               |
|                                                   |
|  Metrics {                                        |
|    Coordinator  CoordinatorMetrics                |
|    Storage      StorageMetrics                    |
|    AntiEntropy  AntiEntropyMetrics                |
|    ReadRepair   ReadRepairMetrics                 |
|    FailureDet   FailureDetectorMetrics            |
|    Server       ServerMetrics                     |
|  }                                                |
|                                                   |
|  NewLogger(cfg LogConfig) *slog.Logger            |
|                                                   |
|  HealthChecker                                    |
|    Liveness()  → HealthStatus                     |
|    Readiness() → HealthStatus                     |
+--------------------------------------------------+
                 |
         prometheus.MustRegister()
                 |
         /metrics endpoint (promhttp)
```

## 5. Core Data Structures (Go)

```go
package observability

import (
    "log/slog"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics is the top-level container for all component metric structs.
type Metrics struct {
    Coordinator CoordinatorMetrics
    Storage     StorageMetrics
    AntiEntropy AntiEntropyMetrics
    ReadRepair  ReadRepairMetrics
    Failure     FailureDetectorMetrics
    Server      ServerMetrics
    Registry    *prometheus.Registry
}

// CoordinatorMetrics tracks Put/Get/Delete outcomes and latencies.
type CoordinatorMetrics struct {
    PutTotal         *prometheus.CounterVec   // labels: result
    GetTotal         *prometheus.CounterVec   // labels: result
    DeleteTotal      *prometheus.CounterVec   // labels: result
    PutDuration      prometheus.Histogram
    GetDuration      prometheus.Histogram
    DeleteDuration   prometheus.Histogram
    SiblingCount     prometheus.Histogram     // per-Get sibling count
    ReplicaErrors    *prometheus.CounterVec   // labels: node, operation
}

// StorageMetrics tracks I/O and GC.
type StorageMetrics struct {
    GetTotal        *prometheus.CounterVec  // labels: result
    PutTotal        *prometheus.CounterVec  // labels: result
    GetDuration     prometheus.Histogram
    PutDuration     prometheus.Histogram
    DiskUsageBytes  prometheus.Gauge
    LiveKeyCount    prometheus.Gauge
    TombstoneCount  prometheus.Gauge
    GCDeletedKeys   prometheus.Counter
    GCDuration      prometheus.Histogram
    ChecksumErrors  prometheus.Counter
}

// AntiEntropyMetrics tracks sync rounds.
type AntiEntropyMetrics struct {
    SyncTotal          *prometheus.CounterVec // labels: result
    DivergentLeaves    prometheus.Histogram
    KeysExchanged      prometheus.Counter
    SyncDuration       prometheus.Histogram
    TreeUpdateDuration prometheus.Histogram
}

// ReadRepairMetrics tracks repair activity.
type ReadRepairMetrics struct {
    TriggeredTotal  prometheus.Counter
    StaleReplicas   *prometheus.CounterVec // labels: node
    SuccessTotal    prometheus.Counter
    ErrorTotal      *prometheus.CounterVec // labels: node
    DroppedTotal    prometheus.Counter
    SkippedTotal    prometheus.Counter
    QueueDepth      prometheus.Gauge
}

// FailureDetectorMetrics tracks heartbeat health.
type FailureDetectorMetrics struct {
    HeartbeatSent      *prometheus.CounterVec // labels: node
    HeartbeatReceived  *prometheus.CounterVec // labels: node
    HeartbeatFailure   *prometheus.CounterVec // labels: node
    StateTransitions   *prometheus.CounterVec // labels: node, state
    MissedHeartbeats   *prometheus.GaugeVec   // labels: node
    ActivePeerCount    prometheus.Gauge
    FailedPeerCount    prometheus.Gauge
}

// ServerMetrics tracks API layer.
type ServerMetrics struct {
    GRPCRequestsTotal   *prometheus.CounterVec  // labels: method, code
    GRPCDuration        *prometheus.HistogramVec // labels: method
    HTTPRequestsTotal   *prometheus.CounterVec  // labels: path, method, status
    HTTPDuration        *prometheus.HistogramVec // labels: path
}

// LogConfig controls log output format and level.
type LogConfig struct {
    Level  string `yaml:"level" default:"info"`  // debug, info, warn, error
    Format string `yaml:"format" default:"json"` // json, text
}

// HealthStatus is the result of a health probe.
type HealthStatus struct {
    Healthy bool
    Checks  map[string]CheckResult
}

// CheckResult is one named sub-check.
type CheckResult struct {
    OK      bool
    Message string
}
```

## 6. Public Interfaces

```go
package observability

// NewMetrics creates and registers all Prometheus metrics under a custom registry.
func NewMetrics(namespace string) *Metrics

// NewLogger creates a slog.Logger configured by cfg.
func NewLogger(cfg LogConfig) *slog.Logger

// NewHealthChecker creates a HealthChecker with the given dependency probes.
func NewHealthChecker(
    storageReady func() bool,
    coordinatorReady func() bool,
    activePeerCount func() int,
    minPeers int,
) *HealthChecker

// Liveness checks if the process is alive (always true if serving).
func (h *HealthChecker) Liveness() HealthStatus

// Readiness checks if the node is ready to serve traffic.
// Returns unhealthy if storage is not open or fewer than minPeers are active.
func (h *HealthChecker) Readiness() HealthStatus

// Handler returns the promhttp.Handler for the /metrics endpoint.
func (m *Metrics) Handler() http.Handler
```

## 7. Internal Algorithms

### Health Readiness Check
```
Readiness():
  checks = {}

  // Storage check
  if !storageReady():
    checks["storage"] = {OK: false, Message: "storage not open"}
  else:
    checks["storage"] = {OK: true}

  // Coordinator check
  if !coordinatorReady():
    checks["coordinator"] = {OK: false, Message: "coordinator not initialized"}
  else:
    checks["coordinator"] = {OK: true}

  // Peer connectivity check
  active = activePeerCount()
  if active < minPeers:
    checks["peers"] = {OK: false, Message: fmt.Sprintf("only %d active peers, need %d", active, minPeers)}
  else:
    checks["peers"] = {OK: true, Message: fmt.Sprintf("%d active peers", active)}

  healthy = all checks are OK
  return HealthStatus{Healthy: healthy, Checks: checks}
```

### NewMetrics (metric registration)
```
NewMetrics(namespace):
  reg = prometheus.NewRegistry()
  reg.MustRegister(prometheus.NewGoCollector())
  reg.MustRegister(prometheus.NewProcessCollector(...))

  m = Metrics{Registry: reg}

  m.Coordinator.PutTotal = promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
    Namespace: namespace,
    Subsystem: "coordinator",
    Name:      "put_total",
    Help:      "Total Put operations by result.",
  }, []string{"result"})

  // ... register all other metrics ...
  return m
```

## 8. Persistence Model

Not applicable. Prometheus metrics are in-memory and scraped on demand. Logs are written to stdout (or a configured writer). No observability data is persisted locally.

## 9. Concurrency Model

| Mechanism | Usage |
|-----------|-------|
| Prometheus atomic operations | All Counter/Gauge/Histogram operations are goroutine-safe by the Prometheus library |
| slog.Logger | `slog.Logger` is goroutine-safe; all components share the same logger instance |
| HealthChecker callback functions | Callbacks (`storageReady`, etc.) must be goroutine-safe (they access shared state) |

## 10. Configuration

```go
type ObservabilityConfig struct {
    Log LogConfig `yaml:"log"`
    Metrics MetricsConfig `yaml:"metrics"`
}

type LogConfig struct {
    Level  string `yaml:"level" default:"info"`
    Format string `yaml:"format" default:"json"` // json | text
}

type MetricsConfig struct {
    Namespace string `yaml:"namespace" default:"goquorum"`
}
```

## 11. Observability

The observability package is itself the source of observability. It does not have its own metrics (that would be circular). It does register the default Go runtime metrics (GC pause, goroutine count, memory) via `prometheus.NewGoCollector()`.

## 12. Testing Strategy

- **Unit tests**:
  - `TestNewMetricsRegistersAll`: create Metrics, assert all expected metric names present in registry
  - `TestHealthLiveness`: always returns healthy
  - `TestHealthReadinessStorageNotOpen`: storageReady=false, assert Readiness.Healthy=false
  - `TestHealthReadinessNotEnoughPeers`: activePeerCount=0, minPeers=1, assert unhealthy
  - `TestHealthReadinessAllGood`: all checks pass, assert healthy
  - `TestLoggerJSONFormat`: logger with format=json, write message, assert output is valid JSON
  - `TestLoggerLevelFilter`: logger with level=warn, write debug message, assert not emitted
- **Integration tests**:
  - `TestMetricsEndpointScrapeable`: start HTTP server, GET /metrics, assert Prometheus text format parseable

## 13. Open Questions

None.
