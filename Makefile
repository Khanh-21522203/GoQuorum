.PHONY: proto build test clean run

# Proto generation
proto:
	@echo "Generating proto files..."
	cd api/proto && buf generate

# Build
build:
	@echo "Building GoQuorum..."
	go build -o bin/goquorum ./cmd/quorum

# Run
run: build
	./bin/goquorum --config config.yaml

# Test
test:
	go test -v ./...

# Clean
clean:
	rm -rf bin/
	rm -rf api/v1/*.pb.go
	rm -rf api/v1/*.pb.gw.go

# Install dependencies
deps:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
	go install github.com/bufbuild/buf/cmd/buf@latest

# Format
fmt:
	go fmt ./...

# Lint
lint:
	golangci-lint run

# Docker build
docker-build:
	docker build -t goquorum:latest .

# Docker compose up
docker-up:
	docker-compose up -d

# Docker compose down
docker-down:
	docker-compose down
