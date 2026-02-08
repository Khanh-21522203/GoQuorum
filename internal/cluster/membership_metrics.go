package cluster

import "github.com/prometheus/client_golang/prometheus"

// MembershipMetrics tracks membership metrics (Section 9.1)
type MembershipMetrics struct {
	PeersTotal       prometheus.Gauge
	PeersActive      prometheus.Gauge
	PeersFailed      prometheus.Gauge
	PeersRecovered   prometheus.Counter
	PeerStateChanges prometheus.Counter
}

func NewMembershipMetrics() *MembershipMetrics {
	return &MembershipMetrics{
		PeersTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cluster_peers_total",
			Help: "Total configured peers (Section 9.1)",
		}),
		PeersActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cluster_peers_active",
			Help: "Peers in ACTIVE state (Section 9.1)",
		}),
		PeersFailed: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cluster_peers_failed",
			Help: "Peers in FAILED state (Section 9.1)",
		}),
		PeersRecovered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cluster_peers_recovered_total",
			Help: "Peers recovered from failure (Section 9.2)",
		}),
		PeerStateChanges: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "peer_state_changes_total",
			Help: "Peer state transitions (Section 9.1)",
		}),
	}
}
