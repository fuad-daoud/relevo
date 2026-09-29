# Cockpit review: waves 1-2 against the mockups (2026-09-24)

Scope actually covered: the UX walk in a real terminal (§3.2 of the handoff: `:fleet`, `:stats`,
`:`, round detail, a confirm, at ~200 columns inside herdr), compared with the 13-board canvas
(https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA), and a full code review of slice C2 (stats).
**Not yet reviewed:** the code of A1, A3a, A2 (+A2a/A5a), B1 and B2, the ANSI sweep, and the docs check.
Their reviewers were stopped before they reported.

Disposition key: **fix now** = before any new slice; **next wave** = fold into the next plan that
touches the area; **accept** = leave it.

## Headline

The built screens are not the mockups. The five boards that are built (fleet, round detail,
`:` palette, confirm, stats) render real data with the pre-cockpit components: the old round pane,
the old dash grid, an inline command line and an inline confirm. The mockup layouts were never
built. The goldens did not catch this, because they render tidy fixture data (a few short-named
bindings) and were never compared against the canvas. The real data breaks both the build and
parts of the design: 34 bindings (27 DONE), long tokens, worktree paths, 124 unrecorded rounds,
255 "no outcome" reports and sparse spend.

## Findings, ranked

| # | sev | area | finding | evidence | disposition |
|---|---|---|---|---|---|
| R1 | high | `:`, confirm | Nothing is drawn as a modal. The `:` completion list overwrites the top body lines (in `:stats` the candidates, spend and reliability heads vanish). The confirm is four plain lines at the bottom-left. The mockups draw a boxed overlay over a shaded screen. | walk U19, U24 | fix now (visual wave) |
| R2 | high | confirm | The confirm is thinner than the design. It lacks: spend so far, files changed, the consequence ("its relevo wait returns `stopped`…"), "worktree and branch are kept", a bold target, and a highlighted planner. | U25 | fix now (visual wave) |
| R3 | high | header (every view) | Gated candidates print as full tokens in amber and fill the header: `agy/antigravity/claude-sonnet-4-6 gated until Sep 26 18:04 · codex/openai/gpt-5.6-terra:high gated until …`. This breaks A1 (names on every human surface) and §4.5 (amber only for needs-you). The mockup has dim `openai gated 26d`. | U1, U10 | fix now |
| R4 | high | `:fleet` | 27 of 34 rows are DONE bindings, and they drown the live ones. The design has no rule for this (the mockup shows `1 done today`). A design amendment is needed: fold done bindings into one line or hide them behind a key. | U2 | fix now (design + build) |
| R5 | high | round detail | It is still the old round pane (planner/builder/tree/usage block). The name and round show three times: breadcrumb, context row, pane head. The builder line prints the raw token in literal backticks. The tabs are plan·report·terminal·diff·log. The mockup has plan·artifacts·diff·log·transcript plus a `[ ] round r1 r2` switcher. | U21-U23 | fix now (layout); artifacts tab: next wave (A4/A5) |
| R6 | medium | stats + header | One gate shows two expiry times on one screen: the header says `gated until Sep 26 18:04`, the stats "active" line says `rate-limited until Sep 26 15:04`. Codex agrees (22:16) in both places. The cause is not yet traced (two sources, or a timezone). | U11 | fix now (investigate) |
| R7 | medium | `:fleet` | The REPO column shows the binding's worktree path, left-truncated (`…local/state/relevo/.worktrees/spool-db`), for worktree bindings, and the repo for the others. | U3 | fix now |
| R8 | medium | `:fleet` | An unexplained amber `●` sits in the gutter of ~20 rows, mostly DONE, while the header says 0 need you. There is no legend, and amber is reserved for needs-you. Heatmap counts are amber/red too. | U4, U18 | fix now |
| R9 | medium | stats | Outcomes shows two taxonomies side by side. `rounds reported 429 · halted 1 · switched 0 …` and `reports done 153 · halted 20 · … no outcome 255` contradict each other: halted 1 vs 20, and switched 0 vs `switches 22` in reliability. A relaunch still makes a round `switched` and "closed" with a bogus duration (C2-1). The mockup has one bar list. | U14, C2-1 | fix now (pick one taxonomy) |
| R10 | medium | stats | `$/RND` reads as nonsense: sonnet $3.84 × 69 rounds ≫ the $27.01 total. It is a mean over the few rounds with a known cost, and nothing says how few. The plan exclusion uses the candidate's current plan flag, not a per-round one (C2-5). The header total drops unrecorded rows' cost while spend and repos keep it (C2-3). | U12, C2-3, C2-5 | fix now (C2-3, show n); C2-5 next wave |
| R11 | medium | stats | The spend chart is four thin one-column bars at the right of an empty panel, with the axis labels cramped at the left. In the 7d window last week is always $0 and the change reads `new` (C2-2). `new` also prints when both weeks are $0 (C2-8). | U13, C2-2, C2-8 | fix now |
| R12 | medium | process | The goldens use tidy fixtures and were never compared against the canvas. A visual wave needs goldens built from a real-shaped fixture (30+ bindings, most DONE, long tokens, worktree paths, gates, unrecorded rows) at 132×34, and each golden reviewed beside its board. | all | fix now (with the visual wave) |
| R13 | low | stats | TTFT is not windowed (always 30 days). Limits and the heatmap are silently capped at 30 days for 90d/all (C2-4). | C2-4 | next wave |
| R14 | low | stats | There is no actor scoping: no `a` key, `Keep` is unwired, and the context row has no `actor builder`. Active gates print the token instead of `Gate.Name`. | U16, C2-12 | Gate.Name: fix now; actor: next wave (A4) |
| R15 | low | stats | Panel titles are dim `— candidates —` rules, where the mockup uses bold CAPS plus a dim subtitle. Repos show `(none)` for 142 rounds and raw keys (`/home/fuad/tmp/relay-try/.git`, the old `…/relay` name). | U15, U16 | fix now (visual wave) |
| R16 | low | `:` | The command line offers only view verbs: no resources (`:actor designer`, `:candidate haiku`), no counts, no hint line. The list starts with `log`. | U20 | visual wave; resources: C1 |
| R17 | low | `:fleet` | No selected-row background (only a thin bar), and the cursor started on the last row. No header or footer bar backgrounds. ACTIVE rows read `idle` with no explanation. SPEND is blank and PLANNER `–` with no meaning given. | U5-U8 | fix now (visual wave) |
| R18 | low | `:fleet` | There is no "recent" events strip. | U9 | next wave |
| R19 | low | stats CLI | The header's Since is UTC, Until is local (C2-7). The cursor is not clamped after a window change; pgdn is unbounded; bars top out at `▇` (C2-10). Tests: local-day bucketing and the relaunch note format are not pinned (C2-9). | C2-7, C2-9, C2-10 | C2-7: fix now; rest: next wave |
| R20 | low | stats | A buried `:stats` keeps refetching every 30s (C2-6). `Gates` can write to stderr during the TUI fetch (C2-11). | C2-6, C2-11 | accept |

C2 checks that passed: the division-by-zero guards, the `(unrecorded)` exclusion, `unstructured` → `no outcome`,
one implementation shared by `history --stats` and `:stats` (the wave-1 `internal/relevo/stats.go` is gone), and
`history --stats` output is plain text with no ANSI.

Full evidence: the UX walk notes (U1-U25) and the C2 report are kept with this review's working files.
