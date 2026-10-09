import { describe, expect, mock, test } from 'claude-code/testing'

const RUN_OK = { exitCode: 0, stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false }
const STATUS_OK = { ...RUN_OK, stdout: '{"mastermind":{"id":"mm-1"}}' }

type Opts = {
  status?: 'ok' | 'fail' | 'reject'
  submit?: (text: string) => unknown
  deny?: boolean
  ackExit?: () => number
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
  const pipes: { push: (t: string) => void; end: () => void }[] = []

  on('session.id', () => ({ value: 'sess-1' }))
  on('session.start', () => ({ cwd: '/w' }))
  on('ui.log', ($: any, e: any) => {
    log.push(e.text)
    return { value: undefined }
  })
  on('process.run', ($: any, e: any) => {
    runs.push({ argv: e.argv, env: e.init?.env })
    if (e.argv[1] === 'status') {
      if (opts.status === 'reject') throw new Error('ENOENT')
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
  on('prompt.submit', async ($: any, e: any) => {
    const out = await (opts.submit?.(e.text) ?? { text: e.text })
    if (!(out as any).drop) submits.push(e.text)
    return out
  })
  if (opts.deny) on('session.append', () => ({ deny: 'refused' }))
  on('turn.start', ($: any, e: any) => ({ turnId: e.turnId }))
  on('turn.step', async function* ($: any, e: any) {
    return { turnId: e.turnId, index: e.index, answer: '', toolUses: [] }
  })
  on('turn.complete', () => ({ text: '' }))
  return { clock, log, runs, acks, statuses, submits, appends, spawns, pipes }
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
    expect(f.statuses()).toHaveLength(1)
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

  test('a failed ack is retried a bounded number of times', async ($, on) => {
    let n = 0
    const f = fakes(on, { ackExit: () => (++n < 3 ? 1 : 0) })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(5))
    await f.clock.settle()
    expect(f.acks()).toHaveLength(3)
    expect(f.log).toHaveLength(0)
  })

  test('an ack that never succeeds stops after 3 tries and logs', async ($, on) => {
    const f = fakes(on, { ackExit: () => 1 })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(5))
    await f.clock.settle()
    expect(f.acks()).toHaveLength(3)
    expect(f.log).toHaveLength(1)
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
    expect(f.acks()).toHaveLength(0)
    expect(f.log.length).toBeGreaterThan(0)
    expect(f.log[0]).toContain('refused')
    await f.clock.advance(1_000)
    expect(f.spawns).toHaveLength(2)
    f.pipes[1]?.push(report(9))
    await f.clock.settle()
    expect(f.log.length).toBeGreaterThan(3)
  })

  test('a dropped submit: no ack, logged, child ended', async ($, on) => {
    const f = fakes(on, { submit: () => ({ drop: 'blocked' }) })
    await startSession($)
    await f.clock.settle()
    f.pipes[0]?.push(report(9))
    await f.clock.settle()
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
