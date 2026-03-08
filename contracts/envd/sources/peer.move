/// fractalmind-envd — Peer Registry
/// Manages envd node WireGuard public key and endpoint registration,
/// implementing decentralized peer discovery via SUI Events.
module fractalmind_envd::peer {
    use sui::table::{Self, Table};
    use sui::event;
    use std::string::String;

    use fractalmind_protocol::agent::AgentCertificate;
    use fractalmind_protocol::organization::Organization;
    use fractalmind_protocol::agent;
    use fractalmind_protocol::organization;
    use fractalmind_protocol::constants;

    // ===== Error Codes (8xxx) =====

    const E_PEER_ALREADY_REGISTERED: u64 = 8001;
    const E_PEER_NOT_FOUND: u64 = 8002;
    const E_NOT_PEER_OWNER: u64 = 8003;
    const E_INVALID_WIREGUARD_KEY: u64 = 8004;
    const E_NO_ENDPOINTS: u64 = 8005;

    // ===== Peer Status =====

    const PEER_STATUS_ONLINE: u8 = 0;
    const PEER_STATUS_OFFLINE: u8 = 1;

    // ===== Structs =====

    /// Global Peer Registry (shared object).
    /// One per fractalmind-envd deployment.
    public struct PeerRegistry has key {
        id: UID,
        /// node_address → PeerNode
        peers: Table<address, PeerNode>,
        /// Total peer count
        peer_count: u64,
    }

    /// Network information for a single envd node.
    /// Stored in PeerRegistry.peers Table.
    public struct PeerNode has store, drop {
        /// Organization this node belongs to
        org_id: ID,
        /// Associated AgentCertificate ID
        cert_id: ID,
        /// WireGuard public key (32 bytes, Curve25519)
        wireguard_pubkey: vector<u8>,
        /// Network endpoints ["1.2.3.4:51820", "10.0.0.1:51820"]
        endpoints: vector<String>,
        /// Hostname for identification
        hostname: String,
        /// Online/Offline status
        status: u8,
        /// Registration time (epoch ms)
        registered_at: u64,
        /// Last update time (epoch ms)
        last_updated: u64,
    }

    // ===== Events =====
    // envd nodes subscribe to these events for peer discovery

    /// New node registered — contains all info needed to establish WireGuard tunnel
    public struct PeerRegistered has copy, drop {
        peer: address,
        org_id: ID,
        wireguard_pubkey: vector<u8>,
        endpoints: vector<String>,
        hostname: String,
    }

    /// Node endpoint changed (IP drift, port change)
    public struct PeerEndpointUpdated has copy, drop {
        peer: address,
        org_id: ID,
        new_endpoints: vector<String>,
    }

    /// Node status changed (online/offline)
    public struct PeerStatusChanged has copy, drop {
        peer: address,
        org_id: ID,
        new_status: u8,
    }

    /// Node deregistered
    public struct PeerDeregistered has copy, drop {
        peer: address,
        org_id: ID,
    }

    // ===== Init =====

    /// Create and share PeerRegistry. Called automatically on publish.
    fun init(ctx: &mut TxContext) {
        let registry = PeerRegistry {
            id: object::new(ctx),
            peers: table::new(ctx),
            peer_count: 0,
        };
        transfer::share_object(registry);
    }

    // ===== Public Functions =====

    /// Register an envd node.
    /// Requires: caller holds an active AgentCertificate and is a member of the Organization.
    public fun register_peer(
        registry: &mut PeerRegistry,
        org: &Organization,
        cert: &AgentCertificate,
        wireguard_pubkey: vector<u8>,
        endpoints: vector<String>,
        hostname: String,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();
        let org_id = organization::org_id(org);
        let now = ctx.epoch_timestamp_ms();

        // Authorization checks
        assert!(agent::cert_agent(cert) == sender, constants::e_unauthorized());
        assert!(agent::cert_status(cert) == constants::agent_status_active(), constants::e_agent_not_active());
        assert!(agent::cert_org_id(cert) == org_id, constants::e_not_member());
        assert!(organization::has_agent(org, sender), constants::e_not_member());

        // Parameter validation
        assert!(wireguard_pubkey.length() == 32, E_INVALID_WIREGUARD_KEY);
        assert!(!endpoints.is_empty(), E_NO_ENDPOINTS);
        assert!(!registry.peers.contains(sender), E_PEER_ALREADY_REGISTERED);

        let node = PeerNode {
            org_id,
            cert_id: object::id(cert),
            wireguard_pubkey,
            endpoints,
            hostname,
            status: PEER_STATUS_ONLINE,
            registered_at: now,
            last_updated: now,
        };

        // Event contains all info needed to establish WireGuard tunnel
        event::emit(PeerRegistered {
            peer: sender,
            org_id,
            wireguard_pubkey: node.wireguard_pubkey,
            endpoints: node.endpoints,
            hostname: node.hostname,
        });

        registry.peers.add(sender, node);
        registry.peer_count = registry.peer_count + 1;
    }

