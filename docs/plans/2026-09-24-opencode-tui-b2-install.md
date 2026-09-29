# Round B2: ship the OpenCode plugin in the binary, install it, and doctor it (#393)

Binding `oc-tui-a`, branch `relevo/oc-tui-a`, rebased onto `origin/main`
(`b66c6fcc`, cockpit wave 2) by the planner; tip `1011984f`. Rounds A, B1 and
B1-fix are done: `internal/harness/opencodeplugin/{package.json,tui.tsx,server.ts}`
exist and pass `scripts/opencode-plugin-smoke.sh` (14 assertions).
Spec: `docs/specs/2026-09-24-opencode-tui-plugin-design.md` §5.4, §5.5.

**If a step is impossible as written or contradicts the code, stop and report.
Do not improvise around it.** Never write to the real `~/.config/opencode/`:
every test uses a temp home through the existing `InstallEnv` / doctor `Env`
seams. CI has no harness binary and no network: no `cmd/relevo` test may run a
subcommand that spawns a harness or reaches the network.

## 1. System Overview

relevo already installs its agent definitions with `relevo config agents`
(`cmd/relevo/agent.go` `cmdAgentInstall` → `harness.Install`,
`internal/harness/install.go`), refreshes them on daemon start
(`cmd/relevo/main.go` ~l.2960, `harness.Install(env, InstallOptions{})`) and in
`relevo config init` (`cmd/relevo/init.go` ~l.82), and reports drift in
`relevo doctor` (`rolesCheck`, `internal/doctor/doctor.go` ~l.454). This round
adds the plugin package to that machinery as **shipped files** of the
`opencode` harness, with one difference from agent definitions: the plugin is
**opt-in**. Only `relevo config agents` writes it when it is absent; once
installed, every `Install` refreshes it under the same rules as a definition
(write if absent-and-opted-in, refresh an unmodified copy, keep a user edit
unless `--force`).

This round deletes no behaviour. One existing test is **ported** (not deleted):
the table test in `internal/harness/harness_test.go` (~l.730) that asserts the
whole opencode `Harness` literal gains the new `Files` field.

## 2. File Structure

```
internal/harness/
  files.go              NEW  ShippedFile; the go:embed of opencodeplugin/*; ShippedFileBytes
  files_test.go         NEW
  harness.go            MOD  Harness.Files; the opencode entry (~l.175-195) lists the 3 plugin files
  harness_test.go       MOD  PORT: the opencode table literal (~l.730-745) gains Files
  install.go            MOD  InstallOptions.Files; Install loops Files after Roles; installOne → shared installBytes
  install_test.go       MOD  new tests
internal/doctor/
  opencode.go           MOD  opencodePluginCheck, opencodePluginKeysCheck
  opencode_test.go      MOD  new tests
  doctor.go             MOD  call the two checks in the opencode block (~l.770-777, next to opencodeAllowlistCheck)
cmd/relevo/
  agent.go              MOD  cmdAgentInstall sets Files: true; usage text says it also installs the OpenCode plugin
docs/specs/2026-09-24-opencode-tui-plugin-design.md   MOD  §5.4, §5.5 text (§7 step 6)
```

`internal/harness/opencodeplugin/*` does not change.

## 3. Data Structures

```
// internal/harness/files.go
type ShippedFile struct {
	Name  string // install label and result "role" column: "opencode-plugin/package.json" etc.
	Path  string // home-relative install path: ".config/opencode/plugins/relevo/package.json"
	Embed string // path inside the embed FS: "opencodeplugin/package.json"
}
```
`Harness.Files []ShippedFile` -- nil for every kind except `opencode`, which has,
in this order: `package.json`, `server.ts`, `tui.tsx` (Name
`opencode-plugin/<file>`, Path `.config/opencode/plugins/relevo/<file>`, Embed
`opencodeplugin/<file>`).

`InstallOptions.Files bool` -- when true, an absent shipped file is written;
when false, an absent shipped file produces **no result row** (the plugin is not
installed and stays that way). A present file follows the definition rules in
both cases.

