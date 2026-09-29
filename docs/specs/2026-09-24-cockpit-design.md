# The cockpit: actors, agents, candidates, and a TUI that acts

Date: 2026-09-24. Status: **design approved in conversation; spec for review**.
Mockups: the "relevo cockpit" design canvas (13 terminal screens at 132×34),
https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA. Supersedes the "builder → actor
vocabulary rename: deferred, no issue" row of `2026-09-24-writer-roles-design.md`, and
the "strictly read-only" rule of `2026-09-17-relay-ui-split-design.md` and
`2026-09-21-dashboard-design.md`.

## 1. Purpose

Today the things a user configures and watches are spread over text dumps and JSON
paths:

- `relevo config` prints three blocks (roles, pick, candidates). Each repeats the same
  candidate tokens with a different annotation, so "who runs next, and why not the
  others?" takes all three to answer.
- Editing means JSON paths. Reordering the builder list is
  `relevo config set roles.builder.candidates '[…six tokens…]'` or `$EDITOR` on JSON.
- The only identity a candidate has is `harness/provider/model`
  (`opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high`), and it is truncated
  everywhere it is shown.
- `relevo ui` is read-only by design and shows no config at all.
- Stats are split across `history --stats`, `history --tab` and the dashboard grid.
  Old rows dominate (`unknown 137` builders, `unstructured 250` outcomes), and nothing
  shows a candidate's health in one place.
- The model is written twice: every candidate lists its `roles` while every role lists
  its `candidates`, and a tier can be set in four places.
- relevo now runs more than builders. Planner, researcher and designer rounds each do
  different work and leave different output, and "builder" no longer names what a
  binding runs.

This spec does four things. It reshapes the model into **candidates, agents and
actors**. It turns `relevo ui` into a **k9s-style cockpit** that acts on rounds and on
config. It makes every config write a **reviewable, audited, reversible revision**.
And it rebuilds **stats** around four questions. Breaking changes are accepted.

## 2. Decisions

| # | decision |
|---|---|
| D1 | The TUI acts: on config (drafted, saved, audited) and on rounds (stop, done, unbind, gate, retry-on, send, bind). It does not land (`relevo land` stays CLI-only). |
| D2 | Every candidate has a short unique **name**, derived automatically and renameable. The name is its identity on every human surface and every input. The `harness/provider/model` triple is kept as attributes, is what the DB records, and still resolves on the CLI. |
| D3 | **Agent** = the prompt and contract: shape (writer/reader), output label, one source rendered for every harness kind. **Actor** = a named agent plus candidates in order of availability, plus tier and check. Several actors may share one agent. **Role** is retired. |
| D4 | A **binding** is a running instance of one actor, of either shape. Each round it is that actor's agent on the first available candidate. |
| D5 | Every round has an **artifact directory**, which may hold any files (`.md`, `.html`, `.css`, `.js`, …). Its `summary.md` is the final message and is always present. A writer also commits to the tree and leaves `report.md`. |
| D6 | A **reader** round runs in a throwaway detached **scratch worktree** at the binding's HEAD plus its uncommitted diff. Whatever it does to that tree is discarded at close. |
| D7 | Custom agents have **one source** (frontmatter plus prompt body) stored in relevo.db. relevo renders and installs each harness kind's file from it. |
| D8 | The TUI is **k9s-style**: views reached with `:` commands, tables, Enter drills in, Esc goes back, `/` filters, `?` helps. |
| D9 | Config edits in the TUI go into a persistent **draft**. Save shows the list of changes and asks for confirmation. Every write from any source is a **revision**. Rollback loads an old revision into a draft and saves it as a new revision. |
| D10 | `:stats` answers four questions: which candidate is best (per actor), spend over time, reliability/limits, and per repo/feature. |

## 3. The model

### 3.1 Candidate

