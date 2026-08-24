// Package adapter provides Layer 3 adapters for storage and transport subsystems in GoQuorum.
//
// It bridges the low-level asynchronous I/O primitives (journal.Store, iouring.Client)
// with high-level domain ports (Storage, Transport) consumed by the engine's coordinator,
// anti-entropy, read repair, gossip, and failure detector subsystems.
package adapter
