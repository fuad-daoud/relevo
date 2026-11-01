# Candidates

A candidate is one way to run an actor, named by the token `harness/provider/model`. `harness` and `provider` are single segments; `model` is the rest, so `opencode/openrouter/z-ai/glm-5.3-flash` is one token. **relevo ships no candidates**: which model you are entitled to run is a fact about your accounts, not about relevo.

Candidates are the `candidates` section of relevo.db, a JSON array you read
and write with `relevo config`: show it with `relevo config get candidates`, set
the whole array with `relevo config set candidates '[…]'`, or edit the document
with `relevo config edit`. The array is:

```json
[
  {
    "harness":  "claude",
    "provider": "anthropic",
    "model":    "sonnet"
  },
  {
    "harness":  "opencode",
    "provider": "openrouter",
    "model":    "z-ai/glm-5.3-flash",
    "extra_args": ["--auto"]
  },
  {
    "harness":    "agy",
    "provider":   "google",
    "model":      "gemini-3.8-flash-high",
    "extra_args": ["--dangerously-skip-permissions"]
  }
]
```

- `harness` — a kind relevo knows: `agy`, `claude`, `opencode`, `codex`, `muse`. Required.
- `provider` — who enforces the quota; free text. Required.
- `model` — passed to the harness as-is. Required.
- `tree` — `binding` (the default) or `none`.
- `extra_args` — appended verbatim after what relevo renders.
- `tier` — default permission tier for this candidate: `harness`, `read`, `edit`, `yolo`. Optional; defaults to the actor's tier, else `harness`.
- `denial_patterns` — regexes that replace the harness default denial patterns for this candidate when detecting permission-blocked exits. Optional.
- `limit_patterns` — extra regexes, appended to the harness defaults, for the text this candidate's provider prints when it closes a session on quota. Extend-only; a default that misfires is a bug to report.
- `dialog_patterns` — extra regexes appended to the harness defaults, matched against a runner's output to detect a blocking dialog; a match refuses `send` as blocked; extend-only.

A section that does not validate is refused with a message naming the entry; an absent section is zero candidates.

| actor | shape | agent |
| --- | --- | --- |
| `builder` | writer | `plan-executor` |
| `librarian` | writer | `librarian` |
| `reviewer` | reader | `reviewer` |
| `researcher` | reader | `researcher` |

An actor is relevo's name for a job; its agent is the harness definition `relevo config agents` installs.

### How relevo launches one

| kind | args |
| --- | --- |
| `agy` | `--model <model> --agent <agent>` |
| `claude` | `--model <model> --agent <agent>` |
| `opencode` | `--agent <agent> -m <provider>/<model>` |
| `codex` | `-p <agent> -m <id> -c model_provider=<provider> [-c model_reasoning_effort=<effort>]` |
| `muse` | `exec --json --model <model> [--reasoning-effort <effort>] -c run.developer_prompt=<agent definition>` |

For `codex` the candidate's `model` is `<id>[:<effort>]`: `gpt-5.6-terra:high` runs `-m gpt-5.6-terra -c model_reasoning_effort=high`, and the suffix stays in the token so two efforts are two candidates.

For `claude` the `model` may end in `:low|medium|high|xhigh|max`: `opus:medium` runs `--model opus --effort medium`, and any other suffix stays part of the model id, so a Bedrock id such as `...-v1:0` passes through whole. For `opencode` the effort is a variant, `model#<variant>`, passed as is to `opencode run -m`: variants are per model, and opencode refuses an unknown one.

Under `workspace-write`, codex also cannot write to Go's default build cache (`~/.cache/go-build`), so a Go plan fails at `go build` unless the plan sets `GOCACHE` inside the worktree or `/tmp`, or your `~/.codex/config.toml` lists it under `sandbox_workspace_write.writable_roots`. relevo adds only its own binding's `out/` directory (`<binding>/out/`, where the report, done marker and artifacts live).

Any `extra_args` are appended verbatim after what relevo renders. Because relevo renders the argv, the token in `relevo status` is exactly what was started.

> **Note on `--dangerously-skip-permissions`:** it lets the builder act without approval prompts, which makes an unattended relevo loop work, but it is a real grant of trust. It is an `extra_args` entry you add once you have watched a few rounds and trust the loop with that tree; relevo never adds it.

