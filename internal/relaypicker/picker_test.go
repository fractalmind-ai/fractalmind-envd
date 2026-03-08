package relaypicker

import (
	"testing"

	"github.com/fractalmind-ai/fractalmind-envd/internal/sui"
)

func TestSelectBest_OrgMatchPrioritized(t *testing.T) {
	cache := NewRelayLoadCache()
	picker := NewPicker("org-1", "cn-east", "aliyun", cache)

	peers := []sui.PeerInfo{
		{Address: "0xA", OrgID: "org-2", IsRelay: true, Region: "cn-east", ISP: "aliyun"},
		{Address: "0xB", OrgID: "org-1", IsRelay: true, Region: "us-west", ISP: "aws"},
		{Address: "0xC", OrgID: "org-1", IsRelay: true, Region: "cn-east", ISP: "aliyun"},
	}

	result := picker.SelectBest(peers, 5)
	if len(result) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(result))
	}

	// 0xC should be first: org(100) + region(50) + isp(30) = 180
	if result[0].Peer.Address != "0xC" {
		t.Errorf("expected 0xC first (org+region+isp), got %s", result[0].Peer.Address)
	}
	if result[0].Score != 180 {
		t.Errorf("expected score 180, got %d", result[0].Score)
	}

	// 0xB should be second: org(100) = 100
	if result[1].Peer.Address != "0xB" {
		t.Errorf("expected 0xB second (org only), got %s", result[1].Peer.Address)
	}
	if result[1].Score != 100 {
		t.Errorf("expected score 100, got %d", result[1].Score)
	}

	// 0xA should be last: region(50) + isp(30) = 80
	if result[2].Peer.Address != "0xA" {
		t.Errorf("expected 0xA last (region+isp only), got %s", result[2].Peer.Address)
	}
	if result[2].Score != 80 {
		t.Errorf("expected score 80, got %d", result[2].Score)
	}
}

func TestSelectBest_TopN(t *testing.T) {
	cache := NewRelayLoadCache()
	picker := NewPicker("org-1", "", "", cache)

	peers := []sui.PeerInfo{
		{Address: "0xA", OrgID: "org-1", IsRelay: true},
		{Address: "0xB", OrgID: "org-1", IsRelay: true},
		{Address: "0xC", OrgID: "org-1", IsRelay: true},
		{Address: "0xD", OrgID: "org-1", IsRelay: true},
		{Address: "0xE", OrgID: "org-1", IsRelay: true},
		{Address: "0xF", OrgID: "org-1", IsRelay: true},
	}

	result := picker.SelectBest(peers, 3)
	if len(result) != 3 {
		t.Fatalf("expected 3 candidates (top 3), got %d", len(result))
	}
}

func TestSelectBest_FilterNonRelay(t *testing.T) {
	cache := NewRelayLoadCache()
	picker := NewPicker("org-1", "", "", cache)

	peers := []sui.PeerInfo{
		{Address: "0xA", OrgID: "org-1", IsRelay: false}, // not a relay
		{Address: "0xB", OrgID: "org-1", IsRelay: true},
		{Address: "0xC", OrgID: "org-1", IsRelay: false}, // not a relay
	}

	result := picker.SelectBest(peers, 5)
	if len(result) != 1 {
		t.Fatalf("expected 1 relay candidate, got %d", len(result))
	}
	if result[0].Peer.Address != "0xB" {
		t.Errorf("expected 0xB, got %s", result[0].Peer.Address)
	}
}

func TestSelectBest_LoadMetricsBonus(t *testing.T) {
	cache := NewRelayLoadCache()
	// Low load, low latency
	cache.Update("0xA", 10, 100, 20)
	// High load, high latency
	cache.Update("0xB", 90, 100, 200)

	picker := NewPicker("org-1", "", "", cache)

	peers := []sui.PeerInfo{
		{Address: "0xA", OrgID: "org-1", IsRelay: true},
		{Address: "0xB", OrgID: "org-1", IsRelay: true},
	}

	result := picker.SelectBest(peers, 5)
	if len(result) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(result))
	}

	// 0xA: org(100) + latency(20, <50ms→full bonus) + load(10, 10%→full bonus) = 130
	if result[0].Peer.Address != "0xA" {
		t.Errorf("expected 0xA first (low load+latency), got %s", result[0].Peer.Address)
	}
	if result[0].Score != 130 {
		t.Errorf("expected score 130, got %d", result[0].Score)
	}

	// 0xB: org(100) + latency(200ms, no bonus) + load(90%, no bonus) = 100
	if result[1].Score != 100 {
		t.Errorf("expected score 100, got %d", result[1].Score)
	}
}

func TestSelectBest_EmptyPeers(t *testing.T) {
	cache := NewRelayLoadCache()
	picker := NewPicker("org-1", "", "", cache)

	result := picker.SelectBest(nil, 5)
	if len(result) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(result))
	}
}

func TestRelayLoadCache_UpdateAndGet(t *testing.T) {
	cache := NewRelayLoadCache()

	if got := cache.Get("0xA"); got != nil {
		t.Error("expected nil for unknown peer")
	}

	cache.Update("0xA", 42, 100, 15)
	got := cache.Get("0xA")
	if got == nil {
		t.Fatal("expected non-nil after update")
	}
	if got.CurrentLoad != 42 {
		t.Errorf("CurrentLoad = %d, want 42", got.CurrentLoad)
	}
	if got.Capacity != 100 {
		t.Errorf("Capacity = %d, want 100", got.Capacity)
	}
	if got.AvgLatencyMs != 15 {
		t.Errorf("AvgLatencyMs = %d, want 15", got.AvgLatencyMs)
	}
}

func TestScore_UptimeScoreDefault(t *testing.T) {
	cache := NewRelayLoadCache()
	picker := NewPicker("org-1", "cn-east", "aliyun", cache)

	// Peer with high uptime score but no load metrics
	peer := sui.PeerInfo{
		Address:     "0xA",
		OrgID:       "org-1",
		IsRelay:     true,
		UptimeScore: 95,
	}

	score := picker.score(peer)
	// org(100) + uptime≥90 bonus(10) = 110
	if score != 110 {
		t.Errorf("score = %d, want 110 (org + uptime bonus)", score)
	}
}
