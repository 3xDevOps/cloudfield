import { useEffect, useState } from 'react'
import { Linear, Search } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button, buttonVariants } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Input } from '@/components/ui/input'
import { PageBody } from '@/components/ui/page-body'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { StatusDot } from '@/components/ui/status-dot'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useIsMobile } from '@/lib/breakpoints'
import { useElementSize } from '@/lib/hooks'
import type { LinearIssue, LinearTeam } from '@/lib/types'
import { cn } from '@/lib/utils'
import { linearTask } from '@/routes/linear/issues'
import { LinearStatusIcon } from '@/routes/linear/status-icon'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useHeaderPrimary } from '@/store/hooks'
import { loadLinear } from '@/store/linear'
import { isTerminal } from '@/store/runs'

const allTeams = 'all'
const searchDelay = 250
// The fixed columns take 592px, which leaves a title at least 328px.
const tableWidth = 920

type Read = { issues: LinearIssue[] } | { error: string } | null

function Status({ issue }: { issue: LinearIssue }) {
  return (
    <Badge>
      <LinearStatusIcon type={issue.state.type} />
      {issue.state.name}
    </Badge>
  )
}

function Actions({ issue }: { issue: LinearIssue }) {
  const open = useStore((s) => Object.values(s.runs).find((run) => run.linear_issue === issue.identifier && !isTerminal(run.status))?.id)
  return (
    <span className="flex shrink-0 items-center gap-2">
      {open ? (
        <Button size="sm" variant="secondary" onClick={() => useStore.getState().navigate('run', { runId: open })}>
          <StatusDot tone="working" />Open session
        </Button>
      ) : (
        <Button size="sm" variant="secondary" onClick={() => useStore.getState().openLaunchWithTask(linearTask(issue), { identifier: issue.identifier, url: issue.url })}>Start session</Button>
      )}
      <a className={cn(buttonVariants({ variant: 'ghost', size: 'sm' }), 'gap-1.5')} href={issue.url} target="_blank" rel="noreferrer">
        <Linear aria-hidden className="size-3.5" />Open in Linear
      </a>
    </span>
  )
}

type Fault = 'no-key' | 'refused' | 'failed'

/** Without a key, or with one Linear refuses, Retry cannot help: Settings is the way on. */
function ReadError({ fault, error, onRetry }: { fault: Fault; error: string; onRetry: () => void }) {
  const settings = (
    <Button size="sm" variant={fault === 'failed' ? 'secondary' : 'primary'} onClick={() => useStore.getState().navigate('settings', { section: 'linear' })}>Open Linear settings</Button>
  )
  const retry = <Button size="sm" variant={fault === 'refused' ? 'secondary' : 'primary'} onClick={onRetry}>Retry</Button>
  return (
    <Callout
      tone="needs-you"
      role="alert"
      title={fault === 'no-key' ? 'Linear is not connected' : fault === 'refused' ? 'Linear refused your API key' : 'Could not load issues'}
      actions={
        fault === 'no-key' ? settings : fault === 'refused' ? <>{settings}{retry}</> : <>{retry}{settings}</>
      }
    >
      {error}
    </Callout>
  )
}

export function LinearRoute({ params, client = api }: RouteProps & { client?: Api }) {
  const identityKey = useStore((s) => s.identityKey)
  return <LinearPage key={identityKey} params={params} client={client} />
}

