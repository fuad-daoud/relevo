# Cockpit config views, round 1: the backend (2026-09-25)

Branch `relevo/ck-config`. This is the first of four rounds. Make **one commit**; later rounds amend it.

## 1. System overview

The cockpit is getting three config screens, `:candidates`, `:actors` and `:agents`, that **write** config. This round
builds only what those screens call. Everything here is a pure function or a thin store wrapper, and every piece is
unit-tested. **No `internal/ui` change in this round.**

1. `internal/relevo/configedit.go` is a set of config-edit operations, one per thing the user can do:
   - add, edit or delete a candidate;
   - set an actor's entries, edit an actor, add or delete an actor;
   - delete a custom agent.

   Each operation takes the current config and returns a validated `ConfigEdit`: the changed sections plus a revision
   message. A write helper stores it as one revision, and a reload helper refreshes a `Runtime`.
2. `internal/harness` gets:
   - a per-kind **provider allow-list** (`Harness.Providers`);
   - `PinnedModel`, **moved** from `internal/doctor`;
   - `AgentFiles`/`ResetAgentFile`, the per-harness state of one agent's definition file.

Facts this plan relies on (origin/main `5d7d67e`):
- `config.Store.PutDoc(map[Section]json.RawMessage) ([]string, error)` validates each section and writes all of them
  in one transaction with one revision (`internal/config/config.go:391`). `Store.As(source, message)`
  (`revision.go:28`) labels that revision.
- `config.Validate` checks one section in isolation. The cross-section rules live only in `actors.ToRolesFile`
  (`internal/actors/convert.go:17`) and `roles.Build`:
  - an actor's agent must exist;
  - a builtin actor name (`builder`, `reviewer`, `researcher`) must keep its builtin shape;
  - `check:true` is refused on a reader;
  - the tier must be within `max_tier`.

  So an edit must **dry-run** those before it writes.
- `candidate.DeriveNames` (`internal/candidate/names.go:38`) derives a name for every entry whose `Name` is empty and
  reserves every explicit name and every provider.
- An actor entry references a candidate by name or by `harness/provider/model` token (`actors.Entry`,
  `internal/actors/actors.go:60`). An empty candidate list is valid (`roles/file.go:65`).
- `harness.Install(env, InstallOptions{Kind, Role, DryRun: true})` (`internal/harness/install.go:96`) reports one
  `InstallResult` per shipped definition, with these outcomes:
  - `would write`: absent;
  - `kept (identical)`: up to date;
  - `would update`: stale;
  - `kept (differs; --force to overwrite)`: the user's edit.

  `Force: true` overwrites. It covers shipped agents only (`plan-executor`, `reviewer`, `researcher`, `architect`).
  relevo never writes custom agents, so this round gives them no file operation.
- The model-pin readers `pinnedModel`, `frontmatterModel` and `tomlTopLevelModel` live in
  `internal/doctor/doctor.go:265-330`, and they are unexported.

## 2. File structure

```
internal/harness/
  harness.go        Harness gains `Providers []string`; set on agy and claude in knownHarnesses (line ~131)
  pin.go            NEW: PinnedModel + frontmatterModel + tomlTopLevelModel, moved verbatim from internal/doctor
  pin_test.go       NEW: the doctor tests of those three helpers, moved
  agentfiles.go     NEW: FileState, AgentFile, AgentFiles, ResetAgentFile
  agentfiles_test.go NEW
internal/doctor/
  doctor.go         deletes the three helpers; its calls become harness.PinnedModel
  doctor_test.go    loses the moved tests (and nothing else)
internal/relevo/
  configedit.go     NEW: ConfigDoc, FieldError, ConfigEdit, ActorSlot, the operations, WriteConfigEdit, ReloadConfig
  configedit_test.go NEW
docs/plans/2026-09-25-cockpit-config-r1-backend.md   this plan, copied in as the last step
```

## 3. Data structures

### `internal/harness`

```go
// Providers is the closed list of providers a candidate on this kind may name,
// in display order; nil means any provider. Only the cockpit's candidate form
// enforces it: candidate.Parse does not, so an existing config keeps loading.
Providers []string
```

