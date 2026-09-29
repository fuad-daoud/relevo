# A4-2a: the binding record and the status document say runner, actor, candidate

Spec: `docs/specs/2026-09-26-a4-state-rename-design.md`, §2, §3.2, §3.3 and §4.

It is a **clean break** (D1): nothing writes or emits an old key. It keeps internal
names (D3): **Go field names stay** (`BuilderCandidate`, `Role`, `Builder` …); only JSON
tags, stored values and printed words change.

**Vocabulary:**
- an **actor** is config;
- a **runner** is the live process and plays one actor;
- a **candidate** is the model a round runs on;
- `builder` stays only as the seeded actor's name.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

**Owned elsewhere, do not edit:**
- the wire: `internal/remote/proto.go`, `WhoAmI`, `BuildersView`, and serve status's
  `builders` census;
- the round table (merged in #600).

## 1. The binding record (internal/store), format 6 → 7

| Go field (unchanged) | JSON today | JSON A4 |
|---|---|---|
| `Binding.Builder` (Endpoint) | `builder` | `runner` |
| `Binding.BuilderCandidate` | `builder_candidate` | `candidate` |
| `Binding.Role` | `role`, omitted for builder | `actor`, **always written**; the empty value is stored as `"builder"` |
| `Binding.BuilderMissingSince` | `builder_missing_since` | `runner_missing_since` |
| `Binding.BuilderScreen`, `BuilderScreenAt` | `builder_screen`, `builder_screen_at` | `runner_screen`, `runner_screen_at` |
| `Consult.Role` (consult.go:39) | `role` | `actor` |
| `LogEntry.BuilderSession` (log.go:119) | `builder_session` | `runner_session` |
| `DirToBuilder` (log.go:25) value | `to_builder` | `to_runner` (the constant name stays) |

This amends spec §2's `runner.actor`: the record carries a top-level `actor`, like the
status document. It is simpler, and the Endpoint type is shared with the planner.
Put this one-line amendment in the spec file itself, in §3.2's row.

**Reading old state (lazy migration, amends spec §4):**
- Decoding a binding, consult or log entry accepts **both** the old and the new keys and
  directions. The old ones map to the same fields, and when both are present the new
  key wins.
- Encoding writes **only** the new ones.
- Records are therefore migrated on their next save, with no "rounds open" gate. That
  gate existed for the spool-path rename, which moved to A5.
- Delete spec §4's "Open rounds" bullet and replace it with one sentence describing
  this lazy decode.
- Implement it with small `UnmarshalJSON` methods that decode into an alias type plus
  the legacy fields. Do not hand-parse.
- `Binding.Role == ""` after decode means `builder`. Normalise it on decode, so readers
  never see "" from new records; old readers of `Role == ""` keep working.

**Format:**
- `BindingFormat` becomes 7, and `recordFormat` returns 7 for every record, since every
  record now has the new keys. An older binary refuses it: that is the clean break.
- Rewrite the `format.go` comment to say so, keeping to the comment rules: why, not
  history.
- Regenerate `internal/store/testdata/binding-shape.golden`.

**SQL:** check whether anything queries `entry_json` or `record_json` by the old
**keys or values** (`json_extract(…'$.direction')`, `'$.role'`, `'$.builder…'`) in
`internal/db`, `internal/store` or `internal/ingest`. If it does, those queries must
match both, or read through the Go decode. List each one. If you cannot make one match
both safely, stop and report.

## 2. The status document (`view.BindingStatus`, internal/view/status.go:46-84)

| Go field | JSON today | JSON A4 |
|---|---|---|
| `BuilderCandidate` | `builder_candidate` | `candidate` |
| `BuilderName` | `builder_name` | `candidate_name` |
| `Role` | `role`, omitempty | `actor`, **always present**, "builder" when the binding's is empty |
| `BuilderKind` | `builder_kind` | `harness` |
| `BuilderDefinition` | `builder_definition` | `agent_definition` |
| `BuilderDefinitionCustom` | `builder_definition_custom` | `agent_definition_custom` |
| `BuilderStatus` | `builder_status` | `runner_status` |

- The statusline row (`internal/view/statusline.go:~344`) drops its `role` field.
  `actor` and `candidate` are already there.
- Regenerate, and list with a reason:
  - `cmd/relevo/testdata/contract/status.golden`, `status-all.golden`,
    `status-name.golden` and `statusline-json.golden`;
  - `show-log*.golden` (the direction);
  - `internal/mcp/testdata/contract/tool-status.golden`, if it moves;
  - any serve golden that embeds `BindingStatus` (`serve-status.golden`,
    `internal/serve/testdata/contract/status-document.golden`). Only the embedded
    per-binding keys change there; the `builders` census is the wire round's.

## 3. The opencode plugin (internal/harness/opencodeplugin/tui.tsx)

- Line ~358: `row?.actor || row?.role || "builder"` becomes `row?.actor || "builder"`.
  Update the comment at ~355.
- Lines ~1446 and 1464: the direction regex `(to_builder|to_planner)` becomes
  `(to_runner|to_planner)`.
- Search tui.tsx and server.ts for other reads of the renamed status or history keys
  (`builder_candidate`, `builder_name`, `builder_status`, `builder_kind`, `role`,
  `BuilderCandidate`…), and update each one.
- Fixtures in `scripts/testdata/opencode-plugin/`:
  - `status-1.json` and `status-2.json`: the new status keys, with `"actor": "designer"`
    in place of `"role": "designer"`;
  - `history.json`: the `Candidate*` and `Actor` keys that #600 now emits;
  - `show-log.json`: the direction.
- Then run `sh scripts/opencode-plugin-smoke.sh` if it runs on this server; if it
  needs a TTY or tmux it cannot get, say so in the report. Also run
  `sh scripts/check-plugin-version.sh`.

## 4. The leftovers from A4-1a

- The dry-run's user-facing `  builder   headless …` label (`RenderDryRun`, in
  internal/relevo or internal/view; find it by text) becomes `  runner    headless …`,
  keeping the column alignment.
