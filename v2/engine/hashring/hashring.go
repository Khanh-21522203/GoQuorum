package hashring

import (
	"errors"
	"fmt"
	"hash/fnv"
	"sort"

	"goquorum.io/v2/contracts/node"
)

// Errors returned by ring mutation and lookup operations.
var (
	// ErrNodeExists is returned by AddNode when the node's ID is already
	// present on the ring.
	ErrNodeExists = errors.New("hashring: node already exists")
	// ErrNodeNotFound is returned by RemoveNode when the given ID is not
	// present on the ring.
	ErrNodeNotFound = errors.New("hashring: node not found")
	// ErrEmptyRing is returned by lookups performed against a ring with no
	// physical nodes.
	ErrEmptyRing = errors.New("hashring: ring has no nodes")
)

// virtualNode represents a virtual node's position on the ring.
type virtualNode struct {
	Hash     uint64      // Position on the ring.
	NodeID   node.NodeID // Physical node owning this vnode.
	VNodeIdx int         // Virtual node index (0 to vnodeCount-1).
}

// HashRing implements consistent hashing with virtual nodes.
type HashRing struct {
	vnodes     []virtualNode // Sorted ascending by Hash.
	nodes      map[node.NodeID]*node.Node
	vnodeCount int
}

// NewHashRing creates a new hash ring with vnodeCount virtual nodes per
// physical node. If vnodeCount <= 0, it defaults to 256.
func NewHashRing(vnodeCount int) *HashRing {
	if vnodeCount <= 0 {
		vnodeCount = 256
	}
	return &HashRing{
		vnodes:     make([]virtualNode, 0),
		nodes:      make(map[node.NodeID]*node.Node),
		vnodeCount: vnodeCount,
	}
}

// AddNode adds a physical node and creates its virtual nodes on the ring.
// It returns ErrNodeExists if n.ID is already present.
func (hr *HashRing) AddNode(n *node.Node) error {
	if _, exists := hr.nodes[n.ID]; exists {
		return ErrNodeExists
	}

	hr.nodes[n.ID] = n

	for i := 0; i < hr.vnodeCount; i++ {
		hr.vnodes = append(hr.vnodes, virtualNode{
			Hash:     hashVNode(n.ID, i),
			NodeID:   n.ID,
			VNodeIdx: i,
		})
	}

	sort.Slice(hr.vnodes, func(i, j int) bool {
		return hr.vnodes[i].Hash < hr.vnodes[j].Hash
	})

	return nil
}

// RemoveNode removes a physical node and all of its virtual nodes from the
// ring. It returns ErrNodeNotFound if id is not present.
func (hr *HashRing) RemoveNode(id node.NodeID) error {
	if _, exists := hr.nodes[id]; !exists {
		return ErrNodeNotFound
	}

	delete(hr.nodes, id)

	filtered := hr.vnodes[:0]
	for _, v := range hr.vnodes {
		if v.NodeID != id {
			filtered = append(filtered, v)
		}
	}
	hr.vnodes = filtered

	return nil
}

// GetNode retrieves a physical node by its ID.
func (hr *HashRing) GetNode(id node.NodeID) (*node.Node, bool) {
	n, ok := hr.nodes[id]
	return n, ok
}

// UpdateNodeState updates the health state of a node on the ring.
func (hr *HashRing) UpdateNodeState(id node.NodeID, state node.NodeState) error {
	n, exists := hr.nodes[id]
	if !exists {
		return ErrNodeNotFound
	}
	n.UpdateState(state)
	return nil
}

// GetPreferenceList returns up to n distinct physical nodes responsible for
// key, walking the ring clockwise starting from key's hash position. If
// fewer than n physical nodes exist, it returns all of them. It returns
// ErrEmptyRing if the ring has no physical nodes.
func (hr *HashRing) GetPreferenceList(key string, n int) ([]node.NodeID, error) {
	if len(hr.nodes) == 0 {
		return nil, ErrEmptyRing
	}
	if n > len(hr.nodes) {
		n = len(hr.nodes)
	}

	keyHash := hashKey(key)

	// Locate the first vnode at or after keyHash; wrap to the start of the
	// ring if keyHash falls after every vnode.
	start := sort.Search(len(hr.vnodes), func(i int) bool {
		return hr.vnodes[i].Hash >= keyHash
	})
	if start >= len(hr.vnodes) {
		start = 0
	}

	result := make([]node.NodeID, 0, n)
	seen := make(map[node.NodeID]struct{}, n)

	for i := 0; len(result) < n && i < len(hr.vnodes); i++ {
		id := hr.vnodes[(start+i)%len(hr.vnodes)].NodeID
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}

	return result, nil
}

// GetPrimaryNode returns the first node in key's preference list.
func (hr *HashRing) GetPrimaryNode(key string) (node.NodeID, error) {
	ids, err := hr.GetPreferenceList(key, 1)
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// Nodes returns a snapshot of all physical nodes currently on the ring.
func (hr *HashRing) Nodes() []*node.Node {
	out := make([]*node.Node, 0, len(hr.nodes))
	for _, n := range hr.nodes {
		out = append(out, n)
	}
	return out
}

// Size returns the number of physical nodes currently on the ring.
func (hr *HashRing) Size() int {
	return len(hr.nodes)
}

// hashKey hashes an arbitrary string onto the ring's 64-bit hash space.
func hashKey(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}

// hashVNode hashes virtual node idx of physical node id onto the ring.
// Mixing the index into the hashed string spreads each physical node's
// vnodes across the ring instead of clustering them at one point.
func hashVNode(id node.NodeID, idx int) uint64 {
	return hashKey(fmt.Sprintf("%s:%d", id, idx))
}
