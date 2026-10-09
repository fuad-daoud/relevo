// Delivers `relevo push` lines into the MasterMind's session.
import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { StatusDoc, StatusRow } from '../types'

type Engine = EngineInterface

// The engine's `$` may not be passed around, so each call the module needs is
// wrapped where `$` is in scope and handed on as a function.
type Io = {
  run: Engine['process']['run']
  spawn: Engine['process']['spawn']
  submit: Engine['prompt']['submit']
  append: Engine['session']['append']
  log: (text: string) => void
  now: () => Promise<number>
  sleep: (ms: number) => Promise<void>
  every: Engine['clock']['every']
  store: (doc: StatusDoc) => Promise<void>
  toast: (text: string) => void
}

type PushEvent = {
  seq: number
  binding: string
  round: number
  kind: string
  state: string
  text: string
}

type Unread = { binding: string; round: number; kind: string; state: string }

type Ctx = {
  io: Io
  env: Record<string, string>
  busy: boolean
  unread: Unread[]
  delivered: Set<string>
  prev?: StatusDoc
}

const statusDoc = atom({ plugin: 'relevo', key: 'status' } as const, null)

const RESOLVE_RETRY_MS = 10_000
const POLL_MS = 5_000
const POLL_SLOW_MS = 30_000
const POLL_FAILS_BEFORE_SLOW = 3
const MAX_INBOX = 3
const AMBER = '#f0b452'
const TEAL = '#72c8d8'
const PHASE = '#aab4b0'
const DIM = '#8a9591'
const FAINT = '#66716d'
const DONE_GREEN = '#86d093'
const AMBER_TINT = '#2a2212'
const TEAL_TINT = '#12282d'
// Restart delay doubles from the floor to the cap; a child that ran for
// STABLE_MS counts as healthy and drops the delay back to the floor.
const BACKOFF_MIN_MS = 1_000
const BACKOFF_MAX_MS = 60_000
const STABLE_MS = 30_000
const DELIVER_TRIES = 3
const DELIVER_RETRY_MS = 500
// Ack waits 1, 2, 4 ... 32 s between its 7 tries: about a minute in all.
const ACK_TRIES = 7
const ACK_RETRY_MIN_MS = 1_000

function parseEvent(line: string): PushEvent | undefined {
  try {
    const v = JSON.parse(line) as Partial<PushEvent>
    if (
      typeof v.seq === 'number' &&
      typeof v.binding === 'string' &&
      typeof v.text === 'string'
    ) {
      return {
        seq: v.seq,
        binding: v.binding,
        round: typeof v.round === 'number' ? v.round : 0,
        kind: typeof v.kind === 'string' ? v.kind : '',
        state: typeof v.state === 'string' ? v.state : '',
        text: v.text,
      }
    }
  } catch {
    // not a push line
  }
  return undefined
}

function parseDoc(stdout: string): StatusDoc | undefined {
  try {
    const v = JSON.parse(stdout) as Partial<StatusDoc> | null
    const id = v?.mastermind?.id
    if (typeof id !== 'string' || id === '') return undefined
    return {
      ...v,
      mastermind: { id, name: typeof v?.mastermind?.name === 'string' ? v.mastermind.name : id },
      now: typeof v?.now === 'string' ? v.now : '',
      rows: Array.isArray(v?.rows) ? v.rows : [],
    }
  } catch {
    return undefined
  }
}

// The first document seen only seeds the diff: a toast is a change between two polls.
async function observe(ctx: Ctx, doc: StatusDoc): Promise<void> {
  const prev = ctx.prev
  ctx.prev = doc
  await ctx.io.store(doc)
  if (prev === undefined) return
  for (const text of toastTexts(prev, doc)) ctx.io.toast(text)
}

async function fetchDoc(io: Io, env: Record<string, string>): Promise<StatusDoc | undefined> {
  try {
    const r = await io.run(['relevo', 'status', '--line', '--json'], { env })
    return r.exitCode === 0 ? parseDoc(r.stdout) : undefined
  } catch {
    return undefined
  }
}

