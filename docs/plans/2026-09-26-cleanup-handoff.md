# Codebase cleanup -- handoff (2026-09-26)

Start a new planner session here. The cleanup's spec is
`docs/specs/2026-09-25-codebase-cleanup-design.md` (on main). This file is the
state of play and the working method; it is not committed (it lives in the main
checkout, untracked, like the other handoffs).

## 1. Goal and the owner's decisions

The owner wants relevo small and plain enough to read every line of the
**finished** codebase (he reviews the end state, not the PRs). Decisions he made:

- Humans use the cockpit TUI (later a web GUI); the CLI is for AI agents. Human-only
  CLI output may be dropped. An AI-first CLI redesign is #505, **after** the cleanup.
- Nothing is off limits, except: keep codex, `ask`, remote builders, the cockpit,
  the relay->relevo migration and old-state compatibility (until #411).
- Style: dexpace Go styleguide **minus** "2 assertions per function" and mandatory
  exported doc comments. Comments say *why* only; no issue numbers / `§` / history.
  Functions <= 70 lines, gocognit <= 30, files <= 600. Enforced in `make check`
  (golangci-lint `.golangci.yml`, `scripts/check-comments.sh`,
  `scripts/check-filesize.sh`, `scripts/check-coverage.sh`), with a per-package
  exclusion ratchet that phase 3 removes.
- `scratch.go` deleted (done). **Cockpit split: approved, the planner decides.**
  `internal/ui` (~16k non-test lines, one package) plus `ui/dash` and `pick` may be
  split into packages however the planner sees fit (for example views / forms /
  fetch / shell). Survey it first (import graph, what each file owns), propose the
  split to the owner in a few lines, then run it as moves-only rounds like the
  rest of phase 2 -- and only when no other planner has cockpit work in flight
  (check open PRs and `relevo status --all` for ck-* bindings). cmd/relevo's
  phase-3 finish comes **after** phase 2.
- **At most 3-4 local builder rounds**; heavy test rounds go **remote**
  (`relevo bind --server contabo --base <origin/main sha>`). Two laptop crashes
  happened from local load.

## 2. Where it stands (main at 94c27eeb plus whatever landed since)

- **Phase 0 (pin) -- done:** CLI/MCP/DB/config goldens (#523), wire goldens (#517),
  lint + comment + file-size tooling (#519), coverage guard tied to go1.27
  linux/amd64 and enforced on CI's ubuntu/stable leg (#522).
- **Phase 1 (delete) -- done:** land/review/fork (#512), pane leftovers + edge (#515,
  `Binding.Edges` kept as a record shim), human-only history/serve-status text
  (#516), dead code (#522).
- **Phase 3 (finish) -- done for every package that stays put:** histq, candidate,
  classify, transcript, jsonshape, legacy, patch, setup, release, upgrade,
  chatlabel, git, proc, hooks, mcp, store, db, planner, policy, doctor, migrate,
  ingest, usage, serve, remote/client, harness, agentsrc, stats, config
  (#526-#566). Each: comments <= half, no growth, lint-clean, off every exclusion.
- **Phase 2 (restructure) -- in progress:**
  - done: cmd/relevo split into per-verb files (#525); `internal/capture` (#571),
    `internal/spawn` (#572, the runner types + the one `ExitTrailer` /
    `RusageTrailerPrefix`), actors folded into `internal/roles` and config off the
    remote client (#573, `ToRolesFile` -> `FromActors`), `internal/availability`
    (#577 merges ledger/history/latency; #585 moves relevo's gate/probe/limit-matching
    code; `headlessLaunch` -> `spawn.HeadlessLaunch`), `internal/delivery` (#582, incl.
    push/origin; scratch.go deleted), `remote.go` split in-package into 7 files +
    `pull.go` -> delivery (#588), `internal/consult` + `internal/reporttail` (#589),
    `internal/view` part a -- status read model + rendering (#592).
  - **in flight at handoff:** `cl-p2-view-b` (remote, contabo) -- `show.go` and the
    store-only transcript reader -> `internal/view`. STATUS: **finished, not yet verified or merged.** Report `~/.local/state/relevo/cl-p2-view-b/001-report.md` (status done, no golden touched, `Show(ctx, view.ShowDeps, opts)`); branch `relevo/cl-p2-view-b` at `cc84ed33` (22 files, base `94c27eeb`). Next: rebase onto origin/main, verify per §3.4 (build/vet, lint on internal/view, scope check, golden diff empty), open the PR, merge via merge-if-green.sh, then `relevo done --name cl-p2-view-b`.
  - left: (1) **Runtime** -- the spec says dissolve it; a survey was started and
    stopped. Redo it (fields x users, `Runtime{` literals, by-value vs pointer) and
    pick the smallest worthwhile step (likely: pass `*Runtime`, build the Deps structs
    once in `cmd/relevo/wire.go`). (2) spec duplicates: the cockpit's live fetchers in
    `internal/ui/fetch.go` vs `view.Show`; `dash.shortTokens` vs `usage.ShortTokens`.
    (3) rename the check-command "gate" (relevo gate.go / repair.go) to "check" -- but
    never rename CLI flags, DB columns or JSON fields named gate.
- **Phase 3 still to do after phase 2:** the new packages (spawn cov 60, availability
  83, consult 12 -- they need package-level tests; capture, delivery, view,
  reporttail, roles), the relevo core, cmd/relevo, and the cockpit packages (after their split).
- Design changes from the spec (told to the owner, unchallenged): remote.go was
  **split in place**, not moved to `internal/remote` (two-way coupling, name cycle);
  `view` holds rendering + read-model types, while `Status` / `statusRow` / `Done`
  stay in relevo; `exec` was named `spawn`.

## 3. How a round runs (the method that worked)

1. Survey with an Explore agent first (callers file:line, what the moving code calls
   back into, Runtime fields, test helpers). Write a plan from that.
2. Plans live in `~/.cache/relevo-cleanup/plans` (the session scratchpad gets wiped).
   `p2-common.md` is the shared rules block for phase-2 plans; `p3-template.md` for
   phase-3 (placeholders @@PKGS@@ @@PKGLIST@@ @@BASE@@ @@SLUG@@). Every plan says:
   foreground only (a headless builder that backgrounds `make check` and ends its
   turn exits), keep `t.Parallel()`, goldens untouched, no new lint exclusions,
   golangci-lint v2.14.0 must actually run (contabo lacks it -> install), **do not
   rebase** (contabo worktrees have no origin), touch only the named files, moves are
   moves (no behaviour change), phase-3 hard targets (non-test lines no growth,
   comments <= half).
3. Send remote: `relevo bind --server contabo --base $(git rev-parse --short=8 origin/main) --name <n>`,
   then `relevo send`. Wait by polling the state dir
   (`~/.local/state/relevo/<n>/*-report.md`) or git -- installed relevo v0.14.0 holds
   the state lock during a remote `relevo wait` and blocks other planners.
4. Verify locally, cheaply: `git worktree add` the branch in the scratchpad, rebase
   onto origin/main (conflict helpers below), `go build ./... && go vet` of touched
   packages, `golangci-lint run --allow-parallel-runners` on touched packages,
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh`, measure the targets
   yourself, and **scope-check every changed file** (`git diff --name-only`) -- a
   builder once edited `scripts/test-shard.sh` out of scope and broke macOS CI.
   Never run the full race suite locally (laptop OOM); CI is the full check.
5. Merge **only** via `~/.cache/relevo-cleanup/merge-if-green.sh <pr>` (refuses
   unless every check on the current head passed/skipped and it merges cleanly).
   #578 was once merged red by hand and had to be reverted (#581).

Helpers in `~/.cache/relevo-cleanup/`: `rv.sh` (retry relevo on "busy"),
`resolve-lists.py <pkgs>` (.golangci.yml / allow-list conflicts: take main, drop the
pkgs), `resolve-baseline.py` (coverage-baseline conflicts: min per package, drop
deleted packages), `union-imports.py <files>` (Go conflicts that are import-only),
`merge-if-green.sh`.

## 4. Things to know

- Other planners work in the same repo (cockpit, CI speed, lock fixes). Real code
  conflicts with their merges (db #530/#541, store #541/#520, harness #549/#557,
  availability vs d4571f54) were handled by a rebase round or a small exact
  resolution that keeps their logic line for line -- verify by checking their added
  logic lines still exist.
- Merge races: #574 merged after #573 deleted a package it imported and broke main
  (fixed in #576). Since 2026-09-26 the `protect` ruleset is **active** on main:
  no deletion, no force-push, required checks `changes`, `lint`, `coverage`,
  `cross-compile`, and PR branches must be up to date with main (so rebase before
  merging). It has **no bypass actor**, so a direct push to main (the owner's
  release flow) is rejected until he adds one or releases through a PR.
- Issues filed along the way: #513 #514 #518 #521 (fixed by another planner), #580
  (test-shard.sh BSD awk with >1 split package), #505 (AI-first CLI).
- `scripts/test-shard_test.sh` sanity floor is now >100 relevo tests (it was >1000
  and broke when tests moved out).
- Don't `pkill -f` a pattern that also matches your own shell command.

## 4a. Session 2026-09-26 evening (architect-8) -- paused before starting

- Owner's order of work: (1) merge cl-p2-view-b, (2) Runtime survey + propose the
  smallest step before sending, (3) phase-2 leftovers, (4) cockpit split (propose
  first; only with no cockpit work in flight), (5) phase 3: spawn + availability
  (**not consult: A5 removes `ask`, owner said skip it**), relevo core, cmd/relevo.
- Owner said **wait entirely** until the A4/A5 work lands (architect-5's `a4-2a`,
  remote, touches internal/relevo, view, mcp; architect-3 waiting on A5). Nothing
  was sent or rebased. Resume when he says so.
- #599 already did "gate becomes check" for some names (availability/gates.go);
  check what is left of leftover (3) before planning it.
- `relevo status --all` is this planner's bindings including DONE, not every
  binding; find other planners' rounds with `relevo planner list` (bindings
  column) and `relevo status --name <n>`, or the state dirs by mtime.

## 5. First steps for the next session

1. Read this file and the spec.
2. Finish `cl-p2-view-b` per §3.4-5 (or re-send its plan
   `~/.cache/relevo-cleanup/plans/cl-p2-view-b.md` with a fresh base if it failed).
3. Ask the owner before resuming if other planners are busy; send at most the rounds
   the server can take.
4. Redo the Runtime survey, then plan the last phase-2 round(s).
