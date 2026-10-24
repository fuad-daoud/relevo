# Plan: Meta's Muse Code CLI as a harness kind

Issue: #1357. This adds `muse` as a fifth harness kind beside claude, codex, agy and opencode, so a candidate such as `muse/meta/muse-spark-1.3#xhigh` can serve any actor. Builders are the main target. The reason is that the Muse Code subscription works only inside the `muse` CLI, and one measured builder round used about 4% of the $5 plan's 5-hour window.

## Facts about `muse exec` (Muse Code 1.4.11)

- **Headless form:** `muse exec [OPTIONS] [PROMPT]`. Exit codes: 0 completed, 1 failed or cancelled, 2 usage error, 130/143 signal.
- **`--json` output:** JSONL on stdout. Every line has `payload_type` and `payload`, and `stream` is `{kind, id}`.
  - The **session id** is `stream.id` on any line whose `stream.kind` is `"session"` (first line onward).
  - `run.model.configured`: `payload.model_id`, `payload.provider_id`.
  - `tool.result`: `payload.text`, plus `payload.correlation_facts.tool_name` and `.outcome`.
  - `run.output.delta`: streamed assistant text.
  - **`run.usage`**, one per run near the end:
    - `payload.models[]` holds `{model, input_tokens, cached_tokens, output_tokens, reasoning_tokens, calls}`.
    - `input_tokens` INCLUDES `cached_tokens`, so fresh input is `input_tokens - cached_tokens`.
    - `payload.allowance` holds `{window:{used_percent,resets_at_ms}, weekly:{used_percent,resets_at_ms}}`: the subscription meter. It's null under API-key billing.
  - **`run.terminal.<kind>`**, the last line, where `<kind>` is `completed`, `failed` or `cancelled`. `payload.terminal` repeats the kind, and `payload.text` is the final answer.
  - On failure there is an error object `{kind, message, retryable, quota?}`. `quota` (`limit_kind` such as `tierLimit`, plus `resets_at_ms`) is present only for a quota stop. No failed stream has been captured yet, so match the limit on text as well.
- **Model and effort:** `--model <id>` and `--reasoning-effort none|minimal|low|medium|high|xhigh|max|ultra`. Model ids are `muse-spark-1.3`, `muse-spark-1.2` and `muse-wonder-1.0`; `-contributor` variants exist on the API.
- **Workspace:** `--workspace <PATH>` roots the tools at PATH. The default is the cwd.
- **Role definitions:** `muse exec` has no agent-definition flag (`--agents` is TUI-only). It does accept `-c run.developer_prompt=<JSON string>`, a per-run setting whose value is JSON. This was verified: the model follows it.
- **Resume:** `muse exec --session-id <uuid> "<prompt>"` sends a follow-up turn into an existing session. It refuses a workspace mismatch, and relevo always resumes in the same tree. `muse resume` is TUI-only.
- **Permissions:**
  - `--yolo` disables approval and the sandbox, trusts the workspace and withholds user-input requests.
  - Muse's sandbox blocks unix sockets and treats `~/.local/state/relevo` as read-only. Relevo's own test suite fails inside it, and a reader cannot write its output file under relevo's state dir. So only `yolo` and `harness` are honoured.
- **Always-on flags for a relevo runner:**
  - `--disable-reminders`: Muse's "skill-reminder" helper otherwise doubles the model calls.
  - `--disable-request-user-input`: a headless run must never wait on a question.
- **Auth:**
  - `muse login` signs in with a device code that you approve in a browser.
  - The credential lives at `$XDG_CONFIG_HOME/muse/auth.json`, else `~/.config/muse/auth.json`.
  - `META_API_KEY` takes priority and is billed pay-as-you-go.
- **Provider:** `meta`.

## Part A: harness core (`internal/harness`, `internal/spawn`, `internal/agentsrc`)

