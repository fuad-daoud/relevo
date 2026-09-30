# Cockpit C1 handoff (2026-09-26, end of session 5)

Read this first. It continues `docs/plans/2026-09-25-cockpit-d2-handoff-3.md`. Handoff-2's §1 (how this user works:
canvas first, real data only, no captions, no dollars, 3-cell margins, every table lists everything) and §4
(operations) still hold in full. Read them. This file adds what session 5 did.

## 1. What landed

| PR | what |
|---|---|
| #506 | Mouse wheel. The wheel sends ↑↓ ×3 to the top view; Shift+drag selects text. Merged `574b11f`. |
| #510 | **C1: `:candidates`, `:actors`, `:agents`**. Merged `0583bea` after the user verified it (7 builder rounds on binding `ck-config`; round 7 auto-picks a sole provider, e.g. claude → anthropic). |

## 2. The C1 design, as the user approved it

The canvas rows are "Config in D2", "…: edit and delete", "Actors and agents in D2" and "Agents in D2: files, edit,
reset, delete" (y 9490-13000). The canvas is https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA. The generator was
`scratchpad/gen_config.py`, which is **not kept**. The boards are real data except where a title says
"(illustration)".

**Candidates**
- **Columns:** CANDIDATE, HARNESS, PROVIDER, MODEL, SERVES, STATUS. **No round counts:** "the config view does not need
  to know about how many rounds, this is not its job".
  - SERVES is `builder 4 · reviewer 2`: the actor and its position.
- **Dimming:** a row is dim only when **no actor has it on**.
- **Add:** the harness is picked from the supported list only; new harnesses (deepseek, pi…) come later as code.
- **Providers:** agy and claude have **locked** provider lists (`harness.Providers`: agy `google, agy-extra`; claude
  `anthropic`). opencode and **codex** take any typed provider, with the existing ones offered as chips and
  `new provider` shown for an unknown one.
- **Edit:** every field can change: harness, provider and model. **The name follows the model**, and actor lists follow
  the rename. Validation runs live and blocks the save.
- **Delete:** needs a red y/n confirm. It is refused when an actor would be left with no candidate.
- **On/off lives on each actor's entry** (`actors.Entry.Off`), not on the candidate. `:candidates` has no on/off key.

**Actors**
- An actor maps an agent to an ordered candidate list.
- The list is edited on `actors › <name>`: shift+↑↓ reorders, space toggles on/off, `a` picks from the candidates not in
  the list, `d` removes one.
- `e` opens a form for the agent, tier and check. A builtin actor only offers agents of its shape, and check is disabled
  for readers.
- The **CHECK column was dropped**. It is a plain line on the opened actor instead. On this machine
  `policy.gate.default` is unset, so "check on" runs nothing; the line says so and points at `:settings`.
- Actors can be added and deleted; builtin ones (builder, reviewer, researcher) cannot be deleted.

**Agents**
- The table has a SOURCE column (`shipped`/`custom`) and a per-harness state column.
- `enter` opens a per-harness file table:
  - `e` edits in `$EDITOR`;
  - `r` resets `your edit`/`stale`/`missing`, behind a confirm.
- `d` deletes **custom** agents only.
- **Built as true, not as drawn:**
  - relevo installs all four shipped agents on every harness, so reviewer and architect show `ok` everywhere;
  - relevo never writes custom agents to disk, so delete removes the config entry only.

## 3. Open items for the next session

1. **#510 is merged** (the user verified it). Install it where the user runs relevo, if they ask.
2. **Finish C1 (the user deferred these): `:settings` (policy) and `:audit` (config revisions).**
   - The canvas row-2 boards (`Settings.dc.html`, `Audit.dc.html`, `SaveDraft.dc.html`) predate D2.
   - Redesign them on the canvas with real data, one at a time, and get approval before building.
   - Real data:
     - `relevo config` / `config_doc` `policy` = `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}}}`, with no `gate.default`;
     - revisions: `sqlite3 -readonly ~/.local/state/relevo/relevo.db "select rev,at,source,message from config_revision order by rev desc limit 20"`.
     - The C1 views now write revisions with source `ui`.
3. **Known gaps in the C1 PR** (tell the user; none blocks):
   - The **footer cannot grey a key**: `KeyHelp` has no disabled flag, so `d delete` on a shipped agent and `r reset` on
     an up-to-date file are listed normally and answer with a notice. A fix is a `Disabled` field that `keysView`
     honours. Watch out: positional `KeyHelp{"k","h"}` literals are everywhere.
   - **`p probe` was dropped from the candidate form**, because `p` must type into the fields. Probe from the list.
   - **Other views don't see a config change until restart.** `:stats` names and the bind picker use the startup
     `rt.Candidates`. `plannerActions` reloads its own copy after each write, but `plannerSource` holds a value copy of
     rt. The config views themselves re-read the doc on every status refresh.
   - The **"stale gate" case**: after a candidate's provider changes, or its last candidate is deleted, the old
     provider's rate-limit gate stays in the ledger and nothing shows it (`relevo.Gates` projects onto configured
     tokens only). The design said to list gates on providers no candidate uses; that is **not built**.
   - The **running-builder token bug** (found this session, not fixed): after a candidate edit, a running binding's
     `BuilderCandidate` still holds the old token. `gatedBuilder` (internal/relevo/switch.go:22) and send
     (send.go:279) compare it with `Gates()`, which only emits configured tokens, so the daemon never switches that
     builder off a gated provider. It is its own small PR: map the old token to the candidate by name, or record the
     name.
   - **Agent `your edit` + newer copy:** relevo cannot tell "your edit" from "your edit and relevo has a newer copy"
     today (install.go only says kept-differs). The user wanted that shown; it needs install.go to compare against the
     current shipped copy as well.
4. **The rest of the roadmap** (handoff-3 §3): A2 round 4 (custom agents; `:agents` will list them), A3b, A4, A5, C3.

## 4. Operational notes from this session

- **Builder:** relevo picked sonnet for #506 and deepseek-v4.1-flash (opencode/cline-pass) for all 6 ck-config rounds,
  each 6-16 minutes. There were no halts. deepseek's reports sometimes end with an unclosed `not_done` list, which makes
  `relevo wait` exit 2 ("unstructured"); the report is still complete. Read the output file.
- **Plans ahead:** writing rounds 3-5 while round 2 ran worked well. Each later plan says "use the real name and report
  it" for names an earlier round might change.
- **Verification** (the laptop hook refuses `make check`):
  - on the server: `dev run sh -c 'go vet ./... && go test -race -count=1 ./...'` from the worktree;
  - locally: gofmt, `go mod tidy -diff`, check-plugin-version, check-name and shellcheck.
  - Real-screen checks built the branch commit in a separate detached worktree (`~/.cache/relevo-verify/wt-r2`),
    because the builder was editing the binding's worktree. Remove it with `git worktree remove` when done.
- **Mutation tests found a real gap in #506:** `fakeView` recorded only keys, so "mouse events are never broadcast" was
  unpinned until round 2 added a `mice` counter. Keep asking every round for one required mutation.
- **The main checkout is still at `9471682`.** `git pull` aborts on the untracked `docs/plans` files. Use worktrees.
