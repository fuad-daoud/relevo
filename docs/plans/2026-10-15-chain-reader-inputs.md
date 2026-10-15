# Plan: chain-reader-inputs (lane B: issues #1170 + #1171)

Base: `origin/main` at `41bf202d`. One branch, plan name `chain-reader-inputs`.
Builder: plan-executor on server zen, starting from that base, new commits only.

Both issue bodies were read and every premise below was verified in the code
at the base commit. Where this plan states a premise is false or partly false,
it says so inline marked `[PREMISE]`.

---

## 1. Behaviour and cases

### Part A — reviewer's findings file is kept (#1170)

**Behaviour.** A chain reviewer or security member that writes a non-empty
findings file keeps that file. The verdict (resp. finding count) is taken from
the final message's fenced relevo block. A fenced block inside the kept file is
stripped from the file. The stream message is written into the file only when
the file is missing or empty. A pointer-only final message never replaces
findings. No length heuristic is used anywhere: `reader_deliverable.go:13-18`
already forbids narration/DSML heuristics, and the issue's "prefer the longer"
alternative is rejected for that reason.

**Cases.**

1. Non-empty findings file + pointer-plus-block final message (the se3c case):
   file kept byte-identical, verdict parsed from the message's block, chain
   advances instead of planning off a placeholder.
2. Non-empty file that itself carries a fenced block: the block is stripped in
   place (rewritten without it), findings kept; the verdict still comes from
   the message's block via the existing file-first/stream-second outcome
   bodies. If stripping would leave the file empty, the file is left
   byte-identical (a block-only file still carries the verdict).
3. Missing or empty (zero-byte or whitespace-only) file: unchanged behaviour —
   the block-carrying stream message, block stripped, is saved as the artifact
   (this is the rule that keeps the #809 recap-after-block protection; see §3).
4. Plain (non-chain) reader with a non-empty file: unchanged — left alone, no
   fallback (pinned by `TestWriteReaderSummaryLeavesAPlainReadersOwnFileAlone`).
5. Refusals unchanged: symlink/directory/fifo at the output path is refused,
   never followed, and a refusal is not a fallback.
6. Verdict/status plumbing needs no change: `chainOutcomeBodies` orders the
   saved body first then stream texts newest-first, and `ParseOutcomes` takes
   the first body carrying a valid value per key — so with a block-free kept
   file, the message's block already wins. The builder verifies this by test
   rather than by re-plumbing.

### Part B — planner and reviewer read base-branch plan files (#1171)

**Behaviour.** For the reviewer step and the correction-planner step only, the
seed additionally carries every repo file the chain's plan texts name
(including a source plan file such as `docs/plans/….md` when a plan text names
it) plus the plan set, each read from the chain's base ref and copied into the
chain's `inputs/` directory. A base ref present only on the remote is fetched
first. An unreadable file renders as the existing path-free
`not available: <reason>` clause, never as a path the runner cannot open, and
never fails the send.

**Cases.**

1. Plan file exists only on the chain's base branch (absent from the
   planner's scratch tree, which is cut from the planner binding's own HEAD):
   the correction planner's prompt/inputs carry its text and the correction
   round runs instead of halting with "plan text absent".
2. Base ref not present locally (remote-builder chains): fetch first, then
   read; a fetch/read failure degrades to the `not available` clause.
3. `[PREMISE — partly false as a "scratch cut" story]` The issue body says
   "`CreateScratch` cut that from the planner binding's own HEAD". Verified:
   the reader scratch is built by `sendChainRound` from the member's own
   captured baseline (`chain_send.go:98-105`), and locally a reader shares the
   builder's tree (`chainMemberBinding`, `chain_start.go:366-390`). The scratch
   is therefore not the thing to change per the decision: the decision orders
   the inputs approach, not re-cutting scratch from the base, and this plan
   implements inputs only. A re-cut is explicitly out of scope.
4. Security/fix seeds are out of scope: the decision names the correction
   planner's and the reviewer's inputs only.

---

## 2. Seams (file, type/function, line range at base)

**Part A.**

- `internal/relevo/summary.go:55-107` — `writeReaderSummary`; the overwrite
  rule is `internal/relevo/summary.go:79-81` and the present-file rewrite is
  `:96-100`.
- `internal/relevo/summary.go:196-208` — `readerOutputText` (chain reader:
  `LastWithBlock` stripped, else `FinalText`).
