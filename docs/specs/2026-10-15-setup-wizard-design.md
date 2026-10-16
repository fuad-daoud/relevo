# relevo setup: an optional one-time setup wizard

## Goal

A stranger's first relevo session must work. Today a new machine runs
`relevo config init`, which seeds the owner's personal lineup (openrouter
glm/deepseek models, `gpt-5.6-terra:high`, `opus:medium`), `max_tier: yolo`
without saying so, no `reviewer`/`security`/`lite-planner` actors for the
default chain, and nothing for the plugin, the service or a first round.

`relevo setup` walks a newcomer through four things once: which models build
and plan, the permission tier, the plugin and the daemon service, and one real
round that proves the install. It is never mandatory: people who install by
hand, and agents installing relevo for their human, both use it, and anyone can
skip it and use `relevo config` directly.

## Non-goals

- opencode. v1 covers claude, codex and agy; opencode on PATH gets a note
  pointing at `relevo config`, and a follow-up adds it.
- A full-screen TUI. Prompts are plain lines that work over ssh, in CI and in
  an agent's shell.
- Changing the shipped agent files' model pins (the codex researcher's
  `gpt-5.6-luna`, the opencode agents' openrouter models).

## Shape

One question engine, two front ends.

- `internal/setup` is the engine. It keeps owning the starter configuration and
  gains three pure functions:
  - `Probe(env) Inventory`: which of claude, codex and agy are on PATH, whether
    each is logged in, and which models each can run.
  - `Questions(inv, answers) []Question`: the questions still open.
  - `Build(inv, answers) (Files, error)`: the candidates, policy and actors
    document, the same `Files` shape `Plan` returns today.

  Commands run through an injected runner (the way `harness.InstallEnv` injects
  `LookPath`), so every rule is testable with no terminal and no harness
  binary. CI has neither.
- `cmd/relevo/setup.go` is the front end and owns the side effects: it reads
  answers, asks what is still open, and applies.

### One run

1. Probe, then print a short inventory:
   `codex: logged in · claude: logged in · agy: on PATH, not logged in -> run agy once to sign in`.
2. Take answers from `--answer <id>=<value>` flags (repeatable).
3. If questions are still open:
   - On a TTY (stdin and stdout both terminals) and without `--json`: ask each
     as a numbered prompt; Enter takes the recommended pick.
   - Otherwise: print the questions document (below) to stdout and exit 3,
     relevo's existing "a human must decide" code. Nothing is written.
4. Apply the steps in order (below), printing one line per step, `ok` or the
   failure with a `next:` fix.

### Running it again

On a configured machine the current values are the recommended picks, so
Enter-Enter-Enter keeps what is there. Applying writes a new config revision;
the old one stays recoverable through the config revision history. No
`--force`.

## Probe

Each probe command has a 10-second timeout, so a hung CLI cannot stall setup.

| harness | logged in | models offered |
|---|---|---|
| claude | `claude auth status` prints JSON with `"loggedIn": true` | `sonnet`, `opus`, `haiku` (claude has no list command; the aliases always resolve) |
| codex | `codex login status` exits 0 | `codex debug models`: entries with `"visibility": "list"`, in ascending `priority` |
| agy | `agy models` exits 0 | each line's first tab-separated field, after any `Fetching ...` line |

The candidate each probed model becomes:

