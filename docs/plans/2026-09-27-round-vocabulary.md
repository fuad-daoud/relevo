```relevo-plan
# The round vocabulary: prompt in, output out

Owner decisions, 2026-09-27, after the MasterMind rename landed (`e80f74a`). This plan
covers exactly one builder round. Read CLAUDE.md and the code first: a round's **input is
its prompt**; a **plan** is what a planner writes and a builder executes.

Commit this plan as `docs/plans/2026-09-27-round-vocabulary.md` in this round's commit
(last step). Halt and report instead of improvising if a step is impossible as written or
contradicts the code. Never delete a test to get green; retarget it. Where a line below names
two spellings, both are required: the new one for new entries, the old one for state already
written. Line numbers are HEAD's (`e80f74a`); if one moved, find the named function.

## 1. System Overview

The word `plan` currently names a round's input file, its log kind, the `show` section, the
UI tab and the dry-run fields. This round makes the round's input its **prompt**:

- new rounds stage `NNN-prompt.md` (readers fall back to `NNN-plan.md`);
- new log entries carry kind `prompt` (readers match the historical `plan` through one
  helper, because `KindPlan` gates behaviour on live bindings);
- `relevo show --prompt` is the section and the flag, default included; `--plan` becomes an
  unknown flag;
- the pane's `plan` tab and the log line's kind word follow; so does the statusline phase
  word (`prompt sent`);
- the dry-run fields and the dry-run's file label follow.

A reader round's artifact is named after its output label: `plan.md` (architect, so `planner`
and `lite-planner`), `findings.md` (reviewer), `notes.md` (researcher). `summary.md` retires
for new rounds and stays as the read fallback. `relevo show --summary` becomes `--output`,
and the close line's reader arm becomes `<Label>: relevo show <name> --round <n> --output`;
the writer's `Report: … --report` arm stays byte-identical. The reader prompt's "Write every
file you produce into this directory" sentence goes: the actor writes nothing, relevo saves
the final message.

Nothing already written is rewritten: old `NNN-plan.md` files, `kind: plan` log entries,
`summary.md` rows and sealed files keep working through read fallbacks. The database is the
one exception: migration `009` rewrites the artifact kind (item 5), because the db is
relevo's own and has a migration path.

Two follow-ons the owner's list implies. Do not re-open them; say them in the report:

- **S1 — the dry run's second `prompt` line.** The staged-file line's label `plan` becomes
  `prompt`. The line under it that shows the prompt's first lines (today also labelled
  `prompt`) becomes `head`: one label per fact; it renders `DryRun.PromptHead`, whose JSON
  key `prompt_head` does not change.
- **S2 — the statusline phase word.** `view.phase` returns `prompt sent` for a prompt
  payload, old or new kind, and `no prompt yet` when there is no payload; `no plan yet`
  would name the input by the wrong word. Leaving it `plan sent` would contradict this round's vocabulary;
  leaving the new kind unmatched would drop the phase for every new round.

**Scope fence.** The word `plan` stays wherever a *plan* is what it names: the `planner` /
`lite-planner` actors, the `architect` and `plan-executor` agents, `docs/plans/`, the usage
lane and cost basis (`usage.Spend.Plan`, `internal/usage/format.go:14`), the send picker's
"plan file" field, `RetryPlan`'s name, and the
shipped agent definitions (the tool/definition half is the next round). The wire spellings
also stay: the multipart field `plan` (`internal/remote/client/rounds.go:78`,
`internal/serve/rounds.go:58`) and the serve round-file kind `plan`
(`internal/serve/roundfiles.go:26`) are protocol between separately shipped sides; both map
through `PromptPath`, so an old and a new file both answer.

## 2. File Structure

New:

```
internal/db/migrations/009_prompt_artifact_kind.sql   the artifact kind rewrite (item 5)
cmd/relevo/testdata/contract/show-prompt.golden       renamed from show-plan.golden (Section "prompt")
```

Moved (`git mv`): `cmd/relevo/testdata/contract/show-plan.golden` → `show-prompt.golden`.

Edited by area (test files follow their package's code in the same step):

```
internal/store/log.go            KindPrompt, IsPromptKind; KindPlan deleted; DirToBuilder kept
internal/store/paths.go          PromptPath (old-file fallback), OutputPath; PlanPath/SummaryPath deleted
internal/store/*_test.go         retargeted; new PromptPath and OutputPath cases
internal/db/types.go             ArtifactPrompt = "prompt"; ValidArtifactKind follows
internal/db/migrations/009_…sql  UPDATE artifact SET kind='prompt' WHERE kind='plan'
internal/db/{migrate_test,testdata/schema.golden}   version-9 case; regenerated golden
internal/ingest/{facts,dedupe}.go, *_test.go        prompt case; sealed base tries prompt.md then plan.md
internal/relevo/send.go          KindPrompt writes; "read prompt %s"; DryRun.PromptPath/From/Bytes; readerPrompt; composePrompt arg
internal/relevo/repair.go        KindPrompt; PromptPath
internal/relevo/remote_send.go   KindPrompt; PromptPath; "write prompt %s"
internal/relevo/queue.go         PromptPath in composePrompt
internal/relevo/switch.go        PromptPath in composePrompt
internal/relevo/headless.go      PromptPath; reopen gates via IsPromptKind/HasPromptEntry; "reader output not written"
internal/relevo/wait.go          DefaultWaitRound + WaitOutcome via IsPromptKind/HasPromptEntry
internal/relevo/nudge.go         IsPromptKind
internal/relevo/builder_change.go  IsPromptKind, HasPromptEntry
internal/relevo/served.go        HasPromptEntry
internal/relevo/remote_gates.go  HasPromptEntry
internal/relevo/remote_sync.go   HasPromptEntry
internal/relevo/status.go        PlanRound scan via IsPromptKind
internal/relevo/logline.go       tier suffix via IsPromptKind
internal/relevo/show.go          ShowPrompt/ShowOutput; PromptPath; readOutput; showSections output param
internal/relevo/artifacts.go     RoundArtifacts output param; output-first sort; readOutput; fallback
internal/relevo/actoroutput.go   OutputFile (exported)
internal/relevo/summary.go       reportPathFor -> OutputPath; writeReaderSummary doc/behaviour
internal/relevo/closeline.go     reader arm --output; writer arm byte-identical
internal/relevo/text.go          dry-run labels: prompt + head
internal/relevo/{stop,reconcile}.go  warn text and comments; close-line call sites
internal/relevo/retry.go         read via PromptPath
internal/relevo/verify.go (consult)  verifyQuestion via PromptPath
internal/relevo/*_test.go        retargeted; new close/fallback/prompt cases
internal/view/status.go          IsPayloadKind + RoundFacts via IsPromptKind; comments
internal/view/statusline.go      phase -> "prompt sent" via IsPromptKind, "no prompt yet" for no payload
internal/ingest/facts.go         plan timestamp via IsPromptKind
internal/ui/fetch.go             tabPrompt/fetchPrompt, tabTitles "prompt", PromptPath, artifactOutput, OutputFile
internal/ui/view_log.go          event kind matches prompt and plan
internal/ui/round_pane.go        source line "prompt rN"
internal/ui/artifacts.go         artifactCaption marks the output and summary.md
internal/ui/*_test.go, testdata/*.golden   retargeted and regenerated
cmd/relevo/show.go               --prompt/--output flags, usage, conflict text, default ShowPrompt
cmd/relevo/serve_show.go         usage line
cmd/relevo/bind.go               rebound hint path via PromptPath
cmd/relevo/contract_test.go, show_test.go, read_verbs_test.go, owner_reads_test.go, log_test.go
internal/serve/{roundfiles,rounds}.go, internal/remote/client/*  PromptPath (wire spellings kept)
internal/delivery/contract_test.go  KindPrompt
internal/e2e/headless_test.go    fake reader parses the saved-as path
internal/e2e/reader_test.go      reviewer output = findings.md, close says --output
README.md, docs/design.md, claude-plugin/commands/show.md   live docs follow the new spellings
docs/plans/2026-09-27-round-vocabulary.md                   this plan, committed
```

## 3. Data Structures & Type Definitions

### 3.1 `store.Kind` and the prompt predicate (`internal/store/log.go`)

```
KindPrompt Kind = "prompt"        // new entries write this; the value is persisted
kindPlanLegacy Kind = "plan"      // unexported: the value already written, never written again
func IsPromptKind(k Kind) bool    // k == KindPrompt || k == kindPlanLegacy
```

`KindPlan` is deleted, not aliased: every write site must fail to compile until it writes
`KindPrompt`. A test that needs the historical value writes `store.Kind("plan")` (or a local
const) explicitly, with a why comment only where the "why" is not obvious.

### 3.2 store paths (`internal/store/paths.go`)

```
func (s *Store) PromptPath(name string, round int) string
func (s *Store) OutputPath(name string, round int, actor, label string) string
```

- `PromptPath` mirrors `StreamPath` (paths.go:160-175): the new
  `<state>/<name>/NNN-prompt.md` when it exists on disk or as a sealed row; else the
  pre-rename `NNN-plan.md` when that exists; else the new name. Total: a miss and an error
  are both not-found. Writers stage through it, so a resend into a round that already has the
  old file keeps writing that one file; readers always find exactly one.
- `OutputPath` is `ArtifactDir(name, round, actor)/<label>.md`; `label` is the actor's
  resolved output label (`ActorOutput`/`readerOutputLabel`), never empty.
- `PlanPath` and `SummaryPath` are deleted. The pre-rename `summary.md` is a *rel* the read
  helpers fall back to, not a path helper; a caller that needs its path builds
  `filepath.Join(ArtifactDir(...), "summary.md")`.

### 3.3 Artifact listing (`internal/relevo/artifacts.go`)

```
func OutputFile(rt Runtime, b store.Binding) string          // readerOutputLabel(rt,b) + ".md"; exported for the ui
func RoundArtifacts(rt Runtime, name string, round int, actor, output string) ([]ArtifactFile, error)
func roundArtifacts(st *store.Store, name string, round int, actor, output string) ([]ArtifactFile, error)
func sortArtifacts(files []ArtifactFile, output string)
func readOutput(rt Runtime, name string, round int, actor, output string) ([]byte, error)
```

`output` is the output rel, e.g. `"findings.md"`. The sort puts it first, then the fallback
`"summary.md"`, then by Rel. `output == ""` (the artifact-cap size sum, which only needs a
total) sorts `summary.md` first as today. `ArtifactFile`, `ArtifactLine` and `ReadArtifact`
are unchanged; `ReadArtifact` passes `""` because a membership check does not care about
order. `readOutput` replaces `readSummary`: `output` when the listing holds it, else
`summary.md`, else nil.

### 3.4 DryRun (`internal/relevo/send.go:675-692`)

```
PromptPath  string `json:"prompt_path"`
PromptFrom  string `json:"prompt_from"`
PromptBytes int64  `json:"prompt_bytes"`
```

`preflight.planPath` → `promptPath`; `PlanPath`/`PlanFrom`/`PlanBytes` are deleted.

### 3.5 Show sections (`internal/relevo/show.go:28-39`)

```
ShowPrompt ShowSection = "prompt"
ShowOutput ShowSection = "output"
```

`ShowPlan` and `ShowSummary` are deleted. `ShowOptions`/`ShowResult` shapes are unchanged;
`ShowResult.Section` (JSON `section`) prints the new values.

### 3.6 UI tab content (`internal/ui/fetch.go:46-71`)

`tabContent` gains `artifactOutput string`, the rel `OutputFile` names. `tabPlan`,
`fetchPlan` become `tabPrompt`, `fetchPrompt`.

## 4. Interface Definitions & Component Contracts

### 4.1 store

- `IsPromptKind` is the only place the historical `"plan"` value is named. Every reader that
  asks "is this round's prompt" calls it: `internal/relevo/{wait,nudge,builder_change,
  headless,served,remote_gates,remote_sync,status,show,logline}.go`,
  `internal/view/{status,statusline}.go`, `internal/ingest/facts.go`,
  `internal/ui/{fetch,view_log}.go`. No site compares to `KindPrompt` alone for that
  question, and none compares to `"plan"` alone.
- `HasPromptEntry(entries []store.LogEntry, round int) bool` (new, in
  `internal/relevo/builder_change.go` beside `roundOpenIn`): true when a `DirToBuilder` entry
  of round is a prompt and its Note is not `nudgeNote` — exactly `HasEntry`'s exclusion
  (reconcile.go:653). The six open-round gates call it: `wait.go` WaitOutcome's not-started
  arm, `builder_change.go:71`, `headless.go:604`, `served.go:25`, `remote_gates.go:172`,
  `remote_sync.go:228`. `DefaultWaitRound` (wait.go:77-88) uses `IsPromptKind` in its own
  max-round loop.
- Writers append `KindPrompt`: `send.go:568`, `repair.go:146`, `remote_send.go:187`.
  `send.go:799` passes it to `OriginLine`; the to-builder origin text does not change.
- Every `store.PlanPath` call site becomes `store.PromptPath` (step 4): `send.go:267,484`,
  `queue.go:37`, `switch.go:174`, `headless.go:882`, `repair.go:115-116`, `remote_send.go:178`,
  `show.go:279`, `retry.go:17`, `ui/fetch.go:145`, `consult/verify.go:240`,
  `serve/roundfiles.go:27`, `serve/rounds.go:29`, `cmd/relevo/bind.go:276`.

### 4.2 Database (migration 009, the one rewrite)

`internal/db/migrations/009_prompt_artifact_kind.sql`: one statement,
`UPDATE artifact SET kind = 'prompt' WHERE kind = 'plan';`, with a file comment saying the
artifact kind follows the file name and the db is the one place stored bytes may be
rewritten. The version guard (migrate.go:150-161) runs it once; no new table or index, so
`internal/db/testdata/schema.golden` changes only in its last line (`schema_versions have=9
know=9`). `db.ArtifactPlan` → `db.ArtifactPrompt = "prompt"`; `ValidArtifactKind` follows.
`showDB` passes the section value (`"prompt"`), so the rewrite leaves no reader needing a
fallback.

`internal/ingest/dedupe.go`: the prompt artifact's sealed round-file base is `prompt.md` for
a round sealed after this round and `plan.md` for one sealed before it. `planArtifacts`
(tries the round file for the kind) tries both names, exactly as `sealedStream`
(dedupe.go:339-350) tries both stream names. No other ingest path writes or reads the
artifact kind; the event rows keep their stored kind, so the mirror's old `"plan"` rows stay
readable through `IsPromptKind`.

### 4.3 `relevo show`

- `showSections` (show.go:275) gains an `output string` parameter (the resolved reader
  label). `ShowPrompt` reads `read(rt.Store.PromptPath(name, round))`; `ShowReport` on a
  reader and `ShowOutput` read `readOutput(rt, name, round, actor, output)`; every other
  section is unchanged; `showDB`'s switch renames its two cases.
- `showLive` (show.go:202) and `showArchived` (show.go:364) pass
  `readerOutputLabel(rt, b)` / `readerOutputLabel(rt, ab.Binding)`.
- CLI (`cmd/relevo/show.go`): `--prompt` (default) and `--output` registrations;
  `showSectionArgs.prompt/output`; the conflict message and both usage lines name only the
  current flags; `serveShowUsage` follows. `--plan` and `--summary` are unregistered, so the
  flag package answers `flag provided but not defined: -plan` / `-summary`, exit 1, like
  every retired spelling.
- `ErrNoCompletedRound` (show.go:19) says "an open round's prompt"; a missing section still
  prints `no <section> for round N` with the new names.

### 4.4 Reader output and close

- `reportPathFor` (summary.go:15): reader arm
  `rt.Store.OutputPath(b.Name, b.Round, bindingRole(b), readerOutputLabel(rt, b))`.
- `writeReaderSummary` (summary.go:33): unchanged logic, but it stats and writes the output
  path. A runner that wrote its own output file keeps it; a runner that wrote only
  `summary.md` does not — relevo writes `<label>.md` from the stream's final message and the
  old file stays an extra artifact. The caller logs "reader output not written" on failure
  (headless.go:749, reconcile.go:374, stop.go:244).
- `artifactClause` (closeline.go:15): reader arm `showCommand(name, round, "output")`, the
  label title-cased as today; writer arm byte-identical.
- `readerPrompt` (send.go:62-80): the "Write every file you produce into this directory
  (create it): %s" sentence goes; the `changed_paths` block comment becomes
  `# files you changed in the throwaway tree`; the saved path is the output path.
  `readerPromptFor` (send.go:812) passes the same label `reportPathFor` records, so the
  prompt and the close cannot drift.

### 4.5 UI

- `tabTitles[tabPrompt] = "prompt"` (fetch.go:31); `writerTabs`, `readerTabs`,
  `sectionForTab`, `tabForSection`, `fetchFor`, `detail.go:79` follow the identifier rename.
- `fetchPlan` prose becomes `no prompt for round N`; its log-time scan uses
  `store.IsPromptKind`.
- `round_pane.sourceLine` (round_pane.go:420-425) says `prompt rN` / `prompt rN · HH:MM`.
- `view_log.go`'s event case matches both `"prompt"` and `"plan"` through
  `store.IsPromptKind(store.Kind(e.Kind))`; Word stays `sent`.
- `fetchArtifacts` (fetch.go:649) computes `output := relevo.OutputFile(rt, b)` before the
  listing, passes it to `RoundArtifacts`/`ReadArtifact`, and sets `content.artifactOutput`.
  `artifactCaption` (ui/artifacts.go:121) marks `artifactOutput` and `summary.md` — never any
  other rel — as the final message.

### 4.6 Statusline

`view.phase` (statusline.go:162-177) returns `prompt sent` for `store.IsPromptKind`, and
`no prompt yet` when `LastPayload` is nil; `report in`, `question in` and `answered` are
unchanged.

## 5. High-Level Pseudocode

```
PromptPath(name, round):
    new = <dir>/NNN-prompt.md ; old = <dir>/NNN-plan.md
    if StatFile(new) present: return new        # on disk or a sealed row
    if StatFile(old) present: return old
    return new                                  # the name a fresh round writes

RoundArtifacts(rt, name, round, actor, output):
    rels = sealed-or-live listing of NNN-<actor>/
    order: output first, then "summary.md", then Rel

readOutput(rt, name, round, actor, output):
    files = RoundArtifacts(rt, name, round, actor, output)
    for rel in [output, "summary.md"]:
        if rel in files: return ReadArtifact(rel)   # missing, sealed or archived all answer
    return nil, nil

show section -> read:
    prompt -> read(PromptPath(name, round))
    report -> reader: readOutput(...) ; writer: read(ReportPath)
    output -> readOutput(...)

close a reader round:
    path = OutputPath(name, round, actor, label)
    if path missing and the stream's final message is non-empty: write it
    report entry Path = path, payload clause = "<Label>: relevo show <name> --round N --output"

ui artifacts tab:
    output = OutputFile(rt, b)                    # e.g. "findings.md"
    files  = RoundArtifacts(rt, name, round, actor, output)
    caption = output, else summary.md -> "<rel> · the <actor>'s final message"

db open -> applyMigrations:
    009 in one transaction: UPDATE artifact SET kind='prompt' WHERE kind='plan'
```

## 6. Error Handling Strategy

- No new error types. A retired flag is the flag package's `flag provided but not defined`,
  exit 1. `relevo show`'s conflict message names only current flags.
- A missing prompt answers `Missing` as today ("no prompt for round N"); a missing output
  answers "no output for round N"; neither is an error.
- The migration is one transaction: a failure rolls it back and `open` fails; a second open
  is a no-op.
- Halt and report, do not improvise, if: a test pins `--plan`, `--summary`, `KindPlan`,
  `PlanPath`, `SummaryPath` or `db.ArtifactPlan` outside this plan's listed retargets; a
  golden diff carries bytes this plan does not name; a legacy round's evidence
  (`kind:"plan"`, `NNN-plan.md`, `summary.md`) stops answering a read; or
  `internal/store/testdata/` or `internal/db/testdata/schema.golden` changes beyond the
  `schema_versions` line.

## 7. Working Efficiently

- Two mechanical sweeps, scripted, never hand-edited; each runs exactly once:
  1. kind: `git grep -l 'KindPlan' -- '*.go' ':!internal/store/log.go' | xargs sed -i 's/KindPlan/KindPrompt/g'`,
     then hand-edit `internal/store/log.go` (add `KindPrompt`/`kindPlanLegacy`/`IsPromptKind`;
     delete `KindPlan`), then hand-edit the §4.1 matcher list to `IsPromptKind`/`HasPromptEntry`.
  2. path: `git grep -l 'PlanPath' -- '*.go' ':!internal/store/paths.go' | xargs sed -i 's/PlanPath/PromptPath/g'`,
     then hand-edit `internal/store/paths.go` (keep the new `PromptPath`, delete the old
     `PlanPath`). `SummaryPath` gets no sweep: each of its ~10 call sites needs the label.
  `ArtifactPlan` → `ArtifactPrompt` is the same shape, one sed plus `internal/db/types.go`.
- Read once, from these locations, in one parallel batch before editing:
  `internal/store/{log,paths,seal}.go`; `internal/db/{types,migrate,write}.go`;
  `internal/ingest/{facts,dedupe}.go`; `internal/relevo/{send,show,artifacts,summary,closeline,text,wait,headless}.go`;
  `internal/ui/{fetch,artifacts,view_log,round_pane}.go`; `cmd/relevo/{show,serve_show,bind}.go`;
  `internal/view/{status,statusline}.go`; `internal/serve/{roundfiles,rounds}.go`;
  `internal/e2e/{headless_test,reader_test}.go`.
- One edit call per file. Goldens are regenerated, never hand-edited, then read.
- Focused loop, every reported error fixed before the next run:
  `go build ./... && go test ./internal/store/ ./internal/db/ ./internal/ingest/ -count=1`
  `go test ./internal/relevo/ ./internal/view/ ./internal/delivery/ ./internal/serve/ ./internal/consult/ -count=1`
  `go test ./cmd/relevo/ -count=1` · `go test ./internal/ui/ -count=1`
- Goldens: `go test ./cmd/relevo -run Contract -update`, `go test ./internal/ui -run Golden -update`,
  `go test ./internal/db -run Contract -update`; then `git diff -- '**/*.golden'` and confirm every
  changed line is one of this plan's renames (a tab title, a source line, a section value, a
  caption, a file name), never a fixture's plan body.