- `cmd/relevo/ask.go:~91` "asked round %d's builder (…)" becomes "asked round %d's
  runner (…)".
- Stale comments that quote changed strings:
  - `internal/view/candidates_list.go:~36` "(no role)";
  - `internal/view/candidates_list.go:~152` "roles missing (builder, reviewer) until
    cleared";
  - `internal/relevo/send.go:~636` "roles missing: …".

  Use the current strings: "(no actor)" and "agents missing".
- README.md: the `--json` field documentation (around the lines documenting `role <r>`
  and the `role` JSON key, ~1444-1456) takes the new keys from §2.

## 5. Tests

1. `TestBindingDecodesFormat6Record`: a format-6 fixture JSON with `builder`,
   `builder_candidate`, `role: "designer"`, `builder_missing_since`, a consult with
   `role`, and a log entry with `to_builder` and `builder_session`.
   - It decodes to the same Go values as the new keys would.
   - Re-encoding writes only new keys, at format 7.
2. `TestBindingEmptyRoleReadsAsBuilder`: an old record with no `role` decodes to
   `Role == "builder"`, and encodes `"actor":"builder"`.
3. `TestStatusDocumentAlwaysNamesTheActor`: a builder binding's status JSON has
   `"actor":"builder"` and no `role`, `builder_*` or `builder_status` key.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) Drop the legacy `builder_candidate` mapping from the decoder: test 1 fails.
  - (b) Encode `role` too: test 1 fails.
  - (c) Make the status `actor` omitempty: test 3 fails.

## 6. Working efficiently

- Batch-read these:
  - internal/store/binding.go, consult.go, log.go, format.go, lifecycle.go:150-170 and
    290-320;
  - internal/store/format_test.go;
  - internal/view/status.go and statusline.go;
  - the plugin's tui.tsx around the cited lines;
  - the fixture JSONs;
  - cmd/relevo/contract_test.go.
- Focused loop: `go build ./... && go test -count=1 ./internal/store/ ./internal/view/ ./internal/mcp/ ./internal/serve/ && go test -count=1 ./cmd/relevo/ -run 'Contract|Status|Show'`
- Full check, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`

## 7. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a4-2a-record-and-status.md`. Commit
everything, including the spec amendments, as **one new commit**:
`feat(a4): the binding record and status say runner, actor and candidate`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- the SQL finding;
- every golden that moved, and why;
- the plugin changes and the smoke result;
- the mutations;
- `make check`'s last lines;
- anything that did not match.