- claude: provider `anthropic`, model the alias.
- codex: provider `openai`, model the slug.
- agy: provider `google` (the first of the agy harness's providers).

Notes, not questions:

- a harness on PATH but not logged in: `<harness>: on PATH, not logged in ->
  <login command>` (`claude auth login`, `codex login`, and for agy, which has no login verb, `run agy once to sign in`); rerun
  setup afterwards.
- opencode on PATH: `opencode: not covered by setup yet; configure it with
  relevo config`.
- no harness logged in: setup stops before writing anything, with code
  `not_available`, a message listing each login command, and
  `next: relevo setup` once one is logged in. No harness on PATH at all keeps
  init's existing message.

## Questions

Each question has an id, a prompt, choices (value, label, one-line why), a
recommended value, and whether several values may be picked in order. A
question that cannot apply is dropped.

1. `builder` (ordered, one or more, comma-separated): who writes code. The
   order is the fallback order when one hits a usage limit. Each model is one
   choice; agy's per-effort models (`gemini-3.8-flash-low`, `-high`, ...) fold
   into one. Recommended: one model per logged-in harness at medium effort
   (a builder follows a plan whose thinking is done), in the order codex,
   agy, claude:
   - codex: its first listed model.
   - agy: its first `gemini-*-flash` model, else its first model.
   - claude: `sonnet`.
2. `planner` (one): who writes plans. Recommended at high effort: claude
   `opus` when claude is logged in, else codex's first listed model, else
   agy's first `claude-opus-*` model, else its first model.

On a terminal, each picked builder or planner model that runs at more than
one effort gets a follow-up effort question, recommending medium for a
builder and high for the planner. The efforts
are codex's reported reasoning levels, claude's `--effort` levels for sonnet
and opus, and agy's model-name suffixes. An `--answer` value carries the
effort itself: `<harness>/<provider>/<model>:<effort>`, the choice's
`efforts` listing the valid ones.
3. `tier` (one): `yolo` (recommended) or `edit`. The prompt says plainly:
   yolo lets builders run any command in their worktree without asking; at
   edit they change files but a claude builder cannot run tests or git.
4. `plugin` (yes/no): install the relevo plugin into Claude Code, the
   MasterMind side. Asked only when claude is on PATH and the plugin is not
   installed; recommended yes.
5. `service` (yes/no): run the daemon as a user service (systemd on Linux,
   launchd on macOS). Asked only when no unit is installed; recommended yes.
   Without it, relevo starts the daemon on demand.
6. `smoke` (yes/no): run one small real round now. Recommended yes.

Derived, not asked, and printed in the summary:

- `librarian`: the builder list, scope `@docs` with comments, as init writes
  today.
- `lite-planner` and `reviewer`: the cheapest picked model, preferring claude
  `sonnet`, then codex's last listed model, then agy's first `*-flash-*` model.
- `security`: the planner's candidate.

These fill the default chain's `reviewer`, `security` and `lite-planner`
actors, which init never seeded. Every one stays editable with `relevo config`.

Candidate names come from `candidate.DeriveNames`, as today. A model picked for
two actors is one candidate both actors list.

### Answer syntax

- On a TTY an ordered pick is typed as numbers, `1,3`; a yes/no takes `y`/`n`.
- As flags, a value is the choice's value: `--answer
  builder=codex/openai/gpt-6-luna:high,claude/anthropic/sonnet --answer
  tier=yolo --answer plugin=yes`. A candidate value is
  `<harness>/<provider>/<model>`.
- An answer that is not one of the choices is a usage error naming the valid
  values. An answer for a question that does not apply is ignored with a
  stderr note.

## The JSON contract

`relevo setup --json` with questions open prints, and exits 3:

```json
{
  "inventory": [
    {"harness": "codex", "on_path": true, "logged_in": true, "models": ["gpt-6-luna", "gpt-5.6-terra"]},
    {"harness": "agy", "on_path": true, "logged_in": false, "login": "run agy once to sign in"}
  ],
  "notes": ["agy: on PATH, not logged in -> run agy once to sign in"],
  "questions": [
    {
      "id": "tier",
      "prompt": "How much may a builder do without asking?",
      "multi": false,
      "recommended": ["yolo"],
      "choices": [
        {"value": "yolo", "label": "yolo", "why": "builders run tests, git and any command in their worktree"},
        {"value": "edit", "label": "edit", "why": "builders only edit files; a claude builder cannot run tests or git"}
      ]
    }
  ],
  "next": "relevo setup --answer builder=<...> --answer tier=<...>"
}
```

The guide for agents (README and the plugin's relevo guide) says: show each
question to your human with the recommended choice marked, never answer the
tier question yourself, then rerun with every answer.

With every question answered, `--json` prints the result document and exits 0
when every step passed:

```json
{
  "steps": [
    {"step": "config", "ok": true, "detail": "builder: gpt-6-luna-high, sonnet; planner: opus-medium; tier: yolo"},
    {"step": "agents", "ok": true},
    {"step": "plugin", "ok": true},
    {"step": "service", "ok": true},
    {"step": "smoke", "ok": false, "detail": "round closed without a report", "next": "relevo show setup-smoke --round 1 --transcript"}
  ]
}
```

A failed step exits 1 with that step's own error code (`internal` when it has
none); the steps before it stay applied and the document says which failed. `relevo help --json` registers
the verb, its flags, both documents and its codes.

## Apply

In order; a step that fails stops the steps after it.

1. `config`: `Build`, then one `PutDoc` writing candidates, policy and actors
   as one revision, as init does. Policy is `max_tier` from the `tier` answer;
   writer actors (builder, librarian) carry the same tier.
2. `agents`: `harness.Install` for each logged-in harness, as init does.
3. `plugin` (when answered yes): run `claude plugin marketplace add
   fuad-daoud/relevo`, then `claude plugin install relevo@relevo` (the doctor's
   existing fix command). A marketplace that is already added is not a
   failure.
4. `service` (when answered yes): what `relevo service install` does, after the
   unit fix below.
5. `smoke` (when answered yes): below.

Then `next: relevo doctor`, and for the MasterMind:
`next: open Claude Code in a repository and ask it to plan something`.

### Service unit fix

The Linux unit hardcodes `ExecStart=%h/.local/bin/relevo` and a fixed `PATH`,
so a binary installed elsewhere (go install's `~/go/bin`, a package manager's
`/usr/bin`, a tarball in `~/bin`) and harnesses installed under their own homes
(`~/.npm-global/bin`, `~/.bun/bin`, `~/.opencode/bin`) are not found by the
daemon. macOS already renders its plist from `os.Executable()`.

- The shipped `relevo.service` becomes a template like the plist: `@BIN@` in
  `ExecStart` and `@PATH@` in `Environment=PATH=`.
- Rendering substitutes `@BIN@` with `os.Executable()` and `@PATH@` with the
  current fixed list plus the directory of every harness binary `LookPath`
  finds, deduplicated, in that order.
- Reading an installed unit canonicalises it back (binary path, then the
  `Environment=PATH=` line's value) before hashing, the way `CanonicalPlist`
  does, so the stale/edited decision and the doctor's drift check keep
  working. The shipped-unit index keeps the template's hash, so a unit an
  older release wrote is still stale, not edited.
- This applies to `relevo service install` and the daemon's start-time refresh
  too, not only to setup.

## The smoke round

A real round, small enough to finish in a minute or two, that touches nothing
of the user's.

1. Make a throwaway git repository under the state directory
   (`$XDG_STATE_HOME/relevo/setup-smoke/repo`), with one committed `README.md`.
2. Bind the builder actor on it, current tree, `--name setup-smoke
   --no-feature`, the first builder candidate. The MasterMind is resolved as
   bind resolves it; when none resolves, setup registers one named `setup`.
3. Send a plan that asks the builder to create `HELLO.md` containing
   `relevo works`, commit it, and report.
4. Wait with a 10-minute budget, printing a dot line while it runs.
5. Pass when the round closed with a report and `HELLO.md` holds the line.
   Print the builder that ran it and the round's duration.
6. Clean up on pass: unbind, remove the repository and the binding's state.
   On failure keep everything, and print the `relevo show setup-smoke --round 1
   --transcript` command plus, when the round never started, `relevo doctor`.

A leftover `setup-smoke` binding from an earlier run is unbound first.

## config init

`config init` becomes `Build(Probe(env), every recommended answer)`: the same
probed lineup, no personal models, the derived actors included. It skips the
plugin, service and smoke steps, keeps `--force`, `--no-agents` and `--json`,
and prints the tier on its own line:

```
tier: yolo (builders run commands without asking) -- relevo config set policy.max_tier edit to restrict
```

`Defaults` and `PlannerDefaults` go; the builder note for a claude-only
machine goes too, since claude now builds.

## Unconfigured machine hint

When no candidates section exists, a verb that needs config prints one stderr
line, `relevo is not set up yet: run relevo setup`, and continues as today.
`--json` output stays clean, and `setup`, `config`, `help`, `version` and
`doctor` do not print it. `relevo doctor` shows the same as a row whose fix is
`relevo setup`.

## Documentation

- README: the first-run steps become install, `relevo setup`, then open Claude
  Code. `config init` moves to a "scripted install" note.
- The relevo guide (`internal/mastermind/guide.md`) gains the agent rule above,
  two lines.

## Testing

- `internal/setup`: table tests over fake runner output for the probe (logged
  in, logged out, a hung command, malformed JSON), the recommendations, the
  dropped questions, answer validation, and `Build`'s actors and tier.
- `internal/serviceunit`: render and canonicalise round-trip; a unit an older
  release wrote is still stale.
- `cmd/relevo`: the flag surface and the questions document from a fake
  inventory, with no harness executed and no network (CI has neither).
- Live: an env sandbox built from the branch, setup run both on a TTY and as
  an agent would (`--json`, then rerun with answers), smoke round included.
