import { useState } from 'react'
import { RequestAction, StaleReason } from '@/components/ui/request-choice'
import { useStale } from '@/store/hooks'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

export interface DecisionAnswers {
  /** Each answer's label, at rest and while it is sent. */
  approve: [string, string]
  deny: [string, string]
  /** A refusal is `danger`; putting something off is not. */
  denyVariant: 'danger' | 'secondary'
}

const approveOrDeny: DecisionAnswers = { approve: ['Approve', 'Approving…'], deny: ['Deny', 'Denying…'], denyVariant: 'danger' }

export function DecisionButtons({ decide, answers = approveOrDeny }: { decide: (approve: boolean) => Promise<unknown>; answers?: DecisionAnswers }) {
  const [pending, setPending] = useState<{ approve: boolean } | null>(null)
  // A painted snapshot may be minutes old: the request could be answered already.
  const stale = useStale()
  const late = usePastDeadline(pending !== null)
  const press = (approve: boolean) => {
    const mine = { approve }
    setPending(mine)
    void decide(approve).finally(() => setPending((current) => (current === mine ? null : current)))
  }
  const label = (approve: boolean) => {
    const [rest, sending] = approve ? answers.approve : answers.deny
    return stale || pending?.approve !== approve ? rest : late ? stillWaiting : sending
  }
  return (
    <>
      <StaleReason />
      <RequestAction label={label(false)} variant={answers.denyVariant} disabled={pending !== null} onClick={() => press(false)} />
      <RequestAction label={label(true)} variant="confirm" disabled={pending !== null} onClick={() => press(true)} />
    </>
  )
}
