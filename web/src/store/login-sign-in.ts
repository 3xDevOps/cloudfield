import { useEffect, useRef } from 'react'
import { create } from 'zustand'
import { ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentLogin } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'
import { unanswered } from '@/store/optimistic'

/** A sign-in of a new Claude Code login. `opened` is when its terminal first opened in this tab, which starts the server's 15 minutes. */
export type LoginSignIn = { id: string; name: string; opened?: number }
export type LoginSignInOutcome = { login: AgentLogin } | { refused: string }

/** The member's sign-in still running, kept while they move between pages, and the outcome of the last one that ended. */
export const useLoginSignIn = create<{ running: LoginSignIn | null; ended: { id: string; outcome: LoginSignInOutcome } | null }>(() => ({ running: null, ended: null }))

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useLoginSignIn.setState({ running: null, ended: null }, true)
})

export function rememberLoginSignIn(signIn: LoginSignIn) {
  const { running } = useLoginSignIn.getState()
  useLoginSignIn.setState({ running: running?.id === signIn.id ? { ...running, ...signIn } : signIn })
}

export function noteLoginSignInOpened(id: string) {
  const { running } = useLoginSignIn.getState()
  if (running?.id === id && running.opened === undefined) useLoginSignIn.setState({ running: { ...running, opened: Date.now() } })
}

const rewaitMs = 1000

/**
 * Waits on agent.login.result for the running sign-in, one wait at a time, until the server says how it ended.
 * A wait the server did not answer (the gateway's bound, a dropped hop, a failed fetch) is not an outcome: it waits again.
 */
export function useLoginSignInWait(client: Pick<Api, 'agentLoginResult'>, agent: string, onEnded: () => void) {
  const id = useLoginSignIn((s) => s.running?.id)
  const ended = useRef(onEnded)
  ended.current = onEnded
  useEffect(() => {
    if (!id) return
    const abort = new AbortController()
    const same = sameIdentity()
    let timer: ReturnType<typeof setTimeout> | undefined
    const end = (outcome: LoginSignInOutcome) => {
      if (abort.signal.aborted || !same()) return
      useLoginSignIn.setState({ running: null, ended: { id, outcome } })
      ended.current()
    }
    const wait = () => {
      client.agentLoginResult(agent, id, abort.signal).then(
        (login) => end({ login }),
        (err: unknown) => {
          if (abort.signal.aborted) return
          if (err instanceof ApiError && !unanswered(err)) end({ refused: message(err) })
          else timer = setTimeout(wait, rewaitMs)
        },
      )
    }
    wait()
    return () => {
      abort.abort()
      clearTimeout(timer)
    }
  }, [client, agent, id])
}
