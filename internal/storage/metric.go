package storage

import (
	"github.com/prometheus/client_golang/prometheus"
)

type StorageMetrics struct {
	// Section 11.1 - Storage metrics
	ReadLatency  prometheus.Histogram
	WriteLatency prometheus.Histogram
	BytesRead    prometheus.Counter
	BytesWritten prometheus.Counter
	KeysTotal    prometheus.Counter

	// Error tracking
	ReadErrors     prometheus.Counter
	WriteErrors    prometheus.Counter
	CorruptedReads prometheus.Counter

	// Sibling tracking (Section 6.3)
	SiblingsPruned   prometheus.Counter
	SiblingExplosion prometheus.Counter

	// Tombstone GC
	TombstonesGCed prometheus.Counter

	// Disk failure metrics (Section 8)
	DiskFullErrors prometheus.Counter
	IOErrors       prometheus.Counter

	// Vector clock pruning
	VClockEntriesPruned prometheus.Counter
}

func NewStorageMetrics() *StorageMetrics {
	return &StorageMetrics{
		ReadLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "storage_read_latency_seconds",
			Help:    "Local read latency (Section 11.1)",
			Buckets: prometheus.ExponentialBuckets(0.00001, 2, 20), // 10μs to 10s
		}),
		WriteLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "storage_write_latency_seconds",
			Help:    "Local write latency (Section 11.1)",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 20), // 100μs to 100s
		}),
		BytesRead: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_bytes_read_total",
			Help: "Bytes read from storage (Section 11.1)",
		}),
		BytesWritten: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_bytes_written_total",
			Help: "Bytes written to storage (Section 11.1)",
		}),
		KeysTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_keys_total",
			Help: "Total keys stored (Section 11.1)",
		}),
		ReadErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_read_errors_total",
			Help: "Read operation errors",
		}),
		WriteErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_write_errors_total",
			Help: "Write operation errors",
		}),
		CorruptedReads: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_corrupted_reads_total",
			Help: "Reads with CRC32 mismatch (Section 4.3)",
		}),
		SiblingsPruned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_siblings_pruned_total",
			Help: "Siblings pruned due to max limit (Section 6.3)",
		}),
		SiblingExplosion: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_sibling_explosion_detected_total",
			Help: "Keys exceeding sibling warning threshold (Section 6.3)",
		}),
		TombstonesGCed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_tombstones_gced_total",
			Help: "Tombstones garbage collected (Section 5.4)",
		}),
		DiskFullErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_disk_full_errors_total",
			Help: "Disk full errors (Section 8.2)",
		}),
		IOErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_io_errors_total",
			Help: "Disk I/O errors (Section 8.1)",
		}),
		VClockEntriesPruned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "storage_vclock_entries_pruned_total",
			Help: "Vector clock entries pruned (Section 4.3)",
		}),
	}
}

func (m *StorageMetrics) Register(registry *prometheus.Registry) {
	registry.MustRegister(
		m.ReadLatency,
		m.WriteLatency,
		m.BytesRead,
		m.BytesWritten,
		m.KeysTotal,
		m.ReadErrors,
		m.WriteErrors,
		m.CorruptedReads,
		m.SiblingsPruned,
		m.SiblingExplosion,
		m.TombstonesGCed,
		m.DiskFullErrors,
		m.IOErrors,
		m.VClockEntriesPruned,
	)
}
