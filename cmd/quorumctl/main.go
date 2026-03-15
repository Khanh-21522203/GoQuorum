// quorumctl is a CLI tool for interacting with a GoQuorum cluster.
//
// Usage: quorumctl [--addr host:port] <command> [args...]
//
// Commands:
//
//	get <key>                     Get value for key
//	put <key> <value>             Put value for key
//	delete <key> <context>        Delete key (context is base64-encoded JSON vclock)
//	status                        Show cluster health
//	ring                          Show cluster info
//	key-info <key>                Show replica info for key
//	compact                       Trigger storage compaction
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	pb "GoQuorum/api"
	"GoQuorum/internal/vclock"
	"GoQuorum/pkg/client"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "localhost:7070", "GoQuorum server address (host:port)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: quorumctl [--addr host:port] <command> [args...]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  get <key>                   Get value for key\n")
		fmt.Fprintf(os.Stderr, "  put <key> <value>           Put value for key\n")
		fmt.Fprintf(os.Stderr, "  delete <key> <context>      Delete key (context is base64-encoded JSON vclock)\n")
		fmt.Fprintf(os.Stderr, "  status                      Show cluster health\n")
		fmt.Fprintf(os.Stderr, "  ring                        Show cluster info\n")
		fmt.Fprintf(os.Stderr, "  key-info <key>              Show replica info for key\n")
		fmt.Fprintf(os.Stderr, "  compact                     Trigger storage compaction\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	switch cmd {
	case "get":
		runGet(*addr, cmdArgs)
	case "put":
		runPut(*addr, cmdArgs)
	case "delete":
		runDelete(*addr, cmdArgs)
	case "status":
		runStatus(*addr)
	case "ring":
		runRing(*addr)
	case "key-info":
		runKeyInfo(*addr, cmdArgs)
	case "compact":
		runCompact(*addr)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		flag.Usage()
		os.Exit(1)
	}
}

// ── get ───────────────────────────────────────────────────────────────────────

func runGet(addr string, args []string) {
	if len(args) != 1 {
		fatalf("usage: quorumctl get <key>\n")
	}
	key := args[0]

	c := mustNewClient(addr)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	siblings, err := c.Get(ctx, []byte(key))
	if err != nil {
		fatalf("get failed: %v\n", err)
	}

	if len(siblings) == 0 {
		fmt.Println("(no values found)")
		return
	}

	for i, sib := range siblings {
		ctxB64, err := vclockToBase64(sib.Context)
		if err != nil {
			fatalf("encode context: %v\n", err)
		}
		fmt.Printf("sibling[%d]:\n", i)
		fmt.Printf("  value:   %s\n", string(sib.Value))
		fmt.Printf("  context: %s\n", ctxB64)
		fmt.Printf("  ts:      %d\n", sib.Timestamp)
		if sib.Tombstone {
			fmt.Printf("  tombstone: true\n")
		}
	}
}

// ── put ───────────────────────────────────────────────────────────────────────

func runPut(addr string, args []string) {
	if len(args) != 2 {
		fatalf("usage: quorumctl put <key> <value>\n")
	}
	key := args[0]
	value := args[1]

	c := mustNewClient(addr)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Blind write: empty vector clock
	newVC, err := c.Put(ctx, []byte(key), []byte(value), vclock.NewVectorClock())
	if err != nil {
		fatalf("put failed: %v\n", err)
	}

	ctxB64, err := vclockToBase64(newVC)
	if err != nil {
		fatalf("encode context: %v\n", err)
	}

	fmt.Printf("ok\ncontext: %s\n", ctxB64)
}

// ── delete ────────────────────────────────────────────────────────────────────

func runDelete(addr string, args []string) {
	if len(args) != 2 {
		fatalf("usage: quorumctl delete <key> <context>\n  <context> is the base64-encoded JSON vclock returned by get or put\n")
	}
	key := args[0]
	ctxArg := args[1]

	vc, err := base64ToVClock(ctxArg)
	if err != nil {
		fatalf("decode context: %v\n", err)
	}

	c := mustNewClient(addr)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.Delete(ctx, []byte(key), vc); err != nil {
		fatalf("delete failed: %v\n", err)
	}

	fmt.Println("ok")
}

// ── status ────────────────────────────────────────────────────────────────────

