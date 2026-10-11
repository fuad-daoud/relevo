# MasterMind amendments (these override the plan below)

1. **`live-builder` exists as an actor, not a role.** `relevo config` lists actor `live-builder` with agent plan-executor and role builder. Keying the shim on the role's `ShapeBuilder` (not on actor names) already covers it -- drop the "halt if live-builder appears" clause, and pin in a test that the decision follows the round's role, not its actor name.
2. **Find the subcommand after git's global options.** `git -C dir stash`, `git -c k=v stash push`, `git --git-dir=x --work-tree=y stash`, `git --no-pager stash list` must all be refused. Parse leading global options (those taking a separate value: -C, -c, --git-dir, --work-tree, --namespace, --exec-path= forms, --config-env; flags like --no-pager, -p, -P, --bare, --literal-pathspecs) and test each form above. `git stash` inside an alias is out of scope; say so in the report.
3. **No self-recursion.** When searching PATH for the real git, skip every entry that resolves to the shim's own directory (compare canonical paths, and also skip a candidate that is the shim file itself), so a duplicate PATH entry or a symlinked state dir cannot loop. Test it with the shim dir listed twice on PATH.
4. **The harness's own git use must keep working.** opencode (and other harnesses) call git internally (e.g. snapshots); only `stash` is refused, everything else passes through with arguments, stdin, exit code and output untouched. The pass-through test covers stdin (e.g. a fake git that echoes stdin).
5. Container mode: if mounting the shim is not a small change, ship the documented skip with its pin; do not grow the round.
6. Do not fetch (no origin remote). Never use `git stash` yourself. Commit the plan with these amendments as docs/plans/2026-10-11-builder-no-stash.md.

---

# Plan: builders on one server share a git stash stack across worktrees (builder-no-stash)

Fixes #1131. Base: origin/main f1931b74.

## 1. Premise and seed correction

The premise holds on current main. One repository serves every worktree of a
binding set: a headless round runs with `Dir: roundTree(rt, b)`
(`internal/relevo/headless.go:389-390`, tree resolved in
`internal/relevo/scratch.go:97`), and a server lays owners out as
`bindings/<hex>/.worktrees` plus `.scratch` (`internal/serve/tenant.go:109-113`).
Linked worktrees of one repository share that repository's single `refs/stash`,
so `git stash` / `git pop` in one builder's worktree can take another lane's
work. No `stash` call exists in non-docs code today (grep finds it only under
`docs/`), and neither the agent definitions under `internal/harness/agents/`
nor `internal/mastermind/guide.md` tells builders to avoid it — the "say so"
half of this round is new guidance, not an edit of existing guidance.

Seed correction (said, not guessed around): the seed names writer actors as
"builder, live-builder, librarian". Current main has no `live-builder` role.
The writer set is `harness.roleTable` rows with `ShapeBuilder`
(`internal/harness/harness.go:35-60`): exactly `builder` and `librarian`
(`reviewer`/`researcher` are `ShapeConsult`); `internal/roles/builtin.go:13-42`
derives the registry from that same table, so no second table can drift. A repo
grep for `live-builder`/`LiveBuilder` hits only stale `docs/plans/` prose. The
builder therefore keys the shim on `ShapeBuilder`, not on a hard-coded triple:
any future writer role gets the protection, and any future reader stays clean.
If a `live-builder` role appears before this builds, the round halts and
reports rather than guessing which shape it is.

## 2. Behaviour

1. Every writer round's process (`builder`, `librarian`, i.e. every
   `ShapeBuilder` role) runs with a relevo-owned `git` shim first on `PATH`.
   `git stash` (any `stash` subcommand form) is refused with a one-line message
   naming the alternatives (a WIP commit, or copies outside the repo). Every
   other git invocation execs the real git, found by searching `PATH` after the
   shim's own directory — never a hard-coded path. Non-zero exit on refusal;
   the real git's exit code and output pass through untouched otherwise.
2. The shim directory lives under relevo state (`store`-rooted, so a server's
   per-owner store roots each get their own), is written idempotently (second
   ensure changes nothing), and is created before `Runner.Start` — a write
   failure refuses the round start with no process spawned, mirroring the
   `EnsureOutDir` precedent (`internal/relevo/headless.go:327-330`).
3. Reader rounds (consults, `internal/consult/verify.go:298-305`), gate
   processes (`internal/relevo/gate_proc.go:29-36`, which carry no `Env` at
   all), and every non-writer actor keep byte-identical environments. The shim
   reaches remote (`relevo serve`) rounds through the same `startProcess` funnel
   — server runtimes only wrap the runner (`internal/serve/serve.go:364-452`),
   never rebuild the spec — so no serve-side env fork is needed.