1. **`knownHarnesses["muse"]`** in `internal/harness/harness.go`:
   - Kind and Binary are `muse`; MinVersion is `1.4.11`; Providers is `[]string{"meta"}`.
   - LimitPatterns: `(?i)usage limit`, `(?i)tier.?limit`, `(?i)quota (exceeded|reached|exhausted)`, `(?i)"limit_kind"`, `(?i)rate.?limit`, `(?i)error 429`, `(?i)too many requests`. Add a comment that only the quota field's shape is known from Muse's wire schema; no limit stream has been captured yet.
   - DenialPatterns: `(?i)permission denied`, `(?i)(approval|tool call) (was )?(rejected|denied)`.
   - Roles: the six shipped roles at `.config/muse/agents/<role>.md` with `Doc: "<role>.muse"`. Path is home-relative, as for the other kinds.
2. **`Launch`, `case "muse"`:**
   - Split the model with `SplitEffort` exactly as codex does. A bad model is `ErrBadModel`.
   - Print is `exec --json --model <id> [--reasoning-effort <effort>] --disable-reminders --disable-request-user-input --workspace DirPlaceholder -c DefinitionPlaceholder PromptPlaceholder`.
   - `promptAt` is the index of PromptPlaceholder.
3. **`DefinitionPlaceholder`**, a new constant beside the others:
   - Add a final `definition string` parameter to `Launch.PrintArgs`. It fills DefinitionPlaceholder with `run.developer_prompt=` + the JSON encoding of `definition` (`json.Marshal` of the string).
   - Update every caller: `internal/harness/resume.go` (`ResumeBuild` takes and passes it) and `internal/spawn/launch.go`. Every other kind passes `""`, and its Print has no DefinitionPlaceholder.
   - In `spawn.HeadlessLaunch`, when the rendered launch contains DefinitionPlaceholder, read the role's installed definition file: `harness.DefinitionPath("muse", role.Definition)` under `os.UserHomeDir()`. Strip a leading YAML frontmatter block if present, and pass the body.
   - A missing file is an error that names the path and says `relevo config agents --force` installs it. HeadlessLaunch's doc comment must stop saying "Pure" and say it reads that one file for muse.
   - Keep `bind.go`'s validation call working: it passes an empty prompt and must not fail on a missing definition. Give HeadlessLaunch a way to skip the read only when the prompt is empty, or have bind tolerate that error. Choose the smaller change and say which in the report.
4. **`PermissionArgs`, `case "muse"`:**
   - TierYolo: `--yolo`.
   - TierRead and TierEdit: ErrTierUnsupported. The message says Muse's sandbox blocks unix sockets and relevo's state dir, and suggests `--tier yolo (--yolo)` or `--tier harness (muse settings decide)`.
   - **`PermissionFlags`, `case "muse"`:** `--yolo`, `--disable-approval`, `--disable-sandbox`, `--approval-mode`, `--permission-profile`, `--sandbox-network`, `--trust-workspace`.
5. **Resume (`internal/harness/resume.go`):**
   - `Resume`, `case "muse"`: `exec --json --session-id <id> --disable-reminders --disable-request-user-input <prompt>`, then the PermissionArgs.
   - `ResumeBuild`, `case "muse"`: the selector is `--session-id <id>`.
   - `DeleteSession`: muse sessions persist on disk, but no delete command exists. Return ErrSessionDeleteUnsupported or the kind's current "not supported" error, whichever already fits the switch.
6. **`DefinitionPath`** (`internal/harness/definition.go`): the convention route for a non-shipped name is `.config/muse/agents/<name>.md`. `HomeEnv` and `AccountHome` stay "no per-process home" for muse; no account support in this plan.
7. **Rendering (`internal/agentsrc/render.go`), `case "muse"`:** the body only, with no frontmatter. Start with one HTML comment line, `<!-- relevo agent <name> for muse, rendered by relevo from its source; edit the source (relevo config agents), not this file. -->`, then a blank line, then `s.Body`. Check `RenderedKinds` and `source.go`: if sources render to every known kind by default, muse is now included.
8. **Shipped files:**
   - Add `internal/harness/agents/<role>.muse.md` for all six roles by rendering each shipped source the same way the other kinds' shipped files are produced. Find the generator or the test that pins them, and follow it.
   - Refresh the checksum file with `sh scripts/agents-shipped.sh --write`.
