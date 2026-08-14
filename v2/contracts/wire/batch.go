package wire

// TODO(v2): replace with buf-generated code (proto/ + buf.gen.yaml).

// BatchGetRequest is the wire shape of a batched KV Get request.
type BatchGetRequest struct {
	Keys    [][]byte
	Options GetOptions
}

// BatchGetResult is a single key's outcome within a BatchGetResponse.
type BatchGetResult struct {
	Key      []byte
	Siblings []Sibling
	Error    string
}

// BatchGetResponse is the wire shape of a batched KV Get response.
type BatchGetResponse struct {
	Results []BatchGetResult
}

// BatchPutItem is a single key/value/context triple within a
// BatchPutRequest.
type BatchPutItem struct {
	Key     []byte
	Value   []byte
	Context Context
}

// BatchPutRequest is the wire shape of a batched KV Put request.
type BatchPutRequest struct {
	Items   []BatchPutItem
	Options PutOptions
}

// BatchPutResult is a single key's outcome within a BatchPutResponse.
type BatchPutResult struct {
	Key     []byte
	Context Context
	Error   string
}

// BatchPutResponse is the wire shape of a batched KV Put response.
type BatchPutResponse struct {
	Results []BatchPutResult
}
