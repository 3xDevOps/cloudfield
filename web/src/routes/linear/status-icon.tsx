import type { LinearIssue } from '@/lib/types'

/** Linear's own status glyph by state type, in the surrounding text colour: the badge stays neutral, so Cloudfield's session-state colours never describe an issue. */
export function LinearStatusIcon({ type }: { type: LinearIssue['state']['type'] }) {
  return (
    <svg aria-hidden viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5">
      <circle cx="7" cy="7" r="5.75" strokeDasharray={type === 'backlog' ? '2 2.2' : undefined} />
      {type === 'started' && <path d="M7 3.5a3.5 3.5 0 0 1 0 7Z" fill="currentColor" stroke="none" />}
      {type === 'completed' && <path d="m4.5 7.2 1.8 1.8 3.2-3.6" strokeLinecap="round" strokeLinejoin="round" />}
      {type === 'canceled' && <path d="m5 5 4 4m0-4-4 4" strokeLinecap="round" />}
    </svg>
  )
}
