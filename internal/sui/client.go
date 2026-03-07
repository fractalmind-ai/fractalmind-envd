package sui

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"strings"

	"github.com/block-vision/sui-go-sdk/models"
	suisdk "github.com/block-vision/sui-go-sdk/sui"
	"github.com/fractalmind-ai/fractalmind-envd/internal/config"
)

// RPCClient abstracts the SUI RPC methods we use, for testability.
type RPCClient interface {
	MoveCall(ctx context.Context, req models.MoveCallRequest) (models.TxnMetaData, error)
	SignAndExecuteTransactionBlock(ctx context.Context, req models.SignAndExecuteTransactionBlockRequest) (models.SuiTransactionBlockResponse, error)
	SuiXQueryEvents(ctx context.Context, req models.SuiXQueryEventsRequest) (models.PaginatedEventsResponse, error)
}

// Client is the SUI blockchain client for peer registry operations.
type Client struct {
	rpc        RPCClient
	keypair    *Keypair
	packageID  string
	registryID string
	orgID      string
	certID     string
}

// NewClient creates a SUI client from config.
func NewClient(cfg config.SUIConfig) (*Client, error) {
	kp, err := LoadOrGenerateKeypair(cfg.KeypairPath)
	if err != nil {
		return nil, fmt.Errorf("load sui keypair: %w", err)
	}

	rpc := suisdk.NewSuiClient(cfg.RPC)

	log.Printf("[sui] address=%s", kp.Address())

	return &Client{
		rpc:        rpc,
		keypair:    kp,
		packageID:  cfg.PackageID,
		registryID: cfg.RegistryID,
		orgID:      cfg.OrgID,
		certID:     cfg.CertID,
	}, nil
}

// newClientWithRPC creates a client with a custom RPC (for testing).
func newClientWithRPC(rpc RPCClient, kp *Keypair, packageID, registryID, orgID, certID string) *Client {
	return &Client{
		rpc:        rpc,
		keypair:    kp,
		packageID:  packageID,
		registryID: registryID,
		orgID:      orgID,
		certID:     certID,
	}
}

// Address returns the SUI address of this client.
func (c *Client) Address() string {
	return c.keypair.Address()
}

// RegisterPeer registers this node on-chain with its WireGuard public key and endpoints.
// If the peer is already registered (abort code 8001), it calls GoOnline instead.
func (c *Client) RegisterPeer(ctx context.Context, wgPubKey []byte, endpoints []string, hostname string) error {
	pubKeyHex := hex.EncodeToString(wgPubKey)

	err := c.executeMoveCall(ctx, "peer", "register_peer", []interface{}{
		c.registryID,
		c.orgID,
		c.certID,
		pubKeyHex,
		endpoints,
		hostname,
	})
	if err != nil {
		// If already registered (E_PEER_ALREADY_REGISTERED = 8001), go online instead
		if strings.Contains(err.Error(), "8001") {
			log.Printf("[sui] peer already registered, calling go_online instead")
			return c.GoOnline(ctx, endpoints)
		}
		return fmt.Errorf("register peer: %w", err)
	}

	log.Printf("[sui] peer registered on-chain")
	return nil
}

// UpdateEndpoints updates this node's endpoints on-chain.
func (c *Client) UpdateEndpoints(ctx context.Context, endpoints []string) error {
	err := c.executeMoveCall(ctx, "peer", "update_endpoints", []interface{}{
		c.registryID,
		endpoints,
	})
	if err != nil {
		return fmt.Errorf("update endpoints: %w", err)
	}
	log.Printf("[sui] endpoints updated on-chain")
	return nil
}

// GoOffline marks this node as offline on-chain.
func (c *Client) GoOffline(ctx context.Context) error {
	err := c.executeMoveCall(ctx, "peer", "go_offline", []interface{}{
		c.registryID,
	})
	if err != nil {
		return fmt.Errorf("go offline: %w", err)
	}
	log.Printf("[sui] marked offline on-chain")
	return nil
}

