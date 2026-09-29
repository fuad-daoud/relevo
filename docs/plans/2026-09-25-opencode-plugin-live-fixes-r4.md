# OpenCode plugin live fixes, round 4: ship rounds 2 and 3

Base: branch `relevo/oc-live` at `b520136`, plus the uncommitted round 2 and
round 3 changes in the worktree. Keep them exactly as they are. If a step
cannot be done as written, halt and report.

## Decision on round 3's halt

Round 3 halted because M1 and M2 (removing the `dialogOpen` key guards) did
not fail assertions 39, 40 or 47. The planner accepts that result.

With `focused={!store.dialogOpen}`, the pages do not hold focus while a dialog
is open, so the dialog's keys never reach the page handlers. The guards are a
backstop that no test can pin. M8, which removes the reactive focus, does
fail 47 and 48, so it is the focus rule that is pinned. The planner also
walked the build live:

- a scrolled report held its position across 20 s of polls;
- Tell the planner delivered `a [a] a` intact after an 8 s wait in the
  dialog;
- Tab worked straight after the dialog closed.

## Steps

1. In `tui.tsx`, put a comment of at most 3 lines directly above each of the
   two `if (dialogOpen) return;` guards. It says the guard is a backstop: the
   page yields focus while a dialog is open (`focused={!store.dialogOpen}`),
   so smoke assertions 47 and 48 pin the focus rule and not this line. Make
   no other code change.
2. Run `bash scripts/opencode-plugin-smoke.sh` once. It must print 48/48.
   Paste the final line.
3. Save the plans verbatim, copying each from its round file:
   - `~/.local/state/relevo/oc-live/002-plan.md` to
     `docs/plans/2026-09-25-opencode-plugin-live-fixes-r2.md`;
   - `003-plan.md` to `docs/plans/2026-09-25-opencode-plugin-live-fixes-r3.md`;
   - this round's `004-plan.md` to
     `docs/plans/2026-09-25-opencode-plugin-live-fixes-r4.md`.
4. Make one new commit, without amending: `fix(opencode): the binding page
   updates in place -- scroll and dialog focus survive polls; drop the 100 ms
   delay (#496)`.
5. Push with `git push origin relevo/oc-live`.
6. Comment on PR #503 with:
   - the smoke line;
   - round 3's mutation table (M10 fails 19; M8 fails 47 and 48; M1 and M2
     pass, a result accepted by the planner, with the reason above);
   - round 3's full-check result.

## Scope

`git diff --stat b520136` shows only:

- `tui.tsx`;
- `scripts/opencode-plugin-smoke.sh`;
- `scripts/testdata/opencode-plugin/relevo`;
- the three plan files.
