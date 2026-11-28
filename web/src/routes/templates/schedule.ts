export type Repeat = 'daily' | 'weekly' | 'monthly' | 'cron'

/** The schedule editor's fields. `days` are cron weekdays (0 is Sunday), `at` is the time input's HH:MM. */
export type Plan = { repeat: Repeat; at: string; days: number[]; day: number; zone: string; rule: string }

export const weekdayNames = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
export const mondayFirst = [1, 2, 3, 4, 5, 6, 0]

const list = new Intl.ListFormat('en', { type: 'conjunction' })

function split(rule: string): { zone: string; body: string } {
  const zoned = /^(?:CRON_)?TZ=(\S+)\s+(.+)$/.exec(rule)
  return zoned ? { zone: zoned[1], body: zoned[2] } : { zone: 'UTC', body: rule }
}

function number(field: string, min: number, max: number): number | null {
  return /^\d{1,2}$/.test(field) && Number(field) >= min && Number(field) <= max ? Number(field) : null
}

function weekdays(field: string): number[] | null {
  const days = new Set<number>()
  for (const part of field.split(',')) {
    const range = /^([0-6])(?:-([0-6]))?$/.exec(part)
    if (!range) return null
    const [from, to] = [Number(range[1]), Number(range[2] ?? range[1])]
    if (from > to) return null
    for (let day = from; day <= to; day++) days.add(day)
  }
  return [...days].sort()
}

/** Reads a stored rule into the fields; a rule the fields cannot show exactly opens as cron. */
export function readRule(stored: string, browserZone: string): Plan {
  const rule = stored.trim()
  const fresh: Plan = { repeat: 'daily', at: '09:00', days: [1], day: 1, zone: browserZone, rule }
  if (!rule) return fresh
  const { zone, body } = split(rule)
  const asCron: Plan = { ...fresh, repeat: 'cron', zone }
  const fields = body.split(/\s+/)
  if (fields.length !== 5) return asCron
  const [m, h, dom, month, dow] = fields
  const minute = number(m, 0, 59)
  const hour = number(h, 0, 23)
  if (minute === null || hour === null || month !== '*') return asCron
  const at = `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`
  if (dom === '*' && dow === '*') return { ...asCron, repeat: 'daily', at }
  const days = dom === '*' ? weekdays(dow) : null
  if (days) return { ...asCron, repeat: 'weekly', at, days }
  const day = dow === '*' ? number(dom, 1, 28) : null
  if (day !== null) return { ...asCron, repeat: 'monthly', at, day }
  return asCron
}

/** The rule the server stores for these fields: five-field cron, prefixed with CRON_TZ= outside UTC. */
export function writeRule(plan: Plan): string {
  if (plan.repeat === 'cron') return plan.rule.trim()
  const [hour, minute] = plan.at.split(':').map(Number)
  const days = plan.days.join(',')
  const dow = plan.repeat !== 'weekly' || plan.days.length === 7 ? '*' : days === '1,2,3,4,5' ? '1-5' : days
  const body = `${minute} ${hour} ${plan.repeat === 'monthly' ? plan.day : '*'} * ${dow}`
  const zone = plan.zone.trim()
  return zone === 'UTC' ? body : `CRON_TZ=${zone} ${body}`
}

/** Plain words for a rule the fields can show; any other rule comes back as its cron text and zone. */
export function describeRule(rule: string): { words: string } | { cron: string; zone: string } {
  const plan = readRule(rule, 'UTC')
  if (plan.repeat === 'cron') return { cron: split(rule.trim()).body, zone: plan.zone }
  const days = plan.days.join(',')
  const when =
    plan.repeat === 'monthly'
      ? `monthly on day ${plan.day}`
      : plan.repeat === 'daily' || plan.days.length === 7
        ? 'every day'
        : days === '1,2,3,4,5'
          ? 'weekdays'
          : list.format(mondayFirst.filter((day) => plan.days.includes(day)).map((day) => `${weekdayNames[day]}s`))
  return { words: `${when} at ${plan.at} ${plan.zone}` }
}