| field | type | rule |
|---|---|---|
| `name` | string | Required once migrated. `^[a-z0-9][a-z0-9.-]{0,23}$`: no `/`, so a name can never be mistaken for a token. Unique among candidates. Must not equal any configured provider name, so `relevo gate --clear <x>` stays unambiguous. |
| `harness`, `provider`, `model` | string | As today. The triple stays unique. |
| `effort` | — | Not a new field. It stays inside `model` (`#high`, `:high`), as today. |
| `plan` | bool | As today: a subscription lane, shown as `plan`, never `$0`. |
| `tier` | tier | Optional override of the actor's tier. |
| `tree`, `extra_args`, `limit_patterns`, `denial_patterns` | as today | Unchanged. |
| ~~`roles`~~ | — | Removed. An actor's list is the only place that says who a candidate serves (A2). |
| ~~`dialog_patterns`~~ | — | Removed. It has been ignored since #303. |

**Name derivation.** It is deterministic and runs in config order:

1. Take the model's last `/`-segment.
2. Strip an effort suffix (`#…` or `:…`).
3. Lowercase it, turn each run of characters outside `[a-z0-9.-]` into `-`, trim leading `-`/`.`, and truncate to 24 characters.
4. On a collision with an earlier name or a provider name, append `-<effort>` if
   there is one.
5. Still colliding: prefix `<harness>-`. If that collides too, try it with
   `-<effort>` appended.
6. Still colliding: append `-2`, `-3`, …

Explicit names and every provider name are reserved before any name is derived.

On this machine that gives `deepseek-v4.1-flash`, `gpt-5.6-terra`,
`claude-sonnet-4-6`, `gemini-3.8-flash-high`, `sonnet`, `haiku` and `glm-5.3-flash`.

**Resolution.** One function takes a user string to a candidate:

- A string containing `/` is parsed as a token and looked up by its triple.
- Anything else is looked up by name.
- An unknown string errors with `unknown candidate "x" (known: <names>)`.

Every input that accepts a token today accepts a name.

**Display.** Every human surface prints the name. A stored token whose triple is no
longer configured prints as the token itself. `--json` outputs keep the token and add
`name` beside it.

### 3.2 Agent

| field | type | rule |
|---|---|---|
| `name` | string | `^[a-z0-9][a-z0-9._-]{0,63}$`, because it becomes a file name, as in roles-and-actors §3. |
| `description` | string | One line, used for the harness's agent frontmatter. |
| `shape` | `writer` \| `reader` | A writer may change the tree. A reader may write only its artifact dir. |
| `output` | label | `^[a-z][a-z0-9-]{0,23}$`. Names what the round leaves: `report`, `plan`, `findings`, `notes`, `design`, … It labels the artifact tab and is what a chain (#388) hands forward. |
| `prompt` | markdown | The body, identical for every kind. |
| `requires` | []agent name | Helper agents this one spawns (plan-executor requires researcher). |
| `kinds` | []harness kind | Which kinds to render and install for. Default: every known kind. |
| `overrides` | map kind → agent name | Escape hatch: use an existing harness-native agent for that kind instead of rendering. |
| `source` | `shipped` \| `custom` | Derived. Shipped agents (`plan-executor`, `reviewer`, `researcher`, `architect`) are read-only. Duplicating one yields a custom copy. |

**Rendering.** A pure function takes (custom agent, kind) to the bytes of that kind's
file: the claude `.md`, opencode `.md`, agy `.md` with its tool list, and codex
`.toml`.

- **Shipped agents are not rendered.** They stay the hand-maintained per-kind files in
  `internal/harness/agents/`. The agy bodies are rewritten, plan-executor has three
  body variants, and the frontmatter was chosen by hand (see the A2a plan), so one
  source cannot reproduce them.
- **Duplicating a shipped agent** starts the custom copy from its claude body.
- **Shape does not change the tool list.** A reader needs write tools to fill its
  artifact directory, and the scratch worktree is what keeps it off the binding's tree
  (D6). So a reader and a writer render the same tools. Shape decides where the round
  runs, not what the agent may call.

**Install state per kind:** `installed`, `stale` (the source changed since the last
install) or `not installed`.

### 3.3 Actor

| field | type | rule |
|---|---|---|
| `name` | string | Same pattern as agent names. Unique. |
| `agent` | agent name | Must exist. |
| `candidates` | `[{candidate, off}]` | An ordered list of candidate names with no duplicates. `off: true` keeps the position but skips the candidate. A candidate whose harness kind the agent is not installed for is **flagged**, not dropped: the TUI marks it, and the pick skips it with that reason. |
| `tier` | tier | Capped by `policy.max_tier`. |
| `check` | bool | Writers only: run the project's check after the round (today's role `gate`). Defaults to true for writers. Refused on readers. |

