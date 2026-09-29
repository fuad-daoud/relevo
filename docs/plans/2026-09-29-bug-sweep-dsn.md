# Plan: bug sweep batch 3 — DSN escaping, serve tmp mode, launch tier cap (#681, #686, #684)

One builder round, three commits, never pushed. The plan itself is committed verbatim as `docs/plans/2026-09-29-bug-sweep-dsn.md` in commit 1. If the tree contradicts anything below, or a done-when fails for a reason not stated here, halt and report — do not improvise.

## 1. Seed vs tree (read this first)

All three issues are open in this tree, every seed anchor resolves, and no seed point contradicts the code, so the round proceeds. Drift and one uncovered site:

- **#681**: the issue says `db.go:90`; in this tree the write DSN is `internal/db/db.go:99` and the read-only DSN is `internal/db/config.go:197`. Both are the two production DSNs decision 2 names. Raw `"file:"+path` DSNs in `db_test.go:90`, `helpers_test.go:43,76` and `migrate_test.go:14` are test fixtures and stay (§7).
- **#686**: `internal/serve/auth.go:38` is `os.MkdirAll(tmpDir, 0o755)`, as the seed says. The mode to reuse is `stateRootMode` in `internal/store/store.go:30-33` — unexported today, so this round exports it as `StateRootMode` (rename only; its three uses at `store.go:164`, `internal/store/db.go:25`, `internal/store/daemonlock.go:61` change spelling). `serve.New` already creates the same directory 0700 (`serve.go:106-107`). The #660 test to mirror is `TestEnsureStateRootIsOwnerOnly` (`internal/serve/root_test.go:71-97`).
- **#684**: `internal/relevo/headless.go:214` (spawn) and `internal/relevo/send.go:274` (preflight) both use `effectiveTier(b)` with no cap check, as the seed says. `internal/relevo/tier.go:42` is `checkTierCap`'s doc comment in this tree (the function begins at `:45`): `checkTierCap` **stays** in tier.go, unchanged, and is reused by the new launch check. The legacy `resolveTier` (`tier.go:38`, built on `legacyRegistry` for pre-roles.json callers) also **stays** — its own comment says tests keep it. The tree has a third launch path the seed does not list: `resumeRound` (`headless.go:341-374`, tier used at `:361`). The rule is "before every launch", so this round guards it too and says so. The sibling plan `docs/plans/2026-09-29-bug-sweep-trust.md` §7 deferred exactly this re-check, so #684 is the follow-up that plan names.
- The worktree carries untracked draft plans under `docs/plans/`; commit only the files this plan names (no `git add -A`).

## 2. Behaviour and cases

### #681 — the path inside the `file:` DSN

- **Form chosen: percent-encoding for the `file:` URI**, produced as `(&url.URL{Scheme: "file", Path: path}).String()` + `"?"` + params, factored into one unexported helper `fileDSN(path, params string) string` next to `open` in `internal/db/db.go`. Why not a no-URI DSN form: modernc v1.59.0 splits any DSN at the first `?` (`conn.go:67-76`) and keeps a `file:` DSN whole for SQLite's URI parser, so a bare-path DSN still truncates on `?`; percent-encoding fixes `#`, `?` and `%` at once and keeps `mode=ro` and the `_pragma` parameters in the DSN. Verified against the module cache: `url.URL{Scheme:"file",Path:"/t/a#b?c%d/x"}.String()` is `file:///t/a%23b%3Fc%25d/x`, and SQLite opens exactly that file.
- `db.go:99` calls `fileDSN(path, fmt.Sprintf("_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=journal_size_limit(%d)", busy, journalSizeLimit))`; `config.go:197` calls `fileDSN(path, "mode=ro&_pragma=busy_timeout(5000)")`.
- Unchanged: `seedFromTemplate`, `ensurePrivateFile`, `chmodPrivate` and the `-wal`/`-shm` chmods keep taking the raw filesystem path; every caller passes a plain path (store root, `cmd/relevo` opens absolute paths); `fileDSN` documents that its path must be absolute, which every call site satisfies.
- Cases: a path containing `#`, `?` or `%` opens and migrates that exact file; the truncated prefix is never created; `OpenReadOnly` on such a path reads the same file and reports its schema (today the `#` swallows `mode=ro` and the read-only open silently opens a different file read-write).

