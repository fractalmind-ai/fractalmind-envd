/// fractalmind-envd — Relay Info Extension
/// Stores relay-specific metadata as dynamic fields on PeerRegistry.
/// Uses Dynamic Fields to extend PeerRegistry without modifying PeerNode struct
/// (compatible with existing deployed package layout).
module fractalmind_envd::relay_info {
    use sui::dynamic_field;
    use sui::event;
    use std::string::String;

    use fractalmind_envd::peer::{Self, PeerRegistry};

    // ===== Error Codes (8100+) =====

    const E_NOT_A_RELAY: u64 = 8101;
    const E_ALREADY_RELAY: u64 = 8102;

    // ===== Structs =====

    /// Key for dynamic field: one RelayInfo per peer address
    public struct RelayInfoKey has copy, drop, store { peer: address }

    /// Relay-specific metadata for a peer node.
    /// Stored as dynamic field on PeerRegistry.
    public struct RelayInfo has store, drop {
        /// Relay server address (e.g., "1.2.3.4:3478")
        relay_addr: String,
        /// Geographic region (e.g., "cn-east", "us-west")
        region: String,
        /// Network provider (e.g., "aliyun", "aws")
        isp: String,
        /// Max simultaneous relay connections
        relay_capacity: u64,
        /// Uptime score (0-100, updated by coordinator or self)
        uptime_score: u64,
        /// Registration time (epoch ms)
        registered_at: u64,
    }

    // ===== Events =====

    /// Emitted when a peer registers as a relay node
    public struct RelayRegistered has copy, drop {
        peer: address,
        relay_addr: String,
        region: String,
        isp: String,
        relay_capacity: u64,
    }

    /// Emitted when uptime score is updated
    public struct UptimeScoreUpdated has copy, drop {
        peer: address,
        new_score: u64,
    }

    /// Emitted when a relay deregisters
    public struct RelayDeregistered has copy, drop {
        peer: address,
    }

    // ===== Public Functions =====

    /// Register the calling node as a relay.
    /// Requires the peer to be already registered in PeerRegistry.
    public fun register_relay(
        registry: &mut PeerRegistry,
        relay_addr: String,
        region: String,
        isp: String,
        relay_capacity: u64,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();
        let now = ctx.epoch_timestamp_ms();

        // Must be a registered peer
        assert!(peer::has_peer(registry, sender), peer::e_peer_not_found());

        // Must not already be a relay
        let uid = peer::borrow_registry_uid(registry);
        let key = RelayInfoKey { peer: sender };
        assert!(!dynamic_field::exists_(uid, key), E_ALREADY_RELAY);

        let info = RelayInfo {
            relay_addr,
            region,
            isp,
            relay_capacity,
            uptime_score: 100, // Start at 100%
            registered_at: now,
        };

        event::emit(RelayRegistered {
            peer: sender,
            relay_addr: info.relay_addr,
            region: info.region,
            isp: info.isp,
            relay_capacity,
        });

        let uid_mut = peer::borrow_registry_uid_mut(registry);
        dynamic_field::add(uid_mut, key, info);
    }

    /// Update uptime score. Only the relay itself can call.
    public fun update_uptime_score(
        registry: &mut PeerRegistry,
        new_score: u64,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();
        let key = RelayInfoKey { peer: sender };

        let uid = peer::borrow_registry_uid(registry);
        assert!(dynamic_field::exists_(uid, key), E_NOT_A_RELAY);

        let uid_mut = peer::borrow_registry_uid_mut(registry);
        let info = dynamic_field::borrow_mut<RelayInfoKey, RelayInfo>(uid_mut, key);
        info.uptime_score = new_score;

        event::emit(UptimeScoreUpdated {
            peer: sender,
            new_score,
        });
    }

    /// Deregister as relay. Only the relay itself can call.
    public fun deregister_relay(
        registry: &mut PeerRegistry,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();
        let key = RelayInfoKey { peer: sender };

        let uid_mut = peer::borrow_registry_uid_mut(registry);
        assert!(dynamic_field::exists_<RelayInfoKey>(uid_mut, key), E_NOT_A_RELAY);

        let _info: RelayInfo = dynamic_field::remove(uid_mut, key);

        event::emit(RelayDeregistered {
            peer: sender,
        });
    }

    // ===== Query Functions =====

    /// Check if a peer is registered as a relay
    public fun is_relay(registry: &PeerRegistry, addr: address): bool {
        let uid = peer::borrow_registry_uid(registry);
        dynamic_field::exists_(uid, RelayInfoKey { peer: addr })
    }

    /// Get relay info for a peer (aborts if not a relay)
    public fun get_relay_info(registry: &PeerRegistry, addr: address): &RelayInfo {
        let uid = peer::borrow_registry_uid(registry);
        dynamic_field::borrow<RelayInfoKey, RelayInfo>(uid, RelayInfoKey { peer: addr })
    }

    public fun relay_addr(info: &RelayInfo): String { info.relay_addr }
    public fun relay_region(info: &RelayInfo): String { info.region }
    public fun relay_isp(info: &RelayInfo): String { info.isp }
    public fun relay_capacity(info: &RelayInfo): u64 { info.relay_capacity }
    public fun uptime_score(info: &RelayInfo): u64 { info.uptime_score }
}