// GoOnline marks this node as online on-chain with updated endpoints.
func (c *Client) GoOnline(ctx context.Context, endpoints []string) error {
	err := c.executeMoveCall(ctx, "peer", "go_online", []interface{}{
		c.registryID,
		endpoints,
	})
	if err != nil {
		return fmt.Errorf("go online: %w", err)
	}
	log.Printf("[sui] marked online on-chain")
	return nil
}

// QueryPeers fetches all PeerRegistered events and overlays status/endpoint
// updates to build the current peer state.
func (c *Client) QueryPeers(ctx context.Context) ([]PeerInfo, error) {
	peers := make(map[string]*PeerInfo)

	// Fetch PeerRegistered events
	eventType := fmt.Sprintf("%s::peer::PeerRegistered", c.packageID)
	if err := c.fetchAllEvents(ctx, eventType, peers, applyPeerRegistered); err != nil {
		return nil, fmt.Errorf("query PeerRegistered events: %w", err)
	}

	// Overlay PeerEndpointUpdated
	endpointType := fmt.Sprintf("%s::peer::PeerEndpointUpdated", c.packageID)
	if err := c.fetchAllEvents(ctx, endpointType, peers, applyPeerEndpointUpdated); err != nil {
		return nil, fmt.Errorf("query PeerEndpointUpdated events: %w", err)
	}

	// Overlay PeerStatusChanged
	statusType := fmt.Sprintf("%s::peer::PeerStatusChanged", c.packageID)
	if err := c.fetchAllEvents(ctx, statusType, peers, applyPeerStatusChanged); err != nil {
		return nil, fmt.Errorf("query PeerStatusChanged events: %w", err)
	}

	// Remove deregistered peers
	deregType := fmt.Sprintf("%s::peer::PeerDeregistered", c.packageID)
	if err := c.fetchAllEvents(ctx, deregType, peers, applyPeerDeregistered); err != nil {
		return nil, fmt.Errorf("query PeerDeregistered events: %w", err)
	}

	// Convert map to slice, filtering for our org and online peers
	var result []PeerInfo
	for _, p := range peers {
		if p.OrgID == c.orgID && p.Status == PeerStatusOnline && p.Address != c.keypair.Address() {
			result = append(result, *p)
		}
	}

	log.Printf("[sui] discovered %d online peers in org", len(result))
	return result, nil
}

// PollNewEvents fetches events since the given cursor and returns updated peers.
func (c *Client) PollNewEvents(ctx context.Context, cursor string) ([]PeerInfo, string, error) {
	peers := make(map[string]*PeerInfo)
	newCursor := cursor

	// Poll all event types with the cursor
	eventTypes := []string{
		fmt.Sprintf("%s::peer::PeerRegistered", c.packageID),
		fmt.Sprintf("%s::peer::PeerEndpointUpdated", c.packageID),
		fmt.Sprintf("%s::peer::PeerStatusChanged", c.packageID),
		fmt.Sprintf("%s::peer::PeerDeregistered", c.packageID),
	}

	appliers := []func(map[string]interface{}, map[string]*PeerInfo){
		applyPeerRegistered,
		applyPeerEndpointUpdated,
		applyPeerStatusChanged,
		applyPeerDeregistered,
	}

	for i, eventType := range eventTypes {
		resp, err := c.rpc.SuiXQueryEvents(ctx, models.SuiXQueryEventsRequest{
			SuiEventFilter: models.EventFilterByMoveEventType{
				MoveEventType: eventType,
			},
			Cursor: cursor,
			Limit:  50,
		})
		if err != nil {
			return nil, cursor, fmt.Errorf("poll events %s: %w", eventType, err)
		}

		for _, evt := range resp.Data {
			appliers[i](evt.ParsedJson, peers)
		}

		if resp.NextCursor.TxDigest != "" {
			newCursor = resp.NextCursor.TxDigest
		}
	}

	var result []PeerInfo
	for _, p := range peers {
		if p.OrgID == c.orgID && p.Address != c.keypair.Address() {
			result = append(result, *p)
		}
	}

	return result, newCursor, nil
}

