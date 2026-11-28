import { describeRule, readRule, writeRule, type Plan, zoneName } from '@/routes/templates/schedule'

const clock = (at: string) => new Date(`2000-01-01T${at}:00Z`).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', timeZone: 'UTC' })

const plan = (fields: Partial<Plan>): Plan => ({ repeat: 'daily', at: '09:00', days: [1], day: 1, zone: 'UTC', rule: '', ...fields })

describe('schedule rules', () => {
  it.each([
    ['0 3 * * *', { repeat: 'daily', at: '03:00', zone: 'UTC' }, `every day at ${clock('03:00')} UTC`],
    ['30 9 * * 1-5', { repeat: 'weekly', at: '09:30', days: [1, 2, 3, 4, 5] }, `weekdays at ${clock('09:30')} UTC`],
    ['0 9 * * 1,3', { repeat: 'weekly', days: [1, 3] }, `Mondays and Wednesdays at ${clock('09:00')} UTC`],
    ['0 18 * * 0,5,6', { repeat: 'weekly', at: '18:00', days: [0, 5, 6] }, `Fridays, Saturdays, and Sundays at ${clock('18:00')} UTC`],
    ['0 9 * * 0-6', { repeat: 'weekly', days: [0, 1, 2, 3, 4, 5, 6] }, `every day at ${clock('09:00')} UTC`],
    ['CRON_TZ=Europe/Berlin 0 9 1 * *', { repeat: 'monthly', day: 1, zone: 'Europe/Berlin' }, `monthly on day 1 at ${clock('09:00')} Europe/Berlin`],
    ['TZ=America/New_York 05 7 * * *', { repeat: 'daily', at: '07:05', zone: 'America/New_York' }, `every day at ${clock('07:05')} America/New_York`],
  ])('reads %s into the fields and describes it', (rule, fields, words) => {
    expect(readRule(rule, 'Asia/Tokyo')).toMatchObject(fields)
    expect(describeRule(rule)).toEqual({ words })
  })

  it.each([
    ['*/15 * * * *', '*/15 * * * *', 'UTC'],
    ['@daily', '@daily', 'UTC'],
    ['0 9 31 * *', '0 9 31 * *', 'UTC'],
    ['0 9 * * MON-FRI', '0 9 * * MON-FRI', 'UTC'],
    ['0 9 1 * 1', '0 9 1 * 1', 'UTC'],
    ['0 9 * 1 *', '0 9 * 1 *', 'UTC'],
    ['CRON_TZ=Europe/Berln 0 9-17 * * *', '0 9-17 * * *', 'Europe/Berln'],
    ['CRON_TZ=Europe/Berlin', 'CRON_TZ=Europe/Berlin', 'UTC'],
    ['TZ=UTC\t0 9 * * *', '9 * * *', 'UTC\t0'],
  ])('keeps %s as cron text', (rule, cron, zone) => {
    expect(readRule(rule, 'Asia/Tokyo')).toMatchObject({ repeat: 'cron', rule, zone })
    expect(describeRule(rule)).toEqual({ cron, zone })
  })

  it('keeps the zone the server took, saves any other Area/Location name in the browser spelling, and refuses abbreviations', () => {
    expect(zoneName('Asia/Kolkata', 'Asia/Kolkata')).toBe('Asia/Kolkata')
    expect(zoneName('EST', 'EST')).toBe('EST')
    expect(zoneName(' europe/berlin ', '')).toBe('Europe/Berlin')
    expect(zoneName('utc', '')).toBe('UTC')
    expect(zoneName('PST', '')).toBeNull()
    expect(zoneName('BST', '')).toBeNull()
    expect(['Asia/Kolkata', 'Asia/Calcutta']).toContain(zoneName('asia/kolkata', ''))
    expect(zoneName('Europe/Berln', '')).toBeNull()
    expect(zoneName('+05:30', '')).toBeNull()
    expect(zoneName('', '')).toBeNull()
  })

  it('starts a new schedule daily at 09:00 in the browser zone', () => {
    expect(readRule('', 'Asia/Tokyo')).toEqual(plan({ zone: 'Asia/Tokyo' }))
  })

  it.each([
    [plan({}), '0 9 * * *'],
    [plan({ repeat: 'weekly', days: [1, 2, 3, 4, 5], at: '08:30', zone: 'Europe/Berlin' }), 'CRON_TZ=Europe/Berlin 30 8 * * 1-5'],
    [plan({ repeat: 'weekly', days: [0, 6] }), '0 9 * * 0,6'],
    [plan({ repeat: 'weekly', days: [0, 1, 2, 3, 4, 5, 6] }), '0 9 * * *'],
    [plan({ repeat: 'monthly', day: 28, at: '23:59' }), '59 23 28 * *'],
    [plan({ repeat: 'cron', rule: ' CRON_TZ=Asia/Tokyo */5 * * * * ' }), 'CRON_TZ=Asia/Tokyo */5 * * * *'],
  ])('writes %o as %s, which reads back the same', (fields, rule) => {
    expect(writeRule(fields)).toBe(rule)
    expect(writeRule(readRule(rule, 'Asia/Tokyo'))).toBe(rule)
  })
})
