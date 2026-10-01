# Plan: #822 — the live row tells the truth

Issue #822. Builder binding `status-activity`, branch `relevo/status-activity`, off `origin/main` (`49571e73`). One builder, new commits only — never amend or rebase — ending with `make check` green. Line references are as of `49571e73`; if a step contradicts the code, halt and report.

## Behaviour

1. **One rule.** `view.ActivityWord(b BindingStatus) string`, beside `phase()`: `working` → `working`, or `quiet X` when `QuietFor` is set (the progress sampler has gone silent); `stalled X`, `exploring X`, `gating X`, `exited N`, `exited`, `queued (…)` (the bare `queued` too), `running` → verbatim; `idle`, `unknown`, `""` and every other word (`unreachable`, `auth: …`, `cert`, `closed`, `gone`) → `""`.
2. **Where it shows.** In `rowStatus` the word takes the phase slot when the round is in flight (`RoundStart` non-zero **and** `RoundEnd` zero) and the word is non-empty. Tone stays `phase` (dim). Precedence is untouched: `NEEDS YOU` (including a stalled pending report), `REPORT IN`, `QUESTION IN`, `PAUSED`, `HELD`, `DONE` and the `Display` words still win; a closed round, a row with no round, or an empty word still reads its phase (`prompt sent`, `report in`, `answered`, `no prompt yet`). Chain rows keep their own status column. No spinner, no animation.
3. **The live diff.** `StatusLineRow` gains `Live *LiveDiff` (`json:"live,omitempty"`), copied from `BindingStatus.Live`; when non-nil the middle appends `· +A/-R in F` (` (shared)` when `LiveDiff.Shared`) before the token cell: `○ webshop  r4 · builder on opencode · +142/-8 in 6 · 3.1k tok   quiet 3m   4m`. No round open / no baseline / git failed → `Live` nil → byte-identical to today.
4. **JSON additive only.** `rows[].live` appears only when carried; every existing key is unchanged. The contract goldens (`cmd/relevo/testdata/contract/statusline*.golden`) stay byte-identical: no live fixture is added in `cmd/relevo` (its fixture deliberately sets no `RoundStartedAt`/`Progress`); live cases are pinned in `internal/view`.
5. **Cockpit.** `internal/ui`'s `rowNow` takes its working word from `view.ActivityWord` (falling back to `b.BuilderStatus` when the rule is empty, so an `unknown` row still says `unknown`) and its private copy of the rule is deleted. Every `internal/ui` golden stays byte-identical.
6. **OpenCode sidebar.** Line B appends the same diff segment (before the tokens, mirroring the statusline); line A already takes the word from `row.status`/`row.tone`, so the rule reaches it through the document.
7. **`relevo status` unchanged — decision 5's premise, stated.** Decision 5 says the runner line "already prints the activity word": true for every definite word (it prints `b.BuilderStatus`), false for a quiet working row, where `BuilderStatus` is `working` while `ActivityWord` says `quiet X`. Since decision 5 also keeps the layout and forbids a second rendering, this plan reads it as vocabulary consistency: `render.go` output stays byte-identical, and a test pins that the runner line's word equals the shared rule's word for each definite word and that `quiet X` stays on the round line. Switching the runner line to `ActivityWord` (one line, but `quiet X` would then appear twice) is a deviation to report, not to do silently.
8. **The clock is not touched.** The acceptance's `4m 02s` is an example; `AgeText` renders whole minutes (`4m`). Do not add seconds.

## Seams

| File | Function / type | Lines | Change |
|---|---|---|---|
| `internal/view/statusline.go` | new `ActivityWord` | after `phase()` :186 | the rule + a why-comment |
| `internal/view/statusline.go` | `rowStatus` | :208-238 (phase :233-237) | activity word in the phase slot while in flight |
| `internal/view/statusline.go` | `StatusLineRow` | :340-380 | `Live *LiveDiff` after `Tokens` (:359) |
| `internal/view/statusline.go` | `renderedStatusLineRow` | :96-108 | diff segment after `Reason`, before `Tokens` |
| `internal/view/statusline.go` | `statusLineRowOf` | :461-482 | `Live: b.Live` |
| `internal/view/statusline_test.go` | new tests | — | `TestActivityWord`; in-flight/closed/no-start; live row; JSON live present/omitted |
| `internal/ui/fleet_group.go` | `rowNow` | :105-159 (rule :116-131) | call `view.ActivityWord`; fallback `b.BuilderStatus`; round age append unchanged |
| `internal/ui/fleet_test.go` | new case | — | `rowNow` quiet word and `unknown` word |
| `internal/harness/opencodeplugin/tui.tsx` | sidebar line B | :811-818 | same segment before tokens |
| `internal/view/render.go` | `writeBuilderLine` | :150-185 | none; consistency test only (decision-5 note) |
| `README.md` | "Status line" | :1160-1192 | the word while a round runs, the live diff, the `live` JSON field |
| `docs/specs/2026-10-01-statusline-activity-design.md` | new | — | amends `docs/specs/2026-09-24-statusline-redesign-design.md` |
| `docs/plans/2026-10-01-statusline-activity.md` | new | — | this plan, committed verbatim |

