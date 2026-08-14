package observability

import "goquorum.io/v2/contracts"

// Registry is a stub metrics registry. The real implementation wraps a
// Prometheus registry exposing storage, coordinator, and cluster metrics.
//
// (v1: internal/storage/metric.go StorageMetrics,
// internal/cluster/coordinator_metrics.go, anti_entropy_metrics.go,
// failure_detector_metrics.go, membership_metrics.go,
// read_repair_metrics.go)
type Registry struct{}

// NewRegistry creates a new metrics registry.
//
// TODO(v2): import github.com/prometheus/client_golang/prometheus; embed a
// *prometheus.Registry and register typed counters/gauges/histograms for
// each subsystem (v1: as above).
func NewRegistry() (*Registry, error) {
	return nil, contracts.ErrNotImplemented
}