## 4. Interfaces and Contracts

```
// internal/harness/files.go
//go:embed opencodeplugin/package.json opencodeplugin/server.ts opencodeplugin/tui.tsx
var pluginFS embed.FS
func ShippedFileBytes(kind, name string) ([]byte, error)   // by the table's Embed, never by caller path; unknown → ErrNoAgentDoc
```

```
// internal/harness/install.go
```
- Extract `installOne`'s decision table (~l.180-245) into
  `installBytes(env InstallEnv, opts InstallOptions, res InstallResult, homeRel string, shipped []byte, manifest map[string]string, shippedBeforeKey string) (InstallResult, bool, error)`;
  `installOne` keeps its signature and calls it with
  `agentDocBase(r, kind)` as the key. `ShippedBefore` is consulted only when the
  key is non-empty; shipped files pass "" (they have no pre-manifest history).
  The table's behaviour for roles is byte-for-byte unchanged -- every existing
  install test must pass untouched.
- `Install`: after the roles loop of each kind, and only when `opts.Role == ""`,
  loop `h.Files`: read `ShippedFileBytes`; if the file is absent on disk and
  `!opts.Files`, skip it (no row); else `installBytes(…)` with `Role: f.Name`,
  `Path: f.Path`. The manifest keys are the home-relative paths, as for roles.

```
// internal/doctor/opencode.go
func opencodePluginCheck(env Env) Check          // Group "opencode", Name "plugin"
func opencodePluginKeysCheck(env Env) Check      // Group "opencode", Name "plugin keys"; zero Check = no row
```
`opencodePluginCheck`: for each `h.Files` of `harness.Lookup("opencode")`, resolve
`env.HomePath(f.Path)`, `env.Stat`, `env.ReadFile`, compare with
`harness.ShippedFileBytes` using `harness.DocEqual`.
- none present → `SevOK`, Detail `not installed -- relevo config agents installs the OpenCode plugin`
- all present and equal → `SevOK`, Detail `installed (~/.config/opencode/plugins/relevo)`
- otherwise → `SevWarn`, Detail naming each missing or differing file,
  Fix `relevo config agents (add --force to replace your edits)`.

`opencodePluginKeysCheck`: only when the plugin is installed (at least one file
present), else zero Check. Read, in order, `~/.config/opencode/opencode.jsonc`,
`opencode.json`, `cli.json` (skip unreadable/unparseable files; `stripJSONC`
~l.178 of this file already exists). In each, a top-level `keybinds` object maps
command id → a key string or an array of key strings. Any command id not
starting with `relevo.` bound to one of `<leader>o`, `<leader>j`, `ctrl+x o`,
`ctrl+x j` → `SevWarn`, Detail `<file>: <command> is bound to <key>, which the
relevo plugin uses`. None → `SevOK`, Detail `ctrl+x o and ctrl+x j are free`.

```
// internal/doctor/doctor.go, the opencode block (~l.770-777)
```
Inside `if !cfg.adopted`, next to `opencodeAllowlistCheck`: append
`opencodePluginCheck(env)`, then `opencodePluginKeysCheck(env)` when its Name is
non-empty. Nothing else changes; `rolesCheck`'s dry run (Files false) now also
flags a stale or edited installed plugin file, which is the intended effect.

```
// cmd/relevo/agent.go cmdAgentInstall
```
`opts.Files = true`. Usage/help text: add one sentence, "For opencode it also
installs the relevo OpenCode plugin (~/.config/opencode/plugins/relevo)."

## 5. Pseudocode

```
Install(env, opts):
  for each kind in scope:
     for each role r: (unchanged) installOne
     if opts.Role == "":
        for each f in h.Files:
           shipped := ShippedFileBytes(kind, f.Name)
           full := env.HomePath(f.Path)
           if absent(full) && !opts.Files: continue
           res, changed := installBytes(env, opts, {Kind, Role: f.Name, Path: f.Path}, f.Path, shipped, manifest, "")
           append res
  save manifest once if changed (unchanged)
```
`writeDoc` already creates parent directories? Check it (`install.go` ~l.257);
if it does not `MkdirAll` the parent, add that for shipped files only (the
plugins/relevo directory will not exist on first install). Report which.

