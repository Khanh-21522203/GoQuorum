package server

import (
	"encoding/json"
	"net/http"
	"syscall"
	"time"
)

// HealthResponse represents the health check response
type HealthResponse struct {
	Status        string                 `json:"status"`
	NodeID        string                 `json:"node_id"`
	Version       string                 `json:"version"`
	UptimeSeconds int64                  `json:"uptime_seconds"`
	Checks        map[string]CheckResult `json:"checks"`
}

// CheckResult represents a single health check result
type CheckResult struct {
	Status      string `json:"status"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	Error       string `json:"error,omitempty"`
	PeersActive int    `json:"peers_active,omitempty"`
	PeersTotal  int    `json:"peers_total,omitempty"`
	FreeBytes   int64  `json:"free_bytes,omitempty"`
	TotalBytes  int64  `json:"total_bytes,omitempty"`
}

// handleHealth handles GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := s.checkHealth()

	statusCode := http.StatusOK
	if resp.Status == "unhealthy" {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}

// handleLive handles GET /health/live (liveness probe)
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Liveness: just check if process is alive
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "alive",
	})
}

// handleReady handles GET /health/ready (readiness probe)
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ready := s.isReady()

	w.Header().Set("Content-Type", "application/json")
	if ready {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ready",
		})
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "not_ready",
		})
	}
}

// checkHealth performs all health checks
func (s *Server) checkHealth() HealthResponse {
	resp := HealthResponse{
		Status:        "healthy",
		NodeID:        s.nodeID,
		Version:       s.version,
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
		Checks:        make(map[string]CheckResult),
	}

	// Check storage
	storageCheck := s.checkStorage()
	resp.Checks["storage"] = storageCheck
	if storageCheck.Status != "healthy" {
		resp.Status = "unhealthy"
	}

	// Check cluster
	clusterCheck := s.checkCluster()
	resp.Checks["cluster"] = clusterCheck
	if clusterCheck.Status == "unhealthy" {
		resp.Status = "unhealthy"
	} else if clusterCheck.Status == "degraded" && resp.Status == "healthy" {
		resp.Status = "degraded"
	}

	// Check disk
	diskCheck := s.checkDisk()
	resp.Checks["disk"] = diskCheck
	if diskCheck.Status == "degraded" && resp.Status == "healthy" {
		resp.Status = "degraded"
	}

	return resp
}

// checkStorage checks storage health
func (s *Server) checkStorage() CheckResult {
	start := time.Now()

	if s.storage == nil {
		return CheckResult{
			Status: "unhealthy",
			Error:  "storage not initialized",
		}
	}

	// Try to get stats as a health check
	_ = s.storage.Stats()
	latency := time.Since(start).Milliseconds()

	return CheckResult{
		Status:    "healthy",
		LatencyMs: latency,
	}
}

// checkCluster checks cluster health
func (s *Server) checkCluster() CheckResult {
	if s.membership == nil {
		return CheckResult{
			Status:      "healthy",
			PeersActive: 0,
			PeersTotal:  0,
		}
	}

	active := s.membership.ActivePeerCount()
	total := s.membership.TotalPeerCount()

	status := "healthy"
	if total > 0 {
		// Check if we have quorum
		if active < total/2 {
			status = "degraded"
		}
		if active == 0 && total > 1 {
			status = "unhealthy"
		}
	}

	return CheckResult{
		Status:      status,
		PeersActive: active,
		PeersTotal:  total,
	}
}

// checkDisk checks disk health using the storage data directory
func (s *Server) checkDisk() CheckResult {
	if s.storage == nil {
		return CheckResult{Status: "healthy"}
	}

	dataDir := s.dataDir
	if dataDir == "" {
		dataDir = "."
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &stat); err != nil {
		return CheckResult{
			Status: "degraded",
			Error:  err.Error(),
		}
	}

	totalBytes := int64(stat.Blocks) * stat.Bsize
	freeBytes := int64(stat.Bavail) * stat.Bsize

	status := "healthy"
	if totalBytes > 0 {
		usedPct := float64(totalBytes-freeBytes) / float64(totalBytes)
		if usedPct >= 0.95 {
			status = "unhealthy"
		} else if usedPct >= 0.80 {
			status = "degraded"
		}
	}

	return CheckResult{
		Status:     status,
		FreeBytes:  freeBytes,
		TotalBytes: totalBytes,
	}
}

// isReady checks if the server is ready to serve requests
func (s *Server) isReady() bool {
	// Check storage is accessible
	if s.storage == nil {
		return false
	}

	// Check if we have sufficient cluster connectivity
	if s.membership != nil {
		active := s.membership.ActivePeerCount()
		total := s.membership.TotalPeerCount()

		// Need at least half of peers for quorum
		if total > 1 && active < total/2 {
			return false
		}
	}

	return true
}
