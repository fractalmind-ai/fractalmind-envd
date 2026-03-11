package relay

import (
	"encoding/binary"
	"fmt"
)

// WSS relay framing protocol.
//
// Data messages (WebSocket binary): [2-byte peer_id][WG packet]
// Control messages (WebSocket text): JSON
//
// Auth handshake:
//  1. Server sends challenge: {"type":"challenge","nonce":"<hex>"}
//  2. Client signs nonce with Ed25519 key and responds:
//     {"type":"auth","sui_address":"0x...","public_key":"<hex>","signature":"<hex>"}
//  3. Server verifies: derive SUI address from public_key, check match, verify signature
//
// This keeps data path minimal (2 bytes overhead) while control
// messages use human-readable JSON for debuggability.

// Signer signs data with an Ed25519 keypair. Implemented by sui.Keypair.
type Signer interface {
	Sign(data []byte) []byte
	PublicKeyBytes() []byte
}

// ControlMsg is a JSON control message sent over WebSocket text frames.
type ControlMsg struct {
	Type string `json:"type"`

	// Challenge fields (relay → client)
	Nonce string `json:"nonce,omitempty"` // hex-encoded random challenge

	// Auth fields (client → relay)
	SUiAddress  string `json:"sui_address,omitempty"`
	PublicKey   string `json:"public_key,omitempty"`    // hex-encoded Ed25519 public key
	Signature   string `json:"signature,omitempty"`     // hex-encoded Ed25519 signature of nonce
	WGPublicKey string `json:"wg_public_key,omitempty"` // hex-encoded WireGuard public key

	// Allocated fields (relay → client)
	Endpoint string `json:"endpoint,omitempty"`

	// Peer management (client → relay)
	PeerID uint16 `json:"id,omitempty"`
	Target string `json:"target,omitempty"` // "ip:port" of the WG peer
}

// Control message types.
const (
	MsgTypeChallenge  = "challenge"
	MsgTypeAuth       = "auth"
	MsgTypeAllocated  = "allocated"
	MsgTypeAddPeer    = "add_peer"
	MsgTypeRemovePeer = "remove_peer"
	MsgTypePing       = "ping"
	MsgTypePong       = "pong"
	MsgTypeError      = "error"
)

// EncodeDataMsg prepends a 2-byte peer ID to a WireGuard packet.
func EncodeDataMsg(peerID uint16, wgPacket []byte) []byte {
	msg := make([]byte, 2+len(wgPacket))
	binary.BigEndian.PutUint16(msg[:2], peerID)
	copy(msg[2:], wgPacket)
	return msg
}

// DecodeDataMsg extracts the peer ID and WireGuard packet from a data message.
func DecodeDataMsg(msg []byte) (peerID uint16, wgPacket []byte, err error) {
	if len(msg) < 3 { // 2-byte header + at least 1 byte of data
		return 0, nil, fmt.Errorf("data message too short: %d bytes", len(msg))
	}
	peerID = binary.BigEndian.Uint16(msg[:2])
	wgPacket = msg[2:]
	return peerID, wgPacket, nil
}