- agy: `[]string{"google", "agy-extra"}`.
- claude: `[]string{"anthropic"}`.
- codex and opencode: nil.

```go
type FileState string
const (
    FileUpToDate FileState = "up to date"   // InstallResult outcome "kept (identical)"
    FileStale    FileState = "stale"        // "would update"
    FileEdited   FileState = "your edit"    // "kept (differs; --force to overwrite)"
    FileMissing  FileState = "missing"      // "would write"
)
type AgentFile struct {
    Kind  string    // harness kind
    Path  string    // absolute path, from the InstallResult
    State FileState
    Model string    // PinnedModel of the file on disk; "" when missing or unpinned
}
```

Use the outcome constants already in `install.go` (`OutcomeKeptIdentical` and the rest). Do not compare strings.

### `internal/relevo/configedit.go`

```go
// ConfigDoc is the editable config: the stored sections decoded, candidates in stored order.
type ConfigDoc struct {
    Candidates []candidate.Candidate
    Actors     map[string]actors.Actor
    Agents     map[string]actors.AgentEntry
    Policy     policy.Policy
}

// CandidateInput is what the add/edit candidate form submits.
type CandidateInput struct{ Harness, Provider, Model string }

// FieldError is a validation failure the form shows under one field.
// Field is "harness", "provider", "model" or "" (a whole-form error).
type FieldError struct{ Field, Msg string }
func (e *FieldError) Error() string   // returns Msg

// ConfigEdit is one validated change, ready for WriteConfigEdit.
type ConfigEdit struct {
    Sections map[config.Section]json.RawMessage // only the sections the edit changes
    Message  string                             // revision message, e.g. "add candidate glm-5.3-flash"
    Name     string                             // the name the edit produced or touched (the new name after a rename)
}

// ActorSlot is one place a candidate sits in an actor's list.
type ActorSlot struct {
    Actor    string
    Position int  // 1-based
    Off      bool
}

var ErrNoChange = errors.New("nothing changed")
```

Encode sections the way the store's own encoders do:
- candidates: `json.MarshalIndent(d.Candidates, "", "  ")` plus a trailing newline;
- actors: `actors.EncodeActors`;
- agents: `actors.EncodeAgents`.

## 4. Contracts

### `internal/harness`

- `func PinnedModel(kind string, raw []byte) string`. This is a pure move of `pinnedModel` and its two helpers. Behaviour
  and doc comments are unchanged, and the helpers stay unexported in `harness`. `internal/doctor` calls
  `harness.PinnedModel`.
- `func AgentFiles(env InstallEnv, agent string) ([]AgentFile, error)`.
  - For every kind in `All()` whose binary `env.LookPath` finds, in `All()` order, run
    `Install(env, InstallOptions{Kind: k, Role: agent, DryRun: true})`.
  - Map each result's outcome to a `FileState`.
  - For any state but `FileMissing`, read the file with `env.ReadFile(result.Path)` and set `Model` from
    `PinnedModel(k, raw)`.
  - An `OutcomeError` result, or an `Install` error, returns the error.
  - A non-shipped `agent` returns `nil, nil`.
  - **Postcondition:** it never writes, because `DryRun` holds.
- `func ResetAgentFile(env InstallEnv, kind, agent string) (InstallResult, error)`.
  - Runs `Install(env, InstallOptions{Kind: kind, Role: agent, Force: true})` and returns its single result. Zero
    results is an error, `"<kind> has no <agent> definition"`.

### `internal/relevo/configedit.go`

- `func LoadConfigDoc(s *config.Store) (ConfigDoc, error)`.
  - Reads each section with `s.Body(sec)`. A missing section is empty. Decode candidates into
    `[]candidate.Candidate`, actors with `actors.ParseActors`, agents with `actors.ParseAgents`, and policy with
    `policy.Parse`.
