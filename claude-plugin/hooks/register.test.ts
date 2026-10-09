import { describe, expect, mock, test } from 'claude-code/testing'

const RUN_OK = { exitCode: 0, stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false }
const STATUS_OK = { ...RUN_OK, stdout: '{"mastermind":{"id":"mm-1"}}' }

type Opts = {
  status?: 'ok' | 'fail' | 'reject'
  submit?: (text: string) => unknown
  deny?: boolean
  ackExit?: () => number
  // Overrides the status answer: a document, or 'fail' for a non-zero exit.
  doc?: () => object | 'fail'
  // Holds a poll (a status run under the resolved env) until it settles.
  hold?: () => Promise<void>
}

// Fakes sit beneath the plugin: every call it makes lands here and is recorded.
function fakes(on: any, opts: Opts = {}) {
  const clock = mock.clock(on)
  const session = opts.deny ? undefined : mock.session(on)
  const log: string[] = []
  const runs: { argv: readonly string[]; env?: Record<string, string> }[] = []
  const acks = () => runs.filter((r) => r.argv[2] === '--ack')
  const statuses = () => runs.filter((r) => r.argv[1] === 'status')
  const submits: string[] = []
  const appends = () => (session?.appended() ?? []).map((r: any) => r.message.content[0].text)
  const spawns: any[] = []
  const fills: string[] = []
  const toasts: string[] = []
  const pipes: { push: (t: string) => void; end: () => void }[] = []

  on('session.id', () => ({ value: 'sess-1' }))
  on('session.start', () => ({ cwd: '/w' }))
  on('ui.log', ($: any, e: any) => {
    log.push(e.text)
    return { value: undefined }
  })
  on('process.run', async ($: any, e: any) => {
    runs.push({ argv: e.argv, env: e.init?.env })
    if (e.argv[1] === 'status') {
      if (opts.status === 'reject') throw new Error('ENOENT')
      if (e.init?.env?.RELEVO_MASTERMIND) await opts.hold?.()
      if (opts.doc) {
        const d = opts.doc()
        if (d === 'fail') return { value: { ...RUN_OK, exitCode: 1 } }
        return { value: { ...RUN_OK, stdout: JSON.stringify(d) } }
      }
      return { value: opts.status === 'fail' ? { ...RUN_OK, exitCode: 1 } : STATUS_OK }
    }
    return { value: { ...RUN_OK, exitCode: opts.ackExit?.() ?? 0 } }
  })
  on('process.spawn', async function* ($: any, e: any) {
    spawns.push(e)
    const q: string[] = []
    let wake: (() => void) | undefined
    let done = false
    pipes.push({
      push: (t) => {
        q.push(t)
        wake?.()
      },
      end: () => {
        done = true
        wake?.()
      },
    })
    for (;;) {
      if (q.length > 0) {
        yield { stream: 'stdout', text: q.shift() as string }
        continue
      }
      if (done) return { value: { code: 0, signal: null } }
      await new Promise<void>((r) => {
        wake = r
      })
    }
  })
  on('ui.toast', ($: any, e: any) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('prompt.fill', ($: any, e: any) => {
    fills.push(e.text)
    return { isFilled: true, draft: e.text }
  })
  on('prompt.submit', async ($: any, e: any) => {
    const out = await (opts.submit?.(e.text) ?? { text: e.text })
    if (!(out as any).drop) submits.push(e.text)
    return out
  })
  if (opts.deny) on('session.append', () => ({ deny: 'refused' }))
  on('ui.render', () => ({ type: 'Text', props: {}, children: ['engine row'] }))
  on('turn.start', ($: any, e: any) => ({ turnId: e.turnId }))
  on('turn.step', async function* ($: any, e: any) {
    return { turnId: e.turnId, index: e.index, answer: '', toolUses: [] }
  })
  on('turn.complete', () => ({ text: '' }))
  return { clock, log, runs, acks, statuses, fills, toasts, submits, appends, spawns, pipes }
}

const line = (o: object) => JSON.stringify(o) + '\n'
const report = (seq: number, text = 'report text') =>
  line({ seq, binding: 'b1', round: 2, kind: 'report', text })
const startSession = ($: any) => $.session.start({ cwd: '/w', surface: null, isInteractive: false })
const startTurn = ($: any, turnId = 't1') => $.turn.start({ text: 'hi', turnId })
const endTurn = ($: any, extra: object = {}) =>
  $.turn.complete({ answer: 'x', durationMs: 1, isAborted: false, turnId: 't1', reason: 'answer', ...extra })
const step = async ($: any, extra: object = {}) => {
  for await (const _ of $.turn.step({ turnId: 't1', index: 0, model: 'm', messageCount: 1, ...extra })) void _
}

describe('identity', () => {
  for (const status of ['fail', 'reject'] as const) {
    test(`not a MasterMind (${status}): no spawn, retries every 10 s, no error surfaced`, async ($, on) => {
      const f = fakes(on, { status })
      await startSession($)
      await f.clock.settle()
      expect(f.statuses()).toHaveLength(1)
      await f.clock.advance(10_000)
      expect(f.statuses()).toHaveLength(2)
      await f.clock.advance(10_000)
      expect(f.statuses()).toHaveLength(3)
      expect(f.spawns).toHaveLength(0)
      expect(f.log).toHaveLength(0)
    })
  }

  test('resolved: spawns relevo push with RELEVO_MASTERMIND, no input, and stops retrying', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await f.clock.settle()
    expect(f.statuses()[0]?.argv).toEqual(['relevo', 'status', '--line', '--json'])
    expect(f.statuses()[0]?.env).toEqual({ CLAUDECODE: '1', CLAUDE_CODE_SESSION_ID: 'sess-1' })
    expect(f.spawns).toHaveLength(1)
    expect(f.spawns[0].argv).toEqual(['relevo', 'push'])
    expect(f.spawns[0].input).toBeUndefined()
    expect(f.spawns[0].env).toEqual({ RELEVO_MASTERMIND: 'mm-1', CLAUDECODE: '1', CLAUDE_CODE_SESSION_ID: 'sess-1' })
    await f.clock.advance(60_000)
    expect(f.statuses().filter((r) => r.env?.RELEVO_MASTERMIND === undefined)).toHaveLength(1)
  })
})