function LinearPage({ client }: RouteProps & { client: Api }) {
  const status = useStore((s) => s.linear)
  const [typed, setTyped] = useState('')
  const [query, setQuery] = useState('')
  const [team, setTeam] = useState(allTeams)
  const [teams, setTeams] = useState<LinearTeam[]>([])
  const [teamsError, setTeamsError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)
  const [read, setRead] = useState<Read>(null)
  // The sidebar takes 260px of the viewport, so the pane's own width decides, not the window's.
  const [paneRef, pane] = useElementSize<HTMLDivElement>()
  const wide = pane.width >= tableWidth

  useEffect(() => {
    const timer = setTimeout(() => setQuery(typed.trim()), searchDelay)
    return () => clearTimeout(timer)
  }, [typed])

  useEffect(() => {
    let live = true
    setTeamsError(null)
    client.linearTeams().then(
      (list) => { if (live) setTeams(list) },
      (err: unknown) => { if (live) setTeamsError(message(err)) },
    )
    return () => { live = false }
  }, [client, attempt])

  useEffect(() => {
    let live = true
    setRead((prev) => (prev && 'error' in prev ? null : prev))
    client
      .linearIssues({ ...(team !== allTeams ? { team } : {}), ...(query ? { query } : {}) })
      .then(
        (issues) => { if (live) setRead({ issues }) },
        (err: unknown) => { if (live) setRead({ error: message(err) }) },
      )
    return () => { live = false }
  }, [client, team, query, attempt])

  const mobile = useIsMobile()
  const noKey = status?.connected === false && !status.error
  const refused = status?.refused === true
  const issues = read && 'issues' in read ? read.issues : null
  const filters = !noKey && !(refused && issues === null)
  useHeaderPrimary(read !== null && 'error' in read)
  const retry = () => { setAttempt((n) => n + 1); void loadLinear(useStore, client) }
  const subtitle = read && 'error' in read ? undefined : status?.organization ? `Assigned to you · ${status.organization}` : 'Assigned to you'

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader title="Linear" subtitle={subtitle} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <PageBody width="xl" className="gap-4">
          {mobile && subtitle && <p className="-mt-1 text-ui-sm text-muted">{subtitle}</p>}
          {filters && <div className="flex min-w-0 flex-wrap items-center gap-2">
            <label className="relative min-w-0 flex-[1_1_16rem]">
              <span className="sr-only">Search issues</span>
              <Search aria-hidden className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted" />
              <Input type="search" value={typed} onChange={(event) => setTyped(event.target.value)} placeholder="Search by title" className="w-full pl-7" />
            </label>
            <Select value={team} onValueChange={setTeam}>
              <SelectTrigger aria-label="Team" className="w-40 max-sm:flex-1">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={allTeams}>All teams</SelectItem>
                {teams.map((t) => <SelectItem key={t.key} value={t.key}>{t.name}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>}
          {teamsError && filters && !(read && 'error' in read) && (
            <Callout tone="needs-you" role="alert" title="Could not load teams" actions={<Button size="sm" variant="secondary" onClick={retry}>Retry</Button>}>{teamsError}</Callout>
          )}
          {read && 'error' in read ? (
            <ReadError fault={noKey ? 'no-key' : refused ? 'refused' : 'failed'} error={read.error} onRetry={retry} />
          ) : (
          <section aria-label="Issues" className="flex min-w-0 flex-col gap-2">
            <SectionLabel as="h2">
              {issues === null ? 'Loading issues' : issues.length === 1 ? '1 open issue' : `${issues.length} open issues`}
            </SectionLabel>
            <div ref={paneRef} className="overflow-hidden rounded-panel border border-seam">
              {issues && issues.length > 0 && (wide ? (
                <table className="w-full table-fixed text-ui">
                  <thead className="border-b border-seam bg-chrome text-left text-ui-sm text-muted">
                    <tr>
                      <th className="w-22 px-3 py-1.5 font-medium">ID</th>
                      <th className="px-3 py-1.5 font-medium">Title</th>
                      <th className="w-32 px-3 py-1.5 font-medium">Status</th>
                      <th className="w-16 px-3 py-1.5 font-medium">Team</th>
                      <th className="w-20 px-3 py-1.5 font-medium">Updated</th>
                      <th className="w-58 px-3 py-1.5"><span className="sr-only">Actions</span></th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-seam">
                    {issues.map((issue) => (
                      <tr key={issue.identifier} className="transition-colors hover:bg-hover motion-reduce:transition-none">
                        <td className="px-3 py-2 text-muted tabular-nums">{issue.identifier}</td>
                        <td className="truncate px-3 py-2 font-medium text-text" title={issue.title}>{issue.title}</td>
                        <td className="px-3 py-2"><Status issue={issue} /></td>
                        <td className="truncate px-3 py-2 text-muted" title={issue.team?.name}>{issue.team?.key}</td>
                        <td className="px-3 py-2 whitespace-nowrap text-muted">{issue.updated_at && <RelativeTime at={issue.updated_at} compact />}</td>
                        <td className="px-3 py-1.5"><span className="flex justify-end"><Actions issue={issue} /></span></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <ul className="divide-y divide-seam">
                  {issues.map((issue) => (
                    <li key={issue.identifier} className="flex min-w-0 flex-col gap-2 px-3 py-2.5">
                      <span className="flex min-w-0 items-baseline gap-2 text-ui">
                        <span className="shrink-0 text-muted tabular-nums">{issue.identifier}</span>
                        <span className="min-w-0 font-medium text-text">{issue.title}</span>
                      </span>
                      <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-ui-sm text-muted">
                        <Status issue={issue} />
                        {issue.team && <span title={issue.team.name}>{issue.team.key}</span>}
                        {issue.updated_at && <span className="flex items-center gap-2 whitespace-nowrap"><span aria-hidden>·</span><RelativeTime at={issue.updated_at} compact /></span>}
                      </span>
                      <Actions issue={issue} />
                    </li>
                  ))}
                </ul>
              ))}
              {issues?.length === 0 && <p className="px-3 py-6 text-center text-ui text-muted">No open issue assigned to you matches.</p>}
              {issues === null && <p className="px-3 py-6 text-center text-ui text-muted">Loading…</p>}
            </div>
          </section>
          )}
        </PageBody>
      </div>
    </div>
  )
}

registerRoute('linear', LinearRoute)
