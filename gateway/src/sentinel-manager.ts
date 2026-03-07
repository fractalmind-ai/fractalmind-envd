import WebSocket, { WebSocketServer } from 'ws';
import { v4 as uuid } from 'uuid';
import {
  SentinelInfo,
  WsMessage,
  HeartbeatPayload,
  CommandRequest,
  CommandResult,
  AlertPayload,
} from './types';

export class SentinelManager {
  private sentinels: Map<string, SentinelInfo> = new Map();
  private pendingCommands: Map<string, {
    resolve: (result: Record<string, unknown>) => void;
    timer: NodeJS.Timeout;
  }> = new Map();

  private commandTimeout = 30_000; // 30s

  getSentinels(): SentinelInfo[] {
    return Array.from(this.sentinels.values());
  }

  getSentinel(id: string): SentinelInfo | undefined {
    return this.sentinels.get(id);
  }

  findByHostname(hostname: string): SentinelInfo | undefined {
    for (const s of this.sentinels.values()) {
      if (s.hostname === hostname) return s;
    }
    return undefined;
  }

  handleConnection(ws: WebSocket): void {
    const tempId = `temp-${uuid().slice(0, 8)}`;
    console.log(`[ws] new connection: ${tempId}`);

    let sentinelId = tempId;

    ws.on('message', (data: WebSocket.Data) => {
      try {
        const msg: WsMessage = JSON.parse(data.toString());
        this.handleMessage(sentinelId, ws, msg);

        // Update sentinelId after registration
        if (msg.type === 'register') {
          const payload = msg.payload as { host_id: string; hostname: string; version: string };
          const newId = payload.host_id || payload.hostname || tempId;
          if (newId !== sentinelId) {
            this.sentinels.delete(sentinelId);
            sentinelId = newId;
          }
        }
      } catch (err) {
        console.error(`[ws] invalid message from ${sentinelId}:`, err);
      }
    });

    ws.on('close', () => {
      console.log(`[ws] disconnected: ${sentinelId}`);
      this.sentinels.delete(sentinelId);
    });

    ws.on('error', (err) => {
      console.error(`[ws] error from ${sentinelId}:`, err.message);
    });
  }

  private handleMessage(sentinelId: string, ws: WebSocket, msg: WsMessage): void {
    switch (msg.type) {
      case 'register': {
        const payload = msg.payload as { host_id: string; hostname: string; version: string };
        const id = payload.host_id || payload.hostname || sentinelId;

        const sentinel: SentinelInfo = {
          id,
          hostId: payload.host_id,
          hostname: payload.hostname,
          version: payload.version,
          ws,
          connectedAt: new Date(),
          lastHeartbeat: null,
          agents: [],
          system: null,
          uptimeSeconds: 0,
        };

        this.sentinels.set(id, sentinel);
        console.log(`[sentinel] registered: ${id} (${payload.hostname}, v${payload.version})`);
        break;
      }

      case 'heartbeat': {
        const payload = msg.payload as HeartbeatPayload;
        const sentinel = this.sentinels.get(sentinelId);
        if (sentinel) {
          sentinel.lastHeartbeat = new Date();
          sentinel.agents = payload.agents || [];
          sentinel.system = payload.system || null;
          sentinel.uptimeSeconds = payload.uptime_seconds || 0;
        }
        break;
      }

      case 'command_result': {
        const payload = msg.payload as CommandResult;
        const pending = this.pendingCommands.get(payload.request_id);
        if (pending) {
          clearTimeout(pending.timer);
          pending.resolve(payload.result);
          this.pendingCommands.delete(payload.request_id);
        }
        break;
      }

      case 'alert': {
        const payload = msg.payload as AlertPayload;
        console.warn(`[alert] ${sentinelId}: ${payload.type} — ${payload.message}`);
        // TODO: Forward to Slack/TG via fractalbot
        break;
      }

      case 'pong':
        break;

      default:
        console.log(`[ws] unknown message type from ${sentinelId}: ${msg.type}`);
    }
  }

  async sendCommand(sentinelId: string, command: string, agentId?: string, args?: string): Promise<Record<string, unknown>> {
    const sentinel = this.sentinels.get(sentinelId);
    if (!sentinel) {
      throw new Error(`sentinel ${sentinelId} not found`);
    }

    if (sentinel.ws.readyState !== WebSocket.OPEN) {
      throw new Error(`sentinel ${sentinelId} not connected`);
    }

    const requestId = uuid();
    const cmd: CommandRequest = {
      command,
      agent_id: agentId,
      args,
      request_id: requestId,
    };

    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pendingCommands.delete(requestId);
        reject(new Error(`command timed out after ${this.commandTimeout}ms`));
      }, this.commandTimeout);

      this.pendingCommands.set(requestId, { resolve, timer });

      const msg: WsMessage = { type: 'command', payload: cmd };
      sentinel.ws.send(JSON.stringify(msg));
    });
  }
}