### Choosing a candidate

Pass a candidate's name or token to `relevo bind --no-feature --candidate
claude/anthropic/sonnet` and relevo starts exactly that, gated or not (with a
`note:` on stderr if it is). It must still serve the actor: a candidate missing
from the actor's `candidates` list is refused, so add it there first, e.g.
`relevo config set actors.builder.candidates '["sonnet", "glm-5.3-flash"]'`.

With `--candidate` omitted, relevo decides, by one rule:

- exactly one configured candidate serves the actor → that one, unless it
  is gated;
- several serve it and the actor's `candidates` list orders them (see
  [Actors and agents](#actors-and-agents)) → the first in that list that is not
  gated or `off`, then any serving candidate the list does not name, in token
  order;
- several serve it and the actor lists none → relevo refuses and says so.
  Name one, or add it to the actor.

When every candidate serving the actor is gated or `off`, relevo refuses and
says why each one is; an explicit `--candidate` still bypasses that. The same
rule applies to `relevo bind --worktree`.

Every choice is written down. `bind` and `send` print one
line saying what was picked and why, and the same line lands in the
binding's log as a `pick` entry, so `relevo show --log` shows it later:

```
picked claude/anthropic/sonnet for builder: order #2; skipped agy/google/gemini-3.8-flash-high (rate-limited until 20:28)
```

`relevo config` prints the actors, the pick per actor and the candidates.

### Account pools

The `accounts` section names the harness logins relevo may run as: one entry
per login, with the quota groups its candidates use, so several logins for one
provider cover for each other — the pick fails over to the first login no gate
covers, or spreads fresh rounds across the pool. Read it with
`relevo config get accounts` and write it with
`relevo config set accounts '[…]'`.

### Policy

The `policy` section of relevo.db holds the tuning knobs shared by every
binding and the permission ceiling. Read it with `relevo config get policy`
and change it with `relevo config set policy.<key> <json>` or `relevo config
edit`:

```json
{
  "max_tier": "edit",
  "max_switches": 2,
  "limit_gate_default_ms": 3600000,
  "stall_after_ms": 900000,
  "silent_after_ms": 480000,
  "progress_interval_ms": 30000,
  "explore_after_ms": 1200000,
  "stale_after_ms": 14400000,
  "scan_patterns": ["(?i)<instruction-tag"],
  "classify": { "provider": "jev", "model": "jev-latest", "injection_threshold": 0.7, "timeout_ms": 4000 },
  "gate": { "default": "make check", "timeout_ms": 600000, "regate": 0 }
}
```

Who runs what is not here: an actor's `candidates` list is its order and its
`tier` is its permission tier (see [Actors and agents](#actors-and-agents)).
`max_tier` bounds permission autonomy across all commands (`read`, `edit`, `yolo`),
defaulting to `edit`; commands requesting a tier above `max_tier` require
`--allow-yolo` or raising `max_tier`. `max_switches` bounds
how many times the daemon may replace a builder mid-round before the
binding goes `NEEDS YOU`; absent defaults to 2, `0` turns switching off.
`limit_gate_default_ms` is how long a rate limit relevo detects itself
gates the provider when the matched line names no reset time; absent
defaults to one hour. `stall_after_ms` is how long a live headless
builder's stream may go without an event before `relevo status` and
`relevo ui` label it `stalled`; absent defaults to fifteen minutes, and
must be `> 0` when present. `silent_after_ms` is how long a running
opencode builder's model may go quiet before the round is aborted and
resumed; absent defaults to eight minutes, and must be `> 0` when
present. `progress_interval_ms` is how often the
daemon samples a binding's progress signals while its round is open;
absent defaults to thirty seconds, and must be `> 0` when present.
`explore_after_ms` is how long a builder's output or screen may keep
moving while its tree has not before relevo labels it `exploring`; absent
defaults to twenty minutes, and must be `> 0` when present.
`stale_after_ms` is how long a `NEEDS YOU` binding may sit
unacted before relevo labels it `stale`; absent defaults to four hours,
and must be `> 0` when present. `scan_patterns` is an optional list of extra
regular expressions appended to relevo's built-in instruction-shaped scan list;
each pattern must compile. `gate` configures the default acceptance command
(see [Gate](rounds.md#gate)): `default` is the acceptance command a binding
falls back to, absent or `""` meaning no gate;
`timeout_ms` bounds one gate run, absent defaulting to ten minutes, and must
be `> 0` when present. `regate` caps the repair-round loop for a new
binding (see [Repair
rounds](rounds.md#repair-rounds)), absent or `0` meaning none, and must be
`>= 0` when present.

`classify` configures an optional classifier (TypeSafe's Jev model) to run
beside the regex scan. When absent, relevo scans with regexes only. The block
requires `"provider": "jev"`; `model` defaults to `"jev-latest"`,
`injection_threshold` defaults to `0.7`, and `timeout_ms` defaults to `4000`.
The API key is read from the `TYPESAFE_API_KEY` environment variable or from
the `typesafe` secret (`relevo config secret set typesafe` reads the value from
stdin). The key is stripped from every builder's environment so that agents
running arbitrary plans never inherit relevo's own secrets. The secret exists
because the daemon runs as a systemd user unit that inherits no login
environment (`systemctl --user set-environment TYPESAFE_API_KEY=...` also
works). `relevo doctor` reports which key source was found or warns if neither
is set. In `relevo log`, an entry like
`flagged=3 by=both p=0.94` records the de-duplicated union of regex-hit lines
and classifier paragraphs at or above the threshold, which judge flagged the
content (`regex`, `jev`, or `both`), and the maximum probability seen across
all paragraphs. The 0.7 threshold is provisional pending `make jev`. Model
output is never altered and delivery is never held: a high probability flags the
entry for the MasterMind to see, but never halts delivery.

`relevo config` shows what relevo would do right now:

```
builder  (config actors)
  1  gemini-3.8-flash-high    order     rate-limited until 20:28
  2  sonnet                   order     <- would pick
  3  glm-5.3-flash            order     limited 2x around 14:00 (30d)
