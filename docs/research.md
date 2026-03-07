# 竞品调研 + 架构设计: fractalmind-envd

> fractalmind-envd KR1 | 2026-03-07 | Owner: OpenClaw
> **v2** — 经 Elliot 评审修订: 去掉 Gateway, 数据面改为 WireGuard P2P, 设计 SUI 合约

---

## 调研目标

研究主流远程控制/穿透产品的架构，明确 fractalmind-envd 的差异化定位：**用 SUI 区块链替代中央服务**。

---

## 竞品分析

### 1. 花生壳 (Oray)

**产品**: DDNS + NAT 穿透服务，国内最大的内网穿透提供商。

| 维度 | 详情 |
|------|------|
| **架构** | 中心化。客户端 → Oray 服务器 → 转发到内网设备 |
| **穿透方式** | STUN/TURN + Oray 私有协议。免费版通过 Oray 服务器中转，付费版支持 P2P |
| **认证** | Oray 账号 + SN 码（设备绑定硬件序列号） |
| **信任根** | Oray 服务器。所有流量可被 Oray 审查/中断 |
| **优势** | 中文生态好，企业级支持，无需公网 IP |
| **劣势** | 强依赖 Oray 服务器，免费版带宽极小，数据过中心 |

### 2. ToDesk

**产品**: 远程桌面控制，对标 TeamViewer/向日葵。

| 维度 | 详情 |
|------|------|
| **架构** | 中心化信令 + P2P 数据流。信令服务器协调连接，数据尝试 P2P 直连 |
| **穿透方式** | ICE (STUN + TURN)。P2P 失败时回退到 ToDesk relay |
| **认证** | ToDesk 账号 + 设备码 + 临时密码 |
| **信任根** | ToDesk 信令服务器。控制连接的建立/断开 |
| **优势** | 画面流畅（4K 60fps），跨平台，P2P 优先 |
| **劣势** | 面向 GUI 桌面控制，非为 headless 服务器设计 |

### 3. TeamViewer

**产品**: 远程桌面/远程支持行业标杆，全球 60 万+ 付费客户。

| 维度 | 详情 |
|------|------|
| **架构** | 中心化 Master Server 做连接撮合，P2P (~70%) + Relay (~30%) 数据传输 |
| **穿透方式** | 专有 UDP hole punching (port 5938)，fallback TCP/443、TCP/80。P2P 成功率 ~70%，失败时通过 TeamViewer Router Network 中转 |
| **认证** | TeamViewer ID + 密码，由 Master Server 验证 |
| **信任根** | TeamViewer Master Server。控制连接建立/断开、ID 分配、密码验证 |
| **加密** | RSA 4096 密钥交换 + AES-256 会话加密 (E2E，Relay 无法解密) |
| **协议** | 专有协议 (port 5938 TCP/UDP)，封装远程桌面画面 + 键鼠 + 文件传输 |
| **费用** | 免费个人版；Remote Access $24.90/月；Business $50.90/月；Premium $112.90/月；Corporate $229.90/月 (均年付) |
| **优势** | P2P 成功率高 (~70%)、E2E 加密、全平台支持、企业级功能完善 |
| **劣势** | 专有协议不可审计、Master Server 是单点信任、商业授权贵、面向 GUI 非 CLI |

**关键架构细节**:
- Master Server 角色: 连接撮合 (brokering) + ID 认证 + NAT 信息交换
- P2P 流程: 双方先连 Master Server (HTTPS/SSL) → PIN/Code 验证 → Master Server 交换双方公网 IP:port → UDP hole punching 建立直连
- Relay 流程: UDP hole punching 失败 → 通过 TeamViewer Router Network 中转 (TCP/HTTPS tunneling)
- 即使经过 Relay，数据也是 E2E 加密的，TeamViewer 无法解密

### 4. Tailscale

**产品**: 基于 WireGuard 的 Mesh VPN，零配置组网。

| 维度 | 详情 |
|------|------|
| **架构** | 半中心化。**控制平面** (Coordination Server) 中心化，**数据平面** WireGuard P2P |
| **穿透方式** | WireGuard + ICE + DERP relay (Tailscale 自建，无状态中转) |
| **认证** | SSO (Google/MS/GitHub) → Coordination Server → 分发 WireGuard 公钥 |
| **信任根** | Tailscale Coordination Server。管理节点加入/退出、ACL、密钥轮换 |
| **优势** | 零配置、WireGuard 加密、MagicDNS、ACL、开源替代 (Headscale) |
| **劣势** | 控制平面仍是中心化信任，key 由中心管理 |