4. Guidance: every shipped `plan-executor` and `librarian` definition and
   `internal/mastermind/guide.md` say never to use `git stash` in a relevo
   worktree (shared stack across builders' worktrees) and name the same two
   alternatives.

Cases the builder pins:

- `git stash`, `git stash push -m x`, `git stash list` → refused, non-zero,
  one line naming WIP commit / copies outside the repo.
- `git status`, `git log`, `git` (bare), `-C <dir> status` → byte-identical
  passthrough of the real git's output and exit code.
- `PATH` empty in the daemon → `PATH=<shimdir>` (no trailing colon; a trailing
  colon would put the cwd on `PATH`).
- Second ensure → file unchanged (content comparison, not mtime trust).
- Shim dir missing/unwritable at round start → round start error, zero spawns.
- User-mode tenant: `isolate.UserSpec` (`internal/isolate/isolate.go:124-137`)
  denies home/identity vars but never `PATH`, so the shim entry survives; the
  store-rooted location keeps it traversable per owner. Container mode:
  host-path shim is not automatically visible inside the container — the
  builder verifies `ContainerArgv` and either mounts it or records the explicit
  skip with a test pinning whichever behaviour ships. No silent gap either way.
- Consult env (`RELEVO_RUNNER=` only) and gate spec (no `Env`) unchanged —
  pinned by test, not by inspection.

## 3. Seams (file:line evidence)

- Writer set: `internal/harness/harness.go:35-60` (`roleTable`,
  `ShapeBuilder`), `internal/roles/builtin.go:13-42` (registry derives from it).
- Round env funnel: `internal/relevo/headless.go:173-191` (`builderEnv`),
  `:199-211` (`roundEnv`, `roundEnvFor`), `:213-230` (`accountEnvEntry`),
  `:288-293` (`startRound` argv), `:325-396` (`startProcess`, spec at
  `:389-396`), `:437-482` (`resumeRound` reuses the funnel).
- Spawn env mechanics: `internal/proc/proc.go:154-162` (`buildCmd`),
  `:307-321` (`spawnEnv`), `internal/proc/env.go:28-43` (`ChildEnv`),
  `internal/proc/gitenv.go:14-27` (`gitNoFsmonitorEnv` — the pattern for
  additive env entries).
- Server wrapping (no env fork): `internal/serve/serve.go:364-398`
  (`runtimeAt`), `:407-452` (`applyContainer`, `applyTenant`),
  `internal/serve/tenant_tmp.go:53-66` (`tmpDirRunner.Start` — the per-owner
  env-append precedent), `internal/isolate/isolate.go:92-137` (deny lists,
  `UserSpec`), `:327-351` (`boundary.Start`).
- Must-not-change envs: `internal/consult/verify.go:298-305` (consult),
  `internal/relevo/gate_proc.go:29-36` (gate).
- Local wiring: `cmd/relevo/wire.go:493-502` (`store.TempDir`, `proc.New()`),
  `internal/store/store.go:38` (`StateRootMode 0700`), `:156-212`
  (`DefaultRoot`, `TempDir`), `internal/store/paths.go:16` (`Dir`).
- Guidance + shipping: `internal/harness/agents/plan-executor.{claude.md,
  opencode.md,agy.md,codex.toml}` and `librarian.{claude.md,opencode.md,
  agy.md,codex.toml}` (4+4 files), `scripts/agents-shipped.sh` (`--write`
  regenerates `internal/harness/agents/shipped.sha256`),
  `internal/harness/shipped.go` (index), `internal/mastermind/guide.md`.
- Existing env-pin precedents: `internal/relevo/headless_test.go:100-159`
  (`TestRoundEnvMarksTheRunner`, `TestStartProcessMarksTheRunner`),
  `internal/relevo/accountenv_test.go:34-64` (`roundEnvFor` rules).
- Shell harness-env note: all four harnesses are spawned as child processes
  (`spawn.HeadlessLaunch`, `internal/spawn/launch.go:16-29`), so the shell tool
  inherits the round's `PATH` on every kind; the builder confirms this by
  reading each kind's launch path rather than by running a harness (CI has no
  harness binary).

## 4. Ordered steps

Each step is one line: deliverable + how the builder knows it worked.

1. Deliverable: read-only confirmation of premise, writer set, and the
   `live-builder` absence, recorded in the report with file:line quotes. Know
   it worked: report cites `scratch.go:97`, `harness.go:35-60`,
   `tenant.go:109-113`, and the empty code grep for `live-builder`.
2. Deliverable: new `internal/relevo/gitshim.go` — POSIX-`sh` shim source
   const, idempotent `ensureGitShim(dir)` (mkdir, write `git` 0755 iff content
   differs), pure `gitShimPATH(path, dir)` prepend, pure
   `needsGitShim(role string)` keyed on `ShapeBuilder` via
   `harness.RoleByName`. Know it worked: `gofmt`, `go vet`, file ≤ 600 lines,
   functions ≤ 70 lines.
