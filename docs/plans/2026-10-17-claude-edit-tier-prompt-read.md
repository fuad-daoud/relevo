# Plan: fix #1253 -- a Claude runner at tier read/edit cannot read its own round prompt

Saved verbatim as `docs/plans/2026-10-17-claude-edit-tier-prompt-read.md` in the last step.

## Findings (checked against the code, not only the seed)

- Every round's handoff text (`builderPrompt`, `readerPrompt`, `chainReaderPrompt` in `internal/relevo/send.go` 30-125) is a pointer: it says `Read: <PromptPath>`. The prompt body is never passed inline, and cannot be (one argv element, Linux caps it near 128 KiB; builder plans and chain seeds exceed that). So "pass the text inline and drop the Read instruction" is not a cleaner mechanism. Rejected.
- The claude print form (`internal/harness/harness.go` Launch, ~line 305) passes `--add-dir <OutputDirPlaceholder>`, rendered as `Store.OutDir(name)` = `<state>/<binding>/out`. The prompt is `<state>/<binding>/NNN-prompt.md` (or the legacy `NNN-plan.md`, both resolved by `Store.PromptPath`, `internal/store/paths.go:167`), one directory up. Report and done marker are under `out/` already, so only the prompt read is blocked. The same gap hits builders (`builderPrompt`) at edit on a Claude candidate, not only readers.
- Claude Code permission rules (context7, code.claude.com/docs/en/permissions): Read and Edit rules use gitignore patterns; `//path` is "absolute path from filesystem root" (`Read(//Users/alice/secrets/**)` matches `/Users/alice/secrets/**`); a single leading `/` is project-relative, so it is NOT an absolute path. Rule for abs path `/home/u/s/b/001-prompt.md` is therefore `Read(//home/u/s/b/001-prompt.md)`, i.e. `"Read(/" + absPath + ")"`.
- `--allowedTools` is variadic in the claude CLI: `--allowedTools X <next>` could swallow a following positional or extra arg. Render it as ONE argv element in `=` form: `--allowedTools=Read(//abs/path)`. `PermissionFlags()` (`internal/harness/tier.go:105`) already matches `--allowedTools=` as an extra_args permission flag, so a candidate's `extra_args` cannot collide silently.
- `Harness.PermissionArgs` is also used by `Harness.Resume` (`internal/harness/resume.go:56`, consult/ask resume with an inline prompt, no round prompt file) and by tier probes (`internal/availability/probe.go:62`, `internal/relevo/tier.go:79`). Putting a placeholder in `PermissionArgs` would leak it unrendered into `Resume`. So the rule is added in `Launch` only, after `PermissionArgs`, not in the tier table.
- agy: `--add-dir <DirPlaceholder> --add-dir <OutputDirPlaceholder>`; its prompt file lives outside both dirs exactly like Claude's. Nothing in the repo shows agy failing here (no transcript, no test, no doc), and relevo has no way to grant read of a single file to agy: `--add-dir` takes a directory, and adding the binding directory would widen the writable root, which decision 1 forbids. Therefore: do NOT change agy in this round. State "agy has the same layout, but no evidence of the same refusal and no single-file grant exists; unverified" in the report. The builder checks `command -v agy`; if agy is installed in the sandbox, run one `--mode accept-edits` headless round reading a file outside both add-dirs and record the result in the report (observation only, no code change). If it is not installed, say so.
- codex edit (`-s workspace-write`) reads anywhere; opencode only runs at yolo; yolo claude (`--dangerously-skip-permissions`) is unaffected. Only claude read/edit is changed.
- Tier-source in dry run: `sendPreflight` (`send.go` 236-250, 340-346) knows whether the tier came from `--tier` (`opts.Tier`) or from the binding (`launchTier`/`effectiveTier`, `internal/relevo/tier.go`). The binding's tier is a stored string with no record of whether actor, policy default or the reader floor produced it; recording that needs a new binding field and a migration. Not small. Decision: add only a two-valued `tier_from` (see step 6) and list full provenance as a follow-up.

## Behaviour

1. Claude launch at tier `read` or `edit` gains exactly one argv element: `--allowedTools=Read(//<abs round prompt path>)`, placed after the permission-mode flags and before candidate `extra_args`. At `yolo` and `harness` (no relevo permission flags) nothing is added. Other kinds unchanged.
2. The path is the round's own `Store.PromptPath(binding, round)`, absolute. If the path given to the renderer is empty or not absolute, the element is omitted (never rendered as `Read(//)` or a relative rule, which would be wider or meaningless).
3. Writable root, `--add-dir`, and tier floors are untouched. No actor becomes yolo; policy default max_tier stays edit.
4. `send --dry-run` text and `--json` gain `tier_from`: `round override (--tier)` when `opts.Tier` is set, else `binding (fixed at bind; rebind to pick up config changes)`. Remote bindings: same two values.

