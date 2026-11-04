#!/usr/bin/env node
// Prints a free TCP port on the given host (default 127.0.0.1). `make e2e` gives every run its
// own port so parallel worktrees never attach to each other's servers (E2E_PORT forces one).
import { createServer } from 'node:net';

const host = process.argv[2] ?? '127.0.0.1';
const server = createServer();
server.once('error', (error) => {
  console.error(`free-port: ${error.message}`);
  process.exit(1);
});
server.listen(0, host, () => {
  const address = server.address();
  const port = typeof address === 'object' && address ? address.port : 0;
  server.close(() => {
    console.log(String(port));
  });
});
