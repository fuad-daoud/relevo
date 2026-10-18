# Plan: a round's base commit reaches `make check-affected` (RELEVO_BASE)

Save this plan to `docs/plans/2026-10-18-round-base-env.md` as the last step and commit it with the code.

## Premises checked (and one caveat)

- `gh issue view 1268 --comments` returned nothing in the planner sandbox (no network). The plan rests on the round prompt and the code. The builder runs the command once; if the issue text contradicts a premise below, halt and report.
- The script this plan edits is **PR #1275's** `scripts/affected-packages.sh` (it adds `parent_dir`, `fall_back` and `--direct`). The builder starts after #1275 merges into main; main's copy in this planning tree is the older one. If `grep -n 'fall_back\|--direct' scripts/affected-packages.sh` finds neither, #1275 is not merged: **halt**.
- A served worktree has no `origin/main`. The script's only other source today is `--base`/`BASE` (`resolve_base`, `scripts/affected-packages.sh:13-40` on main), so the fallback is `./...`.
- The base is already on the binding. `store.Binding.Base` (`internal/store/binding.go:363`) is a 40-hex commit on every path that matters:
  - Served bind: `internal/serve/bindings.go:87-88` validates 40 hex; `:291` sets `Base: req.BaseCommit`.
  - Served chain member: `internal/relevo/chain_served.go:280` (`Base: f.base`, from `req.Base`, validated at `internal/serve/chains.go:96-97`).
  - Local bind: `internal/relevo/add.go` `cutWorktree` (`:497-536`) returns the resolved commit; the adopted-branch path sets `base = tip` (`:343-360`).
  - Local chain member: `internal/relevo/chain_start.go:395` (`b.Base = base.commit`).
  - Resume / `bind --resume` keeps the stored record, so the base survives. Nothing in `internal/relevo/bind.go` resets it.
- Round env is built in one place for every launch (fresh send, nudge, relaunch, resume after daemon restart, mid-round builder switch, queued admit): `roundEnvFor(b, account)` at `internal/relevo/headless.go:401`, built from `roundEnv(b)` (`:199`) which already appends `RELEVO_RUNNER=<name>`. Served rounds run the same code (the serve daemon calls it for its tenants), so the variable reaches zen builders and local builders alike.
- **The gate does not use that env.** Gates start through `startGateProc` (`internal/relevo/gate_proc.go:25-44`) with a `spawn.ProcSpec` that sets no `Env`. Three callers build a `gateProcSpec`: `gateStep` (`internal/relevo/gate.go:36`, a binding's round gate, also a chain builder's gate), `servedCheckSpec` (`internal/relevo/served_check.go:275`) and `chainCheckSpec` (`internal/relevo/chain_check.go:480`, no binding). The runbook line about `--gate 'make check-affected BASE=<sha>'` is about exactly the first. So the gate path needs its own plumbing, or the fix helps only the builder's own runs.
- Parent-env hygiene: `proc.DeniedEnv` (`internal/proc/env.go:23`) strips relevo identity variables from every spawn, and `spawnEnv` (`internal/proc/proc.go:307-321`) appends `spec.Env` after the strip.

## Decision: (b) export `RELEVO_BASE`, not (a) a `refs/relevo/<name>/base` ref

- (b) works for local worktrees (they have `Binding.Base` too), needs no ref lifecycle, and the script needs no binding name. (a) would need the script to infer `<name>` from the branch (`relevo/<name>`) and would not cover local bindings, so it fails the "prefer what works locally" criterion.
- (b) needs no cleanup in `releaseServedRefs` (`internal/serve/cleanup.go:129-153`) or in unbind/gc: it is process environment, not repo state. Say this in the report; do not touch `internal/serve`.
- Resume and chain members need no extra code: they read `b.Base` from the stored binding on every spawn.

## Behaviour and cases

`RELEVO_BASE=<sha>` is set in the environment of (1) every round process and (2) every gate/check process relevo starts for a binding, when `b.Base != ""`. Empty `Base` adds nothing (old records, adopted chain readers without a base, tests).

`scripts/affected-packages.sh` precedence, after #1275's structure: `--base`, `BASE`, `RELEVO_BASE`, `origin/main` merge-base, `./...`.
- `RELEVO_BASE` set and resolvable (`git rev-parse --verify "$X^{commit}"`): use it as is (not a merge-base; it is already a commit the round was cut from). A base older than the current main only makes the diff a superset, which is safe for a test selector.
- `RELEVO_BASE` set but unresolvable (object not in this repo): skip it with a one-line stderr note and continue to `origin/main`. It is **not** a hard fallback to `./...` like an explicit `--base`/`BASE` that fails to resolve, because it is ambient, not typed by the user.
- Empty `--base`/`BASE` strings stay "not given" (Makefile passes `BASE=""`, `Makefile:79`).
- `--direct` (coverage scope) composes: it reads the same base, nothing else changes.

