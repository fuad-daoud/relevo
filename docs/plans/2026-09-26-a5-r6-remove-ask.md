# A5 R6: remove `relevo ask`

Owner decision, A5: **`relevo ask` is removed.** Reader output comes only from bound reader
actors (`relevo bind --actor <reader>` + `relevo send`), which R3-R5 built; their output
is `NNN-<actor>/summary.md` plus files, read with `relevo show --summary` and
`--artifacts`, and the cockpit's artifacts tab. `ask --round N` (a question to a closed
round's own session) goes too, with no replacement (owner confirmed).

**`send --verify` must keep working.** It runs the reviewer through `consult.StartVerify`
and `consult.Reconcile`, **not** through `ask`. The verify path and the stored consult
records stay.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. The deletion list (closed)

Delete **exactly** these. Anything not on this list stays.

1. `cmd/relevo/ask.go`: the whole file. The verb's dispatch in `cmd/relevo/main.go`
   (~line 172) and its usage line (~line 48) go too.
2. `internal/relevo/ask.go`: the whole file (`CheckAskInput`, `Ask`, `askRound` and
   helpers).
3. In `internal/consult/ask.go`:
   - `Spawn`, `reserve`, `start`, `save` and `record`;
   - `ErrNotAConsultRole` and `ErrTreelessUnsupported`;
   - `ErrConsultCap`, **only if** `StartVerify`'s path does not use it.
4. In `internal/consult/`: `RoundQuestion`, `roundAskPrompt`, `RoundRole`, and
   `headlessPrompt` plus its inline cap (prompt.go ~13-61), **only if** verify does not
   use them.
   - **Rule for items 3 and 4:** delete, run `go build ./...`, and restore exactly what
     the verify path still needs.
   - The known survivors are `Running`, `consultCap`, `RandomID`, `newID`, `brief`,
     `Reconcile`, `finishConsult`, `applyVerdict`, `StartVerify` and
     `VerifyDiffCommand`.
   - List every function you kept that is on this list, with the reason.
5. `internal/relevo/names.go` (~12-40), the `ConsultRolesTooLong*` checks;
   `cmd/relevo/candidates.go` (~18-36); and their calls in `cmd/relevo/bind.go`
   (~294, ~393). This is the "name too long for the researcher consult role" note.
   **Keep** it if the verify path (`.worktrees/.verify/<binding>-NNN`, agent names)
   still needs a name-length check; say which.
6. The text that points users at `ask`:
   - `cmd/relevo/doctor_checks.go:~205`;
   - `internal/relevo/writer_role.go` (if any remains);
   - `internal/serve/bindings.go:~160` (if any remains);
   - the reviewer agent bodies' "asked by the planner through `relevo ask`"
     (`internal/harness/agents/reviewer.{claude,opencode,agy}.md` and
     `reviewer.codex.toml`, ~line 45). Say instead: "asked by the planner as a bound
     reviewer, or by `relevo send --verify`".

     Then run `sh scripts/agents-shipped.sh --write` and `--check`.
7. Tests that exist only to test `ask`:
   - `internal/relevo/ask_test.go`;
   - the `Ask(` calls in `pick_test.go`, `roles_runtime_test.go`, `consult_test.go` and
     `remote_test.go`;
   - cmd/relevo `contract_test.go:~699` and `main_test.go` ~219, ~1028 and ~1074
     (`TestAskFlagsHaveActorNotRole`).
   - **Port, don't drop,** any of them whose assertion is about behaviour that survives:
     the consult reconcile (the final message becomes the findings round file), the
     stderr sharing the stream, the consult paths scope, and pick-note parsing.
     - Seed consult records directly, or go through `StartVerify`.
     - Examples: `TestAskRoundFinalMessageBecomesFindings`,
       `TestConsultStderrSharesTheStream`, `TestAskScopesBothConsultPaths`.
   - **In the report, list every deleted test with the item number above it falls
     under, and every ported test with its new name.**

## 2. What stays (do not delete)

- `store.Consult`, `b.Consults`, `AskPath`, `FindingsPath`, `ConsultStreamPath`,
  `KindAsk`, `KindFindings` and `DirToConsult`, because old records and verify use them;
- `relevo show --findings <id>`, `delivery.FindingsCommand`,
  `consultDeps().ResolveReviewer`, and the `reviewer` actor;
- the status `consults` count, the usage consult split, and the UI's KindFindings
  handling;
- `ingest`'s `isOtherActorPick`;
- `internal/consult/verify.go` entirely.

## 3. A clear message for the old verb

`relevo ask …` prints, on stderr, "relevo ask is gone: bind a reader actor (relevo bind
--actor reviewer) and send it a plan", and exits 2. This follows the `config roles-init`
stub at `cmd/relevo/config.go:~62`.
- Test it by parsing and dispatching only. Spawn nothing.

## 4. Docs

- **README.md:** remove `ask` from the verb list and usage. Rewrite the "Planner actors"
  workflow (~1820-1830), which is built on `ask` + `show --findings`, as:
  - `relevo bind --actor lite-planner --name plan-x`;
  - `relevo send --name plan-x --file task.md`;
  - `relevo show plan-x --summary` (or the cockpit's artifacts tab);
  - hand the plan to a builder with `relevo send --name <builder> --file <the summary path>`.

  Also update every other `relevo ask` mention the grep finds.
- `docs/design.md:~87`: the same.
- `claude-plugin/`: grep for `ask`, and update any mention.

## 5. Tests

1. `TestAskIsGone`: the message and exit code.
2. `TestVerifyStillRunsTheReviewer`: `send --verify` on a writer round still starts a
   verify consult and reconciles its findings. It uses the existing verify tests; name
   the ones that pin this, or add one if none does end to end with fakes.
3. The ported tests from §1 item 7.
- **Required mutation.** Delete `StartVerify`'s call site in the close path; test 2 must
  fail. Then restore it.
- No test spawns a harness or reaches the network. cmd tests only parse and dispatch.

## 6. Working efficiently

- Before deleting anything, find every reference in one pass:
  `grep -rn 'Ask(\|askRound\|RoundQuestion\|RoundRole\|roundAskPrompt\|ConsultRolesTooLong\|consult\.Spawn\|ErrConsultCap\|ErrNotAConsultRole\|ErrTreelessUnsupported\|headlessPrompt\|relevo ask' --include='*.go' --include='*.md' --include='*.toml' --include='*.json' .`
- Then delete per §1 in one pass and fix compile errors.
- Focused loop: `go build ./... && go test -count=1 ./internal/consult/ ./internal/relevo/ ./cmd/relevo/ ./internal/harness/ ./internal/serve/`
- Before committing, run `grep -n '§\|#[0-9]' <every file you touched>`: it must print
  nothing in comments.
- Full, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check && make e2e`
- The coverage baselines may drop because code is removed. If `check-coverage` fails
  **only** because a package lost tested code along with the deleted feature, regenerate
  with `sh scripts/check-coverage.sh --write` and **say so** (CLAUDE.md allows this for
  a round that moves or removes code). Never lower a baseline for any other reason.

## 7. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-r6-remove-ask.md`. Commit as
**one new commit**: `feat(a5): remove relevo ask; readers are bound actors`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- the deleted-tests table with item numbers;
- the ported tests;
- the kept-functions list with reasons;
- whether the coverage baseline was regenerated;
- `make check` and `make e2e`'s last lines;
- anything that did not match.
