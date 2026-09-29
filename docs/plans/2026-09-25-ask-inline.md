# A consult's question goes in its prompt, with a file fallback over 64 KiB

Date: 2026-09-25. Base: origin/main (cfad7cfc or later).
Follows the file-writes spike (docs/specs/2026-09-24-file-writes-spike.md) and #446, which
did the same for findings. One round, one PR.

**Stop rather than improvise.** If a step is impossible as written, if the code contradicts
a fact stated here, or if an existing test outside §7.3 fails, halt and report it. Do not
bend a test.

## 1. System overview

A consult is a one-shot agent: `relevo ask`, `relevo ask --round`, or the verify reviewer at
round close. Today relevo writes the question to `$STATE/<name>/NNN-<id>-ask.md` and starts
the consult with a prompt that only says `Read: <that path> …`.

After this round the question text goes **inside the prompt**. The whole prompt is passed as
one command-line argument (`harness.Launch.PrintArgs`, internal/harness/harness.go ~388, and
`Resume` for round consults), and Linux refuses to start a process when one argument exceeds
`MAX_ARG_STRLEN` = 128 KiB (32 pages of 4 KiB). So:

- **Inline when it fits.** When the complete inline prompt is at most `inlineAskMax` =
  64 KiB, it is used as is. The question is recorded straight into `round_file` under its
  usual name, `NNN-<id>-ask.md`, with `Tx.PutRoundFile`, as findings are since #446. Nothing
  is written to disk.
- **Fallback otherwise.** Otherwise the prompt is today's `Read: <AskPath>` form, and the
  question is written to disk exactly as today, and sealed later as today.

`Consult.AskPath` and the ask log entry's `Path` stay the canonical path in both cases.
`Store.ReadFile` resolves it from disk or from the row.

64 KiB is a margin, not a kernel number. The argument also carries the prompt's own
instructions, and the process start may pass through systemd-run, the `sh` supervisor and
the harness's own relaunch, each with the same per-argument limit. The fallback is what
guarantees a large question can never stop a consult from starting.

## 2. File structure

```
internal/relevo/ask.go        inlineAskMax, askInlineBlock, inlinePrompt; both prompt consts take a question reference; Ask/askRound/reserveConsult use them
internal/relevo/verify.go     startVerifyConsult records the question inline or falls back
internal/relevo/ask_test.go   N1-N4; fenced ports
internal/relevo/reconcile_test.go / headless_test.go   fenced ports only (verify tests that read the ask file from disk)
internal/store/store.go       AskPath doc comment
README.md                     the consult passage (~1905-1912)
docs/plans/2026-09-25-ask-inline.md   this plan, verbatim
```

## 3. Data structures

There are no struct, JSON, golden or `BindingFormat` changes.

**`const inlineAskMax = 64 << 10`** (ask.go). Its doc comment says:

- it is the largest whole prompt, in bytes, that relevo passes inline;
- the prompt is one argv element, and Linux's `MAX_ARG_STRLEN` is 128 KiB per argument;
- half of that leaves room for the wrapper layers;
- a larger question falls back to a staged file.

## 4. Interfaces and contracts

### 4.1 Prompt templates (ask.go ~31-46)

- `consultHeadlessPrompt` becomes a format whose single `%s` is the **question reference**.
  Keep the text after it unchanged:
  `"%s\n\nAnswer as your final message: your findings, complete, in markdown. Do not modify any file in this repository. Do not write a findings file; relevo records your final message."`
- `roundAskPrompt`: replace its `Read: %s` with `%s`. Every other character stays the same.
- Update both doc comments.

### 4.2 `askInlineBlock(question []byte) string` (pure)

Returns exactly:

```
The question:

-----BEGIN QUESTION-----
<question, with one trailing newline trimmed if present>
-----END QUESTION-----
```

- The line is `"The question:\n\n-----BEGIN QUESTION-----\n" + q + "\n-----END QUESTION-----"`.
- The delimiters make the question's own markdown unambiguous.

### 4.3 `inlinePrompt(render func(ref string) string, question []byte) (string, bool)` (pure)

- `p := render(askInlineBlock(question))`.
- It returns `(p, true)` when `len(p) <= inlineAskMax`, and `("", false)` otherwise.
- The decision depends only on the question and the template, never on a path. That lets
  `Ask` decide before `reserveConsult` knows `AskPath`.

The file-form reference is `"Read: " + askPath`, which is today's text. Build it where the
path is known.

### 4.4 `reserveConsult` (ask.go ~251-300) takes `inline bool`