Seeded actors: `builder` (plan-executor), `reviewer` (reviewer) and `researcher`
(researcher).

**Pick.** Walk `candidates` top to bottom and take the first entry that is on, not
gated, and has the agent installed for its kind. A usage limit gates the whole
provider. A spawn failure gates one candidate for 10 minutes. Both are unchanged from
today.

### 3.4 Binding and round

- A binding records its **actor** (chosen at bind, fixed thereafter) and, for each
  round, the **candidate** that ran it. This is the writer-roles rule with the word
  changed.
- Each round's artifact dir lives at `spool/<binding>/NNN-<actor>/` while the round is
  open. When the round seals it becomes `artifact` rows (db-as-record D7).
- `summary.md` is the actor's final message, written by relevo, as the consult's
  findings are today (`internal/relevo/ask.go:35`).
- A writer also writes `report.md` there, which is today's report.
- A reader round's scratch worktree is `.worktrees/.scratch/<binding>-NNN`, next to
  `.worktrees/.verify/`.
  - It is detached at the binding's HEAD. The binding tree's whole working state is
    then written in: tracked edits, deletions and untracked files (not ignored ones).
    relevo does this with `SnapshotTree`, then `read-tree --reset -u <tree>`, then
    `reset -q`, so staged and unstaged edits arrive as unstaged changes.
  - It is removed with `git worktree remove --force` at seal, even on halt or stop.
  - A daemon sweep removes leftovers whose round is closed.
- If the scratch worktree cannot be created, the round refuses to start and the
  binding goes NEEDS YOU with the reason. There is never a fallback to the binding's
  own tree.
- Artifact size cap: `policy.artifact_max_mb`, default 25. Above it the round goes
  NEEDS YOU, and nothing is dropped silently.

### 3.5 Tier

Tier is set on the actor, optionally overridden on a candidate, and capped by
`max_tier`. `policy.tier` and `policy.order` are removed.

### 3.6 History

A round keeps the raw candidate token and the actor name. Reads join the token to the
current candidate name, so a rename relabels past rounds, and a deleted candidate shows
its token. Rows with no recorded builder group as `(unrecorded)` and are excluded from
scorecards. The `unstructured` report outcome is displayed as `no outcome`.

### 3.7 Renames (breaking)

| today | becomes |
|---|---|
| role | actor |
| `roles` config section | `agents` + `actors` sections |
| binding `builder` slot and `Role` field | `actor` + per-round `candidate` |
| `--builder <token>` (bind, add, fork, send) | `--candidate <name>` |
| `bind --role`, `ask --role` | `--actor` |
| `config roles-init` | folded into `config init` |
| role `gate` | actor `check` |
| `NNN-builder.*` spool files | `NNN-<actor>/` artifact dir |
| `relevo ui --dashboard` | `relevo ui :rounds` |

### 3.8 Migration

It runs once, at daemon start or first CLI open, and only while no round is open.
While rounds are open, `relevo status` says `migration pending: N rounds open`.

- A1 names the candidates in config order.
- A2 turns each `roles` row into an agent reference plus an actor, folds
  `candidate.roles` and `policy.order` into actor lists (the existing
  `roles.<r>.candidates` order first), and folds `policy.tier` into actors.
- A4 renames state rows and spool paths.

Every migration step is recorded as a config revision with source `migration`.

## 4. The cockpit (TUI)

### 4.1 Launching it

- `relevo` with no arguments on a terminal opens the cockpit at `:fleet`. Without a
  terminal it prints help, as today.
- `relevo ui [:view [arg]]` opens a specific view.
- `relevo config` on a terminal opens `:actors`. `relevo config --text`, or no
  terminal, prints text as today.