// A failed or stale poll keeps the last stored document. After
// POLL_FAILS_BEFORE_SLOW failures in a row the next try waits POLL_SLOW_MS,
// until one succeeds.
function startPoller(ctx: Ctx): void {
  let polling = false
  let fails = 0
  let nextAt = 0
  const tick = async (): Promise<void> => {
    if (polling) return
    polling = true
    try {
      const now = await ctx.io.now()
      if (now < nextAt) return
      const doc = await fetchDoc(ctx.io, ctx.env)
      if (doc !== undefined) {
        await observe(ctx, doc)
        fails = 0
        nextAt = 0
        return
      }
      fails++
      if (fails >= POLL_FAILS_BEFORE_SLOW) nextAt = now + POLL_SLOW_MS
    } catch (err) {
      ctx.io.log(`relevo status: poll failed: ${String(err)}`)
    } finally {
      polling = false
    }
  }
  ctx.io.every(POLL_MS, () => {
    void tick()
  })
}

// A refusal is a returned drop/deny, never a throw; both count as not delivered.
async function submitOnce(ctx: Ctx, ev: PushEvent): Promise<string | undefined> {
  const { io } = ctx
  if (!ctx.busy) {
    const r = await io.submit({ text: ev.text })
    return r.drop === undefined ? undefined : String(r.drop)
  }
  const r = await io.append({
    message: { type: 'user', content: [{ type: 'text', text: ev.text }] },
  })
  if (r.deny !== undefined) return r.deny
  ctx.unread.push({ binding: ev.binding, round: ev.round, kind: ev.kind, state: ev.state })
  return undefined
}

async function deliver(ctx: Ctx, ev: PushEvent): Promise<boolean> {
  for (let i = 0; i < DELIVER_TRIES; i++) {
    try {
      const refusal = await submitOnce(ctx, ev)
      if (refusal === undefined) return true
      ctx.io.log(`relevo push: ${ev.binding} #${ev.seq} refused: ${refusal}`)
    } catch (err) {
      ctx.io.log(`relevo push: ${ev.binding} #${ev.seq} failed: ${String(err)}`)
    }
    if (i < DELIVER_TRIES - 1) await ctx.io.sleep(DELIVER_RETRY_MS * 2 ** i)
  }
  return false
}

// False when the ack never landed: the caller ends the child so `relevo push`
// clears the admit and resends the line after the restart.
async function ack(ctx: Ctx, ev: PushEvent): Promise<boolean> {
  if (ev.seq === 0) return true
  const argv = ['relevo', 'push', '--ack', ev.binding, String(ev.seq), '--json']
  for (let i = 0; i < ACK_TRIES; i++) {
    try {
      const r = await ctx.io.run(argv, { env: ctx.env })
      if (r.exitCode === 0) return true
    } catch {
      // retried below
    }
    if (i < ACK_TRIES - 1) await ctx.io.sleep(ACK_RETRY_MIN_MS * 2 ** i)
  }
  ctx.io.log(`relevo push: ack of ${ev.binding} #${ev.seq} failed`)
  return false
}

// Returns when a line could not be delivered or acked: the caller ends the child.
async function consume(ctx: Ctx): Promise<void> {
  const child = ctx.io.spawn({ argv: ['relevo', 'push'], env: ctx.env })
  let buf = ''
  for await (const chunk of child) {
    if (chunk.stream !== 'stdout') continue
    buf += chunk.text
    const lines = buf.split('\n')
    buf = lines.pop() ?? ''
    for (const line of lines) {
      const ev = parseEvent(line)
      if (ev === undefined) continue
      const key = `${ev.binding}#${ev.seq}`
      if (ev.seq === 0 || !ctx.delivered.has(key)) {
        if (!(await deliver(ctx, ev))) return
        if (ev.seq > 0) ctx.delivered.add(key)
      }
      if (!(await ack(ctx, ev))) return
    }
  }
}

function nudgeText(unread: Unread[]): string {
  const states = unread.filter((u) => u.kind === 'state')
  const reports = unread.filter((u) => u.kind !== 'state')
  const parts: string[] = []
  if (reports.length > 0) {
    parts.push(`the report for ${reports.map((u) => `${u.binding} r${u.round}`).join(', ')} is above -- act on it`)
  }
  for (const u of states) {
    parts.push(`${u.binding} r${u.round} changed state (${u.state.toLowerCase()}) -- see above`)
  }
  return `relevo: ${parts.join('; ')}`
}

