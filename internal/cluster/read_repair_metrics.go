package cluster

import "github.com/prometheus/client_golang/prometheus"

// ReadRepairMetrics tracks read repair operations (Section 9.1)
type ReadRepairMetrics struct {
	ReadRepairTriggered    prometheus.Counter
	ReadRepairSent         prometheus.Counter
	ReadRepairFailed       prometheus.Counter
	ReadRepairKeysRepaired prometheus.Counter
	ReadRepairLatency      prometheus.Histogram
}

func NewReadRepairMetrics() *ReadRepairMetrics {
	return &ReadRepairMetrics{
		ReadRepairTriggered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "read_repair_triggered_total",
			Help: "Read repairs triggered (Section 9.1)",
		}),

		ReadRepairSent: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "read_repair_sent_total",
			Help: "Repair messages sent (Section 9.1)",
		}),

		ReadRepairFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "read_repair_failed_total",
			Help: "Repair send failures (Section 9.1)",
		}),

		ReadRepairKeysRepaired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "read_repair_keys_repaired_total",
			Help: "Keys successfully repaired (Section 9.1)",
		}),

		ReadRepairLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "read_repair_latency_seconds",
			Help:    "Repair operation latency (Section 9.1)",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 15),
		}),
	}
}