- `relevo serve ui` stays read-only: it gets no actions (§6.2).

### 4.2 The frame

- **Row 1, header:** a breadcrumb on the left (`relevo › actors › builder`). On the
  right, whatever needs attention in any view: `draft · N unsaved`, `● N need you`,
  gated providers, and the clock.
- **Row 2:** view context (counts, window, sort) on the left and a one-line hint on the
  right.
- **Body:** a table, or a table plus a detail section below a titled rule.
- **Last two rows:** a rule, then the current view's keys.

Global keys:

| key | action |
|---|---|
| `:` | command line with fuzzy completion over view names and resource names (`:actor designer`, `:candidate haiku`) |
| `/` | filter the current table (the full histq grammar in `:rounds`) |
| `?` | help overlay listing every key of the current view |
| `esc` | back, or close an overlay |
| `enter` | drill in |
| `q` | quit at the root (`ctrl+c` quits anywhere) |
| `S`, `U` | save or discard the draft, when one exists |

### 4.3 Views

| view | rows | enter | keys |
|---|---|---|---|
| `:fleet` | bindings sorted by attention: name, actor, on (candidate), round, state, now, spend, planner, repo. A NEEDS YOU question is quoted under its row. A "recent" event log sits below. | the binding's rounds | `s` send plan, `b` bind, `x` stop, `D` done, `u` unbind, `g` gate, `r` retry on…, `o` shell |
| `:rounds` | today's dashboard grid with `by:` grouping | round detail | the fleet round actions, scoped |
| round detail | tabs: plan · artifacts · diff (writers) · log · transcript; `[` `]` move between rounds | open a file: `.md` in the pager, `.html` in the browser, anything else in `$EDITOR` | `o` open, `x` stop, `s` send next |
| `:actors` | name, agent, shape, candidates in order, next pick, tier | the ordered list with live status | `n` new, `R` rename, `d` delete |
| `:actors › <a>` | #, candidate, harness, status + reason, ttft, 30d rounds, done %, $/round, draft note | — | `K`/`J` move, `space` on/off, `a` add, `x` remove, `t` tier, `p` probe, `g` gate provider, `e` edit agent |
| `:agents` | name, shape, output, kinds (installed / stale / not installed), source, used by | prompt preview + install state | `n` scaffold, `e` `$EDITOR`, `c` duplicate, `i` reinstall, `d` delete |
| `:candidates` | name, harness, provider, model, actors, status, ttft, 30d, done %, $/round | detail + recent rounds | `a` add (form), `e` edit, `R` rename, `p` probe, `g` gate, `c` clear gate, `d` delete |
| `:gates` | active gates, plus the 30-day by-hour heatmap | the rounds each gate hit | `g` gate, `c` clear |
| `:stats` | four panels (§5) | `:rounds` filtered to the row | `tab` panel, `w` window, `a` actor, `p` split spend by provider |
| `:settings` | each policy knob: value, default, meaning | inline edit | `backspace` reset to default |
| `:draft` | pending changes | — | `x` undo one change |
| `:audit` | revisions: rev, when, by, changes, message | the changes tab and the json diff tab | `r` roll back to this |
| `:planners`, `:servers` | small tables | — | `d` forget / remove |

### 4.4 How edits work

- **One field** (tier, rename, a setting): a single-line prompt, validated by the
  same validators as `config set`.
- **Several fields** (adding a candidate): a short form. It picks the harness from
  what is on PATH, then provider, model (with suggestions where the harness can list
  them), a suggested name, and which actors to join. It offers a probe after save.
- **The long tail** (patterns, `extra_args`, an agent's prompt): `e` opens that one
  resource in `$EDITOR`. It is validated on save and reopened with the error as a
  comment at the top. Quitting without a change adds nothing.
- **Destructive actions** (stop, done, unbind, delete, gate) ask a one-line `y/N`
  that names the target and anyone affected, for example `planner architect-4 is
  waiting on this round`.
- **Results and errors** show in the footer, in the same words the CLI prints.

### 4.5 Look

This is the site's visual language in 256-colour: one blue accent (focus, the draft,
links), amber only for needs-you, red for gates and errors, tinted greys, thin rules,
and a box only around a modal. `internal/ui/styles.go` stays the only file that names
a colour.

