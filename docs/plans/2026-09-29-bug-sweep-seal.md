# Plan: reserved round-file names for relevo-authored rows (#683)

One builder round. Two commits, never pushed. This plan is committed verbatim as `docs/plans/2026-09-29-bug-sweep-seal.md` in commit 1 (byte-identical, including this line). If the tree contradicts anything below — an anchor that is not where it says, a caller whose key is not what the table says, a fixture the plan does not name — halt and report; do not improvise.

## 1. Seed vs tree (read first)

Anchors verified in this tree (HEAD `19a16eec`, detached, 10 pre-existing untracked docs under `docs/plans/` and `docs/specs/` — leave them untracked; they are not part of this round):

- `Store.ReadFile` is `internal/store/seal.go:28-53` (disk read `:29`, row fallback `:37-52`), `SealRound` is `internal/store/seal.go:301-354` (loop `:323`, `RoundFilePut` `:332`), and `internal/store/roundfile_put.go:22-50` is `Tx.PutRoundFile`, whose upsert is `db.(*Tx).RoundFilePut` at `internal/db/roundfile.go:23-38`. The seed names all three correctly.
- The deferral this round closes is `docs/plans/2026-09-29-bug-sweep-trust.md:108` (its §7 first bullet). It is still accurate in this tree.
- The issue's anchors (`seal.go:28`, `seal.go:301`, `roundfile_put.go:22`) all hold.

Three seed readings resolved here, so the builder does not have to guess:

1. **The guard cannot sit at the upsert.** The seed's parenthesis "(roundfile_put.go:22 is the upsert)" names the mechanism being abused, not the place for the guard. `Tx.PutRoundFile` is relevo's own authoring path and re-puts reserved rows while a round runs: `internal/relevo/headless.go:280` re-puts `builder-segments.json` on every spawn and mid-round switch, `internal/relevo/remotefetch.go:253` and `:265` re-put a log and a drift row, `internal/relevo/remote_catchup.go:93` re-puts a fetched diff. A refusal inside `RoundFilePut` would break relevo. The refusal goes in the **disk→row path** (the round-file walk that `SealRound`, `SealAll` and `forkRoundFiles` all read from); `RoundFilePut` keeps its upsert.
2. **`ask` is not in the set.** Relevo authors `NNN-<id>-ask.md` either as a row or as a file (`internal/consult/verify.go:341` inline, `:346` when the question is over `InlineAskMax`), so it fails the seed's own test ("keys relevo authors *only* in the database"). The reserved set is exactly the four keys in §2. The `question` key (`NNN-question.md`) is out too: this tree has no row producer for it (`store/paths.go:202`, read at `relevo/waiting.go:16`).
3. **Fixture fallout the seed does not mention (test-only).** Eight test fixtures in `internal/store`, `internal/relevo`, `internal/serve` and `cmd/relevo` write a reserved key to disk to simulate a relevo-authored artifact; that is now the refused form, so each must author a row instead. They are listed in §6. There are **no production caller changes** outside `internal/store`.

Nothing above halts the round. A halt is called for if, while implementing, a `ReadFile` caller turns out to read a reserved key from disk today in a way the classification in §5 does not cover, or if a fifth key turns out to have a disk producer.

## 2. Behaviour and cases

### Reserved names — the closed set

Flat round-file names, and only these:

| Key | Producer (row only) |
| --- | --- |
| `NNN-diff.patch` | `capture/capture.go:214`, `relevo/remote_catchup.go:93` |
| `NNN-drift.patch` | `capture/drift.go:64`, `relevo/remotefetch.go:265` |
| `NNN-builder-segments.json` | `relevo/headless.go:280` |
| `NNN-<id>-findings.md` | `consult/reconcile.go:136`; `<id>` is the consult id, 8 lowercase hex (`consult/ask.go:61-70`) |

