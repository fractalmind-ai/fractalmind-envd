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
	SuiExecuteTransactionBlock(ctx context.Context, req models.SuiExecuteTransactionBlockRequest) (models.SuiTransactionBlockResponse, error)
	SuiXQueryEvents(ctx context.Context, req models.SuiXQueryEventsRequest) (models.PaginatedEventsResponse, error)
	SuiXGetOwnedObjects(ctx context.Context, req models.SuiXGetOwnedObjectsRequest) (models.PaginatedObjectsResponse, error)
}

// Client is the SUI blockchain client for peer registry operations.
type Client struct {
	rpc               RPCClient
	keypair           *Keypair
	packageID         string
	protocolPackageID string
	registryID        string
	orgID             string
	certID            string
	sponsor           *SponsorClient
}

// NewClient creates a SUI client from config.
func NewClient(cfg config.SUIConfig) (*Client, error) {
	kp, err := LoadOrGenerateKeypair(cfg.KeypairPath)
	if err != nil {
		return nil, fmt.Errorf("load sui keypair: %w", err)
	}

	rpc := suisdk.NewSuiClient(cfg.RPC)

	log.Printf("[sui] address=%s", kp.Address())

	c := &Client{
		rpc:               rpc,
		keypair:           kp,
		packageID:         cfg.PackageID,
		protocolPackageID: cfg.ProtocolPackageID,
		registryID:        cfg.RegistryID,
		orgID:             cfg.OrgID,
		certID:            cfg.CertID,
	}

	return c, nil
}

// newClientWithRPC creates a client with a custom RPC (for testing).
func newClientWithRPC(rpc RPCClient, kp *Keypair, packageID, protocolPackageID, registryID, orgID, certID string) *Client {
	return &Client{
		rpc:               rpc,
		keypair:           kp,
		packageID:         packageID,
		protocolPackageID: protocolPackageID,
		registryID:        registryID,
		orgID:             orgID,
		certID:            certID,
	}
}

// Address returns the SUI address of this client.
func (c *Client) Address() string {
	return c.keypair.Address()
}

// SetSponsor attaches a sponsor client for gas sponsorship.
func (c *Client) SetSponsor(sc *SponsorClient) {
	c.sponsor = sc
	log.Printf("[sui] sponsor client attached")
}

