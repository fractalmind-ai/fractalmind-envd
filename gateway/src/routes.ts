import express from 'express';
import { SentinelManager } from './sentinel-manager';

export function createRouter(manager: SentinelManager): express.Router {
  const router = express.Router();

  // GET /sentinels — list all connected sentinels
  router.get('/sentinels', (_req, res) => {
    const sentinels = manager.getSentinels().map(s => ({
      id: s.id,
      host_id: s.hostId,
      hostname: s.hostname,
      version: s.version,
      connected_at: s.connectedAt,
      last_heartbeat: s.lastHeartbeat,
      agent_count: s.agents.length,
      uptime_seconds: s.uptimeSeconds,
      system: s.system,
    }));
    res.json({ sentinels, count: sentinels.length });
  });

  // GET /sentinels/:id — get sentinel details
  router.get('/sentinels/:id', (req, res) => {
    const sentinel = manager.getSentinel(req.params.id) || manager.findByHostname(req.params.id);
    if (!sentinel) {
      res.status(404).json({ error: `sentinel ${req.params.id} not found` });
      return;
    }

    res.json({
      id: sentinel.id,
      host_id: sentinel.hostId,
      hostname: sentinel.hostname,
      version: sentinel.version,
      connected_at: sentinel.connectedAt,
      last_heartbeat: sentinel.lastHeartbeat,
      agents: sentinel.agents,
      system: sentinel.system,
      uptime_seconds: sentinel.uptimeSeconds,
    });
  });

  // GET /sentinels/:id/agents — list agents on a sentinel
  router.get('/sentinels/:id/agents', (req, res) => {
    const sentinel = manager.getSentinel(req.params.id) || manager.findByHostname(req.params.id);
    if (!sentinel) {
      res.status(404).json({ error: `sentinel ${req.params.id} not found` });
      return;
    }

    res.json({ agents: sentinel.agents, count: sentinel.agents.length });
  });

  // POST /sentinels/:id/command — send command to sentinel
  router.post('/sentinels/:id/command', async (req, res) => {
    const sentinel = manager.getSentinel(req.params.id) || manager.findByHostname(req.params.id);
    if (!sentinel) {
      res.status(404).json({ error: `sentinel ${req.params.id} not found` });
      return;
    }

    const { command, agent_id, args } = req.body;
    if (!command) {
      res.status(400).json({ error: 'command is required' });
      return;
    }

    try {
      const result = await manager.sendCommand(sentinel.id, command, agent_id, args);
      res.json(result);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err);
      res.status(500).json({ error: message });
    }
  });

  // GET /health — health check
  router.get('/health', (_req, res) => {
    const sentinels = manager.getSentinels();
    res.json({
      status: 'ok',
      sentinels: sentinels.length,
      total_agents: sentinels.reduce((sum, s) => sum + s.agents.length, 0),
    });
  });

  return router;
}
