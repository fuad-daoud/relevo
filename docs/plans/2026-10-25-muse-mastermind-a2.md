# Slice A2: muse MasterMind hooks and plugin scripts

## Goal

Teach `relevo mastermind init --hook muse` and `relevo mastermind notice --hook muse` to accept the same Claude-shaped stdin payload and register kind `muse`, using muse's no-env answer wording. Branch the two plugin scripts on `MUSE_PLUGIN_ID`. Consent behaves exactly as for `--hook claude`.

## Assumed context (do not re-derive or re-implement)

- Slice A1 has landed: `mastermind.Detect` in `internal/mastermind/ident.go` has a `muse` branch keyed on a valid-UUID `MUSE_SESSION_ID` (Claude still wins when both are set), plus a muse host-pid lookup. This slice never touches `Detect`; where the hook needs "the session", it means the muse branch of `Detect` from A1 (kind `muse`, session from `MUSE_SESSION_ID`).
- The stdin payload for `--hook muse` is byte-identical to `--hook claude` (`session_id`, `cwd`, `source`, `hook_event_name`; see `HookInput` / `ParseHookInput` in `internal/mastermind/hook.go:15-53`). Parse it with the existing parser; no new payload shape.
- Consent (ask / enable / disable / reset / repo) is kind-agnostic and works exactly as for `--hook claude`. Do not change any consent semantics.

## Behavior and cases

1. `relevo mastermind init --hook muse` reads the Claude-shaped payload from stdin, resolves consent via `mastermindEffective` for kind `muse`, and on grant registers with `InitInput{Kind: "muse"}` through the existing registry path.
2. Muse has no `$CLAUDE_ENV_FILE`, so the muse init answer always uses the no-env wording, reworded for muse: it keeps the identity sentence and the guide, and says the session resolves through `MUSE_SESSION_ID` (not `RELEVO_MASTERMIND`, not the host process). It never appends to an env file on the muse path, even if `CLAUDE_ENV_FILE` happens to be set.
3. The claude init path is unchanged: it still tries the `$CLAUDE_ENV_FILE` append when set and falls back to the existing claude no-env text (`HookOutputNoEnv` / `NoEnvText`, `internal/mastermind/hook.go:93-103`) when unset.
4. `relevo mastermind notice --hook muse` mirrors the claude notice exactly (baseline compare via `SessionTold`, late registration on grant, silent when nothing changed), but for kind `muse`, and a grant notice uses the muse no-env wording from case 2.
5. Both verbs reject any `--hook` value other than `claude` or `muse` with exit 2 (`init` via `fail(codeUsage, ...)`; `notice` via the existing stderr + exit-code-2 pattern). `--hook` with `--kind`/`--session` on `init` is still rejected.
6. `claude-plugin/scripts/mastermind-init.sh` and `mastermind-notice.sh` exec `relevo mastermind init|notice --hook muse` when `MUSE_PLUGIN_ID` is set (non-empty) and `--hook claude` otherwise. Both stay POSIX `sh`, stay 5-ish lines, and keep the silent `command -v relevo || exit 0` guard.
7. `relevo help --json` goldens reflect the flag's documented values if the flag help text changes (see step 6 of the steps).

## Seams (read these before editing)