reviewer  (config actors)
  1  opus                     sole      <- would pick
```

The marker is computed by the same code `bind` runs, so it cannot
disagree with what `bind` does next. Change an actor's order by editing its
`candidates` list (`relevo config set actors.builder.candidates '["…"]'` or
`relevo config edit`).

### Actors and agents

An **agent** is what does the work: a named agent definition. An **actor** is
who runs it -- the agent plus an ordered list of candidates, a tier, and, for a
writer, whether its round closes on a gate.

- A **shipped** agent is one relevo renders and installs: `plan-executor`,
  `librarian`, `reviewer`, `researcher` and `architect`. It needs no `agents`
  entry -- an actor names it directly.
- A **custom** agent is one you write, carried in the `agents` section as its
  `source` text (an `agentsrc` definition; its `name` must equal its key).
- A **native** agent points at a harness definition you already have: it names
  the `agent` and `requires` per kind, plus its `shape`. Nothing is rendered or
  installed for it.

| shipped agent | shape | output | requires |
| --- | --- | --- | --- |
| `plan-executor` | writer | `report` | `researcher` |
| `librarian` | writer | `report` | -- |
| `designer` | writer | `report` | -- |
| `reviewer` | reader | `findings` | -- |
| `researcher` | reader | `notes` | -- |
| `architect` | reader | `plan` | -- |

`librarian` is a writer whose definition states the docs-only contract: it
edits markdown, agent instruction files, sketches and diagrams, and code
comments, and never anything that changes behaviour. That contract is a
**scope**, and relevo enforces it when a round closes.

`designer` is a writer that produces design drafts: static HTML/CSS mockups
under `.design/<topic>/<draft>/` for a human to choose from. It needs the
external `design-runbooks` skill installed for the harness that runs it, and
`relevo config init` does not add a designer actor, because a seeded actor
would hand every new user one they cannot serve. Add it with
`relevo config edit`, scoped to the drafts and the two contract files:

```json
"designer": {"agent": "designer", "candidates": ["sonnet"], "scope": {"paths": [".design/**", "PRODUCT.md", "DESIGN.md"]}}
```

A repo whose `.gitignore` ignores `.design/` hides its drafts from the scope
check, so a round's drafts are neither judged nor committed.

`actors.<name>.scope` is `{"paths": [...], "comments": bool}`. Each `paths`
entry is a glob over the repo-relative path -- `*` matches within one segment,
`**` matches any number of segments, a leading `!` excludes, and the last match
wins -- or `@docs`, which expands to `**/*.md`, `**/*.markdown`, `**/*.mdx`,
`**/*.mmd`, `**/*.rst`, `**/*.adoc`, `**/*.asciidoc`, `**/*.svg` and
`**/*.excalidraw`, excluding `!**/testdata/**`,
`!vendor/**` and `!**/node_modules/**`. With `comments` true, an edit to an
out-of-scope source file is still in scope when the old and new contents differ
only in non-directive comments. For `.go` the two sides are compared as token
streams, so whitespace is free as `gofmt` moves it, and a change to a directive
comment (`//go:`, `// +build`, `//line`, `//export`, `//nolint`, `//lint:`,
anything containing `#nosec`, the `// Code generated ... DO NOT EDIT.` marker,
a test `Output:` comment) or to any code token is refused. The other languages
the comment table carries get the code-line rule: every comment is removed and
the remaining lines must match one for one, in order, so a comment's own line,
comment text and blank lines are free while everything else -- including
leading whitespace -- is code; a docstring or any other string literal is code,
and indentation is significant. Those languages are `.py`/`.pyi`,
`.sh`/`.bash`/`.zsh`, `.yaml`/`.yml`, `.toml`, `.r`, `.js`/`.jsx`/`.mjs`/`.cjs`
and `.ts`/`.tsx`, `.java`/`.kt`/`.kts`/`.scala`, `.cs`, `.swift`, `.dart`,
`.c`/`.h`/`.cc`/`.cpp`/`.hpp`, `.rs`, `.sql`, `.lua` and `.hs`; their own
directives are refused too -- `# noqa`, `# type:`, `#!`, `// @ts-ignore`,
`// eslint-disable-next-line`, `// NOLINT`, `-- noqa`, `-- name:`,
`---@type`, `{-# LANGUAGE #-}` and their kin. A file added or deleted, a
status the rules cannot judge, an extension outside the table (`.json`,
`.txt`, `.rb`, `.php`, `.pl`, a binary, an extensionless `Makefile` or
`Dockerfile`) or a symlink is refused as before.

A refusal still closes the round -- its report, diff and usage are recorded --
but its outcome is forced to `halted`, the binding goes to **NEEDS YOU** naming
the offending file, and the gate never runs. A round relevo cannot judge (no
baseline tree, or a git error) refuses the same way. A writer with no `scope`
closes exactly as before.

`relevo config init` seeds `librarian` with
`{"paths": ["@docs"], "comments": true}`. There is no migration: an existing DB
adds the scope with `relevo config edit`. The scope is actor config, so a served
binding is judged by the server's own `actors` section, the same rule that
applies to its shape and definitions.

Agents and actors are the `agents` and `actors` sections of relevo.db; read
them with `relevo config get agents` and `relevo config get actors`, and change
them with `relevo config edit`. Both blocks below are copied from a real
`relevo config export`:

```json
"agents": {
  "my-exec": {
    "native": {
      "claude": {
        "agent": "my-exec",
        "requires": [
          "my-scout"
        ]
      }
    },
    "shape": "writer"
  }
}
```

```json
"actors": {
  "builder": {
    "agent": "plan-executor",
    "candidates": [
      "sonnet",
      {
        "candidate": "glm-5.3-flash",
        "off": true
      }
    ],
    "tier": "yolo"
  }
}
```

Agent entries:

- `source` -- the agent definition's text. relevo renders and installs it.
  Exactly one of `source` and `native` is set.
- `native` -- kind to `{"agent": "...", "requires": ["..."]}`, for a
  definition the harness already has. The agent and requires names follow the
  same shape as an agent key (`^[a-z0-9][a-z0-9._-]{0,63}$`).
- `shape` -- `writer` or `reader`. Required with `native`; forbidden with
  `source`, which carries its own.

Actor entries:

- `agent` -- required. A shipped agent's name, a key in `agents`, or a native
  harness definition's name.
- `candidates` -- the actor's candidate tokens, most preferred first. A bare
  string is **on**; `{"candidate": "...", "off": true}` keeps the entry in its
  place but the pick skips it unless you name it explicitly. relevo prints
  `off` in the pick block and a note if it runs an off candidate you named.
- `tier` -- the actor's permission tier.
- `placement` -- where the actor's rounds run, most preferred first: `local`
  names this machine, and every other entry must name a `servers` section entry,
  checked when the config loads. Absent or empty means `["local"]`. `bind`
  probes the list in order and `--server`/`--local` override it.
- `check` -- writers only: whether a round closes on a gate; defaults to true.
  An actor whose agent is a reader must not set it.
- `outputs` -- the outcomes the actor's rounds report, `{"<name>": "artifact" |
  "count" | {"one-of": [...]}}`. A shipped agent has them built in; an actor
  over a custom agent declares none unless you set them, and a chain step that
  branches on an outcome the actor does not declare is refused at `chain
  --dry-run`.
- `skills` -- the skills the actor's harness must be able to load, names in the
  agent-name shape, no repeats. The shipped `designer` agent defaults to
  `["design-runbooks"]`; any actor may declare its own, and an actor with none
  needs none.

### Skills

A skill is `<name>/SKILL.md` under a harness kind's skill directories, project
first, then user:

| Kind | Looked at, in order |
| --- | --- |
| `claude` | `<repo>/.claude/skills`, `~/.claude/skills` |
| `opencode`, `agy` | `<repo>/.agents/skills`, `~/.agents/skills` |
| `codex` | `<repo>/.agents/skills`, `~/.agents/skills`, `~/.codex/skills` |
| `muse` | `<repo>/.agents/skills`, `<repo>/.claude/skills`, `~/.agents/skills`, `~/.claude/skills` |

Any other kind cannot load skills. A skill at the project path wins over the
user path. For every actor with skills and every harness kind among its
candidates (off ones included), `relevo doctor` prints one row per skill: OK
with the path found, or FAIL `skill design-runbooks for designer on claude not
found: looked at <paths>` with the fix to install it there. Doctor takes
`<repo>` from its working directory. Accounts with their own per-process home
(`CLAUDE_CONFIG_DIR`, `CODEX_HOME`) are checked against the default home only.

The pick applies the same check. A candidate whose harness cannot see a
required skill is skipped, and the pick line says why; when every candidate is
skipped the pick fails with that reason, and a candidate named with
`--candidate` is refused. A bind that cuts a worktree runs in a fresh tree, so
a project-local skill there must be committed at the base: an untracked one is
reported as such (`commit it or install it per user`). A bind on a server is
not judged here; the server's own pick judges its own disk. A chain start does not skip, because its members' picks are
made before the chain's tree exists; a designer member whose picked candidate
cannot see the skill is refused when the members are built.

An actor named `builder`, `librarian`, `reviewer` or `researcher` keeps that builtin's
shape: `builder` and `librarian` must run a writer agent, `reviewer` and `researcher` a reader.
An unknown agent, a reader with `check`, or a builtin actor with the wrong
shape is refused when the config loads.

A custom agent can stand in for `reviewer` in a chain by declaring the reviewer
contract on its actor -- `relevo config set actors.morphic-reviewer.outputs '{"verdict":{"one-of":["pass","changes"]},"findings":"artifact"}'`
-- and by ending its output with a fenced `relevo` block holding `verdict: pass`
or `verdict: changes`, which relevo's round prompt already tells it to write;
then `--reviewer-actor morphic-reviewer`. Outcomes are declared on actors, never
on agents; `outcomes` or `role` on an agent is refused.

- A new **reader** actor runs as a binding's actor too, as [Reader actors](readers.md#reader-actors) describes; a planner actor as [Planner actors](readers.md#planner-actors) describes. A new **writer** actor runs a round the same way. Every round of that binding runs it.

With `relevo bind --worktree --no-feature --server S --actor <r>`, the server resolves `<r>`
against **its own** actors section, and your local sections do not travel. A
server too old to run custom actors refuses the add.

**Custom definitions.** A definition is *custom* when its name -- or a name it
requires -- is not one relevo ships. relevo never installs or refreshes a custom
definition; each harness looks for it in its own place:

- claude: `~/.claude/agents/<n>.md`
- opencode: `~/.config/opencode/agents/<n>.md`
- agy: `~/.gemini/config/agents/<n>.md`
- codex: `~/.codex/<n>.config.toml`

A missing custom definition gates its candidates for that actor only, and
`relevo doctor` lists it.

**Bring your own agent.** Everything relevo's round protocol needs travels in
the prompt relevo sends: the working tree and its `git status` check, the prompt
path, the report path, the done marker and the closing `relevo` block. A custom
definition only shapes behaviour; `requires` names the definitions your agent
dispatches to (the shipped builder requires `researcher`).

**Migrating.** A config with no actors section still reads its legacy `roles`
section, or the candidates' `roles`/`tier` and the policy's `order`/`tier`, and
keeps working. The first load after the update migrates it in one pass: it
writes `agents` (when an actor needs a custom or native definition) and `actors`,
drops the legacy fields, and records one revision whose source is `migration`.
`relevo config log` lists that revision and `relevo config rollback <rev>`
restores the pre-migration document. Once actors exist the legacy keys are
ignored, and relevo warns about any that remain.

**Seeing it.** `relevo config` shows the actors block, then the pick per actor
labelled `(config actors)`, then the candidates; `relevo status --json` has
`agent_definition` for a custom runner. `relevo status` shows `actor <r>` on
the runner line, and `status --json` has `actor`.

### Availability

relevo keeps a ledger of when a candidate could not be used: spawn failures it
observed itself, rate limits you report. It shows the ledger, and an omitted
`--candidate` skips what the ledger gates (see [Choosing a
candidate](#choosing-a-candidate)). It is a table in the database, not a file.

Report a limit with:

```
relevo gate claude/anthropic/sonnet --reason "5-hour window"
relevo gate claude/anthropic/sonnet --for 2h
relevo gate --clear anthropic
```

A limit gates the **provider** (every candidate with `provider: anthropic`),
because that is who enforces the quota, not the model. Without `--for` it
stays gated until you run `relevo gate --clear`, because relevo does not know
your provider's reset schedule.

`relevo gate --clear` also clears the gate on every server your bindings name
and prints each server's answer; on a box running `relevo serve`, use `relevo
gate --serve --clear`.

Spawn failures need no command: relevo records one itself when starting an
agent fails, gating that one candidate for ten minutes, and it expires on
its own.

Where it shows: `relevo status` gains a `candidates` block only while
something is gated; `relevo config` marks gated rows `unavailable:`;
`relevo doctor` warns per gated candidate with the command that clears it.
`bind`/`send` with an explicit token print a `note:` on
stderr when the candidate is gated and **proceed** -- you named it. With
the token omitted they skip gated candidates and refuse when nothing
ungated serves the actor.

A candidate whose harness agent files are missing on disk is gated the same
way (`roles missing` in `relevo config` and `relevo
doctor`), fixed with `relevo config agents --kind <kind>` -- except an
explicit `--candidate` pick of it is **refused**, not allowed to proceed,
because it cannot succeed. `relevo serve` logs each configured harness
kind's agent coverage once at startup.

#### Mid-round switching

A builder relevo spawned can be replaced by the daemon while a round is
open, in two cases:

- its process exits without writing a report;
- you gate its provider with `relevo gate` -- which is how you
  tell relevo a running builder hit its limit. The command names the
  bindings the daemon will switch.

The daemon resolves `builder` again through the builder actor's candidate list
and the ledger (an omitted token, so the actor's order applies even to a builder
you named), starts the pick in the **same** tree, and hands it the **same**
round's prompt. The round number does not change; the round clock
restarts. The new builder inherits whatever the old one left in the
tree. A `switch` entry in the log says what was tried and why:

```
switched builder (rate-limited: 5h window): picked opencode/openrouter/z-ai/glm-5.3-flash for builder: order #3; skipped claude/anthropic/sonnet (rate-limited until cleared)
```

`relevo status` shows `switched 1x` on the builder line. After
`max_switches` replacements in one round (default 2; set it in
the policy section, `0` turns switching off), or when nothing ungated
serves `builder`, the binding goes `NEEDS YOU` with the reason, and
recovers on its own once `relevo gate --clear` clears a provider. A
failed replacement spawn counts as a switch and the daemon walks to
the next candidate. The halt always states its reason, even on a round
that has already notified once; a `relevo send` re-send resets the
round's switch budget, since the human asked for another attempt.

A builder gone between rounds is `BROKEN`: `relevo bind --resume`
starts a fresh one. relevo selects an agent for every builder it starts, with
`--agent` on the launch line.

`aliases.json` is not read.

#### History

Every gate relevo records -- a limit you report, a spawn failure it hit
-- is also kept for 30 days in the database's gate history, by
provider and local hour. `relevo config` shows it twice: a `limited 3x
around 21:00 (30d)` note on a candidate whose provider was limited
within an hour of now, and a `history` block with a 24-hour row per
provider. It changes nothing about which candidate is picked; it is the
cue to write a different order, or to `relevo gate <provider>` a provider
before it bites.

## Permission tiers

relevo defines four permission tiers that control how autonomously agents may use tools and make changes:

- `harness` — relevo passes no permission flags to the harness; the harness CLI defaults or configuration files decide (relevo default). Outside the tier order.
- `read` — read-only inspection and planning; write operations are denied.
- `edit` — file editing and standard workspace modification commands permitted.
- `yolo` — full autonomy; interactive permission prompts and confirmation dialogs bypassed.

The flags rendered for each harness kind (verified 2026-09-19 on claude 2.1.278, agy 1.2.7, opencode 2.0.8, codex 0.155.1):

| kind | harness | read | edit | yolo |
|---|---|---|---|---|
| claude | (none) | `--permission-mode plan --allowedTools=Read(//<round prompt>) --allowedTools=Read(//<chain dir>/**)` | reader: `--permission-mode acceptEdits --allowedTools=Read(//<round prompt>) --allowedTools=Read(//<chain dir>/**)`; builder: refuse | `--dangerously-skip-permissions` |
| agy | (none) | `--mode plan` | refuse | `--dangerously-skip-permissions` |
| opencode | (none) | refuse | refuse | `--auto` |
| muse | (none) | refuse | refuse | `--yolo` |
| codex | (none) | refuse | `-s workspace-write -c sandbox_workspace_write.writable_roots=["<binding>/out"]` | `--dangerously-bypass-approvals-and-sandbox` |

muse takes only `yolo` and `harness`: Muse's own sandbox blocks unix sockets and relevo's state directory, so `read` and `edit` cannot work. Its candidates look like `muse/meta/muse-spark-1.3:xhigh`, the effort after `:`. The Muse Code subscription works only inside the `muse` CLI, which is why muse is a harness and not a provider. Setting `META_API_KEY` switches billing to pay-as-you-go. `muse login` uses a device code, so it works over SSH. `muse exec` has no agent flag, so relevo hands it each role definition as `-c run.developer_prompt=...`.

opencode does not support `read` or `edit` tiers because it has no read-only or edit-only CLI flag. Choosing `read` or `edit` for an opencode candidate is refused immediately with an error directing you to use `--tier harness` (where `opencode.jsonc` decides) or `--tier yolo` (`--auto`).

A claude builder does not support the `edit` tier: headless `acceptEdits` approves file edits and read-only commands such as `git status`, `git log` and `git diff`, but denies `go`, `make` and `git commit`, so a builder could change files without running the gate or committing them. Binding or sending a claude builder (`plan-executor`, `librarian` or any writer actor) at `edit` is refused immediately with an error directing you to `--tier yolo` (`--dangerously-skip-permissions`) or `--tier harness` (where claude's `settings.json` decides). A claude reader keeps `edit`: it only reads the tree and writes its own output file.

agy does not support the `edit` tier: headless `--mode accept-edits` auto-denies every shell command, and relevo cannot grant allow-rules without editing your agy settings. Choosing `edit` for an agy candidate is refused immediately with an error directing you to `--tier yolo` (`--dangerously-skip-permissions`) or `--tier harness` (where agy's `settings.json` decides). A reader on agy therefore binds at `yolo` and needs `--allow-yolo` under the default `max_tier`.

For a chain member, the claude `read` and `edit` cells gain a second `--allowedTools=Read(//<chain dir>/**)` rule, where `<chain dir>` is the chain's directory under `~/.local/state/relevo/.chains/<chain>/`. It is what lets a chain member read the plan copy, the task and the staged `inputs/...` (including `inputs/repo/...`) files a chain seed names. It is a read grant only — it grants no Edit — and it is omitted entirely for a binding in no chain.

codex does not support the `read` tier: `-s read-only` cannot write the report, marker, question and findings files relevo stages under `~/.local/state/relevo/<binding>/out/`, and codex ignores `writable_roots` under read-only. Choosing `read` for a codex candidate is refused with an error directing you to `--tier edit` or `--tier harness`. At `edit` relevo adds the binding's `out/` directory as a writable root; that is the only path outside the worktree the sandbox lets the builder write.

For agy at `read`, relevo stages a copy of the round prompt under `<binding>/out/` (`.NNN-prompt.md`) and points the launch prompt to it, since agy's sandbox restricts reads to the worktree and `out/`.

### Ceiling semantics and ordering

Tiers are ordered as `read < edit < yolo`. `harness` is outside this hierarchy and is never compared as above or below other tiers.

`max_tier` in the policy section defines the permission ceiling across all commands, defaulting to `edit`. Any command requesting a tier above `max_tier` (such as `yolo` under default policy) is refused unless:
- The command includes `--allow-yolo` on the command line (e.g. `relevo bind --no-feature --tier yolo --allow-yolo` or `relevo send --tier yolo --allow-yolo`), or
- `max_tier` is explicitly raised to `"yolo"` in the policy section.

`max_tier` cannot be set to `"harness"` because `"harness"` is outside the rank order and does not represent a ceiling.

### Resolution chain

When starting an agent, relevo resolves the permission tier through a precedence chain:
1. Explicit CLI flag: `--tier <tier>` passed to `bind` or `send`.
2. Candidate configuration: `"tier"` set on the candidate in the candidates section.
3. Actor configuration: `"tier"` set on the actor in the actors section.
4. Fallback default: `harness`.

The result is then capped by the policy's `max_tier`.

For the verify reviewer, tier resolves from the policy section's `tier.reviewer` when set, else the candidate's `tier`, else `harness` -- and then capped by `max_tier`. The reviewer accepts no `--tier` flag, and a reviewer requesting `yolo` requires `max_tier: "yolo"` in the policy section since `send --verify` has no `--allow-yolo` flag.

### Per-round tiers

Every builder runs a new process for each round, so a round may temporarily override the tier with `relevo send --tier <tier> [--allow-yolo]`. The override applies to that round only, and resets to the binding's default tier when the round completes.

`relevo send --candidate <token>` moves the binding to another configured candidate from this round on. It is refused while a live round is open (stop it first with `relevo stop`) -- except on a served binding halted in `NEEDS YOU`, where `stop` is itself refused and naming the candidate re-points that round instead. An explicit pick of a gated candidate is recorded and proceeds, as with `relevo bind --worktree --candidate`. The binding's tier is re-derived for the new candidate. On a remote binding the server must advertise the `builder` feature.

### Permission-blocked exits

When a headless builder exits without producing a report file, relevo inspects the tail of its stdout/stderr log against the harness's default denial patterns (or the candidate's `denial_patterns` if configured).

If a permission denial pattern matches (for example, if a tool was refused because the agent attempted an edit while in `read` mode, or executed a command without required approvals):
- The binding halts and transitions to `NEEDS YOU`.
- The halt message quotes the matching denial line and instructs the MasterMind to re-send with a higher tier or adjust harness allow lists.
- The ledger records an exit entry with note suffix `; permission-blocked: <line>`.
- relevo does **not** switch to another candidate (which would waste quota on a configuration error) and does **not** record a rate-limit gate.

To recover from a permission-blocked halt, adjust the tier (e.g. `relevo send --tier edit` or `relevo send --tier yolo --allow-yolo`) or update permissions, then re-send the plan.
