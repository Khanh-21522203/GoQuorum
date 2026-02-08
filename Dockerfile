# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o goquorum ./cmd/quorum

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN adduser -D -g '' goquorum

# Create directories
RUN mkdir -p /var/lib/goquorum/data /etc/goquorum && \
    chown -R goquorum:goquorum /var/lib/goquorum /etc/goquorum

# Copy binary
COPY --from=builder /app/goquorum /usr/local/bin/goquorum

# Copy default config
COPY --from=builder /app/config.yaml /etc/goquorum/config.yaml

# Switch to non-root user
USER goquorum

# Expose ports
# 7070 - gRPC
# 8080 - HTTP (health, metrics)
EXPOSE 7070 8080

# Data volume
VOLUME ["/var/lib/goquorum"]

# Entry point
ENTRYPOINT ["/usr/local/bin/goquorum"]
CMD ["--config", "/etc/goquorum/config.yaml"]
