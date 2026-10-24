# MasterMind override (wins over anything below)

- **No fallback root.** Drop step 5's `$XDG_RUNTIME_DIR` fallback: `/run/user/<uid>` is a small tmpfs too, and Go builds write large files under TMPDIR. When the computed TMPDIR would exceed the budget (an unusually long state dir), log one warning and proceed with the long path. The round never fails for it. Keep the warning as a pure, tested selection rule.
- Everything else stands. Keep the leaf derivation deterministic and the sweep in short-name space, as planned.

---

# Plan: short TMPDIR for served and local rounds and checks (#1367)

## Goal

Every TMPDIR relevo hands to a round or check process leaves room for a unix
socket: the longest TMPDIR relevo can hand out, plus a 40-byte socket suffix,
stays under Linux's 108-byte `sun_path` limit. Target: at most ~60 bytes on a
typical home, e.g. `/home/<user>/.local/state/relevo-serve/t/<8 hex>/<8 hex>`.
Today a served check TMPDIR reaches ~170 bytes
(`<serve state>/tmp/<64-hex owner>/checks/<long unit>` plus the test's own
`MkdirTemp` dir and socket name), so every socket test under it fails with
`bind: invalid argument`. Local rounds (`<state>/tmp/...` via
`cmd/relevo/wire.go`) get the same treatment.

## Behaviour and cases

1. Shorten both levels that make up a TMPDIR: the per-owner root (served) and
   the per-round / per-check leaf (served and local). Hashing the leaf also
   removes unbounded growth from long binding names (cap 32, `MaxAgentNameLen`
   in `internal/store/store.go:26-28`) and growing round numbers.
