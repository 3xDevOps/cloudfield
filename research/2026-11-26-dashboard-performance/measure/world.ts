// Starts the measured world: a real server and a real `cloudfield gui`
// gateway from dist/, four workspaces, and the seeded store. Prints one JSON
// line ({url, addr, token, dataDir}) to the file given as argv[2] and stays up
// until killed.
//
//   make build
//   cd web && bun ../research/2026-11-26-dashboard-performance/measure/world.ts /tmp/perf-world.json

import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { GatewayClient } from '../../../web/e2e/harness/client'
import { startGateway } from '../../../web/e2e/harness/gateway'
import { seedRepo } from '../../../web/e2e/harness/git'
import { repoRoot } from '../../../web/e2e/harness/paths'
import { startServer } from '../../../web/e2e/harness/server'
import { seedWorkspace } from '../../../web/e2e/harness/setup'

const out = process.argv[2]
if (!out) throw new Error('usage: world.ts <state-file> [seed flags...]')
const dir = mkdtempSync(path.join(tmpdir(), 'cf-perf-'))
const server = await startServer(dir)
const gateway = await startGateway(dir, 'admin')
const api = new GatewayClient(gateway.addr, gateway.token)
const admin = { name: 'admin', api, url: gateway.url, home: gateway.home } as never
// Named so the busy workspace sorts first, which is the one the seed fills most.
for (const name of ['a-busy', 'b-shop', 'c-docs', 'd-tools']) {
  await seedWorkspace(admin, server.addr, await seedRepo(path.join(dir, 'repos'), name), name)
}
await server.stop()
const seeded = execFileSync(
  'go',
  ['run', '-tags', 'perfseed', './research/2026-11-26-dashboard-performance/seed', '--data-dir', server.dataDir, ...process.argv.slice(3)],
  { cwd: repoRoot, encoding: 'utf8' },
)
process.stdout.write(seeded)
await server.restart()
writeFileSync(out, JSON.stringify({ url: gateway.url, addr: gateway.addr, token: gateway.token, dataDir: server.dataDir, server: server.addr }) + '\n')
console.log('world ready', gateway.url)
process.on('SIGTERM', async () => {
  await gateway.stop()
  await server.stop()
  process.exit(0)
})
setInterval(() => {}, 1 << 30)
