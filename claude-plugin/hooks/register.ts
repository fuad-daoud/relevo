// Delivers `relevo push` lines into the MasterMind's session.
import type { EngineInterface, Register } from 'claude-code'

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
}

const RESOLVE_RETRY_MS = 10_000
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

async function resolveIdentity(
  io: Io,
  sessionEnv: Record<string, string>,
): Promise<string | undefined> {
  try {
    const r = await io.run(['relevo', 'status', '--line', '--json'], {
      env: sessionEnv,
    })
    if (r.exitCode !== 0) return undefined
    const id = (JSON.parse(r.stdout) as { mastermind?: { id?: unknown } })
      .mastermind?.id
    return typeof id === 'string' && id !== '' ? id : undefined
  } catch {
    return undefined
  }
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
      const id = await resolveIdentity(ctx.io, sessionEnv)
      if (id === undefined) return
      timer.cancel()
      ctx.env = { ...sessionEnv, RELEVO_MASTERMIND: id }
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
}
