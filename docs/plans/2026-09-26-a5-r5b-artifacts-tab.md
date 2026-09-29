# A5 R5b: the cockpit's artifacts tab for reader rounds

Spec: `docs/specs/2026-09-24-cockpit-design.md`, §4's round-detail row: "tabs: plan ·
artifacts · diff (writers) · log · transcript … open a file: `.md` in the pager, `.html`
in the browser, anything else in `$EDITOR`".

The **approved design** is the canvas board "round detail D2 · a reader round's
artifacts (A5, illustration)", reproduced in §4.

This branch has:
- R1-R4: reader rounds end to end;
- R5a: `relevo.RoundArtifacts`, `ReadArtifact`, `ArtifactFile{Rel, Size, MTime}`, and
  `relevo show --summary/--artifacts/--artifact`.

**This round:** the cockpit shows them. Writer rounds must look **exactly** as today.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. Tabs by shape (internal/ui)

- **Writer binding:** the tabs are unchanged (`plan, report, transcript, diff, log`,
  fetch.go:~18-30). Every existing round golden must pass untouched. If one moves, stop
  and report.
- **Reader binding** (`Binding.Shape == "reader"`; the round view has the binding or its
  status row, so find where), the tabs are `plan · artifacts N · log · transcript`:
  - N is the file count;
  - there are no report and no diff tabs;
  - add a `tabArtifacts` and make the tab list shape-dependent in the three places that
    enumerate tabs (`tabTitles`, round_pane.go:~202 and ~376 `tabsRow`, and
    `sectionForTab`/`fetchFor` in fetch.go);
  - the `[` `]` round stepper and every other key keep working.

## 2. The artifacts tab body

- A table, `FILE`, `SIZE`, `WRITTEN` and `OPENS IN`, in the cockpit's faint bold header
  style, one row per `ArtifactFile`:
  - summary.md first, as RoundArtifacts orders them;
  - SIZE is human-readable (`1.1k`, `11.3k`), using R5a's formatter (don't write a
    second one);
  - WRITTEN is `HH:MM` local;
  - OPENS IN is `pager` for `.md`, `browser` for `.html`/`.htm`, and `$EDITOR` for
    anything else.
- A cursor row, with the same selection band as other tables.
- Below the table, a blank line, then one faint line
  `<rel> · the <actor>'s final message` for summary.md (for other files, `<rel>`), then
  the selected file:
  - `.md` rendered with the cockpit's existing markdown renderer (the one the plan and
    report tabs use; find it in detail.go or markdown.go);
  - any other file: its first lines as plain text, or `binary file, <size>` when it is
    not valid UTF-8.
- Data is fetched off the update loop, like the other tabs' fetchers, through
  `RoundArtifacts` and `ReadArtifact`.

## 3. Opening a file

- `enter` on an artifacts row opens the file:
  - `.md` goes to the pager (`$PAGER`, else `less -R`);
  - `.html`/`.htm` goes to the browser (`xdg-open` on Linux, `open` on macOS);
  - anything else goes to `$EDITOR`, reusing the existing editor helper
    (`plannerActions.AgentEditor`, internal/ui/actions.go:~543, or confirm.go's
    `tea.ExecProcess` pattern).
- A sealed file (no longer on disk) is written to a temp file under `os.TempDir()` with
  its own name first. Say how in the report.
- The footer shows `enter open file` only on the artifacts tab.
- The pager and editor run with the terminal released, via `tea.ExecProcess` like the
  existing `$EDITOR` path. The browser is started detached, without waiting.
- Opening is behind the `Actions` seam, so tests use the fake Actions and never run a
  real pager, browser or editor. Add `Actions.OpenArtifact(ctx, path, kind)` or the
  closest fit with today's seam, and say which.

## 4. The header and card for a reader round

Per the board:
- **Row 2:**
  `<actor> on <candidate> · reader · planner <p> · scratch worktree @ <short head> + dirty diff`,
  where the short head is the round's `RoundBaselineHead`. Writers keep today's row 2.
- **Card:** the state chip and time as today, then `· N artifacts · <total size>` and,
  once closed, `· repository unchanged`. Writers keep today's card.
- **Keys on a reader card:** no `g gate`; `s send next` and `r retry on…` as for writers.

The board, as text (132 columns, a closed reader round with the artifacts tab open):

```
◆ relevo    fleet › review-568 › r1                                        v…     16:40
   reviewer on gpt-5.6-terra  ·  reader  ·  planner architect-5  ·  scratch worktree @ 043cf35 + dirty diff

 ╭─ review-568  round 1 ───────────────────────────────────────────────────── sealed ─╮
 │  reported   9m  ·  2 artifacts  ·  12.4k  ·  repository unchanged                   │
 │  tokens  in 212k  ·  cache 1.9M (90%)  ·  out 18k                                  │
 │                                                                                    │
 │   s  send next      r  retry on…                                                   │
 ╰────────────────────────────────────────────────────────────────────────────────────╯

   plan    [artifacts 2]    log    transcript                        round  [   r1 of 1   ]

     FILE                          SIZE    WRITTEN   OPENS IN
     summary.md                    1.1k    16:38     pager        <- cursor band
     findings.md                   11.3k   16:37     pager

     summary.md · the reviewer's final message

     <summary.md rendered>

 enter open file   tab next tab   [ ] round   ↑↓ move   s send next   esc back   ? all keys
```

## 5. Tests and goldens

- **New goldens**, generated with `-update`; read each one and check it against §4:
  - `round-reader-artifacts-132`: a closed reader round, artifacts tab, cursor on
    summary.md;
  - `round-reader-artifacts-100`.
  - The fixture is a seeded reader binding plus files through the store; follow how
    the existing `round-*.golden` fixtures seed a round.
- **Unit tests:**
  1. `TestReaderRoundTabs`: a reader round shows `plan, artifacts, log, transcript`, and
     a writer round shows today's tabs.
  2. `TestArtifactsEnterOpensByKind`: `enter` on `.md`, `.html` and `.txt` rows calls
     the fake open action with pager, browser and editor.
  3. `TestArtifactsTabReadsSealedFiles`: after `SealRound`, the tab still lists and
     renders them.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) A reader round shows the writer tabs: test 1 fails.
  - (b) `.html` opens in the pager: test 2 fails.
  - (c) The tab reads only disk files: test 3 fails.

## 6. Working efficiently

- Batch-read these:
  - internal/ui/{fetch,round_pane,view_round,detail,card,markdown,actions,confirm}.go;
  - the round golden tests in golden_test.go;
  - internal/relevo/artifacts.go (from R5a).
- Focused loop: `go build ./... && go test -count=1 ./internal/ui/ -run 'Round|Artifact|Golden|Tab'`
- Before committing, run `grep -n '§\|#[0-9]' <every file you touched>`: it must print
  nothing in comments. The comment script can pass here without checking anything.
- Full, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check && make e2e`

## 7. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-r5b-artifacts-tab.md`. Commit as
**one new commit**: `feat(a5): the cockpit's artifacts tab for reader rounds`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- both goldens' full text;
- the open-action seam you chose;
- the mutations;
- `make check` and `make e2e`'s last lines;
- anything that did not match.