    /// Update endpoints (IP drift, port change). Only the node itself can call.
    public fun update_endpoints(
        registry: &mut PeerRegistry,
        new_endpoints: vector<String>,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();

        assert!(registry.peers.contains(sender), E_PEER_NOT_FOUND);
        assert!(!new_endpoints.is_empty(), E_NO_ENDPOINTS);

        let node = registry.peers.borrow_mut(sender);
        node.endpoints = new_endpoints;
        node.last_updated = ctx.epoch_timestamp_ms();

        event::emit(PeerEndpointUpdated {
            peer: sender,
            org_id: node.org_id,
            new_endpoints: node.endpoints,
        });
    }

    /// Mark offline (graceful shutdown). Only the node itself can call.
    public fun go_offline(
        registry: &mut PeerRegistry,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();

        assert!(registry.peers.contains(sender), E_PEER_NOT_FOUND);

        let node = registry.peers.borrow_mut(sender);
        node.status = PEER_STATUS_OFFLINE;
        node.last_updated = ctx.epoch_timestamp_ms();

        event::emit(PeerStatusChanged {
            peer: sender,
            org_id: node.org_id,
            new_status: PEER_STATUS_OFFLINE,
        });
    }

    /// Come back online. Only the node itself can call.
    public fun go_online(
        registry: &mut PeerRegistry,
        new_endpoints: vector<String>,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();

        assert!(registry.peers.contains(sender), E_PEER_NOT_FOUND);
        assert!(!new_endpoints.is_empty(), E_NO_ENDPOINTS);

        let node = registry.peers.borrow_mut(sender);
        node.status = PEER_STATUS_ONLINE;
        node.endpoints = new_endpoints;
        node.last_updated = ctx.epoch_timestamp_ms();

        event::emit(PeerStatusChanged {
            peer: sender,
            org_id: node.org_id,
            new_status: PEER_STATUS_ONLINE,
        });

        // Also emit endpoint update since IP may have changed on reconnect
        event::emit(PeerEndpointUpdated {
            peer: sender,
            org_id: node.org_id,
            new_endpoints: node.endpoints,
        });
    }

    /// Deregister a node. Only the node itself or org admin can call.
    public fun deregister_peer(
        registry: &mut PeerRegistry,
        peer_address: address,
        org: &Organization,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();

        assert!(registry.peers.contains(peer_address), E_PEER_NOT_FOUND);
        // Self or org admin
        assert!(
            sender == peer_address || organization::admin(org) == sender,
            E_NOT_PEER_OWNER,
        );

        let node = registry.peers.remove(peer_address);
        registry.peer_count = registry.peer_count - 1;

        event::emit(PeerDeregistered {
            peer: peer_address,
            org_id: node.org_id,
        });
    }

    // ===== Query Functions =====

    public fun peer_count(registry: &PeerRegistry): u64 { registry.peer_count }

    public fun has_peer(registry: &PeerRegistry, addr: address): bool {
        registry.peers.contains(addr)
    }

    public fun get_peer(registry: &PeerRegistry, addr: address): &PeerNode {
        registry.peers.borrow(addr)
    }

    public fun peer_org_id(node: &PeerNode): ID { node.org_id }
    public fun peer_wireguard_pubkey(node: &PeerNode): vector<u8> { node.wireguard_pubkey }
    public fun peer_endpoints(node: &PeerNode): vector<String> { node.endpoints }
    public fun peer_hostname(node: &PeerNode): String { node.hostname }
    public fun peer_status(node: &PeerNode): u8 { node.status }
    public fun peer_is_online(node: &PeerNode): bool { node.status == PEER_STATUS_ONLINE }

    public fun peer_status_online(): u8 { PEER_STATUS_ONLINE }
    public fun peer_status_offline(): u8 { PEER_STATUS_OFFLINE }
    public fun e_peer_not_found(): u64 { E_PEER_NOT_FOUND }

    // ===== Package-visible UID Accessors =====
    // Enable dynamic field extensions from other modules in this package

    public(package) fun borrow_registry_uid(registry: &PeerRegistry): &UID { &registry.id }

    public(package) fun borrow_registry_uid_mut(registry: &mut PeerRegistry): &mut UID { &mut registry.id }

    // ===== Test Helpers =====

    #[test_only]
    public fun create_test_registry(ctx: &mut TxContext): PeerRegistry {
        PeerRegistry {
            id: object::new(ctx),
            peers: table::new(ctx),
            peer_count: 0,
        }
    }

    #[test_only]
    public fun destroy_test_registry(registry: PeerRegistry) {
        let PeerRegistry { id, peers, peer_count: _ } = registry;
        peers.drop();
        object::delete(id);
    }
}
