import { createContext, useContext } from 'react'
import type { AgentInfo } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunRoom } from '@/routes/run/room'
import type { RunShells } from '@/routes/run/shells'
import { isRunRoute } from '@/routes/run/views'
import { useStore } from '@/store'

/** What one on-screen session shares with every frame that shows it. */
export interface RunSessionValue {
  agent: AgentTerminal
  agentName: string
  agentEntry: AgentInfo | undefined
  room: RunRoom
  shells: RunShells
}

/** The open run's agent, for surfaces inside the run view that send it a message. */
export const RunAgentContext = createContext<RunSessionValue | null>(null)

export function useRunAgent() {
  return useContext(RunAgentContext)
}

/** False inside a session tile that is not the focused one: nothing in it takes the keyboard or answers a key unasked. */
export const FrameFocusContext = createContext(true)

export const RunParamsContext = createContext<Record<string, string> | null>(null)

const none: Record<string, string> = {}

/** The params of the frame around a session view: the route's in the focused tile, the tab's elsewhere, and the route's outside a frame. */
export function useRunParams(runID: string): Record<string, string> {
  const framed = useContext(RunParamsContext)
  const route = useStore((s) => s.route)
  return framed ?? (isRunRoute(route, runID) ? route.params : none)
}