---

## 架构对比矩阵

| 特征 | 花生壳 | ToDesk | TeamViewer | Tailscale | **fractalmind-envd** |
|------|--------|--------|------------|-----------|---------------------|
| **信任根** | Oray 服务器 | ToDesk 服务器 | TV Master Server | Tailscale Coord | **SUI 区块链** |
| **控制平面** | 中心化 | 中心化 | 中心化 | 中心化 | **链上 (去中心化)** |
| **数据平面** | 中心转发 | P2P/relay | **P2P 70%** / relay 30% | **WireGuard P2P** | **WireGuard P2P** |
| **P2P 成功率** | 低 (付费) | ~60-80% | **~70%** (专有 UDP) | **~95%** (WireGuard) | **~95%** (WireGuard) |
| **身份认证** | 账号密码 | 设备码 | ID+密码 | SSO + WG keys | **SUI keypair** |
| **Peer 发现** | Oray 服务器 | ToDesk 服务器 | Master Server | Coord Server | **SUI Events** |
| **Relay** | Oray 转发 | ToDesk relay | TV Router Network | DERP (无状态) | **TURN (无状态, 可选)** |
| **加密** | TLS | TLS | **RSA4096 + AES256 E2E** | **WireGuard (ChaCha20)** | **WireGuard (ChaCha20)** |
| **审计性** | 不透明 | 不透明 | 不透明 | ACL 日志 | **链上全记录** |
| **治理** | 厂商决策 | 厂商决策 | 厂商决策 | ACL 规则 | **DAO 链上投票** |
| **单点故障** | Oray 宕机 | ToDesk 宕机 | TV Master 宕机 | Coord 宕机 | **无 (链不停)** |
| **月费 (团队)** | ¥6-168/年 | ¥8-25/月 | **$50.90-229.90/月** | $6/user/月 | **~$0.14/月** |

**吸收的竞品优势**:
- Tailscale → WireGuard P2P 数据平面 + DERP relay 模式
- TeamViewer → P2P 优先 + Relay 兜底架构 + E2E 加密 (即使过 Relay)
- ToDesk → P2P 优先, relay 兜底
- 花生壳 → STUN/TURN NAT 穿透

**我们的差异化**:
- Tailscale Coordination Server → **SUI 区块链** (去中心化控制平面)
- 中心化密钥管理 → **链上 WireGuard 公钥注册** (不可篡改)
- 厂商 ACL → **链上 AgentCertificate + DAO 治理**

---

## 修订后架构: 无 Gateway

### 为什么去掉 Gateway

初版架构有一个 Gateway (TypeScript) 作为中心节点。Elliot 评审指出：

> "Gateway 本质上还是中央服务器。如果真的相信 SUI 替代中央服务，就该把 Gateway 也去掉。"
> "如果要保留，应该叫 Relay — 只做 NAT 穿透失败时的数据中转。"

Gateway 的 5 个职责拆解:

| 原 Gateway 职责 | 替代方案 |
|-----------------|---------|
| 身份认证 | → SUI AgentCertificate (链上) |
| Peer 发现 | → SUI Events (PeerRegistered) |
| 指令路由 | → WireGuard P2P 直连 |
| 状态聚合 | → P2P 心跳 + 链上关键状态 |
| 数据中转 | → TURN Relay (无状态, 可选) |

### 最终架构

```
SUI 区块链 (唯一协调层)
├── PeerRegistry: WireGuard 公钥 + endpoint 注册
├── AgentCertificate: 身份 + 权限 + 声誉
├── Organization: 组织成员关系 (决定谁能发现谁)
├── Events: PeerRegistered / PeerUpdated / PeerOffline
└── Governance: DAO 投票决策

envd (统一二进制, 每台机器运行同一个程序)
├── WireGuard: P2P mesh 隧道 (数据平面)
├── SUI Client: 读 peer 列表 / 注册自己 / 监听事件 (控制平面)
├── Agent Scanner: tmux session 扫描
├── Self-heal: 崩溃检测 + 自动重启 (max 3 次)
├── REST API: 本地管理接口 (coordinator 模式开启)
├── STUN Client: NAT endpoint 发现
└── P2P Heartbeat: 节点间直接心跳

TURN Relay (可选, 无状态)
└── 仅在 P2P 不通时中转加密的 WireGuard 包
```

### 连接流程

