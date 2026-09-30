# Plan: placement S4 round 2 — finish the `:servers` round: the size split, the two pins, the commit

Round 1 of this binding implemented S4's steps 1–4 and 7 and verified them,
then halted at step 5 because `make check` failed on two defects the plan's
closed list did not cover, one of them a conflict inside the plan:

- **B1:** `internal/ui/actions.go` is 595 lines on HEAD and §1.2 put
  `mastermindActions.ServerProbes` there; §6 caps non-test files at 600 with no
  new exclusions. The implementation block (21 lines) pushes the file to 620.
- **B2:** the new `servers` command entry moves two pinned expectations outside
  §5: `internal/ui/overlays_test.go`'s overflow count and
  `internal/ui/testdata/command-real-132.golden`.

The omission was the plan's, not the round's. This round finishes the work:
the size split, the two expectation updates, one small audit narration the
round flagged, `make check`, the commit.

**Input:** the uncommitted round-1 work in this worktree (11 paths). Do not
revert or rewrite it; read the round-1 plan and report first. If the worktree
does not match the round-1 changed-paths list, halt and say so.

## 1. What changes

1. **`internal/ui/actions_servers.go`** (new) — move
   `mastermindActions.ServerProbes`, doc comment and body verbatim, out of
   `internal/ui/actions.go` into this file, following the existing
   `actions_audit.go` precedent exactly. The `Actions` interface method and its
   comment stay in `actions.go`. Done when `internal/ui/actions.go` is at most
   600 lines (expected 599) and `sh scripts/check-filesize.sh` prints ok.
2. **`internal/ui/overlays_test.go`** — the `:r`-style overflow expectation
   `"+ 17 more match; keep typing"` becomes `"+ 18 more match; keep typing"`
   (14 commands now; the fixture bindings are unchanged).
3. **`internal/ui/testdata/command-real-132.golden`** — regenerate with
   `go test ./internal/ui -run TestGoldenViews -update`, then read the diff:
   the only moved line should be the overflow footer `+ 33 more match` →
   `+ 34 more match` (`servers` matches `r`; the visible rows are unchanged).
   Re-run without `-update` to confirm stability. If any other line moves,
   stop and report it rather than accepting the golden.
4. **`internal/relevo/configaudit.go`** — round 1 flagged that
   `changeSubject`'s closed list has no `servers` arm, so a servers edit's
   audit and rollback-preview lines name a raw JSON path. Add the arm next to
   the `actors`/`agents` ones:
   `servers.<name>` → subject `server <name>`, and
   `servers.<name>.<field>` → the same subject with `field`.
   - Leave `configDocOf` untouched and say so in the report: it feeds
     `CheckDoc`'s dry run, which does not take a servers section, and the write
     path's `PutDoc` is the cross-check that refuses a bad servers document.
   - Pin it in `internal/relevo/configaudit_test.go`: a `servers.zen.url`
     change line's subject is `server zen`, field `url`; a `servers.zen`
     add/remove names the server alone.
5. **`docs/plans/2026-09-30-placement-s4-finish.md`** — save this plan and
   commit it.

## 2. Ordered steps (done-when)

1. The split (1) and the size script passes.
2. The two expectation updates; `go test ./internal/ui -count=1` passes.
3. The audit arm and its test; `go test ./internal/relevo -count=1` passes.
4. `make check` once, at the end: green.
5. Commit **all** round-1 and round-2 work in one commit on this binding's
   branch, message `feat(ui): a servers view, and the servers section is
   editable`. The commit carries:
   `README.md`, `internal/relevo/configedit.go`,
   `internal/relevo/configedit_test.go`, `internal/relevo/configaudit.go`,
   `internal/relevo/configaudit_test.go`, `internal/ui/actions.go`,
   `internal/ui/actions_servers.go`, `internal/ui/actions_test.go`,
   `internal/ui/cmdline.go`, `internal/ui/cmdline_test.go`,
   `internal/ui/view_rounds.go`, `internal/ui/view_servers.go`,
   `internal/ui/view_servers_test.go`, `internal/ui/overlays_test.go`,
   `internal/ui/testdata/command-real-132.golden`,
   `docs/plans/2026-09-30-placement-s4-servers.md`,
   `docs/plans/2026-09-30-placement-s4-finish.md`.
   `git status` clean afterwards.

## 3. Mutation pins

Each mutation must fail its named check, then be reverted.

- Move the implementation block back into `internal/ui/actions.go` (delete
  the new file) → `sh scripts/check-filesize.sh` fails on the 620-line file.
- Remove the `servers` arm from `changeSubject` → the new audit test fails.
- Remove `servers` from the command table → `TestGoldenViews` (or the overflow
  test) fails. This is round 1's pin, re-run once here to prove the updated
  expectations are load-bearing rather than blindly regenerated.

## 4. Files this round touches (closed)

1. `internal/ui/actions_servers.go` (new)
2. `internal/ui/actions.go`
3. `internal/ui/overlays_test.go`
4. `internal/ui/testdata/command-real-132.golden` (regenerated)
5. `internal/relevo/configaudit.go`
6. `internal/relevo/configaudit_test.go`
7. `docs/plans/2026-09-30-placement-s4-finish.md` (new, this plan)

The round-1 files are already in the intended state; only commit them. Halt
on anything outside this list plus the round-1 list.

## 5. Rules

- `make check` is the gate; run it once, at the end. On this server host it may
  take longer than on the laptop.
- Comments say why, never what; no issue numbers, no "round N", no spec
  citations in code.
- Functions ≤ 70 lines, non-test files ≤ 600; no new exclusions. Moving to
  `actions_servers.go` is the split, not an exclusion.
- Coverage: report whether a baseline moved; never lower one.
- The round-1 deviation (AddServer refuses an existing name) and its README
  note are accepted as written. No action.

## 6. What the report must include

- `git diff --stat` for the commit; the full hash and subject.
- The exact `command-real-132.golden` diff (must be the footer line alone).
- For each mutation in §3: the mutation and the failing check.
- `make check` result and coverage numbers.
- Confirmation that `git status` is clean and `actions.go` is ≤ 600 lines.
