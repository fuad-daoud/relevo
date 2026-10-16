# MasterMind amendments (read first; these override the plan below)

1. MECHANISM CHANGE. Do not extend `lockOwnerSection` to the prune, do not move the
   prune out of `admitMu`, and do not put absorbs/InitBare/resume under the owner
   section. Every lane one client runs is the same owner, so holding the section
   across a bundle absorb would serialise all of that owner's binds and round starts
   behind slow git I/O, and re-ordering the tick's locks across ~8 sites is a deadlock
   risk for a release. Build a bare-repo pin instead:

   - A small server-side pin set (map bare path -> count) guarded by its own mutex.
     That mutex is a leaf: nothing else is acquired while it is held.
   - A bind path pins the bare repo BEFORE it creates or absorbs into it, and unpins
     only AFTER the row that references it is saved (or on every error path -- use
     defer). Paths: plain bind (`buildServedBinding` InitBare -> `handleCreateBinding`
     save), chain create (`chainInitAndAbsorb` -> `ServedChainCreate` row write),
     resume (`handleResume` checkout of a released/archived binding). Round start on
     a live binding needs no pin (its row already references the repo) -- confirmed by
     reading `unusedRepos`: it counts a repo as referenced when any binding
     `Store.List()` returns names it via `Serve.BareRepo` (or `Repo`), and List drops
     only archived rows, so a round start's row always counts. No live-but-not-counted
     state exists on that path.
   - `pruneUnusedRepos` takes the pin mutex for each owner's pass: under it, it lists
     the rows, applies `unusedRepos`, skips every pinned path, and deletes. Because a
     bind unpins only after its row is saved, the prune sees either the pin or the
     row -- never neither.
   - The prune stays where it is in `Tick` under `admitMu`; the lock order is
     admitMu -> pin mutex, and bind paths take the pin mutex alone. Cross-owner
     concurrency and every existing test stay as they are.

   The plan's owner-section approach is the rejected alternative, for the reason
   above: section-across-absorb serialises an owner's binds behind slow git I/O, and
   re-ordering the tick's locks is a release-risk deadlock.

2. The deterministic tests keep the plan's shape (seam between worktree creation and
   row save on chain create; between InitBare and save on plain bind; resume since it
   is pinned): fail before, pass after, fail again with the pin check in the prune
   removed.

3. Never `git stash`. To show fails-before, copy the changed production files aside:
   the runs below replaced only `internal/serve/pins.go` (no-op pin / check-always-
   false), leaving seams and tests in place.

4. Commit the work on the branch before writing the report (new commits only).

---

# Plan: serve's unused-repo prune must not delete a bare repo a new bind is using

## 1. Behaviour

`relevo serve`'s tick deletes a bare repo (`repos/<owner>/<repoid>.git`) while a
concurrent bind is between "created or absorbed into that bare repo" and "binding row
saved that references it". The saved binding then points at a deleted repo; the first
git command in the worktree fails with `fatal: gitfile does not point to a valid
repository` (seen as `TestChainServerE2E` failing at
`internal/e2e/chain_server_test.go:173`, `git add hello.txt`, log line `pruned unused
bare repo` one second before `admitted`).

The fix orders the two sides with a bare-repo pin (`internal/serve/pins.go`), not with
a lock the bind holds across git I/O:

- A bind path pins the bare repo before it creates or absorbs into it and unpins only
  after the row that references it is saved; defer covers every error path.
- `pruneUnusedRepos` keeps its place in `Tick` under `admitMu`, and holds the pin
  mutex across each owner's list-decide-delete pass, so the row read and the pin
  check are one step.
- The prune's purpose is unchanged: a bare repo no live binding references and
  nothing pins is still deleted, and empty owner repo dirs are still removed.

Lock order after the fix: `admitMu` -> pin mutex -> (store/db reads), and bind paths
take the pin mutex alone. The pin mutex is a leaf: nothing is acquired while it is
held, so a pin never queues a bind behind git I/O, an owner section or admitMu.
Cross-owner concurrency is unchanged: one owner's bind never blocks another owner's
poll, and the only cross-owner wait a pin can add is bounded by one owner's prune
pass.

## 2. Seams (files, types, functions)

Prune side (deletes):

- `internal/serve/cleanup.go` `unusedRepos` -- pure liveness predicate; unchanged,
  reused by the new tests.
- `internal/serve/cleanup.go` `pruneUnusedRepos` -- walks `repos/<owner>`, resolves
  each owner's runtime, and hands the pass to `pruneOwnerRepos`; the empty-owner-dir
  removal stays part of the pass.
- `internal/serve/cleanup.go` `pruneOwnerRepos` -- one owner's pass under the pin
  mutex: `Store.List`, `unusedRepos`, skip `pinnedLocked` paths, `os.RemoveAll` the
  rest.
- `internal/serve/daemon.go` `Tick` -- unchanged: `collectSettled` ->
  `pruneUnusedRepos` -> `admitLocked` still run in that order under one `admitMu`
  hold.
- `internal/serve/serve.go` `admitMu` comment -- documents the extended order.

Pin set:

- `internal/serve/pins.go` -- `barePins` (map clean path -> count under its own
  mutex), `pinBareRepo` (returns the release; once-guarded so a double release is
  harmless), `pinnedLocked` (caller holds the mutex).

Bind side, chain create (bare before row):

- `internal/serve/chains.go` `handleCreateChain` -- pins `bare` before
  `chainInitAndAbsorb` (which `InitBare`s and absorbs) and unpins when the handler
  returns, i.e. after `ServedChainCreate` has written the rows (or on any failure).
- `internal/serve/chains.go` `chainFinishCreate` -- unchanged apart from the test
  seam (below); the owner section, re-check, branch cut, row write and `admit` keep
  their shape.

