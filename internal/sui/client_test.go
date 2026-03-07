package sui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/block-vision/sui-go-sdk/models"
)

// mockRPC implements RPCClient for testing.
type mockRPC struct {
	moveCallFn    func(ctx context.Context, req models.MoveCallRequest) (models.TxnMetaData, error)
	signExecFn    func(ctx context.Context, req models.SignAndExecuteTransactionBlockRequest) (models.SuiTransactionBlockResponse, error)
	queryEventsFn func(ctx context.Context, req models.SuiXQueryEventsRequest) (models.PaginatedEventsResponse, error)
}

func (m *mockRPC) MoveCall(ctx context.Context, req models.MoveCallRequest) (models.TxnMetaData, error) {
	if m.moveCallFn != nil {
		return m.moveCallFn(ctx, req)
	}
	return models.TxnMetaData{}, nil
}

func (m *mockRPC) SignAndExecuteTransactionBlock(ctx context.Context, req models.SignAndExecuteTransactionBlockRequest) (models.SuiTransactionBlockResponse, error) {
	if m.signExecFn != nil {
		return m.signExecFn(ctx, req)
	}
	return models.SuiTransactionBlockResponse{}, nil
}

func (m *mockRPC) SuiXQueryEvents(ctx context.Context, req models.SuiXQueryEventsRequest) (models.PaginatedEventsResponse, error) {
	if m.queryEventsFn != nil {
		return m.queryEventsFn(ctx, req)
	}
	return models.PaginatedEventsResponse{}, nil
}

func testKeypair(t *testing.T) *Keypair {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Keypair{Private: priv, Public: pub}
}

func TestKeypairAddress(t *testing.T) {
	kp := testKeypair(t)
	addr := kp.Address()
	if !hasPrefix(addr, "0x") {
		t.Errorf("address should start with 0x, got %s", addr)
	}
	// BLAKE2b-256 produces 32 bytes = 64 hex chars + 0x prefix
	if len(addr) != 66 {
		t.Errorf("address should be 66 chars (0x + 64 hex), got %d", len(addr))
	}
}

func TestLoadOrGenerateKeypair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.key")

	// Generate new keypair
	kp1, err := LoadOrGenerateKeypair(path)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	// File should exist with 0600 perms
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat keypair file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("keypair file perms: got %o, want 0600", info.Mode().Perm())
	}

	// Load existing keypair
	kp2, err := LoadOrGenerateKeypair(path)
	if err != nil {
		t.Fatalf("load keypair: %v", err)
	}

	if kp1.Address() != kp2.Address() {
		t.Errorf("loaded keypair address mismatch: %s != %s", kp1.Address(), kp2.Address())
	}
}

func TestRegisterPeer(t *testing.T) {
	kp := testKeypair(t)
	var calledModule, calledFunction string

	mock := &mockRPC{
		moveCallFn: func(_ context.Context, req models.MoveCallRequest) (models.TxnMetaData, error) {
			calledModule = req.Module
			calledFunction = req.Function
			return models.TxnMetaData{}, nil
		},
	}

	client := newClientWithRPC(mock, kp, "0xpkg", "0xreg", "0xorg", "0xcert")

	wgKey := make([]byte, 32)
	err := client.RegisterPeer(context.Background(), wgKey, []string{"1.2.3.4:51820"}, "test-host")
	if err != nil {
		t.Fatalf("RegisterPeer: %v", err)
	}

	if calledModule != "peer" || calledFunction != "register_peer" {
		t.Errorf("expected peer.register_peer, got %s.%s", calledModule, calledFunction)
	}
}

func TestGoOffline(t *testing.T) {
	kp := testKeypair(t)
	var calledFunction string

	mock := &mockRPC{
		moveCallFn: func(_ context.Context, req models.MoveCallRequest) (models.TxnMetaData, error) {
			calledFunction = req.Function
			return models.TxnMetaData{}, nil
		},
	}

	client := newClientWithRPC(mock, kp, "0xpkg", "0xreg", "0xorg", "0xcert")

	if err := client.GoOffline(context.Background()); err != nil {
		t.Fatalf("GoOffline: %v", err)
	}

	if calledFunction != "go_offline" {
		t.Errorf("expected go_offline, got %s", calledFunction)
	}
}

func TestQueryPeers(t *testing.T) {
	kp := testKeypair(t)
	peerAddr := "0xdeadbeef1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	wgKey := make([]byte, 32)
	wgKey[0] = 0x42

	mock := &mockRPC{
		queryEventsFn: func(_ context.Context, req models.SuiXQueryEventsRequest) (models.PaginatedEventsResponse, error) {
			filter, ok := req.SuiEventFilter.(models.EventFilterByMoveEventType)
			if !ok {
				return models.PaginatedEventsResponse{}, nil
			}

			// Only return data for PeerRegistered
			if filter.MoveEventType == "0xpkg::peer::PeerRegistered" {
				return models.PaginatedEventsResponse{
					Data: []models.SuiEventResponse{
						{
							ParsedJson: map[string]interface{}{
								"peer":              peerAddr,
								"org_id":            "0xorg",
								"wireguard_pubkey":  hex.EncodeToString(wgKey),
								"endpoints":         []interface{}{"1.2.3.4:51820"},
								"hostname":          "peer-host",
							},
						},
					},
					HasNextPage: false,
				}, nil
			}

			return models.PaginatedEventsResponse{}, nil
		},
	}

	client := newClientWithRPC(mock, kp, "0xpkg", "0xreg", "0xorg", "0xcert")

	peers, err := client.QueryPeers(context.Background())
	if err != nil {
		t.Fatalf("QueryPeers: %v", err)
	}

	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}

	p := peers[0]
	if p.Address != peerAddr {
		t.Errorf("peer address: got %s, want %s", p.Address, peerAddr)
	}
	if p.Hostname != "peer-host" {
		t.Errorf("peer hostname: got %s, want peer-host", p.Hostname)
	}
	if len(p.Endpoints) != 1 || p.Endpoints[0] != "1.2.3.4:51820" {
		t.Errorf("peer endpoints: got %v", p.Endpoints)
	}
	if p.Status != PeerStatusOnline {
		t.Errorf("peer status: got %d, want %d", p.Status, PeerStatusOnline)
	}
}

func TestApplyPeerStatusChanged(t *testing.T) {
	peers := map[string]*PeerInfo{
		"0xpeer1": {Address: "0xpeer1", Status: PeerStatusOnline},
	}

	applyPeerStatusChanged(map[string]interface{}{
		"peer":       "0xpeer1",
		"new_status": float64(PeerStatusOffline),
	}, peers)

	if peers["0xpeer1"].Status != PeerStatusOffline {
		t.Errorf("peer status should be offline, got %d", peers["0xpeer1"].Status)
	}
}

func TestApplyPeerDeregistered(t *testing.T) {
	peers := map[string]*PeerInfo{
		"0xpeer1": {Address: "0xpeer1"},
		"0xpeer2": {Address: "0xpeer2"},
	}

	applyPeerDeregistered(map[string]interface{}{
		"peer": "0xpeer1",
	}, peers)

	if _, ok := peers["0xpeer1"]; ok {
		t.Error("peer1 should be removed after deregister")
	}
	if _, ok := peers["0xpeer2"]; !ok {
		t.Error("peer2 should still exist")
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
