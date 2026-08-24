// Package journal implements an append-only, io_uring-native Write-Ahead Log
// (WAL) storing raw key-value pairs ([]byte -> []byte).
//
// # Architecture
//
//   - On-Disk WAL: Sequential append-only file of CRC32-checksummed KV records.
//   - In-Memory Index: Sparse map of active keys to on-disk offset and length.
//   - Startup Recovery: Sequential replay scans valid records and rebuilds index.
//   - Zero Locks: Pure single-threaded reactor execution (io_uring pwrite/pread).
//
// # Record Binary Layout
//
// 0               4          8          10              10+KeyLen
// ┌───────────────┬──────────┬──────────┬───────────────┬──────────────────────┐
// │ Length uint32 │  CRC32   │  KeyLen  │      Key      │    Value (Payload)   │
// │ (Excl Length) │ (uint32) │ (uint16) │ (KeyLen bytes)│   (variable bytes)   │
// └───────────────┴──────────┴──────────┴───────────────┴──────────────────────┘
//
// # Ownership Contract
//
// Store submits io_uring operations and registers completion callbacks.
// The reactor driving the runtime dispatches completions:
//
//	store, _ := journal.Open(rt, opts)
//	r.SetEventHandler(store.HandleCompletion)
//	go r.Run()
package journal
