# A5 R5a: reading a reader round's artifacts, and the size cap

Spec:
- `docs/specs/2026-09-24-cockpit-design.md` D5, §3.4 (the size cap), §7 ("Artifacts over
  the cap: NEEDS YOU; nothing is dropped") and §4's round-detail row.

This branch has R1-R4: reader rounds run end to end, and their files live in
`NNN-<actor>/`, with `summary.md` as the final message. Those files seal into
`round_file` under the name `NNN-<actor>/<rel>`.

**This round:**
- the helper every surface uses to list and read a round's artifacts;
- `relevo show` support for them;
- the cap.

The cockpit tab is R5b, and uses this round's helper.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. The helper (internal/relevo, new file `artifacts.go`)

```
type ArtifactFile struct {
    Rel   string    // path inside NNN-<actor>/, slash-separated, e.g. "summary.md", "site/index.html"
    Size  int64
    MTime time.Time
}
// RoundArtifacts lists round's artifact files for binding name, live (on disk) or
// sealed (round_file), sorted with summary.md first, then by Rel.
func RoundArtifacts(rt Runtime, name string, round int, actor string) ([]ArtifactFile, error)
// ReadArtifact returns one artifact's bytes; rel must be a listed Rel (no "..").
func ReadArtifact(rt Runtime, name string, round int, actor, rel string) ([]byte, error)
```

These are interface contracts, not an implementation.
- Build them on `store.RoundFiles`, `ReadFile` and `StatFile` with the nested names from
  R1. A past (archived) binding reads its sealed rows the way `show`'s archived path does
  for flat names; follow it.
- The actor for a binding's round is `bindingRole(b)`. The helper takes it explicitly so
  that an archived record works too.

## 2. `relevo show` (internal/relevo/show.go, cmd/relevo/show.go)

- New sections:
  - `ShowSummary` (`--summary`): the round's `summary.md`;
  - `ShowArtifacts` (`--artifacts`): the file list, one line per file,
    `<size>  <written HH:MM>  <rel>`, with sizes human-readable (`1.2k`, `11.3k`, `2.1M`),
    the same widths as the approved board (FILE, SIZE, WRITTEN);
  - `--artifact <rel>`: one file's bytes, raw, to stdout.
- The `--json` ShowResult carries `Artifacts []ArtifactFile` for `--artifacts`. Tag the
  JSON fields `rel`, `size` and `mtime`, snake_case.
- `ValidShowSection` gains the two sections. Add the usage lines and flag help.
- On a **writer** binding, `--summary` and `--artifacts` answer Missing (not an error)
  when the round has no artifact dir.
- `--report` on a **reader** binding's round shows the summary: the report path is the
  summary path, so this should already hold. Verify it with a test and do not duplicate.

## 3. The cap (policy)

- Add `policy.artifact_max_mb` (int, default **25**; `0` means the default, and it must
  not be negative). Follow the existing policy fields exactly:
  - `internal/policy/policy.go` (the field and an accessor `ArtifactMaxBytes()`) and
    `validate.go`;
  - `internal/relevo/configpolicy.go` (Settings, SettingPaths), so `:settings` lists it
    in the group that fits. Choose it by reading the groups, and say which;
  - the settings form in `internal/ui`, if the list is data-driven there, plus the
    settings goldens that move. List them.
- **At a reader round's close**, after summary.md is written: if the artifact dir's
  total size is over the cap,
  - the round closes as usual, and the report is queued as usual;
  - the binding is marked NEEDS YOU with the reason "artifacts over the cap: <size> >
    <cap> MB; raise policy.artifact_max_mb to seal them". Use the same mechanism other
    NEEDS YOU reasons use;
  - **nothing is deleted or truncated.**
- **Sealing:** `store.Sealable` (or the daemon's `sealRounds`) must **not** seal a round
  whose artifact dir is over the cap, so the files stay on disk. Once the cap is raised
  above the size, the next tick seals it.
  - `Sealable` is in `store` and cannot see policy, so pass the cap in, or check it in
    `sealRounds` (internal/relevo/daemon.go:~415) before calling SealRound. Choose the
    smaller change and say which.
- Writers are unaffected: the cap is about artifact directories only.

## 4. Tests

1. `TestRoundArtifactsLiveAndSealed`: a reader round with `summary.md` and
   `site/index.html` is listed with summary first, both while on disk and after
   `SealRound`, and `ReadArtifact` returns the bytes both ways. `ReadArtifact` with `..`
   is refused.
2. `TestShowSummaryAndArtifacts`: the `--summary`, `--artifacts` and `--artifact <rel>`
   outputs, plus the JSON. A writer round answers Missing.
3. `TestArtifactsOverTheCapHoldTheSeal`: a cap of 1 MB and a 2 MB artifact:
   - the binding is NEEDS YOU with the reason;
   - the daemon tick does not seal, and the files are still on disk;
   - after raising the cap, a tick seals them.
4. `TestArtifactMaxMBValidates`: negative is refused, and 0 falls back to the default.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) The seal ignores the cap: test 3 fails.
  - (b) `RoundArtifacts` skips sealed rows: test 1 fails.
  - (c) `ReadArtifact` accepts `..`: test 1 fails.
- CLI tests in cmd/relevo only parse and print from a seeded store. They never spawn a
  harness or reach the network (CLAUDE.md).

## 5. Working efficiently

- Batch-read these:
  - internal/relevo/{show,daemon,reconcile,configpolicy}.go;
  - cmd/relevo/show.go;
  - internal/policy/{policy,validate}.go;
  - internal/store/{seal,paths}.go;
  - the settings form in internal/ui (grep `max_switches`);
  - the tests next to each.
- Focused loop: `go build ./... && go test -count=1 ./internal/relevo/ -run 'Artifact|Show|Cap|Seal|Policy|Setting' && go test -count=1 ./internal/policy/ ./internal/store/ ./internal/ui/ && go test -count=1 ./cmd/relevo/ -run 'Show|Contract|Setting'`
- Before committing, run `grep -n '§\|#[0-9]' <every file you touched>`: it must print
  nothing in comments. The comment script can pass here without checking anything.
- Full, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check && make e2e`

## 6. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-r5a-show-and-cap.md`. Commit as
**one new commit**: `feat(a5): show a round's summary and artifacts; the artifact size cap`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- the settings group chosen, and the goldens that moved;
- where the cap check lives;
- the mutations;
- `make check` and `make e2e`'s last lines;
- anything that did not match.
