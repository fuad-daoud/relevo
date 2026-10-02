# Chain lab: handoff (2026-10-01, MasterMind opencode-107)

Read this first. The lab lives in `~/.cache/chain-lab`, evidence in
`~/.cache/chain-lab/evidence/`.

## State: all fixes merged and deployed

- `main` = `bc4de8cc` (PRs #806 and #809); `v0.15.0-24-gbc4de8cc` runs on the
  laptop, zen and contabo. Rollback: zen `~/.local/bin/relevo.prev`, contabo
  `srv.fish rollback relevo-serve`.
- Nine issues found and closed by code or config: #794, #796, #797, #798,
  #799, #800, #803, #804, #805.
- Server config: actors/candidates mirrored from the laptop (contabo's are
  versioned in `~/projects/servers/contabo/services/relevo-serve/relevo-config.json`);
  contabo's `opencode.jsonc` allows `/srv/data/relevo-serve/**`; zen's allows
  its `~/.local/state/relevo-serve/**`.

## The lab project

- `~/.cache/chain-lab`: Python stdlib, `make check`, base `5bace58`.
- `plans/` (untracked): `z1-r1`, `z2-r1/z2-r2`, `z3-r1` (eval loader),
  `c1-r1`, `c2-r1/c2-r2`, `l1-r1/l1-r2`, `v1-r1`.
- Nine chains ran: z1, z2, c1, c2, l1, v1, v2, v3 finished; z3 was abandoned
  into done by #805 (now fixed).

## How to run a chain

```sh
cd ~/.cache/chain-lab
export RELEVO_MASTERMIND=<your mastermind id>   # the shell does not inherit it
relevo chain --name lab-x1 --plan plans/v1-r1.md --feature chain-lab \
  --gate "make check" [--security] --base main --server contabo \
  [--regate N] [--max-corrections K]
```

- Drop `--server` for a local chain; the builder then follows the actor's
  placement (zen first).
- Watch with `relevo show <n> --trace`; wait with `relevo wait --name <n>`.
  Do **not** use `wait --any <chain>`: the chain and its builder share a
  name, and `--any` can resolve the builder binding and trip on a stale
  report. Prefer `--name`.
- Read the round artifacts **before** `relevo done <n>`; done releases the
  members.
- Server chains: `show/wait/status` from the client read the server view; on
  the server box `relevo show <chain>` says "not a chain" -- use sqlite3 on
  the box if you need its raw row.

## Gotchas learned in wave 1

- Candidate gates (2026-10-01): gemini, claude-sonnet-4-6 and terra were
  rate-limited; builders landed on deepseek. Check `relevo status --json`'s
  `gated` list and each server's ledger.
- An opencode reader with no tier runs without `--auto`; a read outside its
  allowlist is auto-rejected and the session aborts, killing the round
  without output. The serve roots are allowed on both boxes now.
- A model that wanders (the planner once searched `~/.cache` for relevo
  source) is the main way a round dies without a report.
- Zen: its `relevo.service` + `relevo-serve` exited cleanly (status 0, no
  systemd stop) three times while another session's bindings (`board`,
  `cc-w1`, `mm-*`) were being polled; it stayed up once that traffic
  stopped. Unexplained -- watch it. Zen also carries `fix-789` from another
  session (idle). Do not restart zen's units while something else runs there
  without checking.
- contabo: builder cap 3, shared with other sessions; zen: cap 15.

## Next: wave 2 (not started)

Negative batch, one invocation each (expect a clean refusal, nothing
created):

- empty plan file; missing plan file
- chain name already taken / member name taken
- chain name longer than 27 characters
- `--feature` and `--no-feature` together
- `relevo chain --resume` on a running chain
- `relevo send` to a running chain member
- `relevo done` while the chain runs
- `--base` naming a missing ref
- `--security-actor` naming an actor a server does not have

Re-verify the fixed paths live (one small chain or scenario each):

- a reviewer that recaps: `findings.md` holds the block-carrying message
  (#804)
- red gate + `--resume --gate "make check"`: the plan is re-sent, gate log
  green (#797)
- `relevo done` on a finished server chain: no `internal`, mirror members
  released (#805)
- after a finish: no unread member rows (#799), and no stale halt delivery
  gets pushed after a resume (#800)

## Method that worked

Small plans, builders on zen/contabo, traces watched closely, evidence
copied into `~/.cache/chain-lab/evidence/` before anything is released,
issues filed in `fuad-daoud/relevo`, fixes with tests, `make check`, PR +
merge, deploy, re-verify.
