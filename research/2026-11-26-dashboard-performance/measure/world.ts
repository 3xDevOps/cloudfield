// Starts the measured world: a real server and a real `cloudfield gui`
// gateway from dist/, four workspaces, and the seeded store. Prints one JSON
// line ({url, addr, token, dataDir}) to the file given as argv[2] and stays up
// until killed.
//
//   make build
//   cd web && bun ../research/2026-11-26-dashboard-performance/measure/world.ts /tmp/perf-world.json
//
// PERF_LINK_DELAY_MS=75 puts a TCP relay between the gateway and the server
// that holds every chunk for 75 ms in each direction, so one round trip
// between them costs 150 ms, as a distant relay would. PERF_CLI names another
// `aether` binary for the gateway and PERF_SERVER another `aether-server`, to
// compare two builds on the same world.

import { execFileSync } from 'node:child_process'
import { copyFileSync, mkdtempSync, writeFileSync } from 'node:fs'
import net from 'node:net'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { GatewayClient } from '../../../web/e2e/harness/client'
import { startGateway } from '../../../web/e2e/harness/gateway'
import { seedRepo } from '../../../web/e2e/harness/git'
import { binaries, repoRoot } from '../../../web/e2e/harness/paths'
import { startServer } from '../../../web/e2e/harness/server'
import { seedWorkspace } from '../../../web/e2e/harness/setup'

const out = process.argv[2]
if (!out) throw new Error('usage: world.ts <state-file> [seed flags...]')
if (process.env.PERF_CLI) copyFileSync(process.env.PERF_CLI, binaries().cli)
if (process.env.PERF_SERVER) copyFileSync(process.env.PERF_SERVER, binaries().server)
const dir = mkdtempSync(path.join(tmpdir(), 'cf-perf-'))
const server = await startServer(dir)
const delay = Number(process.env.PERF_LINK_DELAY_MS ?? 0)
let link = server.addr
if (delay) {
  const [host, port] = server.addr.split(':')
  const relay = net.createServer((near) => {
    const far = net.connect(Number(port), host)
    // Equal timeouts fire in the order they were set, so chunks keep their order.
    const pipe = (from: net.Socket, to: net.Socket) => {
      from.on('data', (chunk) => setTimeout(() => to.write(chunk), delay))
      from.on('close', () => setTimeout(() => to.destroy(), delay))
      from.on('error', () => to.destroy())
    }
    pipe(near, far)
    pipe(far, near)
  })
  await new Promise<void>((resolve) => relay.listen(0, '127.0.0.1', resolve))
  link = `127.0.0.1:${(relay.address() as net.AddressInfo).port}`
}

const gateway = await startGateway(dir, 'admin')
const api = new GatewayClient(gateway.addr, gateway.token)
const admin = { name: 'admin', api, url: gateway.url, home: gateway.home } as never
// Named so the busy workspace sorts first, which is the one the seed fills most.
for (const name of ['a-busy', 'b-shop', 'c-docs', 'd-tools']) {
  await seedWorkspace(admin, link, await seedRepo(path.join(dir, 'repos'), name), name)
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