describe('supervisor', () => {
  test('child exit restarts the child with backoff', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.end()
    await f.clock.settle()
    expect(f.spawns).toHaveLength(1)
    await f.clock.advance(1_000)
    expect(f.spawns).toHaveLength(2)
    f.pipes[1]?.end()
    await f.clock.advance(1_000)
    expect(f.spawns).toHaveLength(2)
    await f.clock.advance(1_000)
    expect(f.spawns).toHaveLength(3)
  })

  test('backoff is capped at 60 s and resets after a child ran 30 s', { timeoutMs: 120_000 }, async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await f.clock.settle()
    // delays 1, 2, 4, 8, 16, 32 then the cap
    for (const delay of [1, 2, 4, 8, 16, 32, 60, 60]) {
      const before = f.spawns.length
      f.pipes[before - 1]?.end()
      await f.clock.advance(delay * 1_000 - 1)
      expect(f.spawns).toHaveLength(before)
      await f.clock.advance(1)
      expect(f.spawns).toHaveLength(before + 1)
    }
    const before = f.spawns.length
    await f.clock.advance(30_000)
    f.pipes[before - 1]?.end()
    await f.clock.advance(1_000)
    expect(f.spawns).toHaveLength(before + 1)
  })
})

describe('delivery', () => {
  test('idle line: one submit of the verbatim text, then one ack', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(7, '  verbatim\ntext  '.replaceAll('\n', ' ')))
    await f.clock.settle()
    expect(f.submits).toEqual(['  verbatim text  '])
    expect(f.appends()).toHaveLength(0)
    expect(f.acks()).toHaveLength(1)
    expect(f.acks()[0]?.argv).toEqual(['relevo', 'push', '--ack', 'b1', '7', '--json'])
    expect(f.acks()[0]?.env?.RELEVO_MASTERMIND).toBe('mm-1')
  })

  test('mid-turn line: one append, then one ack, no submit', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await startTurn($)
    await f.clock.settle()
    f.pipes[0]?.push(report(3))
    await f.clock.settle()
    expect(f.appends()).toEqual(['report text'])
    expect(f.submits).toHaveLength(0)
    expect(f.acks()).toHaveLength(1)
  })

  test('a line split across chunks and two lines in one chunk deliver once each', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await f.clock.settle()
    const whole = report(1, 'one')
    f.pipes[0]?.push(whole.slice(0, 10))
    await f.clock.settle()
    expect(f.submits).toHaveLength(0)
    f.pipes[0]?.push(whole.slice(10) + report(2, 'two'))
    await f.clock.settle()
    expect(f.submits).toEqual(['one', 'two'])
    expect(f.acks()).toHaveLength(2)
  })

  test('a subagent turn does not make the main loop idle or busy', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await startTurn($)
    await endTurn($, { agentId: 'sub-1' })
    await f.clock.settle()
    f.pipes[0]?.push(report(1))
    await f.clock.settle()
    expect(f.appends()).toHaveLength(1)
    expect(f.submits).toHaveLength(0)
  })

  test('state line (seq 0) is delivered and never acked', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(line({ seq: 0, binding: 'b1', round: 2, kind: 'state', text: 'b1 is DONE', state: 'DONE', old_state: 'RUNNING' }))
    await f.clock.settle()
    expect(f.submits).toEqual(['b1 is DONE'])
    expect(f.acks()).toHaveLength(0)
  })
})