```
1. envd 启动
   → 读 sentinel.yaml 获取 SUI RPC + org_id + 本地 keypair
   → 生成 WireGuard keypair (或读取已有)

2. 注册到链上
   → 调用 PeerRegistry::register_peer(cert, wg_pubkey, endpoints)
   → SUI 发出 PeerRegistered 事件

3. 发现 Peer
   → 查询历史 PeerRegistered 事件 (过滤 org_id)
   → 订阅新事件 (实时发现新节点)
   → 建立 WireGuard tunnel 到每个 peer

4. P2P 通信
   → 心跳: envd ← WireGuard → envd (直连)
   → 指令: coordinator envd → WireGuard → target envd
   → 日志: target envd → WireGuard → coordinator envd

5. NAT 穿透失败时
   → 使用 STUN 发现公网 endpoint
   → 更新链上 endpoint (PeerRegistry::update_endpoints)
   → 仍然失败 → TURN relay 兜底
```

### Tailscale 类比

| Tailscale | fractalmind-envd |
|-----------|-----------------|
| Coordination Server | **SUI 区块链** |
| Tailscale Account (SSO) | **SUI Keypair + AgentCertificate** |
| Coordination API | **SUI Events (PeerRegistered)** |
| WireGuard data plane | **WireGuard data plane** (相同) |
| DERP Relay | **TURN Relay** (相同模式) |
| Tailscale ACL | **链上 Organization 成员 + DAO** |
| Tailscale Client | **envd** |

---

## SUI 合约设计

### 设计决策

**Q1: 新 package 还是扩展现有 protocol?**
→ **新 package `fractalmind_envd`**，依赖 `fractalmind_protocol`。
理由: envd 是独立组件, 不应膨胀核心协议。现有 protocol 已部署到 testnet, 加 module 需要 upgrade。

**Q2: Shared object 还是 owned objects?**
→ **PeerRegistry 是 shared object** (全局注册表, 多节点读写)。
节点信息存在 Table 里, 不需要单独的 owned object。

**Q3: 什么上链, 什么不上链?**

| 上链 (需要信任/可审计) | 不上链 (高频/低价值) |
|----------------------|---------------------|
| Peer 注册 (WG pubkey + endpoint) | 心跳 (P2P 直传) |
| Endpoint 变更 (IP 漂移) | 实时 Agent 状态 |
| 节点上下线 | 指令/响应 |
| 重大故障记录 | 日志内容 |

**Q4: Peer 发现机制?**
→ **Event-driven** (类似 Tailscale 的 Coordination API):
- 注册时发出 `PeerRegistered` 事件, 包含完整连接信息
- envd 启动时查询历史事件, 运行时订阅新事件
- SUI Table 不支持链下迭代, 但 Events 天然支持过滤查询

### 合约模块: `peer.move`

