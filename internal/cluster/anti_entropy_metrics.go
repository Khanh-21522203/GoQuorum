package cluster

import "github.com/prometheus/client_golang/prometheus"

// AntiEntropyMetrics tracks anti-entropy operations (Section 9.2)
type AntiEntropyMetrics struct {
	ExchangesTotal      prometheus.Counter
	ExchangesFailed     prometheus.Counter
	KeysExchanged       prometheus.Counter
	BytesSent           prometheus.Counter
	BytesReceived       prometheus.Counter
	ScanDuration        prometheus.Histogram
	MerkleTreeDiffNodes prometheus.Histogram
}

func NewAntiEntropyMetrics() *AntiEntropyMetrics {
	return &AntiEntropyMetrics{
		ExchangesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "anti_entropy_exchanges_total",
			Help: "Exchange attempts (Section 9.2)",
		}),

		ExchangesFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "anti_entropy_exchanges_failed_total",
			Help: "Failed exchanges (Section 9.2)",
		}),

		KeysExchanged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "anti_entropy_keys_exchanged_total",
			Help: "Keys synchronized (Section 9.2)",
		}),

		BytesSent: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "anti_entropy_bytes_sent_total",
			Help: "Data sent (Section 9.2)",
		}),

		BytesReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "anti_entropy_bytes_received_total",
			Help: "Data received (Section 9.2)",
		}),

		ScanDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "anti_entropy_scan_duration_seconds",
			Help:    "Full scan time (Section 9.2)",
			Buckets: prometheus.ExponentialBuckets(1, 2, 15),
		}),

		MerkleTreeDiffNodes: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "merkle_tree_diff_nodes",
			Help:    "Differing nodes per exchange (Section 9.3)",
			Buckets: prometheus.LinearBuckets(0, 10, 20),
		}),
	}
}
