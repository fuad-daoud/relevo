# Chain lab: wave 2 (2026-10-01, MasterMind opencode-112)

Wave 1's handoff (`2026-10-01-chain-lab-handoff.md`) is the method and the lab
layout; this is what wave 2 did on top. Evidence: `~/.cache/chain-lab/evidence/w2/`
(see `INDEX.md`), lab project `~/.cache/chain-lab`.

## State

- Four defects found; all fixed, merged and **deployed** on `v0.15.0-32-gf3697c24`
  (laptop, zen, contabo):
  - #812 start-time input refusals were `internal` (empty/missing plan, bad or
    taken name, over-long name, bad `--base`, resume `--gate` on a remote
    builder) -- PR #814.
  - #813 server actor refusals were `internal` (`chain --security-actor` and
    `bind --actor` on a server) -- PR #814.
  - #816 a server chain's resume left the mirror's queued halt undelivered, so
    the stale NEEDS YOU was handed over later -- PR #817.
  - #819 resume refusals on a settled chain (local running/done, server done)
    were `internal` -- PR #820.
- The wave-2 negative batch is clean on the final build (every start refusal
  coded; nothing created; resume/send/done on a running chain are conflicts).
- Fixed wave-1 paths re-verified live on the final build, chain `lab-h3`
  (server chain on contabo, red gate -> resume with `--gate "make check"`):
  - #797: round 3's prompt is the plan copy (byte-identical), gate green.
  - #800/#816: the mirror's halt was `confirmed=1 route "superseded by resume"`
    right after the resume; the later `wait` delivered the finish only.
  - #804/#818: the reviewer artifact is the block-carrying message minus the
    block, byte-for-byte (two stream messages; the interim one was not chosen).
  - #799: no unread member rows before or after done. #805: `done` on the
    finished server chain -- no internal, mirror and server members released.
- Observation, not filed: on `lab-h2` the `security` (mimo) reviewer wrote a
  full review whose findings file carried `verdict: pass`, yet the chain parsed
  no verdict and halted fail-closed. The raw stream was released before it
  could be inspected; re-check the stream shape if it reproduces.

## Deploy notes

- The turso session deployed #807 (Turso engine + DB conversion) to all three
  boxes first; their DB backups are `relevo.db.pre-466-backup` /
  `relevo.db.pre-turso` on each box, plus this session's
  `relevo.db.pre-turso-<stamp>` on the laptop. Post-conversion, query the store
  with `relevo db query '<SQL>'` -- plain `sqlite3` no longer opens it.
- Zen runs TLS on 127.0.0.1:7777; the health probe is
  `curl -sk https://127.0.0.1:7777/v1/whoami` -> 401, not a plain-HTTP 426.
- Zen watch: no unexplained clean exits during wave-2 chains; other sessions'
  rounds (`cc-w1`, `s0`) ran alongside untouched.

## Housekeeping / next

- Today's lab bindings are done: lab-g1w2, lab-g1w2-x, lab-h1, lab-h2, lab-h3,
  lab-l2w2, lab-r2w2 (plus wave 1's). `relevo unbind --done` when the evidence
  is no longer needed; the evidence in `~/.cache/chain-lab/evidence/w2/` is
  the record.
- Still open from wave 1's list: sub-chains (#754), the `ErrReportPending`
  resume halt, `--regate` on a plain remote bind, the OOM peak.
- A wave-3 idea: re-test a *plain* binding's reviewer recap against #818, and
  the mimo/no-verdict case above with the stream captured.