## 5. Stats

Window: 7d, 30d (default), 90d or all. Everything is computed by `internal/stats`
over `db.RoundRow` and the gate records, and shared with `relevo history --stats`
(text and `--json`).

| panel | contents |
|---|---|
| Candidates | Scoped to one actor (`a` cycles; default `builder`). Per candidate: rounds; **done %** = reported / closed; **halt %** = halted / closed; median duration of closed rounds; ttft p50 over the window; **$/round** = mean over rounds with a known cost basis, or `plan`. Rows with fewer than 5 rounds are dimmed. `(unrecorded)` is excluded. |
| Spend | Cost per day over the window as bars, all actors, with this week against last week. `p` splits the bars by provider. |
| Reliability | Switches (count and % of rounds), gates, spawn failures, the by-hour limit heatmap per provider (what `relevo config` prints today), and the gates active now. |
| Repos & features | Per repo, expanding in place to its features, each with its tickets nested beneath it, carrying the repo's own numbers: rounds, cost, halts, and rounds per landed binding (a binding that reached DONE); a label used by two repos appears under each. `(no feature)` is a feature row with its own tickets, and there are no `(no ticket)` rows. |
| Outcomes | Counts of reported, halted, switched and no outcome. |

On the repos tab `space` toggles a repo and `→`/`←` expand and collapse it in place.
A repo expands to its features by tokens desc, each with its tickets nested beneath it by
tokens desc, then `(no feature)` with its tickets. A ticket two features use appears under
each, counting only that feature's rows in that repo. `enter` on a feature filters `:rounds`
to its repo and label; on a ticket under a feature it adds that feature's term; on a ticket
under `(no feature)` it filters to the repo and the ticket; and on a `(no feature)` or
`(no repo)` row it notices instead.

## 6. Architecture

### 6.1 Packages

| package | owns |
|---|---|
| `internal/ui` | The shell: frame, header/footer, `:` command line, view stack, keymap, the confirm/prompt/form widgets, footer messages. It knows nothing about any particular view. |
| `internal/ui/view_<name>.go` | One sub-model per view, behind one `View` interface. They are files in package `ui`, not packages, because the ui tests are in-package. `internal/ui/dash` stays the rounds grid, hosted by `view_rounds.go`. |
| `internal/candidate` | Gains `name`, derivation, `Resolve` and `NameOf`. |
| `internal/agentsrc` (new) | Parse and validate the single-source agent format; `Render(agent, kind)`; install. |
| `internal/actors` (new; replaces `internal/roles`) | The actor registry, the pick, and validation against agents and candidates. |
| `internal/draft` (new, pure) | Typed changes; `Apply(doc, changes)`, `Describe(change)` and `Diff(docA, docB)`. |
| `internal/config` | Gains revisions. One commit path writes the `config_revision` row in the same transaction as every write. |
| `internal/stats` (new, pure) | Scorecard, spend series, reliability, repos, outcomes. |

### 6.2 Reads and writes

- **Reads:** the existing `ui.Source`, extended with a config snapshot, the draft and
  the stats inputs. Config is reloaded only when `config_meta.version` changes.
- **Writes:** a new `ui.Actions` interface, implemented over the same
  `internal/relevo` functions the CLI verbs call (`Send`, `Stop`, `Done`, `Unbind`,
  `Bind`, `Probe`, gate, the draft operations, rollback). The TUI never shells out to
  `relevo`, and results print through the same `…Text` formatters.
- `serve ui` passes a nil `Actions`, so action keys are hidden there.
- Every action runs as a `tea.Cmd`. Shell and `$EDITOR` open with `tea.ExecProcess`
  (bubbletea v1.3.4 has it).
- Some `internal/relevo` paths write straight to `os.Stderr`: ledger read errors,
  spawn-failure notes, and slog's default logger. A full-screen TUI must not let those
  bytes corrupt the screen. For its lifetime the cockpit points `os.Stderr` and the
  default slog handler at a buffer, and shows the last line in the footer.