```move
/// fractalmind-envd — Peer Registry
/// 管理 envd 节点的 WireGuard 公钥和 endpoint 注册,
/// 实现基于 SUI Events 的去中心化 peer 发现。
module fractalmind_envd::peer {
    use sui::object::{Self, ID, UID};
    use sui::tx_context::{Self, TxContext};
    use sui::transfer;
    use sui::table::{Self, Table};
    use sui::event;
    use std::string::String;
    use std::vector;

    // 跨 package 导入 — 复用现有协议的身份和组织
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

    /// 全局 Peer 注册表 (shared object)。
    /// 每个 fractalmind-envd 部署创建一个。
    public struct PeerRegistry has key {
        id: UID,
        /// node_address → PeerNode
        peers: Table<address, PeerNode>,
        /// 节点总数
        peer_count: u64,
    }

    /// 单个 envd 节点的网络信息。
    /// 存储在 PeerRegistry.peers Table 中。
    public struct PeerNode has store, drop {
        /// 节点所属组织
        org_id: ID,
        /// 关联的 AgentCertificate ID
        cert_id: ID,
        /// WireGuard 公钥 (32 bytes, Curve25519)
        wireguard_pubkey: vector<u8>,
        /// 网络 endpoints ["1.2.3.4:51820", "10.0.0.1:51820"]
        endpoints: vector<String>,
        /// 主机名 (便于识别)
        hostname: String,
        /// 在线/离线
        status: u8,
        /// 注册时间 (epoch ms)
        registered_at: u64,
        /// 最后更新时间 (epoch ms)
        last_updated: u64,
    }

    // ===== Events =====
    // envd 节点通过订阅这些事件来发现 peer

    /// 新节点注册 — 包含建立 WireGuard tunnel 所需的全部信息
    public struct PeerRegistered has copy, drop {
        peer: address,
        org_id: ID,
        wireguard_pubkey: vector<u8>,
        endpoints: vector<String>,
        hostname: String,
    }

    /// 节点 endpoint 变更 (IP 漂移, 端口变化)
    public struct PeerEndpointUpdated has copy, drop {
        peer: address,
        org_id: ID,
        new_endpoints: vector<String>,
    }

    /// 节点状态变更 (上线/下线)
    public struct PeerStatusChanged has copy, drop {
        peer: address,
        org_id: ID,
        new_status: u8,
    }

    /// 节点注销
    public struct PeerDeregistered has copy, drop {
        peer: address,
        org_id: ID,
    }

    // ===== Init =====

    /// 创建并共享 PeerRegistry。发布时自动调用。
    fun init(ctx: &mut TxContext) {
        let registry = PeerRegistry {
            id: object::new(ctx),
            peers: table::new(ctx),
            peer_count: 0,
        };
        transfer::share_object(registry);
    }

    // ===== Public Functions =====

    /// 注册一个 envd 节点。
    /// 要求: 调用者持有 active 的 AgentCertificate, 是该 Organization 的成员。
    public entry fun register_peer(
        registry: &mut PeerRegistry,
        org: &Organization,
        cert: &AgentCertificate,
        wireguard_pubkey: vector<u8>,
        endpoints: vector<String>,
        hostname: String,
        ctx: &mut TxContext,
    ) {
        let sender = tx_context::sender(ctx);
        let org_id = organization::org_id(org);
        let now = tx_context::epoch_timestamp_ms(ctx);

        // 授权检查
        assert!(agent::cert_agent(cert) == sender, constants::e_unauthorized());
        assert!(agent::cert_status(cert) == constants::agent_status_active(), constants::e_agent_not_active());
        assert!(agent::cert_org_id(cert) == org_id, constants::e_not_member());
        assert!(organization::has_agent(org, sender), constants::e_not_member());

        // 参数校验
        assert!(vector::length(&wireguard_pubkey) == 32, E_INVALID_WIREGUARD_KEY);
        assert!(!vector::is_empty(&endpoints), E_NO_ENDPOINTS);
        assert!(!table::contains(&registry.peers, sender), E_PEER_ALREADY_REGISTERED);

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

        // 事件包含建立 WireGuard tunnel 所需的全部信息
        event::emit(PeerRegistered {
            peer: sender,
            org_id,
            wireguard_pubkey: node.wireguard_pubkey,
            endpoints: node.endpoints,
            hostname: node.hostname,
        });

        table::add(&mut registry.peers, sender, node);
        registry.peer_count = registry.peer_count + 1;
    }

    /// 更新 endpoint (IP 漂移、端口变化)。仅节点自己可调用。
    public entry fun update_endpoints(
        registry: &mut PeerRegistry,
        new_endpoints: vector<String>,
        ctx: &mut TxContext,
    ) {
        let sender = tx_context::sender(ctx);

        assert!(table::contains(&registry.peers, sender), E_PEER_NOT_FOUND);
        assert!(!vector::is_empty(&new_endpoints), E_NO_ENDPOINTS);

        let node = table::borrow_mut(&mut registry.peers, sender);
        node.endpoints = new_endpoints;
        node.last_updated = tx_context::epoch_timestamp_ms(ctx);

        event::emit(PeerEndpointUpdated {
            peer: sender,
            org_id: node.org_id,
            new_endpoints: node.endpoints,
        });
    }

    /// 标记下线 (优雅关闭)。仅节点自己可调用。
    public entry fun go_offline(
        registry: &mut PeerRegistry,
        ctx: &mut TxContext,
    ) {
        let sender = tx_context::sender(ctx);

        assert!(table::contains(&registry.peers, sender), E_PEER_NOT_FOUND);

        let node = table::borrow_mut(&mut registry.peers, sender);
        node.status = PEER_STATUS_OFFLINE;
        node.last_updated = tx_context::epoch_timestamp_ms(ctx);

        event::emit(PeerStatusChanged {
            peer: sender,
            org_id: node.org_id,
            new_status: PEER_STATUS_OFFLINE,
        });
    }

    /// 重新上线。仅节点自己可调用。
    public entry fun go_online(
        registry: &mut PeerRegistry,
        new_endpoints: vector<String>,
        ctx: &mut TxContext,
    ) {
        let sender = tx_context::sender(ctx);

        assert!(table::contains(&registry.peers, sender), E_PEER_NOT_FOUND);
        assert!(!vector::is_empty(&new_endpoints), E_NO_ENDPOINTS);

        let node = table::borrow_mut(&mut registry.peers, sender);
        node.status = PEER_STATUS_ONLINE;
        node.endpoints = new_endpoints;
        node.last_updated = tx_context::epoch_timestamp_ms(ctx);

        event::emit(PeerStatusChanged {
            peer: sender,
            org_id: node.org_id,
            new_status: PEER_STATUS_ONLINE,
        });

        // 也发 endpoint 更新，因为重新上线可能 IP 变了
        event::emit(PeerEndpointUpdated {
            peer: sender,
            org_id: node.org_id,
            new_endpoints: node.endpoints,
        });
    }

    /// 注销节点。仅节点自己或 org admin 可调用。
    public entry fun deregister_peer(
        registry: &mut PeerRegistry,
        peer_address: address,
        org: &Organization,
        ctx: &mut TxContext,
    ) {
        let sender = tx_context::sender(ctx);

        assert!(table::contains(&registry.peers, peer_address), E_PEER_NOT_FOUND);
        // 自己或 org admin
        assert!(
            sender == peer_address || organization::admin(org) == sender,
            E_NOT_PEER_OWNER,
        );

        let node = table::remove(&mut registry.peers, peer_address);
        registry.peer_count = registry.peer_count - 1;

        event::emit(PeerDeregistered {
            peer: peer_address,
            org_id: node.org_id,
        });
    }

    // ===== Query Functions =====

    public fun peer_count(registry: &PeerRegistry): u64 { registry.peer_count }

    public fun has_peer(registry: &PeerRegistry, addr: address): bool {
        table::contains(&registry.peers, addr)
    }

    public fun get_peer(registry: &PeerRegistry, addr: address): &PeerNode {
        table::borrow(&registry.peers, addr)
    }

    public fun peer_org_id(node: &PeerNode): ID { node.org_id }
    public fun peer_wireguard_pubkey(node: &PeerNode): vector<u8> { node.wireguard_pubkey }
    public fun peer_endpoints(node: &PeerNode): vector<String> { node.endpoints }
    public fun peer_hostname(node: &PeerNode): String { node.hostname }
    public fun peer_status(node: &PeerNode): u8 { node.status }
    public fun peer_is_online(node: &PeerNode): bool { node.status == PEER_STATUS_ONLINE }

    public fun peer_status_online(): u8 { PEER_STATUS_ONLINE }
    public fun peer_status_offline(): u8 { PEER_STATUS_OFFLINE }
}
```

