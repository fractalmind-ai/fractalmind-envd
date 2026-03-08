// Package relaypicker implements client-side relay selection with weight scoring.
// Connection strategy: WireGuard P2P → Org Relay → Shared Relay.
// When P2P fails (double symmetric NAT), this package selects the best relay
// based on: org match, geographic proximity, ISP match, latency, and load.
package relaypicker

import (
	"log"
	"sort"
	"sync"

	"github.com/fractalmind-ai/fractalmind-envd/internal/sui"
)

// Weights for relay scoring
const (
	WeightOrgMatch = 100 // Same organization
	WeightRegion   = 50  // Same geographic region
	WeightISP      = 30  // Same ISP/network provider
	WeightLatency  = 20  // Low latency bonus (inverse)
	WeightLoad     = 10  // Low load bonus (inverse)
)

// RelayCandidate is a scored relay node.
type RelayCandidate struct {
	Peer  sui.PeerInfo
	Score int
}

// RelayLoadCache caches relay_load metrics received from P2P heartbeats.
type RelayLoadCache struct {
	mu    sync.RWMutex
	loads map[string]*LoadMetrics // key: peer address
}

// LoadMetrics holds cached relay load from P2P heartbeat.
type LoadMetrics struct {
	CurrentLoad  uint64
	Capacity     uint64
	AvgLatencyMs uint64
}

// NewRelayLoadCache creates a new relay load cache.
func NewRelayLoadCache() *RelayLoadCache {
	return &RelayLoadCache{
		loads: make(map[string]*LoadMetrics),
	}
}

// Update stores relay load from a P2P heartbeat.
func (c *RelayLoadCache) Update(peerAddr string, load, capacity, latencyMs uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads[peerAddr] = &LoadMetrics{
		CurrentLoad:  load,
		Capacity:     capacity,
		AvgLatencyMs: latencyMs,
	}
}

// Get returns cached load metrics for a peer.
func (c *RelayLoadCache) Get(peerAddr string) *LoadMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loads[peerAddr]
}

// Picker selects the best relay from available candidates.
type Picker struct {
	myOrgID string
	myRegion string
	myISP   string
	cache   *RelayLoadCache
}

// NewPicker creates a relay picker with local node context.
func NewPicker(orgID, region, isp string, cache *RelayLoadCache) *Picker {
	return &Picker{
		myOrgID:  orgID,
		myRegion: region,
		myISP:    isp,
		cache:    cache,
	}
}

// SelectBest picks the top N relays from the peer list, scored by weight.
// Returns up to topN candidates, sorted by score (highest first).
func (p *Picker) SelectBest(peers []sui.PeerInfo, topN int) []RelayCandidate {
	var candidates []RelayCandidate

	for _, peer := range peers {
		if !peer.IsRelay {
			continue
		}

		score := p.score(peer)
		candidates = append(candidates, RelayCandidate{
			Peer:  peer,
			Score: score,
		})
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	if len(candidates) > topN {
		candidates = candidates[:topN]
	}

	if len(candidates) > 0 {
		log.Printf("[relay-picker] selected %d relays (top: %s score=%d)",
			len(candidates), candidates[0].Peer.Address, candidates[0].Score)
	}

	return candidates
}

// score computes the weighted score for a relay candidate.
func (p *Picker) score(peer sui.PeerInfo) int {
	score := 0

	// Organization match (+100)
	if peer.OrgID == p.myOrgID {
		score += WeightOrgMatch
	}

	// Geographic region match (+50)
	if peer.Region != "" && peer.Region == p.myRegion {
		score += WeightRegion
	}

	// ISP match (+30)
	if peer.ISP != "" && peer.ISP == p.myISP {
		score += WeightISP
	}

	// Latency bonus (+20 max, inversely proportional)
	if metrics := p.cache.Get(peer.Address); metrics != nil {
		// Lower latency = higher score
		if metrics.AvgLatencyMs < 50 {
			score += WeightLatency
		} else if metrics.AvgLatencyMs < 100 {
			score += WeightLatency / 2
		}

		// Load bonus (+10 max, inversely proportional)
		if metrics.Capacity > 0 {
			utilization := float64(metrics.CurrentLoad) / float64(metrics.Capacity)
			if utilization < 0.3 {
				score += WeightLoad
			} else if utilization < 0.7 {
				score += WeightLoad / 2
			}
		}
	} else {
		// No metrics available — give default bonus for uptime score
		if peer.UptimeScore >= 90 {
			score += WeightLatency / 2
		}
	}

	return score
}