- `func AddCandidate(d ConfigDoc, in CandidateInput) (ConfigEdit, error)`.
  - Validates (§4.1), appends `Candidate{Harness, Provider, Model}` with an empty name, and derives names for the whole
    list with `candidate.DeriveNames`, setting only the new entry's `Name`. It changes only the candidates section.
  - Message: `"add candidate <name>"`. Name: the new name.
- `func EditCandidate(d ConfigDoc, name string, in CandidateInput) (ConfigEdit, error)`.
  - The entry must exist, else `FieldError{"", "no candidate named <name>"}`. An input equal to the entry's
    harness/provider/model returns `ErrNoChange`.
  - Validates (§4.1), excluding the entry itself from the duplicate check.
  - **The name follows the model.** Set the entry's `Name` to "", derive names for the whole list, and take this
    entry's result. Every other entry keeps its stored name.
  - Every other field of the entry is kept: tier, patterns, plan and the rest.
  - When the name or the token changed, every actor entry whose `Candidate` equals the old name **or** the old token is
    rewritten to the new name, keeping its `Off` flag and position. The actors section is included only when it
    changed.
  - Message: `"edit candidate <old> → <new>"`, or `"edit candidate <name>"` when the name did not change. Name: the
    new name.
- `func DeleteCandidate(d ConfigDoc, name string) (ConfigEdit, error)`.
  - Removes the entry, and removes every actor entry that references it by name or token.
  - If that leaves an actor that had entries with **zero** entries, return
    `FieldError{"", "<actor> has no other candidate; add one in :actors first"}` and write nothing. Name the first such
    actor in sorted order.
  - Message: `"delete candidate <name>"`.
- `func CandidateSlots(d ConfigDoc, name string) []ActorSlot`: every actor slot that references the candidate by name or
  token, sorted by actor name. Used by the delete confirmation.
- `func SetActorEntries(d ConfigDoc, actor string, entries []actors.Entry) (ConfigEdit, error)`.
  - The actor must exist.
  - Every entry must resolve to a candidate in `d`, else `FieldError{"", "no candidate named <x>"}`.
  - No duplicates.
  - Replaces the actor's list. This one call covers reorder, on/off, add and remove.
  - Message: `"edit actor <actor> candidates"`.
- `func EditActor(d ConfigDoc, actor, agent, tier string, check bool) (ConfigEdit, error)`.
  - The actor must exist.
  - `agent` must be shipped (`actors.Shipped`) or a key of `d.Agents`, else `FieldError{"agent", "no agent named <x>"}`.
  - `tier` is "" (unset) or one of `harness.ParseTier`'s four names.
  - When the agent's shape is reader, store `Check = nil`, whatever `check` says. When it is writer, store
    `Check = &check`.
  - Message: `"edit actor <actor>"`.
- `func AddActor(d ConfigDoc, name, agent string) (ConfigEdit, error)`.
  - The name matches the actor pattern (reuse `actors.ParseActors` validation by encoding and parsing) and is not taken.
  - The agent exists.
  - It is created with no candidates, no tier, and `Check = nil`.
  - Message: `"add actor <name>"`.
- `func DeleteActor(d ConfigDoc, name string) (ConfigEdit, error)`.
  - A builtin actor name (`harness.RoleByName(name)` ok) is refused:
    `FieldError{"", "<name> is built in; it can't be deleted"}`.
  - Message: `"delete actor <name>"`.
- `func DeleteAgent(d ConfigDoc, name string) (ConfigEdit, error)`.
  - A shipped agent is refused: `FieldError{"", "<name> ships with relevo; it can't be deleted"}`.
  - An agent that any actor uses is refused: `FieldError{"", "used by <actor>[, <actor>…]; point it at another agent in :actors first"}`,
    with actors sorted.
  - A name absent from `d.Agents` is `FieldError{"", "no agent named <name>"}`.
  - Only the agents section changes. Message: `"delete agent <name>"`.
- `func WriteConfigEdit(s *config.Store, e ConfigEdit) error`: `s.As("ui", e.Message).PutDoc(e.Sections)`, returning
  its error. Warnings are ignored.