No other file. `internal/relevo/*` already computes every fact; `cmd/relevo` and `internal/serve` are untouched; the OpenCode smoke fixtures and scripts are untouched.

## Steps

1. `git fetch`, branch `relevo/status-activity` off `origin/main`, run `go test ./internal/view/ ./internal/ui/ -run 'Statusline|Activity|Fleet' -count=1` once for the floor. Branch at origin/main, clean tree.
2. Add `ActivityWord` (doc comment explains the definite words, the quiet substitution, the empty return); add `TestActivityWord` over every word plus quiet / idle / unknown / "". Check: `go test ./internal/view/ -run TestActivityWord -count=1`.
3. Wire `rowStatus`: in-flight gate, activity word in the phase slot; add cases for quiet, stalled, closed round, no `RoundStart`, in-flight NEEDS YOU. Check: `go test ./internal/view/ -run 'Statusline|Activity' -count=1` — the existing `prompt sent` expectations still pass (their fixtures carry no `BuilderStatus`).
4. Add `StatusLineRow.Live`, the `statusLineRowOf` copy, and the render segment (+` (shared)`); add the live-row and JSON tests (present with the segment, absent when nil). Check: same focused command, and the exact stripped line contains `+142/-8 in 6 · 3.1k tok`.
5. Replace `rowNow`'s private selection with `view.ActivityWord` + `b.BuilderStatus` fallback; add the quiet/unknown case. Check: `go test ./internal/ui/ -run Fleet -count=1` and `git status --porcelain internal/ui/testdata` empty.
6. Commit 1 (`feat(statusline): the row shows the runner's activity and the round's live diff`) covering steps 2-5; split into two commits if cleaner. No amend.
7. Plugin: insert the live segment before the tokens in line B. Check: for `{files:6,added:142,removed:8}` the constructed string carries `+142/-8 in 6` before the token cell; `git diff` touches no other plugin expression. Commit 2.
8. Write the new spec (the rule, precedence, JSON field, surfaces, what stayed), update README `:1160-1192`, commit this plan verbatim as `docs/plans/2026-10-01-statusline-activity.md`. No `#822` or `§` in Go comments (`scripts/check-comments.sh`). Commit 3.
9. Focused: `go test ./internal/view/ ./internal/ui/ -run 'Statusline|Activity|Fleet' -count=1`. Mutation-check each condition: break the quiet branch, the in-flight gate, the `Live` copy — the named test must fail each time.
10. `make check` once. If coverage for `internal/view` complains, regenerate with `sh scripts/check-coverage.sh --write` and say so (never lower a baseline). If any contract or `internal/ui` golden moved, halt and report.

## Deleted (closed list)

1. `internal/ui/fleet_group.go`, `rowNow`, the groupWorking word selection — the `BuilderStatus != "working"` / `QuietFor` / `working` if-else (~:118-125) — deleted, replaced by `view.ActivityWord`.
Nothing else is deleted. No test is removed or weakened, no golden changes, no `BindingStatus` field is removed, no surface loses a fact. The in-flight phase word is *replaced*, not deleted: `phase()` stays for the closed, no-round and empty-word cases.

## Report must include

- The before/after statusline line for a working builder, exact bytes with SGR stripped (before `… · 3.1k tok   prompt sent   4m`, after `… · +142/-8 in 6 · 3.1k tok   quiet 3m   4m`).
- The `--line --json` row fragment for a live row.
- Files changed (`git diff --stat`), commits, commands run.
- Confirmation that `cmd/relevo/testdata/contract/statusline*.golden` and `internal/ui/testdata/*` are unchanged.
- Every deviation, including the decision-5 reading and any coverage-baseline regeneration.

## Halt

- An `internal/ui` or contract golden moves.
- A needed fact is absent on a row (e.g. `QuietFor` never set on remote rows) so the word cannot be derived from the row: halt rather than invent a source.
- `make check` demands a coverage-baseline lowering.