### #686 — the request temp directory mode

- `auth.go:38` uses `store.StateRootMode` (0700); `serve.go:93` (`EnsureStateRoot`) and `serve.go:107` (`New`) use the same constant so the package spells the mode once (spelling only, no behaviour change).
- **`MkdirAll` never chmods an existing directory**: a `<root>/tmp` that already exists world-readable keeps its mode, and this round adds no chmod and no repair.
- Unchanged: the ignored `MkdirAll` error, the temp-file name, the sweep, the transport and the auth flow.
- Case: a caller that reaches `authenticate` before `New` (root exists, `<root>/tmp` does not) creates it 0700.

### #684 — the stored tier at launch

- New helper `launchTier(b store.Binding, pol policy.Policy, allowYolo bool) (harness.Tier, error)` in `tier.go` beside `checkTierCap`: derive `effectiveTier(b)`, run the existing `checkTierCap`, return the tier. One place states the rule "an over-cap stored tier never launches without the explicit allowance".
- `sendPreflight` (`send.go:273-275`): the stored-tier branch uses `launchTier(b, rt.Policy, opts.AllowYolo)`. The explicit `--tier` check at `:180-190` stays and wins over the stored tier, so `send --tier edit` on an over-cap record launches without the flag.
- `startRound` (`headless.go:197-219`) gains an `allowYolo bool` and derives its tier with `launchTier`; `Send` passes `opts.AllowYolo` (`send.go:544`); the other six production callers pass `false`: `queue.go:68` (Admit), `repair.go:136`, `switch.go:175`, `headless.go:922/930/934` (lost-builder relaunch).
- `resumeRound` (`headless.go:341-374`) derives its tier with `launchTier(b, rt.Policy, false)`; its callers are `nudge.go:92` and `headless.go:915`, both daemon paths.
- **The `--allow-yolo` interaction (explicit escape)**: the flag's own contract is "for this command", so the stored tier is re-checked at every launch and only a launch carrying the allowance starts. `relevo send --allow-yolo` is the only path that carries it. Stated consequences the report must repeat: an opencode reader (whose lowest writing tier is yolo) and any record stored while `max_tier` was raised must be sent with `--allow-yolo`; a **deferred** send (`--defer`) admitted by `serve.admit`, a repair round, a mid-round switch, a nudge/resume and a lost-builder relaunch of an over-cap record refuse and surface NEEDS YOU (`builder spawn failed: tier yolo exceeds max_tier edit; …`) instead of launching; a durable above-default ceiling is `max_tier` in policy.json.
- Unchanged: harness never refuses (`Above` is false when either side is harness); the reader floor, `bind`'s and `add`'s checks, `applyBuilder`'s check and serve's own `checkTierCap(..., false)` (`served.go:215`) stay; `--dry-run` refuses identically because it runs the same preflight.
- Cases: stored tier at or below cap launches as today; over-cap without the flag refuses from the preflight with `ErrTierAboveMax`, stages nothing, starts nothing; with the flag the launch proceeds at the stored tier; daemon-driven launches of the same record refuse.

## 3. Seams (verified in this tree)

