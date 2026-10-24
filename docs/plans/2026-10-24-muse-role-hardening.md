# Plan: muse writer roles must not report results they never saw (#1358)

## Goal

One librarian round, agent text only, no Go behaviour change. Harden the three
muse writer roles against the three observed 2026-10-24 failures: a gate
reported "exit 0" that was actually still `background_running`, a `status: done`
with 35 files uncommitted, and a repair that narrowed product code to fit an old
test.

## Seed-vs-code correction (read first)

The seed says the `*.muse.md` files are "rendered from a source" and orders
editing that source. That is wrong for shipped agents, and the round must not
follow it:

- `internal/agentsrc/source.go:3` (package comment): "The shipped agents under
  `internal/harness/agents` are hand-maintained per kind." There is no
  single-source file for shipped roles; the `agentsrc` source format
  (`internal/agentsrc/source.go`, `render.go`) is for *custom* agents carried in
  `relevo config` (`internal/roles/actors*.go`, `internal/config/agentfiles.go`).
- The `<!-- ... rendered by relevo from its source ... -->` header on the
  `*.muse.md` files is the muse-kind render header (`internal/agentsrc/render.go:58`).
  On shipped files it guards the *installed copy* (`~/.config/muse/agents/`,
  installed verbatim from the embedded bytes via `internal/harness/agents.go`
  `//go:embed` + `harness.Install` in `internal/harness/install.go`), not the
  repo copy. The repo files ARE the source: edit them directly.
- The sync step the seed names (`scripts/agents-shipped.sh --write`) is real
  but is not "keeping shipped copies in sync": it appends the edited files'
  new hashes to `internal/harness/agents/shipped.sha256`. It is still required,
  because `scripts/agents-shipped_test.sh` ("the real working tree passes")
  runs inside `make check-scripts`, which is inside `check-static`, which is
  inside the gate. Skip it and the gate fails.

## Behaviour and cases

Add to the muse writer roles only. Short sentences, in each file's existing
voice (caps headings, imperatives; see the opencode plan-executor's
harness-specific background sentence at
`internal/harness/agents/plan-executor.opencode.md:174` as the pattern to mirror).

1. Foreground gate with muse session polling (all three files). A gate whose
   tool result says `background_running` (or names a `session_id`) has not
   finished: poll that session until it exits and read the exit code. Never
   report a command's result whose exit was not seen. Case covered: the
   `make check-affected 2>&1 | tail -25` incident from issue #1358.
2. Commit-all-before-report (all three files). Every change the round made is
   committed before the report is written; a `status: done` with uncommitted
   work (other than the report file itself where the role writes one) is a
   failed round, not a done one. Case covered: the 35-uncommitted-files done.
3. Failing pre-existing test (plan-executor only). When the plan's intended
   behaviour change breaks a pre-existing test, update the test to the new
   intended behaviour; never narrow the change to fit the old test. Case
   covered: the repair round that weakened the fix. Does NOT apply to
   librarian/designer: both forbid touching code or tests already ("A round
   that changes code is a failed round", librarian.muse.md:15;
   designer.muse.md:45), so there is nothing to add there.

## Non-muse variants: what is missing where

- The generic foreground-gate rule already exists in all four non-muse
  plan-executor variants (claude:172, codex:103, agy:152, opencode:172) and
  the corresponding librarian/designer sentences. Only opencode adds a
  harness-specific background sentence (opencode.md:174). Do NOT touch
  non-muse files in this round.
- The commit-before-report rule and the test-update rule exist in NO variant
  of ANY kind (verified by full-text search over
  `internal/harness/agents/`). The commit rule is arguably needed everywhere,
  but expanding scope to 20+ files breaks "one small round": leave it as a
  named follow-up (see report section).

## Seams

- `internal/harness/agents/plan-executor.muse.md` (129 lines): GATE section
  lines 96-98 (append muse session-polling sentence); HANDLING PROBLEMS
  lines 64-67 or OUTPUT FORMAT lines 113-121 (commit rule + test-update rule,
  wherever each reads naturally, at most one place each).
