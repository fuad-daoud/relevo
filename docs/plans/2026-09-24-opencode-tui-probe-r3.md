# Probe round 3: a server plugin exports the planner into tool shells (#393)

A third **spike** round on binding `oc-tui-probe`, on top of round 2's commit
`d577e7d1`. No Go code changes. Deliverable: evidence and a new findings
section.

## 1. System Overview

When the OpenCode planner agent runs `relevo bind` / `relevo send` from its
shell tool, relevo must know which planner it is. Claude Code solves this with
a SessionStart hook that exports `RELEVO_PLANNER`. OpenCode's shell exports no
session id (`planner.Detect` in `internal/planner/ident.go` recognises only
Claude and agy). The candidate fix is an OpenCode **server** plugin (it runs
inside the OpenCode server, not the TUI) whose shell-environment hook adds a
per-session variable to every shell command.

Rounds 1-2 proved the **TUI** plugin surface on 2.0.14 and found the installed
`@opencode-ai/plugin` 1.18.25 types are stale. Nobody has loaded a **server**
plugin on 2.0.14. Upstream's (possibly stale) spec says a package may export
both `./tui` and `./server`, each a separate module, and v1 server plugins had
a `"shell.env"` hook `(input: { cwd, sessionID?, callID? }, output: { env })`.
Treat all of that as a hypothesis to verify, exactly as round 1 did for the TUI.

Questions:

- **Q17 load.** Does a server plugin load on 2.0.14 from the same
  `<config>/plugins/<name>/` package (an `exports["./server"]` entry), and what
  module shape does v2 require (`{ id, server }`? `{ id, setup }`? other)? Find
  it the way round 1 found `{ id, setup }` for TUI: from the binary's strings
  and from the error text of a wrong shape.
- **Q18 hook.** Is there a hook that sets environment variables for shell
  commands, what is it called, and what does its input carry (session id? call
  id? cwd?)?
- **Q19 reach.** Which shell paths does the hook reach: the user's `!`-prefixed
  shell mode in the TUI, `api.client.session.shell` from the TUI plugin, and the
  agent's bash tool? Record each separately.
- **Q20 cost.** Can the hook run a subprocess (`relevo planner init ...` is the
  real use) and how long may it take before shell commands are visibly delayed?
- **Q21 coexistence.** Does one package with both `./tui` and `./server`
  load both halves?

## 2. File Structure

Only these paths may change:

```
docs/specs/2026-09-24-opencode-tui-probe.md                append "## Round 3: server plugin and shell env (Q17-Q21)" + "After round 3" consequences
docs/specs/probes/2026-09-24-opencode-tui/
  run.sh                                                    add stage r3 (reachable with RELEVO_PROBE_STAGE=r3)
  relevo-probe-server.ts                                     NEW: the server half
  relevo-probe.tsx                                           TUI half: add R15 (see §4) only
  <the probe package's package.json>                         add the ./server export next to ./tui
  out/r3-*                                                   round 3 evidence
```

## 3. Data Structures

**`out/r3-server-loaded.json`** -- written by the server half at load:
`{ shape_used, input_keys (Object.keys of every argument the loader passed),
hook_names_registered, env: { OPENCODE_* keys present, PATH }, pid, ts }`.

**`out/r3-hook-calls.jsonl`** -- one JSON line per hook invocation:
`{ ts, hook, input (the whole input, JSON), env_set: { name: value } }`.

**`out/r3-reach.json`** -- per shell path P1-P3 (§4):
`{ path, ran: bool, how, saw_var: bool, value: string|null, session_id_expected, error }`.

**`out/r3-cost.json`** -- `{ spawn_ms (relevo version from inside the hook),
delay_ms_per_command (median of 5 commands with the hook sleeping 0 / 200 / 1000 ms) }`.

## 4. Probe behaviour

**Server half (`relevo-probe-server.ts`).** At load, write
`r3-server-loaded.json`. Register the shell-environment hook (find its v2
name, starting from `"shell.env"`). In the hook: append to
`r3-hook-calls.jsonl`, and set `RELEVO_PROBE_SESSION` to the session id from
the hook input (or `"none"` if the input carries none) and `RELEVO_PROBE_HOOK=1`.
For Q20, when `RELEVO_PROBE_HOOK_SLEEP_MS` is set in the server's environment,
sleep that long inside the hook, and once run `relevo version` with
`Bun.spawn`/`execFile`, recording its duration.

