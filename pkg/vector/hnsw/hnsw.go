package hnsw

import (
	"cmp"
	"container/heap"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sync"

	"github.com/nobelk/reverb/pkg/vector"
)

// Config holds HNSW algorithm parameters.
type Config struct {
	M              int // max connections per node per layer, default 16
	EfConstruction int // size of dynamic candidate list during construction, default 200
	EfSearch       int // size of dynamic candidate list during search, default 100
}

func (c Config) withDefaults() Config {
	if c.M <= 0 {
		c.M = 16
	}
	if c.EfConstruction <= 0 {
		c.EfConstruction = 200
	}
	if c.EfSearch <= 0 {
		c.EfSearch = 100
	}
	return c
}

// node represents a single vector in the HNSW graph.
type node struct {
	id      string
	vector  []float32
	layer   int                   // max layer this node exists in
	friends []map[string]struct{} // friends[level] = set of neighbor IDs at that level
}

// scoredNode pairs a node with its similarity score for heap operations.
type scoredNode struct {
	n     *node
	score float32
}

// Index is an HNSW approximate nearest neighbor index. Thread-safe.
type Index struct {
	mu         sync.RWMutex
	cfg        Config
	dims       int
	nodes      map[string]*node
	entryPoint *node
	maxLayer   int
	mMax0      int // max connections for layer 0 (2*M)
	ml         float64
	rng        *rand.Rand
}

// New creates a new HNSW index with the given config and vector dimensionality.
func New(cfg Config, dims int) *Index {
	cfg = cfg.withDefaults()
	return &Index{
		cfg:   cfg,
		dims:  dims,
		nodes: make(map[string]*node),
		mMax0: cfg.M * 2,
		ml:    1.0 / math.Log(float64(cfg.M)),
		rng:   rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())),
	}
}

func (idx *Index) Add(_ context.Context, id string, vec []float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Validate vector dimensionality.
	if idx.dims > 0 && len(vec) != idx.dims {
		return fmt.Errorf("vector dimension mismatch: index configured for %d dimensions, got %d", idx.dims, len(vec))
	}
	if idx.dims == 0 {
		idx.dims = len(vec)
	}

	// If the ID already exists, delete it first (overwrite semantics).
	if _, exists := idx.nodes[id]; exists {
		idx.deleteNode(id)
	}

	v := make([]float32, len(vec))
	copy(v, vec)

	level := idx.randomLevel()

	nd := &node{
		id:      id,
		vector:  v,
		layer:   level,
		friends: make([]map[string]struct{}, level+1),
	}
	for i := 0; i <= level; i++ {
		nd.friends[i] = make(map[string]struct{})
	}

	idx.nodes[id] = nd

	// First node becomes entry point.
	if idx.entryPoint == nil {
		idx.entryPoint = nd
		idx.maxLayer = level
		return nil
	}

	ep := idx.entryPoint

	// Phase 1: greedily traverse layers above the node's max layer.
	for lc := idx.maxLayer; lc > level; lc-- {
		ep = idx.greedyClosest(vec, ep, lc)
	}

	// Phase 2: for each layer from min(level, maxLayer) down to 0,
	// search for neighbors and connect them.
	topLayer := min(level, idx.maxLayer)

	for lc := topLayer; lc >= 0; lc-- {
		candidates := idx.searchLayer(vec, ep, idx.cfg.EfConstruction, lc)

		// Select M best neighbors.
		maxConn := idx.cfg.M
		if lc == 0 {
			maxConn = idx.mMax0
		}
		neighbors := idx.selectNeighbors(candidates, maxConn)

		// Bidirectional connections.
		for _, nb := range neighbors {
			nd.friends[lc][nb.n.id] = struct{}{}
			nb.n.friends[lc][nd.id] = struct{}{}

			// Prune neighbor's connections if over limit.
			if len(nb.n.friends[lc]) > maxConn {
				idx.pruneConnections(nb.n, lc, maxConn)
			}
		}

		if len(candidates) > 0 {
			ep = candidates[0].n
		}
	}

	// Update entry point if new node is at a higher layer.
	if level > idx.maxLayer {
		idx.entryPoint = nd
		idx.maxLayer = level
	}

	return nil
}

