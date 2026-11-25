// A session's record as one Markdown file, paged the way `cloudfield logs`
// pages it (cmd/aether/logs.go) but to the start of the history.

import type { Api } from '@/lib/api'
import { saveFile } from '@/lib/download'
import { message } from '@/lib/format'
import type { SessionFrame, SessionItem, SessionMessage, SessionToolCall } from '@/lib/session-types'
import { runLabel } from '@/lib/status'
import type { RecordedPullRequest, Run } from '@/lib/types'
import { toolResult } from '@/store/session-rows'

type Client = Pick<Api, 'runWithPullRequests' | 'runACPHistory' | 'runACPItem' | 'terminalHistory'>

export const outputCap = 20_000

const terminalPage = 200

function capped(text: string): string {
  return text.length > outputCap ? `${text.slice(0, outputCap)}\n[... ${text.length - outputCap} more characters]` : text
}

function fenced(text: string): string {
  const longest = Math.max(2, ...(text.match(/`+/g) ?? []).map((run) => run.length))
  const fence = '`'.repeat(longest + 1)
  return `${fence}\n${text}\n${fence}`
}

// The server cuts an item to fit a history page; when the whole item cannot
// be fetched, the cut text stays, marked with the fetch error.
async function wholeItem(client: Client, runID: string, frame: SessionFrame): Promise<SessionItem | undefined> {
  const item = frame.item
  if (!item || !frame.truncated) return item
  try {
    return await client.runACPItem(runID, item.seq)
  } catch (err) {
    const mark = ` [truncated; run.acp.item: ${message(err)}]`
    if (item.message) return { ...item, message: { ...item.message, text: item.message.text + mark } }
    if (item.tool_call) return { ...item, tool_call: { ...item.tool_call, title: item.tool_call.title + mark } }
    return item
  }
}

async function readConversation(client: Client, runID: string): Promise<{ items: SessionItem[]; expired: boolean }> {
  let items: SessionItem[] = []
  let before: number | undefined
  for (;;) {
    const page = await client.runACPHistory(runID, before)
    const whole: SessionItem[] = []
    for (const frame of page.frames) {
      const item = await wholeItem(client, runID, frame)
      if (item) whole.push(item)
    }
    items = [...whole, ...items]
    const oldest = whole[0]?.seq
    if (oldest === undefined || oldest <= (page.oldest_seq ?? 1)) return { items, expired: Boolean(page.truncated_before) }
    before = oldest
  }
}

type Entry =
  | { message: SessionMessage; text: string }
  | { call: SessionToolCall; output: string }
  | { note: string }

// A message is logged in segments and a tool call in snapshots, so an entry
// gathers its items by id within a turn and sits where its first item was.
function entries(items: SessionItem[]): Entry[] {
  const list: Entry[] = []
  const messages = new Map<string, Extract<Entry, { text: string }>>()
  const calls = new Map<string, Extract<Entry, { output: string }>>()
  for (const item of items) {
    if (item.kind === 'message' && item.message) {
      const key = `${item.turn} ${item.message.message_id}`
      const held = messages.get(key)
      if (held) held.text += item.message.text
      else {
        const entry = { message: item.message, text: item.message.text }
        messages.set(key, entry)
        list.push(entry)
      }
    } else if (item.kind === 'tool_call' && item.tool_call) {
      const key = `${item.turn} ${item.tool_call.id}`
      const held = calls.get(key)
      if (held) {
        held.call = item.tool_call
        held.output += item.tool_call.output ?? ''
      } else {
        const entry = { call: item.tool_call, output: item.tool_call.output ?? '' }
        calls.set(key, entry)
        list.push(entry)
      }
    } else if (item.kind === 'notice' && item.notice) {
      const { severity, title, description } = item.notice
      list.push({ note: `**Notice (${severity}):** ${title}${description ? `\n\n${description}` : ''}` })
    } else if (item.kind === 'reset') {
      list.push({ note: '**New agent conversation**' })
    }
  }
  return list
}

function toolText(call: SessionToolCall, output: string): string {
  const state = [call.status, call.exit_code === undefined ? undefined : `exit ${call.exit_code}`].filter(Boolean).join(', ')
  const blocks = [`### Tool${state ? ` (${state})` : ''}`, fenced(call.title)]
  const result = output || toolResult(call)
  if (typeof result === 'string') {
    if (result) blocks.push(fenced(capped(result)))
  } else if (result !== undefined && result !== null) {
    blocks.push(fenced(capped(JSON.stringify(result, null, 2))))
  }
  for (const diff of call.diffs ?? []) blocks.push(`\`${diff.path}\``, fenced(capped(diff.patch)))
  return blocks.join('\n\n')
}

function entryText(entry: Entry): string {
  if ('note' in entry) return entry.note
  if ('call' in entry) return toolText(entry.call, entry.output)
  const who = entry.message.role === 'user' ? 'User' : 'Assistant'
  return `### ${who}${entry.message.parent_id ? ' (subagent)' : ''}\n\n${entry.text.trim()}`
}

async function conversation(client: Client, runID: string): Promise<string> {
  const { items, expired } = await readConversation(client, runID)
  const blocks = entries(items).map(entryText)
  if (expired) blocks.unshift('[earlier conversation has expired]')
  return ['## Conversation', ...blocks].join('\n\n')
}

async function terminal(client: Client, runID: string): Promise<string> {
  const pages: string[][] = []
  const seen = new Set<string>()
  let before: string | undefined
  let expired = false
  for (;;) {
    const page = await client.terminalHistory({ run_id: runID, limit: terminalPage, ...(before ? { before } : {}) })
    pages.unshift(page.lines.map((line) => line.text))
    if (!page.has_more || !page.next_cursor || seen.has(page.next_cursor)) {
      expired = Boolean(page.truncated_before)
      break
    }
    seen.add(page.next_cursor)
    before = page.next_cursor
  }
  const blocks = ['## Terminal']
  if (expired) blocks.push('[earlier output has expired]')
  blocks.push(fenced(pages.flat().join('\n')))
  return blocks.join('\n\n')
}

function header(run: Run, pullRequests: RecordedPullRequest[], workspace: string | undefined, now: Date): string {
  const lines = [
    `- Session: \`${run.id}\``,
    `- Workspace: ${workspace ?? run.workspace_id}`,
    `- Branch: \`${run.branch}\``,
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

/** Downloads the session's record and resolves to the file's name. */
export async function exportSession(client: Client, runID: string, workspace: string | undefined): Promise<string> {
  const name = `${runID}.md`
  saveFile(new Blob([await sessionMarkdown(client, runID, workspace, new Date())], { type: 'text/markdown' }), name)
  return name
}