### 合约与 envd 的交互流程

```
envd 启动:
  1. SUI RPC: queryEvents(PeerRegistered, filter: org_id) → 获取所有 peer
  2. 对每个 peer: 用 wireguard_pubkey + endpoints 建立 WG tunnel
  3. SUI TX: register_peer(cert, wg_pubkey, my_endpoints, hostname)
  4. SUI Subscribe: 监听新的 PeerRegistered/PeerEndpointUpdated 事件
     → 新节点上线时自动建立 WG tunnel

envd 运行中:
  5. IP 变化: SUI TX → update_endpoints(new_endpoints)
  6. 心跳: WireGuard P2P 直接发送 (不上链)
  7. 指令: WireGuard P2P 直接发送 (不上链)

envd 关闭:
  8. SUI TX: go_offline()
  9. 清理 WireGuard tunnels
```

### Move.toml

```toml
[package]
name = "fractalmind_envd"
version = "0.0.1"
edition = "2024.beta"

[dependencies.Sui]
git = "https://github.com/MystenLabs/sui.git"
subdir = "crates/sui-framework/packages/sui-framework"
rev = "framework/testnet"

[dependencies.fractalmind_protocol]
# 引用已部署的 protocol package
local = "../../fractalmind-protocol/contracts/protocol"

[addresses]
fractalmind_envd = "0x0"
fractalmind_protocol = "0x685d6fb6ed8b0e679bb467ea73111819ec6ff68b1466d24ca26b400095dcdf24"
```

---

## 技术选型结论

| 组件 | 选型 | 理由 |
|------|------|------|
| **envd daemon** | Go | 单二进制, 交叉编译, 内嵌 WireGuard |
| **控制平面** | SUI 区块链 | 去中心化 peer 发现, 身份, 权限 |
| **数据平面** | WireGuard P2P | 吸收 Tailscale 最大优势 |
| **NAT 穿透** | STUN + ICE | 无状态, 非信任点 |
| **Relay 兜底** | TURN (可选) | P2P 失败时中转, 无状态 |
| **Peer 发现** | SUI Events | PeerRegistered 事件驱动 |
| **认证** | SUI keypair Ed25519 | AgentCertificate 链上验证 |
| **Agent 发现** | tmux session 扫描 | 与现有部署方式一致 |
| **合约** | `fractalmind_envd::peer` | 独立 package, 依赖现有 protocol |