- `cmd/relevo/mastermind.go:39-40` — `cmdMasterMind` usage lines naming `--hook claude`.
- `cmd/relevo/mastermind.go:108-116` — `mastermindInitFlagSet`; hook help text on line 113 (`only "claude"`).
- `cmd/relevo/mastermind.go:122-140` — `cmdMasterMindInitRun` hook gate (line 133) and the `mastermindInitHook(*name)` call (line 139); thread the hook kind through here.
- `cmd/relevo/mastermind_hook.go:68-155` — `mastermindInitHook`; every hardcoded `"claude"` (lines 93, 107, 114, 123, 141) becomes the passed kind, and the env-file branch (lines 143-150) is claude-only while muse always emits the muse no-env output.
- `cmd/relevo/mastermind_hook.go:174-178` — `mastermindNoticeFlagSet` hook help text (line 176).
- `cmd/relevo/mastermind_hook.go:183-195` — `cmdMasterMindNotice` gate (line 190-193); accept `claude|muse` and pass the kind into `mastermindNoticeHook`.
- `cmd/relevo/mastermind_hook.go:201-280` — `mastermindNoticeHook`; every hardcoded `"claude"` (lines 222, 237, 241, 257) becomes the passed kind; the `CLAUDE_CODE_AGENT` lookup stays as-is (muse sets no agent var; do not invent one).
- `cmd/relevo/mastermind_hook.go:286-298` — `mastermindNoticeText`; the `$CLAUDE_ENV_FILE` check (line 288) is the claude rule — the muse path always uses the muse no-env grant wording.
- `internal/mastermind/hook.go:73-103` — `hookContext`, `noEnvNote` (line 79), `HookOutput` (89), `HookOutputNoEnv` (94), `NoEnvText` (101); add the muse counterpart (e.g. `NoEnvTextMuse` / `HookOutputMuse`) whose note says the session resolves through `MUSE_SESSION_ID`. Keep functions at most 70 lines, no history comments, comments say why only.
- `internal/mastermind/consent.go:30-50,132-146` — `ConsentText`, `AskNote`, `HookConsent`/`HookConsentFor`; unchanged, reused verbatim for muse.
- `cmd/relevo/registry_rows.go:536-560` — `mastermind init` entry (538) and `mastermind notice` entry (556) `Args` strings; update only if the flag help text changes.
- `cmd/relevo/testdata/contract/help-json.golden` (and `help-json-verb.golden` if it names the hook flag) — regenerated with `go test ./cmd/relevo/ -run TestContract -update` per `cmd/relevo/contract_test.go:24-27`; check in the result.
- `claude-plugin/scripts/mastermind-init.sh` (5 lines) and `claude-plugin/scripts/mastermind-notice.sh` (5 lines) — full files; add the `MUSE_PLUGIN_ID` branch.
- Existing tests to mirror (do not change their claude assertions): `cmd/relevo/mastermind_test.go` `TestMasterMindInitHookWithoutEnvFileSaysSo`, `TestMasterMindNoticeHook`, `TestMasterMindNoticeRequiresClaude` (line 1266), `runWithStdin` helper; `internal/mastermind/handoff_test.go` `TestHookOutputCarriesTheGuide`.

## Steps

1. Add the muse no-env wording in `internal/mastermind/hook.go` (new `NoEnvTextMuse`-style constructor plus output helper reusing `encodeHookContext`); deliverable: the new function beside `NoEnvText`; know it worked when a focused unit test asserting the text mentions `MUSE_SESSION_ID` and the guide passes.
2. Thread the hook kind through `init` in `cmd/relevo/mastermind.go` and `cmd/relevo/mastermind_hook.go` (`mastermindInitHook` takes the kind; all session/consent/baseline/registration calls use it; muse skips the env-file branch and emits the step-1 output); deliverable: `--hook muse` registers kind `muse`; know it worked when the new init-hook test with a yes-repo fixture asserts kind `muse` in the registry and the muse wording on stdout.
3. Thread the hook kind through `notice` (`cmdMasterMindNotice` gate plus `mastermindNoticeHook` and `mastermindNoticeText`); deliverable: `--hook muse` notice mirrors claude for kind `muse` with muse grant wording; know it worked when the new notice test (grant then rename/revoke produces the changed-status notice, unchanged produces `{}`) passes.
4. Update usage strings, both `--hook` flag help texts, and both rejection messages to name `claude` and `muse`; deliverable: `--hook other` on both verbs exits 2 naming both values; know it worked when the invalid-hook tests for both verbs pass.
5. Branch both plugin scripts on `MUSE_PLUGIN_ID` (`${MUSE_PLUGIN_ID:+muse}`-style or an `if`; POSIX `sh` only); deliverable: the two edited scripts; know it worked when `scripts/plugin-shim_test.sh` (which already stubs `mastermind init --hook claude` and `notice --hook claude` at lines 161-162) still passes plus a manual `MUSE_PLUGIN_ID=1 sh -n` syntax check and a stub run showing `--hook muse` is exec'd when set.
6. Regenerate `relevo help --json` goldens if any documented value changed; deliverable: updated golden files with no other diff; know it worked when `go test ./cmd/relevo/ -run TestContract` passes without `-update`.
7. Add focused tests only: init-hook muse registration + muse no-env wording, notice muse grant/change/silent cases, invalid `--hook` value on both verbs, and a script-level assertion for the `MUSE_PLUGIN_ID` branch; each test name states what it pins (e.g. `TestMasterMindInitHookMuseUsesMuseWording`); deliverable: new tests alongside the existing claude ones; know it worked when the full `cmd/relevo` and `internal/mastermind` package suites pass.
8. Run `gofmt -l` on the changed files, then `make check-affected`, fix every failure before the next run, and finish with the full gate for the touched packages only; know it worked when both commands are green in this session.

