# Plan: bug sweep batch 2 — runner-writable state dir (#654, #655)

One builder round, two commits, never pushed. The plan itself is committed verbatim as `docs/plans/2026-09-29-bug-sweep-trust.md` in commit 1. If the tree contradicts anything below (or a done-when fails for a reason not stated here), halt and report — do not improvise.

## 1. Seed vs tree (read this first)

The seed's #654 anchors describe code this tree no longer has, so parts of decision 2 are already done and the plan adapts instead of guessing:

- `internal/store/importPresent`, `importBindingFile`, `legacyPresent`, `importLogFile`, the bind.json/log.jsonl/.viewed imports and serve's startup import were deleted by `8f16627c` ("drop the relay→relevo migration machinery"), which is HEAD~3. `grep -rn 'importPresent\|importBindingFile\|legacyPresent' internal` returns nothing; `internal/store/db.go` has no import code.
- Therefore decision 2's first half — "take the legacy import off the runtime read path", "refuse it once relevo.db holds a record", "never take tier/round_tier/gate/cwd/shape/candidate from the imported file" — is already satisfied, more strongly: there is no import at all, so no field can come from a file. This plan must **not** re-add an import in order to refuse it.
- The still-live half of #654 is the name rule: `Store.read` (the shared boundary of every exported read that takes a name) and the `confirmIndex` path, which in this tree is a write and does not pass through `read`, plus serve's `loadBinding`. That is commit 1. A planted-`bind.json` test pins the deleted path so it cannot return.
- Decision 5's #654 tests map as: planted `bind.json` cannot set gate/tier/cwd/… → `TestLoadIgnoresABindFileOnDisk`; a foreign name is refused → the invalid-name tests; "the import is one-shot" has no equivalent (there is no import) — the planted-file test pins the file is never read, adopted or deleted.
- Line drift: the seed's `summary.go:51` is `summary.go:35` here, `seal.go:30` is `seal.go:28`. All anchors below were verified in this tree.
- Decision 3's "audit only the daemon writes into runner-writable directories": read as *enumerate every daemon write that can land under `<state>/<name>/` and put the list in the report*, while the fix stays in `summary.go`, as decision 1 scopes it. The audit list in §7 is the deliverable for the other writers; do not widen the diff.

## 2. Behaviour and cases

### #654 — validate the name at the store's read boundary and at serve's edge