async function supervise(ctx: Ctx): Promise<void> {
  const { io } = ctx
  let delay = BACKOFF_MIN_MS
  try {
    for (;;) {
      const started = await io.now()
      try {
        await consume(ctx)
      } catch (err) {
        io.log(`relevo push: child failed: ${String(err)}`)
      }
      if ((await io.now()) - started >= STABLE_MS) delay = BACKOFF_MIN_MS
      await io.sleep(delay)
      delay = Math.min(delay * 2, BACKOFF_MAX_MS)
    }
  } catch {
    // the module unloaded: the pending wait was cancelled
  }
}

async function start(ctx: Ctx): Promise<void> {
  const sessionEnv = { CLAUDECODE: '1', CLAUDE_CODE_SESSION_ID: ctx.env.CLAUDE_CODE_SESSION_ID ?? '' }
  let resolving = false
  const attempt = async (): Promise<void> => {
    if (resolving) return
    resolving = true
    try {
      const doc = await fetchDoc(ctx.io, sessionEnv)
      if (doc === undefined) return
      timer.cancel()
      ctx.env = { ...sessionEnv, RELEVO_MASTERMIND: doc.mastermind.id }
      await observe(ctx, doc)
      startPoller(ctx)
      void supervise(ctx)
    } finally {
      resolving = false
    }
  }
  const timer = ctx.io.every(RESOLVE_RETRY_MS, () => {
    void attempt()
  })
  await attempt()
}

// Waiting on the MasterMind is the server's `tone`, not `needs_you`/`report_in`:
// a DONE-displayed row with an unconsumed report has tone `report` but
// `needs_you` false, and `tone` is the one signal the status line itself draws.
function isWaiting(r: StatusRow): boolean {
  return r.tone === 'needs' || r.tone === 'report'
}

function parseTs(ts: unknown): number {
  if (typeof ts !== 'string') return Number.NaN
  return Date.parse(ts.replace(/(\.\d{3})\d+/, '$1'))
}

// Oldest `last_ts` first; a row with no readable time sorts last.
function pickInbox(rows: StatusRow[], limit: number): { shown: StatusRow[]; more: number; rest: StatusRow[] } {
  const key = (r: StatusRow): number => {
    const t = parseTs(r.last_ts)
    return Number.isNaN(t) ? Number.POSITIVE_INFINITY : t
  }
  const waiting = rows
    .filter(isWaiting)
    .sort((a, b) => (key(a) === key(b) ? a.name.localeCompare(b.name) : key(a) < key(b) ? -1 : 1))
  const shown = waiting.slice(0, limit)
  const rest = rows.filter((r) => !isWaiting(r))
  return { shown, more: waiting.length - shown.length, rest }
}