describe('ack gate', () => {
  test('no ack until the submit resolved', async ($, on) => {
    let release: () => void = () => {}
    const f = fakes(on, {
      submit: (text) =>
        new Promise((r) => {
          release = () => r({ text })
        }),
    })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(5))
    await f.clock.settle()
    expect(f.acks()).toHaveLength(0)
    release()
    await f.clock.settle()
    expect(f.acks()).toHaveLength(1)
  })

  test('ack succeeding on the third try: one delivery, one ack, child keeps running', async ($, on) => {
    let n = 0
    const f = fakes(on, { ackExit: () => (++n < 3 ? 1 : 0) })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(5))
    await f.clock.settle()
    expect(f.acks()).toHaveLength(1)
    await f.clock.advance(1_000)
    expect(f.acks()).toHaveLength(2)
    await f.clock.advance(2_000)
    expect(f.acks()).toHaveLength(3)
    await f.clock.advance(120_000)
    expect(f.submits).toEqual(['report text'])
    expect(f.acks()).toHaveLength(3)
    expect(f.log).toHaveLength(0)
    expect(f.spawns).toHaveLength(1)
  })

  test('an ack that never succeeds ends the child; the resent line is only acked', async ($, on) => {
    let fail = true
    const f = fakes(on, { ackExit: () => (fail ? 1 : 0) })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(5))
    await f.clock.settle()
    await f.clock.advance(63_000)
    expect(f.acks()).toHaveLength(7)
    expect(f.log).toHaveLength(1)
    expect(f.submits).toEqual(['report text'])
    await f.clock.advance(1_000)
    expect(f.spawns).toHaveLength(2)
    fail = false
    f.pipes[1]?.push(report(5))
    await f.clock.settle()
    expect(f.submits).toEqual(['report text'])
    expect(f.appends()).toHaveLength(0)
    expect(f.acks()).toHaveLength(8)
  })
})

describe('delivery retry', () => {
  test('a refusal then success delivers once and waits between tries', async ($, on) => {
    let n = 0
    const f = fakes(on, { submit: (text) => (++n < 2 ? { drop: 'busy' } : { text }) })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(5))
    await f.clock.settle()
    expect(n).toBe(1)
    expect(f.acks()).toHaveLength(0)
    await f.clock.advance(500)
    expect(n).toBe(2)
    expect(f.submits).toEqual(['report text'])
    expect(f.acks()).toHaveLength(1)
  })
})

describe('refusal', () => {
  test('a denied append: no ack, logged, child ended, restart re-delivers', async ($, on) => {
    const f = fakes(on, { deny: true })
    await startSession($)
    await startTurn($)
    await f.clock.settle()
    f.pipes[0]?.push(report(9))
    await f.clock.settle()
    await f.clock.advance(1_500)
    expect(f.acks()).toHaveLength(0)
    expect(f.log.length).toBeGreaterThan(0)
    expect(f.log[0]).toContain('refused')
    await f.clock.advance(1_000)
    expect(f.spawns).toHaveLength(2)
    f.pipes[1]?.push(report(9))
    await f.clock.settle()
    await f.clock.advance(1_500)
    expect(f.log.length).toBeGreaterThan(3)
  })

  test('a dropped submit: no ack, logged, child ended', async ($, on) => {
    const f = fakes(on, { submit: () => ({ drop: 'blocked' }) })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(9))
    await f.clock.settle()
    await f.clock.advance(1_500)
    expect(f.acks()).toHaveLength(0)
    expect(f.log[0]).toContain('blocked')
    await f.clock.advance(1_000)
    expect(f.spawns).toHaveLength(2)
  })

  test('a rejected submit: no ack, logged', async ($, on) => {
    const f = fakes(on, {
      submit: () => {
        throw new Error('boom')
      },
    })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(9))
    await f.clock.settle()
    expect(f.acks()).toHaveLength(0)
    expect(f.log[0]).toContain('failed')
  })
})