- `Stop` and `Done` hold the state lock through the kill grace (about 5s), so the
  footer shows the action as running until its message arrives.

### 6.3 The human planner

TUI actions run as a planner record named `you`, of a new harness kind `human`, with
session id `tui`.

- Planner names must match `^[a-z][a-z0-9-]{0,31}$`, so `you@<host>` is not possible.
- relevo.db is per machine, so one `you` per machine is enough.
- A `human` record has no host pid, so it is never pruned.
- A human planner has no deliverer and no channel. Its pending reports are delivered by
  the TUI itself when opened, with confirm route `tui`.

- Bindings bound from the TUI belong to it.
- Their reports surface in `:fleet` as NEEDS YOU `report ready`, and are marked
  delivered once opened.
- Acting on an AI planner's binding is allowed, and its confirm names that planner.
  That planner's `relevo wait` returns exactly as it does after the CLI verb.

### 6.4 Drafts and revisions

> **Amended 2026-09-26: drafts are dropped.** The config views save each edit at once
> as its own revision (`source` `ui`), and `:audit` rolls any revision back. There is no
> `config_draft`, no save step and no conflict list. `config_revision`, `config log` and
> `config rollback` below stand as built. The draft parts of this section, and the
> draft rows in §7 and §8, are kept for the record only.

- **Tables:**
  - `config_draft`: one row with `base_version`, `changes` (JSON list of typed
    changes) and `updated_at`.
  - `config_revision`: `rev` (equals `config_meta.version` after the write), `at`,
    `source` (`tui` | `cli` | `import` | `migration` | `rollback`), `message`,
    `changes` (JSON) and `snapshot` (the whole config document, secrets excluded).
- **Changes** are typed operations on named resources, e.g. `actor.move{actor,
  candidate, to}`, `actor.off{actor, candidate, off}`, `candidate.add{…}`,
  `candidate.rename{from, to}` (which rewrites every actor list),
  `setting.set{path, value}`, `agent.put{…}`.
- `Describe` renders each change as one line, e.g. `builder  deepseek-v4.1-flash
  #3 → #1`.
- **Save:**
  1. Replay the changes onto the current config.
  2. If `base_version` is stale, the replay still runs. A change that no longer
     validates becomes a conflict listed for the user, and save is blocked until each
     conflict is dropped or fixed.
  3. Commit the new document and one revision in one transaction, then clear the
     draft.
- **Rollback of rev N:** load N's snapshot into a draft, whose changes are
  `Diff(current, snapshot)`, and save it as a revision with source `rollback` and
  message `rollback to #N`.
- **Agent installs** happen after the revision commits. A failed install marks that
  kind `stale` and says so in the footer. The revision stands.
- **CLI:**
  - `relevo config log [-n N]` lists revisions.
  - `relevo config log --rev N` shows one.
  - `relevo config rollback N` prints the changes and asks for confirmation, or skips
    the question with `--yes`. It refuses without a terminal unless `--yes` is given.
  - `set`, `unset`, `edit` and `import` each write one revision.

## 7. Errors

| failure | behaviour |
|---|---|
| Refresh fails | Header notice; the last good data stays on screen (as today). |
| Action refused by a core function | Red footer message with that function's error. Core functions are atomic, so nothing is half-done. |
| Edit fails validation | Refused inline with the validator's message. Nothing enters the draft. |
| Save conflicts | Listed. Save is blocked until each one is dropped or fixed. |
| `$EDITOR` result invalid | The file reopens with the error as a comment at the top. |
| Agent install fails after save | The revision stands; the kind is marked `stale`; `i` retries. |
| Scratch worktree cannot be created | The round refuses to start; NEEDS YOU with the reason. |
| Artifacts over the cap | NEEDS YOU; nothing is dropped. |
| Panic | The terminal is restored (as today). |

## 8. Testing

- **Golden renders** of every view at 100 and 160 columns, including draft marks, a
  confirm, the save prompt and a conflict.