3. Deliverable: new `internal/store` accessor for the shim directory
   (state-rooted path) in `internal/store/paths.go` + unit test. Know it
   worked: test asserts the path is under the store root and stable across
   calls.
4. Deliverable: `startProcess` wiring in `internal/relevo/headless.go`
   (~10 lines): ensure shim + append `PATH` prepend iff the binding's role
   needs it; ensure error returns before `Runner.Start`. Know it worked:
   focused `go test ./internal/relevo/ -run 'Shim|RoundEnv|StartProcess'`.
5. Deliverable: `internal/relevo/gitshim_test.go` — shim behaviour test
   (POSIX `sh`, macOS-safe: `command -v`, no bashisms, no network): fake `git`
   later on `PATH`; `stash`/`stash push`/`stash list` refused non-zero with the
   one line, `status`/`log` pass through byte-identical with the fake's exit
   code. Know it worked: fails when the refusal line execs instead
   (mutation: delete the `stash` case), fails when passthrough is broken
   (mutation: point the exec at the wrong binary).
6. Deliverable: round-env test in the same new test file: writer spec `Env`
   carries the shim dir first on `PATH`; consult-shaped and gateless specs
   carry no shim entry. Know it worked: fails when the prepend is dropped
   (mutation: return `path` unchanged), fails when the shim leaks into
   `consult/verify.go`'s env (mutation: apply unconditionally).
7. Deliverable: serve/container verification — test through a fake `Runner`
   that a served `startProcess` spec carries the shim (same funnel), plus an
   explicit container-mode decision (mount or documented skip) with its own
   pin. Know it worked: `go test ./internal/serve/ ./internal/isolate/`.
8. Deliverable: guidance edits — one never-`stash` paragraph (shared stack,
   WIP commit / outside-repo copies) in all 8 agent files
   (plan-executor ×4, librarian ×4), then `sh scripts/agents-shipped.sh
   --write`; same rule added to `internal/mastermind/guide.md`. Know it
   worked: `sh scripts/agents-shipped.sh --check` passes; `git diff --stat`
   shows 8 agent files + `shipped.sha256` + `guide.md` and nothing else.
9. Deliverable: `make check` green on the final tree. Know it worked: exit 0,
   and any coverage-baseline movement is regenerated with
   `sh scripts/check-coverage.sh --write` (stated in the report), never lowered
   to force green.
10. Deliverable: this plan saved as
    `docs/plans/2026-10-11-builder-no-stash.md` and committed with the code in
    new commit(s) whose messages name the issue (`Fixes #1131`); no amend or
    rebase of anything already on the branch. Know it worked: `git log` shows
    only additive commits and `git status` is clean.

## 5. Deleted behaviour (closed list)

1. None — no behaviour is deleted. `git stash` had no sanctioned use in code
   (no non-docs hits); the round only blocks it in writer-round processes and
   adds guidance. No flags, commands, files, or env vars are removed.

## 6. Scope (exact files the builder may touch)

Allowed: new `internal/relevo/gitshim.go`, new `internal/relevo/gitshim_test.go`,
`internal/relevo/headless.go` (wiring only), `internal/store/paths.go` +
`internal/store/paths_test.go` (accessor only), the 8 agent definition files
listed in §3, `internal/harness/agents/shipped.sha256` (via `--write` only),
`internal/mastermind/guide.md`, new `docs/plans/2026-10-11-builder-no-stash.md`,
`testdata/coverage-baseline.txt` (only if regenerated via `--write`, said so
in the report). Forbidden (parallel lanes): `cmd/relevo/args.go`;
`internal/db/**`; `cmd/relevo/daemon.go`; `dist/*.service`;
`cmd/relevo/mastermind_hook.go`; MasterMind attach/status text anywhere. New
files only outside this list (e.g. a helper the shim needs) with a why in the
report; no new lint exclusions (`.golangci.yml` untouched); comments say why
only, no issue numbers in code or tests.

Builder constraints: server worktree with no origin remote — never fetch.
Never `git stash` (use a WIP commit or copies outside the repo for mutation
checks). CLI tests stay pure in `internal/relevo` (CI has no harness/network).
`make check` is the gate; focused `go test` commands iterate before it.

## 7. Report must include

Per-step status; the `live-builder` seed contradiction with its evidence; the
container-mode decision; which harness shell-env inheritance reads were done
and what each returned; files created/modified mapped to steps; every mutation
named in §4 steps 5–6 with kill confirmation; coverage-baseline handling;
`changed_paths`, `commands_run` (both commands: the focused `go test` line and
`make check`), `not_done`, and the `relevo` block.

## Round 2

`Store.GitShimDir()` moved from `<root>/shims` to `<root>/.shims`: `shims` is a
valid binding name, a dot-prefixed one is not. `TestGitShimDirIsOutsideBindingNamespace`
pins it. No walker over a store root lists or deletes unknown dot-directories
(bindings come from the database; per-binding walks stay under `Dir(name)`).
