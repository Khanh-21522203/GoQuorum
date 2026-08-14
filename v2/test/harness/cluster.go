package harness

import (
	"fmt"

	"goquorum.io/v2/infra/config"
)

// Cluster is a set of in-process Nodes started together for a test, e.g.
// to exercise quorum reads/writes, sibling conflicts, or anti-entropy
// convergence across replicas.
type Cluster struct {
	Nodes []*Node
}

// StartCluster starts one Node per entry in cfgs, in order. If any node
// fails to start, every already-started node is stopped before the error
// is returned, so callers do not need to clean up a partially-started
// cluster themselves.
func StartCluster(cfgs []*config.Config) (*Cluster, error) {
	c := &Cluster{Nodes: make([]*Node, 0, len(cfgs))}

	for _, cfg := range cfgs {
		n, err := StartNode(cfg)
		if err != nil {
			c.Stop()
			return nil, fmt.Errorf("harness: start cluster: %w", err)
		}
		c.Nodes = append(c.Nodes, n)
	}

	return c, nil
}

// Stop stops every node in the cluster, in order.
func (c *Cluster) Stop() {
	if c == nil {
		return
	}
	for _, n := range c.Nodes {
		n.Stop()
	}
}
