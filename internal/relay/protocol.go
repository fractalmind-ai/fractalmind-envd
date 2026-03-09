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
// This keeps data path minimal (2 bytes overhead) while control
// messages use human-readable JSON for debuggability.

// ControlMsg is a JSON control message sent over WebSocket text frames.
type ControlMsg struct {
	Type string `json:"type"`

	// Auth fields (client → relay)
	SUiAddress string `json:"sui_address,omitempty"`

	// Allocated fields (relay → client)
	Endpoint string `json:"endpoint,omitempty"`

	// Peer management (client → relay)
	PeerID uint16 `json:"id,omitempty"`
	Target string `json:"target,omitempty"` // "ip:port" of the WG peer
}

// Control message types.
const (
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