- Signature: add `inline bool` after `body []byte`.
- Replace `os.WriteFile(consult.AskPath, body, 0o644)` (~286) with:
  - when `inline`: `tx.PutRoundFile(b.Name, b.Round, consult.AskPath, body)`. On error,
    return `fmt.Errorf("record question at %s: %w", consult.AskPath, err)`.
  - otherwise: the existing `os.WriteFile`, unchanged.

### 4.5 `Ask` (ask.go ~112-230)

- Before `reserveConsult`:
  `render := func(ref string) string { return fmt.Sprintf(consultHeadlessPrompt, ref) }`
  and `prompt, inline := inlinePrompt(render, body)`.
- Pass `inline` to `reserveConsult`.
- After it, when `!inline`: `prompt = render("Read: " + consult.AskPath)`.
- At ~198, pass `prompt` to `headlessLaunch` in place of
  `fmt.Sprintf(consultHeadlessPrompt, consult.AskPath)`.

### 4.6 `askRound` (ask.go ~395-465)

- At ~416-417: `render := func(ref string) string { return fmt.Sprintf(roundAskPrompt, opts.Round, opts.Name, opts.Round, ref) }`.
- Then `prompt, inline := inlinePrompt(render, body)`. If `!inline`, set
  `prompt = render("Read: " + askPath)`. `askPath` is already computed there.
- Pass `inline` to `reserveConsult`.
- `reserveConsult` computes `AskPath` from `b.Round`, as askRound's own `askPath` does, so
  they agree. If they can differ, halt.

### 4.7 `startVerifyConsult` (verify.go ~270-315)

- The question is `question := verifyQuestion(…)`. This is today's `prompt` variable
  renamed, and it is the text written to the ask file today.
- `render := func(ref string) string { return fmt.Sprintf(consultHeadlessPrompt, ref) }`, then
  `prompt, inline := inlinePrompt(render, []byte(question))`.
- If inline: `tx.PutRoundFile(b.Name, round, askPath, []byte(question))`. On error,
  `return fail(fmt.Sprintf("record question at %s: %v", askPath, err))`.
- Else: today's `os.WriteFile(askPath, []byte(question), 0o644)` with its existing `fail`,
  and `prompt = render("Read: " + askPath)`.
- At ~310, `headlessLaunch(…, prompt, …)` replaces
  `fmt.Sprintf(consultHeadlessPrompt, askPath)`.

### 4.8 Docs

- store.go `AskPath` doc: "names a consult's question. relevo passes the question inside
  the consult's prompt and records it straight into round_file under this name; only a
  question too large to inline (see relevo's inlineAskMax) is written here as a file for
  the consult to read. Read it with ReadFile."
- README.md ~1905-1912 ("relevo stages the question and starts the agent. …"). Say instead
  that relevo passes the question in the consult's prompt and keeps a copy with the round.
  A question over 64 KiB is staged as `NNN-<id>-ask.md` for the consult to read instead.
  Leave the rest of the passage unchanged.
- Out of scope: `internal/harness/agents/reviewer.*.md` still say "writes findings to the
  file path the prompt names". That has been stale since #446, and it is shipped and
  hash-pinned. List it under `not_done`.

## 5. Pseudocode

```
inlinePrompt(render, q): p = render(block(q)); return p, len(p) <= 64 KiB
Ask:        prompt, inline = inlinePrompt(headless, body); reserve(inline) -> inline ? PutRoundFile(ask) : WriteFile(ask)
            if !inline: prompt = headless("Read: " + AskPath);  launch(prompt)
askRound:   same with the round template
verify:     question = verifyQuestion(...); same decision; inline ? PutRoundFile : WriteFile + "Read:" prompt
```

## 6. Error handling

- A `PutRoundFile` failure fails the reserve, exactly as a failed `WriteFile` does today:
  - Ask and askRound return the error;
  - verify uses `fail(…)`, so the verify is skipped and the round stays closed.
- No new error types.

## 7. Ordered implementation steps

### 7.0 Working efficiently

- Read these in one parallel batch, and do not re-search what this plan locates:
  - ask.go 25-50, 100-300 and 395-470
  - verify.go 30-60 and 265-340
  - store.go 280-300
  - README.md 1895-1915
  - the tests in §7.3
- Iterate with the focused command
  `go test ./internal/relevo/ -run 'Ask|Consult|Verify|Inline' -count=1`, fixing every
  error before the next run.
- Full check once at the end:
  - `make check`. If a hook blocks it, run its steps directly;
  - paste `gofmt -l $(git ls-files '*.go')`'s empty output;
  - then `make e2e`.
- A cmd/relevo test must not spawn a harness or reach the network. This round adds no CLI
  test.

### 7.1 Steps

1. `inlineAskMax`, `askInlineBlock`, `inlinePrompt` and the two template changes (§3, §4.1-§4.3), with N1.
2. `reserveConsult` and `Ask` (§4.4, §4.5), with N2 and N3.
3. `askRound` (§4.6), with N4.
4. `startVerifyConsult` (§4.7), with N5.
5. Docs (§4.8).
6. Full check, then the §7.4 mutations one at a time, restoring after each.
7. Save this plan verbatim at `docs/plans/2026-09-25-ask-inline.md`.
8. Commit and PR.
   - One commit: `feat(consult): the question goes in the consult's prompt, recorded in
     round_file; a question over 64 KiB falls back to a staged file`.
   - Push with `git push -u origin <branch>`, and open a PR against main with the §7.4
     results in the body.

### 7.2 New tests

- **N1 `TestInlinePrompt`:**
  - a small question gives `ok`; the prompt contains the question and the delimiters, and
    not `Read:`;
  - size a question so the rendered prompt is **exactly** `inlineAskMax` bytes: `ok`;
  - one byte more: not ok;
  - `askInlineBlock` trims exactly one trailing newline.
- **N2 `TestAskInlinesASmallQuestion`**, headless ask with the fake runner:
  - the started argv contains the question text;
  - no file exists on disk at `consult.AskPath`;
  - `rt.Store.ReadFile(consult.AskPath)` returns the question;
  - the ask log entry's `Path == consult.AskPath`.
- **N3 `TestAskFallsBackToAFileOverTheLimit`:** a question of `inlineAskMax + 1` bytes.
  - The argv contains `Read: <consult.AskPath>` and not the question.
  - The file exists on disk with the question.
- **N4 `TestAskRoundInlinesTheQuestion`:**
  - through askRound's existing fixtures (see `TestAskRoundResumesTheSession`, ask_test.go
    ~909), the resumed argv contains the question text, and no file is on disk;
  - plus a fallback case over the limit.
