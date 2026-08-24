package wire

// TODO(v2): replace with buf-generated code (proto/ + buf.gen.yaml).

// PutOptions carries per-request write tuning for a Put.
type PutOptions struct {
	W          uint32
	TimeoutMs  uint32
	TTLSeconds int64
}

// PutRequest is the wire shape of a KV Put request.
type PutRequest struct {
	Key     []byte
	Value   []byte
	Context Context
	Options PutOptions
}

// PutResponse is the wire shape of a KV Put response: the causal context
// after the write.
type PutResponse struct {
	Context Context
}

// GetOptions carries per-request read tuning for a Get.
type GetOptions struct {
	R         uint32
	TimeoutMs uint32
}

// GetRequest is the wire shape of a KV Get request.
type GetRequest struct {
	Key     []byte
	Options GetOptions
}

// Sibling is a single conflicting version of a value returned by Get.
type Sibling struct {
	Value     []byte
	Context   Context
	Tombstone bool
	Timestamp int64
}

// GetResponse is the wire shape of a KV Get response. Multiple siblings
// indicate an unresolved conflict.
type GetResponse struct {
	Siblings []Sibling
}

// DeleteOptions carries per-request write tuning for a Delete.
type DeleteOptions struct {
	W         uint32
	TimeoutMs uint32
}

// DeleteRequest is the wire shape of a KV Delete request. Context is
// required and must come from a prior read.
type DeleteRequest struct {
	Key     []byte
	Context Context
	Options DeleteOptions
}

// DeleteResponse is the wire shape of a KV Delete response (empty).
type DeleteResponse struct{}
