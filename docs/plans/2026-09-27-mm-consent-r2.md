# MasterMind consent R2: the opencode guide and injection

Spec: `docs/specs/2026-09-27-mastermind-consent-design.md` (§6.2). Depends on
R1. This round gives an opencode session the same model-facing text a Claude
session gets: the guide when the repo answered `yes`, the ask-note when unset,
nothing when `no`.

## Goal

`relevo mastermind guide` renders the consent text for a location, and the
shipped opencode plugin pushes it into the session's system instructions
(`ctx.session.hook("context")`) and gates its registration on the answer.

## Facts (verified 2026-09-27; opencode v2.0.18)

- Plugin API: `ctx.session.hook("context", (event) => ...)` edits
  `event.system` (a `SystemPart[]`) for the agent loop; `event.kind` is
  `primary|compaction|title|generate`, `event.sessionID` names the session.
  `ctx.session.get({sessionID})` reads the session (its directory included).
  <https://opencode.ai/v2/docs/build/plugins>
- The shipped server plugin is `internal/harness/opencodeplugin/server.ts`,
  default export `{ id: "relevo-server", setup }`, installed at
  `~/.config/opencode/plugins/relevo/server.ts`.
- The TUI plugin registers every session at `tui.tsx:142`
  (`mastermind init --kind opencode --session <id>`).
- `mastermind init --kind opencode --session <id>` mints a record with
  `HostPID 0`; `mastermind.Resolve` finds it by session (`BySession`).

## Steps

1. **`relevo mastermind guide`**:
   - flags `--cwd DIR` (an override), `--kind K --session S`, `--json`;
   - with `--kind opencode --session <id>` and no `--cwd`, resolve the session's
     directory through `delivery.OpencodeSessionFinder` (a new `Directory`
     method over the existing opencode db read), then fall back to the process
     cwd;
   - resolve the repo from the cwd and read the consent; render through
     `mastermind.ConsentText`;
   - on `enabled` with `--kind opencode --session <id>`, ensure the record
     exists (`Init` with Kind/SessionID, `HostPID 0`) so the identity sentence
     names it; any other kind changes nothing;
   - plain output: the text, exit 0; `--json`:
     `{"state":"enabled|ask|disabled","text":...,"repo":...}`;
   - a missing repo reads `disabled` with an empty text.
   - Pure tests: state mapping and rendering; cmd tests under TestMain's temp
     root (no harness, no network).
2. **`server.ts`**: register `ctx.session.hook("context", ...)`, which runs for
   the agent loop only and carries no `kind`;
   - per `sessionID`, fetch once with
     `relevo mastermind guide --json --kind opencode --session <id>`
     (spawn like `tui.tsx`'s `spawnRelevo`, 10 s timeout), cache
     `{text}`/`{text:""}`; a failure caches the empty text and logs once to
     stderr;
   - on a non-empty cached text, push `{ type: "text", text }` onto
     `event.system`;
   - never throw, never await on the model path after the first fetch.
3. **`tui.tsx`**: replace the unconditional `mastermind init` with
   `relevo mastermind guide --json --kind opencode --session <id>`; on
   `state == "enabled"` store the returned id and name, on `ask`/`disabled`
   register nothing (the model delivers the ask-note).
4. **Probe** (manual, per `docs/specs/2026-09-24-opencode-tui-probe.md`):
   in a scratch repo, `yes` -> the guide is in the system text of the first
   model call; `unset` -> the ask-note; `no` -> neither; a title request never
   carries either. Record the raw evidence in the probe's `out/`.
5. **Verification**: `make check`; the Go side has the new tests; `server.ts`
   and `tui.tsx` are checked by the install-byte test
   (`internal/harness/files_test.go`) and the probe.

## Out of scope

MCP registration, `_meta.sessionID` resolution, per-harness MCP instructions,
and the TUI dialog are later rounds (spec §9).
