# Slice A1: muse MasterMind core — detection and host pid

Scope: decisions 1 and 2 of the muse MasterMind design only
(`Detect` muse branch, UUID session rule, muse host pid, heal guard),
plus wiring where `Resolve`/`Detect` are called for the CLI and
`relevo mcp`. Read `CLAUDE.md` first (70-line functions, 600-line
files, comments say why, no history in code; CI has no harness binary
so no `cmd/relevo` test may spawn a harness or reach the network —
test rules as pure functions in `internal/mastermind` or
`internal/relevo`).

## Behaviour and cases

1. `Detect` returns `{Kind: "muse", SessionID: $MUSE_SESSION_ID,
   HostPID: ppid}` when `MUSE_SESSION_ID` is a valid UUID
   (lower-case 8-4-4-4-12 hex, the existing `validConversationID`
   shape). Check order is claude, then muse, then agy, then opencode:
   `CLAUDECODE=1` wins when both are set (structural, as today).
   Malformed, upper-case, truncated, or empty `MUSE_SESSION_ID` is not
   detected. Nil env is not detected. `Detect` keeps its signature and
   stays pure: no `/proc`, no shell-out.
2. `Record.Validate` rejects a `muse` record whose session id is not a
   UUID (`ErrInvalid`); a UUID session id passes. Empty session id is
   still rejected by the required-field check.
3. Muse host pid: `Detect`'s `ppid` fallback is unchanged. `Resolve`
   rewrites a muse ident's `HostPID` through an injected
   `MuseHost func(ppid int) (pid int, ok bool)` seam on `ResolveInput`:
   `ok == true` means the pid is the nearest ancestor whose command
   name is `muse` (bounded walk, a few levels); `ok == false` (or a nil
   seam) falls back to the parent pid for the host/session steps.
4. Heal (`healGone`) never re-points a `muse` record at a non-muse
   process: when the record's kind is `muse`, the heal proceeds only
   when the seam reports a verified `muse` ancestor (`ok == true`);
   otherwise the record is returned unchanged. Non-muse heals behave
   exactly as today.
5. Wiring: every CLI `Resolve`/`Detect` call site below passes the
   production seam; `relevo mcp` resolves a muse session through the
   same `Resolve` path with no `--kind` change.

## Seams (read these before touching)

- `internal/mastermind/ident.go:18-26` — env markers and UUID rule;
  `:35-63` — `Detect` (add the muse branch here).
- `internal/mastermind/mastermind.go:146-178` — `Validate`
  (`:159-161` is the opencode session rule to mirror for muse).
- `internal/mastermind/resolve.go:34-57` — `ResolveInput` (add the
  `MuseHost` seam); `:83-97` host step; `:115-127` session step.
- `internal/mastermind/heal.go:12-39` — `healGone` (add the muse
  guard).
- `internal/relevo/runtime.go:367-370` — `ProcStart` field (add the
  `MuseHost` field beside it); `internal/relevo/bind.go:59` —
  `resolveVerbMasterMindHeal` (pass it through).
- `cmd/relevo/wire.go:433` + `procStartUnix` — wire the production
  `/proc` reader here, beside `procStartUnix` (same `os.ReadFile`
  pattern as `processArgs` in `cmd/relevo/doctor_checks.go:426`).
- `cmd/relevo/mcp.go:138-153` — `resolveMCPMasterMind` (add the seam;
  no `--kind` change).