function age(row: StatusRow, now: string): string {
  const s = Math.floor((parseTs(now) - parseTs(row.last_ts)) / 1000)
  if (Number.isNaN(s) || s < 0) return '--'
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

function detail(r: StatusRow): string {
  if (r.tone === 'report') {
    const l = r.live
    return l ? `delivered · +${l.added}/-${l.removed} in ${l.files}` : 'delivered'
  }
  return r.halt || r.reason
}

// A report row names the round that reported, not the binding's next round.
function shownRound(r: StatusRow): number {
  return r.tone === 'report' && r.report_round ? r.report_round : r.round
}

function stateWord(r: StatusRow, now: string): string {
  return `${r.status.toLowerCase()} · ${age(r, now)}`
}

type ChainBar = { done: string; current: string; rest: string; tail: string }

// done / total with the phase; the bar marks the plan in flight apart from the ones still to come.
function chainBar(r: StatusRow): ChainBar | undefined {
  const p = r.chain_progress
  if (!p || p.total <= 0) return undefined
  const done = Math.max(0, Math.min(p.done, p.total))
  const running = done < p.total ? 1 : 0
  return {
    done: '■'.repeat(done),
    current: '▣'.repeat(running),
    rest: '□'.repeat(p.total - done - running),
    tail: `${p.done}/${p.total}${p.phase ? ` ${p.phase}` : ''}`,
  }
}

function railWord(r: StatusRow): string {
  if (r.chain) return r.chain
  return r.activity ? r.activity : r.status
}

function fit(text: string, cols: number): string {
  if (cols <= 0) return ''
  return text.length <= cols ? text : `${text.slice(0, Math.max(0, cols - 1))}…`
}

function chainMoved(prev: StatusRow, cur: StatusRow): string | undefined {
  const a = prev.chain_progress
  const b = cur.chain_progress
  if (!a || !b || b.done <= a.done) return undefined
  const where = b.done >= b.total ? 'chain finished' : `chain moved to plan ${b.done + 1}/${b.total}`
  return `relevo: ${cur.name} ${where}`
}

function gateTexts(prev: StatusDoc, cur: StatusDoc): string[] {
  if (!Array.isArray(prev.gates) || !Array.isArray(cur.gates)) return []
  const before = new Set(prev.gates.map((g) => g.token))
  const after = new Set(cur.gates.map((g) => g.token))
  return [
    ...cur.gates
      .filter((g) => !before.has(g.token))
      .map((g) => `relevo: gate set on ${g.token} until ${g.until === '' ? 'cleared' : g.until}: ${g.reason}`),
    ...prev.gates.filter((g) => !after.has(g.token)).map((g) => `relevo: gate lifted on ${g.token}`),
  ]
}

// Never for an arriving report or question: only chain, candidate and gate changes toast.
function toastTexts(prev: StatusDoc, cur: StatusDoc): string[] {
  const out: string[] = []
  for (const r of cur.rows) {
    const p = prev.rows.find((x) => x.name === r.name)
    if (p === undefined) continue
    const moved = chainMoved(p, r)
    if (moved !== undefined) out.push(moved)
    if (p.candidate && r.candidate && p.candidate !== r.candidate) {
      out.push(`relevo: ${r.name} switched to ${r.candidate}`)
    }
  }
  return [...out, ...gateTexts(prev, cur)]
}

export const register: Register = (on) => {
  const ctx: Ctx = { io: undefined as unknown as Io, env: {}, busy: false, unread: [], delivered: new Set() }

  on('session.start', async ($, e, next) => {
    const started = await next(e)
    ctx.io = {
      run: (argv, init) => $.process.run(argv, init),
      spawn: (req) => $.process.spawn(req),
      submit: (args) => $.prompt.submit(args),
      append: (args) => $.session.append(args),
      log: (text) => $.ui.log(text),
      now: () => $.clock.now(),
      sleep: (ms) => $.clock.sleep(ms),
      every: (ms, fn) => $.clock.every(ms, fn),
      store: (doc) => update($, statusDoc, () => doc),
      toast: (text) => $.ui.toast(text),
    }
    ctx.env = { CLAUDE_CODE_SESSION_ID: await $.session.id() }
    void start(ctx)
    return started
  })

  on('turn.start', ($, e, next) => {
    ctx.busy = true
    ctx.unread = []
    return next(e)
  })

  // A step reads every row appended before it began.
  on('turn.step', async function* ($, e, next) {
    if (e.agentId === undefined) ctx.unread = []
    return yield* next(e)
  })

  // An append after the last step is never read: nudge once the turn ends.
  on('turn.complete', async ($, e, next) => {
    if (e.agentId !== undefined) return next(e)
    ctx.busy = false
    const unread = ctx.unread
    ctx.unread = []
    const result = await next(e)
    if (unread.length > 0) {
      const text = nudgeText(unread)
      void $.prompt.submit({ text }).catch((err: unknown) => {
        $.ui.log(`relevo push: nudge failed: ${String(err)}`)
      })
    }
    return result
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const doc = await read($, statusDoc)
    if (e.props.hasSurvey || doc === null || doc.rows.length === 0) return next(e)

    const { Box, Button, Text } = $.ui.resolve(e)
    const cols = e.props.bodyColumns
    const { shown, more, rest } = pickInbox(doc.rows, Math.max(0, Math.min(MAX_INBOX, e.props.maxRows - 2)))
    let digit = 0
    const press = (name: string) => () => {
      void $.prompt.fill({ text: `relevo show ${name} --report` })
    }
    // Returns the node and the columns it takes. A plain Button with a hotkey is drawn
    // as `1: label`, so the label must not repeat the digit.
    const button = (r: StatusRow, label: string, room: number) => {
      digit++
      const hotkey = digit <= 9 ? String(digit) : undefined
      const text = fit(label, hotkey === undefined ? room : Math.max(1, room - 3))
      const node = h(Button, { key: `b${r.name}`, plain: true, hotkey, label: text, onPress: press(r.name) })
      return { node, width: text.length + (hotkey === undefined ? 0 : 3) }
    }
    const txt = (text: string, props: object, room: number) =>
      h(Text, { wrap: 'truncate-end', ...props }, fit(text, room))

    const inbox = shown.map((r) => {
      const tone = r.tone === 'needs' ? AMBER : TEAL
      const tint = r.tone === 'needs' ? AMBER_TINT : TEAL_TINT
      let room = cols
      const head = button(r, `● ${r.name} r${shownRound(r)}`, room)
      room -= head.width + 1
      const state = fit(stateWord(r, doc.now), Math.max(0, room))
      room -= state.length + 1
      const nx = r.next
      const nextLabel = nx ? fit(`→ ${nx.label}`, Math.max(0, room)) : ''
      room -= nextLabel === '' ? 0 : nextLabel.length + 1
      const extra = fit(detail(r), Math.max(0, room))
      return h(
        Box,
        { key: `i${r.name}`, flexDirection: 'row', gap: 1, backgroundColor: tint },
        head.node,
        state === '' ? null : h(Text, { color: tone }, state),
        extra === '' ? null : h(Text, { color: DIM }, extra),
        nx && nextLabel !== ''
          ? h(Button, {
              key: `n${r.name}`,
              plain: true,
              label: nextLabel,
              onPress: () => {
                void $.prompt.fill({ text: nx.text })
              },
            })
          : null,
      )
    })

    const title = fit(`relevo · ${doc.mastermind.name}`, cols)
    const rail: unknown[] = []
    let used = title.length
    let hidden = 0
    for (const r of rest) {
      const room = cols - used - 2
      if (room < 4) {
        hidden++
        continue
      }
      const b = button(r, `○ ${r.name}`, room)
      const bar = chainBar(r)
      const wordRoom = Math.max(0, room - b.width - 1)
      const parts: unknown[] = [b.node]
      let width = b.width
      if (bar !== undefined) {
        const glyphs = bar.done.length + bar.current.length + bar.rest.length
        if (wordRoom >= glyphs) {
          parts.push(
            h(Text, { key: `d${r.name}`, color: DONE_GREEN }, bar.done),
            h(Text, { key: `c${r.name}`, bold: true }, bar.current),
            h(Text, { key: `r${r.name}`, color: FAINT }, bar.rest),
            txt(bar.tail, { color: PHASE }, wordRoom - glyphs - 1),
          )
          width += 1 + Math.min(wordRoom, glyphs + 1 + bar.tail.length)
        }
      } else {
        const word = fit(railWord(r), wordRoom)
        parts.push(h(Text, { color: PHASE }, word))
        width += 1 + word.length
      }
      used += 2 + width
      rail.push(h(Box, { key: `rail${r.name}`, flexDirection: 'row', gap: 1 }, ...parts))
    }

    const railRow = h(
      Box,
      { flexDirection: 'row', gap: 2 },
      h(Text, { wrap: 'truncate-end', dimColor: true, bold: true }, title),
      ...rail,
      hidden > 0 ? h(Text, { wrap: 'truncate-end', color: DIM }, `+${hidden}`) : null,
    )
    return h(
      Box,
      { flexDirection: 'column' },
      ...inbox,
      more > 0 ? txt(`+${more} more · /relevo:status`, { color: DIM }, cols) : null,
      railRow,
    )
  })
}
