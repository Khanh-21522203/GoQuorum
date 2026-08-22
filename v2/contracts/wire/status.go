package wire

import (
	"errors"
	"fmt"

	"goquorum.io/v2/contracts/quorumerr"
)

// StatusCode is a small, wire-stable numeric encoding of the error.
type StatusCode uint16

const (
	StatusOK            StatusCode = 0
	StatusKeyNotFound   StatusCode = 1
	StatusCorruptedData StatusCode = 2
	StatusStorageClosed StatusCode = 3
	StatusStorageFull   StatusCode = 4
	StatusStorageIO     StatusCode = 5
	StatusUnknownError  StatusCode = 65535
)

// String returns the human-readable name of the status code.
func (s StatusCode) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusKeyNotFound:
		return "KeyNotFound"
	case StatusCorruptedData:
		return "CorruptedData"
	case StatusStorageClosed:
		return "StorageClosed"
	case StatusStorageFull:
		return "StorageFull"
	case StatusStorageIO:
		return "StorageIO"
	case StatusUnknownError:
		return "UnknownError"
	default:
		return "UNKNOWN"
	}
}

// StatusCodeFromError maps an engine/storage error to its wire status code.
func StatusCodeFromError(err error) StatusCode {
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(err, quorumerr.ErrKeyNotFound):
		return StatusKeyNotFound
	case errors.Is(err, quorumerr.ErrCorruptedData):
		return StatusCorruptedData
	case errors.Is(err, quorumerr.ErrStorageClosed):
		return StatusStorageClosed
	case errors.Is(err, quorumerr.ErrStorageFull):
		return StatusStorageFull
	case errors.Is(err, quorumerr.ErrStorageIO):
		return StatusStorageIO
	default:
		return StatusUnknownError
	}
}

// StatusCodeToError is the inverse of StatusCodeFromError.
func StatusCodeToError(code StatusCode) error {
	switch code {
	case StatusOK:
		return nil
	case StatusKeyNotFound:
		return quorumerr.ErrKeyNotFound
	case StatusCorruptedData:
		return quorumerr.ErrCorruptedData
	case StatusStorageClosed:
		return quorumerr.ErrStorageClosed
	case StatusStorageFull:
		return quorumerr.ErrStorageFull
	case StatusStorageIO:
		return quorumerr.ErrStorageIO
	default:
		return fmt.Errorf("wire: unrecognized status code %d", uint16(code))
	}
}
