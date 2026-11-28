import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import type { ServerCapacity } from '@/lib/types'
import { MachinesSection } from '@/routes/settings/machines'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, run, serverInfo } from '@/test/fixtures'

const gib = 2 ** 30
const refusalText = 'scheduler: insufficient host memory headroom: 1288490188 available bytes; reserve 1717986918 bytes plus 536870912 bytes for this start and 0 bytes for starts in flight'

function full(over: Partial<ServerCapacity> = {}): ServerCapacity {
  return {
    memory_total_bytes: 16 * gib,
    memory_available_bytes: 2 * gib,
    memory_floor_bytes: 1.6 * gib,
    start_memory_bytes: 2 * gib,
    server_stall: 12.5,
    server_stall_limit: 10,
    host_stall: 3,
    host_stall_limit: 20,
    disks: [{ name: 'Cloudfield data filesystem', total_bytes: 500 * gib, free_bytes: 200 * gib, reserve_bytes: 25 * gib }],
    start_disk_bytes: 4 * gib,
    vms: { awake: 3, frozen: 1, paused: 2, saved: 5, stopped: 1, closed: 2 },
    last_launch_refusal: { at: '2026-11-27T10:00:00Z', op: 'launch', cause: 'memory', text: refusalText, run_id: 'run_1' },
    last_wake_refusal: { at: '2026-11-27T09:00:00Z', op: 'wake-auto', cause: 'stall', text: 'scheduler: the server stalled on memory for 12.5% of the last 60s', run_id: 'run_9' },
    day: [
      { kind: 'refusal', op: 'launch', cause: 'memory', count: 2, used_bytes: 10 * gib, idle_hour_bytes: 5 * gib, last_at: '2026-11-27T10:00:00Z' },
      { kind: 'wait-end', op: 'subsession', cause: 'started', count: 2, waited_seconds: 180, max_waited_seconds: 120, last_at: '2026-11-27T10:05:00Z' },
    ],
    fortnight: [
      { kind: 'refusal', op: 'launch', cause: 'memory', count: 4, used_bytes: 20 * gib, idle_hour_bytes: 5 * gib, last_at: '2026-11-27T10:00:00Z' },
      { kind: 'refusal', op: 'launch-retry', cause: 'memory', count: 1, used_bytes: 10 * gib, idle_hour_bytes: 5 * gib, last_at: '2026-11-26T10:00:00Z' },
      { kind: 'refusal', op: 'wake', cause: 'stall', count: 3, used_bytes: 10 * gib, idle_hour_bytes: 0, last_at: '2026-11-27T09:00:00Z' },
      { kind: 'refusal', op: 'wake-auto', cause: 'memory', count: 1, last_at: '2026-11-27T08:00:00Z' },
      { kind: 'wait', op: 'subsession', count: 6, last_at: '2026-11-27T10:00:00Z' },
      { kind: 'wait-end', op: 'subsession', cause: 'started', count: 4, waited_seconds: 600, max_waited_seconds: 300, last_at: '2026-11-27T10:05:00Z' },
      { kind: 'freeze', count: 1, available_bytes: gib, floor_bytes: 1.6 * gib, last_at: '2026-11-25T10:00:00Z' },
    ],
    ...over,
  }
}

function seed() {
  useStore.setState({
    ...useStore.getInitialState(),
    info: { ...serverInfo, member: alice },
    connection: 'live',
    hydrated: true,
    runs: { run_1: toRecord(run({ id: 'run_1', title: 'Fix the login page' })) },
    capabilities: { gateway: 'remote', methods: ['server.capacity'], ws: [] },
  }, true)
}

async function show(result: ServerCapacity | Error) {
  seed()
  const serverCapacity = vi.fn(async () => {
    if (result instanceof Error) throw result
    return result
  })
  render(<MachinesSection client={{ serverCapacity }} />)
  await act(async () => {})
  return serverCapacity
}