## 6. Error Handling

As for definitions: a missing embed is an `OutcomeError` row; a write failure is
the row's error; the manifest is saved once. Doctor checks never fail the run:
unreadable config files are skipped.

## 8. Working Efficiently

- One batch of reads: `internal/harness/{install.go,harness.go,agents.go,shipped.go}`,
  `internal/harness/install_test.go` (its fake env), `harness_test.go` ~l.720-760,
  `internal/doctor/{opencode.go,doctor.go ~l.440-500 and ~l.700-780,env.go}`,
  `internal/doctor/opencode_test.go` (its fake Env), `cmd/relevo/agent.go`.
- One edit per file. Focused runs:
  `go test ./internal/harness/ -count=1`
  `go test ./internal/doctor/ -run 'Opencode|Roles' -count=1`
  `go test ./cmd/relevo/ -run 'Agent|Doctor' -count=1`
- Full check once: `make check` (it also runs `scripts/agents-shipped.sh --check`;
  plugin files are not agent definitions and must not be added to
  `shipped.sha256`).
- After `make check`, run `bash scripts/opencode-plugin-smoke.sh` once to prove the
  plugin itself is untouched (14 PASS).

## 7. Ordered Steps

1. **Embed + table**: `files.go`, `Harness.Files`, the opencode entry, the ported
   `harness_test.go` literal. Test `TestShippedFileBytes`: each of the three names
   returns the bytes of the file under `internal/harness/opencodeplugin/`
   (read it with `os.ReadFile` in the test and compare); an unknown name and a
   non-opencode kind return an error.
2. **Install**: `installBytes` extraction, `InstallOptions.Files`, the Files loop.
   Tests (`install_test.go`, fake env): `Files:false` + nothing on disk → no plugin
   rows; `Files:true` + nothing on disk → three `wrote` rows, bytes on disk equal
   the embed, manifest has the three paths; second `Files:false` run → three
   `kept identical`; an unmodified older copy (manifest sha matches) → `updated`
   even with `Files:false`; a user-edited copy → `kept (differs)`, and with
   `Force` → `overwrote`; `DryRun` writes nothing; `--role plan-executor` → no
   plugin rows. Every existing install test passes untouched.
3. **Doctor**: the two checks + the doctor.go calls. Tests (`opencode_test.go`,
   fake Env): plugin none/all-equal/one-edited/one-missing; keys: none installed →
   no row; `opencode.jsonc` with `// comment` and `"keybinds": {"session.redo": "<leader>o"}`
   → WARN naming `session.redo`; `cli.json` binding `relevo.open` to
   `<leader>o` → OK; no keybinds → OK.
4. **cmd**: `cmdAgentInstall` `Files: true` and the usage sentence. Flag-parsing
   test only if one exists for this verb today; do not add a test that runs the
   install against the real home.
5. **Spec text** (`docs/specs/2026-09-24-opencode-tui-plugin-design.md`): in §5.4,
   replace the sentence about `package.json`'s `version` and the
   `$OPENCODE_CONFIG_DIR` parenthesis with: "The plugin is opt-in: only `relevo
   config agents` writes it when absent; once installed, every install (daemon
   start, `relevo config init`) refreshes an unmodified copy and keeps an edited
   one. It installs under `~/.config/opencode/plugins/relevo/` like the agent
   definitions under `~/.config/opencode/agents/`." In §5.5, change the `plugin`
   row's "WARN `not installed`" to "OK `not installed -- relevo config agents
   installs the OpenCode plugin`".
6. **Verify**: `make check`; the smoke once; `git diff --stat` lists only §2's
   files. Commit:
   `feat(opencode): relevo config agents installs the OpenCode plugin; doctor reports it and its keys (#393)`.

## Report

Per step: status and tests added; whether `writeDoc` needed `MkdirAll`; the
ported literal diff; `make check` tail; the smoke's last line; `git diff --stat`.
