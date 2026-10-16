# MasterMind amendments (read first; these override the plan below)

1. The notice must also say how to clear a row whose chain is finished (done or
   stopped): its hint names both commands, e.g.
   `(relevo status --name <m>; relevo done <m> clears it)`. A member of a running or
   halted chain keeps today's hint only (the chain owns it; `done` on a running-chain
   member is refused). Put the hint choice in the same pure function family and add
   table rows for it.
2. Seed contradiction 1 is accepted: never suggest `relevo unbind --done <name>`.
3. Keep the CLI tests free of harness spawns and network (Remote nil or a stub), as
   the plan says.
4. Commit your work on the branch before writing the report (new commits only).

---

# Plan: "waiting on you" rows that no command can find (#1219)

## Root cause (verified live, read-only)

The notice and `status` read the same store through different shapes:

- The notice scans raw records. `WaitingOnYou` (`internal/relevo/waiting.go:30-55`)
  iterates `rt.Store.List()` with no MasterMind scope and no chain collapsing, so a
  halted `needs_you` chain member is listed with hint
  `relevo status --name <member>` (`internal/view/waiting.go:83-100`).
- Every status surface collapses members. `scopedStatus`
  (`internal/relevo/statusline.go:57-71`) runs `applyChains`
  (`internal/relevo/chain_status.go:142-201`), which replaces each member row with
  its chain's single row. `status --name <member>` then misses in `filterReport`
  (`cmd/relevo/status.go:177-182`, predicate `status.go:30-40`) and answers
  `binding_not_found`. The chain-name pre-check (`status.go:45-48,150`) only matches
  chain names, never member names, so members take the binding path and die there.
- `status --all` hiding them is the scope rule working as designed
  (`ScopeReport`, `internal/relevo/scope.go:26-46`; `narrowBindings`,
  `statusline.go:76-84`): the rows belong to stale owners, not the caller.
  `status --all-masterminds` (without `--all`) hides the chains because they are
  DONE (`chainStoreState`, `chain_status.go:103-116`); with `--all` it shows the
  chain row, never the member name. No status spelling can show the member name.

Live confirmation (sqlite, `mode=ro`, owner `""` is the CLI scope per
`internal/store/store.go:110-113` + `cmd/relevo/wire.go:167`): owner-`""`
`binding_record` rows `ch-transcr`, `ch-transcr-plan`, `ch-transcr-rev`,
`ch-status-rev` are `needs_you`; `chain_member` puts the first three in done chain
`ch-transcr` and the fourth in done chain `ch-status`. All four `status --name`
spellings therefore 404 while the notice repeats them.

Seed contradictions, stated not guessed around:

1. `relevo unbind --done <name>` is not a valid clear command: `--done` refuses
   any name (`cmd/relevo/unbind.go:85-90`). The existing clear commands are
   `relevo done <name>` and `relevo unbind <name>` (both `Load` by name with no
   scope: `done.go:38-53`, `bind.go:904-908`), followed by
   `relevo unbind --done [--all-masterminds]` once they are DONE (`gc.go:86-92
   touches only `StateDone`).
2. The issue's five-row example (`mm-r1..r3`, `si-fix`) is stale: those names now
   exist only under a non-CLI owner (`SHA256:…`) as ACTIVE, so no notice fires for
   them from this machine's CLI. The actionable set is the four `ch-*` rows above.
3. The halt text `zen: binding removed by the server admin` was written by an older
   binary; current code halts a 404 as `<name>: <server>: binding not found on the
   server (…)` (`internal/relevo/remote_sync.go:165-168`). Cosmetic only.

## Behaviour and cases

- `relevo status --name <member>` and positional `relevo status <member>`, where
  `<member>` belongs to a chain, answer that chain's report (chain row + member
  rows, the `ChainStatus` shape at `chain_status.go:244-274`; server chains keep the
  graceful shape at `chain_server_view.go:34-66`, where an unreadable server lands
  on the row's detail and member rows stay local). The notice's existing hint
  becomes true; the notice itself keeps listing the member (no silent hiding).
- Unknown non-member names still answer `binding_not_found` via the unchanged
  `filterReport` path. Scoped surfaces (`--all`, `--all-masterminds`, `--line`,
  `--chains`) are unchanged.
- Members of running/halted chains get the same resolution (they 404 today too).
- Clearing is pinned, not built: `done` on a done-chain member passes the
  running-chain refusal (`chain.go:53-57`), a remote 404 falls through to a local
  settle (`done.go:92-103`), a server-chain member settles locally without calling
  (`done.go:80`); `unbind` is likewise 404-tolerant (`bind.go:918-936`). After
  `done`, `WaitingOnYou` skips the row (`waiting.go:41`) and GC can archive it, so
  an acknowledged binding never reappears. `send`/`bind` notices need no separate
  change (same `warnWaitingOnYou`, `cmd/relevo/args.go:149-158`).
- The rule is a pure function in `internal/relevo` (member name + store/chain read
  → owning chain or none) with a table test; the new-condition test is named below.

## Ordered steps

1. Add the red regression test: fixture with a DONE chain + halted `needs_you`
   remote member asserting `status --name <member>` shows the member and the
   notice hint agrees — deliverable: failing test; worked: it fails with
   `binding_not_found` on current code.
2. Add the pure member→chain resolver in `internal/relevo` with a table test
   (member of done/running/halted/server chain → chain; non-member/missing →
   none) — deliverable: function + test; worked: `go test ./internal/relevo/`
   green and reverting the resolver reddens the table test.
3. Wire member resolution into `cmdStatus` at the `statusChain`/`isChain` branch
   (`cmd/relevo/status.go:45-48,150-169`) so member names take the `ChainStatus`
   path — deliverable: `status --name <member>` and `status <member>` print the
   chain report; worked: new CLI fixture test green with `Remote` nil (no harness,
   no network per CI constraint) and the existing status suites green.
4. Pin the clear path with stub-`Remote` tests: `done`/`unbind` by name on the
   halted fixture member with a 404ing server settle locally, then GC archives the
   DONE row — deliverable: regression tests; worked: green, and removing either
   404 fall-through reddens them.
5. Cover the server-chain member read: test that the member resolution shows local
   member rows with the server failure on the chain row's detail — deliverable:
   test, or a documented no-op if already covered; worked: green.
6. Save this plan to `docs/plans/2026-10-16-orphaned-waiting-rows.md`, run the full
   check, and commit the work as new commits (never amend/rebase a pushed branch)
   — deliverable: committed plan + code; worked: `make check` green and the plan
   file present in the commit.

Focused command: `go test ./internal/relevo/ ./cmd/relevo/ -count=1` (then the new
tests by name once written). Full command: `make check`. If `internal/relevo`
coverage drops, compare with `RELEVO_REQUIRE_COVERAGE=1`; regenerate the baseline
only via `sh scripts/check-coverage.sh --write` if code moves packages, and say so
in the report. Follow `CLAUDE.md` style (≤70-line functions, ≤600-line files, no
history in comments, no new lint exclusions).

## What is deleted (closed list)

1. `status --name <chain-member>` (and positional `status <member>`) no longer
   answers `binding_not_found`; it answers the member's chain report.
2. Nothing else is removed: no flags, verbs, or error codes change; scope, GC,
   sync, and reconcile semantics are untouched; old halt texts on disk are not
   rewritten.

## The report must include

- The root-cause seam chain with file:line, and the live rows that confirm it.
- The named red-then-green test plus the mutation check (reverted condition fails
  it), `make check` output, and any coverage-baseline handling.
- The three seed contradictions above and the verdict on each.
- What was deliberately left: scope filtering, `mastermind list`, halt-text
  migration, and the `SHA256:…`-owned ACTIVE mirror rows.