describe('unread append nudge', () => {
  test('append then turn.complete with no step between: exactly one nudge submit', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await startTurn($)
    await f.clock.settle()
    f.pipes[0]?.push(report(4))
    await f.clock.settle()
    await endTurn($)
    await f.clock.settle()
    expect(f.submits).toEqual(['relevo: the report for b1 r2 is above -- act on it'])
    expect(f.acks()).toHaveLength(1)
  })

  test('append followed by a step: no nudge', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await startTurn($)
    await f.clock.settle()
    f.pipes[0]?.push(report(4))
    await f.clock.settle()
    await step($)
    await endTurn($)
    await f.clock.settle()
    expect(f.submits).toHaveLength(0)
  })

  test('a subagent step does not count as the main loop reading the row', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await startTurn($)
    await f.clock.settle()
    f.pipes[0]?.push(report(4))
    await f.clock.settle()
    await step($, { agentId: 'sub-1' })
    await endTurn($)
    await f.clock.settle()
    expect(f.submits).toHaveLength(1)
  })
})

describe('state nudge wording', () => {
  test('a state line says the binding changed state, not report', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await startTurn($)
    await f.clock.settle()
    f.pipes[0]?.push(line({ seq: 0, binding: 'b1', round: 2, kind: 'state', state: 'NEEDS_YOU', text: 'b1 needs you' }))
    await f.clock.settle()
    await endTurn($)
    await f.clock.settle()
    expect(f.submits).toEqual(['relevo: b1 r2 changed state (needs_you) -- see above'])
  })

  test('a report line keeps the report wording', async ($, on) => {
    const f = fakes(on)
    await startSession($)
    await startTurn($)
    await f.clock.settle()
    f.pipes[0]?.push(report(4))
    await f.clock.settle()
    await endTurn($)
    await f.clock.settle()
    expect(f.submits).toEqual(['relevo: the report for b1 r2 is above -- act on it'])
  })
})

const NOW = '2026-10-09T12:00:00Z'
const row = (name: string, o: object = {}) => ({
  name,
  round: 1,
  status: 'working',
  tone: 'phase',
  reason: '',
  last_ts: '2026-10-09T11:00:00Z',
  ...o,
})
const mkdoc = (rows: object[]) => ({ mastermind: { id: 'mm-1', name: 'main' }, now: NOW, rows })
const PROPS = { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 80, scroll: { bodyRows: 9 }, view: {} }
const SURFACES = ['terminal', 'desktop'] as const
const mount = ($: any, surface: string, props: object = {}) =>
  $.ui.mount({ plugin: 'relevo', surface, component: 'AbovePrompt', props: { ...PROPS, ...props } })
const labels = async (ui: any) => (await ui.findAll({ type: 'Button' })).map((b: any) => b.text as string)
const texts = async (ui: any) => (await ui.findAll({ type: 'Text' })).map((b: any) => b.text as string)