---

## Gas 成本分析

### SUI Gas 模型

SUI Gas 费用 = **RGP × CU + SP × SU - SR**

| 术语 | 含义 |
|------|------|
| RGP | Reference Gas Price (当前主网 ~551 MIST) |
| CU | Computation Units (执行消耗) |
| SP × SU | 存储价格 × 存储单元 (新建对象/扩大 Table) |
| SR | Storage Rebate (删除对象时退还) |

### 各操作 Gas 估算

| 操作 | CU 估计 | 存储 | 总 Gas (SUI) | 说明 |
|------|---------|------|-------------|------|
| `register_peer` | ~2,000 | ~1KB (new Table entry) | **~0.0017** | 首次注册, 含存储费 |
| `update_endpoints` | ~1,500 | 0 (修改现有) | **~0.0011** | IP 漂移更新, 无新存储 |
| `go_offline` | ~1,000 | 0 | **~0.0008** | 状态变更 |
| `go_online` | ~1,500 | 0 | **~0.0011** | 状态 + endpoint 更新 |
| `deregister_peer` | ~1,000 | 负 (删除) | **~0.0003** | 删除退还存储费 |

### 月度成本估算

**场景: 2 节点 (本机 + RoseX)**

| 操作 | 频率 | 次/月 | 单价 (SUI) | 月费 (SUI) |
|------|------|-------|-----------|-----------|
| register_peer | 启动时 | 30 (日重启) | 0.0017 | 0.051 |
| update_endpoints | IP 变化 | 10 | 0.0011 | 0.011 |
| go_offline/online | 每次重启 | 60 | 0.001 | 0.060 |
| **合计** | | | | **~0.122 SUI** |

> 按 SUI ≈ $1.15 计算: **~$0.14/月** (2 节点)

**场景: 100 节点 (中型团队)**

| 操作 | 频率 | 次/月 | 月费 (SUI) |
|------|------|-------|-----------|
| register_peer | 启动时 | 1,500 | 2.55 |
| update_endpoints | IP 变化 | 500 | 0.55 |
| go_offline/online | 重启 | 3,000 | 3.00 |
| **合计** | | | **~6.10 SUI ≈ $7.13/月** |

### 心跳不上链的验证

如果心跳 (30s 间隔) 也上链:

| 节点数 | 心跳/月 | Gas (SUI) | 月费 |
|--------|---------|-----------|------|
| 2 | 172,800 | ~138 | **~$159** |
| 100 | 8,640,000 | ~6,912 | **~$7,948** |

> ✅ 正确决策: 心跳通过 WireGuard P2P 直传, 仅注册/endpoint 变更/上下线上链。

### 竞品价格对比

| 产品 | 2 节点月费 | 100 节点月费 | 模式 |
|------|-----------|-------------|------|
| Tailscale Team | $12 | $600 | 中心化 SaaS |
| TeamViewer Business | $101.80 | N/A (per-user) | 中心化 SaaS |
| ToDesk 专业版 | ¥50 (~$7) | N/A | 中心化 SaaS |
| **fractalmind-envd** | **$0.14** | **$7.13** | **去中心化链上** |

> fractalmind-envd 比 Tailscale Team 便宜 **~85x**, 同时实现去中心化。

---

## Gas 代付机制 (Sponsored Transactions)

### 需求

管理员 (org admin) 统一为所有 envd 节点代付 Gas, 让 worker 节点无需持有 SUI token。

### SUI Sponsored Transaction 原理

SUI 原生支持 **Sponsored Transaction** (SIP-15):

```
普通交易:    sender 签名 + sender 付 gas
代付交易:    sender 签名 + sponsor 签名 + sponsor 付 gas
```

流程:
1. **Worker** 构造交易 (TransactionData), 但不设 gas budget/coin
2. **Worker** 签名交易 → 发给 **Sponsor Service**
3. **Sponsor** 验证交易合法性 → 添加 gas budget + gas coin → 签名
4. **双签名交易** 提交到 SUI 网络 → gas 从 sponsor 扣除

> 关键: Worker 无需持有任何 SUI token, 但仍然用自己的 keypair 签名 (身份不变)。

### 架构设计

