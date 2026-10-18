# Plan: fix #1267 -- agy at tier edit refuses every shell command in headless mode

Saved verbatim as `docs/plans/2026-10-18-agy-edit-tier-refused.md` in the last step.

**Choice: (b). Refuse tier `edit` for agy with `ErrTierUnsupported` and a hint (`use --tier yolo`, or `--tier harness` where settings.json decides), exactly as opencode does; keep tier `read` and `yolo` unchanged. The owner confirmed this choice on 2026-10-18.** Why: (a) needs two things and neither holds. First, agy has no per-invocation allow-list: `agy --help` (agy 1.4.0) lists no flag for rules, no `--settings`, no `--allowed-tools`; its only permission flags are `--mode {accept-edits,auto,plan}`, `--dangerously-skip-permissions` and `--sandbox`. The allow rules the error message names (`permissions.allow` entries such as `command(ls)`) live only in the user's global `~/.gemini/antigravity-cli/settings.json` (a real file on this machine holding `command(which)`, `command(ls)`, `command(cat)`), and relevo renders no agy settings file anywhere (no agy `settings.json` write exists in `internal/harness` or `internal/delivery`; `deliver_agy.go:54,127` only reads agy's state root). Writing rules there would edit the user's real agy settings, which the seed forbids; a workspace-level settings file is unverified in agy and not worth betting on. Second, relevo gives Claude's `edit` no Bash allow-list either (see Findings 1), so there is no "what edit means for Claude" command list to copy. A rule list invented for agy alone would make `edit` mean different things per harness.

## Findings (checked against the code and the installed binary)