describe('poller', () => {
  test('first poll comes 5 s after resolve, with the resolved env', async ($, on) => {
    const f = fakes(on, { doc: () => mkdoc([]) })
    await startSession($)
    await f.clock.settle()
    const polls = () => f.statuses().filter((r) => r.env?.RELEVO_MASTERMIND === 'mm-1')
    expect(polls()).toHaveLength(0)
    await f.clock.advance(5_000)
    expect(polls()).toHaveLength(1)
    await f.clock.advance(5_000)
    expect(polls()).toHaveLength(2)
  })

  test('no poll before identity resolves', async ($, on) => {
    const f = fakes(on, { status: 'fail' })
    await startSession($)
    await f.clock.advance(4_000)
    expect(f.statuses().every((r) => r.env?.RELEVO_MASTERMIND === undefined)).toBe(true)
  })

  test('backs off to 30 s after 3 failures and resets on one success', async ($, on) => {
    let healthy = true
    const f = fakes(on, { doc: () => (healthy ? mkdoc([]) : 'fail') })
    await startSession($)
    await f.clock.settle()
    const polls = () => f.statuses().filter((r) => r.env?.RELEVO_MASTERMIND === 'mm-1').length
    healthy = false
    await f.clock.advance(15_000)
    expect(polls()).toBe(3)
    await f.clock.advance(25_000)
    expect(polls()).toBe(3)
    healthy = true
    await f.clock.advance(5_000)
    expect(polls()).toBe(4)
    await f.clock.advance(5_000)
    expect(polls()).toBe(5)
  })

  test('does not overlap a poll still in flight', async ($, on) => {
    let release: (() => void) | undefined
    const f = fakes(on, {
      doc: () => mkdoc([]),
      hold: () =>
        new Promise<void>((r) => {
          release = r
        }),
    })
    await startSession($)
    await f.clock.settle()
    await f.clock.advance(5_000)
    await f.clock.advance(10_000)
    expect(f.statuses().filter((r) => r.env?.RELEVO_MASTERMIND === 'mm-1').length).toBe(1)
    release?.()
  })
})

