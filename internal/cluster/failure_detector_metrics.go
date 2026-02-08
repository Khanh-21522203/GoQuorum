package cluster

import "github.com/prometheus/client_golang/prometheus"

type FailureDetectorMetrics struct {
	HeartbeatSuccess prometheus.Counter
	HeartbeatFailure prometheus.Counter
	NodeFailed       prometheus.Counter
	NodeRecovered    prometheus.Counter
	SlowNodeDetected prometheus.Counter

	PeerLatency prometheus.HistogramVec // Per peer (Section 6.2)
}

func NewFailureDetectorMetrics() *FailureDetectorMetrics {
	return &FailureDetectorMetrics{
		HeartbeatSuccess: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "failure_detector_heartbeat_success_total",
			Help: "Total successful heartbeats (Section 3.3)",
		}),
		HeartbeatFailure: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "failure_detector_heartbeat_failure_total",
			Help: "Total failed heartbeats (Section 3.3)",
		}),
		NodeFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "failure_detector_node_failed_total",
			Help: "Total nodes marked as failed (Section 3.3)",
		}),
		NodeRecovered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "failure_detector_node_recovered_total",
			Help: "Total nodes recovered from failure (Section 3.5)",
		}),
		SlowNodeDetected: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "failure_detector_slow_node_detected_total",
			Help: "Total slow nodes detected (Section 6.2)",
		}),
		PeerLatency: *prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "failure_detector_peer_latency_seconds",
			Help:    "Heartbeat latency per peer (Section 6.2)",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
		}, []string{"peer_id"}),
	}
}
