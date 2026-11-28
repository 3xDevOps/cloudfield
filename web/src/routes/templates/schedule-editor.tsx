import { useId, useState } from 'react'
import { browserZone, clockTime, message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Input } from '@/components/ui/input'
import { FormField } from '@/components/ui/form-field'
import { Label } from '@/components/ui/label'
import { RadioPills, RadioPillsItem } from '@/components/ui/radio-pills'
import { RelativeTime } from '@/components/ui/relative-time'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api, type Api } from '@/lib/api'
import type { Schedule } from '@/lib/types'
import { mondayFirst, readRule, weekdayNames, writeRule, zoneName, zones, type Plan, type Repeat } from '@/routes/templates/schedule'
import type { Failure } from '@/routes/templates/template-form'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

const repeats: [Repeat, string][] = [['daily', 'Daily'], ['weekly', 'Weekly'], ['monthly', 'Monthly'], ['cron', 'Cron']]

export function ScheduleEditor({
  workspaceID,
  template,
  schedule,
  draft,
  failure,
  saving,
  client = api,
  onSave,
  onRetry,
  onChanged,
}: {
  workspaceID: string
  template: string
  schedule?: Schedule
  draft?: string
  failure?: Failure
  saving: boolean
  client?: Api
  onSave: (cron: string) => void
  onRetry: (cron: string) => void
  onChanged: () => void
}) {
  const id = useId()
  const [plan, setPlan] = useState(() => readRule(draft ?? schedule?.cron ?? '', browserZone()))
  const [typedCron, setTypedCron] = useState(false)
  const [busy, setBusy] = useState(false)
  const late = usePastDeadline(busy || saving)
  const [error, setError] = useState<string | null>(null)
  const set = (fields: Partial<Plan>) => setPlan((current) => ({ ...current, ...fields }))
  const kept = schedule ? readRule(schedule.cron, '').zone : ''
  const zone = zoneName(plan.zone, kept)
  // A zone refused on opening, such as a misspelled draft, says why at once.
  const [zoneLeft, setZoneLeft] = useState(!zone)
  const rule = writeRule(zone ? { ...plan, zone } : plan)
  const ready = plan.repeat === 'cron' ? Boolean(rule) : Boolean(plan.at && zone && (plan.repeat !== 'weekly' || plan.days.length))
  const saved = schedule && writeRule(readRule(schedule.cron, '')) === rule

  const remove = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.scheduleDelete(workspaceID, template)
      setPlan(readRule('', browserZone()))
      onChanged()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <form
        className="flex min-w-0 flex-col gap-3"
        aria-label={`Schedule for ${template}`}
        onSubmit={(e) => {
          e.preventDefault()
          onSave(rule)
        }}
      >
        <div className="flex flex-col gap-1">
          <span id={`${id}-repeats`} className="text-ui font-medium text-text">Repeats</span>
          <RadioPills
            aria-labelledby={`${id}-repeats`}
            value={plan.repeat}
            onValueChange={(value) => {
              const repeat = value as Repeat
              const typed = readRule(plan.rule, plan.zone)
              // A rule typed in Cron carries into the fields when they can show it; one only Cron can show stays there.
              if (plan.repeat === 'cron' && typedCron && plan.rule.trim() && typed.repeat !== 'cron') {
                setPlan({ ...typed, repeat, rule: plan.rule })
                if (!zoneName(typed.zone, kept)) setZoneLeft(true)
              } else set(repeat === 'cron' && ready && typed.repeat !== 'cron' ? { repeat, rule } : { repeat })
              setTypedCron(false)
            }}
          >
            {repeats.map(([value, label]) => <RadioPillsItem key={value} value={value}>{label}</RadioPillsItem>)}
          </RadioPills>
        </div>
        {plan.repeat === 'weekly' && (
          <div role="group" aria-labelledby={`${id}-on`} className="flex flex-col gap-1">
            <span id={`${id}-on`} className="text-ui font-medium text-text">On</span>
            <div className="flex flex-wrap gap-x-4 gap-y-2 coarse:gap-y-6">
              {mondayFirst.map((day) => (
                <div key={day} className="flex items-center gap-2">
                  <Checkbox
                    id={`${id}-day-${day}`}
                    aria-label={weekdayNames[day]}
                    checked={plan.days.includes(day)}
                    onCheckedChange={(on) => set({ days: on === true ? [...plan.days, day].sort() : plan.days.filter((d) => d !== day) })}
                  />
                  <Label htmlFor={`${id}-day-${day}`} className="font-normal">{weekdayNames[day].slice(0, 3)}</Label>
                </div>
              ))}
            </div>
            {plan.days.length === 0 && <p role="status" className="text-ui-sm text-state-failed">Pick at least one day.</p>}
          </div>
        )}
        {plan.repeat === 'monthly' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor={`${id}-day`}>Day of the month</Label>
            <Select value={String(plan.day)} onValueChange={(value) => set({ day: Number(value) })}>
              <SelectTrigger id={`${id}-day`} aria-describedby={`${id}-day-help`} className="w-28">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {Array.from({ length: 28 }, (_, i) => <SelectItem key={i} value={String(i + 1)}>{i + 1}</SelectItem>)}
              </SelectContent>
            </Select>
            <p id={`${id}-day-help`} className="text-ui-sm text-muted">For days 29 to 31, use Cron: months without that day are skipped.</p>
          </div>
        )}
        {plan.repeat === 'cron' ? (
          <FormField
            label="Cron rule"
            help={<>Five fields: minute hour day month weekday, such as <code className="font-code whitespace-nowrap">30 9 * * 1-5</code>, or <code className="font-code whitespace-nowrap">@hourly</code>. UTC unless it starts with <code className="font-code whitespace-nowrap">CRON_TZ=&lt;zone&gt;</code> and a space.</>}
          >
            <Input
              placeholder="0 3 * * *"
              value={plan.rule}
              spellCheck={false}
              onChange={(e) => {
                setTypedCron(true)
                set({ rule: e.target.value })
              }}
              className="font-code"
            />
          </FormField>
        ) : (
          <>
            <div className="flex min-w-0 flex-wrap gap-3">
              <FormField label="At">
                <Input type="time" required value={plan.at} className="w-28" onChange={(e) => set({ at: e.target.value })} />
              </FormField>
              <FormField
                label="Time zone"
                help="An IANA zone, such as Europe/Berlin."
                error={
                  zoneLeft && !zone
                    ? !plan.zone.trim()
                      ? 'Enter a time zone.'
                      : plan.zone.includes('/')
                        ? 'Not an IANA zone name. Check the spelling.'
                        : 'Use a full zone name such as America/Los_Angeles.'
                    : undefined
                }
                className="min-w-0 flex-1 basis-40"
              >
                <Input required value={plan.zone} spellCheck={false} list={`${id}-zones`} onBlur={() => setZoneLeft(true)} onKeyDown={(e) => e.key === 'Enter' && setZoneLeft(true)} onChange={(e) => set({ zone: e.target.value })} />
              </FormField>
              <datalist id={`${id}-zones`}>
                {zones.map((zone) => <option key={zone} value={zone} />)}
              </datalist>
            </div>
          </>
        )}
        {((ready && plan.repeat !== 'cron') || (saved && schedule.next_fire_at)) && (
          <div className="flex min-w-0 flex-col gap-1 text-ui-sm text-muted">
            {ready && plan.repeat !== 'cron' && <p>Saves as <code className="font-code break-all">{rule}</code></p>}
            {saved && schedule.next_fire_at && <p>Next launch <RelativeTime at={schedule.next_fire_at} /> ({clockTime(schedule.next_fire_at)} your time)</p>}
          </div>
        )}
        {error && (
          <p role="alert" className="text-ui-sm text-state-failed">
            {error}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          {schedule && (
            <Button type="button" variant="ghost" disabled={busy || saving} onClick={() => void remove()}>
              {late ? stillWaiting : busy ? 'Unscheduling…' : saving ? 'Saving…' : 'Unschedule'}
            </Button>
          )}
          <Button type="submit" disabled={busy || !ready}>
            {schedule ? 'Update schedule' : 'Schedule'}
          </Button>
        </div>
      </form>
      {failure && <FailureCallout error={failure.error} onRetry={failure.unanswered && ready ? () => onRetry(rule) : undefined} />}
    </div>
  )
}
