import { mkdir, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'
import { expect, test } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

test.use({ launchOptions: { ignoreDefaultArgs: ['--hide-scrollbars'] } })

for (const width of [900, 1280]) {
  test(`file drags share one overlay across the Chat pane and composer at ${width}px`, async ({ page, aether }, testInfo) => {
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.setViewportSize({ width, height: 720 })
    const alice = await aether.member('alice')
    const repo = await aether.seedRepo('project')
    await seedWorkspace(alice, aether.server.addr, repo)
    const member = await memberID(alice)
    const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
    const workspace = workspaces[0].id
    const now = new Date().toISOString()
    const run = {
      id: 'run-attachments', workspace_id: workspace, member_id: member, account_member_id: member,
      task: 'Review the importer', harness: 'claude', mode: 'tui', status: 'running', state: 'working',
      status_seq: 1, branch: 'aether/run-attachments', created_at: now, started_at: now,
    }
    await page.route('**/api/v1/run.list', (route) => route.fulfill({ json: { runs: [run] } }))
    await page.route('**/api/v1/run.get', (route) => route.fulfill({ json: { run } }))
    await page.route('**/api/v1/run.room.list', (route) => route.fulfill({ json: { messages: Array.from({ length: 40 }, (_, index) => ({
      id: `msg-attachments-${index}`, workspace_id: workspace, run_id: run.id, actor_id: member,
      kind: 'steer_request', state: 'sent', body: `Review note ${index + 1} for the importer.`,
      created_at: new Date(Date.parse(now) - (40 - index) * 1_000).toISOString(), updated_at: now,
    })) } }))
    await page.route('**/api/v1/run.room.status', (route) => route.fulfill({
      json: { workspace_id: workspace, run_id: run.id, protected: false, watchers: [], queued_steers: 0 },
    }))
    await page.route('**/api/v1/run.children', (route) => route.fulfill({ json: { children: [] } }))
    const uploads: string[] = []
    await page.route('**/api/v1/terminal.file', (route) => {
      const { name } = route.request().postDataJSON()
      uploads.push(name)
      return route.fulfill({ json: { path: `/tmp/${name}` } })
    })

    await page.goto(`${alice.url}&run=${run.id}`)
    await page.getByRole('tab', { name: 'Chat' }).click()
    const log = page.getByRole('log', { name: 'Chat' })
    await expect(log).toBeVisible()
    const box = page.getByRole('textbox', { name: 'Message the agent' })
    await box.fill('Read both files')
    const overlay = page.locator('[data-slot="drop-overlay"]')
    const center = async (target: typeof box) => {
      const bounds = await target.boundingBox()
      if (!bounds) throw new Error('Drop target is not visible')
      return { x: bounds.x + bounds.width / 2, y: bounds.y + bounds.height / 2 }
    }
    const scrollState = () => log.evaluate((element) => ({
      top: element.scrollTop,
      gutter: Math.round(element.getBoundingClientRect().width) - element.clientWidth,
      max: element.scrollHeight - element.clientHeight,
    }))
    await expect.poll(async () => (await scrollState()).max).toBeGreaterThan(200)
    await expect.poll(async () => (await scrollState()).gutter).toBeGreaterThan(0)
    const thumbColor = () => log.evaluate((element) => getComputedStyle(element, '::-webkit-scrollbar-thumb').backgroundColor)
    await expect.poll(thumbColor).not.toBe('rgba(0, 0, 0, 0)')
    const chat = await center(log)
    await page.mouse.move(chat.x, chat.y)
    await page.mouse.wheel(0, -10_000)
    await expect.poll(async () => (await scrollState()).top).toBe(0)
    const beforeCancel = await scrollState()
    await page.screenshot({ path: testInfo.outputPath('file-drop-before.png') })
    const composer = await center(box)
    const cdp = await page.context().newCDPSession(page)
    const dragFile = async (name: string) => {
      const file = testInfo.outputPath(name)
      await mkdir(dirname(file), { recursive: true })
      await writeFile(file, name)
      const data = { items: [], files: [file], dragOperationsMask: 1 }
      return (type: 'dragEnter' | 'dragOver' | 'drop' | 'dragCancel', point: typeof chat) =>
        cdp.send('Input.dispatchDragEvent', { type, ...point, data })
    }

    const cancelled = await dragFile('cancelled.txt')
    await cancelled('dragEnter', composer)
    await cancelled('dragOver', composer)
    await expect(overlay).toHaveText('Drop to attach 1 file')
    const veil = await overlay.elementHandle()
    await cancelled('dragOver', chat)
    await expect(overlay).toHaveText('Drop to attach 1 file')
    await cancelled('dragOver', composer)
    await expect(overlay).toHaveText('Drop to attach 1 file')
    await expect.poll(thumbColor).toBe('rgba(0, 0, 0, 0)')
    expect(await scrollState()).toEqual(beforeCancel)
    expect(await veil?.evaluate((element) => element.isConnected)).toBe(true)
    await page.screenshot({ animations: 'disabled', path: testInfo.outputPath('file-drop-dark.png') })
    // CDP dragCancel alone does not send a dragleave to the page.
    const outsideChat = { x: 0, y: 0 }
    await cancelled('dragOver', outsideChat)
    await cancelled('dragCancel', outsideChat)
    await expect(overlay).toHaveCount(0)
    await expect(page.getByRole('button', { name: /Remove attached file/ })).toHaveCount(0)
    await expect.poll(async () => (await scrollState()).top).toBe(beforeCancel.top)
    expect((await scrollState()).gutter).toBe(beforeCancel.gutter)
    await expect.poll(thumbColor).not.toBe('rgba(0, 0, 0, 0)')

    await page.mouse.move(chat.x, chat.y)
    await page.mouse.wheel(0, 120)
    await expect.poll(async () => (await scrollState()).top).toBeGreaterThan(beforeCancel.top)
    const beforeDrop = await scrollState()

    const fromChat = await dragFile('chat.txt')
    await fromChat('dragEnter', chat)
    await fromChat('dragOver', chat)
    await expect(overlay).toHaveText('Drop to attach 1 file')
    await expect.poll(thumbColor).toBe('rgba(0, 0, 0, 0)')
    expect(await scrollState()).toEqual(beforeDrop)
    await fromChat('drop', chat)
    await expect(overlay).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Remove attached file 1: chat.txt' })).toBeEnabled()
    await expect.poll(async () => (await scrollState()).top).toBe(beforeDrop.top)
    expect((await scrollState()).gutter).toBe(beforeDrop.gutter)
    await expect.poll(thumbColor).not.toBe('rgba(0, 0, 0, 0)')
    await page.screenshot({ path: testInfo.outputPath('file-drop-after.png') })
    await page.mouse.move(chat.x, chat.y)
    await page.mouse.wheel(0, 120)
    await expect.poll(async () => (await scrollState()).top).toBeGreaterThan(beforeDrop.top)

    const fromComposer = await dragFile('composer.txt')
    const field = await center(box)
    await fromComposer('dragEnter', field)
    await fromComposer('dragOver', field)
    await expect(overlay).toHaveText('Drop to attach 1 file')
    await fromComposer('drop', field)
    await expect(overlay).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Remove attached file 2: composer.txt' })).toBeEnabled()
    await page.getByLabel('Choose files to attach').setInputFiles({
      name: 'picked.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from('picked'),
    })
    await expect(page.getByRole('button', { name: 'Remove attached file 3: picked.txt' })).toBeEnabled()

    await box.evaluate((field) => {
      const clipboardData = new DataTransfer()
      clipboardData.items.add(new File(['pasted'], 'pasted.txt', { type: 'text/plain' }))
      clipboardData.items.add(new File([new Uint8Array(8 * 1024 * 1024 + 1)], 'oversized.png', { type: 'image/png' }))
      field.dispatchEvent(new ClipboardEvent('paste', { clipboardData, bubbles: true, cancelable: true }))
    })
    await expect(page.getByRole('button', { name: 'Remove attached file 4: pasted.txt' })).toBeEnabled()
    await expect(page.getByText('oversized.png is over the 8 MiB image limit.')).toBeVisible()
    await expect(box).toHaveValue('Read both files')
    expect(uploads).toEqual(['chat.txt', 'composer.txt', 'picked.txt', 'pasted.txt'])
  })
}