```
envd (worker node)                    Gas Sponsor Service (admin 运营)
├── 构造 TX (register_peer等)         ├── 验证 TX 白名单 (仅允许 envd 合约调用)
├── 用自己的 keypair 签名              ├── 检查 sender 是否在 org 内
├── 发送 partial-signed TX ─────────► ├── 添加 gas coin + budget
│   (via HTTPS)                       ├── 用 sponsor keypair 签名
│                                     ├── 提交 dual-signed TX 到 SUI
└── 收到 TX digest 确认 ◄──────────── └── 返回结果
```

### 合约扩展: `sponsor.move`

```move
/// fractalmind-envd — Gas Sponsor Registry
/// 管理 org 级别的 Gas 代付策略。
module fractalmind_envd::sponsor {
    use sui::object::{Self, ID, UID};
    use sui::tx_context::{Self, TxContext};
    use sui::transfer;
    use sui::table::{Self, Table};
    use sui::event;
    use sui::coin::{Self, Coin};
    use sui::sui::SUI;

    use fractalmind_protocol::organization::Organization;
    use fractalmind_protocol::organization;

    // ===== Error Codes (8100) =====

    const E_NOT_ADMIN: u64 = 8101;
    const E_SPONSOR_EXISTS: u64 = 8102;
    const E_SPONSOR_NOT_FOUND: u64 = 8103;
    const E_INSUFFICIENT_BALANCE: u64 = 8104;
    const E_DAILY_LIMIT_EXCEEDED: u64 = 8105;

    // ===== Structs =====

    /// 组织级 Gas 代付配置 (shared object)
    public struct SponsorRegistry has key {
        id: UID,
        /// org_id → SponsorConfig
        sponsors: Table<ID, SponsorConfig>,
    }

    /// 单个组织的代付策略
    public struct SponsorConfig has store {
        /// 代付管理员 (通常是 org admin)
        admin: address,
        /// 是否启用
        enabled: bool,
        /// 每笔交易最大 gas budget (MIST)
        max_gas_per_tx: u64,
        /// 每日 gas 上限 (MIST)
        daily_gas_limit: u64,
        /// 当日已用 gas (MIST), 每日重置
        daily_gas_used: u64,
        /// 上次重置日期 (epoch)
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

    /// Org admin 启用 Gas 代付
    public entry fun enable_sponsor(
        registry: &mut SponsorRegistry,
        org: &Organization,
        max_gas_per_tx: u64,
        daily_gas_limit: u64,
        ctx: &mut TxContext,
    ) {
        let sender = tx_context::sender(ctx);
        let org_id = organization::org_id(org);

        // 仅 org admin 可启用
        assert!(organization::admin(org) == sender, E_NOT_ADMIN);
        assert!(!table::contains(&registry.sponsors, org_id), E_SPONSOR_EXISTS);

        let config = SponsorConfig {
            admin: sender,
            enabled: true,
            max_gas_per_tx,
            daily_gas_limit,
            daily_gas_used: 0,
            last_reset_epoch: tx_context::epoch(ctx),
        };

        table::add(&mut registry.sponsors, org_id, config);

        event::emit(SponsorEnabled {
            org_id,
            admin: sender,
            max_gas_per_tx,
            daily_gas_limit,
        });
    }

    /// 查询 org 的代付配置
    public fun get_sponsor(registry: &SponsorRegistry, org_id: ID): &SponsorConfig {
        table::borrow(&registry.sponsors, org_id)
    }

    public fun is_enabled(config: &SponsorConfig): bool { config.enabled }
    public fun sponsor_admin(config: &SponsorConfig): address { config.admin }
}
```

> **注意**: 链上合约仅管理代付**策略** (限额、白名单)。实际的 Gas 代付通过 SUI 原生 Sponsored Transaction 机制在**链下**完成, 不需要合约持有 SUI coin。

### Gas Sponsor Service (链下)

轻量 HTTP 服务, admin 部署:

```
POST /sponsor
Body: { partial_signed_tx: base64 }

验证流程:
1. 解码 TransactionData
2. 检查: 目标合约 == fractalmind_envd (白名单)
3. 检查: sender 在 org 内 (查链上 Organization)
4. 检查: 链上 SponsorConfig.enabled == true
5. 检查: gas budget ≤ max_gas_per_tx
6. 检查: daily_gas_used + budget ≤ daily_gas_limit
7. 添加 sponsor gas coin + budget
8. Sponsor keypair 签名
9. 提交 dual-signed TX 到 SUI
10. 返回 { tx_digest }
```

### envd 集成

