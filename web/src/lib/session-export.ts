// A session's record as one Markdown file, paged the way `cloudfield logs`
// pages it (cmd/aether/logs.go) but to the start of the history.

import { ApiError, codeNotFound, codeUnavailable, type Api } from '@/lib/api'
import { saveFile } from '@/lib/download'
import { message } from '@/lib/format'
import type { SessionFrame, SessionItem, SessionMessage, SessionToolCall } from '@/lib/session-types'
import { runLabel } from '@/lib/status'
import type { RecordedPullRequest, Run, TerminalHistoryResult } from '@/lib/types'

type Client = Pick<Api, 'runWithPullRequests' | 'runACPHistory' | 'runACPItem' | 'terminalHistory'>

export const outputCap = 20_000

const terminalPage = 200

/** Waits between reads of a terminal history page the server ran out of time on. */
export const stallDelays = [250, 500, 1000, 2000, 4000]

/** Sessions with an export in flight. */
export const exporting = new Set<string>()

function headCapped(text: string): string {
  return text.length > outputCap ? `${text.slice(0, outputCap)}\n[... ${text.length - outputCap} more characters]` : text
}

// The end of a tool's output holds its failure summary, which is why the server keeps the tail too.
function tailCapped(text: string): string {
  return text.length > outputCap ? `[... ${text.length - outputCap} earlier characters]\n${text.slice(-outputCap)}` : text
}

