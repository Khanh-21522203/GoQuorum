package cluster

import "github.com/prometheus/client_golang/prometheus"

type CoordinatorMetrics struct {
	// Request latency (Section 11.1)
	ReadLatency  prometheus.Histogram
	WriteLatency prometheus.Histogram

	// Success/failure counters
	ReadSuccess  prometheus.Counter
	WriteSuccess prometheus.Counter

	// Quorum failures (Section 9 - Error Conditions)
	ReadQuorumFailures  prometheus.Counter
	WriteQuorumFailures prometheus.Counter

	ReadSiblingsCount prometheus.Histogram

	// Hinted handoff
	HintedHandoffWrites prometheus.Counter
	HintsReplayed       prometheus.Counter

	// Request counts by operation
	GetRequestsTotal    prometheus.Counter
	PutRequestsTotal    prometheus.Counter
	DeleteRequestsTotal prometheus.Counter

	// Replica health
	ReplicaTimeouts prometheus.Counter
	ReplicaErrors   prometheus.Counter
}

// NewCoordinatorMetrics creates a new metrics instance
func NewCoordinatorMetrics() *CoordinatorMetrics {
	return &CoordinatorMetrics{
		// Latency histograms
		ReadLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "coordinator_read_latency_seconds",
			Help:    "Coordinator read operation latency including quorum wait",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms to 16s
		}),

		WriteLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "coordinator_write_latency_seconds",
			Help:    "Coordinator write operation latency including quorum wait",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms to 16s
		}),

		// Success counters
		ReadSuccess: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_read_success_total",
			Help: "Total successful read operations (R quorum met)",
		}),

		WriteSuccess: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_write_success_total",
			Help: "Total successful write operations (W quorum met)",
		}),

		// Quorum failures (Section 9.1)
		ReadQuorumFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_read_quorum_failures_total",
			Help: "Total read operations that failed to meet R quorum (Section 9.1)",
		}),

		WriteQuorumFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_write_quorum_failures_total",
			Help: "Total write operations that failed to meet W quorum (Section 9.1)",
		}),

		// Hinted handoff (Section 3.4)
		HintedHandoffWrites: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_hinted_handoff_writes_total",
			Help: "Total writes using hinted handoff (sloppy quorum)",
		}),

		HintsReplayed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_hints_replayed_total",
			Help: "Total hints successfully replayed to recovered nodes",
		}),

		// Request counters
		GetRequestsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_get_requests_total",
			Help: "Total Get requests received by coordinator",
		}),

		PutRequestsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_put_requests_total",
			Help: "Total Put requests received by coordinator",
		}),

		DeleteRequestsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_delete_requests_total",
			Help: "Total Delete requests received by coordinator",
		}),

		// Replica health
		ReplicaTimeouts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_replica_timeouts_total",
			Help: "Total replica timeouts during read/write operations",
		}),

		ReplicaErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coordinator_replica_errors_total",
			Help: "Total replica errors (excluding timeouts)",
		}),
		ReadSiblingsCount: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "coordinator_read_siblings_count",
			Help:    "Number of siblings per successful read",
			Buckets: prometheus.LinearBuckets(1, 1, 10), // 1 to 10 siblings
		}),
	}
}

// Register registers all metrics with Prometheus registry
func (m *CoordinatorMetrics) Register(registry *prometheus.Registry) {
	registry.MustRegister(
		m.ReadLatency,
		m.WriteLatency,
		m.ReadSuccess,
		m.WriteSuccess,
		m.ReadQuorumFailures,
		m.WriteQuorumFailures,
		m.HintedHandoffWrites,
		m.HintsReplayed,
		m.GetRequestsTotal,
		m.PutRequestsTotal,
		m.DeleteRequestsTotal,
		m.ReplicaTimeouts,
		m.ReplicaErrors,
		m.ReadSiblingsCount,
	)
}