- `func ReloadConfig(rt Runtime) (Runtime, error)`.
  - `L, err := rt.Config.Load()`, then set `rt.Candidates`, `rt.Policy`, `rt.Registry` and `rt.ConfigWarnings` from
    `L`, exactly as `ConfigWatcher.Refresh` does (`internal/relevo/reload.go:98-101`).
  - A nil `rt.Config` returns `rt` and an error, `"no config store"`.

### 4.1 Candidate input validation (shared by Add and Edit)

These checks run in this order, and the first failure is returned:
1. `harness.Lookup(in.Harness)` not ok gives `FieldError{"harness", "pick a harness"}`.
2. An empty provider gives `FieldError{"provider", "a provider is required"}`.
3. A provider containing `/` gives `FieldError{"provider", "a provider is one word, with no /"}`.
4. A kind with non-nil `Providers` whose list lacks the provider gives
   `FieldError{"provider", "pick one of <kind>'s providers"}`.
5. An empty model (after `strings.TrimSpace`) gives `FieldError{"model", "a model is required"}`. Store the trimmed
   model. The provider is trimmed too.
6. The same harness/provider/model as another entry gives `FieldError{"model", "already a candidate: <that name>"}`.
7. A provider equal to another candidate's stored name gives
   `FieldError{"provider", "<provider> is already a candidate's name"}`.

After the edit is built, **dry-run the whole config**:
- `config.Validate` for every changed section;
- then `actors.ToRolesFile(agents, actors)` on the post-edit maps;
- then `roles.Build(rf, set, policy)` with the post-edit candidate set from `candidate.Parse`.

Any error becomes `FieldError{"", err.Error()}`. The actor operations and `DeleteAgent` run the same dry-run.

## 5. Pseudocode

```
EditCandidate(d, name, in):
  i = index of entry with Name == name            → FieldError{"", "no candidate named …"} if none
  if in == (entry.Harness, entry.Provider, entry.Model): return ErrNoChange
  validate(in, d, skip=i)                         → FieldError
  oldName, oldTok = entry.Name, entry token
  next = copy(d.Candidates); next[i].{Harness,Provider,Model} = in; next[i].Name = ""
  next[i].Name = DeriveNames(next)[i]
  acts = copy of d.Actors; for each actor, each entry: if Candidate in {oldName, oldTok} → Candidate = next[i].Name
  edit = sections{candidates: encode(next)} + (actors if acts != d.Actors)
  dryRun(next, acts, d.Agents, d.Policy)          → FieldError{"", err}
  return edit
```

## 6. Error handling

- Every validation failure is a `*FieldError`, so the screens can put it under a field.
- Store and DB errors pass through unwrapped from `WriteConfigEdit`, `LoadConfigDoc` and `ReloadConfig`.
- `ErrNoChange` is a sentinel the caller treats as "nothing to save".
- Nothing here logs.

## 7. Working efficiently

- **Read in one parallel batch:**
  - `internal/config/config.go` (Store, Body, Put, PutDoc, Validate)
  - `internal/config/revision.go:20-45`
  - `internal/candidate/candidate.go:25-130, 275-370`
  - `internal/candidate/names.go`
  - `internal/actors/actors.go`, `convert.go`, `shipped.go`
  - `internal/roles/registry.go` (Build signature)
  - `internal/relevo/reload.go:60-110`
  - `internal/harness/harness.go:96-230`, `install.go:40-120, 240-300`
  - `internal/doctor/doctor.go:260-335`
  - `internal/policy` (Parse)

  Do not search for things this plan names.
- **The PinnedModel move is mechanical:** cut the three functions and their tests out of `internal/doctor` and paste
  them into `internal/harness/pin.go`/`pin_test.go`, then export `pinnedModel` as `PinnedModel`. Fix `doctor.go`'s
  callers in the same pass.
- **Focused tests** (local; `make check` is refused on this machine by a hook, so do not run it):
  ```
  env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ -run 'ConfigEdit|Candidate|Actor|Agent|Reload' -count=1
  env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/harness/ ./internal/doctor/ ./internal/config/ ./internal/actors/ -count=1
  ```
