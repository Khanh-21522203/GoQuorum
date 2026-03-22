// benchmarks/main.go — GoQuorum live-cluster benchmark tool.
//
// Connects to a running GoQuorum cluster (docker-compose or bare-metal) via
// gRPC and measures Put, Get, Round-trip, Mixed, and Replication-lag latency
// with P50 / P90 / P99 / P99.9 percentiles and QPS.
//
// Usage:
//
//	go run ./benchmarks/ \
//	  -nodes localhost:7071,localhost:7072,localhost:7073 \
//	  -concurrency 16 \
//	  -duration 30s
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"GoQuorum/internal/vclock"
	"GoQuorum/pkg/client"
)

// ── Config ────────────────────────────────────────────────────────────────────

type BenchConfig struct {
	addrs       []string
	concurrency int
	duration    time.Duration
	warmup      time.Duration
	valueSize   int
	preloadKeys int
}

// ── Worker pool ───────────────────────────────────────────────────────────────

// runWorkload runs fn concurrently until cfg.duration elapses.
// Each goroutine picks a client round-robin. Returns a Result with
// latency samples for successful operations only.
func runWorkload(
	name string,
	cfg BenchConfig,
	clients []*client.Client,
	fn func(ctx context.Context, c *client.Client, seq int64) error,
) *Result {
	nClients := int64(len(clients))
	hist := &Histogram{}
	var errCount atomic.Int64
	var seq atomic.Int64

	run := func(d time.Duration, record bool) {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()

		var wg sync.WaitGroup
		for i := 0; i < cfg.concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
					}
					s := seq.Add(1)
					c := clients[s%nClients]
					t0 := time.Now()
					err := fn(context.Background(), c, s)
					lat := time.Since(t0)
					if err != nil {
						errCount.Add(1)
					} else if record {
						hist.Record(lat)
					}
				}
			}()
		}
		wg.Wait()
	}

	if cfg.warmup > 0 {
		fmt.Printf("  warmup %s ...\n", cfg.warmup)
		seq.Store(0)
		run(cfg.warmup, false)
		seq.Store(0) // reset seq so workload keys don't collide with warmup
	}

	fmt.Printf("  running %s ...\n", cfg.duration)
	start := time.Now()
	run(cfg.duration, true)

	return &Result{
		Name:     name,
		Duration: time.Since(start),
		Hist:     hist,
		Errors:   errCount.Load(),
	}
}

// ── Workloads ─────────────────────────────────────────────────────────────────

func workloadPut(cfg BenchConfig, clients []*client.Client, value []byte) *Result {
	return runWorkload("Put  (N=3, W=2)", cfg, clients,
		func(ctx context.Context, c *client.Client, seq int64) error {
			key := []byte(fmt.Sprintf("bench:put:%d", seq))
			opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_, err := c.Put(opCtx, key, value, vclock.NewVectorClock())
			return err
		})
}

func workloadGet(cfg BenchConfig, clients []*client.Client, preloaded [][]byte) *Result {
	n := int64(len(preloaded))
	return runWorkload("Get  (N=3, R=2)", cfg, clients,
		func(ctx context.Context, c *client.Client, seq int64) error {
			key := preloaded[seq%n]
			opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_, err := c.Get(opCtx, key)
			return err
		})
}

func workloadRoundTrip(cfg BenchConfig, clients []*client.Client, value []byte) *Result {
	return runWorkload("Round-trip Put+Get (N=3, W=2, R=2)", cfg, clients,
		func(ctx context.Context, c *client.Client, seq int64) error {
			key := []byte(fmt.Sprintf("bench:rt:%d:%d", seq, rand.Int63()))
			putCtx, putCancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := c.Put(putCtx, key, value, vclock.NewVectorClock())
			putCancel()
			if err != nil {
				return err
			}
			getCtx, getCancel := context.WithTimeout(ctx, 5*time.Second)
			defer getCancel()
			_, err = c.Get(getCtx, key)
			return err
		})
}

func workloadMixed(cfg BenchConfig, clients []*client.Client, value []byte, preloaded [][]byte) *Result {
	n := int64(len(preloaded))
	return runWorkload("Mixed 80% Get / 20% Put (N=3)", cfg, clients,
		func(ctx context.Context, c *client.Client, seq int64) error {
			opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if seq%5 == 0 { // 20 % writes
				key := []byte(fmt.Sprintf("bench:mix:w:%d", seq))
				_, err := c.Put(opCtx, key, value, vclock.NewVectorClock())
				return err
			}
			key := preloaded[seq%n]
			_, err := c.Get(opCtx, key)
			return err
		})
}

