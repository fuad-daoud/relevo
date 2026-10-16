# Owner amendments (apply before the plan below; they override it)

- Base is now origin/main at d340f867, which already changed `internal/mastermind/guide.md` and the MCP instructions golden. Re-read the lines the plan cites before editing; regenerate the golden with `go test ./internal/mcp -run Contract -update`.
- **Skip step 7 entirely and make no change under `internal/ui/`.** The TUI is being retired; the stats ticket row keeps today's clipping. Drop the `view_stats.go` row from the readers table, the two `internal/ui` tests, and the step-7 mutation from step 10. Everything else stands.
- The plan's stated decision stands: an http(s) URL with `/issues/<non-number>` is stored whole as an opaque ref.

---

# `--ticket` accepts non-GitHub references (issue keys, GitLab subgroups, opaque URLs)

Target doc for the final save: `docs/plans/2026-10-15-ticket-refs.md` (same PR as the code, per CLAUDE.md "Specs and plans").

## Verified against the code before planning

- TRUE — the parser is `internal/store/types.go:114-195`: `ParseTicket` → `splitTicket` → `ownerRepoFromURL`; `ValidTicket` (`types.go:87-112`) then re-checks the stored form. Accepted today: `N`, `#N`, `owner/repo#N`, and a `.../issues/N` URL (with or without a scheme).
- TRUE — a GitLab issue URL is mis-stored exactly as the seed says. `splitTicket` splits on the last `/issues/` and hands `https://gitlab.com/grp/sub/proj/-` to `ownerRepoFromURL`, whose last-two-segments rule yields `proj/-`; `-` and `proj` both pass `validOwnerRepoName`, so the stored form is `proj/-#5`.
- TRUE — `git.OwnerRepo` (`internal/git/repo.go:143-167`) takes the last two path segments, and `internal/git/repo_test.go:275` pins that ("extra path segments take the last two", `https://gitlab.com/group/sub/repo` → `sub/repo`). Its only production callers are `internal/relevo/bind.go:211` (the `--ticket` hint) and `cmd/relevo/db_rename_repo.go:95` (the rename's ticket prefix).
- TRUE — tickets are metadata only. No API client, no fetch, no validation on the way in or out: `internal/ingest/ingest_tx.go:157` copies the string, `internal/remote/proto.go:122-126` carries it to a server that stores it unvalidated, `internal/db/read.go:195,264` read it straight into `RoundRow.Ticket`/`BindingRow.Ticket`. Every reader is listed under "Readers of the stored ticket" below.
- TRUE — the 128-byte cap and the two-segment rule live only in `ValidTicket`; `ParseTicket` is its only caller (`cmd/relevo/bind.go:169`, `cmd/relevo/chain.go:256`, `internal/relevo/bind.go:213`, `internal/relevo/add.go:153`), so both signatures can stay as they are.
- TRUE — the guide text is a shipped contract: `internal/mastermind/guide.md:10-11` names the ticket forms and `internal/mcp/testdata/contract/instructions.golden:79-80` holds a byte-for-byte copy, regenerated with `go test ./internal/mcp -run Contract -update`.
- TRUE — `internal/db/migrations/012_binding_ticket.sql` only says `ticket TEXT`; no schema change is needed and the migration file is left alone.
- NOT TRUE OF THE SEED — nothing in the seed says what a URL that *looks* like an issue link but does not parse as one becomes. Two decisions below state the reading this plan takes; both are consequences the owner can reverse in one place (`splitTicket`'s branch order).

## Behaviour and cases

Inputs are tried in this order. `T` is the raw value trimmed; the `ownerRepo` hint is `git.OwnerRepo(origin)` and is applied only to the number forms.

1. **Issue URL** (with or without a scheme, today's shape kept): if the last `/issues/` is followed by a valid issue number (1..9 digits, no leading zero) and the path before it — minus one trailing `-` segment (GitLab's `/-/issues/N`) — is a valid project path of 2..N segments, store `projectPath#N`.
   - `https://github.com/o/r/issues/607` → `o/r#607` (unchanged); `.../607/` → same.
   - `https://gitlab.com/grp/sub/proj/-/issues/5` → `grp/sub/proj#5` (today: `proj/-#5`).
   - `https://gitlab.example.com:8443/a/b/c/d/-/issues/7` → `a/b/c/d#7` (any subgroup depth).
   - A schemeless `owner/repo/issues/5` keeps working as today; `owner/repo/issues/abc` is still refused (no scheme, so it is not a URL).
2. **Opaque URL**: any other input starting `http://` or `https://` is stored whole, trimmed, no hint. `https://linear.app/team/issue/ENG-123` → itself; `https://example.com/a#5` → itself (not `a#5` — the URL branch precedes the hash branch); `https://example.com/x/y?z=1` → itself.
   - **Decision (stated, reversible):** a URL carrying `/issues/<non-number>` (or an unparseable project path) is "any other http(s) URL" and is stored whole. That turns today's refusal of `https://github.com/o/r/issues/abc` into an accepted opaque ref. The alternative — refuse it — is one extra condition in the issue-URL branch; the plan takes the literal reading of the seed's own sentence.
   - Only `http`/`https` are opaque; `ftp://host/a/b` is refused.
3. **Issue key**: `^[A-Z][A-Z0-9]+-[0-9]+$`, stored verbatim, the hint never applied (`--ticket ENG-42` in a GitHub repo stores `ENG-42`, not `o/r#ENG-42`). `ABC-123`, `ENG-42`, `PROJ1-7` are keys; `abc-123`, `A-1`, `1AB-2`, `ABC-` are not.
4. **Number forms** (unchanged except for the path width): `N` and `#N` take the hint; `path#N` typed in wins over the hint; `path` is 2..N segments, each 1..100 chars of `[A-Za-z0-9._-]`. `r#607` is still refused (a one-segment path names no owner).
5. Anything else is refused with the `ErrInvalidTicket` class, one line, exit 2, before any runtime, state, worktree or network work (unchanged shape; the CLI tests stay pre-runtime, which is what CLAUDE.md's `cmd/relevo` rule requires).

Stored forms and their caps: `#N` / `path#N` ≤ 256 bytes (a GitLab full path can reach 255); issue key ≤ 64; opaque URL ≤ 512. The single 128-byte cap goes.

Display and filters:

- A ticket key is shown **verbatim** and never shortened: two projects can share a leaf (`grp1/x/proj#5` vs `grp2/y/proj#5`), and the row is what a filter query is built from.
- The tail of a ticket key is the identifying part (`#N`, `ENG-42`), so a stats repos-tab ticket row is fitted **keeping its tail** (`stats.FitKey(..., true)`); repos, features and the history key column keep the existing head clip.
- `relevo history --ticket` and `-q ticket:...` match the **stored** form: a bare `N`/`#N` matches any stored ticket ending in `#N`, anything else matches exactly. No parsing, no hint, no change to `ticketMatch`. A stored opaque URL is filterable by typing that URL (exactly).

## Readers of the stored ticket (every one, with its verdict)

| Reader | Verdict |
| --- | --- |
| `internal/db/read.go:129-153` `ticketMatch`, `bareTicketNumber` | No code change. `N`/`#N` → `LIKE '%#' || ?` (digits only, so no wildcard); every new form → exact equality. The `%`/`_` "matches nothing" pins still hold because those values never reach the LIKE branch. |
| `internal/db/read.go:195,264` `ptrIfValid` | No change: a pass-through, it validates the column is non-NULL, not the string's shape. |
| `internal/db/repo_rename.go:185-231` `ticketPrefixes`, `planTickets`, `planRecords` | No code change: it rewrites rows whose ticket starts with `oldOwnerRepo#`, built from `git.OwnerRepo` (`cmd/relevo/db_rename_repo.go:95`), so a subgroup rename now rewrites `grp/sub/old#5` correctly. An issue key and an opaque URL carry no such prefix and are left alone. |
| `internal/stats/groups.go:280-290` `ticketKey` | No change: groups by the stored string verbatim. |
| `internal/histq/group.go:155-160` (`--by ticket`) | No change. `histq.Query.String()`/`splitToken` (`histq.go:210-225,385-395`) split a token at the first `:`, so a quoted URL value round-trips. |
| `internal/ui/view_stats.go:1678-1685` `shortTicket` | No change: verbatim, `(none)` → `(no ticket)`. |
| `internal/ui/view_stats.go:2783-2803` `statsRepoRow` | One change: `clipLeft` true for `kindTicket` at line 2795. |
| `internal/ui/view_stats.go:666-678` `enterRepoChild`, `2845-2868` `statsGroupDetail` | No change: the query term is built quoted (`ticket:"<key>"`), which parses back to the exact key; the detail line is `fit`-ted. |
| `internal/ui/dash/render.go:618-640,665-697` `groupKeyLabel`, `groupLine` | No change: verbatim, clipped to the key column by `clip`. |
| `internal/ui/golden_test.go:725-768` | No change: the fixture's ticket is `#665`, far shorter than the column, so no golden moves. |
| `internal/ingest/ingest_tx.go:157`, `internal/remote/proto.go:120-127` | No change: an opaque string in and out; an older server stores it unvalidated. |
| `internal/store/binding.go:379-381`, `internal/relevo/bind.go:162-166`, `internal/relevo/add.go:79-84`, `internal/relevo/history.go:120-122`, `internal/db/read.go:129-133`, `internal/mastermind/guide.md:10-11`, `README.md:277-279,1090-1096,1154-1155`, `cmd/relevo/bind.go:138`, `cmd/relevo/chain.go:77`, `cmd/relevo/history.go:84` | Doc/comment updates only. |

## Seams: files, types, functions, line ranges

- `internal/store/types.go` — `ErrInvalidTicket` doc (:56-60), `ValidTicket` (:87-112), `ParseTicket` (:114-137), `splitTicket` (:139-171), `errTicketForm` (:173), `ownerRepoFromURL` (:175-195, becomes `projectPathFromURL`), `validOwnerRepo` (:197-205, becomes `validProjectPath`, 2..N segments), `validOwnerRepoName` (:207-224, reused per segment), `validIssueNumber` (:226-237, reused), plus a new `validIssueKey`. No signature change to `ValidTicket`/`ParseTicket`, so `cmd/relevo/bind.go:169`, `cmd/relevo/chain.go:256`, `internal/relevo/bind.go:205-214`, `internal/relevo/add.go:152-158` compile untouched. Every function stays ≤70 lines and the file ends well under 600.
- `internal/git/repo.go:143-167` — `OwnerRepo` returns the whole path after the host (every segment, `/`-joined, each non-empty, at least two) instead of the last two. Host sniffing is deliberately not used: `github.com`/`bitbucket.org` paths are exactly two segments, so their results are unchanged, and a self-hosted GitLab subgroup works the same way.
- Tests to extend: `internal/store/types_test.go:246-334` (`TestValidTicket`, `TestParseTicket`), `internal/store/invalid_class_test.go:29-32,81-88` (`ErrInvalidTicket` class — keeps passing, its schemeless `owner/repo/issues/abc` case is now refused by a different branch), `internal/git/repo_test.go:265-291`, `internal/db/read_test.go:20-35` (the filter table), `internal/db/repo_rename_test.go:128-190`, `internal/relevo/bind_test.go:2062-2205`, `internal/ui/view_stats_test.go:2891-2918,3085-3137`, `cmd/relevo/main_test.go:1210-1224`.
- Goldens: `internal/mcp/testdata/contract/instructions.golden:79-80` only (from the guide edit). `cmd/relevo/testdata/contract/help-json.golden` carries flag *names*, not descriptions, so the usage strings can change without it.
- Style gates that shape the diff: `.golangci.yml` has no exclusion for `internal/store`, `internal/git`, `internal/db`, so `funlen` 70 / `gocognit` 30 apply there; `scripts/check-comments.sh` allow-lists `internal/relevo/bind.go`, `internal/ui/view_stats.go`, `cmd/relevo/bind.go` but **not** `internal/store/*`, `internal/git/*`, `internal/db/*` — no new comment may cite `#NNN` or `§`, and no new allow-list entry. Test names say what they pin, with no issue numbers.

## Ordered steps (one line each: deliverable + how to know it worked)

1. Write the new tables in `internal/store/types_test.go:246-334` first — `TestValidTicket` extended (`grp/sub/proj#607` and `ABC-123` and an opaque URL valid; `r#607`, a 257-byte path and a 513-byte URL refused), plus `TestParseTicketGitLabSubgroupURL`, `TestParseTicketIssueKey`, `TestParseTicketOpaqueURL`, `TestParseTicketKeepsGitHubForms`, `TestParseTicketRefusesTheRemainingGarbage` — done when all of them fail on today's `types.go` and the red output is kept for the report.
2. Rewrite the ticket block in `internal/store/types.go:56-237` to the behaviour above (issue-URL branch → opaque-URL branch → issue-key branch → number branches; per-form caps; `projectPathFromURL`/`validProjectPath`/`validIssueKey`; new `errText` strings that still start `ticket:`) — done when `go test ./internal/store/` is green; mutation to run: delete the trailing `-` segment strip and `TestParseTicketGitLabSubgroupURL` must fail with `proj/-#5`.
3. `internal/git/repo.go:143-167` whole-path `OwnerRepo`, and `internal/git/repo_test.go:275` re-pointed to `group/sub/repo` plus a self-hosted-subgroup case — done when `go test ./internal/git/` is green; mutation to run: restore the last-two-segments rule and `TestOwnerRepo` must fail.
4. Pin the hint path in `internal/relevo/bind_test.go:2062-2205` with `TestBindStoresAGitLabSubgroupTicket` (origin `git@gitlab.com:grp/sub/proj.git`, `--ticket 5` → `grp/sub/proj#5`) and `TestBindStoresAnIssueKeyVerbatim` (GitHub origin, `--ticket ENG-42` → `ENG-42`), and update the comments at `internal/relevo/bind.go:162-166` and `internal/relevo/add.go:79-84` — done when `go test ./internal/relevo/ -run 'TestBindStores'` is green; mutation to run: apply the hint to a key and `TestBindStoresAnIssueKeyVerbatim` must fail.
5. Add the filter cases to `internal/db/read_test.go:20-35` (`ENG-42` exact, `grp/sub/proj#5` exact, an opaque URL exact, `#5` still matching every `#N`-ended ticket, `ABC-1` not matching `ABC-12`) and refresh the comment at `internal/db/read.go:129-133` — done when `go test ./internal/db/ -run 'TestFilter'` is green; mutation to run: make the exact branch a `LIKE` and the key case must match more rows than it should.
6. Add `TestRenameRepoRewritesSubgroupTickets` (binding and chains: `grp/sub/old#5` → `grp/sub/new#5`) and `TestRenameRepoLeavesKeysAndURLs` to `internal/db/repo_rename_test.go:128-190` — done when `go test ./internal/db/ -run 'TestRenameRepo'` is green with no production change in `repo_rename.go`; if the subgroup case needs code, the only allowed change is in `ticketPrefixes` (`repo_rename.go:185-189`), and the report says so.
7. Keep a ticket row's tail in `internal/ui/view_stats.go:2795` (pass `clipLeft` true for `kindTicket`), and add `TestStatsRepoTicketRowKeepsTheIssueNumberVisible` (a repos-tab row whose key is a ~60-char URL in a narrow column shows `…` plus the URL's tail, inside the name column) and a `TestStatsReposChildEnter` case whose ticket key is an opaque URL (the opened `:rounds` query carries the exact key) — done when `go test ./internal/ui/` is green with the goldens unmoved; mutation to run: set `clipLeft` false and the tail assertion must fail.
8. Update the docs and their comments: `cmd/relevo/bind.go:138`, `cmd/relevo/chain.go:77`, `cmd/relevo/history.go:84` (usage strings naming `N`, `#N`, `owner/repo#N`, an issue key like `ABC-123`, an issue URL; the history one saying a bare `N`/`#N` matches any stored ticket ending in `#N` and anything else matches exactly), `internal/store/binding.go:379-381`, `internal/relevo/history.go:120-122`, `README.md:277-279` (bind), `1090-1096` ("What a binding records"), `1154-1155` (the history filter block), `internal/mastermind/guide.md:7-12`, then regenerate the shipped copy with `go test ./internal/mcp -run Contract -update` — done when `go test ./internal/mcp/ ./internal/mastermind/` is green and `git diff --stat internal/mcp/testdata` shows only the guide's ticket sentence.
9. Iterate with `make check-quick PKGS='./internal/store/... ./internal/git/... ./internal/db/... ./internal/relevo/... ./internal/ui/... ./internal/mcp/... ./internal/mastermind/... ./cmd/relevo/...'`, fixing every reported error before the next run — done when the focused command is green.
10. Run the named mutations one at a time (steps 2, 3, 4, 5, 7), reverting each, and record red-with / green-without for each — done when every named test failed with its mutation and passed without it.
11. Save this plan verbatim to `docs/plans/2026-10-15-ticket-refs.md` — done when the file is in `git diff --stat`.
12. Run the full gate once: `make check` (gofmt, vet, lint, comments, filesize, tidy, shell checks, then `go test -race -count=1 -cover ./...`) — done when it is green; no package moved, so no coverage-baseline regeneration is expected (`RELEVO_REQUIRE_COVERAGE=1` optional locally; never lower a baseline).
13. Commit everything on the branch as new commits (never amend or rebase a commit already on a remote binding's branch) — done when `git status --short` is empty and the log shows only new commits.

## What is deleted (closed list)

1. The exactly-two-segment repository rule: `validOwnerRepo` (`internal/store/types.go:197-205`) and `ownerRepoFromURL`'s last-two-segments extraction (`types.go:175-195`), replaced by `validProjectPath` (2..N segments) and `projectPathFromURL` (which also drops GitLab's trailing `-`).
2. `git.OwnerRepo`'s "last two path segments" rule (`internal/git/repo.go:158-166`) and the test case that pins it (`internal/git/repo_test.go:275`).
3. `ValidTicket`'s single 128-byte cap (`internal/store/types.go:94`) — replaced by the per-form caps (256 / 64 / 512).
4. The two old error texts `"ticket: #N or owner/repo#N"` (`types.go:92`) and `"ticket: a number, #N, owner/repo#N, or a .../issues/N URL"` (`types.go:120`), replaced by wording that names the key and URL forms (both keep the `ticket:` prefix the CLI refusal test matches).
5. The `TestValidTicket` "over 128 bytes" case (`internal/store/types_test.go:265`), replaced by the per-form cap cases.
6. Nothing else: no exported name or signature, no CLI flag or verb, no `store.Binding` field, no DB column and no migration edit (012 stays as applied history), no package move, no coverage-baseline change, no golden other than the guide's ticket sentence, and no reader's behaviour changes beyond the two stated above (the URL-with-a-non-number issue path is stored whole, and the repos-tab ticket row keeps its tail).

## What the report must include

- Every changed path and `git diff --stat` against this plan's declared scope; the focused `make check-quick PKGS=...` run and the full `make check` run, with results.
- The forms table as implemented with the parse order, and confirmation of the two stated consequences (a URL with `/issues/<non-number>` is now stored whole instead of refused; a one-segment `path#N` is still refused).
- Each named test with the exact mutation command, and its red-with / green-without result; the red output of step 1's tests against the pre-change parser.
- The seed-vs-code findings: the pinned `OwnerRepo` last-two test, the `proj/-#5` mis-store, the `internal/mcp` instructions golden tied to `guide.md`, the allow-listed comment files (so no new `#NNN` citation was added and no allow-list entry changed), migration 012 left alone, and that no test touches the network or a harness.
- The plan-doc save path, the new commit hashes, a clean `git status`, whether `sh scripts/check-coverage.sh --write` was needed (expected no, and why), and anything deliberately left not done.
