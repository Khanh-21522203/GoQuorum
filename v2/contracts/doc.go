// Package contracts holds the shared, dependency-free vocabulary of GoQuorum v2:
// value types, sentinel errors, and vector clocks used by every other module. It
// imports only the standard library, so it can be depended on from anywhere without
// pulling in I/O, wire formats, or transport concerns. Its subpackages (node,
// quorumerr, vclock, wire) carry the individual type families.
package contracts