| Seam | Anchor |
| --- | --- |
| #681 write DSN, file creation and chmods | `internal/db/db.go:99` (`ensurePrivateFile` :104, chmods :126-133) |
| #681 read-only DSN | `internal/db/config.go:192-197` (`openReadOnly`) |
| #681 test home | `internal/db/db_test.go:16`; `internal/db/config_test.go:138,146` |
| #686 the 0755 creation | `internal/serve/auth.go:37-38`; wired at `internal/serve/routes.go:78` |
| #686 state-root mode and its users | `internal/store/store.go:30-33`, `:164`; `internal/store/db.go:25`; `internal/store/daemonlock.go:61` |
| #686 serve's other 0700 sites | `internal/serve/serve.go:92-93`, `:106-107` |
| #686 pattern to mirror | `internal/serve/root_test.go:71-97` |
| #684 preflight | `internal/relevo/send.go:180-190`, `:273-275`, `:364`, `:498-500`, `:544` |
| #684 spawn and resume | `internal/relevo/headless.go:197-219` (tier :214), `:341-374` (tier :361), `:915`, `:922`, `:930`, `:934` |
| #684 daemon launchers | `internal/relevo/queue.go:68`; `repair.go:136`; `switch.go:175`; `nudge.go:92` |
| #684 helper and cousins | `internal/relevo/tier.go:38-40` (`resolveTier`, stays), `:45-51` (`checkTierCap`, reused), `:80-92` (`effectiveTier`) |
| #684 existing checks kept | `internal/relevo/bind.go:662`, `:772`; `builder_change.go:53-58`; `served.go:215` |
| #684 fixtures | `internal/relevo/bind_test.go:42` (`newRuntime`); `fixture_test.go:37` (`seedHeadless`); `fake_test.go:670` (`newFakeRunner`); `send_test.go:440-505` (yolo bind/send) |
| ceiling and order | `internal/policy/policy.go:241-249` (`MaxTierOrDefault`); `internal/harness/tier.go:29-45` (`Rank`/`Above`) |

## 4. Steps (done-when each)

Each *Done when* is the command to run; run only that focused command until it is green.

1. **Plan + #681 test (red half).** Copy the round's saved plan (`~/.local/state/relevo/bug-dsn-plan/001-lite-planner/plan.md`) byte-identically to `docs/plans/2026-09-29-bug-sweep-dsn.md`. Add `TestOpenPathWithHashOrQuestionUsesThatFile` to `internal/db/db_test.go`, table over `a#b/relevo.db` and `a?b/relevo.db` in separate `t.TempDir()`s with the real parent created: `Open(path)` → nil; `OpenReadOnly(path)` → nil and `SchemaVersions()` gives `have == know > 0`; and the truncated prefix (`<dir>/a`) does not exist after either open.
   *Done when:* `go test ./internal/db/ -run '^TestOpenPathWithHashOrQuestionUsesThatFile$' -count=1` fails on the exact-file/schema assertion (red).
2. **#681 fix.** Add `fileDSN` and switch the two production DSNs (§2).
   *Done when:* the step 1 command passes.
3. **Mutations M1a/M1b** (§5), then restore.
   *Done when:* each mutation fails the step 1 test and the restored tree passes it again.
4. **Commit 1.** `gofmt -w` the changed files; `git add docs/plans/2026-09-29-bug-sweep-dsn.md internal/db/db.go internal/db/config.go internal/db/db_test.go`; subject `fix(db): escape the path in the file: DSN`, body ends `Fixes #681`.
   *Done when:* `git show --stat HEAD` lists only those four paths, `git log -1 --format=%s` is that subject, and nothing is pushed.
5. **#686 test (red half).** New `internal/serve/auth_test.go` with `TestAuthenticateCreatesTmpOwnerOnly`, same umask-control shape as `root_test.go`: control dir 0755 and the skip when the owner bits are masked; wrap a no-op handler with `(&Server{cfg: Config{Root: root, MaxBundleBytes: 1 << 20, Now: time.Now}}).authenticate`; serve an unsigned `httptest` request (Verify stops at the empty client header; `clients`/`nonces` are never called); stat `<root>/tmp` == 0700.
   *Done when:* `go test ./internal/serve/ -run '^TestAuthenticateCreatesTmpOwnerOnly$' -count=1` fails with mode 755 (red).