describe('band', () => {
  for (const surface of SURFACES) {
    test(`${surface}: one waiting and one running draw one inbox row and one rail item, never both`, async ($, on) => {
      const f = fakes(on, {
        doc: () =>
          mkdoc([
            row('w1', { tone: 'needs', status: 'NEEDS YOU', reason: 'gate hit', round: 2, last_ts: '2026-10-09T11:55:00Z' }),
            row('r1', { status: 'prompt sent', activity: 'working' }),
          ]),
      })
      await startSession($)
      await f.clock.settle()
      const ui = await mount($, surface)
      const all = await labels(ui)
      expect(all).toEqual(['● w1 r2', '○ r1'])
      const t = await texts(ui)
      expect(t).toEqual(expect.arrayContaining(['needs you · 5m', 'gate hit', 'working', 'relevo · main']))
      expect(t.filter((x) => x.includes('w1') || x.includes('r1'))).toHaveLength(0)
    })
  }

  test('the label never repeats the hotkey digit the surface draws beside it', async ($, on) => {
    const f = fakes(on, {
      doc: () => mkdoc([row('w1', { tone: 'needs', status: 'NEEDS YOU', reason: 'gate hit' }), row('r1', { activity: 'working' })]),
    })
    await startSession($)
    await f.clock.settle()
    const buttons = await (await mount($, 'terminal')).findAll({ type: 'Button' })
    expect(buttons.map((b: any) => b.props.hotkey)).toEqual(['1', '2'])
    for (const b of buttons) expect(String(b.props.label)).not.toMatch(/^\d+:/)
  })

  test('calm: every binding sits on the rail, status is the word without activity', async ($, on) => {
    const f = fakes(on, { doc: () => mkdoc([row('a', { status: 'quiet 2m' }), row('b')]) })
    await startSession($)
    await f.clock.settle()
    const ui = await mount($, 'terminal')
    expect(await labels(ui)).toEqual(['○ a', '○ b'])
    expect(await texts(ui)).toEqual(expect.arrayContaining(['quiet 2m', 'working']))
  })

  test('inbox shows the oldest three first, then +N more', async ($, on) => {
    const waiting = ['d', 'c', 'b', 'a', 'e'].map((n, i) =>
      row(n, { tone: 'report', status: 'REPORT IN', last_ts: `2026-10-09T11:0${5 - i}:00Z` }),
    )
    const f = fakes(on, { doc: () => mkdoc(waiting) })
    await startSession($)
    await f.clock.settle()
    const ui = await mount($, 'terminal')
    const all = await labels(ui)
    expect(all.map((l) => l.split(' ')[1])).toEqual(['e', 'a', 'b'])
    expect(all.some((l) => l.startsWith('→'))).toBe(false)
    expect(await texts(ui)).toContain('+2 more · /relevo:status')
  })

  test('a report row names the delivered diff from live', async ($, on) => {
    const f = fakes(on, {
      doc: () =>
        mkdoc([row('w', { tone: 'report', status: 'REPORT IN', live: { files: 3, added: 10, removed: 2 } })]),
    })
    await startSession($)
    await f.clock.settle()
    const ui = await mount($, 'terminal')
    expect((await labels(ui))[0]).toBe('● w r1')
    expect(await texts(ui)).toEqual(expect.arrayContaining(['report in · 1h', 'delivered · +10/-2 in 3']))
  })

  test('chain row draws the progress bar when present, the chain string when not', async ($, on) => {
    const f = fakes(on, {
      doc: () =>
        mkdoc([
          row('c1', { chain: 'chain x · plan 2/4', chain_progress: { done: 2, total: 4, phase: 'reviewing' } }),
          row('c2', { chain: 'chain y · plan 1/3 · building' }),
        ]),
    })
    await startSession($)
    await f.clock.settle()
    const ui = await mount($, 'terminal')
    expect(await labels(ui)).toEqual(['○ c1', '○ c2'])
    const t = await texts(ui)
    expect(t).toEqual(expect.arrayContaining(['■■', '▣', '□', '2/4 reviewing', 'chain y · plan 1/3 · building']))
  })

  test('chain bar colors: done green, current bold, rest faint', async ($, on) => {
    const f = fakes(on, { doc: () => mkdoc([row('c1', { chain_progress: { done: 1, total: 3 } })]) })
    await startSession($)
    await f.clock.settle()
    const ui = await mount($, 'terminal')
    const by = async (text: string) => (await ui.findAll({ type: 'Text' })).find((x: any) => x.text === text)
    expect((await by('■')).props.color).toBe('#86d093')
    expect((await by('▣')).props.bold).toBe(true)
    expect((await by('□')).props.color).toBe('#66716d')
  })

  test('a failed poll keeps the last document', async ($, on) => {
    let healthy = true
    const f = fakes(on, { doc: () => (healthy ? mkdoc([row('a')]) : 'fail') })
    await startSession($)
    await f.clock.settle()
    healthy = false
    await f.clock.advance(5_000)
    expect(await labels(await mount($, 'terminal'))).toEqual(['○ a'])
  })

  const passthrough: [string, object | undefined, object][] = [
    ['no bindings', { doc: () => mkdoc([]) }, {}],
    ['a survey holds the band', { doc: () => mkdoc([row('a')]) }, { hasSurvey: true }],
    ['relevo is missing', { status: 'reject' }, {}],
  ]
  for (const [name, opts, props] of passthrough) {
    test(`passthrough: ${name} draws nothing of ours`, async ($, on) => {
      const f = fakes(on, opts as Opts)
      await startSession($)
      await f.clock.settle()
      for (const surface of SURFACES) {
        const ui = await mount($, surface, props)
        expect(await labels(ui)).toHaveLength(0)
        expect(await texts(ui)).toEqual(['engine row'])
      }
    })
  }

  test('a narrow band truncates and never exceeds bodyColumns', async ($, on) => {
    const f = fakes(on, {
      doc: () => mkdoc([row('w', { tone: 'needs', status: 'NEEDS YOU', reason: 'a very long halt reason indeed' })]),
    })
    await startSession($)
    await f.clock.settle()
    for (const l of await labels(await mount($, 'terminal', { bodyColumns: 20 }))) {
      expect(l.length).toBeLessThanOrEqual(20)
    }
  })

  for (const surface of SURFACES) {
    test(`${surface}: a press fills the draft with relevo show and sends nothing`, async ($, on) => {
      const f = fakes(on, { doc: () => mkdoc([row('w', { tone: 'needs', status: 'NEEDS YOU' }), row('r')]) })
      await startSession($)
      await f.clock.settle()
      const ui = await mount($, surface)
      await ui.press({ key: 'bw' })
      await ui.press({ key: 'br' })
      expect(f.fills).toEqual(['relevo show w --report', 'relevo show r --report'])
      expect(f.submits).toHaveLength(0)
    })
  }

  test('an inbox row is tinted and toned by its kind, in lower case', async ($, on) => {
    const f = fakes(on, {
      doc: () =>
        mkdoc([row('n', { tone: 'needs', status: 'NEEDS YOU' }), row('p', { tone: 'report', status: 'REPORT IN' })]),
    })
    await startSession($)
    await f.clock.settle()
    const ui = await mount($, 'terminal')
    const boxes = (await ui.findAll({ type: 'Box' })).map((b: any) => b.props.backgroundColor).filter(Boolean)
    expect(boxes).toEqual(['#2a2212', '#12282d'])
    const t = await ui.findAll({ type: 'Text' })
    expect(t.find((x: any) => x.text === 'needs you · 1h').props.color).toBe('#f0b452')
    expect(t.find((x: any) => x.text === 'report in · 1h').props.color).toBe('#72c8d8')
  })

  for (const surface of SURFACES) {
    test(`${surface}: the next button fills its text and sends nothing, only when next is present`, async ($, on) => {
      const f = fakes(on, {
        doc: () =>
          mkdoc([
            row('w', { tone: 'needs', status: 'NEEDS YOU', last_ts: '2026-10-09T10:00:00Z', next: { label: 'answer', text: 'relevo send w --file a.md' } }),
            row('q', { tone: 'report', status: 'REPORT IN' }),
          ]),
      })
      await startSession($)
      await f.clock.settle()
      const ui = await mount($, surface)
      expect(await labels(ui)).toEqual(['● w r1', '→ answer', '● q r1'])
      await ui.press({ key: 'nw' })
      expect(f.fills).toEqual(['relevo send w --file a.md'])
      expect(f.submits).toHaveLength(0)
    })
  }

  test('a narrow band drops the next button text to fit', async ($, on) => {
    const f = fakes(on, {
      doc: () => mkdoc([row('w', { tone: 'needs', status: 'NEEDS YOU', reason: 'x', next: { label: 'answer the question', text: 't' } })]),
    })
    await startSession($)
    await f.clock.settle()
    for (const l of await labels(await mount($, 'terminal', { bodyColumns: 24 }))) {
      expect(l.length).toBeLessThanOrEqual(24)
    }
  })
})