- `Store.read(name, fn)` returns `ValidName`'s own error before `fn` runs. Covers `Load`, `ReadLog`, `ReadLogAfter`, `PendingForMasterMind`.
- `confirmIndex` gets the same first-line refusal: it is reached from `Store.ConfirmIndex` and `Tx.ConfirmIndex` and is not a `read` caller.
- Unchanged: a valid stored name reads as today; a valid unknown name still gives `Load` → `ErrNotFound` and `ReadLog` → `(nil, nil)`; `Save` is untouched.
- New: `"../escape"`, `"UPPER"`, `"a/b"` → the error `ValidName(name)` returns, from `Load`, `ReadLog`, `ReadLogAfter`, `PendingForMasterMind`, `ConfirmIndex`.
- serve `loadBinding` refuses a `PathValue("name")` that is not `ValidName` by returning `store.ErrNotFound`, so every handler keeps its existing 404 branch instead of drawing the 500 a validation error would. The live case is `%2F`: the mux delivers `/v1/bindings/foo%2Fbar` to `{name}` as `"foo/bar"` (verified against go1.27's mux: `PathValue` = `foo/bar`).
- A planted `<state>/<name>/bind.json` is inert: `Load`/`List` answer from `relevo.db`, the record's own tier/round_tier/gate/cwd/shape/candidate win, and the file is never read, consumed or deleted.

### #655 — no daemon write follows a runner-planted symlink

`writeReaderSummary` (reader rounds only):

- `Lstat` the output path: absent → write; an existing regular file → keep it and return the path (today's behaviour); anything else (symlink, directory, fifo, …) → return an error and create nothing.
- The `NNN-<actor>` directory: `Lstat` it before `MkdirAll`; a symlink (even one pointing at a real directory) or any non-directory → error; absent → `MkdirAll`; a real directory → proceed.
- Create the file with `O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW`, 0o644, then write `text+"\n"` (the flags are the race backstop behind the `Lstat`).
- Unchanged: non-readers return before any filesystem work; an empty final message writes nothing and creates no directory; a runner-written regular output file is still the report; every caller still logs the returned error and closes without an output.

## 3. Seams (verified against this tree)

| Seam | Anchor |
| --- | --- |
| `Store.read` — shared read boundary | `internal/store/lifecycle.go:107-111` |
| `Load` → `read` → `load` | `internal/store/lifecycle.go:18-26`, `:302-318` |
| `ReadLog`, `ReadLogAfter`, `PendingForMasterMind` | `internal/store/log.go:204-212`, `:214-222`, `:232-241` |
| `Store.ConfirmIndex` → `Tx.ConfirmIndex` → `s.confirmIndex` | `internal/store/log.go:243-245`, `:271-273`, `:421-476` |
| `ValidName` (the rule the tests compare against) | `internal/store/store.go:123-146` |
| serve `loadBinding` + the 404 branch it feeds | `internal/serve/bindings.go:38-51`, `:264-284` |
| binding record fields the plant must not reach (`CWD`, `Tier`, `RoundTier`, `Gate`, `Shape`, `BuilderCandidate`) | `internal/store/binding.go:99-125` |
| `writeReaderSummary` (Stat/`MkdirAll`/`WriteFile`) | `internal/relevo/summary.go:35-52` (Stat :40, MkdirAll :48, WriteFile :51) |
| `reportPathFor` → `Store.OutputPath` → `ArtifactDir` | `internal/relevo/summary.go:15-20`; `internal/store/paths.go:41-43`, `:33-36` |
| callers of `writeReaderSummary` | `internal/relevo/reconcile.go:343`, `:373`; `stop.go:248`; `headless.go:759` |
| deferred: disk-first `ReadFile`; `SealRound` upsert | `internal/store/seal.go:28-53`; `:301-354`; `internal/store/roundfile_put.go:22-50` |
| deferred: launch-time tier | `internal/relevo/headless.go:214`; `internal/relevo/send.go:274`; `internal/relevo/tier.go:42-43` |
| test fixtures to reuse | `internal/store/helpers_test.go:95` (`putRecordJSON`), `:8` (`newBinding`); `internal/relevo/bind_test.go:42` (`newRuntime`); `internal/relevo/reader_close_test.go:49` (`writeReaderStream`) |

## 4. Steps (done-when each)

1. **Store tests, red half.** Add to `internal/store/store_test.go`, near `TestValidName`: `TestReadRefusesAnInvalidName` (table over `Load`/`ReadLog`/`ReadLogAfter`/`PendingForMasterMind` with `"../escape"` and `"UPPER"`; each error must equal `ValidName(name)`'s own error; plus `Load("ghost")` → `errors.Is(err, ErrNotFound)` and `ReadLog("ghost")` → `(nil, nil)`), `TestConfirmIndexRefusesAnInvalidName` (`s.ConfirmIndex("../escape", 0, "")` equals `ValidName`'s error), and `TestLoadIgnoresABindFileOnDisk` (save `newBinding("webshop","/repo")`; write `<s.Dir("webshop")>/bind.json` carrying `{"name":…,"cwd":"/evil","gate":…,"tier":"yolo","round_tier":"yolo","shape":"reader","candidate":"evil"}`; `Load`/`List` return the record's fields and not the file's; the file still exists byte-identical after `Load`, `List` and `Save`; a subtest plants `{"name":"other",…}` and pins no `other` record appears).
   *Done when:* `go test ./internal/store/ -run 'TestReadRefusesAnInvalidName|TestConfirmIndexRefusesAnInvalidName|TestLoadIgnoresABindFileOnDisk' -count=1` shows the two name tests failing with `ErrNotFound` and the planted-file test passing.
2. **Store guard.** `ValidName(name)` as the first statement of `read` (`lifecycle.go:109`) and of `confirmIndex` (`log.go:421`). Comments say why only (a name becomes a path in the sibling helpers; this is the one boundary the name-taking reads share) — no issue numbers or history.
   *Done when:* the step 1 command passes.
3. **serve test, red half.** Add `TestGetBindingRefusesAnInvalidName` to `internal/serve/serve_test.go`: `newTestServer(t, 0)`, enrol a generated keypair, signed `GET /v1/bindings/foo%2Fbar` and `GET /v1/bindings/UPPER`, expect `404` with `remote.CodeNotFound`.
   *Done when:* `go test ./internal/serve/ -run TestGetBindingRefusesAnInvalidName -count=1` fails with 500 `malformed client id`.
4. **serve guard.** In `loadBinding` (`bindings.go:38`), after the `caller == ""` check: `store.ValidName(name)` → return `store.ErrNotFound`, before a runtime is built.
   *Done when:* the step 3 command passes.
5. **Mutations M1–M4** (§5): apply each, run its named test, confirm failure, restore.
   *Done when:* all four fail under their mutation and pass after the revert.
6. **Commit 1.** Write this plan verbatim to `docs/plans/2026-09-29-bug-sweep-trust.md`; `gofmt -w` the new/changed Go files; `git add` plan + store + serve files; commit `fix(store): …` whose body ends `Fixes #654` and says the import this finding names was already deleted and is now pinned by test. Do not push.
   *Done when:* `git show --stat HEAD` lists the plan and only the §3 store/serve files, and `git log -1 --format=%s` is the fix(store) subject.
7. **#655 tests, red half.** New `internal/relevo/summary_test.go`:
   - `TestWriteReaderSummaryRefusesADanglingSymlink`: `newRuntime(t)`; reader binding `{Name:"reader-bind", Role:"reviewer", Shape:store.ShapeReader, Round:1, Builder:store.Endpoint{Kind:"claude"}}`; `writeReaderStream(t, rt, "reader-bind", 1, "final message")`; `out := reportPathFor(rt, b)`; `os.MkdirAll(filepath.Dir(out), 0o755)`; `target := filepath.Join(t.TempDir(), "pwned")`; `os.Symlink(target, out)`; call `writeReaderSummary(rt, b)`; want a non-nil error and `os.Lstat(target)` → `fs.ErrNotExist`.
   - `TestWriteReaderSummaryRefusesASymlinkedArtifactDir`: same setup; `target := t.TempDir()`; `os.MkdirAll(rt.Store.Dir(b.Name), 0o755)`; `os.Symlink(target, filepath.Dir(out))`; call; want a non-nil error and `target` empty (`os.ReadDir` length 0).
   *Done when:* `go test ./internal/relevo/ -run 'TestWriteReaderSummaryRefuses' -count=1` fails both, showing the outside file was created.
8. **#655 fix.** `summary.go:35-52` per §2: the two `Lstat` rules and the `O_EXCL|O_NOFOLLOW` create (a small unexported helper in the file is fine); keep the `text == ""` early return before any filesystem work; `writeReaderSummary` stays ≤70 lines and the file ≤600.
   *Done when:* the step 7 command passes.
9. **Audit.** Run `grep -rnE 'os\.(WriteFile|OpenFile|Create|CreateTemp|MkdirAll|Rename)' --include='*.go' internal cmd | grep -v '_test.go'` and list every non-test daemon write that can resolve under `<state>/<name>/`. Expected rows: `summary.go:48,51` (fixed), `reconcile.go:558` (in-place strip of the reader output), `headless.go:511-519` (`appendLines` to a legacy round's builder log), `proc/proc.go:246,250` (round log/stream opened with `O_CREATE`), `send.go:513`, `repair.go:117`, `remote_send.go:179` (prompt staging), `daemon.go:498` (empty binding-dir removal). Add anything the sweep finds.
   *Done when:* the report has one row per path — file:line, what it writes, disposition.
10. **Mutations M5–M6** (§5), same shape.
    *Done when:* both fail their named test and pass after the revert.
11. **Commit 2.** `gofmt -w`, `git add` the relevo files (including the new test), commit `fix(relevo): …` with body `Fixes #655` and one line saying the write audit is in the report. Do not push.
    *Done when:* `git log --oneline -2` shows exactly the two commits.
12. **Final verification.** Run `go test ./internal/store/ ./internal/serve/ ./internal/relevo/ -count=1`, then `make check` once.
    *Done when:* `make check` is green, `git status --porcelain` is empty, and the diff contains no new exclusion/allow-list entry and no coverage-baseline change.

## 5. Mutation checks

| # | Mutation (revert the fix) | Test that must fail |
| --- | --- | --- |
| M1 | delete `ValidName` from `Store.read` (`lifecycle.go:109`) | `TestReadRefusesAnInvalidName` |
| M2 | delete `ValidName` from `confirmIndex` (`log.go:421`) | `TestConfirmIndexRefusesAnInvalidName` |
| M3 | delete the `ValidName` guard from `loadBinding` (`bindings.go:38`) | `TestGetBindingRefusesAnInvalidName` (gets 500, wants 404) |
| M4 | make `load` (`lifecycle.go:302`) read and return the planted `bind.json` before `RecordGet` | `TestLoadIgnoresABindFileOnDisk` |
| M5 | restore `os.Stat(path)` + `os.WriteFile(path, …)` in `writeReaderSummary` | `TestWriteReaderSummaryRefusesADanglingSymlink` |
| M6 | drop the artifact-dir `Lstat` and let `MkdirAll` run | `TestWriteReaderSummaryRefusesASymlinkedArtifactDir` |

Note for M5: mutating only the open flags (back to `O_CREATE|O_TRUNC`) fails no test, because the `Lstat` refuses first. The flags are the race backstop; the report must not claim the tests pin them.

## 6. Deleted behaviour (closed list)

1. A non-regular file at the reader's output path is no longer treated as the round's output; it is refused with an error (symlink to an existing file, directory, fifo, …).
2. The daemon no longer creates the reader output by following a symlink at the output path.
3. The daemon no longer creates or descends the `NNN-<actor>` directory when that path is a symlink.
4. A malformed binding name no longer reads as not-found at the store boundary: `Load`, `ReadLog`, `ReadLogAfter`, `PendingForMasterMind`, `ConfirmIndex` answer with `ValidName`'s error.
5. A malformed `{name}` on serve no longer answers 500 `malformed client id`; it answers 404.

Nothing else is deleted: no functions, files, tests, exclusions, allow-list entries or baseline values.

## 7. Deliberately not done (say it in the report, don't half-do it)

- **`ReadFile`/`SealRound` shadowing** (`seal.go:28`, `:301`; `roundfile_put.go:22`). Disk-first is load-bearing: `PromptPath`/`StreamPath` resolve unsealed round files through `StatFile`, and a resend re-stages a prompt on disk for a round whose earlier prompt may be sealed — "prefer the row" would hide that fresh file. Closing it properly needs a reserved-name policy for the keys relevo authors only in the database (diff, drift, findings, builder-segments) plus an audit of every `ReadFile` caller: not small, not this round. Report it as a follow-up with this reason.
- **`checkTierCap` re-check before launch** (`headless.go:214`, `send.go:274`; `tier.go:42`). The import that could set a stored tier is gone and only a writer of `relevo.db` (outside this threat model) can change one; the seed scoped #654 to the import path and the name. Report as deferred hardening.
- **The other same-class daemon writes** (§4 step 9): audited and reported, not changed — decision 1 puts #655 in `summary.go`. The report should flag `proc/proc.go:246,250` (the round's log/stream are created through a possible symlink) and `reconcile.go:558` as the two worth a follow-up.
- `make e2e` (not part of `check`), push, PR, merge.

## 8. Report must include

1. The seed-vs-tree note of §1: the import machinery was already deleted (`8f16627c`), what this round did instead, and every seed anchor that moved, with the line it is now.
2. Commit 1 and commit 2 (subject + hash), that the plan file rides commit 1 byte-identically, `git log --oneline -2`, nothing pushed, tree clean.
3. The audit table of step 9.
4. Mutation results: each of M1–M6, the exact test that failed, the restored state, and the M5 flags caveat.
5. The focused commands run (steps 1, 3, 7, 12's package run) and `make check`'s result; coverage baseline untouched; no new exclusions.
6. The deferred items of §7 with their reasons.
