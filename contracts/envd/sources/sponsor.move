/// fractalmind-envd — Gas Sponsor Registry
/// Manages org-level gas sponsorship policies.
module fractalmind_envd::sponsor {
    use sui::table::{Self, Table};
    use sui::event;

    use fractalmind_protocol::organization::Organization;
    use fractalmind_protocol::organization;

    // ===== Error Codes (8100) =====

    const E_NOT_ADMIN: u64 = 8101;
    const E_SPONSOR_EXISTS: u64 = 8102;
    const E_SPONSOR_NOT_FOUND: u64 = 8103;

    // ===== Structs =====

    /// Org-level gas sponsorship configuration (shared object)
    public struct SponsorRegistry has key {
        id: UID,
        /// org_id → SponsorConfig
        sponsors: Table<ID, SponsorConfig>,
    }

    /// Sponsorship policy for a single organization
    public struct SponsorConfig has store, drop {
        /// Sponsor admin (usually org admin)
        admin: address,
        /// Whether sponsoring is enabled
        enabled: bool,
        /// Max gas budget per transaction (MIST)
        max_gas_per_tx: u64,
        /// Daily gas limit (MIST)
        daily_gas_limit: u64,
        /// Gas used today (MIST), reset daily
        daily_gas_used: u64,
        /// Last reset epoch
        last_reset_epoch: u64,
    }

    // ===== Events =====

    public struct SponsorEnabled has copy, drop {
        org_id: ID,
        admin: address,
        max_gas_per_tx: u64,
        daily_gas_limit: u64,
    }

    public struct SponsorDisabled has copy, drop {
        org_id: ID,
    }

    // ===== Init =====

    fun init(ctx: &mut TxContext) {
        let registry = SponsorRegistry {
            id: object::new(ctx),
            sponsors: table::new(ctx),
        };
        transfer::share_object(registry);
    }

    // ===== Public Functions =====

    /// Org admin enables gas sponsorship
    public fun enable_sponsor(
        registry: &mut SponsorRegistry,
        org: &Organization,
        max_gas_per_tx: u64,
        daily_gas_limit: u64,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();
        let org_id = organization::org_id(org);

        // Only org admin can enable
        assert!(organization::admin(org) == sender, E_NOT_ADMIN);
        assert!(!registry.sponsors.contains(org_id), E_SPONSOR_EXISTS);

        let config = SponsorConfig {
            admin: sender,
            enabled: true,
            max_gas_per_tx,
            daily_gas_limit,
            daily_gas_used: 0,
            last_reset_epoch: ctx.epoch(),
        };

        registry.sponsors.add(org_id, config);

        event::emit(SponsorEnabled {
            org_id,
            admin: sender,
            max_gas_per_tx,
            daily_gas_limit,
        });
    }

    /// Org admin disables gas sponsorship
    public fun disable_sponsor(
        registry: &mut SponsorRegistry,
        org: &Organization,
        ctx: &mut TxContext,
    ) {
        let sender = ctx.sender();
        let org_id = organization::org_id(org);

        assert!(organization::admin(org) == sender, E_NOT_ADMIN);
        assert!(registry.sponsors.contains(org_id), E_SPONSOR_NOT_FOUND);

        let config = registry.sponsors.borrow_mut(org_id);
        config.enabled = false;

        event::emit(SponsorDisabled { org_id });
    }

    // ===== Query Functions =====

    public fun get_sponsor(registry: &SponsorRegistry, org_id: ID): &SponsorConfig {
        registry.sponsors.borrow(org_id)
    }

    public fun is_enabled(config: &SponsorConfig): bool { config.enabled }
    public fun sponsor_admin(config: &SponsorConfig): address { config.admin }
    public fun max_gas_per_tx(config: &SponsorConfig): u64 { config.max_gas_per_tx }
    public fun daily_gas_limit(config: &SponsorConfig): u64 { config.daily_gas_limit }

    // ===== Test Helpers =====

    #[test_only]
    public fun create_test_registry(ctx: &mut TxContext): SponsorRegistry {
        SponsorRegistry {
            id: object::new(ctx),
            sponsors: table::new(ctx),
        }
    }

    #[test_only]
    public fun destroy_test_registry(registry: SponsorRegistry) {
        let SponsorRegistry { id, sponsors } = registry;
        sponsors.drop();
        object::delete(id);
    }
}
