# A muse session as a relevo MasterMind

A muse session becomes a MasterMind through relevo's existing Claude Code
plugin. Muse loads Claude-compatible plugin bundles (`manifest_family:
claude-compatible`), so the same bundle, the same marketplace and the same
hooks, MCP server, commands and skill serve both harnesses. relevo learns a
`muse` MasterMind kind; it adds no second plugin.

## What muse provides (measured on Muse Code 1.4.11)

- `muse plugins validate claude-plugin` accepts the bundle unchanged: 7
  commands, the `SessionStart` and `UserPromptSubmit` hooks, the `relevo` MCP
  server and the `planner-loop` skill.
- Install: `muse plugins marketplace add relevo
  https://github.com/fuad-daoud/relevo.git`, `muse plugins install
  relevo@relevo`, then `muse plugins approve relevo`. Hooks and MCP servers
  stay inactive until approved; commands and skills are active at once.
  Muse caches a plugin by content hash; `muse plugins update relevo`
  refreshes it.
- Hook stdin is Claude's shape: `session_id`, `cwd`, `source`,
  `hook_event_name` (plus `model`, `permission_mode`, `turn_id`,
  `transcript_path: null`). `session_id` equals `MUSE_SESSION_ID`.
- Hook environment: `MUSE_SESSION_ID`, `MUSE_THREAD_ID`, `MUSE_PLUGIN_ID`,
  `MUSE_PLUGIN_ROOT`, `CLAUDE_PLUGIN_ROOT`, `CLAUDE_PLUGIN_DATA`. There is no
  `CLAUDECODE`, no `CLAUDE_PID` and no `CLAUDE_ENV_FILE`. The hook's parent
  process is `muse`.
- A hook's `hookSpecificOutput.additionalContext` reaches the model, for both
  events.
- The MCP server's environment carries `MUSE_SESSION_ID`; its parent is
  `muse`. Every shell command carries `MUSE_SESSION_ID` (and
  `CLAUDE_CODE_TOOL_USE_ID`, never `CLAUDECODE`); the shell's parent is `muse`.
- Muse's default sandbox blocks unix sockets, so a shell `relevo` cannot dial
  the owner socket. The MCP server runs outside that sandbox: its tools reach
  the daemon in a sandboxed session.

## Decisions

1. **Kind.** `muse` is a MasterMind harness kind. `Detect` returns
   `{Kind: "muse", SessionID: $MUSE_SESSION_ID}` when `MUSE_SESSION_ID` is a
   valid UUID and `CLAUDECODE` is not `1` (Claude still wins when both are
   set, as today). A muse record's session id must be a UUID (Validate),
   like opencode's rule.
2. **Host pid.** A muse record's host is the `muse` process: the hook's and the
   MCP server's parent, and the shell's parent's parent when a command runs
   under `bash -c`. The pure `Detect` keeps taking the pid it is given; the
   caller finds the nearest ancestor whose command name is `muse` (a few
   levels at most, from `/proc`) and falls back to the parent pid. A heal never
   re-points a muse record at a short-lived shell.
3. **Hook.** `relevo mastermind init --hook muse` and `relevo mastermind notice
   --hook muse` parse the same payload as `--hook claude` and register kind
   `muse`. There is no env file, so the answer is the no-env wording: the
   session resolves through `MUSE_SESSION_ID`, not `RELEVO_MASTERMIND`. Consent
   (ask, enable, disable, reset, repo consent) works exactly as for Claude.
4. **Plugin scripts.** `mastermind-init.sh` and `mastermind-notice.sh` pass
   `--hook muse` when `MUSE_PLUGIN_ID` is set and `--hook claude` otherwise.
   Every other script is unchanged; their verbs resolve the session through
   `Detect`.
5. **MCP.** `relevo mcp` started by muse resolves its MasterMind through
   `Detect` and serves the same tools in tools mode; no push and no channel.
6. **Sandbox.** v1 needs muse's shell unsandboxed for the CLI verbs that are
   not tools (`bind`, `chain`, `config`): run muse with `--yolo` or
   `--disable-sandbox`. When the owner socket cannot be dialled because of a
   permission error (a harness sandbox), the CLI says so in its own error code
   and next hint instead of `internal` and `relevo bugreport`. A muse
   permission profile that allows the socket is a follow-up.
7. **Doctor and docs.** `relevo doctor` reports, for muse: the plugin
   installed, enabled and approved (hooks and MCP), its version against
   relevo's, and whether this muse session resolves to a MasterMind. The
   README gains a "Muse" subsection beside "The Claude Code plugin" with the
   install, approve and update lines.

## Out of scope

Report push into a muse session (`muse session-message`), a muse permission
profile for the socket, and a muse entry in `relevo setup`.
