# Cockpit D2 round detail, round 5: a calm two-line header, and air under the title

Date: 2026-09-25. Worktree: ck-d2-round, which holds rounds 1-4 **uncommitted**. Build on it; do not reset or commit.
Design: canvas board **round header · calm A, two lines** (version 20,
https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA). Line numbers are exact in the worktree now.

**Stop rather than improvise.** If anything differs from what is quoted, halt and report.

## 1. Why

The user found the round view's top cluttered. It stacked the context row and a six-row boxed card (state, tokens,
a blank line, keys) directly under the title. The approved design:
- one blank line under the title on **every** view;
- then two calm lines, with no box;
- the round's action keys live only in the footer.

## 2. Reference (132 cols)

```
  ◆ relevo    fleet › ci-trim ›  r1                                                            v0.13.0-33     10:05

    working    7s · quiet 6s      gemini-3.8-flash-high  ·  architect-4  ·  relevo/ci-trim      round 1 of 1 · live
   tokens in 718k · cache 4.2M (85%) · out 64k · no price                                     pid 3412557 since 10:04

   plan    report    transcript    diff    log                                                  round   [   r1 of 1   ]

     plan r1 · 10:05
     …
```

## 3. Changes

### 3.1 Frame: a blank row under the header (every view)

- shell.go:378: `rows := []string{m.headerView(env), m.contextView(env)}` becomes header, `""` (a blank row fitted to
  width), then context.
- frame.go:120-126 `bodyHeight`: `h := env.Height - 3 - env.ErrRows - 2`, so it accounts for the new row.
- Grep `internal/ui` for any other place that assumes the body starts at row 3 or that the chrome above the body is 2 rows
  (`Height - 2`, `height - 2`, `bodyHeight` re-derivations). Fix each one to use `bodyHeight`, and name them in the report.
- Every golden changes by exactly this one row (and one fewer body row at the bottom). That is expected.

### 3.2 Round view: context row is line one

`roundView.Context` (view_round.go:120). **Left, for a live row:**
- three spaces;
- the **state pill**, with the same style and word table as round 1's card: needs you `chipWarn`, working `chipGreen`, idle, on hold, other and done `kbd`;
- three spaces;
- the **age**:
  - working: `ago(RoundStart, now)` in text bold, then muted ` · quiet <QuietFor>` when QuietFor != "";
  - every other group: `rowNow(b, now)` with its `rN · ` prefix removed, in text;
- six spaces;
- `candidateText(b)` in text;
- faint `  ·  `, then the muted planner name (`plannerCell`), or `client <OwnerLabel>` when OwnerLabel != "";
- faint `  ·  `, then the muted branch (or `repoCell`);
- when Dirty: faint `  ·  `, then red `dirty`.

The right side is unchanged: `round N of M · live|archived <date>`. A hist row or nil row is unchanged: muted `detailHeader()`.

### 3.3 Round view: tokens line is line two (the body's first line)

A new `roundPane.tokensLine(b *relevo.BindingStatus) string`, fitted to width via `spread(left, right, width)`:
- **Left:**
  - three spaces;
  - faint `tokens `;
  - then, from `usage.LiveParts(*b.LiveUsage)` (or `usage.Parts(*b.LastUsage)` when there is no LiveUsage), only the parts that start with
    `in `, `cache ` or `out `; any `write N` part whose N is not `0`; and the **last** part (the cost word). All faint, joined by faint ` · `.
  - With no usage at all: faint `no usage yet`.
  - Then, when `LastClose.Commits > 0`: faint ` · +1 commit` / ` · +N commits`.
  - When `spendCell(b) != ""`: faint ` · spend <spendCell>`.
- **Right:** faint `pid N since HH:MM` when `Headless != nil && PID != 0`, then two spaces.
- **Hist / nil row:** left is three spaces + faint `no live facts for a released binding`; there is no right side.

### 3.4 Body order and heights

`roundPane.view` (round_pane.go:585):
1. `tokensLine`;
2. a blank line;
3. `tabsRow`;
4. a blank line;
5. `"     " + sourceLine()`;
6. a blank line;
7. the viewport and hint (unchanged).

- `headRows()` (line 43) returns **6** unconditionally. The `rows >= 18` card condition goes.
- `viewportHeight()` stays `rows - headRows()`.

### 3.5 Keys

`roundView.Keys()` (line 153): `tab next tab`, `[ ] round`, then with actions `x stop`, `g gate`, `o shell`. The footer
tail adds `: command`, `? all keys` and `esc back` as today. `HelpKeys()` is unchanged.

## 4. Deletions (closed list)

- **C1** `roundPane.cardLines` (round_pane.go:319 to its end) and `const roundCardRows = 6` (line 16).
  `renderCard`/`cardKeysRow` in card.go **stay**: the fleet uses them.
- **C2** The `rows >= 18` branch in `headRows` and in `view`.

Everything else survives: tabs, markdown, stepping, fetch and the race fix, the fleet card.

**Ports** (change assertions; cite C1/C2):
- `TestRoundCardByGroup` becomes `TestRoundContextByGroup`: for a row per group, `roundView.Context` left contains the pill word.
  The action-key labels move to `Keys()`: with actions it has `x stop`, `g gate` and `o shell`; without actions none of them.
- `TestRoundCardHist` becomes `TestRoundTokensLineHist`: `no live facts for a released binding`.
- `pane_test.go` assertions on the card (`+1 commit`, live tokens) move to `tokensLine`. Its `roundCardRows` count assertion is
  replaced by `headRows() == 6`.
- `TestRoundHeadRowsMatchView`: at heights 12, 18 and 40, the lines before the first viewport line equal `headRows()`.
- `split_test.go`: any `cardLines` use on the round pane moves to `tokensLine`/`Context`. **Fleet** `cardLines` tests stay as they are.

## 5. Tests

- New `TestFrameBlankRowUnderHeader`: in `Model.View()` output at 132x34, line index 1 is blank (spaces only), and line index 2 is
  the context row.
- New `TestRoundTokensLineKeepsOnlyCounts`: LiveUsage whose parts include the model name, a duration, `write 0` and `write 3`.
  The line contains `in `, `out `, `write 3` and the cost word. It does not contain the model name or `write 0`.
- Goldens: regenerate all of them. Every golden gains the blank row under the header. Round goldens also lose the card. Read
  `round-real-132.golden` and compare it with §2.

## 6. Commands

- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`, plus `-run Golden -update` for the goldens.
- Final: `gofmt -l internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`. Not `make check`.

## 7. Report

List:
- the files changed;
- every place fixed under §3.1's grep;
- every port with its C-number;
- `round-real-132.golden` and `fleet-real-132.golden` verbatim;
- the check results.