6. **#686 fix.** Export `StateRootMode` (rename `stateRootMode`, three store uses); use it at `auth.go:38`, `serve.go:93`, `serve.go:107`.
   *Done when:* the step 5 command passes.
7. **Mutation M2**, then restore. *Done when:* M2 fails the step 5 test (umask caveat in §5) and the restored tree passes.
8. **Commit 2.** `git add` the six store/serve paths (incl. the new test); subject `fix(serve): create the request temp directory owner-only`, body ends `Fixes #686`.
   *Done when:* `git show --stat HEAD` lists only those paths and the subject matches.
9. **#684 tests (red half).** Add `TestSendRefusesStoredTierAboveMaxWithoutAllowYolo` to `internal/relevo/send_test.go` and `TestStartRoundRefusesStoredTierAboveMaxWithoutAllowYolo` / `TestResumeRoundRefusesStoredTierAboveMaxWithoutAllowYolo` to `internal/relevo/headless_test.go`. Each builds a claude binding with `Tier: "yolo", AllowYolo: true` (stored above the default cap, exactly the record #684 names): the no-allowance call returns `ErrTierAboveMax` and records no `Start`; for the send test it must also leave the round unstaged (`PromptPath` absent); the allowance-carrying call starts exactly one process whose argv carries `--dangerously-skip-permissions`. The resume test calls `resumeRound(..., "sess-1", "p", false|true)`.
   *Done when:* `go test ./internal/relevo/ -run '^(TestSendRefusesStoredTierAboveMaxWithoutAllowYolo|TestStartRoundRefusesStoredTierAboveMaxWithoutAllowYolo|TestResumeRoundRefusesStoredTierAboveMaxWithoutAllowYolo)$' -count=1` fails: the no-flag calls launch today.
10. **#684 fix.** Add `launchTier`; wire `send.go:273-275`; give `startRound` and `resumeRound` their `allowYolo bool` and use `launchTier`; `Send` passes `opts.AllowYolo`, the six other production callers `false`, `nudge.go:92`/`headless.go:915` `false`; add the literal to every direct test caller (22 `startRound` sites: `cpus_test.go:193,199,238,245,277,303,345,371`; `headless_test.go:222,252,296,312,361,386,401,4007,4038,4067`; `roles_runtime_test.go:231,251`; `writer_role_test.go:140,441` — all `false`; none exercises the allowance today). Update the two doc comments that state the tier contract (`preflight.tier`, `startRound`'s preconditions). Comments say why only, no issue numbers.
   *Done when:* the step 9 command passes.
11. **Mutations M3a–M3c** (§5), then restore each.
   *Done when:* each mutation fails its named test and the restored tree passes it.
12. **Commit 3.** `gofmt -w`, `git add` the relevo files (incl. both test files); subject `fix(relevo): re-check the stored tier against max_tier before every launch`, body ends `Fixes #684` and notes the daemon-path refusal and that this is defence in depth.
   *Done when:* `git log --oneline -3` shows exactly the three commits; nothing pushed.
13. **Final verification.** `go test ./internal/db/ ./internal/store/ ./internal/serve/ ./internal/relevo/ -count=1`, then `make check` exactly once.
   *Done when:* `make check` is green, `git status --porcelain` is empty, and the diff adds no lint/comment/file-size exclusion, no allow-list entry, and changes no coverage baseline value.

No new test lives in `cmd/relevo` and none spawns a harness: `fakeRunner` is in-process and the serve test is `httptest` only (CI has no harness binary and no network).

## 5. Mutation checks