## Deleted behavior

1. No files are deleted.
2. No verbs, flags, or flag spellings are removed.
3. No existing claude-path output, message, or test assertion is altered except to name both hook values where the message enumerates them.
4. No consent, registry, `Detect`, doctor, README, or MCP behavior is removed or changed.

## Must NOT change

- `internal/mastermind/ident.go` `Detect` and any host-pid lookup (slice A1 owns them); reference them only as "the muse branch of `Detect` from A1".
- `relevo doctor`, the README, and any MCP behavior beyond what registration already gives.
- Any plugin script besides the two `mastermind-*.sh` files; consent semantics; `Record.Validate` session-id rules.
- Test style rules: comments say why, no issue/PR/round numbers in code, test names say what they pin. Per repo rules, `cmd/relevo` tests must not spawn a harness binary or reach the network — follow the existing `runWithStdin`/`captureOutput` patterns, which do neither.
- Never amend or rebase a commit on a remote binding's branch; this work lands as new commits.


## Added by the MasterMind after slice A1 landed (it is on this branch now)

- A1 is commit `bb82c0e9` on this branch: `Detect` has the muse branch (`museSessionEnv = "MUSE_SESSION_ID"`), `Record.Validate` the UUID rule, `ResolveInput.MuseHost` / `Runtime.MuseHost` the ancestor seam, and `mastermindCaller` already rewrites a muse host pid through it. Use them; do not redo them.
- Runner silence: a muse builder relevo starts also runs with `MUSE_SESSION_ID` set, and if the owner installs the plugin at user scope, every muse runner fires these hooks. The `mastermind.IsRunner(os.Getenv)` early return at the top of `mastermindInitHook` and `mastermindNoticeHook` must stay first and must cover `--hook muse` too. Add `TestMasterMindInitHookMuseSilentInRunner` (RELEVO_RUNNER set, `--hook muse`: prints nothing or `{}` exactly as the claude runner case does, registers nothing) and the notice counterpart.
- This builder's own environment carries a real `MUSE_SESSION_ID`. Any test that reads the real environment must `t.Setenv("MUSE_SESSION_ID", "")` (or set it to a fixture UUID) the way A1 isolated two tests.
- Hook registration must record the muse process as host: the hook's parent is `muse` itself, so `mastermindCaller`'s seam already finds it; state in the report which host pid a `--hook muse` registration records in your test.

## Report must include

- The new hook-kind parameter names and the exact muse wording function's name in `internal/mastermind/hook.go`.
- Each new/updated test name and the single behavior it pins.
- `gofmt -l` output, `make check-affected` result, and confirmation the help-json goldens were regenerated or were unaffected.
- Anything left undone and why.

Save this plan as `docs/plans/2026-10-25-muse-mastermind-a2.md`, run `gofmt -l` on the changed files, run `make check-affected`, commit, then report.
