// Package gossip propagates membership state between peers using a
// randomized fan-out exchange.
//
// v1's gossip exchanged state over a hard-coded net/http + encoding/json
// call (internal/cluster/gossip.go). engine may not import net/http (see
// CONVENTIONS.md), so v2 routes the exchange through the transport.Transport
// port instead.
//
// (v1: internal/cluster/gossip.go)
package gossip