- Full check once, at the end: `make check`, then `make e2e`, then `gofmt -l .` (prints
  nothing). No package moves this round, so the coverage baseline is **not** regenerated; a
  coverage failure means a package lost a point and needs a test, not a lower bar.
- No test may spawn a harness or reach the network; `internal/e2e` uses the fake `claude` on
  PATH, so the reader fake must find its artifact directory in the prompt the new way.

## 8. Ordered Implementation Steps

1. **store: the predicate and the two paths (§3.1, §3.2).** `internal/store/log.go`,
   `internal/store/paths.go`, `internal/store/*_test.go`. Deliverable: `KindPrompt`,
   `kindPlanLegacy`, `IsPromptKind`, `PromptPath`, `OutputPath` exist and are pinned; the old
   `KindPlan`/`PlanPath`/`SummaryPath` still exist (deleted in steps 3, 4, 6) so the tree
   still builds. New cases: neither prompt file → the new name; only `NNN-plan.md` on disk →
   it; only a sealed `NNN-plan.md` row → it; both → the new name
   (`internal/store/paths_stream_test.go` is the pattern). Verify:
   `go test ./internal/store/ -count=1`.
2. **db and ingest: the artifact kind (item 5, §4.2).** Add
   `internal/db/migrations/009_prompt_artifact_kind.sql`; `ArtifactPlan` →
   `ArtifactPrompt`; `ValidArtifactKind`; the dedupe dual base; regenerate
   `internal/db/testdata/schema.golden`. New migration case (pattern
   `internal/db/mastermind_test.go`): a schema-8 db holding one artifact row `kind='plan'`
   → after Open the row is `prompt`; a second open is a no-op; `migrationFilesUpTo(t, 8)`.
   New dedupe case: a `prompt` row whose sealed file is `NNN-plan.md` is deleted like a
   stream row would be. Verify: `go test ./internal/db/ ./internal/ingest/ -count=1`;
   `git diff internal/db/testdata/schema.golden` is only the `schema_versions have=9 know=9`
   line. Mutation-check: drop the UPDATE from 009, confirm the migration test fails, restore.
