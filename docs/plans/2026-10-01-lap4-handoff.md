# Handoff for the next chat: finish lap 4 (chain lab + fixes)

Written by MasterMind opencode-116 (`mm_nxra2hb3w34c`). Read with
`docs/plans/2026-10-01-chain-lab-wave3-handoff.md` (it now includes the lap-4
section) and the evidence indexes `~/.cache/chain-lab/evidence/w3/INDEX.md`
and `~/.cache/chain-lab/evidence/w4/INDEX.md` + `NOTES.md`.

## State

- All three boxes run `v0.15.0-36-gd5d86b61` (daemons; zen's `relevo-serve`
  process still the previous build -- the fix it carries is daemon-side).
  `origin/main` has moved on since (`5b1af52f` and further, other sessions).
- Merged and done: PR #831 (`#830` admitted-push read-back), deployed and
  live-verified; closed #830, #797, #798, #799, #800, #805. Filed #847.
- Open PRs (the lap-4 work):
  - **#845** `fix/chain-reader-outcome-804`, head `85ce6bf0`: a chain
    reviewer/security member round records `done` from its parsed block instead
    of `unstructured` (#804 leftover). CI: only the macOS shard 1/3 has failed,
    and only with the known #828 e2e flake.
  - **#848** `fix/lab-leftovers-w4`, head `d4660f3a` (after the CI fixes
    below): OOM requeue stores `PeakBytes`; `CreateBindingRequest.Regate` +
    client resolution + mirror + server `relevo.ResolveRegate`; `RemoveScratch`
    quiet skip (#821); `BindingFormat` bumped to 13 with the golden
    regenerated.
- The correction loop (wave-3 gap) is DONE: `w3-c8` ran
  build -> review `changes` -> correction plan -> builder correction -> review
  `pass` (`corrections: 1`) on `v0.15.0-38-g1b60f9a6`; evidence in w4. The
  trigger is a deterministic fixture (reviewer acceptance about the closing
  round kind) -- seven model-driven attempts had failed.

## Finish #845 and #848

1. If a job is red, check it is the #828 flake (chain e2e: `reviewer/security
   gave no verdict`, 0-byte stream, done marker present); if so rerun the
   failed jobs (`gh run rerun <run-id> --failed`) until green. Do not bisect
   the fixes: the flake reproduces 3/3 at the PRs' base commit on the laptop.
2. Merge: `gh pr merge <N> --squash --match-head-commit <head>` (#845:
   `85ce6bf0`, #848: `d4660f3a`).
3. Deploy the accumulated main:
   - laptop: `git checkout main && git pull`, then
     `GOTMPDIR=~/.cache/gotmp TMPDIR=~/.cache/gotmp make install` (the machine
     hits a /tmp tmpfs quota and TSan allocation limits otherwise; CI is the
     full check). The daemon re-execs (#371).
   - zen: `scp ~/.local/bin/relevo zen:~/.local/bin/relevo.new && ssh zen 'mv -f
     ~/.local/bin/relevo ~/.local/bin/relevo.prev && mv -f ~/.local/bin/relevo.new
     ~/.local/bin/relevo'`, then let the daemon re-exec (verify with
     `ssh zen 'relevo doctor'`). Do NOT restart `relevo-serve` if another
     session has live runners (it had two).
   - contabo: `cp ~/.local/bin/relevo ~/.local/bin/relevo-build` and
     `~/projects/servers/contabo/srv.fish deploy relevo-serve
     ~/.local/bin/relevo-build` (check `relevo doctor` first: 0 runners before
     restarting).
4. Live re-verify (evidence to w4):
   - #845: run one tiny chain after the deploy and read the reviewer member's
     report outcome -- it must be `done`, not `unstructured`
     (`relevo show <chain>-rev --round 1 --log`).
   - #848 regate: `relevo bind --server zen --regate 3 ...` on the lab repo,
     then check the mirror's record and the server's stored binding (`relevo db
     query` locally; on zen read its DB), then `relevo done`/`unbind` the probe.
   - #821: run `relevo done` on a chain whose readers shared the builder's
     tree; capture stderr and assert no `scratch worktree not removed`.
   - OOM peak: unit-level only (`TestOOMKilledLocalRoundBecomesQueued`); an
     OOM kill is not reproducible on demand.
5. Close #821 after the deploy (PR #848 fixes it).

## Still open (decide with the human)

- **#828** -- the e2e reader-close flake. It keeps blocking CI shards; the
  laptop reproduces it 3/3 locally at base. Not selected in the lap-4 scope;
  fixing it would stop the rerun loop. Suspect area: the fake harness's marker
  vs stream print and `holdReaderOnMarker`/`closeOnMarker` (S0 handoff has
  diagnostics in `internal/e2e`).
- **#847** -- the chain resume halt on `ErrReportPending`; needs a captured
  live state first (the issue has the predicate analysis and what to
  instrument).

## Traps / conventions learned

- `scripts/check-comments.sh` refuses issue numbers (`#NNN`) in comments --
  cite nothing, or prose only.
- Any new `store.Binding` JSON key path needs `BindingFormat` bumped and
  `go test ./internal/store -run TestBindingShapeMatchesFormat -update`.
- Local full `make check` can fail on machine resources (/tmp quota, TSan);
  targeted tests plus CI are the practical gate here.
- Zen: restarting `relevo-serve` kills live runners; swap the binary and let
  the daemon re-exec for daemon-side fixes. Contabo: `srv.fish` only.
- Lab housekeeping: wave-3/4 bindings are done; `relevo unbind --done` when
  the records are no longer needed. The wave-3 handoff and `evidence/w3`+`w4`
  are the record; `docs/plans/*chain-lab*` are untracked on purpose.
