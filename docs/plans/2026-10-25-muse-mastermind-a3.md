# Slice A3 of #1380: muse hooks see a scrubbed environment (correction round)

Rounds 1 and 2 (A1 `bb82c0e9`, A2 `8b06cd4f`) are on this branch. An end-to-end
run in a sandbox found the defect this round fixes. Read CLAUDE.md's code-style
rules again before editing: functions at most 70 lines, comments say why, no
history in code, test names say what they pin, and no `cmd/relevo` test may spawn
a harness or reach the network.

## The defect (measured on Muse Code 1.4.11)

Muse runs plugin hooks and plugin MCP servers with an allowlisted environment:
only `HOME`, `LANG`, `PATH`, `TERM`, `USER`, `PWD`, `SHLVL`, `_`, `TRACEPARENT`,
plus its own `MUSE_SESSION_ID`, `MUSE_THREAD_ID`, `MUSE_PLUGIN_ID`,
`MUSE_PLUGIN_ROOT`, `MUSE_PLUGIN_DATA_DIR`, `CLAUDE_PLUGIN_ROOT`,
`CLAUDE_PLUGIN_DATA`, `PLUGIN_ROOT`, `PLUGIN_DATA`. Everything else is dropped,
including `RELEVO_RUNNER`, `XDG_STATE_HOME` and `XDG_CONFIG_HOME`. (Muse's shell
tool gets the full environment; only hooks and MCP servers are scrubbed.)

Consequences:

1. A muse builder relevo starts carries `RELEVO_RUNNER` in the muse process's own
   environment, but its SessionStart hook does not see it, so
   `mastermind.IsRunner(os.Getenv)` is false and the runner registers itself as
   a muse MasterMind. Observed: a sandbox builder round started at 20:32:18 and
   a second muse record appeared at 20:32:19.
2. A user with a non-default `XDG_STATE_HOME` or `XDG_CONFIG_HOME` gets hooks
   and an MCP server that open the default state root, a different database
   from the one their shell uses.

The hook's parent and the MCP server's parent are the muse process itself, and
its environment is readable at `/proc/<pid>/environ` (NUL-separated
`KEY=value` entries) by the same user.

## Behaviour

1. **Runner detection through the host.** When the calling process's env has
   `MUSE_PLUGIN_ID` set (it runs as a muse hook or plugin MCP server) and
   `RELEVO_RUNNER` is empty, relevo reads the muse host's environment (the
   nearest muse ancestor, through the existing `MuseHost` seam, falling back
   to the parent pid) and treats the process as a runner when that
   environment carries a non-empty `RELEVO_RUNNER`. A missing or unreadable
   environ (another OS, a gone process) is not a runner, exactly as today.
   Every place that calls `mastermind.IsRunner(os.Getenv)` today
   (`cmd/relevo/mastermind_hook.go` init and notice, and
   `cmd/relevo/mastermind_consent.go:452`) uses the new check. Keep
   `mastermind.IsRunner` pure; add the host-aware variant beside it with the
   environ reader injected, and wire the `/proc` reader in `cmd/relevo`
   beside `museCommOf`/`musePPidOf` in `wire.go`.
2. **XDG roots from the host.** At the start of `main` (before any state or
   config path is resolved), when `MUSE_PLUGIN_ID` is set, and only for
   `XDG_STATE_HOME` and `XDG_CONFIG_HOME` that are unset in this process,
   copy their values from the muse host's environment when it has them. Put
   the decision in a pure function (inputs: this env, the host env getter;
   output: the variables to set) with its own test; `main` only applies it.
   Never override a variable that is set; never touch anything else.
3. **Session-scoped consent for muse is pinned.** A session-only "yes"
   (`relevo mastermind enable` without `--repo`, recorded for kind `muse` and
   the session id) must make `init --hook muse` register. Add a test where the
   repo has no answer and only the muse session has said yes; it must fail if
   `mastermindEffective` is called with `"claude"` instead of the hook kind.
4. **Error text names muse.** The refusal `not in a detectable mastermind
   session: run it from the MasterMind's own session (Claude Code with the
   relevo plugin, or opencode), or pass --kind and --session` (find it with
   grep) names muse too: `(Claude Code or muse with the relevo plugin, or
   opencode)`. Update any test or golden that pins the old sentence.

## Tests (names say what they pin)

- `TestIsRunnerHostReadsMuseHostEnviron` (internal/mastermind): `MUSE_PLUGIN_ID`
  set, own env without `RELEVO_RUNNER`, host environ with it: runner. Host
  environ without it: not a runner. Reader error: not a runner. Own env
  with `RELEVO_RUNNER`: runner without reading the host. No `MUSE_PLUGIN_ID`:
  host never read.
- `TestParseEnviron` (wherever the NUL parser lives): `KEY=value\x00` entries,
  a value containing `=`, an empty file, a trailing entry without NUL.
- `TestMuseHookXDGFromHost`: unset `XDG_STATE_HOME` is taken from the host;
  a set one is kept; no `MUSE_PLUGIN_ID` changes nothing; other variables
  never copied.
- `TestMasterMindInitHookMuseSilentWhenHostIsRunner` (cmd/relevo): with the
  injected environ reader reporting `RELEVO_RUNNER`, `init --hook muse`
  answers `{}` and registers nothing. Use an injection point that a test can
  set (a package variable the production wiring assigns is fine if that is
  the pattern nearby; follow what `wire.go` does).
- `TestMasterMindInitHookMuseHonoursSessionConsent` (behaviour 3).

Every test that reads the real environment sets
`t.Setenv("MUSE_SESSION_ID", "")` and `t.Setenv("MUSE_PLUGIN_ID", "")` first:
this builder runs inside muse.

## Must NOT change

- Claude and opencode behaviour, `mastermind.IsRunner`'s signature and result
  for a process whose own env has `RELEVO_RUNNER`.
- Detect, Validate, the MuseHost walk, the hook wording, the plugin scripts.
- No new lint or comment/size exclusions; no coverage baseline lowered.

## Close

Mutation-check behaviour 1 yourself (make the host-aware check ignore the host
environ) and name the test that fails. Save this plan as
`docs/plans/2026-10-25-muse-mastermind-a3.md`, `gofmt -l` the changed files,
`make check-affected`, commit, then report: the new function names, each test
and what it pins, the mutation result, and the gate result.
