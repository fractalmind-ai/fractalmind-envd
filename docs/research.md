# 竞品调研: 远程控制/穿透产品对比

> fractalmind-envd KR1 | 2026-03-07 | Owner: OpenClaw

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
| **协议** | HTTP/HTTPS/TCP 端口映射 |
| **费用** | 免费 1Mbps + 1 映射；付费 ¥6-168/年 |
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
| **协议** | 自研远程桌面协议 (画面 + 键鼠 + 文件传输) |
| **费用** | 免费个人版；专业版 ¥8-25/月 |
| **优势** | 画面流畅（4K 60fps），跨平台，文件传输 |
| **劣势** | 面向 GUI 桌面控制，非为 headless 服务器/CLI 设计 |

### 3. Tailscale

**产品**: 基于 WireGuard 的 Mesh VPN，零配置组网。

| 维度 | 详情 |
|------|------|
| **架构** | 半中心化。控制平面 (Coordination server) 中心化，数据平面 P2P (WireGuard) |
| **穿透方式** | WireGuard + ICE + DERP relay (Tailscale 自建) |
| **认证** | SSO (Google/MS/GitHub) → Tailscale Coordination → 分发 WireGuard 公钥 |
| **信任根** | Tailscale Coordination Server。管理节点加入/退出、ACL、密钥轮换 |
| **协议** | WireGuard (L3 VPN)，分配 100.x.x.x 内部 IP |
| **费用** | 免费 100 设备；Team $6/user/月 |
| **优势** | 零配置、WireGuard 加密、MagicDNS、ACL、开源 (Headscale) |
| **劣势** | 控制平面仍是中心化信任，数据平面加密但 key 由中心管理 |

**备注**: Headscale 是 Tailscale 控制平面的开源替代，可自托管。但仍需运维自己的服务器。

---

## 架构对比矩阵

| 特征 | 花生壳 | ToDesk | Tailscale | **fractalmind-envd** |
|------|--------|--------|-----------|---------------------|
| **信任根** | Oray 服务器 | ToDesk 服务器 | Tailscale Coord | **SUI 区块链** |
| **身份认证** | 账号密码 | 设备码+密码 | SSO + WG keys | **SUI keypair 签名** |
| **控制平面** | 中心化 | 中心化 | 中心化 | **链上 (去中心化)** |
| **数据平面** | 中心转发 | P2P/relay | WireGuard P2P | **反向 WebSocket** |
| **审计性** | 不透明 | 不透明 | ACL 日志 | **链上全记录** |
| **故障恢复** | 手动 | 手动 | 手动 | **自动 (≤60s)** |
| **可编程** | SDK 有限 | 无 | API 完善 | **Move 合约可编程** |
| **治理** | 厂商决策 | 厂商决策 | ACL 规则 | **DAO 链上投票** |
| **单点故障** | Oray 宕机全挂 | ToDesk 宕机全挂 | Coord 宕机新连接挂 | **无 (链不停)** |
| **目标场景** | 内网穿透 | 远程桌面 | Mesh VPN | **AI Agent 远程管理** |

---

## 共同弱点: 中心化信任根

所有三款产品都依赖**中心化服务器**作为信任根:

1. **单点故障**: 中心服务器宕机 → 无法建立新连接
2. **审查风险**: 平台可随时封禁账号/设备
3. **数据透明度**: 无法独立验证控制行为的历史记录
4. **治理不透明**: 功能变更、定价调整完全由厂商单方面决定
5. **身份不可移植**: 身份和数据绑定在特定平台

---

## fractalmind-envd 差异化

### 核心差异: SUI 区块链替代中央服务

```
传统产品:
  设备 → [中央服务器] → 设备
        身份管理在这里
        授权决策在这里
        如果服务器挂了全完

fractalmind-envd:
  envd → [SUI 区块链] ← 身份 (AgentCertificate)
       ↕              ← 授权 (capability_tags)
  Gateway (薄中转)    ← 审计 (链上全记录)
       ↕              ← 治理 (DAO 提案)
  envd               ← 无单点故障
```

### 五大差异化优势

