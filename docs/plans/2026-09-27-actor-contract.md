# The planner contract, the injected guide, and the seed guard

Owner decisions, 2026-09-27. One builder round on top of `relevo/mm-r2` (the round
vocabulary: a round's input is its prompt, a reader's artifact its output). Line
anchors are this tree's HEAD, `5c462261`; if one moved, find the named function.

Commit this plan as `docs/plans/2026-09-27-actor-contract.md` in this round's
commit (last step): copy the round's staged prompt — the path your prompt's
`Read:` line names — into that file byte for byte.

Stop and report instead of improvising if a location below does not match, if a
step is impossible as written, or if a test pins a spelling this plan does not
retarget. Never delete a test to get green; retarget it.

## 1. System Overview

Three changes to the actor contract, on top of #586's planner actors and the
MasterMind rename:

1. **The architect definition becomes the planner actor's contract.** The shared
   body of `architect.{claude,opencode,agy}.md` and `architect.codex.toml`
   (today 9,609 bytes, byte-identical across kinds) is rewritten for a reader
   round: seed in, plan out as the final message, minimal plan shape, never
   edit, never build. The whole "Handing off" dispatch section goes — a
   session's job, not an actor's — and `write_to_file` leaves
   `architect.agy.md`'s tool allowlist.
2. **The injected text becomes the MasterMind guide.** `internal/mastermind/handoff.md`
   (byte-equal to the architect's "Handing off" section, pinned by a test)
   becomes `guide.md`: how to use relevo (bind, status, send, wait, show), the
   recommended loop (a small seed to a planner actor, review its output, hand it
   to a builder, verify the diff before done) and the workarounds (`gate`,
   `stop`, `bind --resume`). `hookContext` keeps its one sentence and embeds the
   guide; `internal/mcp/instructions.go` appends the same guide to both mode
   texts.
3. **The seed guard.** `send` refuses a seed over 4 KiB when the target actor's
   resolved definition is `architect`, unless `--force`, in preflight so
   `--dry-run` agrees and a refusal writes nothing.

In the same round: the reader definitions (`researcher.*`, `reviewer.*`) stop
telling the reader to write into an artifact directory, and the live docs
(README, CLAUDE.md, CONTRIBUTING.md, `.github/`, `docs/design.md`) finish the
display rule (prose `MasterMind`) and the round vocabulary (`prompt`,
`<label>.md`, `relevo show --output`).

## 2. File Structure

```
internal/harness/agents/architect.{claude,opencode,agy}.md   body replaced; agy loses write_to_file
internal/harness/agents/architect.codex.toml                 the same body in the TOML literal
internal/harness/agents/{researcher,reviewer}.{claude,opencode,agy}.md
internal/harness/agents/{researcher,reviewer}.codex.toml     artifact-directory clause dropped (8 files)
internal/harness/agents/shipped.sha256                       regenerated, never hand-edited
internal/harness/harness_test.go                             three architect tests retargeted; one new reader test
internal/mastermind/guide.md                                 NEW: handoff.md moved here and rewritten
internal/mastermind/handoff.md                               DELETED (the move's other half)
internal/mastermind/hook.go                                  embed guide.md; hookContext unchanged
internal/mastermind/handoff_test.go                          byte-equality test + helper deleted; hook test retargeted
internal/mastermind/mastermind_test.go                       handoffRules -> Guide() (2 lines)
internal/mcp/instructions.go                                 both mode texts carry the guide
internal/mcp/waitcmd_test.go                                 pin the guide on both modes
internal/mcp/testdata/contract/instructions.golden           regenerated, diff read
internal/relevo/send.go                                      planner cap in preflight; SendOptions.Force
internal/relevo/send_test.go                                 the cap's cases
cmd/relevo/send.go                                           --force
README.md, CLAUDE.md, CONTRIBUTING.md                        prose sweep + the hand edits of step 6
.github/ISSUE_TEMPLATE/feature_request.yml                   prose sweep
docs/design.md                                               prose sweep + one identifier restore
docs/plans/2026-09-27-actor-contract.md                      this plan
```

## 3. Data Structures & Type Definitions

### 3.1 The architect body — the planner contract (4 files, byte-identical)

Replace everything after the frontmatter (claude lines 11-187, opencode 12-188,
agy 22-198; codex inside the `developer_instructions` literal, lines 4-180) with
one canonical body: a two-line contract, then five sections. It must say:

- **The contract.** You are relevo's planning actor: a reader round that takes a
  seed and answers with one plan — not a session. Never edit the repository,
  never build to verify.
- **`## Your input`**: a small seed — a task reference and the decisions already
  made; read CLAUDE.md and the code it points at and discover the rest yourself;
  a seed that contradicts the code is said in the plan, not guessed around.
- **`## Your output`**: your final message is the plan, it is your only output,
  and relevo saves it as the actor's `plan.md`; write no file.
- **`## What a plan says`**: the behaviour and the cases; the seams (files,
  types, functions, line ranges); one-line ordered steps, each naming its
  deliverable and how to know it worked; a closed numbered list when the work
  deletes behaviour; what the report must include. Then one line: no code
  bodies, no implementation essays, no test cases — a strong builder owns the
  how.
- **`## Working efficiently`**: hand over locations, not searches (file,
  function, line range); batch independent reads/searches/edits into one step;
  one edit call per file, or one scripted sweep when the change is mechanical;
  iterate on a focused test command, fixing every reported error before the next
  run, and run the full check once at the end — name both commands.
- **`## Halt rather than improvise`**: a step that is impossible as written, or
  contradicts the code, halts the round and is reported; a halt that surfaces a
  design error beats a green suite that bent a test.

The frontmatter stays, including its `description` (scope fence). What is
dropped is a closed list in §8.

### 3.2 The guide (`internal/mastermind/guide.md`)

≤ ~30 lines, harness-neutral, and it must not contain `broken`, `orphaned` or
`background wait` (`TestMCPInstructionsDependOnMode` bans those in channel mode,
and the guide is appended there):

- a one-line statement that this is the relevo guide;
- **Using relevo**: `relevo bind [--worktree] --name <n>` (a fresh runner on the
  current tree or its own worktree; `relevo status` shows what is bound and what
  it is doing); `relevo send --name <n> --file <path>` (a round's input is its
  prompt; a planner actor's prompt is a small seed); `relevo wait --name <n>
  --timeout <budget>` (0 closes with a report, 3 is NEEDS YOU — ask the human, 4
  is DONE); `relevo show <n> [--round N] --prompt|--report|--output|--diff|--transcript`
  (a reader's artifact is `<label>.md`, printed with `--output`);
- **The recommended loop**: seed a planner actor (`planner`, `lite-planner`)
  with the task and the decisions already made; review its plan with
  `relevo show <name> --output`; hand that file to a builder with `relevo send
  --name <builder> --file <the output path>`; when the round closes, run the
  project's own check and compare the diff against the plan before `relevo done`;
- **When something is stuck**: `relevo gate <token> --reason '<what it said>'`
  when a runner reports a usage limit (relevo switches and resends; `relevo gate
  --clear <provider>` when it lifts); `relevo stop <name>` ends an open round;
  `relevo bind --resume --name <n>` restores a released worktree; a binding that
  says NEEDS YOU is waiting on a human.

### 3.3 The seed cap (`internal/relevo`)

```
const plannerAgent = "architect"   // the definition whose actor answers with a plan
const seedMaxBytes  = 4 << 10      // a planner's input is a seed; it reads the code itself

type SendOptions struct { ...; Force bool }   // send.go:91
```

`Force`'s comment says why: a planner's seed is capped, and forcing is the
explicit escape. `cmd/relevo/send.go`'s `sendFlagValues` gains `force *bool`
inside `sendFlagSet` (`--force`, help: "send a planner actor's seed even when it
is over the 4 KiB cap"), and `cmdSend` maps it into `opts.Force`.

## 4. Interface Definitions & Component Contracts

### 4.1 `Guide() string` — `internal/mastermind`

- Single responsibility: the shared guide text, one copy for the hook and MCP.
- `//go:embed guide.md` keeps the file as the source; `func Guide() string`
  returns it (callers are not handed the mutable variable).
- `HookOutput` (hook.go:81) and `HookOutputNoEnv` (:86) keep today's shape:
  `hookContext(r) + "\n\n" + Guide()` and `hookContext(r) + " " + noEnvNote +
  "\n\n" + Guide()`. `hookContext` and `noEnvNote` are unchanged.

### 4.2 `InstructionsFor(mode) string` — `internal/mcp`

- Returns the mode's existing prelude (`InstructionsChannel` / `InstructionsTools`,
  wording unchanged) followed by a blank line and `mastermind.Guide()`. The
  guide is the last thing either mode's model reads. `internal/mcp` already
  depends on `internal/mastermind`; no new import edge. The consts' comments
  say they are preludes now.
- Precondition: none. Postcondition: both modes end with `Guide()`.

### 4.3 `sendPreflight` — the planner cap (`internal/relevo/send.go:162`)

- Insert after the tier resolution (send.go:265), before `promptPath` (:267):
  when `!opts.Force && len(body) > seedMaxBytes` and the binding's actor resolves
  to `plannerAgent`, refuse and name the size, the cap and the override:
  `binding %q: the seed is %d bytes; a planner actor takes at most %d bytes -- pass --force to send it anyway`.
- The actor check rides `bindingSpec(rt, b, b.Builder.Kind)` (the seam
  `readerOutputLabel` also uses): `spec.Definition == plannerAgent`. A role that
  no longer resolves is not this error's business; the existing error surfaces
  where it does today.
- Preconditions: none beyond the file being readable — the check runs after the
  body is read. Postconditions: on refusal, no write of any kind. It is in the
  read-only preflight, which both `Send` and `SendDryRun` (:699) call, so a
  `--dry-run` returns the identical refusal and changes no state.
- Placement is before the remote early return, so a binding is judged by the
  same rule on either transport; readers are local-only today, so in practice it
  fires on local planner bindings. `Force` is not carried over the remote wire,
  and the MCP send tool and the cockpit keep today's surfaces (scope fence, §8).

## 5. High-Level Pseudocode

```
sendPreflight:
    body := read file                                    (unchanged, first)
    ... paused, --candidate resolution, stale repick, tier ...   (unchanged)
    if !opts.Force and len(body) > seedMaxBytes and actorRunsPlanner(rt, b):
        refuse "binding %q: the seed is %d bytes; a planner actor takes at most
               %d bytes -- pass --force to send it anyway"
    ... every later precondition unchanged ...

actorRunsPlanner(rt, b):
    spec, err := bindingSpec(rt, b, b.Builder.Kind)
    return err == nil and spec.Definition == plannerAgent

hook output:
    additionalContext = hookContext(record) + "\n\n" + guide
    (no-env case: hookContext(record) + " " + noEnvNote + "\n\n" + guide)

mcp initialize:
    instructions = the mode's prelude + "\n" + guide

docs sweep (one scripted run, §7): five files, standalone prose
    "mastermind(s)" -> "MasterMind(s)", command/flag/identifier spellings
    protected, one identifier restored.
```

## 6. Error Handling Strategy

- One new refusal: the planner cap. Recoverable, preflight-only, exit 1; it
  names the size and `--force`. `--force` spends the cap and the round proceeds
  exactly as today. Everything else keeps its current error and wording.
- `Guide()` cannot fail (embedded); the MCP text is a pure function of the mode.
- The docs sweep changes prose only; a wrong change is a review finding caught by
  the residue grep, not a runtime failure.
- Halt and report, do not improvise, if: the four architect bodies are not
  byte-identical when you start (up to the codex TOML wrapper); a retargeted
  harness test pins a fragment that is not in §3.1; the channel-mode test
  rejects a word the guide uses (rewrite the guide, never the test's ban list);
  the docs residue holds a line that is neither a `relevo mastermind` command, a
  `--mastermind`/`--all-masterminds` flag, `RELEVO_MASTERMIND`, an
  identifier/key/path, nor the pane names `cmastermind`/`amastermind`; or
  `sh scripts/agents-shipped.sh --check` fails after `--write`.

## 7. Working Efficiently

- Read once, in one parallel batch: the four architect files;
  `internal/harness/agents/researcher.claude.md` and `reviewer.claude.md` (the
  other six carry the same sentence); `internal/harness/harness_test.go` 160-245;
  `internal/mastermind/hook.go` 60-90; `internal/mastermind/handoff_test.go`;
  `internal/mastermind/mastermind_test.go` 255-310; `internal/mcp/instructions.go`;
  `internal/mcp/waitcmd_test.go` 90-125; `internal/relevo/send.go` 90-115 and
  156-365; `cmd/relevo/send.go`; README.md 260-300, 505-530, 1820-1850,
  1960-2015.
- The body is one text written once and pasted into four files; make each file's
  change in one edit call.
- **The docs sweep is one scripted run, never hand-edited.** It runs exactly
  once, then the one restore:

```sh
python3 - <<'PY'
import re
files = ["README.md", "CLAUDE.md", "CONTRIBUTING.md", "docs/design.md",
         ".github/ISSUE_TEMPLATE/feature_request.yml"]
pat = re.compile(r'(?<![A-Za-z0-9_./-])masterminds?(?![A-Za-z0-9_/])(?!\.[A-Za-z0-9_])')
sub = lambda m: "MasterMinds" if m.group(0).endswith("s") else "MasterMind"
for f in files:
    s = open(f, encoding="utf-8").read()
    s = s.replace("relevo mastermind", "<<relevo-cmd>>")   # command text stays
    s = pat.sub(sub, s)
    open(f, "w", encoding="utf-8").write(s.replace("<<relevo-cmd>>", "relevo mastermind"))
PY
sed -i 's/architect role: `MasterMind`/architect role: `mastermind`/' docs/design.md
```

  The pattern capitalizes the standalone prose word and never a token glued to
  `_`, `/`, `.` or a leading `-`; `relevo mastermind`, `--mastermind`,
  `--all-masterminds`, `RELEVO_MASTERMIND` and `internal/mastermind` are already
  protected by it. The one restore puts `docs/design.md`'s role-name list back
  (`` `mastermind`, `cmastermind`, `amastermind` `` are identifiers).
- Then the hand edits (one edit call per file): README's send bullet, cockpit
  tab, planner-actor sentence and architect section (step 6); CLAUDE.md's
  dispatch paragraph.
- Focused loop, every reported error fixed before the next run:
  `go test ./internal/harness/ ./internal/mastermind/ ./internal/mcp/ -count=1`,
  then `go test ./internal/relevo/ -run 'Send|DryRun' -count=1`, then
  `go test ./cmd/relevo/ -run 'Send|Removed|MasterMind' -count=1`.
- The one shipped-definitions gate: `sh scripts/agents-shipped.sh --write` once,
  after every definition edit (it unions with the committed index, so one run
  records all of them), then `sh scripts/agents-shipped.sh --check`.
- Goldens: `go test ./internal/mcp -run Contract -update`, then read the
  `instructions.golden` diff and confirm the only added bytes are the guide.
- Full check once, at the end: `make check`, then `make e2e`, then `gofmt -l .`
  (prints nothing). No package moves, so the coverage baseline is **not**
  regenerated; a coverage failure means a test is missing, not a lower bar.
- CI has no harness and no network: every test here is pure or fixture-based; do
  not spawn a harness.

## 8. Ordered Implementation Steps

1. **The planner contract in the four architect copies (§3.1).** Replace the
   body; delete `  - write_to_file` from `architect.agy.md`'s `tools:` list
   (line 20). Retarget `internal/harness/harness_test.go`:
   - :166 `TestArchitectShipsOnEveryKindAndIsNotARole`: keep the frontmatter
     wants; the body want becomes `one-line ordered steps`.
   - :191 → `TestArchitectPlannerContractIsSharedAcrossKinds`: wants `a small
     seed`, `Your final message is the plan`, `never edit the repository`,
     `one-line ordered steps`, `a strong builder owns the how`; bans `##
     Handing off`, `relevo bind`, `relevo send`, `relevo wait`,
     `RELEVO_MASTERMIND`, plus today's `Agent tool`/`slash command`; the
     identical-body comparison stays.
   - :219 → `TestArchitectCarriesRoundTripGuidance`: wants `## Working
     efficiently`, `Hand over locations, not searches`, `one edit call`, `run
     the full check once`; its ordering check becomes Working efficiently before
     `## Halt rather than improvise`.
   Verify: `go test ./internal/harness/ -count=1`. Mutation-check: put one
   sentence of `## Handing off` back, confirm the shared-body test fails,
   restore.
2. **The readers write nothing (the eight definition files).** Replace the
   sentence `...git state; when your prompt names an artifact directory, write
   your files there and nowhere else.` — researcher at claude:14, opencode:15,
   agy:25, codex:19; reviewer at claude:15, opencode:15, agy:26, codex:14 —
   with a final-message sentence: the researcher's report / the reviewer's
   findings is the final message and the whole of it, and no file is written.
   Add `TestReaderDefinitionsWriteNoFile` (pattern: `TestArchitectShips...`):
   for all eight files the body has no `artifact directory` and carries the
   final-message sentence. Then `sh scripts/agents-shipped.sh --write` and
   `sh scripts/agents-shipped.sh --check`. Verify:
   `go test ./internal/harness/ -count=1`.
3. **The guide (§3.2, §4.1).** `git mv internal/mastermind/handoff.md
   internal/mastermind/guide.md`; write the guide; `hook.go`: the embed target
   and `Guide()`, and both hook outputs call it. `handoff_test.go`: delete
   `TestHandoffRulesMatchShippedArchitectCopies` (lines 38-61) and
   `handingOffSection` (16-36); retarget what remains to
   `TestHookOutputCarriesTheGuide` (context still starts with `hookContext`,
   now ends with `Guide()`). `mastermind_test.go`: `handoffRules` → `Guide()` in
   the two exact-JSON tests (280, 300). Verify:
   `go test ./internal/mastermind/ -count=1`.
4. **MCP carries the guide (§4.2).** `InstructionsFor` appends `Guide()`;
   `waitcmd_test.go`'s mode test gains: both texts end with `mastermind.Guide()`
   and still pass their existing bans. Regenerate
   `internal/mcp/testdata/contract/instructions.golden`
   (`go test ./internal/mcp -run Contract -update`) and read the diff. Verify:
   `go test ./internal/mcp/ -count=1`.
5. **The seed cap (§3.3, §4.3).** The two consts, the actor check, the preflight
   refusal, `SendOptions.Force`; `--force` in `cmd/relevo/send.go`. Cases in
   `internal/relevo/send_test.go`, with a file registry (`rolesFileRegistry`)
   that adds a reader row for a custom `planner` role (`Shape: ptr("reader")`,
   `Definitions: {"claude": {Agent: "architect"}}`, `Candidates:
   {testClaudeRef}`) beside a builder row; the sending cases follow
   `TestReaderRoundRunsInItsScratch` (a real repo via `readerRepo(t)` and
   `rt.Git = git.NewClient("git", 0, 0)`):
   - a 4097-byte seed to the planner actor is refused; the error names `4097`
     and `--force`; nothing is written (no prompt file, no log entry, the
     binding's round unchanged);
   - `SendOptions{Force: true}` sends the same seed;
   - 4096 bytes is not over the cap and sends;
   - a 5000-byte seed to a `reviewer` actor and to the builder sends;
   - `SendDryRun` returns the same refusal for the same state.
   Verify: `go test ./internal/relevo/ -run 'Send|DryRun' -count=1`.
   Mutation-check: drop the guard, confirm the refusal case fails, restore.
6. **The docs (sweep + hand edits).** Run the §7 sweep once, then the restore;
   then, one edit call per file:
   - README:279 add `[--force]` to the send usage line, change "stage the file
     as the current round's plan" to `prompt`, and add one sentence: a planner
     actor's prompt is a seed, capped at 4 KiB, and a larger one is refused
     unless `--force`;
   - README:524 the cockpit bullet `**plan**` → `**prompt**` ("the round's own
     prompt file");
   - README:1829-1830 "read its plan back from the round's output"; :1842 "(or
     MasterMind step)";
   - README:1968-2012 rewrite the architect section: the architect is the
     planner actor's contract and a session persona; `planner`/`lite-planner`
     run it as a reader round (a seed in, the plan out as the final message,
     saved as `plan.md`, minimal, never edits, never builds); its agy copy pins
     `model: inherit` and a read-only tool set, and claude/opencode leave
     `model:` unset; the `relevo mastermind init` hook injects the relevo guide,
     so a session on any agent receives it; `--actor architect` stays refused,
     and doctor reports only the role table's definitions. Keep the
     `relevo config agents` block and the harness examples.
   - CLAUDE.md:9-12: the dispatch protocol now lives in the injected guide
     (`internal/mastermind/guide.md`), not in the architect definition.
   Verify: `git grep -n 'mastermind' README.md CLAUDE.md CONTRIBUTING.md
   docs/design.md .github/ISSUE_TEMPLATE/feature_request.yml` — every remaining
   line is a `relevo mastermind` command, a flag, `RELEVO_MASTERMIND`, an
   identifier/key/path, or a pane name; and `git grep -n 'summary.md' README.md`
   finds only the legacy-fallback sentence (1771).
7. **Full check, e2e, commit.** `make check`, `gofmt -l .`, `make e2e`; copy the
   round's staged prompt to `docs/plans/2026-09-27-actor-contract.md`; one
   commit holding code, tests, definitions, `shipped.sha256`, goldens, docs and
   the plan.

### Closed list of what is deleted (everything not on this list survives)

1. The architect body's `## Handing off` section in all four copies, replaced by
   the guide, which now lives once in `internal/mastermind/guide.md`.
2. The architect body's `## Strict Boundaries`, `## Output Structure`,
   `## Quality Standards`, `## Self-Verification Checklist` and `## Writing for
   the Builder's Round Trips` sections, replaced by §3.1's minimal contract (the
   owner's "a strong builder owns the how" decision); every fragment the harness
   tests pinned among them is retargeted in step 1.
3. `  - write_to_file` in `architect.agy.md`'s tool list.
4. `internal/mastermind/handoff.md`, as a name and as text (moved to `guide.md`
   and rewritten).
5. `internal/mastermind/handoff_test.go`'s
   `TestHandoffRulesMatchShippedArchitectCopies` (38-61) and its helper
   `handingOffSection` (16-36) — they pin the byte tie this round ends.
6. The reader definitions' clause `when your prompt names an artifact directory,
   write your files there and nowhere else` (8 files), replaced by the
   final-message sentence.

No other test is deleted. Every test that asserted a listed item is retargeted
and keeps its other assertions.

### Report must include

- `git diff --stat`, and the statement that a harness test proves the four
  architect bodies share one body.
- `sh scripts/agents-shipped.sh --check`'s output and the `shipped.sha256` line
  delta.
- The mutation checks of steps 1 and 5, each with the failing test's name.
- The two verification greps of step 6, verbatim.
- `make check` and `make e2e`, and that the coverage baseline did not move (no
  package moved).
- The scope fences a reader should expect: the frontmatter `description` stays;
  the MCP send tool and the cockpit gain no force argument (Bash `relevo send
  --force` is the override, and a remote binding's admission re-checks the cap
  without `Force`); `docs/design.md` is capitalization-only; `docs/plans/**` is
  untouched.