- **Final:** `go vet ./internal/...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"`, `go mod tidy -diff` and
  `sh scripts/check-name.sh`. The planner runs the race suite on the server.

## 8. Steps

1. **`Harness.Providers`**: the field, its doc comment, and the agy and claude values.
   - Verify: `go build ./...`.
2. **Move PinnedModel** into `internal/harness/pin.go` with its tests.
   - Verify: `go test ./internal/harness/ ./internal/doctor/` passes, and `git grep -n pinnedModel internal/doctor`
     shows only the call sites, now `harness.PinnedModel`.
3. **`agentfiles.go`**, with `agentfiles_test.go`. Use a fake `InstallEnv`; copy the fake the existing
   `install_test.go` uses. Cases:
   - identical file gives up to date;
   - manifest-matching old copy gives stale;
   - hand-edited file gives your edit, with its model pin read;
   - absent file gives missing;
   - a kind whose binary is not on PATH is absent from the result;
   - a custom agent name gives nil;
   - `ResetAgentFile` on an edited file overwrites it, and a following `AgentFiles` says up to date.
4. **`configedit.go`**, with `configedit_test.go`. Build `ConfigDoc` values directly in the tests.
   - The candidates are this machine's seven, as JSON literals:
     - `agy/google/gemini-3.8-flash-high`
     - `agy/agy-extra/claude-sonnet-4-6`
     - `claude/anthropic/sonnet`
     - `claude/anthropic/haiku`
     - `opencode/openrouter/z-ai/glm-5.3-flash`
     - `opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high`
     - `codex/openai/gpt-5.6-terra:high`
   - The actors are:
     - builder: plan-executor with [gemini-3.8-flash-high, claude-sonnet-4-6, deepseek-v4.1-flash, gpt-5.6-terra,
       sonnet, glm-5.3-flash], tier yolo;
     - reviewer: reviewer with [sonnet, gpt-5.6-terra], tier yolo;
     - researcher: researcher with [haiku].
   - **Required cases** (a subtest each):
     - every §4.1 rule, and that it sets the right `Field`;
     - Add derives the name and changes only the candidates section;
     - Edit of deepseek's model to `cline-pass/deepseek-v4.2-flash#high` renames it to `deepseek-v4.2-flash`, and the
       builder's third entry follows the rename;
     - Edit keeps an entry's tier and patterns;
     - Edit with an unchanged input returns `ErrNoChange`;
     - an off entry keeps `Off` through a rename;
     - Delete of `haiku` is refused ("researcher has no other candidate…");
     - Delete of `gpt-5.6-terra` removes it from builder and reviewer;
     - `CandidateSlots("gpt-5.6-terra")` gives builder 4 and reviewer 2;
     - SetActorEntries rejects an unknown name and a duplicate;
     - EditActor of `builder` to agent `reviewer` fails the dry-run (a builtin actor keeps its shape);
     - EditActor to a reader agent stores `Check` nil;
     - AddActor followed by DeleteActor on a custom name works; DeleteActor(`builder`) is refused;
     - DeleteAgent: shipped is refused, one used by an actor is refused, and an unused custom native agent works;
     - WriteConfigEdit + LoadConfigDoc round-trip on a real `config.Store` over a temp `db.Open`, with the revision
       source `"ui"` and the message as set;
     - ReloadConfig picks up an added candidate.
   - **Mutation check (required):** remove the actor-rename loop in EditCandidate, and the rename subtest must fail.
     Remove the §4.1 rule 4 check, and its subtest must fail. Restore both.
5. **Final checks** (§7). Copy this plan to the worktree's `docs/plans/`. Make one commit:
   `feat(cockpit): config edit operations for the candidates, actors and agents views (backend)`.

## 9. Stop rather than improvise

If a signature, line reference or behaviour this plan states is wrong, **halt and report**. Examples: `PutDoc` does not
take a map of sections; `DeriveNames` does not honour explicit names; an empty actor candidate list fails
`roles.Build`; `Install` with `DryRun` saves the manifest. Do not bend a test to fit.
