# TCP Relay Fallback — Design Document

> **Issue**: [#8](https://github.com/fractalmind-ai/fractalmind-envd/issues/8)
> **RFC**: [Discussion #9](https://github.com/fractalmind-ai/fractalmind-envd/discussions/9)
> **Status**: Prototype

## Problem

When envd runs behind a firewall that blocks all outbound UDP (except DNS port 53), both STUN endpoint discovery and WireGuard P2P connectivity fail completely. The node registers with endpoint `0.0.0.0:51820` and is effectively unreachable.

## Solution: WebSocket Relay (WSS/443)

Add a TCP-based relay transport as a fallback when UDP is unavailable. Port 443 (HTTPS) is essentially never blocked by firewalls.

## Architecture

```
┌─────────────────────┐                           ┌─────────────────────┐
│  Restricted Node    │                           │  Normal Peer        │
│                     │         WSS/443           │                     │
│  WireGuard ──UDP──▶ │  ◀══════════════════════▶ │                     │
│  (wg0)    local     │                           │  WireGuard ──UDP──▶ │
│       proxy ports   │     ┌───────────────┐     │  (wg0)              │
│                     │     │  Relay Server │     │                     │
│  127.0.0.1:59001 ───┼────▶│               │◀────┼── relay_ip:51923   │
│  127.0.0.1:59002 ───┼────▶│  UDP :51923   │     │                     │
│         ...         │     │  WSS :443     │     │                     │
└─────────────────────┘     └───────────────┘     └─────────────────────┘
```

### Key Design: Transparent Relay Endpoint

The relay allocates a **public UDP port per restricted client**. The client registers `relay_ip:allocated_port` as its SUI endpoint. Other peers send WireGuard packets to this address normally — they don't know the target is relayed.

### Packet Flow

**Outgoing (Restricted → Peer):**
```
1. WireGuard sends to peer endpoint 127.0.0.1:59001 (local proxy)
2. Local proxy receives UDP packet
3. Proxy wraps: [2-byte peer_id][WG packet] → WSS binary message
4. Relay receives, looks up peer_id → target endpoint (e.g., 203.0.113.5:51820)
5. Relay sends UDP from allocated_port to target endpoint
```

**Incoming (Peer → Restricted):**
```
1. Peer sends WireGuard packet to relay_ip:allocated_port
2. Relay receives UDP, looks up source → peer_id
3. Relay wraps: [2-byte peer_id][WG packet] → WSS binary message
4. Client proxy receives, looks up peer_id → local port 59001
5. Proxy sends UDP from 127.0.0.1:59001 to WireGuard (127.0.0.1:51820)
6. WireGuard processes normally (identifies peer by public key)
```

## Detection Flow

```
envd starts
  │
  ├─ STUN probe ──── success ──▶ UDP direct mode (existing)
  │
  └─ STUN fails
       │
       ├─ UDP probe to relay ──── success ──▶ UDP relay mode (existing TURN)
       │
       └─ UDP probe fails
            │
            └─ TCP probe to relay WSS ──── success ──▶ WSS relay mode (NEW)
                                     └─── fails ──▶ offline (retry)
```

## Protocol

WebSocket binary/text message separation:

### Data Messages (Binary)
```
[2 bytes: peer_id (big-endian uint16)][N bytes: raw WireGuard packet]
```
Overhead: 2 bytes per packet. WireGuard packets are already encrypted.

### Control Messages (Text/JSON)
```json
// Client → Relay: register for relay service
{"type": "auth", "sui_address": "0x...", "signature": "..."}

// Relay → Client: allocated public endpoint
{"type": "allocated", "endpoint": "relay.example.com:51923"}

// Client → Relay: add peer route
{"type": "add_peer", "id": 1, "target": "203.0.113.5:51820"}

// Client → Relay: remove peer route
{"type": "remove_peer", "id": 1}

// Bidirectional: keepalive (every 25s)
{"type": "ping"}
{"type": "pong"}
```

## Package Structure

```
internal/relay/
├── server.go          # Existing STUN + TURN server
├── protocol.go        # NEW: WSS framing types + constants
├── wshandler.go       # NEW: WSS relay handler (server-side)
├── wsclient.go        # NEW: WSS relay client (restricted nodes)
├── wsclient_test.go   # NEW: Client unit tests
└── wshandler_test.go  # NEW: Handler unit tests
```

## Key Interfaces

```go
// WSSRelayHandler handles WSS connections from restricted clients (relay server side).
type WSSRelayHandler struct {
    publicIP   string
    portMin    int                         // UDP port pool start (default: 51900)
    portMax    int                         // UDP port pool end (default: 51999)
    clients    map[string]*relayAllocation // SUI address → allocation
    httpServer *http.Server
}

// WSSRelayClient connects to a relay via WSS (restricted node side).
type WSSRelayClient struct {
    relayURL      string
    conn          *websocket.Conn
    wgListenAddr  string              // WireGuard's UDP address (127.0.0.1:51820)
    peers         map[uint16]*proxyPeer // peer_id → local UDP proxy
    relayEndpoint string              // Assigned public endpoint from relay
}
```

## Config

```yaml
relay:
  listen_port: 3478                          # Existing: UDP STUN/TURN
  max_connections: 100                       # Existing
  tcp_fallback: true                         # NEW: enable WSS fallback
  relay_url: "wss://relay.example.com/wg-relay"  # NEW: WSS relay endpoint
  wss_listen_port: 443                       # NEW: WSS server port (relay mode)
  wss_port_min: 51900                        # NEW: UDP port pool start
  wss_port_max: 51999                        # NEW: UDP port pool end
```

## Security

Defense-in-depth:
1. **WSS/TLS** encrypts the WebSocket transport. TLS can be configured directly (`wss_cert_file`/`wss_key_file`) or terminated at a reverse proxy (nginx, Caddy, cloud LB). If neither is configured, the server starts as plain HTTP with a warning.
2. **WireGuard** encrypts inner packets (relay sees opaque ciphertext)
3. **SUI identity** — clients authenticate via Ed25519 challenge-response: relay sends a random nonce, client signs with their SUI keypair, relay verifies signature and derives SUI address from the public key to confirm ownership.
4. **Target validation** — `add_peer` rejects loopback/unspecified addresses to prevent relay abuse.

The relay only sees encrypted WireGuard packets inside an encrypted WSS tunnel.

## Testing Strategy

1. **Unit tests**: Mock WebSocket connections, verify framing encode/decode
2. **Integration tests**: Local WSS server + client, verify UDP packets round-trip through WSS
3. **Loopback test**: Two WireGuard peers communicating through local WSS relay
4. **Manual test**: Run on @rosexrwa's restricted machine against testnet relay

## Limitations (Prototype)

- Single relay endpoint (no relay failover)
- No bandwidth limiting
- No SUI-signed authentication (placeholder for KR5)
- Port pool is static (not dynamic)
- No metrics/monitoring on WSS relay connections