**Shell paths**, all in a throwaway session created exactly as round 2 did
(`api.client.session.create`, removed with
`opencode session delete --standalone <id>` at cleanup):

| # | Path | How to trigger without a model |
|---|------|--------------------------------|
| P1 | TUI shell mode | In the TUI, the prompt's shell mode (`!` prefix, or the mode round 2 saw as `mode: "shell"`). Type `!env \| grep RELEVO_PROBE` + Enter. Confirm from its `src` or a capture that shell mode runs the command **without** a model turn; if it would start a model turn, do not use it -- record why. |
| P2 | `api.client.session.shell` | TUI half R15: a palette command `relevo.probe.shell` that calls `api.client.session.shell` for the throwaway session with command `env \| grep RELEVO_PROBE`. Read its `src` first; call it only if it runs a command without a model turn. |
| P3 | the agent's bash tool | **Do not trigger.** It needs a model turn. Record only whether the hook's source or the binary strings show the bash tool calling the same hook, and mark P3 `NOT TESTED (needs a model)`. |

For each path that ran, read the output from the capture or the message it
added, and fill `r3-reach.json`.

## 5. Halt conditions -- stop and report, do not improvise

- Any step would start a model turn, in any session.
- Any step would write to a session other than the throwaway one, or leave the
  throwaway session behind.
- Anything under `~/.config/opencode/` would have to change.
- The **shared OpenCode service** (`~/.config/opencode/service.json`) would load
  the probe's server plugin. Every launch stays `--standalone` with
  `OPENCODE_CONFIG_DIR` pointing at the probe directory, as in rounds 1-2.
  Verify at the end that the service's plugin list (`opencode plugin list`
  without `--standalone`, or its equivalent) does not include the probe.

Rounds 1-2 recorded contradicted premises and kept going; that remains right
for API-shape surprises. The four conditions above are hard stops.

## 6. Working Efficiently

- Read once, in one batch: the findings doc (rounds 1-2), `relevo-probe.tsx`,
  `run.sh`, the probe `package.json`, and round 2's `out/r2-notes.txt`. They
  hold the loader, isolation, throwaway-session and capture machinery; reuse
  it, do not re-derive it.
- To find the v2 server hook names, search the binary once, e.g.
  `LC_ALL=C grep -a -o -E '"shell\.env"|"tool\.execute\.[a-z]+"|"chat\.[a-z]+"|server:[^;]{0,80}' /usr/bin/opencode | sort | uniq -c`
  (plain ASCII `LC_ALL=C` patterns; the default grep here is ugrep and rejects
  long Unicode ranges).
- Iterate with `RELEVO_PROBE_STAGE=r3 bash docs/specs/probes/2026-09-24-opencode-tui/run.sh`.
- Final check, once: `make check` and `git status --short` (only §2's paths).

## 7. Ordered Steps

1. Read the §6 files. *Done when* you can name round 2's throwaway-session
   create/remove calls.
2. Find the v2 server module shape and shell-env hook name (binary strings, then
   a wrong-shape load to read the error). *Done when* the server half loads and
   writes `r3-server-loaded.json`.
3. Hook + P1 + P2 (+ R15). *Done when* `r3-hook-calls.jsonl` and `r3-reach.json`
   exist, or their absence is explained.
4. Q20 cost runs (0 / 200 / 1000 ms sleep). *Done when* `r3-cost.json` exists.
5. Append the findings section (Q17-Q21 with verdicts, evidence, exact shapes,
   verbatim errors) and `After round 3` consequences (3-6 bullets, facts only).
6. Verify (including the shared-service check in §5) and commit:
   `docs(specs): OpenCode probe round 3 -- server plugin shell env (#393)`.

## 8. Report

Verdicts Q17-Q21; the `After round 3` bullets verbatim; the exact server module
shape and hook signature; the throwaway session id and proof of removal; proof
the shared service does not list the probe; `git diff --stat HEAD~1`.