// RegisterPeer registers this node on-chain with its WireGuard public key and endpoints.
// If the peer is already registered (abort code 8001), it calls GoOnline instead.
// If certID is not configured, auto-discovers or creates an AgentCertificate.
func (c *Client) RegisterPeer(ctx context.Context, wgPubKey []byte, endpoints []string, hostname string) error {
	// Auto-discover cert if not configured
	if c.certID == "" {
		certID, err := c.ensureAgentCert(ctx)
		if err != nil {
			return fmt.Errorf("ensure agent cert: %w", err)
		}
		c.certID = certID
	}

	pubKeyHex := "0x" + hex.EncodeToString(wgPubKey)

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

// RegisterRelay registers this node as a relay on-chain with relay metadata.
func (c *Client) RegisterRelay(ctx context.Context, relayAddr, region, isp string, capacity uint64) error {
	err := c.executeMoveCall(ctx, "relay_info", "register_relay", []interface{}{
		c.registryID,
		relayAddr,
		region,
		isp,
		fmt.Sprintf("%d", capacity),
	})
	if err != nil {
		return fmt.Errorf("register relay: %w", err)
	}
	log.Printf("[sui] registered as relay on-chain (addr=%s, region=%s)", relayAddr, region)
	return nil
}

// UpdateUptimeScore updates this relay's uptime score on-chain.
func (c *Client) UpdateUptimeScore(ctx context.Context, score uint64) error {
	err := c.executeMoveCall(ctx, "relay_info", "update_uptime_score", []interface{}{
		c.registryID,
		fmt.Sprintf("%d", score),
	})
	if err != nil {
		return fmt.Errorf("update uptime score: %w", err)
	}
	log.Printf("[sui] uptime score updated to %d", score)
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

	// Overlay RelayRegistered (relay_info module)
	relayRegType := fmt.Sprintf("%s::relay_info::RelayRegistered", c.packageID)
	if err := c.fetchAllEvents(ctx, relayRegType, peers, applyRelayRegistered); err != nil {
		return nil, fmt.Errorf("query RelayRegistered events: %w", err)
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
func (c *Client) PollNewEvents(ctx context.Context, cursor interface{}) ([]PeerInfo, interface{}, error) {
	peers := make(map[string]*PeerInfo)
	newCursor := cursor

	// Poll all event types with the cursor
	eventTypes := []string{
		fmt.Sprintf("%s::peer::PeerRegistered", c.packageID),
		fmt.Sprintf("%s::peer::PeerEndpointUpdated", c.packageID),
		fmt.Sprintf("%s::peer::PeerStatusChanged", c.packageID),
		fmt.Sprintf("%s::peer::PeerDeregistered", c.packageID),
		fmt.Sprintf("%s::relay_info::RelayRegistered", c.packageID),
	}

	appliers := []func(map[string]interface{}, map[string]*PeerInfo){
		applyPeerRegistered,
		applyPeerEndpointUpdated,
		applyPeerStatusChanged,
		applyPeerDeregistered,
		applyRelayRegistered,
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
			newCursor = resp.NextCursor
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
// If sponsor is enabled, routes through the Sponsor Service for gas sponsorship.
func (c *Client) executeMoveCall(ctx context.Context, module, function string, args []interface{}) error {
	if c.sponsor != nil {
		return c.executeViaSponsorship(ctx, module, function, args)
	}
	return c.executeDirect(ctx, module, function, args)
}

// executeDirect builds, signs, and executes a TX directly (self-paying gas).
func (c *Client) executeDirect(ctx context.Context, module, function string, args []interface{}) error {
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

// executeViaSponsorship sends the move call intent to the Sponsor Service,
// receives back (tx_bytes, sponsor_signature), co-signs with our key, and submits.
func (c *Client) executeViaSponsorship(ctx context.Context, module, function string, args []interface{}) error {
	req := SponsorRequest{
		Sender:    c.keypair.Address(),
		PackageID: c.packageID,
		Module:    module,
		Function:  function,
		TypeArgs:  []interface{}{},
		Args:      args,
	}

	resp, err := c.sponsor.RequestSponsorship(ctx, req)
	if err != nil {
		return fmt.Errorf("sponsor request: %w", err)
	}

	// Co-sign the tx_bytes with our keypair
	txnMeta := models.TxnMetaData{TxBytes: resp.TxBytes}
	signed := txnMeta.SignSerializedSigWith(c.keypair.Private)

	// Submit with both signatures (sponsor first, then sender)
	_, err = c.rpc.SuiExecuteTransactionBlock(ctx, models.SuiExecuteTransactionBlockRequest{
		TxBytes:   resp.TxBytes,
		Signature: []string{resp.SponsorSignature, signed.Signature},
		Options: models.SuiTransactionBlockOptions{
			ShowEffects: true,
			ShowEvents:  true,
		},
		RequestType: "WaitForLocalExecution",
	})
	if err != nil {
		return fmt.Errorf("execute sponsored tx: %w", err)
	}

	log.Printf("[sui] sponsored TX executed")
	return nil
}

// fetchAllEvents paginates through all events of the given type and applies them.
func (c *Client) fetchAllEvents(
	ctx context.Context,
	eventType string,
	peers map[string]*PeerInfo,
	apply func(map[string]interface{}, map[string]*PeerInfo),
) error {
	var cursor interface{} // nil for first request, EventId for subsequent
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
		cursor = resp.NextCursor
	}
	return nil
}

// ensureAgentCert discovers or creates an AgentCertificate for this node.
// Returns the cert object ID. Requires protocolPackageID to be configured.
func (c *Client) ensureAgentCert(ctx context.Context) (string, error) {
	if c.protocolPackageID == "" {
		return "", fmt.Errorf("protocol_package_id not configured; required for auto cert discovery")
	}

	certType := fmt.Sprintf("%s::agent::AgentCertificate", c.protocolPackageID)
	log.Printf("[sui] looking for AgentCertificate (type=%s)", certType)

	// 1. Query owned objects for existing cert
	resp, err := c.rpc.SuiXGetOwnedObjects(ctx, models.SuiXGetOwnedObjectsRequest{
		Address: c.keypair.Address(),
		Query: models.SuiObjectResponseQuery{
			Filter:  models.ObjectFilterByStructType{StructType: certType},
			Options: models.SuiObjectDataOptions{ShowType: true},
		},
		Limit: 10,
	})
	if err != nil {
		return "", fmt.Errorf("query owned objects: %w", err)
	}

	for _, obj := range resp.Data {
		if obj.Data != nil && obj.Data.ObjectId != "" {
			log.Printf("[sui] found existing AgentCertificate: %s", obj.Data.ObjectId)
			return obj.Data.ObjectId, nil
		}
	}

	// 2. No cert found — self-register as agent (permissionless)
	log.Printf("[sui] no AgentCertificate found, registering as agent in org %s...", c.orgID)
	err = c.executeProtocolCall(ctx, "entry", "register_agent", []interface{}{
		c.orgID,
		[]string{"envd-node"},
	})
	if err != nil {
		return "", fmt.Errorf("register agent: %w", err)
	}

	// 3. Query again to find newly created cert
	resp, err = c.rpc.SuiXGetOwnedObjects(ctx, models.SuiXGetOwnedObjectsRequest{
		Address: c.keypair.Address(),
		Query: models.SuiObjectResponseQuery{
			Filter:  models.ObjectFilterByStructType{StructType: certType},
			Options: models.SuiObjectDataOptions{ShowType: true},
		},
		Limit: 10,
	})
	if err != nil {
		return "", fmt.Errorf("query cert after registration: %w", err)
	}

	for _, obj := range resp.Data {
		if obj.Data != nil && obj.Data.ObjectId != "" {
			log.Printf("[sui] registered agent, cert: %s", obj.Data.ObjectId)
			return obj.Data.ObjectId, nil
		}
	}

	return "", fmt.Errorf("no AgentCertificate found after registration")
}

// executeProtocolCall builds, signs, and executes a Move call on the protocol package.
func (c *Client) executeProtocolCall(ctx context.Context, module, function string, args []interface{}) error {
	if c.sponsor != nil {
		req := SponsorRequest{
			Sender:    c.keypair.Address(),
			PackageID: c.protocolPackageID,
			Module:    module,
			Function:  function,
			TypeArgs:  []interface{}{},
			Args:      args,
		}
		resp, err := c.sponsor.RequestSponsorship(ctx, req)
		if err != nil {
			return fmt.Errorf("sponsor request: %w", err)
		}
		txnMeta := models.TxnMetaData{TxBytes: resp.TxBytes}
		signed := txnMeta.SignSerializedSigWith(c.keypair.Private)
		_, err = c.rpc.SuiExecuteTransactionBlock(ctx, models.SuiExecuteTransactionBlockRequest{
			TxBytes:   resp.TxBytes,
			Signature: []string{resp.SponsorSignature, signed.Signature},
			Options: models.SuiTransactionBlockOptions{
				ShowEffects: true,
				ShowEvents:  true,
			},
			RequestType: "WaitForLocalExecution",
		})
		if err != nil {
			return fmt.Errorf("execute sponsored tx: %w", err)
		}
		return nil
	}

	txn, err := c.rpc.MoveCall(ctx, models.MoveCallRequest{
		Signer:          c.keypair.Address(),
		PackageObjectId: c.protocolPackageID,
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

func applyRelayRegistered(data map[string]interface{}, peers map[string]*PeerInfo) {
	addr, _ := data["peer"].(string)
	if addr == "" {
		return
	}

	p, ok := peers[addr]
	if !ok {
		return
	}

	p.IsRelay = true
	if relayAddr, ok := data["relay_addr"].(string); ok {
		p.RelayAddr = relayAddr
	}
	if region, ok := data["region"].(string); ok {
		p.Region = region
	}
	if isp, ok := data["isp"].(string); ok {
		p.ISP = isp
	}
	if capacity, ok := data["relay_capacity"].(float64); ok {
		p.RelayCapacity = uint64(capacity)
	}
	p.UptimeScore = 100 // default from contract
}
