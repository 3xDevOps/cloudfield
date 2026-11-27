import { type ReactNode, useEffect, useRef } from 'react'
import { enhancedSupported, label, ready, signedOutState, supportWords } from '@/components/agents/agent-copy'
import { Ellipsis } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { IconTile } from '@/components/ui/icon-tile'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { StatusDot } from '@/components/ui/status-dot'
import type { AgentInfo, AgentLogin } from '@/lib/types'

/**
 * An added login the list found, by the kind it read at sign-in. agent.login.list does not say whether a login can still
 * sign in, so a found one counts as agent.list's login_found does for the default login.
 */
function addedLoginFound(logins: AgentLogin[] | undefined): boolean {
  return logins?.some((login) => login.id !== 'default' && login.kind !== undefined) ?? false
}

function facts(agent: AgentInfo, logins: AgentLogin[] | undefined): string {
  if (agent.installed !== true) return 'Not installed'
  if (logins && logins.length > 1) return agent.login_problem ? `Installed · ${logins.length} logins · default login ${signedOutState(agent).toLowerCase()}` : `Installed · ${logins.length} logins`
  if (agent.login_problem) return signedOutState(agent)
  if (agent.login_found === undefined) return 'Installed'
  return agent.login_found ? 'Installed · Login found' : 'Installed · No login found'
}

export function AgentList({
  agents,
  label: listLabel = 'Agents',
  onSetUp,
  onRun,
  onRemove,
  extra,
  below,
  logins,
  returnFocusTo,
  primary,
}: {
  agents: AgentInfo[]
  label?: string
  onSetUp?: (agent: AgentInfo) => void
  onRun?: (agent: AgentInfo) => void
  onRemove?: (agent: AgentInfo) => void
  extra?: (agent: AgentInfo) => ReactNode
  /** Full-width content under the row, such as Claude Code's logins. */
  below?: (agent: AgentInfo) => ReactNode
  /** The agent's logins, where it keeps added ones. */
  logins?: (agent: AgentInfo) => AgentLogin[] | undefined
  returnFocusTo?: string
  primary?: string
}) {
  const list = useRef<HTMLUListElement>(null)
  useEffect(() => {
    if (!returnFocusTo) return
    const actions = list.current?.querySelectorAll<HTMLElement>('[data-agent-action]') ?? []
    Array.from(actions).find((action) => action.dataset.agentAction === returnFocusTo)?.focus()
  }, [returnFocusTo])
  return (
    <InsetList ref={list} aria-label={listLabel}>
      {agents.map((agent) => {
        const name = label(agent)
        const shown = logins?.(agent)
        // Another login that is found can run the agent while the default one cannot.
        const usable = ready(agent) || (agent.installed === true && addedLoginFound(shown))
        const runnable = usable && onRun
        const removable = onRemove && (agent.source === 'member' || agent.installed === true)
        const action = runnable ? 'Run' : agent.installed === true ? 'Log in' : 'Set up'
        return (
          <InsetListItem key={agent.name} className="flex-wrap gap-y-2">
            <IconTile className="size-8">
              <AgentGlyph agent={agent.glyph ?? agent.name} colored className="size-5" />
            </IconTile>
            <span className="flex min-w-48 flex-1 flex-col">
              <span className="truncate text-ui font-medium text-text">{name}</span>
              <span className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-ui-sm text-muted">
                <StatusDot tone={agent.installed === true && agent.login_problem ? 'needs-you' : usable ? 'done' : 'neutral'} />
                <span>{facts(agent, shown)}</span>
                <span aria-hidden>·</span>
                <span aria-label={enhancedSupported(agent) ? 'Supports Standard and Enhanced' : 'Supports Standard only'}>{supportWords(agent)}</span>
              </span>
              {agent.installed === true && agent.login_problem && <span className="font-code text-ui-sm break-words text-muted">{agent.login_problem}</span>}
            </span>
            <span className="ml-auto flex flex-wrap items-center justify-end gap-2 max-sm:order-1">
              {extra?.(agent)}
              {(runnable || onSetUp) && (
                <Button
                  size="sm"
                  variant={agent.name === primary ? 'primary' : 'secondary'}
                  data-agent-action={agent.name}
                  aria-label={`${action} ${name}`}
                  onClick={() => (runnable ? onRun(agent) : onSetUp?.(agent))}
                >
                  {action}
                </Button>
              )}
            </span>
            {/* On a phone the overflow menu stays on the title line and the buttons wrap under it. */}
            {((runnable && onSetUp) || removable) && (
              <Menu>
                <MenuTrigger asChild>
                  <Button size="icon" variant="ghost" label={`More for ${name}`}>
                    <Ellipsis />
                  </Button>
                </MenuTrigger>
                <MenuContent align="end">
                  {runnable && onSetUp && <MenuItem onSelect={() => onSetUp(agent)}>Set up again</MenuItem>}
                  {removable && <MenuItem onSelect={() => onRemove(agent)}>Remove…</MenuItem>}
                </MenuContent>
              </Menu>
            )}
            <div className="order-2 flex basis-full flex-col gap-2 empty:hidden">{below?.(agent)}</div>
          </InsetListItem>
        )
      })}
    </InsetList>
  )
}
