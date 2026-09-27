# Plan: the claude-only `config init` note names the builder harnesses

One-session change. Four files, two of them tests. No new files, no new packages, no import changes.

## 1. System Overview

`relevo config init` seeds the builder actor from the harness binaries on PATH. claude never builds (it has no `setup.Defaults` entry; it is seeded as a planner), so when claude is the only harness found, the builder actor is written with no candidates and `cmdInit` prints one note telling the user how to add one. The note names only the command, not which harnesses can provide a builder. This change appends the harness kinds `setup.Defaults` can seed a builder for — agy, codex, opencode — to that note in a fixed order, and pins the new output in the existing claude-only test. Nothing else changes: the trigger condition, the position of the note and the command fragment stay byte-identical.

## 2. File Structure

```
internal/setup/setup.go       + BuilderKinds(): the kinds Defaults can seed a builder for, in a fixed order
internal/setup/setup_test.go  + TestBuilderKindsListsBuilderHarnessesInOrder
cmd/relevo/init.go            cmdInit: the note line now names the harnesses
cmd/relevo/init_test.go       TestInitClaudeOnlyNotesMissingBuilder: assert the whole note line
```

## 3. Data Structures & Type Definitions

No new data structures.

- `setup.BuilderKinds` returns `[]string`, each element a harness kind (a key of `setup.Defaults`): `"agy"`, `"codex"`, `"opencode"`. Three elements today, ascending by kind, never nil, a fresh slice owned by the caller (mutating it must not affect a later call).

## 4. Interface Definitions & Component Contracts

### `setup.BuilderKinds` — package `internal/setup`

- Signature: `func BuilderKinds() []string`
- Single responsibility: name the harness kinds that can provide a builder, in a fixed order.
- Preconditions: none. No PATH lookup, no I/O, no env.
- Postconditions: exactly the kinds that are in `Defaults` and in `harness.All()`, in `harness.All()` order (ascending by kind, an order already pinned by `internal/harness/harness_test.go` and already used for `Files.Kinds`); the caller may mutate the returned slice; it is never nil.
- Errors: none (total function).
- Dependencies: `harness.All()` and `Defaults`, both already in package `setup`.
- Doc comment (a *why*; must not cite issue or section numbers): `// BuilderKinds names the harness kinds Defaults can seed a builder for, in harness.All() order: a fixed order, so the note init prints reads the same on every run.`

### The printed note — observable contract of `cmdInit`

When, and only when, the written builder actor has zero candidates (the condition at `cmd/relevo/init.go:100` is unchanged), `cmdInit` prints exactly one stdout line:

```
note: no builder candidate (claude only plans); add one from a builder harness (agy, codex, opencode): relevo config set actors.builder.candidates '["<name>"]'
```

The harness list is one `%s` slot filled with `setup.BuilderKinds()` joined by `, ` and printed printf-style with a trailing `\n` (one line, as today). Everything else on the line, including the command fragment, is byte-identical to today's note. The position is unchanged: immediately after the `wrote actors (...)` line and before agent installation.

## 5. High-Level Pseudocode

```
BuilderKinds():
    out = []
    for h in harness.All():        # already sorted by kind
        if Defaults has h.Kind:    # membership only
            out.append(h.Kind)
    return out

cmdInit, note block (condition unchanged):
    if builder actor has no candidates:
        harnesses = join(BuilderKinds(), ", ")
        print one line:
            "note: no builder candidate (claude only plans); add one from a builder harness (" +
            harnesses +
            "): relevo config set actors.builder.candidates '[\"<name>\"]'"
```

## 6. Error Handling Strategy

No new errors. `BuilderKinds` cannot fail (no I/O, no PATH). `cmdInit`'s error paths, exit codes and output order are untouched; the note stays best-effort stdout after a successful config write. Halt and report instead of improvising if a named location does not match the code, if `harness.All()` or `Defaults` no longer has the three kinds, or if the note does not sit in the block quoted in step 2.

## 7. Working Efficiently

- Read once, from these locations; do not re-search: `internal/setup/setup.go` 16-24, `internal/setup/setup_test.go` 280-288 (end of file), `cmd/relevo/init.go` 96-103, `cmd/relevo/init_test.go` 147-170.
- The four file changes are independent: batch the four edits as parallel tool calls in one step, one edit call per file.
- Focused command, one invocation for both packages: `go test ./internal/setup/ ./cmd/relevo/ -run 'TestBuilderKinds|TestInit' -count=1`. Fix every reported error before the next run.
- Full check once at the end: `make check`. Then `git diff --stat` for the report.
- No subagents; cmd/relevo tests stay stub-only — CI has no harness binary and no network.
- This runner is headless: never pause for a decision. If a step is impossible as written, halt and report.
- Commit on the round branch when the full check is green, and save this plan as `docs/plans/2026-09-26-claude-only-note-builder-harnesses.md` in the same commit.

## 8. Ordered Implementation Steps

**Step 1 — add `setup.BuilderKinds` and pin it.**
- `internal/setup/setup.go`: insert `BuilderKinds` (contract in §4) between line 24 (the `}` closing `Defaults`) and line 26 (the `PlannerDefault` doc comment). No import change, no other edit in this file.
- `internal/setup/setup_test.go`: append after line 288 a test named `TestBuilderKindsListsBuilderHarnessesInOrder` with a one-line comment, asserting (a) the result deep-equals `[]string{"agy", "codex", "opencode"}` and (b) mutating a returned slice does not change a later call (fresh slice).
- Deletes: nothing.
- Verify: `go test ./internal/setup/ -run TestBuilderKinds -count=1` is ok.

**Step 2 — change the note and its test.**
- Deletions (closed list): item 1 below.
- `cmd/relevo/init.go:101`: replace the deleted line with the note of §4, printed printf-style (one `%s` for `strings.Join(setup.BuilderKinds(), ", ")`, trailing `\n`). `strings` and `setup` are already imported; lines 100 and 102 do not change.
- Deletions (closed list): item 2 below.
- `cmd/relevo/init_test.go:167-169`: replace with an assertion that `out` contains the full note line of §4 (`want` as a backtick string; the line has no backtick). Lines 163-166 (the `builder: none` check) do not change.
- Verify: `go test ./cmd/relevo/ -run TestInit -count=1` is ok.

**Step 3 — mutation check, full check, report.**
- Mutation: temporarily make `BuilderKinds` return the `Defaults` keys in Go map iteration order (unsorted); run `go test ./internal/setup/ ./cmd/relevo/ -run 'TestBuilderKinds|TestInitClaudeOnly' -count=20` and confirm it fails. Restore the helper, rerun the focused command green. If the mutation passes, the tests do not pin the order — halt and report.
- `make check`. If it fails for anything outside the four files, halt and report.
- Copy this plan to `docs/plans/2026-09-26-claude-only-note-builder-harnesses.md`, then commit everything on the round branch with a message naming the change. `git diff --stat HEAD~1` for the report.
- Report must include: the diff stat, the mutation result, the `make check` result.

### Closed list of what is deleted (everything not on this list survives)

1. `cmd/relevo/init.go:101` — `fmt.Println(`note: no builder candidate (claude only plans); add one: relevo config set actors.builder.candidates '["<name>"]'`)`.
2. `cmd/relevo/init_test.go:167-169` — the loose `strings.Contains(out, "note: no builder candidate")` check and its `t.Errorf`.

No test is deleted. The one modified test keeps every surviving assertion it had. `README.md` 157-158 and 376-377 describe the claude-only case but do not quote the note: out of scope.
