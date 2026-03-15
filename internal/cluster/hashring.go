package cluster

import (
	"GoQuorum/internal/common"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/cespare/xxhash/v2"
)

var (
	ErrNodeExists   = errors.New("node already exists")
	ErrNodeNotFound = errors.New("node not found")
	ErrEmptyRing    = errors.New("hash ring is empty")
)

// VirtualNode represents a virtual node on the hash ring
type VirtualNode struct {
	Hash     uint64        // Position on the ring
	NodeID   common.NodeID // Physical node owning this vnode
	VNodeIdx int           // Virtual node index (0 to VirtualNodeCount-1)
}

// HashRing implements consistent hashing with virtual nodes
type HashRing struct {
	vnodes     []VirtualNode                  // Sorted by Hash
	nodes      map[common.NodeID]*common.Node // Physical nodes
	vnodeCount int                            // Virtual nodes per physical node
	mu         sync.RWMutex
}

// NewHashRing creates a new hash ring.
// If vnodeCount <= 0, it defaults to 256.
func NewHashRing(vnodeCount int) *HashRing {
	if vnodeCount <= 0 {
		vnodeCount = 256
	}
	return &HashRing{
		vnodes:     make([]VirtualNode, 0),
		nodes:      make(map[common.NodeID]*common.Node),
		vnodeCount: vnodeCount,
	}
}

// AddNode adds a physical node and creates its virtual nodes
func (hr *HashRing) AddNode(node *common.Node) error {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	if _, exists := hr.nodes[node.ID]; exists {
		return ErrNodeExists
	}

	hr.nodes[node.ID] = node

	// Create virtual nodes
	for i := 0; i < hr.vnodeCount; i++ {
		vnode := VirtualNode{
			Hash:     hr.hashVNode(node.ID, i),
			NodeID:   node.ID,
			VNodeIdx: i,
		}
		hr.vnodes = append(hr.vnodes, vnode)
	}

	// Re-sort after adding new vnodes
	sort.Slice(hr.vnodes, func(i, j int) bool {
		return hr.vnodes[i].Hash < hr.vnodes[j].Hash
	})

	return nil
}

// RemoveNode removes a physical node and all its virtual nodes
func (hr *HashRing) RemoveNode(nodeID common.NodeID) error {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	if _, exists := hr.nodes[nodeID]; !exists {
		return ErrNodeNotFound
	}

	delete(hr.nodes, nodeID)

	// Remove all virtual nodes belonging to this physical node
	filtered := hr.vnodes[:0] // Reuse slice, avoid allocation
	for _, vnode := range hr.vnodes {
		if vnode.NodeID != nodeID {
			filtered = append(filtered, vnode)
		}
	}
	hr.vnodes = filtered

	return nil
}

// GetPreferenceList returns N nodes responsible for a key
// Given: key K, replication factor N
//
// 1. hash_pos = xxHash64(K)
// 2. Find first virtual node position >= hash_pos (wrap around)
// 3. Identify physical node owning that vnode
// 4. Continue clockwise, collecting N distinct physical nodes
// 5. These N nodes are the "preference list" for key K
func (hr *HashRing) GetPreferenceList(key string, N int) ([]common.NodeID, error) {
	hr.mu.RLock()
	defer hr.mu.RUnlock()

	if len(hr.nodes) == 0 {
		return nil, ErrEmptyRing
	}

	// Limit N to available physical nodes
	if N > len(hr.nodes) {
		N = len(hr.nodes)
	}

	keyHash := xxhash.Sum64String(key)

	// Binary search for first vnode >= keyHash
	idx := sort.Search(len(hr.vnodes), func(i int) bool {
		return hr.vnodes[i].Hash >= keyHash
	})

	// Wrap around if needed
	if idx >= len(hr.vnodes) {
		idx = 0
	}

	result := make([]common.NodeID, 0, N)
	seen := make(map[common.NodeID]struct{}, N)

	// Walk clockwise collecting distinct physical nodes
	for i := 0; len(result) < N && i < len(hr.vnodes); i++ {
		vnodeIdx := (idx + i) % len(hr.vnodes)
		nodeID := hr.vnodes[vnodeIdx].NodeID

		if _, exists := seen[nodeID]; !exists {
			result = append(result, nodeID)
			seen[nodeID] = struct{}{}
		}
	}

	return result, nil
}

// GetExtendedPreferenceList returns up to N+extra distinct nodes for sloppy quorum.
// The first N nodes are the strict preference list; additional nodes are overflow candidates.
func (hr *HashRing) GetExtendedPreferenceList(key string, N, extra int) ([]common.NodeID, error) {
	hr.mu.RLock()
	defer hr.mu.RUnlock()

	if len(hr.nodes) == 0 {
		return nil, ErrEmptyRing
	}

	want := N + extra
	if want > len(hr.nodes) {
		want = len(hr.nodes)
	}

	keyHash := xxhash.Sum64String(key)
	idx := sort.Search(len(hr.vnodes), func(i int) bool {
		return hr.vnodes[i].Hash >= keyHash
	})
	if idx >= len(hr.vnodes) {
		idx = 0
	}

	result := make([]common.NodeID, 0, want)
	seen := make(map[common.NodeID]struct{}, want)

	for i := 0; len(result) < want && i < len(hr.vnodes); i++ {
		vnodeIdx := (idx + i) % len(hr.vnodes)
		nodeID := hr.vnodes[vnodeIdx].NodeID
		if _, exists := seen[nodeID]; !exists {
			result = append(result, nodeID)
			seen[nodeID] = struct{}{}
		}
	}

	return result, nil
}

// GetPrimaryNode returns the primary replica node for a key
func (hr *HashRing) GetPrimaryNode(key string) (common.NodeID, error) {
	nodes, err := hr.GetPreferenceList(key, 1)
	if err != nil {
		return "", err
	}
	return nodes[0], nil
}

// helpers
// hashVNode creates hash for virtual node i of physical node
func (hr *HashRing) hashVNode(nodeID common.NodeID, vnodeIdx int) uint64 {
	key := fmt.Sprintf("%s:%d", nodeID, vnodeIdx)
	return xxhash.Sum64String(key)
}

// GetNode retrieves physical node by ID
func (hr *HashRing) GetNode(nodeID common.NodeID) (*common.Node, bool) {
	hr.mu.RLock()
	defer hr.mu.RUnlock()
	node, exists := hr.nodes[nodeID]
	return node, exists
}

// Nodes returns all physical nodes (copy to prevent mutation)
func (hr *HashRing) Nodes() []*common.Node {
	hr.mu.RLock()
	defer hr.mu.RUnlock()

	nodes := make([]*common.Node, 0, len(hr.nodes))
	for _, node := range hr.nodes {
		nodes = append(nodes, node)
	}
	return nodes
}

// Size returns number of physical nodes
func (hr *HashRing) Size() int {
	hr.mu.RLock()
	defer hr.mu.RUnlock()
	return len(hr.nodes)
}

// VNodeCount returns the number of virtual nodes per physical node
func (hr *HashRing) VNodeCount() int {
	return hr.vnodeCount
}

// GetAllNodes returns all physical node IDs
func (hr *HashRing) GetAllNodes() []common.NodeID {
	hr.mu.RLock()
	defer hr.mu.RUnlock()

	nodeIDs := make([]common.NodeID, 0, len(hr.nodes))
	for nodeID := range hr.nodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	return nodeIDs
}