// workloadReplicationLag measures how long after a Put returns (W=2) before a
// Get from a different coordinator also succeeds.  node1 is the write
// coordinator; otherClients are clients connected to the remaining nodes.
// Each sample = lag observed at the writer.
func workloadReplicationLag(cfg BenchConfig, node1 *client.Client, otherClients []*client.Client, value []byte) *Result {
	hist := &Histogram{}
	var errCount atomic.Int64
	var seq atomic.Int64

	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	// Use lower concurrency so polling doesn't swamp the cluster.
	concurrency := cfg.concurrency / 2
	if concurrency < 1 {
		concurrency = 1
	}

	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				s := seq.Add(1)
				key := []byte(fmt.Sprintf("bench:lag:%d:%d", s, rand.Int63()))

				// Write to node1. When this returns, W=2 nodes have the data.
				putCtx, putCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, putErr := node1.Put(putCtx, key, value, vclock.NewVectorClock())
				putCancel()
				if putErr != nil {
					errCount.Add(1)
					continue
				}

				// Measure how long until ALL other-node coordinators can also
				// satisfy a Get for this key (they may need to fetch it from
				// the nodes that already have it).
				lagStart := time.Now()
				deadline := lagStart.Add(200 * time.Millisecond)

				confirmed := 0
				for time.Now().Before(deadline) {
					confirmed = 0
					for _, other := range otherClients {
						getCtx, getCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
						_, err := other.Get(getCtx, key)
						getCancel()
						if err == nil {
							confirmed++
						}
					}
					if confirmed == len(otherClients) {
						break
					}
					time.Sleep(50 * time.Microsecond)
				}

				if confirmed == len(otherClients) {
					hist.Record(time.Since(lagStart))
				} else {
					errCount.Add(1) // replication did not complete within deadline
				}
			}
		}()
	}

	wg.Wait()
	return &Result{
		Name:     fmt.Sprintf("Replication lag  (W=2 write on node1 → all %d nodes readable)", len(otherClients)+1),
		Duration: time.Since(start),
		Hist:     hist,
		Errors:   errCount.Load(),
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	nodesFlag := flag.String("nodes",
		"localhost:7071,localhost:7072,localhost:7073",
		"comma-separated list of gRPC node addresses (host:port)")
	concurrency := flag.Int("concurrency", 16, "concurrent workers per workload")
	duration := flag.Duration("duration", 30*time.Second, "measurement duration per workload")
	warmup := flag.Duration("warmup", 5*time.Second, "warmup duration before each workload")
	valueSize := flag.Int("value-size", 256, "value size in bytes")
	preloadKeys := flag.Int("preload-keys", 5000, "keys to pre-populate before get/mixed benchmarks")
	skipLag := flag.Bool("skip-lag", false, "skip replication-lag benchmark (needs ≥2 nodes)")
	flag.Parse()

	addrs := strings.Split(*nodesFlag, ",")

	fmt.Println("╔════════════════════════════════════════════════════╗")
	fmt.Println("║         GoQuorum Live-Cluster Benchmark            ║")
	fmt.Println("╚════════════════════════════════════════════════════╝")
	fmt.Printf("  nodes:        %s\n", *nodesFlag)
	fmt.Printf("  concurrency:  %d workers\n", *concurrency)
	fmt.Printf("  duration:     %s per workload\n", *duration)
	fmt.Printf("  warmup:       %s\n", *warmup)
	fmt.Printf("  value-size:   %d bytes\n", *valueSize)
	fmt.Printf("  preload-keys: %d\n\n", *preloadKeys)

	// ── Connect ───────────────────────────────────────────────────────────────
	clients := make([]*client.Client, 0, len(addrs))
	for _, addr := range addrs {
		c, err := client.NewClient(client.ClientConfig{
			Addr:           addr,
			DialTimeout:    10 * time.Second,
			RequestTimeout: 5 * time.Second,
			MaxRetries:     0, // no retries — we want to see raw errors
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: connect to %s: %v\n", addr, err)
			os.Exit(1)
		}
		defer c.Close() //nolint:gocritic // acceptable in main
		clients = append(clients, c)
		fmt.Printf("  ✓ connected to %s\n", addr)
	}
	fmt.Println()

	// ── Pre-populate keys for read workloads ──────────────────────────────────
	fmt.Printf("Pre-populating %d keys...\n", *preloadKeys)
	value := make([]byte, *valueSize)
	for i := range value {
		value[i] = byte('a' + i%26)
	}
	preloaded := make([][]byte, *preloadKeys)
	for i := 0; i < *preloadKeys; i++ {
		key := []byte(fmt.Sprintf("bench:preload:%d", i))
		preloaded[i] = key
		opCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := clients[0].Put(opCtx, key, value, vclock.NewVectorClock())
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "preload key %d: %v\n", i, err)
			os.Exit(1)
		}
	}
	fmt.Printf("Pre-populated %d keys\n\n", *preloadKeys)

	cfg := BenchConfig{
		addrs:       addrs,
		concurrency: *concurrency,
		duration:    *duration,
		warmup:      *warmup,
		valueSize:   *valueSize,
		preloadKeys: *preloadKeys,
	}

	// ── Run workloads ─────────────────────────────────────────────────────────
	var results []*Result

	fmt.Println("── Put ──────────────────────────────────────────────")
	results = append(results, workloadPut(cfg, clients, value))

	fmt.Println("── Get ──────────────────────────────────────────────")
	results = append(results, workloadGet(cfg, clients, preloaded))

	fmt.Println("── Round-trip ───────────────────────────────────────")
	results = append(results, workloadRoundTrip(cfg, clients, value))

	fmt.Println("── Mixed 80R/20W ────────────────────────────────────")
	results = append(results, workloadMixed(cfg, clients, value, preloaded))

	if !*skipLag && len(clients) >= 2 {
		fmt.Println("── Replication lag ──────────────────────────────────")
		results = append(results, workloadReplicationLag(cfg, clients[0], clients[1:], value))
	}

	// ── Print summary ─────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════╗")
	fmt.Println("║                    RESULTS                         ║")
	fmt.Println("╚════════════════════════════════════════════════════╝")
	fmt.Println()
	for _, r := range results {
		r.Print()
	}
}