- `cmd/relevo/unbind.go:290-312` — `gcResolver` (add the seam).
- `cmd/relevo/mastermind_consent.go:73-106` — `mastermindCaller`
  (apply the seam to a muse ident's `HostPID` before registering).
- `cmd/relevo/mastermind.go:197-210` — `mastermindHostStart`
  (unchanged; still stamps the resolved host).

## Steps

1. Deliverable: `docs/specs/2026-10-25-muse-mastermind-design.md`
   written exactly as given at the end of this plan. Done when the
   file is byte-identical to the appended text.
2. Deliverable: muse branch in `Detect`
   (`internal/mastermind/ident.go`). Done when the new
   `TestDetectMuse` passes (valid UUID detects muse with `HostPID`
   ppid; `CLAUDECODE=1` wins; malformed/upper-case/empty/truncated
   ids and nil env do not detect) and all existing `Detect` tests
   pass unchanged in behaviour.
3. Deliverable: muse UUID rule in `Record.Validate`
   (`internal/mastermind/mastermind.go`). Done when
   `TestRecordValidateMuse` passes (non-UUID muse session is
   `ErrInvalid`; UUID passes) and `TestRecordValidate` still passes.
4. Deliverable: pure bounded ancestor walk, new file
   `internal/mastermind/muse_host.go` (or equivalent), taking injected
   `commOf`/`ppidOf` callbacks — no direct `/proc` reads, so tests
   need no real process. Done when `TestMuseHostWalk` passes (finds
   the nearest `muse` ancestor through shells; respects the bound;
   reports not-found when no ancestor is `muse`).
5. Deliverable: `ResolveInput.MuseHost` seam + `Resolve` applying it
   to muse idents (host and session steps use the ancestor pid, ppid
   fallback otherwise). Done when `TestResolveMuseSession` passes
   (muse env resolves to the muse record; fallback ppid resolves when
   the seam reports not-found; nil seam behaves as ppid) and
   `TestResolveOrder` still passes.
6. Deliverable: `healGone` muse guard (`internal/mastermind/heal.go`).
   Done when `TestHealRefusesMuseAtNonMuseHost` passes (muse record,
   unverified host: unchanged, zero `SetHost`) and
   `TestHealRepointsMuseAtMuseHost` passes (verified muse ancestor:
   re-pointed, exactly one `SetHost`); all existing heal tests pass.
7. Deliverable: production `/proc` reader + wiring through
   `Runtime` to `resolveVerbMasterMindHeal`
   (`internal/relevo/bind.go`), `resolveMCPMasterMind`
   (`cmd/relevo/mcp.go`), `gcResolver` (`cmd/relevo/unbind.go`), and
   `mastermindCaller` (`cmd/relevo/mastermind_consent.go`). Done when
   the package builds and the pure rule tests from steps 4–6 cover
   the logic (no `cmd/relevo` test spawns a harness; thin wiring
   only, so the coverage baseline holds).
8. Deliverable: saved plan `docs/plans/2026-10-25-muse-mastermind-a1.md`
   (this plan), `gofmt -l` clean on changed files,
   `make check-affected` green, commit, then report.

## What must NOT change

- Hook paths: `cmd/relevo/mastermind_hook.go` (`init --hook`,
  `notice --hook`), hook payload parsing, and `HookOutput` wording.
- Plugin scripts (`mastermind-init.sh`, `mastermind-notice.sh`) and
  their `--hook` values.
- Doctor: `cmd/relevo/doctor_checks.go` kind gate stays claude-only;
  no `internal/doctor` change.
- Docs/README, `relevo setup`, and the `mcp --kind` values.
- The claude/agy/opencode `Detect` branches and `Resolve`'s
  flag > env > host > session precedence.
- No new `golangci-lint` or comment/filesize exclusions; no coverage
  baseline lowering (regenerate with
  `sh scripts/check-coverage.sh --write` only if code moves between
  packages, and say so in the report).

## Report must include

- `git diff --stat` against this plan's scope (steps 2–7 files only).
- `make check-affected` result, and `gofmt -l` result on changed files.
- For each new test: its name and the one behaviour it pins.
- The mutation check: with the muse branch condition broken (e.g.
  UUID check removed), the named `Detect`/`Validate` tests fail.
- Anything in the seams list that turned out wrong, and any step left
  undone.

---
End of plan. Finish with: save this plan as
`docs/plans/2026-10-25-muse-mastermind-a1.md`, `gofmt -l` the changed
files, `make check-affected`, commit, then report.

Write docs/specs/2026-10-25-muse-mastermind-design.md exactly as given at the end of this plan.


---

## Appendix: docs/specs/2026-10-25-muse-mastermind-design.md (write this file byte for byte, from the next line to the end)

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
