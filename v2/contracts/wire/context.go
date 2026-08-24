package wire

// TODO(v2): replace with buf-generated code (proto/ + buf.gen.yaml).

// Context is the wire form of a causal context (vector clock) exchanged
// between clients and servers.
type Context struct {
	Entries []ContextEntry
}

// ContextEntry is a single node/counter pair within a Context.
type ContextEntry struct {
	NodeID  string
	Counter uint64
}