## Seams (files, functions)

- `internal/harness/harness.go`
  - const block ~268-278: add `PromptFilePlaceholder = "<prompt-file>"` with a 1-2 line why-comment (renders the single-file read grant; separate from `OutputDirPlaceholder` because it names a file and becomes a permission rule).
  - `Launch` (~290-355): after `perm, err := h.PermissionArgs(tier)`, for `h.Kind == "claude"` and tier read or edit, append `PromptFilePlaceholder` to `perm`. Keep function under 70 lines; if it overflows, extract a small helper `promptReadArgs(kind string, tier Tier) []string`.
  - `PrintArgs` (~367-390): signature gains `promptFile string` (after `writableRoot`). `case PromptFilePlaceholder:` appends `"--allowedTools=Read(/" + promptFile + ")"` when `filepath.IsAbs(promptFile)`, else appends nothing. Update the doc comment (empty/relative dropped, the opposite of WritableRootPlaceholder, and why).
- `internal/harness/resume.go`: `ResumeBuild` (line 69, call at 86) gains `promptFile string` and forwards it to `PrintArgs`.
- `internal/spawn/launch.go`: `HeadlessLaunch` gains `promptFile string` after `writableRoot`, forwarded to `PrintArgs`.
- Callers of `HeadlessLaunch` / `ResumeBuild` (all must compile):
  - `internal/relevo/headless.go:288` (startRound) and `:482` (resumeRound): pass `rt.Store.PromptPath(b.Name, b.Round)`.
  - `internal/relevo/send.go:435` (preflight argv): pass `promptPath` (already in scope as `pf.promptPath`/local `promptPath`).
  - `internal/relevo/bind.go:815` (render-only validation): pass `""`.
  - `internal/availability/probe.go:134`: pass `""` (probe prompt is inline).
  - `internal/consult/verify.go:287`: pass `""` (reviewer gets an inline question; tier and prompt are not a round prompt file).
- `internal/relevo/send.go`: `DryRun` struct (781-796) add `TierFrom string \`json:"tier_from"\``; set it in `SendDryRun` (811-823) from a new `preflight.tierFrom` field (struct at 167) assigned in `sendPreflight` where `opts.Tier` is parsed (236-246) vs the binding path (340-346).
- `internal/relevo/text.go` `RenderDryRun` (~102): the tier line becomes `tier  <tier>  (<tier_from>)`.
- Docs: `docs/env-sandbox.md` or the permissions/tier doc that lists the claude tier flags (grep `acceptEdits` under `docs/` and `README.md`); add the one new flag where the tier table is documented. Do not create a new doc.

## Ordered steps

1. Baseline: run `go test ./internal/harness/... ./internal/spawn/... ./internal/relevo/...` once on the unmodified tree to know what is green. Deliverable: a recorded baseline; worked if it is green.
2. Harness change (`harness.go`, `resume.go`, `spawn/launch.go`): placeholder const, `Launch` append, `PrintArgs`/`ResumeBuild`/`HeadlessLaunch` signature. Deliverable: `go build ./...` fails only at the call sites listed above. Worked if the only errors are those callers.
3. Update the six callers listed in Seams. Deliverable: `go build ./... && go vet ./...` clean.
4. Fix existing harness tests that pin the claude argv and now see the placeholder or a changed `PrintArgs` arity: `internal/harness/tier_test.go` (~161, 207), `harness_test.go` (~418, 505, 537, 632, 659-760), `resume_test.go` (~250). Deliverable: `go test ./internal/harness/...` green with updated expectations (read/edit expectations now include `PromptFilePlaceholder`; yolo expectations do not).
5. Tests (names say what they pin; no history in comments):
   - Harness: claude `Launch` at read and at edit includes `PromptFilePlaceholder` after the `--permission-mode` pair and before extra; at yolo and harness it is absent; opencode/agy/codex `Launch` output never contains it. `PrintArgs` with an absolute path yields exactly `--allowedTools=Read(//abs/path)` as one element, with an empty path or a relative path yields no such element and no leftover placeholder; `Print` is not mutated. `ResumeBuild` for claude at edit forwards it.
   - Relevo (pure, no harness spawn): a test next to the existing send/preflight tests that builds the argv through `sendPreflight`/`SendDryRun` with the fake runner already used in `internal/relevo` tests and asserts the argv contains `--allowedTools=Read(//` + `rt.Store.PromptPath(name, round)` + `)` for a claude candidate at edit, and does not for yolo. Do NOT add a `cmd/relevo` test that executes `send`; CI has no harness binary or network, so any CLI-level coverage is a pure function in `internal/relevo`.
   - Dry run: `SendDryRun` returns `TierFrom` for `--tier` override vs binding; `RenderDryRun` includes it; the JSON key is `tier_from`.
   - Deliverable: `go test ./internal/harness/... ./internal/spawn/... ./internal/relevo/... ./internal/availability/... ./internal/consult/...` green.