Bind side, plain bind (bare before row):

- `internal/serve/bindings.go` `handleCreateBinding` -- takes the pin release from
  `buildServedBinding` and defers it, so the unpin lands after the `WithLock` save
  and on every error path.
- `internal/serve/bindings.go` `buildServedBinding` -- pins `bare` immediately before
  `InitBare`; a failed init releases the pin itself.

Resume:

- `internal/serve/resume.go` `handleResume` (moved out of `bindings.go`, which sat
  against the 600-line ceiling) -- pins `Serve.BareRepo` before `CheckoutWorktree`
  and unpins when the handler returns, after the re-activation save.

Round start -- deliberately unpinned: the binding's row is live for the whole
absorb/checkout window (see the amendment's confirmation), so the prune already
spares its repo.

Other bare-repo users, verdicts:

- Unbind ref release (`releaseServedRefs` via `handleUnbind`): a prune that already
  removed the repo only turns its git calls into warned-and-continued errors, and the
  row is already archived; unchanged.
- `gcArchive` (abandoned-binding GC): same path as unbind; unchanged.
- `collectSettled` / `teardownServed`: runs beside the prune in the tick; unchanged.
- Resume checkout: pinned (above).
- Client-side pulls (`SyncRemote`, `internal/sync`): touch the client's copy, never
  the server's bare repos; out of scope, and `internal/sync` is not edited.

Test-only seams (package-level vars, nil in production):

- `internal/serve/chains.go` `chainCreateSeam` -- between the worktree cut and
  `ServedChainCreate`.
- `internal/serve/bindings.go` `bindCreateSeam` -- between `buildServedBinding` (the
  init included) and the save in `handleCreateBinding`.
- `internal/serve/resume.go` `resumeSeam` -- between `CheckoutWorktree` and the save.
- `internal/serve/chains_resume.go` -- `writeChainResumeError` and
  `resumeOptionsFromWire` moved here from `chains.go` to keep it under the 600-line
  ceiling; no behaviour change.

## 3. Cases

1. Chain create raced with the tick: worktree checked out, row not yet written, tick
   runs -- bare repo survives, create returns 201, worktree opens in git.
2. Plain bind raced with the tick: `InitBare` done, row not yet saved, tick runs --
   bare repo survives, create returns 201 with a working `BareRepo` the saved row
   names.
3. Resume raced with the tick and the row archived mid-checkout: bare repo survives,
   resume answers 200.
4. Prune purpose intact: a bare repo referenced by no live binding and pinned by
   nobody is still deleted by the tick; empty owner repo dirs are still removed
   (`TestTickPrunesAnUnusedRepo`, `TestTickPruneRemovesTheEmptyOwnerDir` stay green).
5. No deadlock: the full `internal/serve` suite is `-race` clean; the pin mutex is a
   leaf, so no cycle can close through it.
6. Cross-owner isolation intact: `TestSlowOwnerTickDoesNotBlockAnotherOwner` stays
   green; the prune's pin hold adds no owner section and no admitMu re-entry.

## 4. Ordered steps (as executed)

1. Add `internal/serve/pins.go` -- the pin set and its release.
2. Section the prune per owner -- `pruneOwnerRepos` holds the pin mutex across list,
   filter, skip and delete; `pruneUnusedRepos` walks owners outside it. The prune
   stays inside `Tick`'s `admitMu` hold.
3. Pin the bind windows -- chain create, plain bind and resume as in section 2; every
   pin is released by defer, after the row save on the success path.
4. Add the deterministic race tests -- `internal/serve/prune_race_test.go`:
   `TestTickPruneSparesAChainCreateBetweenWorktreeAndRow`,
   `TestTickPruneSparesABindBetweenInitBareAndSave`,
   `TestTickPruneSparesAResumeBetweenCheckoutAndSave`. Each parks its request on the
   seam, runs the tick's prune mid-park, releases, and asserts the bare repo
   survived and the response is a 201/200 whose repo still works in git.
5. Run the gates -- `make check` and `go test ./internal/serve/ -race -count=1`.
6. Save this record to `docs/plans/2026-10-16-serve-prune-bind-race.md`, committed
   together with the code.

## 5. Deleted behaviour (closed list)

1. The prune's unconditional delete of every repo `unusedRepos` names -- it now
   spares pinned paths; nothing else about the delete semantics changes.
2. Nothing else: `unusedRepos`, the tick's order, the owner sections, and every
   existing test's pinned behaviour stay.

## 6. Verification record

- fails-before: with a no-op pin (only `pins.go` swapped, seams and tests in place)
  all three new tests fail on `bare repo pruned mid-...`, with the prune's
  `pruned unused bare repo` log line beside them.
- pass: with the fix, all three pass (`go test ./internal/serve/ -run
  TestTickPruneSpares -race -count=1`).
- guard-removed mutation: with the prune's `pinnedLocked` answering false, all three
  fail again.
- `make check` exits 0; `go test ./internal/serve/ -race -count=1` passes; the e2e
  witness `TestChainServerE2E` is left unedited.

Rejected alternatives, recorded with their reasons:

- The plan's owner-section approach: extending `lockOwnerSection` to the prune and
  putting absorbs/InitBare/resume under the section serialises an owner's binds and
  round starts behind slow git I/O (every lane one client runs is the same owner),
  and re-ordering the tick's locks across ~8 sites is a deadlock risk for a release.
- Re-checking liveness under the store lock alone: an in-flight bind has saved
  nothing yet, so the re-check still sees an unused repo.
- An mtime grace period for young bare repos: timing-based, not deterministic, and it
  only narrows the window.
- Taking `admitMu` in the bind paths: serialises every owner's bind behind the
  server-wide cap lock and contradicts the per-owner design `lock_test.go` pins.