| # | Mutation (revert the fix) | Test that must fail |
| --- | --- | --- |
| M1a | restore `dsn := fmt.Sprintf("file:%s?...", path, …)` at `db.go:99` | `TestOpenPathWithHashOrQuestionUsesThatFile` — the exact file stays empty while the truncated prefix gets the schema |
| M1b | restore `"file:" + path + "?mode=ro&…"` at `config.go:197` | the same test — the read-only open reads the truncated file (and creates it read-write, because `#` swallows `mode=ro`), so `have == 0` |
| M2 | `0o755` again at `auth.go:38` | `TestAuthenticateCreatesTmpOwnerOnly` — mode 755 (fails only when the umask leaves the owner bits distinguishable, which the control check states) |
| M3a | drop the `launchTier` call in `sendPreflight` (`send.go:273-275`) | `TestSendRefusesStoredTierAboveMaxWithoutAllowYolo` — the no-flag send stages the plan and reaches the spawn; the test's "nothing staged" assertion is what makes this fail, so it must be present |
| M3b | make `startRound` ignore its `allowYolo` (always allowed) | `TestStartRoundRefusesStoredTierAboveMaxWithoutAllowYolo` — the `false` call launches |
| M3c | drop the `launchTier` guard in `resumeRound` | `TestResumeRoundRefusesStoredTierAboveMaxWithoutAllowYolo` — the `false` call launches |

Run each mutation in isolation, restore before the next, and record the exact failing test and the restored result.

## 6. Deleted behaviour (closed list)

1. `<root>/tmp` is never again created world-readable from `authenticate`; its creation mode is `store.StateRootMode` (0700). `MkdirAll` still never chmods an existing directory, so an existing 0755 `<root>/tmp` stays as it is — stated, not changed.
2. The unescaped `file:` DSN compositions at `db.go:99` and `config.go:197` are gone; a path containing `#`, `?` or `%` no longer truncates to another file, and a read-only open of such a path no longer opens a different file read-write.
3. The unexported `stateRootMode` name is gone; `StateRootMode` is its replacement (rename only).
4. The `0o700` literals at `serve.go:93` and `serve.go:107` are gone where the constant is used (spelling only).
5. An over-cap stored tier no longer launches silently: `startRound`/`resumeRound` require an explicit allowance, and every daemon-driven caller passes none, so queue admit, repair, mid-round switch, nudge/resume and lost-builder relaunch of such a record refuse instead of launching.
6. Nothing else is deleted: no functions, files, tests, allow-list entries, file-size or comment exclusions, and no coverage-baseline value.

## 7. Deliberately not done (say it in the report, don't half-do it)

- **Raw DSNs in db tests** (`db_test.go:90`, `helpers_test.go:43,76`, `migrate_test.go:14`): fixtures under literal test names; decision 2 scopes the fix to the two production DSNs. If one ever gains a `#` it truncates exactly like the bug did.
- **Verify/consult and probe launches** (`consult/verify.go:278`, `availability/probe.go:134`, `relevo/bind.go:772` dry argv): their tier comes from `verifyTier`/`probeTier`/bind's own resolution, never from a stored binding row, so #684's stored-tier rule does not apply.
- **Persisting the `--allow-yolo` allowance for the round**: the flag is per command by design; a durable above-cap ceiling is policy `max_tier`. The daemon-path consequence of §2 is the deliberate cost and belongs in the report.
- **No chmod of an existing `<root>/tmp`**: the seed's own statement that `MkdirAll` never chmods is pinned, not worked around.
- `make e2e` (not part of `check`), push, PR, merge.

## 8. Report must include

1. The seed-vs-tree result of §1: all anchors resolve; `db.go:90` → `:99`; `checkTierCap` stays at `tier.go:45` (doc at `:42`) and is reused; `resumeRound` added as the third launch site and why.
2. The three commits (subject + hash), that the plan file rides commit 1 byte-identically, `git log --oneline -3`, nothing pushed, tree clean.
3. The focused commands of steps 1, 5, 9 and 13, and `make check`'s single end-of-round result; coverage baseline untouched, no new exclusion or allow-list entry.
4. Mutations M1a–M3c: each mutation, the exact test that failed, the restore, and the M2 umask caveat.
5. The #684 `--allow-yolo` consequences (daemon-driven launches refuse; an opencode reader needs the flag) and the §7 deferred items with their reasons.