describe('Machines', () => {
  afterEach(cleanup)

  test('a machine short of memory says a launch is refused, why the last ones were, and what was counted', async () => {
    await show(full())
    expect(screen.getByText("Memory is under the launch line and the server's memory stall is at its limit.")).toBeDefined()
    expect(screen.getByText('2.0 GiB available of 16.0 GiB. Starts still taking their memory raise the line, and waking a VM saved to disk needs the memory it saved.')).toBeDefined()
    expect(screen.getByRole('meter', { name: 'Memory available' }).getAttribute('aria-valuetext')).toBe('2.0 GiB free of 16.0 GiB; the launch line is 3.6 GiB')
    expect(screen.getByText(/12\.5% · at 10%, launches are refused/)).toBeDefined()
    expect(screen.getByText('200.0 GiB free of 500.0 GiB.')).toBeDefined()
    expect(screen.getByText('Frozen by the memory brake').nextElementSibling?.textContent).toBe('1')
    expect(screen.getByText('Asleep on disk').nextElementSibling?.textContent).toBe('5')
    expect(screen.getByText('Asleep with nothing kept').nextElementSibling?.textContent).toBe('1')
    expect(screen.getByText('A wake the server tried by itself')).toBeDefined()

    expect(screen.getByText(refusalText)).toBeDefined()
    expect(screen.getByRole('button', { name: 'Copy why the last launch was refused' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Fix the login page' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1' } })
    // A session this dashboard does not hold has no page to open: its id is text, not a link.
    expect(screen.getByText('run_9')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'run_9' })).toBeNull()

    const summary = screen.getAllByRole('table')[0]
    const cells = (label: string) => within(summary).getByRole('rowheader', { name: new RegExp(label) }).parentElement!.querySelectorAll('td')
    expect([...cells('Launches refused')].map((td) => td.textContent)).toEqual(['2', '5'])
    expect([...cells('Subsessions that waited')].map((td) => td.textContent)).toEqual(['0', '6'])
    expect([...cells('Mean wait')].map((td) => td.textContent)).toEqual(['1m 30s', '2m 30s'])
    expect([...cells('Longest wait')].map((td) => td.textContent)).toEqual(['2m 00s', '5m 00s'])
    expect([...cells('idle over an hour')].map((td) => td.textContent)).toEqual(['50%', '25%'])
    expect([...cells('Wakes refused')].map((td) => td.textContent)).toEqual(['0', '4'])
    expect([...cells('tried by the server itself')].map((td) => td.textContent)).toEqual(['0', '1'])
    expect(screen.getByText('2 VMs of closed sessions still hold memory.')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'All 7 counters' }))
    expect(screen.getByRole('rowheader', { name: 'refusal launch-retry memory' })).toBeDefined()
  })

  test('shows each measurement error verbatim', async () => {
    await show(full({
      memory_total_bytes: 0, memory_available_bytes: 0, memory_floor_bytes: 0, memory_error: 'open /proc/meminfo: permission denied',
      server_stall: undefined, host_stall: undefined, stall_error: 'read /sys/fs/cgroup/system.slice/memory.pressure: no such device',
      disks: [], disk_error: 'scheduler: host capacity unavailable: runtime filesystems and host memory: docker: Cannot connect to the Docker daemon',
    }))
    expect(screen.getByText('open /proc/meminfo: permission denied')).toBeDefined()
    expect(screen.getByText('read /sys/fs/cgroup/system.slice/memory.pressure: no such device')).toBeDefined()
    expect(screen.getByText(/Cannot connect to the Docker daemon/)).toBeDefined()
    expect(screen.getAllByText('Not measured: the error is below.')).toHaveLength(2)
    expect(screen.getByText('Memory and disk or the runtime could not be measured.')).toBeDefined()
  })

  test('a stall that could not be read is not known, and one the server does not measure is not claimed as checked', async () => {
    await show(full({ memory_available_bytes: 12 * gib, server_stall: undefined, stall_error: 'read memory.pressure: input/output error' }))
    expect(screen.getByText('Not known')).toBeDefined()
    expect(screen.getByText(/A memory stall could not be read\. If it is the server\u2019s own, every launch is refused/)).toBeDefined()
    expect(screen.queryByText('Fits')).toBeNull()
    cleanup()
    await show(full({ memory_available_bytes: 12 * gib, server_stall: undefined, stall_error: undefined }))
    expect(screen.getByText("Memory and every disk are above the launch line. The server's memory stall is not measured here, so it is not checked.")).toBeDefined()
    expect(screen.getByText('Fits')).toBeDefined()
  })

  test('a healthy machine with nothing counted says a launch fits and nothing happened', async () => {
    await show(full({ memory_available_bytes: 12 * gib, server_stall: 0, host_stall: 0, last_launch_refusal: undefined, last_wake_refusal: undefined, day: [], fortnight: [] }))
    expect(screen.getByText("Memory and every disk are above the launch line, and the server's memory stall is under its limit.")).toBeDefined()
    expect(screen.getAllByText('None in 14 days.')).toHaveLength(2)
    expect(screen.getByText('Nothing counted in 14 days')).toBeDefined()
    expect(screen.queryByRole('table')).toBeNull()
  })

  test('without run ids, as a member who is not a super admin gets it, names no session', async () => {
    const plain = full()
    delete plain.last_launch_refusal!.run_id
    delete plain.last_wake_refusal!.run_id
    await show(plain)
    expect(screen.getByText(refusalText)).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Fix the login page' })).toBeNull()
    expect(screen.queryByText('Session')).toBeNull()
  })

  test('says when the disk check is off instead of listing no disk', async () => {
    await show(full({ memory_available_bytes: 12 * gib, server_stall: 0, disks: [], disk_unchecked: true }))
    expect(screen.getByText(/Not checked: the server was started with/).textContent).toBe('Not checked: the server was started with --min-free-disk negative, so no launch is refused for disk.')
    expect(screen.getByText(/Disk is not checked\./)).toBeDefined()
  })

  test('with the disk check off, still shows that the runtime could not be asked', async () => {
    await show(full({ memory_available_bytes: 12 * gib, server_stall: 0, disks: [], disk_unchecked: true, disk_error: 'runtime: Cannot connect to the Docker daemon' }))
    expect(screen.getByText(/Not checked: the server was started with/)).toBeDefined()
    expect(screen.getByText('runtime: Cannot connect to the Docker daemon')).toBeDefined()
    expect(screen.getByText('The container or VM runtime could not be asked.')).toBeDefined()
  })

  test('a failed read shows the error and reads again on Try again', async () => {
    const serverCapacity = await show(new Error('server.capacity: this server does not measure its machine'))
    expect(within(screen.getByRole('alert')).getByText('server.capacity: this server does not measure its machine')).toBeDefined()
    serverCapacity.mockResolvedValueOnce(full())
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await act(async () => {})
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByText(refusalText)).toBeDefined()
  })
})