2. Keep what the current naming guarantees, and name how in the report:
   - Restart re-run finds the first run's directory: the short derivation is
     deterministic in the full long identity (owner + binding + round for a
     round; the check's unit for a check), never random per start, so a gate
     re-run and a round resume recompute the same directory.
   - Per-round and per-check directories stay unique: the derivation covers
     the whole identity that makes the long name unique; state the collision
     behaviour (hash width and what happens on a collision).
   - The tmp sweep (`internal/relevo/tmpsweep.go`) still finds and removes
     them: the keep-set is computed in short-name space through the same
     derivation helpers the prepare/remove routes use, so the sweep never
     re-spells a name. The `rounds/`/`checks/` subdirectory structure stays,
     so the sweep's per-rule walks and the loose-dir rule are untouched in
     shape. Old long entries left from before the change self-clean through
     the existing rules (unparseable/unlisted names are kept or aged out by
     the rules that own them; say which in the report).
   - Cleanup on close still runs: `removeRoundTmp` and `removeCheckTmp` keep
     calling the same `roundTmpRel`/`checkTmpRel` helpers, so they follow the
     rename with no separate mapping.
3. Long root fallback: when the computed TMPDIR would still exceed the budget
   (unusual long `XDG_STATE_HOME`), fall back to a short root under
   `$XDG_RUNTIME_DIR` (e.g. `$XDG_RUNTIME_DIR/relevo-t/...`), created through
   the store's confined helpers so user-mode tenant ownership is preserved;
   never `/tmp` (small tmpfs on these machines). When no usable runtime dir
   exists, proceed with the long path and log a warning: the round must not
   fail. No new failure mode is introduced by a long root.
4. Container mode is untouched: `TmpRoot` stays empty there and processes see
   the container's own `/tmp`.
5. A pure test pins the bound: the longest TMPDIR relevo can hand a served
   round or check (64-hex owner, longest valid binding name) and a local
   round or check, plus a 40-byte socket suffix, stays under 108 bytes.
6. Tests are pure functions and existing fakes only. No `cmd/relevo` test may
   spawn a harness; test the naming/selection rule as a pure function in
   `internal/relevo` (or `internal/serve` for the owner-root rule) instead.

## Seams

- `internal/serve/tenant_tmp.go:18-20` — `ownerTmpDir`: today
  `<cfg.Root>/tmp/<hex>`; becomes the short per-owner root (e.g. `t/<short>`).
- `internal/serve/serve.go:379-436` — `runtimeAt`: sets `rt.TmpRoot` (line
  415) from `ownerTmpDir`, `MkdirAll`s it, and hands it to `withTmpDir`
  (`tenant_tmp.go:45-73`), which also points every process without its own
  `TmpDir` at the owner root: that default gets short automatically.
- `internal/relevo/roundtmp.go:17-19` — `roundTmpRel`: today
  `rounds/<name>-NNN`; becomes `rounds/<short deterministic id>`.
- `internal/relevo/procprep.go:67-95` — `prepareTmp`: joins root and rel via
  `Store.EnsureTmpEntry`; `101-103` `checkTmpRel` (`checks/<unit>`) becomes
  `checks/<short deterministic id>`; `110-117` `removeCheckTmp` follows via
  the same helper.
- `internal/relevo/headless.go:88-109` — `scopeOwner8`, `scopeUnitNameFor`:
  unit names stay long (scope/systemd naming is unchanged); they become the
  *input* to the check-leaf derivation, not the leaf itself.
- `internal/relevo/served_check.go:274-279` (`servedCheckSpec`) and
  `internal/relevo/chain_check.go:488-493` (`chainCheckSpec`) plus
  `internal/relevo/gate_proc.go` (unit as `procRequest.key`): units flow
  unchanged into `prepareTmp`/`removeCheckTmp`; only the rel derivation
  shortens.
- `internal/relevo/tmpsweep.go:50-53` (`tmpSubRounds`/`tmpSubChecks`),
  `:154-200` (`sweepRoundDirs`, parses names via `parseScratchName`),
  `:211-242` (`sweepCheckDirs`), `:296-310` (`tmpOpenCheckUnits`, builds the
  keep-set from spec helpers): the keep-set must be derived in short-name
  space; round sweeping can no longer parse the binding out of an entry name
  and must match entries against expected short names instead.
- `internal/store/tmpdirs.go:45-134` — `EnsureTmpEntry`, `RemoveTmpEntry`,
  `ListTmpEntries`: confinement and tenant-chown behaviour unchanged; the
  fallback root must also be created through these (or the same chown path)
  so user-mode tenants can write it.
- `cmd/relevo/wire.go:399-409` — local `TmpRoot` (`store.TempDir(root)`,
  `internal/store/store.go:200-216`): the root stays `<state>/tmp`; the
  leaves shorten through the shared helpers, and the long-root fallback
  applies here too.
- Existing pinning tests to update to the new rule (not to delete):
  `internal/serve/tmproot_test.go`, `internal/relevo/procprep_test.go`,
  `internal/relevo/roundtmp_routes_test.go`,
  `internal/relevo/checktmp_routes_test.go`,
  `internal/relevo/tmpsweep_test.go`.

## Steps

1. Add the short-derivation helpers, the byte-budget constant, and the
   long-root fallback selection as pure functions beside the tmp helpers;
   deliverable is the new derivation with no callers yet, verified by a
   focused `go test` on the owning package.
2. Rewire `ownerTmpDir` to the short per-owner root; deliverable is served
   `TmpRoot` values at most ~60 bytes on a typical home, verified by the
   updated `TestRuntimeAtTmpRootPerOwner` passing.
3. Rewire `roundTmpRel` and `checkTmpRel` to the short deterministic leaves;
   deliverable is short per-round/per-check dirs for served and local
   runtimes, verified by the updated prepare/round/check route tests passing.
4. Rewire the sweep keep-set (`sweepRoundDirs`, `tmpOpenCheckUnits`) to
   short-name space and keep `removeRoundTmp`/`removeCheckTmp` on the same
   helpers; deliverable is stale short entries removed and live ones kept,
   verified by the updated `tmpsweep_test.go` passing.
5. Wire the long-root fallback (short root under `$XDG_RUNTIME_DIR`, never
   `/tmp`; warn-and-proceed when none is usable, round never fails);
   deliverable is the fallback path with tenant ownership preserved,
   verified by a focused `go test` on the selection rule.
6. Add the pure length-bound test (longest served/local TMPDIR for a 64-hex
   owner and longest binding name, plus a 40-byte socket suffix, under 108);
   deliverable is the pinning test, verified by it failing when the
   derivation is temporarily reverted (mutation check) and passing with it.
7. Run the gate `make check-affected` for the touched packages until green;
   the full `make check` stays the CI leg, not this round.

## Deleted

1. The served owner root construction `<serve state>/tmp/<64-hex owner id>`
   as a handed-out path (`ownerTmpDir`, `internal/serve/tenant_tmp.go:18`).
2. Long on-disk TMPDIR leaves: `rounds/<binding>-NNN` and
   `checks/<unit>` as path segments (`roundTmpRel`, `checkTmpRel`); the long
   unit/binding strings survive only as derivation inputs, never as paths.
3. Entry-name parsing as the sweep's round-matching rule in
   `sweepRoundDirs` (`internal/relevo/tmpsweep.go:154-200`, `parseScratchName`
   use there), replaced by matching entries against expected short names;
   `parseScratchName` itself stays for the scratch routes that still need it.
4. No exclusions are added to get green (`.golangci.yml`,
   `scripts/check-comments.sh`, `scripts/check-filesize.sh`); a finished
   package's entries are removed, and if code moves between packages the
   coverage baseline is regenerated with
   `sh scripts/check-coverage.sh --write` and said so in the report.

## Report must include

- `git diff --stat` compared against this plan's declared scope.
- `make check-affected` result (the gate the round is held to).
- The measured longest TMPDIR for served and local, plus the 40-byte suffix,
   against the 108-byte limit.
- How each kept guarantee is kept (restart findability, uniqueness +
   collision behaviour, sweep coverage, close cleanup) and what the old long
   entries do after the change.
- The long-root decision taken (fallback path, never `/tmp`) and the
   warn-and-proceed case.
- The mutation check: reverting the derivation fails the length test by
   name.