3. **the log kind (item 4, §4.1).** Run sweep 1, delete `KindPlan`, hand-edit the matcher
   list, add `HasPromptEntry`, switch the writers. Retarget every test that builds entries:
   a test whose point is the legacy value writes `store.Kind("plan")`; every other
   `KindPlan` becomes `KindPrompt`. New cases: a round whose only to-builder entry carries
   `"plan"` still reads open (`WaitOutcome` not 6; `roundOpenIn` true); a new send's entry
   carries `prompt`. Verify: `go build ./... && go test ./internal/relevo/ ./internal/view/
   ./internal/delivery/ ./internal/serve/ ./internal/consult/ ./internal/ingest/ -count=1`.
   Mutation-check: drop the legacy arm of `IsPromptKind`, confirm a named open-round test
   fails, restore.
4. **the prompt path and `show --prompt` (items 2, 3, §4.3, §3.4).** Run sweep 2; delete
   `PlanPath`; rename `ShowPlan` → `ShowPrompt` (`show.go`, `cmd/relevo/show.go`,
   `serve_show.go`, `ui/fetch.go`'s one section mapping); `--plan` → `--prompt` (default);
   usage and conflict text; `git mv` the contract golden and update
   `cmd/relevo/contract_test.go`'s args and seed. Retarget `cmd/relevo/show_test.go`'s
   default-section and `--plan --report` cases; add `--plan` → `flag provided but not
   defined` (pattern `cmd/relevo/main_test.go:1096`). Verify: `go test ./cmd/relevo/
   ./internal/relevo/ ./internal/serve/ ./internal/consult/ ./internal/ui/ -count=1`.
5. **the UI tab and words (item 1, §4.5, §4.6).** Rename `tabPlan`/`fetchPlan` (sweep, it
   is mechanical), set `tabTitles[tabPrompt] = "prompt"`, the source line, the event case,
   the statusline phase word, prose. Regenerate the UI goldens; the diff is tab titles,
   source lines and the reader artifact fixture, nothing else. New cases: reader and writer
   tab bars show `prompt`; a cockpit event row for both kinds reads `sent`; the statusline
   phase for both kinds reads `prompt sent`, and a payload-less statusline reads `no prompt
   yet`. Verify: `go test ./internal/ui/ ./internal/view/
   -count=1`, `git diff internal/ui/testdata/` read line by line.
6. **the reader's output (item 6, §3.3, §4.4).** Rename `SummaryPath` call sites to
   `OutputPath` with the label; delete `SummaryPath`; add `OutputFile`; `readOutput`;
   `RoundArtifacts`/`sortArtifacts` output param and `fetchArtifacts` wiring; `reportPathFor`
   and `writeReaderSummary`; `ShowSummary` → `ShowOutput` with `--output` and the golden;
   the reader prompt sentence (§4.4); the e2e fake's parse and the reader e2e expectations
   (`findings.md`, `--output`, sealed name). New cases: the close writes `<label>.md`, records
   it, and does not write `summary.md`; a round sealed with only `summary.md` answers
   `--output` and is captioned as the final message; a runner-written output is never
   overwritten (retarget the two `KeepsARunnerWrittenSummary`-style tests to the output rel);
   the reader prompt contains no artifact-directory sentence and names `<label>.md`. Verify:
   `go build ./... && go test ./internal/relevo/ ./internal/e2e/ ./internal/ui/ ./cmd/relevo/
   ./internal/store/ -count=1`, then `make e2e`.
7. **the dry run (item 3, §3.4, S1).** `PromptPath`/`PromptFrom`/`PromptBytes` and their
   JSON tags; `preflight.promptPath`; `composePrompt`'s parameter name; `text.go`'s file
   label `prompt` and head line `head`; `TestRenderDryRunShape` and the send dry-run tests;
   README's sample. Verify: `go test ./internal/relevo/ -run 'DryRun|Prompt|Send' -count=1`.
8. **live docs.** README (lines 328, 902, 1082-1083, 1425, 1765-1777, 1830-1835, 2200 at
   HEAD), `docs/design.md` 195-205, `claude-plugin/commands/show.md`. The reader section
   teaches `<label>.md` (`plan.md` for the architect, `findings.md` for the reviewer,
   `notes.md` for the researcher) with `summary.md` named only as the old fallback. Verify:
   `git grep -n -- '--plan\b\|--summary\b' README.md docs/design.md claude-plugin/` finds
   nothing; `git grep -c 'summary.md' README.md` counts only fallback sentences.
9. **full check, e2e, commit.** `make check`, `gofmt -l .`, `make e2e`. Copy this plan to
   `docs/plans/2026-09-27-round-vocabulary.md`; one commit with the code, tests, goldens,
   docs and the plan. Verify: `make check` and `make e2e` green; `git diff --stat` lists only
   §2's files plus the plan; no coverage-baseline change (no package moved).

### Closed list of what is deleted (everything not on this list survives)

1. `internal/store/log.go`'s `KindPlan` constant, replaced by `KindPrompt` (the historical
   value lives only in `kindPlanLegacy`/`IsPromptKind`).