- **N5 `TestVerifyInlinesItsQuestion`:**
  - start from the setup of `TestVerifyRoundStartsOnHeadlessClose` (headless_test.go);
  - the verify consult's argv (fr.specs[1]) contains `Verify round 1 of binding`;
  - there is no ask file on disk;
  - `rt.Store.ReadFile(consult.AskPath)` returns the question.

### 7.3 Fenced tests: the only existing tests you may change

Report each change and cite the reason.

- **`TestAskSpawnsRecordsAndStagesTheQuestion`** (ask_test.go ~124-175): reads of the
  staged question from disk (`os.ReadFile(AskPath)`) become `rt.Store.ReadFile(AskPath)`.
  If it asserts the file is on disk, that becomes "not on disk, readable via ReadFile".
- **`TestAskHeadlessStartsAProcessNotAPane`** (~704-740) and
  **`TestAskRoundResumesTheSession`** (~909-950): an expectation that the prompt/argv
  contains `Read: <AskPath>` becomes "contains the question text".
- **`TestAskRoundRefusesOpenRound`** (~1001-1020): only if it asserts no file at
  `AskPath`. Keep that assertion, and add that `ReadFile` also misses.
- **`TestVerifyRoundStartsAReviewerInAThrowawayWorktree`** (reconcile_test.go ~2010-2070),
  **`TestVerifyGateLogIsPassed`** (~2108-2150) and
  **`TestVerifyRoundHandsTheReviewerAGitDiff`** (headless_test.go ~1004-1060): reads of the
  ask file with `os.ReadFile` become `rt.Store.ReadFile`. The asserted contents stand.

Everything else must pass unchanged. That includes store and types tests of `AskPath`,
`TestSealRoundMovesOneRound`, the consult fixtures in consult_test.go, and the ui golden
test. If one fails, halt.

### 7.4 Mutation checks (run each, report pass/fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | inlinePrompt: `<` instead of `<=` | N1 (exact-size case) |
| M2 | inlinePrompt: always true | N3 |
| M3 | reserveConsult: always `os.WriteFile` | N2 |
| M4 | Ask: keep the `Read:` prompt even when inline | N2 (argv) |
| M5 | askRound: ignore `inline` (always the file form) | N4 |
| M6 | verify: always `os.WriteFile`, `Read:` prompt | N5 |
| M7 | askInlineBlock: drop the question (delimiters only) | N1, N2 |

If a mutation does not make its named test fail, report it. Do not strengthen tests
beyond this plan.

## 8. Scope check

- `git diff --stat` shows only the §2 files and the fenced tests.
- No golden or `BindingFormat` changes.
- No change to the builder's `plan.md` or the `review-plan.md` handling.
