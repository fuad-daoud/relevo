# Plan: pin the `relevo serve status --json` keys the burst provider reads

## 1. System overview

The burst provider in fuad-daoud/servers (`contabo/burst/`) runs
`relevo serve status --json` on each worker node over ssh. It uses the
output to confirm a node is idle before tearing it down.

A recent rename (`builders` → `runners`, `builder_candidate` → `candidate`,
`builder_status` → `runner_status`) broke that in production. The only
guard was a golden file (`internal/serve/testdata/contract/status-document.golden`),
and it was regenerated with `-update` along with the rename.

This round adds one test. It asserts the literal JSON key names the provider
reads, so a future rename fails CI and names the external reader. There is
no production code change.

## 2. Working efficiently

- Every location is named below, so do not search. Read
  `internal/serve/admin_test.go` once. The helpers you need are in it:
  - `newAdminServer` (~line 40), `enrol` (~53), `saveOwnerBinding` (~67)
  - `AdminStatus` usage (~672-703), `StatusDocument` (~620)
- Make the whole change in one edit call: append the new test at the end of
  `internal/serve/admin_test.go`.
- Focused test: `go test -count=1 -run 'TestServeStatusJSONKeysBurstReads' ./internal/serve/`
- Full check, once, at the end: `make check`. If a hook blocks it locally,
  run these instead and say so in the report:
  - `go vet ./internal/serve/`
  - `gofmt -l internal/serve`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
  - `go test -count=1 ./internal/serve/`
- If a step is impossible as written, or contradicts the code, stop and
  report. Do not improvise. In particular, if a key listed in §4 is not in
  the marshalled document on main today, halt and report which one. Do not
  rename anything.

## 3. File structure

```
internal/serve/admin_test.go                          + TestServeStatusJSONKeysBurstReads (append at end)
docs/plans/2026-09-26-pin-serve-status-keys.md        this plan, saved verbatim (last step)
```
Scope is exactly these two files.

## 4. The test contract

**`TestServeStatusJSONKeysBurstReads(t *testing.T)`**

**Setup** (mirror `TestAdminStatusLastSeen`):
- `now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)`
- `s := newAdminServer(t, now)`
- `id := enrol(t, s, "alice")`
- `saveOwnerBinding(t, s, id, "app-a", func(b *store.Binding){ b.Round = 2; b.BuilderCandidate = "<any token>"; b.Serve = &store.ServeFacts{LastSeen: now} })`
  - Use whatever fields the helper's mutate func needs so that the binding
    shows up in the owner's report. If the current Binding field for the
    candidate is not named `BuilderCandidate` on main, use the one that is.
    The JSON key under test is what matters.
- `owners, _, err := AdminStatus(context.Background(), s)`
- `doc := StatusDocument(owners, remote.BuildersView{Running: 1, Cap: 2})`
- `json.Marshal(doc)`, then `json.Unmarshal` into `map[string]any`.

**Assertions:** key presence only. Walk the generic map, so a Go field
rename that keeps the JSON tag still passes.
1. The top-level map has the keys `runners` and `last_contact`.
2. `runners` is an object with the keys `running`, `queued` and `cap`.
3. `owners` is an array, and `owners[0].report.bindings` is a non-empty array.
4. `bindings[0]` has the keys `name`, `round`, `state`, `candidate` and
   `runner_status`.
5. The values for `name` ("app-a") and `round` (2) are the ones seeded, so
   the walk is not reading a zero object.

Each failure message names the missing key and says what breaks: "the burst
provider in fuad-daoud/servers reads <key>; renaming it needs a burst
release that accepts the new name first".

**Comment:** one short doc comment above the test, explaining WHY it exists
and nothing more (CLAUDE.md "Code style": no issue or PR numbers, no
history). Say that:
- an external program (the burst provider in fuad-daoud/servers) parses
  these keys; and
- the test uses literal keys rather than the golden file, because `-update`
  regenerates the golden silently.

## 5. Pseudocode

```
seed one owner with one binding
doc := StatusDocument(AdminStatus(...))
m := unmarshal(marshal(doc)) as map[string]any
requireKeys(t, m, "runners", "last_contact")
requireKeys(t, m["runners"], "running", "queued", "cap")
b0 := m["owners"][0]["report"]["bindings"][0]
requireKeys(t, b0, "name", "round", "state", "candidate", "runner_status")
check b0["name"] == "app-a" and b0["round"] == 2 (float64 after Unmarshal)
```
A small local helper, `requireKeys(t, obj any, keys ...string)`, is fine
inside the test file. Keep it under 20 lines, and fail with `t.Fatalf` when
`obj` is not a map.

## 6. Error handling

This is test-only, and it has no error paths beyond `t.Fatalf` on marshal or
unmarshal errors and on type assertions.

## 7. Ordered steps

1. Append the test (and its helper) to `internal/serve/admin_test.go`.
   - Verify: the focused test passes.
2. Mutation check. Temporarily change the `json:"runner_status"` tag on
   `view.BindingStatus`, which is in `internal/view/status.go` (find the
   field with that tag), to `json:"runner_state"`.
   - The new test must fail with a message naming `runner_status`.
   - Revert, and confirm `git diff` shows no change to `internal/view`.
   - Do the same once for the `json:"runners"` tag in
     `internal/serve/admin.go` (~line 133). Revert.
3. Full check (§2).
4. Save this plan verbatim as
   `docs/plans/2026-09-26-pin-serve-status-keys.md`.
5. Commit both files on the binding's branch:
   `test(serve): pin the serve status --json keys the burst provider reads`.

Report the two mutation results (the failure message seen for each).
