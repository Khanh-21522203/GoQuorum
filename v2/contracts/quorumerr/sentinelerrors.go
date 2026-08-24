package quorumerr

import "errors"

// Storage-layer sentinel errors returned by engine/storage implementations.
var (
	ErrKeyNotFound   = errors.New("key not found")
	ErrCorruptedData = errors.New("data corruption detected")
	ErrStorageClosed = errors.New("storage is closed")
	ErrStorageFull   = errors.New("storage full (disk space exhausted)")
	ErrStorageIO     = errors.New("storage I/O error")
)