6. Dry-run `tier_from` (small): add field, preflight assignment, render. Deliverable: dry-run output shows the source. If `sendPreflight` would exceed 70 lines or `send.go` 600 lines after the edit, move the assignment into a tiny helper rather than adding an exclusion. If it cannot be done in a few lines, drop this step and list it as a follow-up in the report.
7. Real-binary verification in an env sandbox (not part of CI). `scripts/relevo-sandbox.sh create p1253`, `shell p1253`, `export HOME=$SB/home` first (config agents otherwise writes the real `~/.codex` and `~/.config/opencode`); build the branch into the sandbox bin. Then, without relevo: in a scratch dir `A` (cwd) with a second dir `B` outside it holding `x.md`, run `claude -p "read B/x.md and print its first line" --permission-mode acceptEdits --add-dir <C>` (a) with no grant -> expect the refusal from the issue, (b) with `--allowedTools=Read(//B/x.md)` -> expect the content. Repeat (a)/(b) with `--permission-mode plan`. Also confirm a neighbouring file `B/y.md` is still refused under (b) (the grant is one file, not the directory). Then end to end: bind a `lite-planner` on a Claude candidate with the default (untiered) config, `relevo send --dry-run` shows the new flag and `tier_from`, send a trivial seed, confirm the round completes and its transcript (`relevo show <name> --round 1 --transcript`) has a successful Read of `001-prompt.md`. If `plan` mode ignores the allow rule, halt and report (do not widen `--add-dir`; tell the MasterMind that read-tier Claude still cannot start). Record the claude version and the observed outputs in the report.
8. Full check: `make check` once (gofmt, vet, mod tidy, lint, comments/filesize scripts). Coverage baseline: do not regenerate unless the code moved between packages (it does not). No new exclusions.
9. Mutation check (CLAUDE.md): remove the `Launch` append for tier edit and confirm the edit-tier harness test and the relevo-level test fail; change `IsAbs` handling to always render and confirm the empty-path test fails.
10. Save this plan verbatim to `docs/plans/2026-10-17-claude-edit-tier-prompt-read.md` and commit it with the code as new commits (never amend or rebase anything already on a remote branch).

## Declared scope (`git diff --stat` should show only these)

`internal/harness/harness.go`, `internal/harness/resume.go`, `internal/harness/harness_test.go`, `internal/harness/tier_test.go`, `internal/harness/resume_test.go`, `internal/spawn/launch.go` (+ its test if one exists), `internal/relevo/headless.go`, `internal/relevo/send.go`, `internal/relevo/text.go`, `internal/relevo/bind.go`, `internal/availability/probe.go`, `internal/consult/verify.go`, one new or extended `internal/relevo/*_test.go`, one doc file where claude tier flags are documented, `docs/plans/2026-10-17-claude-edit-tier-prompt-read.md`. Anything else (notably `internal/store`, `internal/roles`, `internal/policy`, `internal/config`, agents/ files, agy code) is out of scope; no deletions.

## Not in this round / follow-ups

- Full tier provenance (actor / policy default / reader floor) needs a stored field on the binding; file as an issue.
- agy: unverified, no single-file grant; file as an issue if the sandbox run shows the refusal.
- Chain readers read staged inputs under `<state>/chains/<chain>/inputs/...` (`ChainInputPath`, `chainSeedInput`), also outside `out/`; those paths are not the round prompt, so they stay out of this change. In the sandbox chain run, if a Claude edit-tier chain reader is refused on them, report it as a follow-up.
- Existing bindings keep their stored tier; this fix needs no rebind (the grant is applied at launch from the round's own prompt path).

## Report must include

`make check` result and the `git diff --stat` against the declared scope; the claude version used and the raw outputs of the sandbox permission experiments (edit and plan, grant vs no grant, neighbour file refused); whether the end-to-end round read its prompt; the agy observation or "agy not installed"; whether step 6 was done or deferred; the mutation results; and a note that no coverage baseline was changed.
