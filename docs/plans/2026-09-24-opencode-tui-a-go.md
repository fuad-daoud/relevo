# Round A: an OpenCode session is a planner, and its rows as JSON (#393)

Spec: `docs/specs/2026-09-24-opencode-tui-plugin-design.md` §5.1, §5.2, §5.7
(on branch `docs/opencode-tui-plugin-spec`; it may not be in your tree -- this
plan is complete without it). Round B (the OpenCode plugin itself, its
installer and doctor rows) comes later and consumes what this round builds.

**If a step is impossible as written or contradicts the code, stop and report.
Do not improvise around it.**

## 1. System Overview

relevo can already push a finished report into an OpenCode planner's chat
(`OpencodeDeliverer`, `internal/relevo/deliver_opencode.go`, wired at
`cmd/relevo/main.go:534`), but no OpenCode session ever becomes a planner:
`planner.Detect` (`internal/planner/ident.go`) knows only Claude and agy, and
OpenCode's tool shell carries no session id. Round B ships an OpenCode server
plugin whose shell hook sets `RELEVO_HARNESS=opencode` in every shell command,
and a TUI plugin that registers the session it shows and polls this planner's
rows.

This round builds the relevo side of that:

1. **Detect + Resolve**: `RELEVO_HARNESS=opencode` makes Detect report kind
   `opencode`; Resolve fills the session id by matching the process's working
   directory against OpenCode's session table (`opencode.db`).
2. **Register on demand**: when that session has no planner record yet, the
   verbs that need a planner (`bind`, `add`, `fork`, `ask`, all through
   `resolveVerbPlanner`) register it.
3. **`relevo planner init --host-parent`**: the TUI plugin's registration,
   recording the TUI process as host and looking the record up by session only.
4. **`relevo status --line --json`**: this planner's rows as JSON, same words as
   the text status line.

This round deletes no behaviour. Every existing test must pass unchanged.

## 2. File Structure

```
internal/planner/
  opencode_match.go          NEW  OpencodeSession, MatchOpencodeSession, its two errors
  opencode_match_test.go     NEW
  ident.go                   MOD  Detect: RELEVO_HARNESS=opencode (after claude and agy)
  resolve.go                 MOD  ResolveInput.CWD, .OpencodeSession; the opencode session step;
                                   ErrUnregisteredSession
  resolve_test.go            MOD  new cases (existing cases untouched)
  hook.go                    MOD  InitInput.SessionOnly; findCaller skips the host lookup for it
  init_test.go               MOD  new case
internal/relevo/
  opencode_session.go        NEW  OpencodeSessionFinder: reads opencode.db with sqlite3 -json, then matches
  opencode_session_test.go   NEW
  runtime.go                 MOD  Runtime.OpencodeSession field (next to ProcStart, ~line 254)
  bind.go                    MOD  resolveVerbPlanner: pass CWD + OpencodeSession; register on ErrUnregisteredSession
  bind_test.go               MOD  new cases
  statusline.go              MOD  StatusLineRow, StatusLineDoc, StatusLineRows
  statusline_test.go         MOD  new cases
cmd/relevo/
  main.go                    MOD  newRuntime wiring (ProcStart line ~763); status --line --json (flag + guard ~2003-2012, runStatusline ~2098)
  planner.go                 MOD  cmdPlannerInit --host-parent (~79-146); plannerFilter passes CWD + OpencodeSession (~517-535)
  planner_test.go / main_test.go  MOD  flag-parsing cases only (see §7 step 6)
```

## 3. Data Structures

**`planner.OpencodeSession`** (`internal/planner/opencode_match.go`)

| field | type | meaning |
|---|---|---|
| `ID` | string | `session.id`, `ses_…` |
| `Directory` | string | `session.directory`, absolute |
| `ParentID` | string | `session.parent_id`; "" when NULL. Non-empty = a sub-agent session |
| `Title` | string | `session.title`, for the ambiguity message |
| `Updated` | time.Time | `session.time_updated` (Unix **milliseconds** in the db) |
| `Archived` | bool | `session.time_archived` is not NULL |

