# Feature: Deployment & Docker

## 1. Purpose

GoQuorum ships with a Docker-based deployment setup that makes it easy to run a multi-node cluster locally for development, testing, and demonstration. It provides a `Dockerfile` for building a minimal production image, a `docker-compose.yaml` for a 3-node cluster with integrated Prometheus and Grafana, and example Kubernetes manifests for production deployment. The goal is zero-friction from `git clone` to a running distributed cluster.

## 2. Responsibilities

- **Dockerfile**: Multi-stage build producing a minimal, non-root container image from the Go binary
- **docker-compose.yaml**: Orchestrate a 3-node GoQuorum cluster + Prometheus + Grafana with a single `docker compose up`
- **Node bootstrapping**: Each container reads its configuration from a mounted `config.yaml`; nodes discover each other via static peer addresses (Docker service names)
- **Prometheus scraping**: Prometheus is configured to scrape `/metrics` from all 3 nodes on a 15-second interval
- **Grafana dashboards**: Pre-load a GoQuorum dashboard JSON with panels for throughput, latency, sibling count, replication health, and storage metrics
- **Health checks**: Docker health check for each node using `/health/ready` endpoint
- **Kubernetes manifests**: Example `StatefulSet` + `Service` + `ConfigMap` for production Kubernetes deployment

## 3. Non-Responsibilities

- Does not implement automated cluster scaling
- Does not implement Helm chart (out of scope for MVP; Kubernetes manifests are sufficient)
- Does not implement CI/CD pipelines
- Does not implement production TLS certificate issuance (operator provides certs)

## 4. Architecture Design

```
docker-compose.yaml:
  services:
    node1:  GoQuorum (gRPC :7070, HTTP :8080)
    node2:  GoQuorum (gRPC :7071, HTTP :8081)
    node3:  GoQuorum (gRPC :7072, HTTP :8082)
    prometheus: Prometheus (scrapes :8080/metrics, :8081/metrics, :8082/metrics)
    grafana:    Grafana (loads GoQuorum dashboard, auth: admin/admin)

  networks:
    goquorum-net: bridge

  volumes:
    node1-data:  persistent storage for node1
    node2-data:  persistent storage for node2
    node3-data:  persistent storage for node3
    prometheus-data:
    grafana-data:
```

### Port Mapping (host → container)

| Service | gRPC | HTTP |
|---------|------|------|
| node1 | 7070 | 8080 |
| node2 | 7071 | 8081 |
| node3 | 7072 | 8082 |
| Prometheus | — | 9090 |
| Grafana | — | 3000 |

### Node Discovery
Nodes discover each other via Docker service names:
```yaml
# config/node1.yaml
cluster:
  peers:
    - "node2:7070"
    - "node3:7070"
```

## 5. Core Data Structures (Go)

Not applicable — this feature is infrastructure configuration, not Go code.

## 6. Public Interfaces

### Dockerfile
```dockerfile
# Stage 1: Build
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /goquorum ./cmd/quorum

# Stage 2: Runtime (minimal image)
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /goquorum /goquorum
COPY --from=builder /src/config /config
USER nonroot:nonroot
EXPOSE 7070 8080
ENTRYPOINT ["/goquorum"]
CMD ["--config", "/config/config.yaml"]
```

### docker-compose.yaml (excerpt)
```yaml
version: "3.9"
services:
  node1:
    build: .
    command: ["--config", "/config/node1.yaml"]
    volumes:
      - ./config:/config:ro
      - node1-data:/data
    ports:
      - "7070:7070"
      - "8080:8080"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/health/ready"]
      interval: 10s
      timeout: 5s
      retries: 3
      start_period: 5s
    networks: [goquorum-net]

  prometheus:
    image: prom/prometheus:v2.51.0
    volumes:
      - ./config/prometheus.yaml:/etc/prometheus/prometheus.yml:ro
      - prometheus-data:/prometheus
    ports:
      - "9090:9090"
    networks: [goquorum-net]

  grafana:
    image: grafana/grafana:10.4.0
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
      - GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH=/etc/grafana/provisioning/dashboards/goquorum.json
    volumes:
      - ./config/grafana/provisioning:/etc/grafana/provisioning:ro
      - grafana-data:/var/lib/grafana
    ports:
      - "3000:3000"
    networks: [goquorum-net]

volumes:
  node1-data:
  node2-data:
  node3-data:
  prometheus-data:
  grafana-data:

networks:
  goquorum-net:
```