2. `internal/store/paths.go`'s `PlanPath` (→ `PromptPath`) and `SummaryPath` (→
   `OutputPath` with a label).
3. `cmd/relevo/show.go`'s `--plan`/`--summary` registrations, and `internal/relevo/show.go`'s
   `ShowPlan`/`ShowSummary`, replaced by `--prompt`/`--output` and `ShowPrompt`/`ShowOutput`.
4. `internal/db/types.go`'s `ArtifactPlan`, replaced by `ArtifactPrompt`.
5. The reader prompt's "Write every file you produce into this directory (create it): %s"
   sentence and the `changed_paths` comment's "files you wrote into the artifact directory".
6. `internal/ui`'s `tabPlan`/`fetchPlan` identifiers, replaced by `tabPrompt`/`fetchPrompt`.
7. `internal/ui/artifacts.go`'s caption rule that only `summary.md` can be the final message,
   replaced by output-or-`summary.md`.

No test is deleted. Every test that asserted one of these is retargeted to the new behaviour
and keeps every other assertion it had; a test that simulates state already written keeps
using the legacy spelling explicitly.

### Report must include

- `git diff --stat`; both sweeps' residue: `git grep -n 'KindPlan\|PlanPath\|SummaryPath'`
  (only `store.Kind("plan")` legacy tests and comments may remain).
- The mutation checks of steps 2 and 3, each with the failing test's name.
- Every golden whose bytes changed, one line each, and confirmation that its diff is only a
  rename (no fixture plan body changed).
- `make check` and `make e2e` results, and the explicit statement that the coverage baseline
  did **not** move because no package moved.
- The S1 and S2 decisions, and anything from §1's scope fence that a reader might expect here
  but did not change.
```