func (idx *Index) Search(_ context.Context, query []float32, k int, minScore float32) ([]vector.SearchResult, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.nodes) == 0 || idx.entryPoint == nil {
		return nil, nil
	}

	ep := idx.entryPoint

	// Greedy descent from top layer to layer 1.
	for lc := idx.maxLayer; lc > 0; lc-- {
		ep = idx.greedyClosest(query, ep, lc)
	}

	// Search layer 0 with efSearch candidates.
	ef := max(idx.cfg.EfSearch, k)
	candidates := idx.searchLayer(query, ep, ef, 0)

	// Collect results, filter by minScore, limit to k. Reuse the scores already
	// computed during search — no recomputation needed.
	var results []vector.SearchResult
	for _, c := range candidates {
		if c.score >= minScore {
			results = append(results, vector.SearchResult{ID: c.n.id, Score: c.score})
		}
	}

	slices.SortFunc(results, func(a, b vector.SearchResult) int {
		return cmp.Compare(b.Score, a.Score) // descending
	})

	return results[:min(len(results), k)], nil
}

func (idx *Index) Delete(_ context.Context, id string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.deleteNode(id)
	return nil
}

func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.nodes)
}

// deleteNode removes a node and patches connections. Caller must hold write lock.
func (idx *Index) deleteNode(id string) {
	nd, ok := idx.nodes[id]
	if !ok {
		return
	}

	// Remove the node from all neighbors' friend lists.
	for lc := 0; lc <= nd.layer; lc++ {
		for friendID := range nd.friends[lc] {
			if friend, exists := idx.nodes[friendID]; exists && lc < len(friend.friends) {
				delete(friend.friends[lc], id)
			}
		}
	}

	delete(idx.nodes, id)

	// If deleted node was the entry point, pick a new one.
	if idx.entryPoint != nil && idx.entryPoint.id == id {
		idx.entryPoint = nil
		idx.maxLayer = 0
		for _, n := range idx.nodes {
			if idx.entryPoint == nil || n.layer > idx.maxLayer {
				idx.entryPoint = n
				idx.maxLayer = n.layer
			}
		}
	}
}

// randomLevel returns a random layer for a new node using exponential distribution.
func (idx *Index) randomLevel() int {
	r := idx.rng.Float64()
	return int(math.Floor(-math.Log(r) * idx.ml))
}

// greedyClosest finds the closest node to the query starting from ep at a given layer,
// by greedily following neighbors.
func (idx *Index) greedyClosest(query []float32, ep *node, layer int) *node {
	current := ep
	currentSim := vector.CosineSimilarity(query, current.vector)
	for {
		improved := false
		if layer < len(current.friends) {
			for friendID := range current.friends[layer] {
				friend, ok := idx.nodes[friendID]
				if !ok {
					continue
				}
				sim := vector.CosineSimilarity(query, friend.vector)
				if sim > currentSim {
					currentSim = sim
					current = friend
					improved = true
				}
			}
		}
		if !improved {
			break
		}
	}
	return current
}

// searchLayer performs a beam search at a single layer, returning up to ef
// closest nodes paired with their similarity scores. Returning scoredNodes
// (rather than bare *node) lets callers reuse the scores instead of
// recomputing cosine similarity downstream.
func (idx *Index) searchLayer(query []float32, ep *node, ef int, layer int) []scoredNode {
	visited := make(map[string]struct{})
	visited[ep.id] = struct{}{}

	// candidateHeap: max-heap by score so we always expand the most similar candidate first.
	// resultHeap: min-heap by score so the worst result is at the top for easy trimming.
	candidates := &candidateHeap{}
	results := &resultHeap{}

	epScore := vector.CosineSimilarity(query, ep.vector)

	heap.Push(candidates, scoredNode{n: ep, score: epScore})
	heap.Push(results, scoredNode{n: ep, score: epScore})

	for candidates.Len() > 0 {
		// Get the most similar unprocessed candidate.
		c := heap.Pop(candidates).(scoredNode)

		// If the best candidate is worse than the worst result, stop.
		if results.Len() >= ef {
			worstResult := (*results)[0]
			if c.score < worstResult.score {
				break
			}
		}

		// Expand neighbors.
		if layer < len(c.n.friends) {
			for friendID := range c.n.friends[layer] {
				if _, seen := visited[friendID]; seen {
					continue
				}
				visited[friendID] = struct{}{}

				friend, ok := idx.nodes[friendID]
				if !ok {
					continue
				}

				fScore := vector.CosineSimilarity(query, friend.vector)

				// Add to results if there's room or if this is better than worst result.
				if results.Len() < ef {
					heap.Push(candidates, scoredNode{n: friend, score: fScore})
					heap.Push(results, scoredNode{n: friend, score: fScore})
				} else if fScore > (*results)[0].score {
					heap.Push(candidates, scoredNode{n: friend, score: fScore})
					heap.Pop(results)
					heap.Push(results, scoredNode{n: friend, score: fScore})
				}
			}
		}
	}

	// Extract results sorted by score descending.
	out := make([]scoredNode, results.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(results).(scoredNode)
	}
	return out
}

