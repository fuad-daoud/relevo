# MasterMind consent R1: the repo answer, the Claude hook, doctor

Spec: `docs/specs/2026-09-27-mastermind-consent-design.md` (#632). This round is
the consent core; the opencode guide and injection are R2.

## Goal

A repository's answer (`yes` / `no` / unset) is stored on its `repo` row, the
Claude `SessionStart` hook respects it, and `relevo doctor` shows it. After
this round, a Claude session in a repo that has not answered registers nothing
and instead is told to ask.

## Facts (verified against origin/main 72a90e38)

- `repo` has `id, origin_url, common_dir, first_seen` (`internal/db/migrations/001_initial.sql`).
  `db.UpsertRepo` keys on `origin_url` first, then `common_dir`
  (`internal/db/write.go:58`). The latest migration is 009.
- `mastermindInitHook` (`cmd/relevo/mastermind.go:157`) parses the hook
  payload, opens the db for `PriorID` with a 2 s timeout
  (`mastermindPriorID`), runs `mastermind.Init`, and prints the hook envelope.
- `captureRepo(ctx, rt, cwd)` returns a `*store.RepoRef` from
  `rt.Git.RepoFacts` (`internal/relevo/repo.go:13`).
- `mastermind.HookOutput` / `HookOutputNoEnv` / `HookNote` build the envelope
  (`internal/mastermind/hook.go:85-96`). `Guide()` is the shared text.
- Doctor's MasterMind rows come from `doctor.MasterMindChecks` over
  `mastermindCheckInput` (`cmd/relevo/doctor_checks.go:246`).

## Steps

1. **Migration 010** `internal/db/migrations/010_repo_consent.sql`:
   `ALTER TABLE repo ADD COLUMN mastermind_consent TEXT;` and `consent_at TEXT`,
   with a header in the 001 dialect. Bump nothing else; the schema version is
   the file's number.
2. **db consent ops** in `internal/db`:
   - `type Consent string` with `ConsentUnset`, `ConsentYes`, `ConsentNo`,
     `Valid()`;
   - `(t *Tx) SetRepoConsent(ref Repo, c Consent, now time.Time) (string, error)`
     -- `UpsertRepo` then write the two columns;
   - `(d *DB) SetRepoConsent(...)`;
   - `(d *DB) RepoConsent(ref Repo) (Consent, error)` -- a single SELECT by
     `origin_url` (when set) else `common_dir`; no row or NULL reads as unset.
   - "Later key wins on conflict" needs no code: the ref carries both, and the
     read tries `origin_url` then `common_dir`.
3. **Consent rendering** in `internal/mastermind/consent.go`:
   - `ConsentText(state Consent, rec *Record) string` per spec §5. `yes` +
     record = `hookContext(rec) + "\n\n" + Guide()`; `yes` no record = `Guide()`;
     `unset` = `AskNote`; `no` = "";
   - `AskNote` is the constant from spec §5, and `AskNote`'s command list is
     pinned by a test so a rename of `enable`/`disable` fails it.
   - The hook gains `HookConsent(text string) []byte`: `{}` when the text is
     empty, else the envelope carrying it.
4. **CLI verbs** in `cmd/relevo/mastermind.go`:
   - `relevo mastermind enable [--repo]`: resolve cwd's repo (`captureRepo`);
     with `--repo`, write `yes`; then register the calling session through the
     same path `init` uses (detect, host pid, PriorID) and print what happened.
     With no repo and `--repo`, refuse with the spec's non-git message.
   - `relevo mastermind disable [--repo]`: with `--repo`, write `no` (refuse
     when the repo is unresolvable); without, `Forget` the resolved record.
   - Usage text updated; `mastermind help` lists all five verbs.
5. **Hook gating** in `mastermindInitHook`: resolve the repo and its consent
   first; `unset` (or any read failure, or no repo) prints the ask-note or `{}`
   and never calls `Init`; `no` prints `{}`; `yes` runs today's path. The
   consent read rides the same timed-open goroutine as `PriorID`, so the hook
   still never blocks on sqlite.
6. **Doctor row**: `mastermindCheckInput` gains the current repo's consent (or
   "unset"/"n/a"), and `doctor.MasterMindChecks` adds one row naming the
   answer with the fix command.
7. **Tests**:
   - `internal/db`: migration on a v9 file, round-trip, NULL, origin-only and
     common-dir-only keys.
   - `internal/mastermind`: the four `ConsentText` states with and without a
     record; `AskNote` names the three commands.
   - `cmd/relevo`: hook branches with a temp XDG root and a temp git repo
     (no harness, no network); `enable --repo` writes `yes` and registers;
     `disable --repo` writes `no`; doctor row text.
8. **Verification**: `make check`; `make e2e` unchanged. Mutation: make
   `ConsentText` ignore the state, and a named test fails; make the hook call
   `Init` on `unset`, and the hook test fails.

## Out of scope

opencode (`guide`, `server.ts`, `tui.tsx`) is R2. MCP registration is later.