- `internal/relevo/summary.go:242-252` — `chainReaderPart` (reviewer/security).
- `internal/relevo/summary.go:277-294` — `replaceReaderOutput` (in-place
  truncate-write behind `O_NOFOLLOW`; reuse for the strip step).
- `internal/reporttail/reporttail.go:289-307` — `StripTail` (what "strip the
  block" means; prose-after-fence is left alone).
- `internal/reporttail/blockvalue.go:14` — `BlockValue` (verdict read).
- `internal/relevo/chain_seed.go:95-104` — `chainOutcomeBodies` (body first,
  stream newest-first); `:118-132` `chainReaderStatus`; `:110-112`
  `chainParseOutcomes`.
- `internal/workflow/outcomes.go:18-45` — `ParseOutcomes` (first valid value
  per key wins).
- `internal/relevo/chain_close.go:223-238` — `chainReaderCloseOutcome`;
  `:183-199` `chainCloseArtifacts`; `:39-106` `chainEventFromCloseWF`.
- `internal/relevo/reader_deliverable.go:25-54` — `readerDeliverablePresent`
  (chain file-or-block rule `:33-38`, unchanged).
- `internal/relevo/reconcile.go:759-857` — `queueReport` (close path that
  calls `writeReaderSummary` at `:774`, parses at `:807-826`, applies the
  chain outcome at `:853-857`; unchanged).
- `internal/relevo/send.go:109-124` — `chainReaderPrompt` (file + short-close
  contract the se3c agent followed).
- `internal/harness/agents/reviewer.claude.md:1-34` (and the
  `.agy.md`/`.codex.toml`/`.opencode.md` twins, plus the `security-reviewer.*`
  set) — "write findings to the output file" instruction.
- Tests: `internal/relevo/summary_test.go:20-77`
  (overlaps deletion list item 1), `:533-555` (missing-file recap protection,
  keep).

**Part B.**

- `internal/relevo/chain.go:179-241` — `chainSeedView` (reviewer/correction
  inputs); `:115-125` `chainPlanPaths`; `:165-178` header comment (update for
  the new inputs).
- `internal/relevo/chain_seed.go:74-89` — `chainSeedPlanView`;
  `:306-343` `chainSeedInput` / `chainSeedMissing` (copy + miss-clause
  pattern to reuse).
- `internal/relevo/chain_send.go:43-147` — `sendChainRound` (reader scratch
  from own baseline `:98-105`).
- `internal/relevo/chain_start.go:366-390` — `chainMemberBinding` (readers
  share the builder tree); `:395-441` `chainRow` (`Base` `:427`,
  `PlanStartCommit` `:437`).
- `internal/relevo/chain_render.go:21-66` — `chainRenderSeed`;
  `:141-178` `chainRenderShippedSeed` (reviewer/correction view build).
- `internal/workflow/seeds.go:29-56` — `SeedView` (new field for the
  plan-named file copies); `internal/workflow/seeds/review.md`,
  `internal/workflow/seeds/correct.md` (name the new inputs).
- `internal/workflow/shipped/default.yaml:23-29` — `review`/`correct` steps
  (scope confirmation: only these two seeds change).
- `internal/store/paths.go:424-442` — `ChainInputDir`, `ChainInputPath`
  (round-file-only; plan-named repo files need a sibling copy path under the
  same dir); `:411-422` `ChainPlanPath`, `ChainTaskPath`; `:449-455`
  `DiskRegularFile`.
- `internal/git/sync.go:32` — `Client.Fetch`; `internal/git/refs.go:137` —
  `RefSHA`; `internal/git/client.go:113-124` — `command`/`run` (git read of a
  file at the base ref; no helper exists yet — the builder adds the minimal
  one).
- Test seams: `internal/relevo/chain_start_test.go:52-64` (`chainRuntime`,
  `startedChain`), `internal/relevo/chain_test.go:51-129` (`chainBinding`,
  `chainBuilderClose`), `internal/relevo/bind_branch_fetch_test.go`
  (fetch-test precedent); `internal/e2e/chain_test.go:65` (`TestChainE2E`,
  full-chain shape if the builder needs it — engine-level test preferred).

**Limits.** `summary.go` 317 lines, `chain_seed.go` 343, `chain.go` 309,
`chain_render.go` 225, `chain_send.go` 188, `workflow/seeds.go` 79 — all far
from the 600-line ceiling. New/changed functions stay ≤ 70 lines; comments say
why only; no issue numbers or history in code or test names.

---

## 3. Why the old rule existed (kept protection)

Commit `bc4de8cc` (#809, "block artifacts") made chain reviewer/security
members relevo-owned: the saved artifact became the block-carrying message,
stripped, so a recap after the block ("Done.") could not become the whole
file. Commit `340f8c0a` (#1130, "reader writes its own output file") kept that
as the chain exception while plain readers went file-first. The se3c failure
shows the exception overreaches once agents obey the file instruction. The
protection is still needed for exactly one case — file missing or empty with a
recap after the block — where `readerOutputText` + strip already implements
it. The plan therefore narrows the exception to that case instead of removing
it.

---

## 4. Ordered steps (one line each: deliverable + how to know it worked)

1. Premise check — deliverable: a note in the report confirming each §1
   premise against code at the base (or a halt if one is false); worked: every
   seam in §2 resolves to the cited lines.
2. Part A fix in `internal/relevo/summary.go` — deliverable: non-empty output
   file kept for chain readers, in-file fenced block stripped in place (empty
   after strip ⇒ left alone), stream text written only for missing/empty
   files, `fellBack` true only for the missing/empty fallback; worked:
   `make check-quick PKGS=./internal/relevo/...` green and the new Part A
   test fails when the old `summary.go:79` condition is restored.
3. Part A tests in `internal/relevo/summary_test.go` — deliverable: a test
   where a chain reviewer writes findings to the file and ends with a
   pointer-plus-block message (file kept, verdict parsed from the message) plus
   an in-file-block strip case, and the #809 test rewritten to a missing/empty
   file; worked: each new test fails on today's code (mutation: restore the
   overwrite branch / skip the strip), passes with the fix.
4. Part B inputs helper + seed wiring (`internal/relevo/chain*.go`,
   `internal/workflow/seeds.go`, `seeds/review.md`, `seeds/correct.md`) —
   deliverable: reviewer and correction seeds name `inputs/` copies of every
   repo file the plan texts name plus the plan set, read from the chain's base
   ref with remote fetch fallback and `not available` degradation; worked:
   `make check-quick PKGS=./internal/relevo/... ./internal/workflow/...`
   green and an engine test with the named file present only on the base
   ref shows the correction seed carrying its text (mutation: disable the
   helper ⇒ seed lacks the text / correction halts).
5. Part B test — deliverable: engine-level (preferred; e2e-style only if the
   engine level cannot stage a base-only file) test where the plan file exists
   only on the base branch and the correction planner gets its text; worked:
   fails on today's code, passes with step 4 (no harness spawn, no network —
   pure `internal/relevo`; a `cmd/relevo` test must not run a subcommand that
   spawns a harness or reaches the network).
6. Finish in order — deliverable: plan saved to
   `docs/plans/2026-10-15-chain-reader-inputs.md`, `gofmt -l` clean on changed
   files, full gate
   `PATH=$(echo "$PATH" | tr ':' '\n' | grep -v '\.shims' | paste -sd:) make check`
   green, everything committed on the branch, report written; worked: `git
   status` clean and the report lists the commit(s).

Iterate with `make check-quick PKGS=./internal/<pkg>/...` plus importers of
touched packages while working; the full gate runs once at the end.

---

## 5. Closed numbered list of deleted behaviour

1. Chain reviewer/security overwrite: a non-empty output file is no longer
   replaced by the stripped block-carrying stream message
   (`summary.go:79-81`, `:96-100` present-file branch) — replaced by keep +
   in-place block strip.
2. The #809 test expectation for a non-empty recap file
   (`TestWriteReaderSummaryPrefersTheChainBlockMessage` as written) — rewritten
   to pin the same protection for a missing/empty file; nothing else in that
   test's protection is dropped.
3. Nothing else is deleted: plain-reader file-first, refusals, fallback note
   and header, deliverable/close/verdict plumbing, scratch creation, and all
   other seeds stay as they are.

---

## 6. What the report must include

- Premise-verification outcome per §1 (including the §1-case-3 partial
  correction), or a halt naming the step and the false premise.
- For each fix: the named mutation run and its observed failure (proving the
  test pins the behaviour), then the pass with the fix.
- `make check-quick` invocations with packages, `gofmt -l` result, and the
  full-gate command with the shims-stripped `PATH` and its result.
- Coverage-baseline handling: regenerated with
  `sh scripts/check-coverage.sh --write` only if code moved between packages,
  said so explicitly; never lowered to get green.
- Branch name, commit(s), clean `git status`, and anything deliberately left
   undone (security/fix seeds, scratch re-cut).
