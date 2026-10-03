# Plan: `make check`'s gofmt step never sees untracked files (builder round, base `origin/main`)

## 0. Seed-vs-code contradictions (do not guess around these)

- `scripts/check-filesize.sh:39-40` already lists untracked-not-ignored files (two-line form fixed by an earlier round; see `docs/plans/2026-09-30-tag-batch-a.md:12-13`). The seed's "comment/size scripts?" implies size may still be broken — it is not. No change to that file; it is the reference implementation.
- The Observed example `internal/relevo/chains_doc.go` is not present in this tree; treat it as illustrative, not as a file to touch.
- The Decision's shorthand (`git ls-files --cached --others --exclude-standard`) omits the pathspec separator and pattern; every edited listing must keep the existing `'*.go'` (and `:!` exclusions where present) behind `--`.

## 1. Behaviour and cases

- Every gate that lists Go files from the index must also list untracked-not-ignored files, keeping its current exclusions: `Makefile:35` gofmt step and `scripts/check-comments.sh:37` change; `scripts/check-filesize.sh:39-40` already does and stays untouched.
- Cases that must hold after the round: tracked clean passes; tracked dirty fails (existing behaviour); untracked gofmt-dirty `.go` fails the gate; untracked ignored `.go` (via `--exclude-standard`) passes unlisted; untracked allow-listed `.go` passes for the comment guard; an empty file list keeps the gate green.
- Explicitly out of scope (name in report, do not touch): `scripts/check-name.sh:31` (`git grep` over tracked files) and `:46` (`git ls-files -- '*.md'`) are not Go-file listings; `go vet ./...`, `golangci-lint run ./...`, `go test ./...`, `scripts/test-shard.sh` (`go list ./...`) and `scripts/check-coverage.sh` already read the filesystem, not the index; `scripts/rename-relevo.sh:34,38` and `scripts/rename-mastermind.sh` are one-shot migrations, not gates; `.github/workflows/ci.yml:70` is a comment about cache placement.

## 2. Seams (file, function/region, lines)

- `Makefile:30-35` (`check-static` gofmt line; the inner `git ls-files '*.go'` appears twice and both change to `git ls-files --cached --others --exclude-standard -- '*.go'`).
- `scripts/check-comments.sh:34-42` (listing comment plus `git ls-files -- '*.go' > "$work/all"` and the allow-list subtraction; behaviour of `:44-63` unchanged).
- `scripts/check-filesize.sh:34-45` (reference: tracked plus `--others --exclude-standard` append; no edit, verify only).
- `scripts/check-name.sh:29-52` (no edit; reason in §1).
- `scripts/check-comments_test.sh:13-51` (`stage`/`put`/`run` helpers; verdict `:115`) — extend with an untracked helper mirroring `scripts/check-filesize_test.sh:45-51`, reusing fixtures under `scripts/testdata/check-comments/` (`clean.go`, `cites-issue.go:4`).
- `scripts/check-filesize_test.sh:125-154` (untracked/ignored/allow-listed cases — the pattern to copy, not to edit).
- `Makefile:51-57` (`check-scripts` loop auto-discovers `scripts/*_test.sh`; any new `*_test.sh` is picked up with no Makefile change).
- `docs/plans/2026-10-02-gate-untracked-files.md` (new; verbatim copy of this plan, last step).

## 3. Ordered steps (one line each: deliverable + done-know)

1. Makefile gofmt listing covers tracked plus untracked-not-ignored with `'*.go'` kept — done when a scratch repo with an untracked gofmt-dirty `.go` fails the exact gate line and an ignored one does not.
2. `check-comments.sh` listing covers tracked plus untracked-not-ignored with allow-list semantics unchanged — done when `sh scripts/check-comments.sh` fails on an untracked `cites-issue.go` copy and `sh scripts/check-comments_test.sh` still passes tracked cases.
3. `check-comments_test.sh` gains untracked cases mirroring `check-filesize_test.sh:125-154` — done when the script prints `check-comments: ok` and reverting step 2's listing to plain `ls-files` fails the new untracked cases only.
4. New scripted pin (new `scripts/*_test.sh` picked up by `check-scripts`, or equivalent the builder owns) asserting the gofmt gate names an untracked badly formatted `.go` — done when it passes fixed and fails with the listing mutated back to plain `git ls-files '*.go'`.
5. Full gate plus plan doc — done when `make check` is green once at the end, `git status` shows only §4 files, and the verbatim plan is saved at `docs/plans/2026-10-02-gate-untracked-files.md`; land as new commits, never amend/rebase a pushed branch.

## 4. What is deleted (closed list)

1. Nothing is deleted: no file, function, test, fixture, or allow-list entry is removed.
2. The only dropped behaviour is invisibility of untracked-not-ignored `.go` files to the gofmt and comment gates; nothing else changes, and no new `check-comments.allow` / `check-filesize.allow` entry is added to get green.

## 5. Working rules for the builder

- Read `Makefile`, `scripts/check-comments.sh`, `scripts/check-filesize.sh` (+ `_test.sh` pair) in one batch; one edit per file.
- POSIX `sh` only in scripts; comments say why, never restate code, no issue numbers or round history (CLAUDE.md); focused loop on `sh scripts/check-comments_test.sh` plus the new pin's script and `make check-scripts`, fixing every error before the next run; `make check` exactly once at the end.
- No `cmd/relevo` test changes are expected; if any are added they must stay pure (no harness, no network, no real config/state per CLAUDE.md).

## 6. Report must include

- Commit hashes/subjects and `git diff --stat` (files limited to §4 plus the plan doc).
- Both listing diffs (or diff plus verified-no-op for filesize), the new/changed test names, both mutation results (revert each listing to plain `ls-files` → which new cases fail), `make check-scripts` and final `make check` outcomes.
- Explicit not-done list: `check-name.sh` untouched and why (§1), `check-filesize.sh` untouched and why (§0), and anything else deliberately left out.
