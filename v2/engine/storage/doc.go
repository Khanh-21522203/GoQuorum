// Package storage defines the value types exchanged with the storage layer
// (Sibling, SiblingSet, ScanFunc, Stats) and the Storage port: the interface
// implemented by concrete storage adapters (e.g. infra/storage/pebble).
//
// engine/storage is a PORT boundary: engine depends only on this interface,
// never on a concrete storage engine (v1: internal/storage/engine.go had a
// concrete *storage.Storage; v2 replaces it with this interface so engine
// stays free of I/O and external dependencies).
package storage