**Errors** (same file): `ErrNoOpencodeSession` (sentinel) and
`ErrAmbiguousOpencodeSession` (a struct type with `Dir string; Titles []string`,
`Error()` = `two OpenCode sessions are active in <Dir>: <t1>, <t2>; pass --planner`).

**`planner.ErrUnregisteredSession`** (`internal/planner/resolve.go`): struct
`{ Kind, SessionID string }`, `Error()` =
`no relevo planner for <kind> session <id>`, and
`Is(target error) bool` returns true for `ErrNoPlanner`, so every existing
`errors.Is(err, planner.ErrNoPlanner)` check keeps working.

**`ResolveInput` additions**: `CWD string` (the caller's working directory;
"" skips the opencode step) and
`OpencodeSession func(cwd string, now time.Time) (string, error)` (nil skips the
opencode step).

**`InitInput` addition**: `SessionOnly bool` -- look the caller up by
`(Kind, SessionID)` only, never by host.

**`relevo.Runtime` addition**: `OpencodeSession func(cwd string, now time.Time) (string, error)`;
nil when sqlite3 is not on PATH.

**`relevo.StatusLineRow`** (`internal/relevo/statusline.go`), JSON tags as shown:

| field | json | source |
|---|---|---|
| Name | `name` | `b.Name` |
| Round | `round` | `b.Round` |
| Display | `display` | `b.Display` |
| Harness | `harness` | `harnessSegment(b.BuilderCandidate)`, plus `"@" + b.Server` when `b.Server != ""` -- exactly the segment `RenderStatusLine` builds |
| Candidate | `candidate` | `b.BuilderCandidate` |
| Role | `role,omitempty` | `b.Role` |
| Waiting | `waiting` | `waiting(b)` |
| Clock | `clock` | `roundClock(b, now)` ("--" when the round has no start) |
| Tokens | `tokens` | `roundTokens(b)` ("" when none) |
| LastKind | `last_kind` | `string(b.LastPayload.Kind)`, "" when nil |
| LastTS | `last_ts` | `b.LastPayload.TS` RFC 3339 (UTC), "" when nil |
| Route | `route` | `b.PlannerRoute` |

**`relevo.StatusLineDoc`**: `Planner *StatusLinePlanner` (`planner`, null when
none) with `ID` (`id`) and `Name` (`name`); `Now` (`now`, RFC 3339 UTC);
`Rows []StatusLineRow` (`rows`, never null: `[]` when empty).

## 4. Interfaces and Contracts

```
// internal/planner/opencode_match.go
func MatchOpencodeSession(cwd string, sessions []OpencodeSession, now time.Time) (string, error)
```
Pure. Pre: `cwd` absolute (clean it with `filepath.Clean`). Post, in order:
1. Keep sessions with `ParentID == ""`, `!Archived`, and `Directory` equal to
   `cwd` or an ancestor of it (path-segment aware: `/a/b` is an ancestor of
   `/a/b/c`, not of `/a/bc`).
2. Of those, keep only the ones with the longest `Directory`.
3. None left → `ErrNoOpencodeSession`.
4. Sort by `Updated` descending. If there are ≥2 and the second's `Updated` is
   within 60 s of `now` (`now.Sub(second.Updated) <= 60*time.Second`) →
   `ErrAmbiguousOpencodeSession{Dir, Titles: [first.Title, second.Title]}`.
5. Else the first's `ID`.

