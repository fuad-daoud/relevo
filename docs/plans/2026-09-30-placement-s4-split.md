# Plan: placement S4 round 3 — split the servers view under the line cap, amend, gate green on the committed tree

Round 2 committed all of S4 as `912e9f9` exactly as ordered, and the commit is
correct — but the committed tree fails `make check`:

```
internal/ui/view_servers.go: 603 lines (max 600)
```

The blind spot was the plan's: `scripts/check-filesize.sh` scans
`git ls-files`, so a new untracked file is invisible to it. Round 1's
`view_servers.go` (603 lines) escaped round 1's gate and round 2's pre-commit
gate, and only the commit made it visible. Round 2 correctly refused to change
a round-1 file outside its closed list. This round fixes the file.

**Input:** the committed tree `912e9f9` on this binding's branch (clean). Do not
revert or rewrite anything else; round 1's and round 2's work stands.

## 1. What changes

1. **Split `internal/ui/view_servers.go`** — move one self-contained chunk
   (the add/edit form, or the probe command; whichever divides cleanly) into a
   new file next to it: `internal/ui/view_servers_form.go` or
   `internal/ui/view_servers_probe.go`. Move the code **verbatim**; the only
   edits inside the moved block are the package clause and imports. No
   behavior, rendering or key change, and no test change: `view_servers_test.go`
   stays byte-identical and must stay green.
   - Done when `internal/ui/view_servers.go` is comfortably under 600 (aim
     ~500 or less, not 599) and the new file is well under it too.
2. **`docs/plans/2026-09-30-placement-s4-split.md`** — save this plan; it is
   amended into the same commit.
3. **Amend `912e9f9`** — this is the one authorized exception to "no amend":
   the branch is local to `zen` and has never been pushed, so no other consumer
   exists. Stage the split file, the new file and this plan, then
   `git commit --amend --no-edit`, so the branch still carries exactly one
   commit for the whole S4 change. If amend fails for any reason, a follow-up
   `fix(ui): split the servers view under the file-size cap` commit is the
   accepted fallback — say in the report which was used.

## 2. Ordered steps (done-when)

1. The split; `gofmt -l` clean on both files.
2. Stage the two Go files, the new plan, and run `sh scripts/check-filesize.sh`
   → it must be ok **with the index containing both files** (that is the
   condition round 2 could not satisfy).
3. `make check` → green.
4. Mutation pin (§3) applied, observed, reverted, and the files re-staged.
5. `git commit --amend --no-edit`; then on the committed, clean tree:
   `sh scripts/check-filesize.sh` → ok, and `git status --porcelain` empty.
6. Report; do not push (the MasterMind opens the PR).

## 3. Mutation pin

- Re-unite the split: paste the moved chunk back into
  `internal/ui/view_servers.go` (keeping the new file, temporarily) so the
  file is over 600 → `sh scripts/check-filesize.sh` must fail naming it.
  Restore the split, re-stage, and re-run the script to green. This proves the
  split is what keeps the gate green, not a stale index.

## 4. Files this round touches (closed)

1. `internal/ui/view_servers.go` (shrinks)
2. `internal/ui/view_servers_form.go` or `internal/ui/view_servers_probe.go` (new)
3. `docs/plans/2026-09-30-placement-s4-split.md` (new, this plan)

Plus the already-committed files, unchanged, carried by the amend. Halt on
anything outside these three.

## 5. Rules

- `make check` is the gate; run it once, after staging, before the amend.
- No behavior change; comments stay with the code they explain; no issue
  numbers or spec citations in code.
- Non-test files ≤ 600, enforced against the index: after staging, both files
  must be under.
- Coverage baselines: nothing should move; report if anything does.
- Do not touch round 1's or round 2's other files.

## 6. What the report must include

- The amended commit hash and subject, and `git show --stat HEAD`.
- The line counts of both files with the index staged, and the
  `check-filesize` result on the committed tree.
- The mutation pin: what was moved back, the failing script output, and the
  restored green run.
- `make check` result and coverage numbers.
- Whether the amend or the fallback commit was used, and `git status` empty.