9. **Tests**, following the existing codex cases in each `_test.go` you touch:
   - Launch argv for muse at yolo and harness, with and without an effort.
   - The read and edit refusals.
   - PrintArgs filling DefinitionPlaceholder, including a definition containing quotes and newlines.
   - The Resume and ResumeBuild argv.
   - The render output.
   - HeadlessLaunch reading the definition file (temp HOME), and the missing-file error.
   - Tables that enumerate every kind (harness_test, tier_test, definition_test, agents_test, custom_test, pin_test and others that `git grep '"codex"' -- '*_test.go'` finds) must gain their muse row wherever they iterate all kinds.

## Part B: transcript and usage (`internal/transcript`, `internal/usage`)

Use the fixtures `muse-stream.jsonl` (a trimmed real builder round, 24 lines) and `muse-smoke.jsonl` (a one-word "pong" run). Mirror the codex cases in each file.

1. **`session.go`:** the session id is `stream.id` of the first line with `stream.kind == "session"`.
2. **`final.go`:** the final text is `payload.text` of the last `run.terminal.*` line.
3. **`first.go`:** whatever "first event" means for codex. Mirror it: the first line that proves the model started, e.g. `run.lifecycle.started` or the first `run.output.delta`. Read codex's case to decide, and say which in the report.
4. **`turnend.go`:** clean is `run.terminal.completed`; unclean is `run.terminal.failed` and `run.terminal.cancelled`.
5. **`limit.go` and `errtext.go`:** for muse, the error text is the `message` of an error object on a `run.terminal.failed` line, if any. Search `payload.error` or `payload.reason` as both appear plausible, and fall back to the raw line. Limit detection runs the kind's LimitPatterns over that text. Use a synthetic failed line in a test: `{"payload_type":"run.terminal.failed","payload":{"terminal":"failed","error":{"kind":"quota","message":"Usage limit reached for your plan","retryable":false,"quota":{"limit_kind":"tierLimit","resets_at_ms":1792848178000}}}}`.
6. **`renderer.go`:** render a muse stream for `relevo show --transcript`, at the same level of detail as codex:
   - assistant text, joined from `run.output.delta` text
   - one line per `tool.result`: tool name, outcome and the first line of text
   - the usage summary
   - the terminal
7. **Usage (`internal/usage`):**
   - Add `muse.go` beside `codex.go`. It reads `run.usage` and sums every `models[]` entry: in = input − cached, cache = cached, out = output_tokens.
   - Put reasoning tokens wherever codex puts its reasoning tokens, if it does; otherwise leave them out and say so.
   - No cost: subscription billing has no per-round price. Use whatever `cost_basis` codex or agy uses for "unknown".
   - Wire it into `source.go`, `steps.go` and `carry.go` the way codex is wired.
8. **Fixtures:** copy them into `internal/transcript/testdata/muse.jsonl` (the stream) and `internal/usage/testdata/muse-stream.jsonl`, plus the smoke file where a test needs a minimal run.
   - Expected usage totals from the 24-line fixture: input_tokens 9273123, cached_tokens 9073518, output_tokens 56699, reasoning_tokens 31120, calls 80.
   - The session id is `01a16d03-a71a-75f2-a2cd-394c8814cff2`.

## Part C: doctor, docs (done after A and B merge)

1. **`internal/doctor/logins.go`:** `"muse": {home: ".config/muse", file: "auth.json", login: "muse login"}`, plus its test row.
2. **Setup wizard:** out of scope. The follow-up issue covers it, along with the allowance-as-gate signal and a contributor-mode guard for private repos.
3. **README:** the harness table and tier table get a muse row: yolo and harness only, with why. Also one paragraph covering:
   - the subscription only works inside the `muse` CLI
   - `META_API_KEY` switches billing to pay-as-you-go
   - `muse login` uses a device code, so it works over SSH
4. **This plan file** ships in the PR.

## Rules for every part

- Follow CLAUDE.md:
  - Comments say why, never restate the code.
  - No issue numbers or history in code or test names.
  - Functions are at most 70 lines and non-test files at most 600.
  - `gofmt` every touched file.
- Never push, never use `gh`, never `git stash`, never amend or rebase. Commit on the current branch.
- On this laptop, run only focused `go test ./internal/<pkg>/...` for the packages you touched, plus `go vet` on them. `make check*` is blocked here by a hook; the MasterMind runs the gate on a server.
- Report:
  - files changed
  - each decision the plan left open
  - test names and what they pin
  - one mutation per new rule: break it, name the test that failed