`proc.DeniedEnv` gains `RELEVO_BASE`, so a stale value in the daemon's own environment never shadows a round's (the spec's value is appended after the strip, same as `RELEVO_RUNNER`).

Out of scope, say so in the report: `internal/consult/verify.go:303` (a reviewer harness env, marker only), and the chain-level `chainCheckSpec` (no binding; its `Dir` is the chain's own tree where `origin/main` exists for local chains). Server chains' checks go through `servedCheckSpec`, which is in scope.

## Seams

1. `internal/relevo/headless.go` — `roundEnv` (`:199`): append the base entry after the runner marker. Keep the `// why` comment on the function accurate (it says what the process env holds; add base).
2. New small helper, new file `internal/relevo/round_base_env.go` (headless.go is already 1618 lines and excluded from the size guard; do not grow it more than the one call): `baseEnvEntry(b store.Binding) []string` — nil when `b.Base == ""`, else `["RELEVO_BASE="+b.Base]`. One const for the name, next to it. One-line why-comment: scripts read it to diff against the commit the round was cut from, because a served tree has no `origin/main`.
3. `internal/relevo/gate_proc.go` — add `Env []string` to `gateProcSpec` and pass it as `spawn.ProcSpec.Env` in `startGateProc`.
4. `internal/relevo/gate.go:36` (`gateStep`) and `internal/relevo/served_check.go:275` (`servedCheckSpec`): set `Env: baseEnvEntry(b)`.
5. `internal/proc/env.go:23`: add `"RELEVO_BASE"` to `DeniedEnv` (update the doc comment only if its list wording needs it).
6. `scripts/affected-packages.sh`: in `resolve_base` (or a tiny `env_base` function it calls), add the `RELEVO_BASE` step between the override branch and the `origin/main` check; update the header `Usage`/`Env:` block to list `RELEVO_BASE` and the precedence. No new fall-back path may bypass `fall_back`/`--direct` handling from #1275.
7. `Makefile:73-76` comment: say `RELEVO_BASE` (set by relevo in a round) is used when `BASE` is empty. No recipe change: make passes the environment through and `BASE="$(BASE)"` stays.
8. Docs:
   - `docs/runbook.md:96-99`: replace the "A served worktree has no `origin/main` ... pass `BASE=<sha>`" sentence with: relevo exports `RELEVO_BASE` (the bind's base) to a round and to its gate, so `--gate 'make check-affected'` tests only the affected packages on a served worktree too; `BASE=<sha>` still overrides.
   - `README.md` ~`:797-805` (the "round's session is a runner" bullet): add one sentence that every round and gate process also carries `RELEVO_BASE=<base commit>` when the binding has one.

## Tests (each must fail without the fix)

Shell, `scripts/affected-packages_test.sh` (extend, same `reset_fixture`/`assert_stdout` helpers; the fixture has `origin/main` at the initial commit, so add a second commit on a branch to make bases differ):
- `RELEVO_BASE` beats `origin/main`: with `origin/main` deleted (`git update-ref -d refs/remotes/origin/main`, restore after, as case 9 does), `RELEVO_BASE=<initial sha>` plus an edit in `a/a.go` gives `$want_a`, not `./...`. Mutation: drop the `RELEVO_BASE` step → output `./...`.
- precedence: with `RELEVO_BASE` set to a base that yields `a b c d e` and `--base HEAD` given, the result is empty; `BASE=HEAD` likewise. Mutation: check `RELEVO_BASE` before `--base`/`BASE` → non-empty output.
- unresolvable `RELEVO_BASE=0000...` with `origin/main` present falls through to `origin/main` (normal output, non-empty stderr note, exit 0). Mutation: treat it as a hard fallback → `./...`.
- empty `RELEVO_BASE=` is ignored (behaves like unset).
- `--direct` with `RELEVO_BASE` and no `origin/main` lists only the edited package.
- Must pass under `sh` and `bash --posix`; keep the file under 600 lines (340 now).

Go, `internal/relevo` (pure, no harness, no network — fine for CI):
- `headless_test.go` `TestRoundEnvMarksTheRunner`: add a case with `Base` set expecting `RELEVO_BASE=<sha>` after `RELEVO_RUNNER=`, plus the author case with a base; the existing no-base cases stay unchanged and pin "nothing added when empty". Mutation: drop the append in `roundEnv`.
- `accountenv_test.go`: `roundEnvFor` with a base still puts the account entry last (account value must remain the later entry). Mutation: put the base after the account entry → fails.
- New test next to the helper (`round_base_env_test.go`): `baseEnvEntry` empty/non-empty.
- `gate_proc_test.go` (add or extend the nearest existing gate test file; `gitshim_test.go:299` shows a `startGateProc` use with a fake runner): a gate and a served check started from a binding with a `Base` reach the runner with `RELEVO_BASE` in `ProcSpec.Env`; without a base, `Env` is nil. Use whatever fake `spawn.Runner` the neighbouring tests use, and capture the spec. Mutation: drop `Env:` in `gateStep` → the gate test fails; drop it in `servedCheckSpec` → the check test fails.
- `internal/proc`: extend `TestStartStripsDeniedEnv` (or add a sibling) with `t.Setenv("RELEVO_BASE","stale")` plus `spec.Env = ["RELEVO_BASE=fresh"]`: the child sees `fresh`; with only the parent value it sees unset. Mutation: remove the name from `DeniedEnv` → the stale case fails.

CLAUDE.md rule: no test here executes a harness subcommand from `cmd/relevo`; all rules are pure functions or fake-runner tests in `internal/relevo`. Say so in the report.

## Ordered steps

1. Verify #1275 is merged (`grep -n 'fall_back' scripts/affected-packages.sh`); branch off `origin/main` on zen. Halt if not.
2. Script + shell tests (seams 6, 7). Run `sh scripts/affected-packages_test.sh` and `bash --posix scripts/affected-packages_test.sh`; shellcheck if installed. Done when both pass; then do the two mutations (drop step; reorder) and confirm named cases fail.
3. Go helper, `roundEnv`, `gateProcSpec`/`startGateProc`, `gateStep`, `servedCheckSpec`, `DeniedEnv` (seams 1-5) with their tests. Focused: `go test ./internal/relevo/ -run 'RoundEnv|BaseEnv|Gate|ServedCheck|AccountEnv' ./internal/proc/`. Fix every error before the next run.
4. Mutation-check each Go test as listed above, restore each.
5. Docs (seam 8). `sh scripts/check-comments.sh` passes (no issue/PR numbers or history in code, comments or test names).
6. Real check on a served-like tree: in a temp clone with no `origin/main`, `RELEVO_BASE=$(git rev-parse HEAD~1) make check-affected` lists only affected packages (print `sh scripts/affected-packages.sh`); report the output.
7. Save this plan to `docs/plans/2026-10-18-round-base-env.md`; run `make check-affected` once as the final gate; commit everything (code, tests, docs, plan) as new commits — never amend or rebase.

## Declared scope (`git diff --stat` must stay inside)

`scripts/affected-packages.sh`, `scripts/affected-packages_test.sh`, `Makefile` (comment only), `internal/relevo/headless.go`, `internal/relevo/round_base_env.go` (+ `_test.go`), `internal/relevo/gate_proc.go`, `internal/relevo/gate.go`, `internal/relevo/served_check.go`, the touched `*_test.go` in `internal/relevo` (`headless_test.go`, `accountenv_test.go`, one gate test file), `internal/proc/env.go`, `internal/proc/proc_test.go`, `docs/runbook.md`, `README.md`, `docs/plans/2026-10-18-round-base-env.md`. No `internal/serve` change, no new ref, no coverage-baseline change (no package moves; if `make check` shows a coverage move, report it, do not rewrite the baseline).

## Report must include

- Whether #1275 was merged and which script version was edited.
- The decision (b) and why (a) was dropped; that no ref cleanup is needed.
- The precedence order as implemented and the unresolvable-`RELEVO_BASE` behaviour.
- Each mutation run and the test that failed.
- The step-6 output showing a no-`origin/main` tree selecting a subset.
- That the chain-level `chainCheckSpec` and `consult/verify.go` were left alone, and why.
- The `make check-affected` result and `git diff --stat` against the declared scope.
- Any contradiction found with the issue text (the issue could not be read in planning).
- Live caveat: `RELEVO_BASE` takes effect for a round only once the serve daemon runs the new binary; rounds already in flight keep their old environment.