```
// internal/relevo/opencode_session.go
type OpencodeSessionFinder struct { Exec usage.Exec; DBPath string; Timeout time.Duration }
func (f OpencodeSessionFinder) Find(cwd string, now time.Time) (string, error)
```
Runs `sqlite3 -readonly -json <DBPath> "select id, directory, parent_id, title,
time_updated, time_archived from session"` through `Exec.Run` with a context
bounded by `Timeout` (zero → 2 s), decodes the JSON array (empty output → no
rows), converts to `[]planner.OpencodeSession`, and returns
`planner.MatchOpencodeSession(cwd, sessions, now)`. Any Exec or decode error →
`fmt.Errorf("%w: %v", planner.ErrNoOpencodeSession, err)`. Never guesses.

```
// internal/planner/resolve.go -- Resolve, order unchanged: flag > env > host > session
```
Session step change only: after `ident, detected := Detect(env, in.PPID)`, if
`detected && ident.Kind == "opencode" && ident.SessionID == ""`:
- `in.OpencodeSession == nil || in.CWD == ""` → treat as not detected.
- call it; `ErrNoOpencodeSession` (errors.Is) → treat as not detected;
  `ErrAmbiguousOpencodeSession` or any other error → return it;
  success → `ident.SessionID = id`.
Then the existing `BySession(ident.Kind, ident.SessionID)`. When that returns
`ErrNotFound` **and** `ident.Kind == "opencode"`, return
`ErrUnregisteredSession{Kind: "opencode", SessionID: ident.SessionID}` instead
of `ErrNoPlanner`. Resolve still never creates a record.

```
// internal/planner/ident.go -- Detect
```
After the Claude check and the agy check, and only then:
`env("RELEVO_HARNESS") == "opencode"` → `Ident{Kind: "opencode"}, true`
(SessionID "", HostPID 0). Update the doc comment's sentence about opencode.

```
// internal/planner/hook.go -- findCaller
```
When `in.SessionOnly`, skip the `in.HostPID > 0` by-host block entirely. The
rest of Init is unchanged; `reattach` already replaces the host when
`in.HostPID > 0 && rec.HostPID != in.HostPID`.

```
// internal/relevo/bind.go -- resolveVerbPlanner(rt, ref)
```
Pass `CWD` (os.Getwd; on error "") and `OpencodeSession: rt.OpencodeSession`.
On `errors.As(err, &planner.ErrUnregisteredSession{})` with Kind "opencode":
`planner.Init(rt.Planners, planner.InitInput{Kind: "opencode", SessionID: id,
CWD: cwd, Now: now})` and return that record (found=true). An Init error is
returned as is. `ErrAmbiguousOpencodeSession` is returned as is (its text is the
fix). Every other path is unchanged.