describe('toasts', () => {
  const run = async ($: any, on: any, docs: object[]) => {
    let i = 0
    const f = fakes(on, { doc: () => docs[Math.min(i, docs.length - 1)] })
    await startSession($)
    await f.clock.settle()
    for (; i < docs.length; i++) await f.clock.advance(5_000)
    return f
  }
  const chain = (done: number, o: object = {}) =>
    mkdoc([row('c', { chain_progress: { done, total: 3 }, candidate: 'claude', ...o })])

  test('nothing toasts on the first poll', async ($, on) => {
    const f = await run($, on, [{ ...chain(2), gates: [{ token: 'g', provider: 'p', until: '', reason: 'r' }] }])
    expect(f.toasts).toEqual([])
  })

  test('chain moved toasts once per growth, and finished at the end', async ($, on) => {
    const f = await run($, on, [chain(0), chain(1), chain(1), chain(3)])
    expect(f.toasts).toEqual(['relevo: c chain moved to plan 2/3', 'relevo: c chain finished'])
  })

  test('a candidate change toasts once', async ($, on) => {
    const f = await run($, on, [chain(0), chain(0, { candidate: 'codex' }), chain(0, { candidate: 'codex' })])
    expect(f.toasts).toEqual(['relevo: c switched to codex'])
  })

  test('a gate set and lifted toast once each, with until or cleared', async ($, on) => {
    const g = (until: string) => [{ token: 'tok', provider: 'p', until, reason: 'limit' }]
    const f = await run($, on, [
      { ...mkdoc([]), gates: [] },
      { ...mkdoc([]), gates: g('') },
      { ...mkdoc([]), gates: g('') },
      { ...mkdoc([]), gates: [] },
    ])
    expect(f.toasts).toEqual(['relevo: gate set on tok until cleared: limit', 'relevo: gate lifted on tok'])
  })

  test('a gate with an until time names it; absent gates never toast', async ($, on) => {
    const f = await run($, on, [
      { ...mkdoc([]), gates: [] },
      { ...mkdoc([]), gates: [{ token: 't', provider: 'p', until: '14:00', reason: 'r' }] },
      mkdoc([]),
    ])
    expect(f.toasts).toEqual(['relevo: gate set on t until 14:00: r'])
  })

  test('an arriving report or question never toasts', async ($, on) => {
    const f = await run($, on, [
      mkdoc([row('w')]),
      mkdoc([row('w', { tone: 'needs', status: 'NEEDS YOU' }), row('new', { tone: 'report', status: 'REPORT IN' })]),
    ])
    expect(f.toasts).toEqual([])
  })
})