- **Pure tests:**
  - Name derivation and collisions.
  - Resolve: name, token, unknown.
  - Each migration from an old document to a new one. This machine's real
    `config export` is a fixture.
  - `Diff(a, Apply(a, cs))` describes exactly `cs`.
  - The stats functions.
  - A golden file of every custom agent rendered for every kind, plus a check that
    each rendered frontmatter carries the keys the shipped files use for that kind.
- **Scratch worktree** lifecycle, with real git in a temp dir: the dirty diff is
  carried over, the tree is discarded at seal, and the binding's tree is untouched.
- **Keys → actions:** a fake `Actions` records calls, and tests assert key → confirm
  → call.
- **CI:** no test spawns a harness or reaches the network. Probe and send go through
  fakes; `cmd/relevo` tests cover parsing and text only (CLAUDE.md).
- **`make e2e`** gains one reader round whose fake harness writes `index.html` and
  `style.css` and tries to edit the tree. It asserts the files arrived as artifacts and
  the binding's tree is unchanged.

## 9. Delivery

Two tracks run in parallel on separate worktrees, then join. Each item is one plan in
`docs/plans/`, and large items take several rounds.

| # | plan | delivers | needs |
|---|---|---|---|
| A1 | Candidate names | §3.1: the `name` field, derivation, `Resolve` everywhere a token is accepted, `NameOf` on every display surface, `name` beside tokens in `--json`, the A1 migration | — |
| A2 | Agents + actors | §3.2, §3.3, §3.5: `agents`/`actors` sections replace `roles`; `internal/agentsrc`; `internal/actors`; `--actor` on bind/ask; the A2 migration; `config init` seeds three actors | A1 |
| A3 | Revisions (drafts dropped, see §6.4) | §6.4 without the TUI: `config_revision`, `config_draft`, `internal/draft`, one commit path, `config log` / `config rollback` | A2 |
| A4 | State rename | §3.7: the binding `actor` + round `candidate`; `--candidate`; artifact dirs for writers (`report.md`, `summary.md`); `wait` output, MCP tools, the plugin and `architect.*.md` "Handing off" in the new words; the A4 migration | A2 |
| A5 | Reader rounds | §3.4: any actor can be bound; scratch worktrees; reader artifact dirs; the size cap; the e2e reader round | A4 |
| B1 | Shell | §4.1, §4.2: the frame, `:` command line, view stack, keymap, `?` help; `fleet`, `rounds` and round detail moved onto it, still read-only; bare `relevo` and `relevo ui :view` | — |
| B2 | Round actions | §6.2, §6.3: `ui.Actions`, the human planner (`you`, kind `human`), confirms; send, bind, stop, done, unbind, gate, retry-on, shell | B1 |
| C1 | Config views | §4.3 config rows, §4.4: `:candidates`, `:actors`, `:agents`, `:settings`, `:gates`, `:planners`, `:servers`, `:draft`, `:audit`, save and rollback; `relevo config` on a terminal | A3, B1 |
| C2 | Stats | §5: `internal/stats`, `:stats`, the new `history --stats` | A1, B1 |
| C3 | Site | relevo-site: herdr, statusline, `policy` and `unavailable` removed; the new vocabulary; TUI specimens from the golden renders; "What relevo refuses to do" updated now that the TUI acts | all above |

Order: A1 and B1 start together. Then A2 → A3 → A4 → A5 on track A while B2 and then
C2 run on track B. C1 starts once A3 and B1 are both done. C3 is last.

Relation to open issues:

- **#388:** A5 delivers "rounds that name their role". Chain orchestration stays in
  #388.
- **#202:** unblocked by A5, since a designer reader can leave HTML.
- **#186:** C2 covers `tab --by day`-style spend over time. Budget caps stay in #186.
- **#389:** the planner actor becomes an actor of an agent whose output is `plan`.
  Orchestration stays in #389.

## 10. Out of scope

- Chains (#388) and unattended planner runs (#389), beyond what the model makes
  possible.
- `relevo land` in the TUI.
- Remote (`serve ui`) actions.
- Mouse support beyond what exists today.
- Theming beyond the fixed palette.
