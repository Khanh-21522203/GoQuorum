package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_Load_ValidYAML(t *testing.T) {
	content := `
node:
  node_id: "node-1"
  data_dir: "/tmp/quorum_test"
  log_level: "debug"

cluster:
  node_id: "node-1"
  listen_addr: ":7070"
  members:
    - id: "node-1"
      addr: "127.0.0.1:7070"
      http_addr: "127.0.0.1:8080"
    - id: "node-2"
      addr: "127.0.0.1:7071"
      http_addr: "127.0.0.1:8081"

quorum:
  n: 2
  r: 1
  w: 1
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Node.NodeID != "node-1" {
		t.Errorf("expected node-1, got %s", cfg.Node.NodeID)
	}
	if cfg.QuorumConfig.N != 2 || cfg.QuorumConfig.R != 1 || cfg.QuorumConfig.W != 1 {
		t.Errorf("unexpected quorum config: %+v", cfg.QuorumConfig)
	}
	if cfg.Gossip.FanOut != 3 {
		t.Errorf("expected default fanout 3, got %d", cfg.Gossip.FanOut)
	}
}

func TestConfig_Load_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestConfig_Load_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.yaml")
	if err := os.WriteFile(configPath, []byte("node: [invalid: yaml"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestConfig_Load_ValidationFailure(t *testing.T) {
	content := `
node:
  node_id: "" # Missing required node_id
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected validation error for empty node_id")
	}
}