// executeMoveCall builds, signs, and executes a Move function call.
func (c *Client) executeMoveCall(ctx context.Context, module, function string, args []interface{}) error {
	txn, err := c.rpc.MoveCall(ctx, models.MoveCallRequest{
		Signer:          c.keypair.Address(),
		PackageObjectId: c.packageID,
		Module:          module,
		Function:        function,
		TypeArguments:   []interface{}{},
		Arguments:       args,
		GasBudget:       "10000000",
	})
	if err != nil {
		return fmt.Errorf("build tx: %w", err)
	}

	_, err = c.rpc.SignAndExecuteTransactionBlock(ctx, models.SignAndExecuteTransactionBlockRequest{
		TxnMetaData: txn,
		PriKey:      c.keypair.Private,
		Options: models.SuiTransactionBlockOptions{
			ShowEffects: true,
			ShowEvents:  true,
		},
		RequestType: "WaitForLocalExecution",
	})
	if err != nil {
		return fmt.Errorf("execute tx: %w", err)
	}

	return nil
}

// fetchAllEvents paginates through all events of the given type and applies them.
func (c *Client) fetchAllEvents(
	ctx context.Context,
	eventType string,
	peers map[string]*PeerInfo,
	apply func(map[string]interface{}, map[string]*PeerInfo),
) error {
	cursor := ""
	for {
		resp, err := c.rpc.SuiXQueryEvents(ctx, models.SuiXQueryEventsRequest{
			SuiEventFilter: models.EventFilterByMoveEventType{
				MoveEventType: eventType,
			},
			Cursor: cursor,
			Limit:  50,
		})
		if err != nil {
			return err
		}

		for _, evt := range resp.Data {
			apply(evt.ParsedJson, peers)
		}

		if !resp.HasNextPage {
			break
		}
		cursor = resp.NextCursor.TxDigest
	}
	return nil
}

// Event applier functions — parse event JSON and update peer map.

func applyPeerRegistered(data map[string]interface{}, peers map[string]*PeerInfo) {
	addr, _ := data["peer"].(string)
	if addr == "" {
		return
	}

	orgID, _ := data["org_id"].(string)
	hostname, _ := data["hostname"].(string)

	var wgPubKey []byte
	if keyHex, ok := data["wireguard_pubkey"].(string); ok {
		wgPubKey, _ = hex.DecodeString(strings.TrimPrefix(keyHex, "0x"))
	}

	var endpoints []string
	if eps, ok := data["endpoints"].([]interface{}); ok {
		for _, ep := range eps {
			if s, ok := ep.(string); ok {
				endpoints = append(endpoints, s)
			}
		}
	}

	peers[addr] = &PeerInfo{
		Address:         addr,
		OrgID:           orgID,
		WireGuardPubKey: wgPubKey,
		Endpoints:       endpoints,
		Hostname:        hostname,
		Status:          PeerStatusOnline,
	}
}

func applyPeerEndpointUpdated(data map[string]interface{}, peers map[string]*PeerInfo) {
	addr, _ := data["peer"].(string)
	if addr == "" {
		return
	}

	p, ok := peers[addr]
	if !ok {
		return
	}

	if eps, ok := data["new_endpoints"].([]interface{}); ok {
		p.Endpoints = nil
		for _, ep := range eps {
			if s, ok := ep.(string); ok {
				p.Endpoints = append(p.Endpoints, s)
			}
		}
	}
}

func applyPeerStatusChanged(data map[string]interface{}, peers map[string]*PeerInfo) {
	addr, _ := data["peer"].(string)
	if addr == "" {
		return
	}

	p, ok := peers[addr]
	if !ok {
		return
	}

	if status, ok := data["new_status"].(float64); ok {
		p.Status = uint8(status)
	}
}

func applyPeerDeregistered(data map[string]interface{}, peers map[string]*PeerInfo) {
	addr, _ := data["peer"].(string)
	if addr == "" {
		return
	}
	delete(peers, addr)
}
