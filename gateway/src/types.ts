import { WebSocket } from 'ws';

export interface SentinelInfo {
  id: string;
  hostId: string;
  hostname: string;
  version: string;
  ws: WebSocket;
  connectedAt: Date;
  lastHeartbeat: Date | null;
  agents: AgentInfo[];
  system: SystemInfo | null;
  uptimeSeconds: number;
}

export interface AgentInfo {
  id: string;
  session: string;
  status: string;
  started_at?: string;
}

export interface SystemInfo {
  os: string;
  arch: string;
  num_cpu: number;
}

export interface HeartbeatPayload {
  host_id: string;
  hostname: string;
  timestamp: string;
  agents: AgentInfo[];
  system: SystemInfo;
  uptime_seconds: number;
}

export interface WsMessage {
  type: string;
  payload: unknown;
}

export interface CommandRequest {
  command: string;
  agent_id?: string;
  args?: string;
  request_id: string;
}

export interface CommandResult {
  request_id: string;
  result: Record<string, unknown>;
}

export interface AlertPayload {
  type: string;
  agent: string;
  session: string;
  message: string;
}