func runStatus(addr string) {
	adminClient := mustNewAdminClient(addr)
	defer adminClient.conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := adminClient.client.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		fatalf("health check failed: %v\n", err)
	}

	fmt.Printf("status:  %s\n", resp.GetStatus())
	fmt.Printf("node_id: %s\n", resp.GetNodeId())
	fmt.Printf("version: %s\n", resp.GetVersion())
	fmt.Printf("uptime:  %ds\n", resp.GetUptimeSeconds())

	if len(resp.GetChecks()) > 0 {
		fmt.Println("checks:")
		for name, check := range resp.GetChecks() {
			fmt.Printf("  %s: %s", name, check.GetStatus())
			if check.GetError() != "" {
				fmt.Printf(" (error: %s)", check.GetError())
			}
			if check.GetLatencyMs() > 0 {
				fmt.Printf(" latency=%dms", check.GetLatencyMs())
			}
			if check.GetPeersTotal() > 0 {
				fmt.Printf(" peers=%d/%d", check.GetPeersActive(), check.GetPeersTotal())
			}
			fmt.Println()
		}
	}
}

// ── ring ──────────────────────────────────────────────────────────────────────

func runRing(addr string) {
	adminClient := mustNewAdminClient(addr)
	defer adminClient.conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := adminClient.client.ClusterInfo(ctx, &pb.ClusterInfoRequest{})
	if err != nil {
		fatalf("cluster info failed: %v\n", err)
	}

	fmt.Printf("node_id:        %s\n", resp.GetNodeId())
	fmt.Printf("cluster_status: %s\n", resp.GetClusterStatus())
	fmt.Printf("peers (%d):\n", len(resp.GetPeers()))
	for _, peer := range resp.GetPeers() {
		fmt.Printf("  node_id=%s addr=%s status=%s last_seen=%d latency_ms=%d\n",
			peer.GetNodeId(),
			peer.GetAddress(),
			peer.GetStatus(),
			peer.GetLastSeenUnix(),
			peer.GetLatencyMs(),
		)
	}
}

// ── key-info ──────────────────────────────────────────────────────────────────

func runKeyInfo(addr string, args []string) {
	if len(args) != 1 {
		fatalf("usage: quorumctl key-info <key>\n")
	}
	key := args[0]

	adminClient := mustNewAdminClient(addr)
	defer adminClient.conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := adminClient.client.KeyInfo(ctx, &pb.KeyInfoRequest{Key: []byte(key)})
	if err != nil {
		fatalf("key-info failed: %v\n", err)
	}

	fmt.Printf("key: %s\n", string(resp.GetKey()))
	for _, replica := range resp.GetReplicas() {
		fmt.Printf("  replica node_id=%s has_key=%v", replica.GetNodeId(), replica.GetHasKey())
		if replica.GetError() != "" {
			fmt.Printf(" error=%s", replica.GetError())
		}
		fmt.Println()
		for j, sib := range replica.GetSiblings() {
			fmt.Printf("    sibling[%d]: timestamp=%d value_size=%d tombstone=%v\n",
				j, sib.GetTimestamp(), sib.GetValueSize(), sib.GetTombstone())
		}
	}
}

// ── compact ───────────────────────────────────────────────────────────────────

func runCompact(addr string) {
	adminClient := mustNewAdminClient(addr)
	defer adminClient.conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := adminClient.client.TriggerCompaction(ctx, &pb.TriggerCompactionRequest{})
	if err != nil {
		fatalf("compact failed: %v\n", err)
	}

	if resp.GetStarted() {
		fmt.Println("compaction started")
	} else {
		fmt.Println("compaction not started (already running or not supported)")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustNewClient(addr string) *client.Client {
	cfg := client.DefaultClientConfig(addr)
	c, err := client.NewClient(cfg)
	if err != nil {
		fatalf("connect to %s: %v\n", addr, err)
	}
	return c
}

type adminClientWrapper struct {
	conn   *grpc.ClientConn
	client pb.GoQuorumAdminClient
}

func mustNewAdminClient(addr string) *adminClientWrapper {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr, //nolint:staticcheck
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		fatalf("connect to admin %s: %v\n", addr, err)
	}

	return &adminClientWrapper{
		conn:   conn,
		client: pb.NewGoQuorumAdminClient(conn),
	}
}

// vclockToBase64 serialises a VectorClock as base64(JSON).
func vclockToBase64(vc vclock.VectorClock) (string, error) {
	data, err := json.Marshal(vc)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// base64ToVClock deserialises a VectorClock from base64(JSON).
func base64ToVClock(encoded string) (vclock.VectorClock, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return vclock.VectorClock{}, fmt.Errorf("base64 decode: %w", err)
	}
	var vc vclock.VectorClock
	if err := json.Unmarshal(data, &vc); err != nil {
		return vclock.VectorClock{}, fmt.Errorf("json unmarshal: %w", err)
	}
	return vc, nil
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}
