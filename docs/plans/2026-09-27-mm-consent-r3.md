# MasterMind consent R3: the opencode MCP tools

Spec: `docs/specs/2026-09-27-mastermind-consent-design.md` (§6.2, §9). Depends
on R2. This round gives an opencode MasterMind the `status` / `send` / `done`
tools, with instructions that describe how opencode actually receives reports.

## Goal

`relevo mcp --kind opencode` serves the three tools inside opencode; the
plugin registers the server only for a location the repo answered `yes`; and
each tool call resolves its MasterMind from the `_meta.sessionID` opencode
sends, so one server serves several sessions without a restart.

## Facts

- `internal/mcp/server.go` decodes `tools/call` params into `toolCallParams`
  (`name`, `arguments`) and never reads `_meta`. `Server.Mode` picks the
  instructions; `callTool` appends the tools-mode wait command to a `send`
  result.
- `mcp.Verbs` (`internal/mcp/verbs.go`) is `Status/Send/Done`, resolved against
  one `MasterMind` id at construction. `RelevoVerbs.MasterMind` filters
  `status`; `send`/`done` act by binding name and need no identity.
- Reports reach an opencode MasterMind through the opencode deliverer
  (`internal/relevo/deliver_opencode.go`), so there is no background wait to
  start, unlike Claude's tools mode.
- opencode sends the calling session in `CallToolRequest.params._meta.sessionID`
  over stdio (<https://opencode.ai/v2/docs/mcp-servers#context>).
- `ctx.mcp.transform(editor => editor.set(name, config))` registers a local MCP
  server from a plugin; the config's local shape is
  `{type, command, cwd?, environment?, disabled?, codemode?}`.

## Steps

1. **`internal/mcp`**: `toolCallParams` gains `_meta.sessionID`; `Verbs` methods
   gain a `session string`; `RelevoVerbs` gains
   `ResolveSession func(session string) (string, error)`, used by `Status` when
   `!a.All`; a resolver error is the tool result, so the model can act on it.
   `Server` gains `Kind string`; `InstructionsFor(mode, kind)` returns the
   opencode prelude for `kind == "opencode"`, and `callTool` appends the wait
   command only when the kind is not opencode.
2. **`InstructionsOpencode`** (`internal/mcp/instructions.go`): reports arrive
   as new turns; nothing to start after a send; run the check before `done`;
   the three tools; everything else through the shell. The shared guide is
   appended as for the other modes.
3. **`cmd/relevo/mcp.go`**: a `--kind` flag (`""` = today's Claude behaviour,
   `opencode` = per-call resolution). With opencode: mode is tools (a
   `--mode channel` is refused), the startup MasterMind wait is skipped, and
   `ResolveSession` maps `_meta.sessionID` to `rt.MasterMinds.BySession("opencode", id)`,
   with a not-found message that says how to answer the consent question.
4. **`server.ts`**: at setup, read the location's answer with
   `relevo mastermind guide --json --cwd <location>`; when it is `enabled`,
   register the server through `ctx.mcp.transform` with
   `{type: "local", command: ["relevo","mcp","--kind","opencode"], codemode: false}`.
   Nothing is registered for `ask` or `disabled`; a failed read registers
   nothing.
5. **Tests**: a `tools/call` carrying `_meta.sessionID` reaches the verbs with
   it; the opencode initialize instructions; no wait command on an opencode
   `send`; the resolver's not-found error; the pure kind/mode choice in
   `cmd/relevo`.
6. **Probe** (manual): in an enabled repo, an opencode session lists
   `relevo_status` / `relevo_send` / `relevo_done` and answers the opencode
   instructions; in an unset or `no` repo, no relevo tools exist.

## Out of scope

The TUI consent dialog. Removing a registered MCP server mid-life (a consent
change applies at the next plugin load, like every transform).

## Verification

`make check`; the TS plugin is checked by install bytes and the probe.
