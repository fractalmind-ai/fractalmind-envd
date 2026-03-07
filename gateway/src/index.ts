import http from 'http';
import express from 'express';
import { WebSocketServer } from 'ws';
import { SentinelManager } from './sentinel-manager';
import { createRouter } from './routes';

const PORT = parseInt(process.env.PORT || '8080', 10);

const app = express();
app.use(express.json());

const manager = new SentinelManager();
app.use('/api', createRouter(manager));

const server = http.createServer(app);

// WebSocket server on /ws path
const wss = new WebSocketServer({ server, path: '/ws' });

wss.on('connection', (ws) => {
  manager.handleConnection(ws);
});

// Ping connected sentinels every 30s to keep connections alive
setInterval(() => {
  wss.clients.forEach((ws) => {
    if (ws.readyState === ws.OPEN) {
      ws.send(JSON.stringify({ type: 'ping', payload: null }));
    }
  });
}, 30_000);

server.listen(PORT, () => {
  console.log(`[gateway] listening on :${PORT}`);
  console.log(`[gateway] WebSocket: ws://localhost:${PORT}/ws`);
  console.log(`[gateway] REST API:  http://localhost:${PORT}/api`);
});
