package contracts

import "errors"

// ErrNotImplemented is returned by scaffold stubs that have not yet been implemented.
// It is the single shared sentinel every module reuses during the scaffold phase.
var ErrNotImplemented = errors.New("not implemented")