The rule is the basename matching `^\d{3}-(diff\.patch|drift\.patch|builder-segments\.json|[0-9a-f]{8}-findings\.md)$`. The rule is anchored and contains no `/`, so a nested name (`NNN-<actor>/<rel>`, e.g. a reader's `NNN-reviewer/findings.md` output) is never reserved.

### `Store.ReadFile`

- A path that resolves (via `bindingRelOf`) to a reserved name under the store root **never reads the disk**: it answers from the sealed row — the live record, else the most recently archived one (`sealedLookup` already does that fallback).
- No row → the not-exist result for that path, in the same shape `os.ReadFile` gives (`*fs.PathError` with `fs.ErrNotExist`), so both `errors.Is(err, fs.ErrNotExist)` and `os.IsNotExist(err)` keep working. `os.IsNotExist` is the one that matters at three callers (`relevo/show.go:565`, `serve/roundfiles.go:59`, `ui/fetch.go`), and it does not see a `%w`-wrapped sentinel — do not wrap.
- The disk bytes are never returned, even when a file exists there. No per-read log: the refusal is reported once per pass by the walk below.
- All other keys keep exactly today's order: disk, then row, then the path's own not-exist error.

### `Store.StatFile`

Same policy: a reserved key's size and mtime come from the row (or the call is a miss), never from a disk file. One caller depends on it: `forkRoundFiles` (`internal/store/fork.go:185`) stamps a copied row's mtime from `StatFile`, and a plant must not set it.

### Disk round files

- The round-file walk refuses a reserved flat basename: one `slog.Warn` per path (same style as the symlink skip at `seal.go:418-421`) and skip. The file is not read, not sealed, not removed, and not listed by `RoundFiles`/`RoundsOnDisk`, so `SealRound`, `SealAll`, `Archive` and `forkRoundFiles` never see it. It stays on disk, byte-identical.
- Consequence to report, not to fix: a reserved plant keeps a DONE binding's directory non-empty, so the seal pass's empty-dir removal (`relevo/daemon.go:511-515`) does not fire while it is there; `Archive` and delete remove the directory with `RemoveAll` (`store/lifecycle.go:407`, `:441`), so the file goes with the binding.
- The walk checks the flat branch only. A nested artifact file (`walkArtifactDir`'s `NNN-<actor>/<rel>` names) is never reserved and stays disk-first.

### Unchanged — load-bearing, do not regress

- Prompts and streams are disk-first: `PromptPath`/`StreamPath` resolve through `StatFile` (`paths.go:131-141`, `:180-192`), and a resend re-stages a prompt for a round whose earlier prompt is already sealed — the fresh file must win. `ReadFile(promptPath)` returns the fresh file even when a sealed row exists.
- Every runner-written or captured key stays disk-first: builder log, gate log, consult stream, question, ask, reader output and other artifacts, report.
- `Tx.PutRoundFile`/`RoundFilePut` keep the upsert.
- `ReadFile`'s ordinary path (disk, then row) is untouched for every non-reserved key.

### Cases the tests pin

1. **Shadow**: a row plus a plant of the same name → the row's bytes.
2. **Invent**: a plant with no row → a not-exist miss, never the plant's bytes (for each of the four shapes).
3. **Preserve**: a plant at a non-reserved key (`001-report.md`, `004-reviewer/findings.md`) is still read from disk.
4. **Load-bearing**: prompt and stream with a sealed row and a fresh disk file → the file's bytes; file gone → the row's bytes.
5. **Seal**: a reserved plant never becomes a row, the row it would replace keeps its bytes, the plant stays on disk, and it does not make `RoundsOnDisk` report its round.

## 3. Seams (verified in this tree)

| Seam | Anchor |
| --- | --- |
| `Store.ReadFile` — the policy slot | `internal/store/seal.go:28-53` (disk `:29`, miss `:42`, row `:45-52`) |
| `Store.StatFile` | `internal/store/seal.go:58-84` |
| `sealedLookup` — live record, else newest archived | `internal/store/seal.go:96-117` |
| `bindingRelOf` — path → binding + round_file name | `internal/store/paths.go:93-110` |
| `roundFileRel` — flat vs nested name | `internal/store/paths.go:64-88` |
| the reserved keys | `internal/store/paths.go:194` `BuilderSegmentsPath`, `:208` `DiffPath`, `:212` `DriftPath`, `:254` `FindingsPath` (via `consultFile` `:266`) |
| disk-first keys kept | `internal/store/paths.go:131` `PromptPath`, `:143` `ReportPath`, `:156` `BuilderLogPath`, `:180` `StreamPath`, `:198` `GateLogPath`, `:202` `QuestionPath`, `:248` `AskPath` |
| `SealRound` | `internal/store/seal.go:301-354` |
| `diskFiles` / `walkRoundDir` (flat branch `:437-440`) / `walkArtifactDir` | `internal/store/seal.go:392-399`, `:403-443`, `:449-481` |
| symlink-skip style to copy | `internal/store/seal.go:418-421` |
| `RoundFiles` / `RoundsOnDisk` | `internal/store/seal.go:122-159`, `:164-183` |
| `SealAll` / `archive` (seal, then `RemoveAll`) | `internal/store/archive.go:18-31`, `internal/store/lifecycle.go:388-414` |
| `Tx.PutRoundFile` (unchanged) / `RoundFilePut` (unchanged) | `internal/store/roundfile_put.go:22-50`, `internal/db/roundfile.go:23-38` |
| the seal pass | `internal/relevo/daemon.go:465-517` |
| store test helpers | `internal/store/helpers_test.go:8` `newBinding`, `internal/store/seal_test.go:207` `writeSealFixtures` |
| the deferral this closes | `docs/plans/2026-09-29-bug-sweep-trust.md:108` |

New files: `internal/store/reserved.go` (the regexp, `reservedRoundFile`, the why), `internal/store/reserved_test.go` (the tests), `docs/plans/2026-09-29-bug-sweep-seal.md` (this plan). `internal/store/seal.go` is 521 lines today; the guards and their comments keep it under 600, and `ReadFile` stays far under 70 lines.

## 4. Every `ReadFile` caller, classified

`grep -rn 'ReadFile(' --include='*.go' internal cmd` (non-test) yields the call sites below. R = reserved (row-only after this round), D = disk-first by design (unchanged), M = mixed by the kind asked, O = other.

| # | Caller | Key it reads | Class | Effect of this round |
| --- | --- | --- | --- | --- |
| 1 | `internal/capture/capture.go:388` `ReadDiff` | `NNN-diff.patch` | R | the row; a plant is a miss (`show --diff` says missing) |
| 2 | `internal/capture/drift.go:126` `ReadDrift` | `NNN-drift.patch` | R | same for `--drift` |
| 3 | `internal/consult/reconcile.go:129` | `NNN-<id>-consult.jsonl` (`Endpoint.LogPath`) | D | unchanged (the process is writing it; the row answers after the seal) |
| 4 | `internal/consult/reconcile.go:197` `applyVerdict` | `NNN-<id>-findings.md` | R | the verdict parses the row; a plant cannot set a verdict |
| 5 | `internal/ingest/source.go:146` `storeSource.Open` | the member the caller names | O | reserved members come from the row; plants are no longer listed |
| 6 | `internal/relevo/artifacts.go:87` `ReadArtifact` | nested `NNN-<actor>/<rel>` | D | unchanged (runner-written artifact; its stat is `:52`) |
| 7 | `internal/relevo/remotefetch.go:109` `fetchLogMirror` | `NNN-builder.log` | D | unchanged (legacy file; the row is the remote mirror) |
| 8 | `internal/relevo/remotefetch.go:186` `fetchDrift` | `NNN-drift.patch` | R | the "already stored" probe stops seeing a plant; it fetches the server's drift, which is the intent |
| 9 | `internal/relevo/remotefetch.go:236` `applyLogMirror` | `NNN-builder.log` | D | unchanged |
| 10 | `internal/relevo/retry.go:17` `RetryPlan` | `PromptPath` | D | unchanged — the resend case |
| 11 | `internal/relevo/summary.go:54` | `StreamPath` | D | unchanged |
| 12 | `internal/relevo/usage.go:157` | `src.StreamPath` | D | unchanged |
| 13 | `internal/relevo/waiting.go:16` `questionFirstLine` | `QuestionPath` | D | unchanged (captured file, no row producer) |
| 14 | `internal/relevo/show.go:204` `liveFindingsPresent` | `NNN-<id>-findings.md` | R | a plant no longer resolves a findings round |
| 15 | `internal/relevo/show.go:279` `read` (live) | prompt, report, diff, drift, gate, findings | M | diff/drift/findings from the row |
| 16 | `internal/relevo/show.go:282` `readBytes` (live) | log, streams, segments | M | segments from the row |
| 17 | `internal/relevo/transcript.go:286` `roundSegments` | `NNN-builder-segments.json` | R | segments from the row; a plant cannot change a round's rendering |
| 18 | `internal/serve/roundfiles.go:57` `readRoundBytes` (non-log) | report, diff, stream, plan, drift | M | diff/drift from the row; a plant is 404 |
| 19 | `internal/serve/roundfiles.go:61` (log read) | builder log, runner/builder stream | D | unchanged; the segments read inside `RoundTranscript` is R |
| 20 | `internal/ui/fetch.go:147` | `PromptPath` | D | unchanged |
| 21 | `internal/ui/fetch.go:371` | log, streams, segments via `RoundTranscript` | M | segments from the row |
| 22 | `internal/usage/source.go:143` `readSealed` | `src.StreamPath` (wired at `relevo/usage.go:57`, `:80`) | D | unchanged |
| 23 | `internal/store/fork.go:178` `forkRoundFiles` | every `RoundFiles(src)` name | O | a reserved key is copied from the row; a plant is not listed, so it is never copied |

Not `ReadFile`, worth one line each in the report: `serve/rounds.go:29` reads the prompt with `os.ReadFile` (not the store) and stays disk-first; `show.go`'s archived branch reads `ArchivedFile` (row-only already) at `:443`, `:453`; the other `StatFile` callers (`artifacts.go:52`, `switch.go:243`, `paths.go:133/137/182/186`) never stat a reserved key.

**Net: no production caller changes. The production diff is `internal/store` only.**

## 5. Steps (done-when each, then the commits)

1. **Read tests, red half.** New `internal/store/reserved_test.go`:
   - `TestReadFileRefusesAReservedRoundFileOnDisk`: table over the four shapes (`001-diff.patch`, `001-drift.patch`, `001-builder-segments.json`, `001-7f2a3c1d-findings.md`), each in two cases — (i) a planted file only → `errors.Is(err, fs.ErrNotExist)` **and** `os.IsNotExist(err)`, and the plant's bytes never returned; (ii) a `PutRoundFile` row plus a plant with different bytes → the row's bytes. Plus the negatives: `001-report.md` and `004-reviewer/findings.md` with a plant and no row → the disk bytes.
   - `TestStatFileRefusesAReservedRoundFileOnDisk`: a plant must not set size/mtime; the row's size/mtime answer instead.
   - `TestReadFilePrefersDiskForPromptAndStream` (the load-bearing pin, no mutation — it must pass before and after): for `PromptPath`, `StreamPath` and `BuilderLogPath`, a row plus a fresh file → the file's bytes; file removed → the row's bytes.
   *Done when:* `go test ./internal/store/ -run 'TestReadFileRefusesAReservedRoundFileOnDisk|TestStatFileRefusesAReservedRoundFileOnDisk|TestReadFilePrefersDiskForPromptAndStream' -count=1` fails the two refusal tests (they get the plant) and passes the preference test.
2. **Policy.** New `internal/store/reserved.go`: `reservedRoundFileRe` and `reservedRoundFile(name string) bool`, with the why (row-only keys; a file with one of these names is a plant or a stale copy). `internal/store/seal.go`: the reserved branch in `ReadFile` and in `StatFile`, ahead of `os.ReadFile`/`os.Stat`, answering through the existing `sealedLookup` path and returning the path's own not-exist error on a miss; update the four doc comments (`ReadFile`, `StatFile`, and the `diskFiles`/`RoundFiles` security wording in step 4). Comments say why only — no issue numbers, no history.
   *Done when:* the step 1 command is green.
3. **Read-side fixture sweep** (§6 rows 1-6): author rows where a fixture wrote a reserved key to disk. *Done when:* `go test ./internal/store/ ./internal/relevo/ ./internal/serve/ ./cmd/relevo/ -count=1` is green.
4. **Commit 1.** Write this plan verbatim to `docs/plans/2026-09-29-bug-sweep-seal.md`; `gofmt -w` the changed Go files; `git add` the plan, `internal/store/reserved.go`, `internal/store/reserved_test.go`, `internal/store/seal.go` and the step 3 fixture files; commit `fix(store): answer reserved round-file keys from the row` with a body ending `Fixes #683`. Do not push.
   *Done when:* `git show --stat HEAD` lists exactly those paths and `git log -1 --format=%s` is that subject.
5. **Seal test, red half.** Add `TestSealRoundLeavesAReservedRoundFileAlone` to `internal/store/reserved_test.go`: (i) `PutRoundFile` a diff row, plant the same path with different bytes, `SealRound(name, 1)` → `n == 0`, the row keeps the authored bytes, the plant is byte-identical on disk, `RoundFiles` still lists the name (from the row); (ii) plant only → `n == 0`, no row is created, `RoundFiles` does not list it and `RoundsOnDisk` is empty.
   *Done when:* `go test ./internal/store/ -run TestSealRoundLeavesAReservedRoundFileAlone -count=1` fails (the plant is sealed and the row replaced).
6. **The disk-side guard.** `internal/store/seal.go`, `walkRoundDir`'s flat branch (`:437-440`): skip a reserved basename with one `slog.Warn` ("round walk: skipping reserved round file"), in the style of the symlink skip at `:418-421`; update `diskFiles`' security paragraph and `RoundFiles`/`SealRound` comments to say a reserved name is never a disk round file.
   *Done when:* the step 5 command is green.
7. **Seal-side fixtures** (§6 rows 7-8). *Done when:* `go test ./internal/store/ ./internal/relevo/ -count=1` is green.
8. **Commit 2.** `gofmt -w`, `git add` `internal/store/seal.go`, `internal/store/reserved_test.go` and the step 7 fixture files; commit `fix(store): never seal a reserved round-file name` with a body ending `Fixes #683`. Do not push.
   *Done when:* `git log --oneline -2` shows exactly the two `fix(store)` commits.
9. **Mutations** (§7): apply each, run its named test, confirm the failure, revert, and confirm `git diff` is empty again.
   *Done when:* all three fail under their mutation and pass after the revert.
10. **Final verification.** `make check` once (it is `go test -race -count=1 -cover ./...` plus the coverage gate, gofmt, vet, lint, comments, filesize, tidy).
    *Done when:* `make check` is green; `git status --porcelain` shows only the 10 pre-existing untracked docs; the diff adds no exclusion or allow-list entry and no coverage-baseline value; nothing was pushed.

## 6. Fixture sweep (test-only; each converts a planted reserved key into an authored row)

Read side, commit 1:

1. `internal/store/fork_test.go:60-95` `seedForkSource` (used by `TestForkStateCopiesRoundFilesAndLog:17`): the per-round `DiffPath` file → `PutRoundFile`; compare the diff names against the source row, not the disk snapshot.
2. `internal/relevo/show_test.go:181-221` `newShowLiveStore`: the round-1 diff → a row (keeps the fixture's meaning).
3. `internal/relevo/show_test.go:102-150` `archiveShowFixture`: the fixture's `001-diff.patch`/`002-drift.patch` → rows before `Archive` (archive seals via `SealAll`), used by `TestShowArchivedReadsSealedRoundFiles:565`, `:641`, `:676`, `:858`.
4. `internal/serve/serve_test.go:2776-2805` `TestRoundFileDriftRunning`: the drift file → a row (the 404 case before it stays).
5. `cmd/relevo/show_test.go:55-77` `seedShowDiffStore` diff → row; `:183-205` `TestShowGateAndFindings` findings (id `7f2a3c1d`) → row.
6. `cmd/relevo/contract_test.go:300-320` (the `showsections` fixture) diff, drift, findings → rows; `cmd/relevo/main_test.go:362` `TestDiffCommand`, `:472` `TestDiffAnchorsCommand`, `:516`+`:520` `TestDiffDriftCommand` → rows.

Seal side, commit 2:

7. `internal/store/seal_test.go:207-240` `writeSealFixtures`: drop `003-aabbccdd-findings.md` from `sealed` (it is refused now) and put a consult stream (`003-aabbccdd-consult.jsonl`) in its place, so the consult-file seal path stays pinned; `003-aabbccdd-ask.md` stays (ask is not reserved).
8. `internal/relevo/seal_test.go:31-45` `TestClosedRoundSealsOnceTheNextRoundCloses`: author round 1's diff as a row before the ticks; the rest of the assertions stand.

Do not touch: `internal/relevo/show_test.go`'s three findings fixtures (ids `"abc"` are not 8 hex, so they are not reserved), `internal/store/artifact_test.go`, `archive_test.go`, `roundfile_put_test.go`, `internal/relevo/transcript_test.go`, `internal/e2e/**`, `internal/ui/**` fixtures (they read dirs, not stores).

## 7. Mutation checks

| # | Mutation (revert the fix) | Test that must fail |
| --- | --- | --- |
| M1 | delete the reserved branch from `ReadFile` (make it always disk-first) | `TestReadFileRefusesAReservedRoundFileOnDisk` |
| M2 | delete the reserved branch from `StatFile` | `TestStatFileRefusesAReservedRoundFileOnDisk` |
| M3 | delete the reserved skip in `walkRoundDir` | `TestSealRoundLeavesAReservedRoundFileAlone` |

`TestReadFilePrefersDiskForPromptAndStream` has no mutation on purpose: it pins behaviour that must not change, so it passes before and after. The report must say so rather than claim a mutation for it.

## 8. Deleted behaviour (closed list)

1. `Store.ReadFile` no longer returns a reserved key's disk bytes, in any state (row present, or not): diff, drift, builder segments, findings.
2. `Store.StatFile` no longer reports a reserved key's disk size or mtime.
3. A reserved file on disk is no longer a round file: `RoundFiles`, `RoundsOnDisk` and `roundFilesOfDir` do not list it, and `SealRound`/`SealAll`/`Archive`/`forkRoundFiles` never read, seal, remove or copy it — it stays where it is.
4. A reserved key with no row no longer resolves to a file: it resolves to the row (live or archived) if one exists, otherwise to a not-exist miss.
5. `RoundsOnDisk` no longer reports a round whose only file is a reserved plant, so a plant cannot trigger a seal pass.
6. Fixture-only: writing a reserved key with `os.WriteFile`/`os.WriteFile`-style helpers no longer produces a readable, sealable, listed or archive-able round file in the tests that did it (the eight fixtures of §6 author rows instead).

Nothing else is deleted: no function, file, test, exclusion, allow-list entry or baseline value, and no producer path. `PutRoundFile`/`RoundFilePut` keep the upsert; every other key stays disk-first.

## 9. Deliberately not done (say it in the report, with the reason)

- **`ask` stays disk-first.** Relevo writes it as a file when the question does not fit inline (`consult/verify.go:346`), so reserving it would break the oversized staging and leave that file unsealed forever; nothing in production reads it through `ReadFile`. Follow-up: stage the oversized ask as a row, then reserve it.
- **`question` stays disk-first.** No row producer in this tree; it is a captured file by design.
- **Prompt, stream, report, builder log, gate log, consult stream and reader output/artifacts stay disk-first.** The resend case is load-bearing; plants for those keys remain readable, and that is the accepted boundary of this round.
- **`serve/rounds.go:29`** reads the prompt with `os.ReadFile`, not the store. Out of scope (prompt disk-first by design), noted for a future round.
- **A plant in a DONE binding's directory** keeps the directory from being removed by the seal pass; `Archive`/delete still clear it with `RemoveAll`. Reported, no code change.
- `make e2e` (not part of `check`), push, PR, merge.

## 10. Report must include

1. The commits: subject, hash, body's `Fixes #683`, that the plan file rides commit 1 byte-identically, `git log --oneline -2`, nothing pushed, and that HEAD was left detached as found.
2. The §5 classification table as implemented, and the one-line statement that no production caller changed outside `internal/store`.
3. The §6 fixture list: one line per file saying what became a row and why.
4. The mutation results: M1-M3, the exact failing test each, the revert proof, and the no-mutation note for the preference test.
5. The focused commands run (steps 1, 3, 5, 7) and `make check`'s result; coverage baseline untouched; no new exclusion or allow-list entry; `git status --porcelain` shows only the 10 pre-existing untracked docs.
6. The deferred items of §9 with their reasons.