1. **Claude at edit:** `PermissionArgs` renders `--permission-mode acceptEdits` only (`internal/harness/tier.go:70-71`). `Launch` appends nothing else for claude except two `--allowedTools=Read(//...)` read grants for the round prompt and chain dir (`internal/harness/harness.go:345-349` adds the placeholders, `harness.go:401-408` fills them; `--allowedTools` appears nowhere else in non-test code). There is no `Bash(...)` rule anywhere in relevo. The permission-tier design says `edit` = "edit files without asking; commands and anything else still ask" (`docs/plans/2026-09-19-permission-tiers.md:49`). So headless Claude at edit has file edits plus whatever acceptEdits itself auto-approves (a small set of filesystem commands per Claude's documentation, not re-verified here), and has no relevo-supplied command allow-list. Related, out of scope: that means a Claude builder at edit may also be unable to run `git`/`go test`; this plan does not change it, and the MasterMind may want a separate look. Note `README.md:~2116` words edit as "standard workspace modification commands permitted"; leave that sentence alone.
2. **agy per-invocation allow-list:** none (see the choice paragraph). `PermissionFlags()` for agy already lists `--mode`, `--dangerously-skip-permissions`, `--sandbox` (`tier.go:~135-140`), so `extra_args` cannot smuggle a permission flag under a relevo tier.
3. **Existing refusal pattern to copy:** opencode read/edit cells (`tier.go:88-92`) return `fmt.Errorf("%w: opencode cannot honour tier edit: ... use --tier harness (...) or --tier yolo (--auto)", ErrTierUnsupported)`.
4. **Where the refusal surfaces, with no new plumbing:** every launch renders `PermissionArgs` through `Launch` (`harness.go:346`) or `ResumeBuild` (`resume.go:56`), so:
   - bind/add/fork/chain-start: `resolveBuilder` (`internal/relevo/bind.go:~815`) calls `spawn.HeadlessLaunch` with the tier before anything is started; `Bind`, `Add` (rolls the new worktree back; see `add_test.go:~725` for the opencode precedent) and chain start (`chain_start.go:~352`) all go through it.
   - send/resend/switch of a binding whose stored or overridden tier is edit: `startRound` returns the error, and `send.go:631-634` removes the staged plan and returns it unchanged, with no spawn-failure gate recorded (`send.go:665` comment).
   - CLI: `cmd/relevo/args.go:326` maps `harness.ErrTierUnsupported` to `codeTierCap`; the message carries the hint. No change.
   - config: a candidate or policy `tier: edit` is only parsed (`harness.ParseTier`), not checked against the kind at `config set` time; it is refused at the next bind/send, same as opencode today. No config-validation change. Doctor has no per-candidate tier check (`internal/doctor` never calls `PermissionArgs`); no doctor change.
   - probe/availability: `probeTier` (`availability/probe.go:58-69`) tries read first, so agy still probes at read. Unchanged.
5. **Reader consequence (owner must accept):** `readerTier` (`internal/relevo/tier.go:71-89`) floors a reader at edit and, when the harness refuses edit, falls to yolo. So an agy reader will bind at `yolo` and, under the default `max_tier` edit, needs `--allow-yolo` (`ErrTierAboveMax` otherwise), exactly as an opencode reader does today (`reader_round_test.go:244-275`). Today an agy reader binds at edit but cannot run `git diff`, so it is already crippled; after the change it is either usable at yolo or refused loudly. No code change in `readerTier`; the new test pins it.
6. **Stored state:** existing agy bindings whose stored `Tier` is `edit` start failing on their next `send` with the refusal (`send --tier yolo` overrides per round, `send.go:579`). Candidates configured `tier: edit` for agy fail at bind. The error text is the migration path; say so in the report.
7. **Dead code to trim:** `PromptCopyWanted` (`tier.go:173-175`) is true for agy at read and edit; at edit it becomes unreachable. Make it read only (agy read still runs `--mode plan` under the same sandbox that needs the copy) and fix its comment.

## Behaviour

1. `Harness{Kind:"agy"}.PermissionArgs(TierEdit)` returns `nil` and an error wrapping `ErrTierUnsupported` whose text names agy, tier edit, why (headless `--mode accept-edits` auto-denies shell commands and relevo cannot grant allow-rules without editing the user's agy settings), and the way out: `--tier yolo` (`--dangerously-skip-permissions`) or `--tier harness` (agy's settings.json decides). The text must contain `use --tier yolo`.
2. agy read, harness and yolo are unchanged (`--mode plan`, nothing, `--dangerously-skip-permissions`).
3. Because `Launch`, `ResumeBuild`, `HeadlessLaunch`, `readerTier` and every other caller already go through `PermissionArgs`, all of bind, add, fork, chain start, send and resume refuse agy edit with that error; nothing is started, no process, no kept worktree, no recorded gate.
4. A reader on agy lands at yolo (needs `--allow-yolo` or `max_tier` yolo), by the existing `readerTier` rule.
5. `PromptCopyWanted("agy", TierEdit)` is false; read stays true.

## Seams (files, functions, lines)

- `internal/harness/tier.go`: agy case of `PermissionArgs` (lines 80-86): replace the `TierEdit` cell with a refusal in the opencode style; `PromptCopyWanted` (168-175): drop `TierEdit`, adjust its doc comment (why only: agy at read runs under `--mode plan` without access to the binding directory outside `--add-dir`).
- `internal/harness/tier_test.go`: the table at 95-120 (agy edit row at 104), `TestLaunchRefusesExtraArgsPermissionFlag` (~233-240, uses agy at `TierEdit`), `TestPromptCopyWanted` (~365-395).
- `internal/relevo/promptcopy_test.go`: agy cases at edit become read: lines 51, 232, 299 (`b.Tier = "edit"`), 366 (`Tier: "edit"` on the bind) and 412 (`SendOptions{Tier: "edit"}` override, becomes `"read"`), plus the comments at 46, 227, 358, 376. Read the file around each to see what the assertions expect; the claude cases (189) stay at edit.
- `internal/relevo/bind_test.go`: new test next to `TestBindOpencodeCandidateTierReadRefused` (1805-1830).
- `internal/relevo/tier_test.go`: new pure test for `readerTier` with kind `agy`.
- `README.md`: permission-tier table row for agy (~2124), the opencode paragraph at ~2128 (add agy as also refusing edit), and the agy prompt-copy sentence (~2132, now "at read").
- `docs/plans/2026-10-18-agy-edit-tier-refused.md`: this plan.

Declared scope for `git diff --stat`: exactly those six code/doc files plus the plan. Nothing in `cmd/relevo`, `internal/doctor`, `internal/config` or `internal/spawn`. Historical plans under `docs/plans/` that mention `accept-edits` are left alone.

## Steps (one commit on a fresh branch off origin/main; new commits only, no amend/rebase)

1. `internal/harness/tier.go`: refuse agy edit; trim `PromptCopyWanted`. Done when `go build ./internal/harness` passes.
2. `internal/harness/tier_test.go`: agy edit row now `{"agy", TierEdit, nil, ErrTierUnsupported, "use --tier yolo"}`; `TestLaunchRefusesExtraArgsPermissionFlag` switches its agy case to `TierRead` (the extra-args refusal still must fire there, since `--dangerously-skip-permissions` is an agy permission flag); `TestPromptCopyWanted` agy/edit becomes false. Run `go test ./internal/harness -count=1`; it is green.
3. `internal/relevo/promptcopy_test.go`: move the agy cases to read as listed above. Run `go test ./internal/relevo -run 'PromptCopy|StartRound|SendPreflight' -count=1`.
4. `internal/relevo/bind_test.go`: add a test that `Bind` of `testAgyRef` with `Tier: "edit"` returns `harness.ErrTierUnsupported`, starts no process and leaves no binding (same assertions as the opencode test beside it). Do not run any subcommand in `cmd/relevo`; this test is in `internal/relevo` on the fake runner, so no harness is spawned.
5. `internal/relevo/tier_test.go`: add a pure test that `readerTier(harness.TierHarness, "agy", pol)` returns yolo and `readerTier(harness.TierEdit, "agy", pol)` returns yolo, while the same call for `"claude"` returns edit.
6. `README.md`: update the three spots. Then write `docs/plans/2026-10-18-agy-edit-tier-refused.md` from this plan.
7. Focused loop until green: `go test ./internal/harness ./internal/relevo ./internal/availability -count=1` (fix every error before the next run); then once `gofmt -l .` and `make check-affected` (on zen it needs `BASE=origin/main`). Commit the code and the plan together.

## Regression tests and mutations

- `internal/harness/tier_test.go` agy/edit row (`TestPermissionArgs`-style table): mutation, restore `return []string{"--mode", "accept-edits"}, nil` in the agy `TierEdit` cell; the row fails (wanted error, got args).
- New `internal/relevo/bind_test.go` agy-edit test: same mutation fails it (Bind succeeds and spawns nothing but returns no error), proving the refusal reaches bind and not just the table.
- `TestPromptCopyWanted` agy/edit false: mutation, restore `|| tier == TierEdit`; the row fails.
- New `readerTier` agy test: mutation, restore the agy edit cell; `readerTier` returns edit, so the yolo assertion fails.
- Last two: confirm each fails by name before restoring.

## Not done / do not do

- No agy settings.json writing, no allow-list rendering, no new flag. Nothing deleted beyond the one agy edit cell and the `|| tier == TierEdit` term.
- Do not touch Claude's edit rendering, `readerTier` logic, docs for other kinds, or doctor.
- Do not run a real `agy` or any harness in a test or in CI.

## Report must include

The choice made (b) and that it awaits owner confirmation; `git diff --stat` against the declared scope; the focused test output and the `make check-affected` result; for each mutation, the test name that failed; the migration note (existing agy bindings stored at edit and candidates configured `tier: edit` now refuse with the hint; agy readers need `--allow-yolo`); and the side finding that relevo gives Claude's edit no Bash allow-list either, for the MasterMind to raise separately.