// selectNeighbors selects the best neighbors from candidates (simple heuristic: top M by similarity).
func (idx *Index) selectNeighbors(candidates []scoredNode, m int) []scoredNode {
	if len(candidates) <= m {
		return candidates
	}
	return candidates[:m]
}

// pruneConnections trims a node's connections at a given layer to maxConn.
func (idx *Index) pruneConnections(nd *node, layer int, maxConn int) {
	if layer >= len(nd.friends) {
		return
	}
	friends := nd.friends[layer]
	if len(friends) <= maxConn {
		return
	}

	type friendScore struct {
		id    string
		score float32
	}

	var scored []friendScore
	for fID := range friends {
		f, ok := idx.nodes[fID]
		if !ok {
			continue
		}
		scored = append(scored, friendScore{id: fID, score: vector.CosineSimilarity(nd.vector, f.vector)})
	}

	slices.SortFunc(scored, func(a, b friendScore) int {
		return cmp.Compare(b.score, a.score) // descending
	})

	keep := min(maxConn, len(scored))
	newFriends := make(map[string]struct{}, keep)
	for i := 0; i < keep; i++ {
		newFriends[scored[i].id] = struct{}{}
	}

	// Remove reverse edges from dropped neighbors to prevent dangling one-directional edges.
	for fID := range friends {
		if _, kept := newFriends[fID]; !kept {
			if friend, exists := idx.nodes[fID]; exists && layer < len(friend.friends) {
				delete(friend.friends[layer], nd.id)
			}
		}
	}

	nd.friends[layer] = newFriends
}

// CheckBidirectional verifies that all edges in the graph are bidirectional.
// Returns an error describing the first violation found, or nil if the graph is consistent.
// This is exported for use in tests.
func (idx *Index) CheckBidirectional() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	for id, nd := range idx.nodes {
		for lc, friends := range nd.friends {
			for fID := range friends {
				friend, exists := idx.nodes[fID]
				if !exists {
					return fmt.Errorf("node %q at layer %d has friend %q which does not exist in the index", id, lc, fID)
				}
				if lc >= len(friend.friends) {
					return fmt.Errorf("node %q at layer %d has friend %q whose friends slice has length %d", id, lc, fID, len(friend.friends))
				}
				if _, back := friend.friends[lc][id]; !back {
					return fmt.Errorf("edge %q→%q at layer %d exists but reverse edge %q→%q does not", id, fID, lc, fID, id)
				}
			}
		}
	}
	return nil
}

// --- Heap implementations for searchLayer ---

// candidateHeap is a max-heap by score (highest similarity at top).
// Used to always expand the most promising candidate first.
type candidateHeap []scoredNode

func (h candidateHeap) Len() int           { return len(h) }
func (h candidateHeap) Less(i, j int) bool { return h[i].score > h[j].score }
func (h candidateHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *candidateHeap) Push(x any)        { *h = append(*h, x.(scoredNode)) }
func (h *candidateHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// resultHeap is a min-heap by score (lowest similarity at top).
// Used to track the ef-best results and trim the worst.
type resultHeap []scoredNode

func (h resultHeap) Len() int           { return len(h) }
func (h resultHeap) Less(i, j int) bool { return h[i].score < h[j].score }
func (h resultHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *resultHeap) Push(x any)        { *h = append(*h, x.(scoredNode)) }
func (h *resultHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