```
// internal/relevo/statusline.go
func StatusLineRows(r Report, now time.Time) []StatusLineRow
```
One row per `r.Bindings` entry, in order, fields per §3. Shares `waiting`,
`roundClock`, `roundTokens`, `harnessSegment` with `RenderStatusLine`
(`internal/relevo/statusline.go`, ~l.140-180, as reshaped by #430); does not
change them.

## 5. Pseudocode

**`relevo status --line --json`** (`cmd/relevo/main.go`)
```
status flag parsing (~2003-2012; today it refuses `--json/--all/--name` with --line):
  if --line:
     if --all or --name or positional args: refuse exactly as today (exit 2),
        message "relevo: --line cannot be combined with --all/--name"
     return runStatusline(asJSON)
runStatusline(asJSON):
  if !asJSON: today's body, byte-for-byte unchanged
  else:
    drain stdin exactly as today
    rt := newRuntime(); on error: print {"planner":null,"now":…,"rows":[]} and return nil
    rec, ok := plannerFilter(rt)
    doc := StatusLineDoc{Now: rt.Now().UTC(), Rows: []}
    if ok: doc.Planner = {rec.ID, rec.Name}; rep, err := PlannerStatus(ctx, rt, rec.ID)
           if err == nil: doc.Rows = StatusLineRows(rep, rt.Now())
           else: stderr "relevo status --line: <err>" (rows stay [])
    print json.Marshal(doc) + "\n"; return nil
```
`--line --json` exits 0 in every case, like `--line`. Update the `--line` flag's
usage text to mention `--json`.

**`relevo planner init --host-parent`** (`cmd/relevo/planner.go` cmdPlannerInit)
```
new flag: hostParent := fs.Bool("host-parent", false,
  "with --kind/--session: record the calling process's parent as the host, and look the record up by session only")
if --host-parent without both --kind and --session: stderr error, exit 2
in the --kind/--session branch, when hostParent:
  in.HostPID = os.Getppid(); in.HostStartedAt = plannerHostStart(in.HostPID); in.SessionOnly = true
output unchanged (the "export RELEVO_PLANNER=…" line)
```

**Wiring** (`cmd/relevo/main.go` newRuntime literal, next to `ProcStart: procStartUnix` ~line 763): set
`OpencodeSession` to `relevo.OpencodeSessionFinder{Exec: binExec{}, DBPath:
opencodeDBPath()}.Find` only when `exec.LookPath("sqlite3")` succeeds (the same
check `newDeliverers` makes at ~line 555; `opencodeDBPath` is at ~line 494); else leave nil.
`plannerFilter` (`cmd/relevo/planner.go:517-535`): pass `CWD` (os.Getwd, "" on
error) and `OpencodeSession: rt.OpencodeSession`. `resolveMCPPlanner`
(`cmd/relevo/mcp.go:135`) and the doctor call (`cmd/relevo/doctor.go:705`) are
Claude-only paths: leave them unchanged.

## 6. Error Handling

| Error | Where it surfaces | Recoverable |
|---|---|---|
| `ErrNoOpencodeSession` | swallowed by Resolve (falls through to ErrNoPlanner) | yes: the user registers, or the TUI does |
| `ErrAmbiguousOpencodeSession` | returned by Resolve → printed by bind/add/fork/ask; `status --line` renders nothing, `--json` prints `planner: null` | yes: `--planner` |
| `ErrUnregisteredSession` | Resolve → resolveVerbPlanner registers; everywhere else behaves as ErrNoPlanner | yes |
| sqlite3 missing | `rt.OpencodeSession == nil` → opencode step skipped | yes |

No new log lines. No new exit codes.

## 8. Working Efficiently

Every model step costs a round trip. So:
- Batch the reads in one step. Everything you need is named above: `internal/planner/{ident,resolve,hook,opencode_match(new)}.go` and their tests (`resolve_test.go`: `TestResolveOrder` ~l.32, `TestResolveNeverCreates` ~l.222, `TestDetectOnlyClaude` ~l.251, `TestDetectAgy` ~l.291; `init_test.go`: `TestInitExplicitRegistrationHasNoHost` ~l.162), `internal/relevo/{bind,runtime,statusline,deliver_opencode}.go` (+ `bind_test.go` around its `Planners: reg` fixture ~l.63, `statusline_test.go`), `cmd/relevo/{main,planner}.go` at the line ranges given. Do not search for them again.
- Make every change to a file in one edit.
- Iterate with focused runs, fixing every error before the next:
  `go test ./internal/planner/ -run 'OpencodeSession|Detect|Resolve|Init' -count=1`
  `go test ./internal/relevo/ -run 'StatusLine|ResolveVerbPlanner|OpencodeSessionFinder|Bind' -count=1`
  `go test ./cmd/relevo/ -run 'Status|PlannerInit' -count=1`
- Run the full check once at the end: `make check` (gofmt over tracked files, go vet, go mod tidy check, tests).
- CI has no harness binary and no network: no `cmd/relevo` test may execute a subcommand that spawns a harness or reaches the network. The `cmd/relevo` tests in this round are flag-parsing only; the behaviour is tested as pure functions in `internal/planner` and `internal/relevo`. `cmd/relevo`'s TestMain already points HOME/XDG_* at a temp root; a test needing its own config sets XDG_CONFIG_HOME to a t.TempDir().

## 7. Ordered Implementation Steps

Each step: write the named tests first, watch them fail, implement, watch them pass.

1. **MatchOpencodeSession** (`internal/planner/opencode_match.go` + test).
   Tests (table, one function `TestMatchOpencodeSession`): exact dir; ancestor
   dir; longest match wins over a shorter ancestor; `/a/bc` is not under `/a/b`;
   child session (ParentID set) ignored; archived ignored; none → errors.Is
   ErrNoOpencodeSession; two within 60 s → ErrAmbiguousOpencodeSession naming
   both titles; two where the second is 61 s old → the newest id.
   *Done when* the table passes.
2. **Detect + Resolve** (`ident.go`, `resolve.go`). Tests in `resolve_test.go`:
   `TestDetectOpencodeMarker` (RELEVO_HARNESS=opencode → kind opencode; with
   CLAUDECODE=1 also set → claude; with a valid agy conversation id also set →
   agy); `TestResolveOpencodeSession` cases: finder returns an id with a record
   → that record, ResolutionSession; id without a record → errors.As
   ErrUnregisteredSession AND errors.Is ErrNoPlanner; finder returns
   ErrNoOpencodeSession → ErrNoPlanner; finder returns ambiguous → that error;
   nil finder or CWD "" → ErrNoPlanner without calling it; RELEVO_PLANNER set →
   env wins and the finder is never called. `TestResolveNeverCreates` must still
   pass. *Depends on 1.*
3. **SessionOnly** (`hook.go`). Test `TestInitSessionOnlyIgnoresHost` in
   `init_test.go`: register session A with host P, then Init session B with the
   same host P and SessionOnly → a second record (B), A untouched; Init A again
   with host Q and SessionOnly → A reattached with host Q.
4. **Finder + resolveVerbPlanner** (`internal/relevo/opencode_session.go`,
   `runtime.go`, `bind.go`). Tests: `TestOpencodeSessionFinder` with a fake
   `usage.Exec` returning (a) a JSON array with `time_updated` in ms and a null
   `parent_id`/`time_archived` → the right id, (b) empty output →
   ErrNoOpencodeSession, (c) an exec error → ErrNoOpencodeSession;
   `TestResolveVerbPlannerRegistersOpencodeSession` in `bind_test.go` (reuse its
   registry fixture): env RELEVO_HARNESS=opencode (t.Setenv), `rt.OpencodeSession`
   returns `ses_abc` → a new record with HarnessKind opencode and SessionID
   ses_abc is created and returned; a second call returns the same record id.
   *Depends on 2.*
5. **StatusLineRows** (`statusline.go`). Tests in `statusline_test.go`:
   `TestStatusLineRows` reusing the fixtures the RenderStatusLine tests build:
   a NEEDS YOU row with a report LastPayload → waiting/clock/last_kind/last_ts as
   RenderStatusLine words them; an open round with LiveUsage samples → tokens =
   "<n> tok"; a closed round with RoundUsage → its tokens; a remote row
   (Server set) → harness "<h>@<server>"; no RoundStart → clock "--";
   no payload → last_kind "", last_ts "";
   empty report → `[]` (not nil) once wrapped in StatusLineDoc and marshalled.
6. **cmd wiring** (`main.go`, `planner.go`). Tests, flag parsing only:
   `status --line --name x` still exits 2; `status --line --all` still exits 2;
   `planner init --host-parent` without `--kind/--session` exits 2. Do not add a
   test that runs `status --line --json` end to end if it would need a harness;
   it does not (it reads the store only), so one test may run it under the
   TestMain temp root and assert the output parses as a StatusLineDoc with
   `planner: null` and `rows: []`. *Depends on 2-5.*
7. **Full check**: `make check`. Then `git diff --stat` must list only §2's files.
   Commit: `feat(planner): OpenCode sessions resolve and register as planners; status --line --json (#393)`.

## Report

Per step: status and the test names added. Paste `make check`'s tail and
`git diff --stat`. For step 2, state which existing test(s), if any, you had to
touch and why -- none is expected.