function fenced(text: string): string {
  const longest = Math.max(2, ...(text.match(/`+/g) ?? []).map((run) => run.length))
  const fence = '`'.repeat(longest + 1)
  return `${fence}\n${text}\n${fence}`
}

interface Read {
  item: SessionItem
  /** Why the item is not whole. */
  cut?: string
}

const printed = new Set<SessionItem['kind']>(['message', 'tool_call', 'notice'])

// The server cuts an item to fit a history page; when the whole item cannot
// be fetched, the cut one stays, marked with the fetch error.
async function wholeItem(client: Client, runID: string, frame: SessionFrame): Promise<Read | undefined> {
  let item = frame.item
  if (!item) return undefined
  if (frame.truncated && printed.has(item.kind)) {
    try {
      item = await client.runACPItem(runID, item.seq)
    } catch (err) {
      return { item, cut: `[truncated; run.acp.item: ${message(err)}]` }
    }
  }
  return { item, cut: item.truncated ? '[cut by the server]' : undefined }
}

async function readConversation(client: Client, runID: string): Promise<{ items: Read[]; expired: boolean }> {
  let items: Read[] = []
  let before: number | undefined
  for (;;) {
    const page = await client.runACPHistory(runID, before)
    const whole: Read[] = []
    for (const frame of page.frames) {
      const read = await wholeItem(client, runID, frame)
      if (read) whole.push(read)
    }
    items = [...whole, ...items]
    const oldest = whole[0]?.item.seq
    if (oldest === undefined || oldest <= (page.oldest_seq ?? 1)) return { items, expired: Boolean(page.truncated_before) }
    before = oldest
  }
}

type Entry =
  | { message: SessionMessage; text: string }
  | { call: SessionToolCall; output: string; cuts: Set<string> }
  | { note: string }

// A message is logged in segments and a tool call in snapshots, so an entry
// gathers its items by id within a turn and sits where its first item was.
function entries(items: Read[]): Entry[] {
  const list: Entry[] = []
  const messages = new Map<string, Extract<Entry, { text: string }>>()
  const calls = new Map<string, Extract<Entry, { output: string }>>()
  for (const { item, cut } of items) {
    const mark = cut ? ` ${cut}` : ''
    if (item.kind === 'message' && item.message) {
      const key = `${item.turn} ${item.message.message_id}`
      const held = messages.get(key)
      if (held) held.text += item.message.text + mark
      else {
        const entry = { message: item.message, text: item.message.text + mark }
        messages.set(key, entry)
        list.push(entry)
      }
    } else if (item.kind === 'tool_call' && item.tool_call) {
      const key = `${item.turn} ${item.tool_call.id}`
      let held = calls.get(key)
      if (held) {
        held.call = item.tool_call
        held.output += item.tool_call.output ?? ''
      } else {
        held = { call: item.tool_call, output: item.tool_call.output ?? '', cuts: new Set() }
        calls.set(key, held)
        list.push(held)
      }
      if (cut) held.cuts.add(cut)
    } else if (item.kind === 'notice' && item.notice) {
      const { severity, title, description } = item.notice
      list.push({ note: `**Notice (${severity}):** ${title}${mark}${description ? `\n\n${description}` : ''}` })
    } else if (item.kind === 'reset') {
      list.push({ note: '**New agent conversation**' })
    }
  }
  return list
}

const base64Run = /[A-Za-z0-9+/]{512,}={0,2}/g

// The server keeps an image's type in `content` and drops its bytes, but a
// tool's raw output can still hold them.
function resultText(call: SessionToolCall, output: string): string {
  if (output) return output
  const shown = (call.content ?? []).flatMap((c) =>
    c.type === 'image' ? [`[image${c.mime_type ? `, ${c.mime_type}` : ''}]`] : c.type === 'text' && c.text ? [c.text] : [])
  if (shown.length > 0) return shown.join('\n\n')
  const raw = call.raw_output
  if (raw === undefined || raw === null) return ''
  return (typeof raw === 'string' ? raw : JSON.stringify(raw, null, 2)).replace(base64Run, (run) => `[base64 data, ${run.length} characters]`)
}

function toolText(call: SessionToolCall, output: string, cuts: Set<string>): string {
  const state = [call.status, call.exit_code === undefined ? undefined : `exit ${call.exit_code}`].filter(Boolean).join(', ')
  const blocks = [`### Tool${call.parent_id ? ' (subagent)' : ''}${state ? ` (${state})` : ''}`, ...cuts, fenced(call.title)]
  const result = resultText(call, output)
  if (result) blocks.push(fenced(tailCapped(result)))
  for (const diff of call.diffs ?? []) blocks.push(`\`${diff.path}\``, fenced(headCapped(diff.patch)))
  return blocks.join('\n\n')
}

function entryText(entry: Entry): string {
  if ('note' in entry) return entry.note
  if ('call' in entry) return toolText(entry.call, entry.output, entry.cuts)
  const who = entry.message.role === 'user' ? 'User' : 'Assistant'
  const text = entry.text.trim()
  const unclosed = (text.match(/^\s*```/gm) ?? []).length % 2 === 1
  return `### ${who}${entry.message.parent_id ? ' (subagent)' : ''}\n\n${text}${unclosed ? '\n```' : ''}`
}

async function conversation(client: Client, runID: string): Promise<string> {
  const { items, expired } = await readConversation(client, runID)
  const blocks = entries(items).map(entryText)
  if (expired) blocks.unshift('[earlier conversation has expired]')
  return ['## Conversation', ...blocks].join('\n\n')
}

const cursorExpired = 'earlier terminal history has expired'

// Pages like the Terminal view's history cache (routes/terminal/history-cache.ts):
// a page the server ran out of time on comes back empty with the cursor it was
// asked for, and is read again.
async function terminal(client: Client, runID: string): Promise<string> {
  const lines: string[] = []
  const seen = new Set<string>()
  const consumed = new Set<string>()
  let before: string | undefined
  let stalls = 0
  let marker: string | undefined
  for (;;) {
    let page: TerminalHistoryResult
    try {
      page = await client.terminalHistory({ run_id: runID, limit: terminalPage, ...(before ? { before } : {}) })
    } catch (err) {
      if (!(err instanceof ApiError)) throw err
      if (err.code === codeNotFound && before === undefined) marker = '[no terminal history]'
      else if (err.code === codeUnavailable && err.message.includes(cursorExpired)) marker = '[earlier output has expired]'
      else throw err
      break
    }
    const next = page.next_cursor || undefined
    if (page.lines.length === 0 && page.has_more && next === before) {
      const delay = stallDelays[stalls++]
      if (delay === undefined) throw new Error(`terminal.history did not advance after ${stallDelays.length} retries; nothing was saved`)
      await new Promise((resolve) => setTimeout(resolve, delay))
      continue
    }
    stalls = 0
    const fresh = page.lines.filter((line) => !seen.has(line.cursor))
    for (const line of fresh) seen.add(line.cursor)
    lines.unshift(...fresh.map((line) => line.text))
    if (!page.has_more) {
      if (page.truncated_before) marker = '[earlier output has expired]'
      break
    }
    if (next === undefined || next === before || consumed.has(next)) {
      throw new Error('terminal.history returned a cursor that does not advance; nothing was saved')
    }
    if (before) consumed.add(before)
    before = next
  }
  const blocks = ['## Terminal']
  if (marker) blocks.push(marker)
  if (lines.length > 0) blocks.push(fenced(lines.join('\n')))
  return blocks.join('\n\n')
}

function header(run: Run, pullRequests: RecordedPullRequest[], workspace: string | undefined, now: Date): string {
  const lines = [
    `- Session: \`${run.id}\``,
    `- Workspace: ${workspace ?? run.workspace_id}`,
    ...(run.branch ? [`- Branch: \`${run.branch}\``] : []),
    `- Agent: ${[run.harness, run.model].filter(Boolean).join(', ')}`,
    `- Status: ${run.status}${run.reason ? ` (${run.reason})` : ''}`,
    ...pullRequests.map((pr) => `- Pull request: ${pr.url} (${pr.state})`),
    `- Exported: ${now.toISOString()}`,
  ]
  return `# ${runLabel(run)}\n\n${lines.join('\n')}`
}

export async function sessionMarkdown(client: Client, runID: string, workspace: string | undefined, now: Date): Promise<string> {
  const { run, pull_requests } = await client.runWithPullRequests(runID)
  const body = await (run.acp ? conversation(client, runID) : terminal(client, runID))
  return `${header(run, pull_requests ?? [], workspace, now)}\n\n${body}\n`
}

export async function exportSession(client: Client, runID: string, workspace: string | undefined): Promise<string> {
  exporting.add(runID)
  try {
    const name = `${runID}.md`
    saveFile(new Blob([await sessionMarkdown(client, runID, workspace, new Date())], { type: 'text/markdown' }), name)
    return name
  } finally {
    exporting.delete(runID)
  }
}