- `internal/harness/agents/librarian.muse.md` (61 lines): gate sentence
  line 35 under ONE WRITER, IN THE FOREGROUND (lines 29-35); commit rule in
  THE ROUND lines 49-51.
- `internal/harness/agents/designer.muse.md` (75 lines): gate sentence
  line 53 under ONE WRITER (lines 47-53); commit rule in THE ROUND line 65.
- Out of scope (readers; run no gates, make no commits):
  `researcher.muse.md`, `reviewer.muse.md`, `security-reviewer.muse.md`,
  `architect.muse.md`.
- Must NOT edit (agent text only): `internal/harness/agents_test.go`,
  `internal/harness/agents_roundtrips_test.go` — note their kind lists
  (`agents_test.go:46,60,75,268,306,344`, `agents_roundtrips_test.go:12,29`)
  pin `claude, opencode, agy, codex` but not `muse`, so no existing test pins
  the new sentences. That is a follow-up, not this round.
- Sync/index step: `scripts/agents-shipped.sh` (`--write` then `--check`);
  index file `internal/harness/agents/shipped.sha256`; enforcing test
  `scripts/agents-shipped_test.sh`.

## Steps

1. Edit `internal/harness/agents/plan-executor.muse.md`: add the muse
   session-polling sentence to the GATE COMMANDS RUN IN THE FOREGROUND
   section, the commit-before-report rule, and the test-update rule.
   Deliverable: the three rules present in the file's existing voice, no
   other section touched. Done when: a read of lines 96-121 shows all three
   and `git diff --stat` names only the three `.muse.md` files plus (after
   step 4) `shipped.sha256`.
2. Edit `internal/harness/agents/librarian.muse.md`: add the muse
   session-polling sentence to the gate sentence (line 35) and the
   commit-before-report rule to THE ROUND. Deliverable: both rules present,
   nothing else touched. Done when: read-back of lines 29-51 shows both.
3. Edit `internal/harness/agents/designer.muse.md`: same two rules in the
   same two places (line 53, THE ROUND line 65). Deliverable and done
   criterion: as step 2.
4. Run `sh scripts/agents-shipped.sh --write`, then
   `sh scripts/agents-shipped.sh --check`. Deliverable: index updated.
   Done when: `--check` exits 0 and `git status` shows `shipped.sha256`
   modified alongside exactly the three edited `.muse.md` files.
5. Verify exclusivity and voice: grep the new muse-specific wording (e.g.
   `session_id`, `background_running`) across `internal/harness/agents/` and
   confirm it appears only in the three edited muse files. Deliverable:
   no leakage into other kinds. Done when: the grep lists only those three
   files.
6. Run the gate: `make check-affected`. Deliverable: green gate. Done when:
   it exits 0 (expect `check-static` incl. `check-scripts` to run and Go
   tests to report "no Go package affected; tests skipped", since only
   `.md` files and the sha index changed).

## Deleted behaviour

None. This round adds role text only; no existing sentence is removed or
narrowed:

1. No agent sentence is deleted.
2. No test, gate, or install behaviour changes.
3. No non-muse variant is touched.

## Report must include

- Per-file status: which of the three `.muse.md` files changed and the rule
  each gained, with line numbers.
- The seed-vs-code correction above restated briefly (repo files are the
  hand-maintained source; `--write` updates the hash index, not copies).
- `git status` / `git diff --stat` showing exactly the three `.muse.md`
  files plus `shipped.sha256`, and the `make check-affected` result verbatim.
- Named follow-ups deliberately left out: (a) commit-before-report rule for
  non-muse variants (missing everywhere); (b) extending the
  `agents_test.go` / `agents_roundtrips_test.go` kind lists to include
  `muse` so the new sentences are pinned like the other four kinds;
  (c) the launch-side question from #1358 (whether `muse exec`
  `--enable-shell-tool` or another flag disables the managed shell's
  auto-backgrounding) — Go behaviour, not this round. A local `muse exec
  --help` (Muse Code 1.4.11) shows `--enable-shell-tool` ("legacy shell
  tool instead of the managed platform shell") as the only candidate,
  unverified.
- The closing `relevo` block, filled in honestly.