### Prometheus scrape config
```yaml
# config/prometheus.yaml
scrape_configs:
  - job_name: "goquorum"
    static_configs:
      - targets: ["node1:8080", "node2:8080", "node3:8080"]
    metrics_path: /metrics
    scrape_interval: 15s
```

### Grafana Dashboard Panels
| Panel | Metric |
|-------|--------|
| Requests/sec (read/write) | `rate(goquorum_coordinator_put_total[1m])` |
| Put/Get latency P99 | `histogram_quantile(0.99, goquorum_coordinator_put_duration_seconds_bucket)` |
| Quorum error rate | `rate(goquorum_coordinator_put_total{result="quorum_error"}[1m])` |
| Active nodes | `goquorum_fd_active_peer_count` |
| Sibling count P95 | `histogram_quantile(0.95, goquorum_coordinator_sibling_count_bucket)` |
| Storage disk usage | `goquorum_storage_disk_usage_bytes` |
| Anti-entropy keys exchanged | `rate(goquorum_anti_entropy_keys_exchanged_total[5m])` |
| GC deleted keys/hr | `rate(goquorum_gc_deleted_keys_total[1h])` |

## 7. Internal Algorithms

### Health Check Script
The `wget` health check polls `/health/ready` which returns HTTP 200 only when:
1. Storage engine is open
2. Coordinator is initialized
3. At least `min_peers` peers are ACTIVE

During initial cluster bootstrap, nodes may briefly fail readiness until they establish heartbeats with peers. `start_period: 5s` gives nodes time to bootstrap before health failures count against `retries`.

### Node Startup Order
`docker compose up` starts all services in parallel. Each node retries peer connections with exponential backoff. The cluster is fully operational once all nodes achieve mutual heartbeat connectivity (typically within 5–10 seconds for a fresh start).

## 8. Persistence Model

Each node's storage is mounted as a named Docker volume (`node1-data`, etc.) to persist data across container restarts. The volumes survive `docker compose down` but are removed by `docker compose down -v`.

## 9. Concurrency Model

Not applicable — this is infrastructure configuration.

## 10. Configuration

### Node config file structure for 3-node cluster:
```yaml
# config/node1.yaml
node:
  id: "node1"
  grpc_addr: ":7070"
  http_addr: ":8080"

cluster:
  n: 3
  r: 2
  w: 2
  peers:
    - "node2:7070"
    - "node3:7070"

storage:
  data_dir: /data
  sync_writes: true
  cache_size_mb: 256

observability:
  log:
    level: info
    format: json
```

## 11. Observability

The deployment ships with:
- Prometheus scraping all nodes every 15 seconds
- Grafana at `http://localhost:3000` (admin/admin) with pre-loaded GoQuorum dashboard
- Docker compose logs (`docker compose logs -f node1`) for structured JSON log streaming
- All nodes write structured JSON logs to stdout (captured by Docker)

## 12. Testing Strategy

- **Docker build test**:
  - `docker build -t goquorum:test .` — assert build succeeds, image size < 20MB
- **Compose smoke test**:
  - `docker compose up -d && sleep 10` — assert all 3 nodes pass health check
  - `curl http://localhost:8080/health/ready` — assert HTTP 200
  - `curl -X PUT http://localhost:8080/v1/keys/test -d '{"value":"hello"}'` — assert 200
  - `curl http://localhost:8080/v1/keys/test` — assert "hello" in response
  - `docker compose down`
- **Persistence test**:
  - Write key, `docker compose restart node1`, assert key still readable after restart
- **Kubernetes manifest test** (if applicable):
  - Apply manifests to a local cluster (kind/minikube), assert StatefulSet pods reach Running state

## 13. Open Questions

- Should we provide a Helm chart for production Kubernetes deployments?
- Should the Grafana dashboard be published to grafana.com for community use?