| # | 维度 | 传统产品 | fractalmind-envd |
|---|------|----------|------------------|
| 1 | **身份** | 平台账号 (可封禁) | SUI AgentCertificate (链上不可封) |
| 2 | **认证** | 账号密码/设备码 | SUI keypair 签名 (密码学保证) |
| 3 | **审计** | 平台内部日志 | 链上全记录 (任何人可验证) |
| 4 | **治理** | 厂商决策 | DAO 投票 (代码执行提案) |
| 5 | **协调** | 中心服务器 | Gateway 只做转发, 信任在链上 |

### Gateway 为什么不是单点故障?

传统产品的中心服务器承担三个角色：身份认证 + 授权决策 + 数据转发。fractalmind-envd 将前两个移到链上：

- **身份**: AgentCertificate 在 SUI 链上，不依赖 Gateway
- **授权**: capability_tags 和 reputation_score 在链上
- **Gateway 只做数据转发**: 如果 Gateway 挂了，身份和授权不丢失。重启 Gateway 或切换到备份 Gateway，envd 自动重连

---

## 网络穿透方案选型

### 方案对比

| 方案 | NAT 穿透率 | 延迟 | 复杂度 | 适合 MVP |
|------|-----------|------|--------|----------|
| **反向 WebSocket** | 100% (出站连接) | 低 | 低 | ✅ 推荐 |
| WireGuard mesh | 95% (ICE) | 极低 | 中 | v2 考虑 |
| TURN relay | 100% | 中 | 中 | 备选 |
| libp2p | 90% | 中 | 高 | 不适合 MVP |

### MVP 推荐: 反向 WebSocket

理由:
1. **100% NAT 穿透**: envd 主动向外连接 Gateway，不需要公网 IP 或端口映射
2. **低复杂度**: Go 标准库 `gorilla/websocket` 即可实现
3. **双向通信**: WebSocket 建立后可双向发送消息
4. **自动重连**: 断线重连逻辑简单
5. **与设计文档一致**: `agent-sentinel-design.md` 已选定反向 WebSocket

### v2 路线: WireGuard mesh

当需要高带宽 Agent 间通信时（如文件传输、模型权重同步），可叠加 WireGuard:
- 使用 SUI 链上注册的公钥建立 WireGuard tunnel
- 数据平面走 WireGuard P2P，控制平面仍走 WebSocket
- 参考 Tailscale/Headscale 架构但用链上替代 Coord server

---

## MVP 架构

```
┌─────────────────┐
│   SUI Testnet    │  身份 + 授权 + 审计
│  AgentCertificate│
└────────┬────────┘
         │ 查询/上报
┌────────┴────────┐
│    Gateway       │  WebSocket server + REST API
│  (TypeScript)    │  链上事件监听 + 状态汇总
└────────┬────────┘
         │ 反向 WebSocket (wss://)
    ┌────┴────┐
    │         │
┌───┴──┐  ┌──┴───┐
│ envd │  │ envd │  Go daemon
│host-A│  │host-B│  心跳 + Agent 发现 + 自愈
└──┬───┘  └──┬───┘
   │         │
┌──┴──┐   ┌──┴──┐
│tmux │   │tmux │  AI Agent 进程
│sess.│   │sess.│
└─────┘   └─────┘
```

---

## 技术选型结论

| 组件 | 选型 | 理由 |
|------|------|------|
| **envd daemon** | Go | 单二进制分发, 交叉编译, 系统级操作 |
| **网络穿透** | 反向 WebSocket (MVP) | 100% 穿透, 低复杂度, 双向通信 |
| **认证** | SUI keypair Ed25519 签名 | 复用链上身份, 无需额外账号 |
| **Agent 发现** | tmux session 扫描 | 与现有部署方式一致 |
| **配置** | YAML (`sentinel.yaml`) | 人类可读, 易编辑 |
| **v2 数据平面** | WireGuard mesh | 高带宽 P2P, 链上密钥管理 |

---

## 行动项

1. ✅ 竞品调研完成
2. → KR2: 创建 Go daemon 项目骨架, 实现 WebSocket 反向连接 + 心跳
3. → KR3: Gateway Service (TypeScript)
4. → 安装 envd 到 RoseX 机器进行实测