`sentinel.yaml` 增加 sponsor 配置:

```yaml
sui:
  rpc: https://fullnode.testnet.sui.io:443
  keypair_path: ~/.sui/envd.key
  org_id: "0x..."
  sponsor:
    enabled: true
    url: https://sponsor.fractalmind.ai/sponsor  # Gas Sponsor Service
    # 如果 enabled=false, envd 自己付 gas (需持有 SUI)
```

envd 交易发送流程:

```
if sponsor.enabled:
    1. 构造 TX (不含 gas)
    2. 用自己 keypair 签名
    3. POST /sponsor → 获取 tx_digest
else:
    1. 构造 TX (含 gas coin)
    2. 签名 + 提交
```

### 成本对比

| 模式 | Worker 需持有 SUI | Admin 管理 | 适用场景 |
|------|-------------------|-----------|---------|
| **自付 (默认)** | 是 (~0.1 SUI/月) | 无 | 少量节点, 技术用户 |
| **代付 (sponsor)** | 否 | 统一充值 + 限额 | 企业部署, 多节点 |

> 企业场景: Admin 充值 10 SUI 到 sponsor wallet → 够 100 节点运行 ~1.5 个月。

---

## Roadmap

### Phase 1: 调研 + 设计 ✅ COMPLETE

> 目标: 明确架构方向, 完成技术选型

- ✅ 竞品调研 (花生壳, ToDesk, TeamViewer, Tailscale)
- ✅ 架构设计: SUI 链上控制面 + WireGuard P2P 数据面
- ✅ SUI 合约设计 (`peer.move` + `sponsor.move`)
- ✅ Gas 成本评估 + 代付机制设计
- ✅ 吸收竞品优势: WireGuard P2P, E2E 加密, STUN/TURN 穿透, 无状态 Relay

### Phase 2: SUI 合约部署 + 验证

> 目标: 链上基础设施就绪

- 部署 `fractalmind_envd::peer` 合约到 SUI Testnet
- 部署 `fractalmind_envd::sponsor` 合约到 SUI Testnet
- SDK 验证: register_peer → update_endpoints → go_offline/online → deregister
- Event 查询验证: queryEvents(PeerRegistered, filter: org_id)
- Gas 代付验证: Sponsored Transaction 端到端测试

### Phase 3: envd v2 核心 (WireGuard + SUI)

> 目标: envd 从 WebSocket 架构升级为 WireGuard P2P + SUI 链上

- envd 集成 SUI Client (Go `sui-go-sdk`)
  - 启动: 读 org_id → 查询 PeerRegistered 事件 → 获取 peer 列表
  - 注册: register_peer(cert, wg_pubkey, endpoints)
  - 运行时: 订阅 SUI Events (实时发现新节点)
  - 关闭: go_offline()
- envd 集成 WireGuard (Go `wireguard-go` 或 `wgctrl`)
  - 生成/加载 WireGuard keypair
  - 动态添加/移除 peer (基于 SUI Events)
  - P2P tunnel 建立 + 心跳
- STUN Client: NAT endpoint 发现 (公网 IP:port)
- Sponsored TX 集成 (可选): 向 Gas Sponsor Service 提交 partial-signed TX

### Phase 4: Relay + 穿透保障

> 目标: 处理 NAT 穿透失败场景, 保证 100% 连通

- TURN Relay 服务 (无状态, 仅转发加密 WireGuard 包)
- 连接策略: WireGuard P2P → STUN 重试 → TURN Relay 兜底
- endpoint 自动更新: IP 漂移检测 → update_endpoints 上链
- Gas Sponsor Service (轻量 HTTP, 可选, 企业部署)

### Phase 5: 双机部署 + 故障自愈

> 目标: 生产验证

- envd 安装到本机 + RoseX 机器
- WireGuard P2P 隧道验证 (跨 NAT)
- 远程 Agent 管理: status, restart, logs, kill
- 故障自愈: Agent 崩溃 → envd 自动重启 (≤60s, max 3 次) → 链上状态更新
- 告警: 恢复失败 → Slack/TG 通知
- agent-manager 集成: 本地 + 远程 Agent 统一管理

### Phase 6: 生产就绪

> 目标: 主网部署, 长期稳定运行

- SUI 合约部署到 Mainnet
- envd 二进制分发 (GitHub Releases, 多架构)
- 安装脚本: `curl -sSL install.sh | sh`
- 监控 Dashboard: 节点状态, P2P 连通率, gas 消耗
- 文档: 安装指南, 配置参考, 故障排查
