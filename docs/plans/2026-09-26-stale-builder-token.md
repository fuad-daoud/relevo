# Stale builder token: a binding whose candidate was edited or deleted (2026-09-26)

## 1. System overview

A binding stores its builder as a canonical token, `harness/provider/model`, in `store.Binding.BuilderCandidate`. The
cockpit (`:candidates`, #510) can now edit a candidate's harness, provider or model, or delete it. The daemon's
`ConfigWatcher` then reloads `rt.Candidates`. A binding bound before the edit still holds the **old** token, which the
configured set no longer holds. This is a *stale* token. Four things go wrong today:

1. **A running round is never switched off a gated provider.** `gatedBuilder` (`internal/relevo/switch.go:21-28`)
   searches `Gates(rt)`, and `Gates` projects the ledger onto *configured* tokens only
   (`internal/relevo/ledger.go:258-285`). So a stale token never matches. The same function also guards `Admit`
   (`queue.go:41`).
2. **Automatic limit detection is off for that running round.** `limitPatterns` (`limit.go:289-316`) returns nil
   when `Lookup` misses, so `gateOnLimit` never matches a limit line.
3. **The next round cannot start.** `sendPreflight` (`send.go` ~274-281), `startRound` (`headless.go:201-208`) and
   `resumeRound` (`headless.go:331-338`) fail with "candidate … not found". This hits `relevo send`, a queued `Admit`,
   a repair round (`repair.go:123`) and a daemon-restart relaunch (`headless.go:825-880`).
4. **`gatedNote` prints nothing for a stale token** (`ledger.go:444-462`).

**Decision (the user chose this):** the running process keeps running on the triple it was started with, so its gate
check and limit patterns use **that triple**. The **next** round picks again by the binding's actor order, as a
switch does. The actor lists already follow a rename, so the pick usually lands on the edited candidate. The pick is
logged. Remote bindings are out of scope: their server decides.

## 2. File structure (all existing files; no new files except tests)

```
internal/relevo/
  ledger.go      + ledgerGates(rt, tokens); Gates and gatedNote use it
  switch.go      gatedBuilder uses ledgerGates on the binding's own token
  stale.go       NEW: staleBuilder, repickStale (small, pure)
  limit.go       limitPatterns falls back to the token's harness patterns on a Lookup miss
  send.go        preflight re-picks a stale builder; the in-lock re-apply; the Pick line
  queue.go       Admit switches a stale queued builder
  repair.go      startRepairRound re-picks a stale builder before startRound
  headless.go    the restart relaunch switches a stale builder instead of resuming it
  stale_test.go  NEW: every test in §7 step 6 that is not an edit of an existing file's test
docs/plans/2026-09-26-stale-builder-token.md   this plan (last step)
```

## 3. Data structures

- **`preflight` (`send.go` ~96-116)** gains one field:
  - `staleToken string`: the binding's old `BuilderCandidate` when the preflight re-picked because the token was
    stale. It is `""` otherwise. When it is non-empty, `pick` is non-nil.
- No store, DB or config schema changes. `store.Binding` is unchanged.
- **Log entries:**
  - A re-pick writes a normal `store.KindPick` entry through `pickEntry`, whose note is `ExplainResolution` and
    unchanged, because the outcome and stats parsers read those words.
  - A switch writes the normal `KindSwitch` entry through `switchBuilder`, with the reason text given below.

## 4. Interfaces and contracts

### 4.1 `ledgerGates` (`internal/relevo/ledger.go`, new, placed just above `Gates`)

```
// ledgerGates projects the live ledger onto tokens, whether or not the configured set holds them:
// a rate limit gates every token of its provider, a spawn failure gates its own token.
func ledgerGates(rt Runtime, tokens []string) []ledger.Gate
```
- **Pre:** none. `rt.Candidates` may be nil.
- **Post:** equals `ledger.Gated(l, tokens, providerOf, rt.Now())`, where `l` is the ledger `Gates` loads today.
  - It returns nil when `rt.Gates == nil` (an empty ledger).
  - On a load error it writes the same stderr line `Gates` writes today and returns nil.
  - It fills no `Name`.
- **`Gates(rt)` becomes:**
  - nil when `rt.Candidates == nil`, as today;
  - otherwise `ledgerGates(rt, rt.Candidates.Refs())`, then `rolesMissingGates`, then the same name fill.
  - Its output must be byte-identical to today's. The existing `TestGates*` tests in `ledger_test.go:211-420` pin it.

### 4.2 `gatedBuilder` (`switch.go:15-28`)

- **Body contract:** loop over `ledgerGates(rt, []string{b.BuilderCandidate})` instead of `Gates(rt)`. Keep the
  RateLimited-only filter and the doc comment's SpawnFailed paragraph.
- **Update the doc comment:** "Pure over the ledger and b's own token: a token the configured set no longer holds
  (the candidate was edited or deleted mid-round) is still checked, because the running process is on that triple."
- An empty `b.BuilderCandidate` returns false. Every caller already guards it through `switchable`; keep that true.

### 4.3 `gatedNote` (`ledger.go:444-462`)

- Iterate `append(ledgerGates(rt, []string{token}), rolesMissingGates(rt)...)`, keeping only `g.Token == token`.
- Everything else stays unchanged. The existing `TestGatedNote*` tests (`ledger_test.go:386-450`) must pass
  unchanged.

### 4.4 `limitPatterns` (`limit.go:289-316`)

- Only when `rt.Candidates.Lookup(ref)` errors: use `harness.Lookup(ref.Harness)`'s `LimitPatterns` alone. There are
  no candidate patterns, because the candidate is gone.
- The `rt.Candidates == nil` and bad-ref cases keep returning nil.

### 4.5 `internal/relevo/stale.go` (new)

```
// staleBuilder reports whether b's builder token names a candidate the configured set no longer holds:
// the candidate was edited or deleted after the binding picked it.
func staleBuilder(rt Runtime, b store.Binding) bool
```
- **true** only when all of these hold: `b.BuilderCandidate != ""`, `!b.Builder.Remote()`, `rt.Candidates != nil`,
  and `rt.Candidates.NameFor(b.BuilderCandidate)` returns ok == false. It is false otherwise. Pure.

```
// repickStale picks b's builder again by its actor's order when staleBuilder(rt, b): resolveRole with an empty
// token over Gates(rt), then applyBuilder. It returns b unchanged and a nil *Resolution when b is not stale.
func repickStale(rt Runtime, b store.Binding, allowYolo bool) (store.Binding, *Resolution, error)
```
- **Errors:** `resolveRole`'s error (every candidate gated, or none serves the role) and `applyBuilder`'s
  `ErrBadBuilder` (tier cap), each wrapped as `fmt.Errorf("builder %s is no longer configured and no other candidate
  can take it: %w", old, err)`, where `old` is the stale token.
- **Post, on success:** `BuilderCandidate`, `Builder.Kind` and `Tier` follow `applyBuilder`, and `RoundExcluded` is
  cleared. Pure: no writes, no log.

## 5. High-level pseudocode

### 5.1 Send (`send.go`)

```
sendPreflight, right after the `if opts.Builder != "" { ... }` block closes (before `if tier == ""`, line 193):
  if pick == nil and staleBuilder(rt, b):
      old = b.BuilderCandidate
      b, res, err = repickStale(rt, b, opts.AllowYolo)
      if err: return preflight{}, err
      pick = res; staleToken = old
  (set pf.staleToken = staleToken in the preflight literal)

Send, in-lock block `if pf.pick != nil {` (line 369):
  the roundOpenIn refusal runs only when pf.staleToken == ""  -- a stale re-pick is not a --builder change
  applyBuilder is re-applied exactly as today

Send, line ~462, where pickLine is set:
  if pf.staleToken != "": pickLine = "note: builder " + pf.staleToken + " is no longer configured; " + PickText(...)
  else unchanged
```
- The pick entry is written exactly where the --builder pick entry is written today (lines ~437 and ~460).
- `SendDryRun` shares the preflight, so it shows the re-picked builder. Report what the dry run prints.

### 5.2 Admit (`queue.go:39-46` and the note at ~65)

```
reason = ""
if gatedBuilder(rt, b): reason = "gated while queued"
else if staleBuilder(rt, b): reason = "candidate " + b.BuilderCandidate + " is no longer configured"
if reason != "": switched = true; b, startErr = switchBuilder(ctx, rt, tx, b, reason, false, false)
else: startRound as today
the KindQueue note uses reason instead of the literal "gated while queued"
```

### 5.3 Repair (`repair.go`, just before `started, err := startRound(...)` at line 123)

```
b, res, err := repickStale(rt, b, false)
if err: return haltBinding(ctx, rt, b, "<name>: repair round <n> could not start: <err>")
if res != nil: tx.AppendLog(b.Name, pickEntry(rt.Now().UTC(), b.Round, bindingRole(b), *res))  -- a log error halts the same way
```
- The `prompt` does not depend on the builder, so it may stay computed where it is.

### 5.4 Restart relaunch (`headless.go`, inside `if lost && switchable {`, after the `b.Owner != ""` re-queue branch returns, before `text := composePrompt(...)`)

```
if staleBuilder(rt, b):
    return switchBuilder(ctx, rt, tx, b,
        "lost to a daemon restart; candidate " + b.BuilderCandidate + " is no longer configured", false, false)
```
- An accepted cost: `switchBuilder` restarts `RoundStartedAt`, unlike the same-candidate relaunch. Say so in a
  one-line comment.
- A server's re-queue path is untouched: `Admit` handles the stale token when it re-admits.

### 5.5 Running round (no code beyond §4.2 and §4.4)

`reconcileHeadless` (`headless.go:667-676`) calls `gatedBuilder`, which now sees the gate on the process's own
provider. `switchBuilder` then resolves over the configured set, as today.

## 6. Error handling

- **A re-pick that finds no candidate:**
  - Send returns the wrapped error and nothing is staged.
  - Admit takes its existing spawn-failure path (NEEDS YOU), because `switchBuilder` halts.
  - Repair and relaunch halt with the reason.
- **No new error types.**
- **Logging:** `switchBuilder` already `slog.Info`s its switches. `repickStale` callers add nothing beyond the pick
  entry.
- **Not changed:** the advisory gate loop in `sendPreflight` (~285-292) keeps `Gates(rt)`. After a re-pick the token
  is configured, and a stale token never reaches it, because the `Lookup` above it would have failed first.

## 7. Ordered implementation steps

### 0. Working efficiently (read before step 1)

**How to work:**
- Every location above is on this worktree's HEAD (`0583bea`). Read `switch.go`, `ledger.go:250-290,440-470`,
  `limit.go:285-320`, `send.go:90-300,320-470`, `queue.go:20-80`, `repair.go:100-140` and `headless.go:810-900` in
  **one** batch of parallel reads. Do not search for them again.
- Make every change to a file in one edit.
- Model the new tests on the existing ones named in step 6. Read them in one batch with the files above.

**Commands:**
- Focused loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ -run 'Stale|Gated|Gates|Admit|Regate|LostToDaemonRestart|SendBuilder|LimitPattern' -count=1`
- Full package: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ -count=1`
- Final checks, once:
  - `go vet ./internal/relevo/`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-name.sh`
- `make check` is refused on this machine by a hook, so do not run it.
- No test in `cmd/relevo` is needed or wanted: CI runners have no harness binary. Every test goes in
  `internal/relevo`.

### Step 1: `ledgerGates` and `Gates` (§4.1)

- **Depends on:** nothing.
- **Verify:** `go test ./internal/relevo/ -run 'Gates|GatedNote'` passes unchanged.

### Step 2: `gatedBuilder` and `gatedNote` (§4.2, §4.3)

- **Depends on:** step 1.

### Step 3: `limitPatterns` fallback (§4.4)

### Step 4: `stale.go` (§4.5)

### Step 5: Wire the four start paths (§5.1-§5.4)

- **Depends on:** step 4.

### Step 6: Tests

Create `stale_test.go`. It may also hold the gate tests. Each test builds a set, then puts the binding on a token the
set does not hold: a same-harness triple with a different provider or model.

- **a. `TestStaleBuilder`:** a table covering a configured token (false), an unconfigured one (true), `""` (false), a
  remote binding (false) and a nil set (false).
- **b. `TestGatedBuilderSeesAStaleTokensProvider`:** the binding is on `h/old/m`, the set holds only `h/new/m`, and
  the ledger rate-limits `old`. The result is gated.
- **c. `TestGatedBuilderIgnoresTheEditedCandidatesNewProvider`:** the same setup, but the ledger gates `new`. The
  result is **not** gated, because the process is on `old`.
- **d. `TestReconcileHeadlessStaleGatedSwitches`:** model it on `TestReconcileHeadlessGatedKillsAndSwitches`
  (`headless_test.go:3119`). A running stale builder whose old provider is gated is killed and switched to a
  configured candidate.
- **e. `TestLimitPatternsStaleTokenUsesHarnessPatterns`:** use a harness whose `LimitPatterns` are non-empty (check
  `internal/harness/harness.go:143`). The result is non-empty and equals that harness's count.
- **f. `TestSendStaleBuilderPicksAgain`:** model it on `TestSendBuilderGatedTokenProceeds` (`send_test.go:1120`).
  - Send succeeds, and the saved binding's `BuilderCandidate` is the configured pick.
  - The round's log holds a `KindPick` entry.
  - `SendResult.Pick` starts with `note: builder <old> is no longer configured; `.
- **g. `TestSendStaleBuilderNoCandidateRefuses`:** every configured candidate is gated. Send errors with
  `no longer configured`, and no plan entry is written.
- **h. `TestAdmitStaleBuilderSwitches`:** model it on `TestAdmitGatedSwitches` (`queue_test.go:190`). The KindQueue
  note contains `no longer configured`.
- **i. `TestRegateStaleBuilderPicksAgain`:** model it on `TestRegateHeadlessStartsRepairProcess`
  (`headless_test.go:2718`). The repair round starts on the configured pick, and a `KindPick` entry is logged.
- **j. `TestReconcileHeadlessLostToDaemonRestartStaleSwitches`:** model it on
  `TestReconcileHeadlessLostToDaemonRestartRelaunches` (`headless_test.go:1455`). The binding is not halted, a
  `KindSwitch` entry contains `no longer configured`, and the new process runs the configured candidate.
- **k. `TestGatedNoteStaleToken`:** a stale token whose provider is gated gives a non-empty note that names the token.

**Required mutations.** Run each, confirm the named test fails, then revert. Report every result, with the build
line, because a mutation that does not compile proves nothing.

- **M1:** `gatedBuilder` loops `Gates(rt)` again → b fails.
- **M2:** delete the preflight stale branch → f fails.
- **M3:** delete the Admit `staleBuilder` branch → h fails.
- **M4:** `limitPatterns` returns nil on a Lookup miss again → e fails.

### Step 7: Checks, the plan, the commit

1. Run the full package and the final checks listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-stale-builder-token.md` in the worktree.
3. Make one commit (this is round 1; there is no commit to amend). Run `git add -A`, then commit with this message:

   ```
   fix(relevo): a binding whose candidate was edited or deleted is still switched off a gated provider, and picks again on its next round
   ```

## 8. Deletions (closed list)

- **D1.** The literal `"gated while queued"` in the Admit KindQueue note becomes the `reason` variable. The words
  stay the same for the gated case.

Nothing else is deleted. No existing test is deleted or has its assertions changed. If one fails, halt and report it:
it means this plan contradicts the code.

## 9. Stop rather than improvise

Halt and report if any of these happens:
- a named location is not where this plan says;
- `Gates`' output changes for any existing test;
- `switchBuilder` does not start the round (§5.2 and §5.4 rely on it doing so);
- a step is impossible as written.

A halt that finds a planning error is worth more than a green suite bent to fit.
