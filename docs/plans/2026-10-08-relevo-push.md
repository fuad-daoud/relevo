# Plan: `relevo push` replaces the Claude channel transport (#955 Go half)

One builder round, Go only. TypeScript mod (`register.ts`, `hooks.json` modules), statusLine step-aside (#952), band/pane, and OpenCode/agy deliverer behaviour are out of scope. `docs/runbook.md` + `CLAUDE.md` bind: temp space aside, focused test command while iterating, full `make check` + `make e2e` once at the end; no `cmd/relevo` test spawns a harness or touches the network (rules as pure functions in `internal/`); no new lint/size/coverage exclusion.

## Behaviour and cases

**Part A — `relevo mcp` serves tools only.**

- No `--mode`, `--interval`, argv mode detection, `startMCPChannel`/`pollMCPChannel`, `claude/channel` capability/notification, or `InstructionsChannel`. `relevo mcp` starts in tools mode with no flags; `wait` stays a tool; `send` keeps pointing at it.
- Route and claim rename `channel` → `push` everywhere user-visible: `ErrClaimHeld` text, `ConfirmIndex` route, `DeliverPending` route-1, `mastermindRoute`/status labels (`internal/relevo/status.go`, `internal/view/status.go`), doctor rows, guide, `README.md`, `claude-plugin/README.md`, goldens/fixtures. Rows already confirmed as `channel` stay readable (route is free text; nothing filters on it).
- Claim-key premise (checked, true): the key is `claim/<mastermind-id>` (`internal/delivery/channel.go:18,21`) and the `Claim` JSON shape is unchanged, so the key does **not** change on upgrade — a live old-format claim survives; only the route label and error text change.
- Sessions keep receiving reports through MCP `wait` and `relevo wait` exactly as today when no push claim is held.

**Part B — `relevo push` holds the push claim and drains to the mod.**

- `relevo push` resolves its MasterMind like every verb (`--mastermind`, `$RELEVO_MASTERMIND`, then host/session via `mastermind.Resolve`; the mod passes `RELEVO_MASTERMIND` from `$.session.id()` at `session.start` per #951 P2 — no `CLAUDECODE` in a mod's child).
- Holds this MasterMind's push claim: write `Claim`, refresh `SeenAt` within `ClaimTTL` (10s, `internal/delivery/channel.go:13-14`); a second holder gets `ErrClaimHeld`; release (`Remove`) on exit. TTL covers a crash.
- Drain loop per entry, in order: `AdmitIndex` FIRST (hides the entry from `wait`/`show`/cockpit via the claimable scans), then one NDJSON line on stdout (`seq`, `binding`, `round`, `kind`, the text `delivery.PushText` gives, capped at `MaxPushBytes`), then `ConfirmIndex` with route `push` only on a stdin line `ack <seq>`. `stdin` EOF ends cleanly. Unacked admitted entries go back to pending as described under **MasterMind corrections** below (NOT via the deliverers' settle).
- MCP `wait` while THIS MasterMind's push claim is held: still blocks until the round closes, returns only the outcome line + "delivered by the mod", claims/confirms nothing. Non-peek MCP `show` never consumes an admitted entry (claimable scan already excludes admitted; `claimPrinted` match in `internal/relevo/show_claim.go:35-52` stays). CLI `relevo wait` contract unchanged (owner rule): `internal/relevo/wait.go` + `cmd/relevo` wait path untouched.
- `status`: a held push claim yields route `push`, live true (`mastermindRoute`), hence pushable and never "stalled pending" (`pendingStalled` returns false when `MasterMindRouteLive`, `internal/view/statusline_pending.go:32-42`).
- Guide (`internal/mastermind/guide.md`) and MCP instructions reworded for no channel; no mod instructions yet.

**False premise to record, not work around:** `scripts/check-comments.allow:77-78` names `internal/relevo/channel.go` / `channel_test.go`; no such file exists in this tree (delivery's is `internal/delivery/channel.go`). The builder drops/repairs those allow lines as stale, and says so in the report.

## Seams (file, type/function, lines)

- `internal/mcp/mode.go:1-61` — `channelFlags`, `DetectMode`, `ParentArgv`.
- `internal/mcp/server.go:22-30` `Mode`/`ModeChannel`; `:196-213` `initializeResult` (capability + instructions); `:251-258` wait gating; `:402-409` send wait-pointer; `:485-513` `Push` (`notifications/claude/channel`).
- `internal/mcp/instructions.go:5-44` `InstructionsChannel`; `:46-113` `InstructionsTools`; `:149-160` `InstructionsFor`.
- `internal/mcp/tools.go:105-132` `waitToolSpec`, `ToolsFor`.
- `internal/mcp/wait.go:216-273` `waitOnTarget`, `Wait`, `formatWaitResult`; `internal/mcp/verbs.go:18-60` `Verbs`, `RelevoVerbs.masterMindFor`.
- `cmd/relevo/mcp.go:19-47` flags; `:54-169` `cmdMCP`; `:187-230` `resolveMCPMode`/`mcpResolveMode`; `:270-354` `startMCPChannel`/`pollMCPChannel`.
- `internal/delivery/channel.go:13-66` `ClaimTTL`, `claimKey`, `Claim`, `ErrClaimHeld`, `ClaimStore`/`ClaimBulk`; `:68-221` `KVClaims`.
- `internal/delivery/drain.go:14-18` `Pusher`; `:20-83` `Drain`/`DrainState`; `:101-144` `pendingFor`/`pushPayload`; `:146-203` `pushState`/`isChannelState`/`stateEventContent`.
- `internal/delivery/deliver.go:12-27` `Delivery`; `:73-102` `DeliverPending` route 1; `:104-172` `ConfirmAdmitted`; `:237-289` `admitExpired`/`expireAdmit`/`deliveryOf`; `internal/delivery/deliverer.go:31-67` `MasterMindDeliverer` (+ `deliver_opencode.go:109-230`, `deliver_agy.go:97-100,257+` as the settle pattern).
- `internal/delivery/pull.go:39-127` `PullMatching`/`pullMatching` (claimable scan); `:149-185` `PullPendingThroughEntries`; `internal/delivery/push.go:21-69,94-112` `showCommand`/`LogRef`/`PushText`/`MaxPushBytes`.
- `internal/store/log.go:154-159` `AdmittedAt`; `:307-388` `PendingFor`/`Admit`/`ClearAdmit`/`ConfirmIndex`/`ClaimableForMasterMind(Through)`; `internal/store/admit.go:17-54,55-171` claimable scan + admit/clear.
- `internal/relevo/status.go:29-51` `mastermindRoute`, `:241-242` row wiring; `internal/view/status.go:76-91` route fields; `internal/view/statusline_pending.go:29-55` `pendingStalled`.
- `internal/relevo/show.go:104-153` `Show` + non-peek claim; `internal/relevo/show_claim.go:35-52` `claimPrinted`; `internal/relevo/wait.go:20-69` `WaitResult`/codes; `internal/relevo/reconcile.go:218-225` settle call.
- `cmd/relevo/main.go:36-103` usage, `:214-274` dispatch; `cmd/relevo/registry.go:137+` + `cmd/relevo/testdata/contract/help-json.golden` (help JSON); `internal/doctor/mastermind.go:53-74,145-177` input + session check; `cmd/relevo/doctor_checks.go:286-375` `mastermindCheckInput`.
- `internal/mastermind/resolve.go:58+,127+` `Resolve`/env step; `internal/mastermind/guide.md` full.
- `internal/e2e/headless_test.go:85-309` scenario; `:698-786` `startMCP`/`startChannel`; `:899-921` `waitNote`; `:1127-1134` `claimExists`.
- `README.md:440,2592-2612`, `claude-plugin/README.md`, `scripts/check-coverage.sh`, `testdata/coverage-baseline.txt`.

## Ordered steps (each: deliverable + how to know it worked)

1. Strip the channel transport: delete mode detection/flags/poll/capability/`Push`/instructions branch; deliverable is tools-only `relevo mcp` with `wait` always listed; know it worked when `go test ./internal/mcp/...` passes and no `claude/channel` string remains outside history/tests being rewritten.
2. Rename route+claim `channel`→`push` (error text, `DeliverPending` route-1, `mastermindRoute`, status/doctor/guide/READMEs/goldens/fixtures, old `channel` rows still render); deliverable is a tree-wide rename with readable history; know it worked when `go test ./internal/relevo/... ./internal/delivery/... ./internal/doctor/...` passes and `status_route_test`-style assertions read `push`.
3. Add the push loop in `internal/delivery` (new file, e.g. `push.go`): `RunPush(ctx, deps, mastermind, in io.Reader, out io.Writer) error` owns claim hold/refresh/release, stale-admit clearing on start, the drain (admit -> NDJSON line -> confirm route `push` on `ack <seq>`), unacked-admit clearing on exit and stdin EOF. `cmd/relevo/push.go` is wiring only (flags, MasterMind resolve, `RunPush(ctx, deps, rec, os.Stdin, os.Stdout)`). Test the loop in `internal/delivery` over `io.Pipe`; no `cmd/relevo` test spawns a harness or reaches the network. Know it worked when the new `internal/delivery` tests pass.
4. Gate MCP `wait`/`show` on a live push claim for this MasterMind (outcome-line-only + "delivered by the mod", claims nothing; admitted entries never claimable); CLI `relevo wait` untouched; know it worked when `go test ./internal/mcp/...` passes including the wait-vs-drain race test.
5. Reword guide + MCP instructions for no channel. The instructions are fixed at `initialize` and do NOT vary with the claim (owner chose option 2 over option 1); add one sentence to the tools-mode instructions: a `wait` result that says "delivered by the mod" means the report is already in the transcript, so act on it there. No other mod instructions. Update `status`/doctor wording. Know it worked when `go test ./internal/mastermind/... ./internal/mcp/... ./internal/doctor/...` passes.
6. Rewrite the e2e channel half (`internal/e2e/headless_test.go`: `startChannel`/`waitNote`/route-`channel` assertions) to `relevo push` over a pipe pair (read line, assert report text, write `ack <seq>`, assert one confirm route `push`; keep the tools half); know it worked when `go test ./internal/e2e/ -run TestHeadlessE2E -count=1` passes.
7. Add/adjust unit tests: wait-vs-drain race never returns an admitted entry; kill between write and ack leaves the entry pending (next `wait`/holder redelivers after lapse); second `push` gets `ErrClaimHeld`; old `channel`-route rows render; know it worked when the focused package suites pass.
8. Run one mutation check per protocol rule (admit-before-write; confirm-on-ack-only) naming the test that must fail; know it worked when each mutation flips its named test red and the revert is green.
9. Regenerate `help --json` golden (`go test ./cmd/relevo -run TestHelpJSONDocumentsTheSurface -update`), and if coverage moved between packages regenerate with `sh scripts/check-coverage.sh --write` and say so; know it worked when `make check-static` and the golden diff show only the intended surface.
10. Save this plan verbatim to `docs/plans/2026-10-08-relevo-push.md` and commit code + tests + plan together as new commit(s); know it worked when `make check` exits 0, `make e2e` passes, `git status --porcelain` is clean, and `git show --stat HEAD` lists exactly the declared scope plus the plan.

Focused command while iterating: `go test` on the package being changed (e.g. `go test ./internal/mcp/...`); final full commands: `make check` then `make e2e`. Expected diff scope: `cmd/relevo/mcp.go`, `cmd/relevo/main.go` (usage), `cmd/relevo/push.go` (new, thin), `cmd/relevo/registry.go` + help-json golden, `internal/mcp/` (mode/server/tools/instructions/wait/verbs + tests), `internal/delivery/` (channel/deliver/drain/pull + push-verb rules + tests), `internal/relevo/status.go` + tests, `internal/store/` only if the rename touches it (prefer not), `internal/doctor/mastermind.go` + `cmd/relevo/doctor_checks.go` + tests, `internal/mastermind/guide.md`, `internal/e2e/headless_test.go`, `README.md`, `claude-plugin/README.md`, `docs/plans/2026-10-08-relevo-push.md`, plus `testdata/coverage-baseline.txt` only if regenerated via `--write`.

## What is deleted (closed list)

1. `internal/mcp/mode.go` channel detection: `channelFlags`, `DetectMode`, `ParentArgv` (and `mode_test.go`).
2. `mcp.ModeChannel` and every mode branch: `ToolsFor` channel arm, `initializeResult` `experimental.claude/channel` capability, `handleToolsCall` wait refusal in non-tools mode, `callTool` send wait-pointer channel exception.
3. `Server.Push` channel notification (`notifications/claude/channel`) and its `Pusher` implementation.
4. `InstructionsChannel`; `InstructionsFor` channel arm.
5. `cmd/relevo/mcp.go`: `--mode`/`--interval` flags + `minMCPInterval`, `resolveMCPMode`/`mcpResolveMode`/`mcpResolveKind` channel arm, `startMCPChannel`/`pollMCPChannel`, channel claim write/remove in `cmdMCP`.
6. The `"channel"` route string and `"mastermind already has a live channel"` text (replaced by `"push"`; history rows untouched).
7. The e2e channel-half helpers/assertions (`startChannel` poll loop, `waitNote` kind-`report` channel notification, route-`channel` confirm, "channel already took the report" pull assertions) replaced by the push-pipe equivalents.
8. Stale `scripts/check-comments.allow` lines for the nonexistent `internal/relevo/channel.go` / `channel_test.go`.
9. No amend/rebase of any commit already on a remote binding's branch; all work lands as new commits.

## Report must include

- `git diff --stat` against the seam list above; every file outside it named with why.
- Each new/changed test by name with its focused-command result; the e2e result.
- For each mutation check: what was broken, the named test that failed, revert confirmation.
- Coverage-baseline arithmetic: regenerated with `--write` or untouched, and why.
- Confirmation that no `cmd/relevo` test spawns a harness or reaches the network, no commit was amended/rebased/force-pushed, and no lint/comment/filesize exclusion was added.
- Upgrade note: claim key unchanged (`claim/<id>`), so a live pre-upgrade claim survives; pre-upgrade `channel`-route confirm rows render as history.
- Anything said here that the tree proved false.


## MasterMind corrections (review of this plan; these override anything above)

1. **No settle reuse.** `ConfirmAdmitted`/`admitExpired`/`expireAdmit` look up
   `d.Deliverers[kind]`; only `agy` and `opencode` have deliverers
   (`cmd/relevo/wire.go` `newDeliverers`), so for a Claude MasterMind they
   return "no deliverer" and an entry `relevo push` admitted would stay
   admitted -- unclaimable -- forever. Instead add ONE helper in
   `internal/delivery` (e.g. `clearOrphanAdmit`) with this rule: an entry
   whose MasterMind kind has no deliverer, that is admitted, while that
   MasterMind's push claim is NOT live, gets `ClearAdmitIndex` (claimable for
   `wait` again). Call it at the top of `DeliverPending` and of
   `ConfirmAdmitted` (the done/paused path). While the claim IS live, leave
   the entry for the holder.
2. **Holder start and exit.** `RunPush`, right after taking the claim, clears
   admits on this MasterMind's entries left by a dead holder (it now owns the
   claim, so any admitted entry of a no-deliverer MasterMind is orphaned) and
   re-sends them. On clean exit (stdin EOF, ctx done) it clears the admits it
   wrote but never saw acked, then releases the claim. Unit tests: crash
   (exit without clear) + claim lapse -> next `DeliverPending` makes the entry
   claimable; restart of `RunPush` -> the unacked entry is sent again with a
   new line.
3. **MCP `wait` under a live claim** returns when the awaited round's entry is
   admitted or confirmed (the round closed), with the outcome line + "delivered
   by the mod", and never admits, claims or confirms. Pin it with a test where
   `RunPush` delivers and acks while an MCP `wait` is blocked: `wait` returns
   the outcome line only, and the log shows exactly one confirm, route `push`.
4. **Mutation checks** must include: remove the `clearOrphanAdmit` call from
   `DeliverPending` -> the crash test fails.
5. `scripts/check-comments.allow` stale lines: verify they name files that do
   not exist before removing; removing a stale allow line is fine, adding one
   is not.
