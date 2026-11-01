# The cockpit: relevo ui

`relevo ui [:view [args]]` is the cockpit: a full-screen terminal reader for
live bindings. `relevo status` remains the tool for shell pipes and scripts
(`watch -n2 relevo status` for a ticker); `relevo ui` is the interactive
sibling that lets you inspect substance instead of just state.

It is **read-only**: it never mutates state and never
appends to round logs. It holds the state lock only for the duration of a
read, exactly as `relevo status` does.

`:fleet` is the root: every binding as a table row (name, actor, candidate,
round, state, age, spend, MasterMind, repo). `s` toggles attention and name
order and `/` filters the rows; `enter` opens the selected binding's round
detail, `esc` goes back to it, `:` opens the command line (`:fleet`,
`:rounds [query]`, `:round <binding> [N]`, `:chains`, `:stats`, `:candidates`,
`:actors`, `:workflows`, `:agents`, `:servers`, `:sync`, `:settings`, `:audit`,
`:log`), `?` lists every key, and `q`
quits at the root. A writer round's pane has five tabs:
- **prompt** — the round's own prompt file, first in the order.
- **report** — that round's MasterMind-bound report or question payload.
- **terminal** — recent live terminal output from the builder's log.
- **diff** — the captured git patch from the round.
- **log** — the formatted append-only round log, scoped to the round.

A reader round (reviewer, researcher, planner) instead shows four:
**prompt**, **artifacts**, **log** and **terminal** — no report and no diff,
with the artifacts tab in their place.

`[` and `]` step the round a tab reads, from round 1 through the binding's
current round -- every tab refetches for the new round. Stepping onto a
live binding's own open (not yet closed) round reads as prose, not an
error: the report and diff tabs say so ("round N is open; report arrives
when it closes", "diff is captured when round N closes") rather than
showing stale content.


## `relevo ui`'s `:rounds` view: every round, filtered and regrouped

`:rounds` opens a second view: every round relevo's database has recorded, live
or archived, as a grid. It is the same data
[`relevo history`](commands.md#relevo-history) reads, with the query language
as a filter line and the sums of `--by` above the rows. `esc`
returns to the fleet with the query intact; `relevo ui :rounds` opens here
directly, and `relevo ui :rounds harness:agy` opens it with a query.

The first line is the applied query and the regroup axis; under it a tiles
line — `rounds 57   cost $14.20 (3 unknown)   tokens 41.2M   halted 4 · exited
2   median 23m   bindings 12 · builders 3` — then the grid:

```
started           binding       rnd  builder                               outcome        commits  tree    gate   tokens   cost     duration
2026-09-20 22:01  persist       r5   claude/anthropic/sonnet               reported       +1       clean   pass   1.2M     $0.42    27m
```

`/` opens the query line, prefilled with the current query; `enter` applies it,
`esc` cancels, and a parse error is shown under the input with the previous
query kept. The grammar is exactly [`relevo history -q`](commands.md#relevo-history)'s:

```
/ harness:agy outcome:halted since:30d
/ auth cost>1            (then b to regroup by builder)
/ by:day since:14d
```

`b` regroups by the next axis (binding, repo, feature, ticket, builder, harness,
provider, model, day, outcome); groups carry rounds, reported, halted, commits,
tokens, cost and the last round's date, and `enter` on a group expands its
rounds beneath it. `s` cycles the sort column for the level under the cursor
(rounds: started, cost, tokens, duration, commits; groups: cost, rounds,
halted, last) and `S` flips the direction; `r` re-queries now, and the fleet's
tick re-queries at most every 10 seconds while the screen is up. `enter` on a
round row opens that binding's round detail, live or archived. The query text
and the sort column are remembered in the machine database.

Below 140 columns the grid drops `gate`, below 120 `tree` and `commits`, below
100 `tokens`; `cost` always stays. A round's cost reads `unknown` as `?`, a
round with no usage at all as `-`; archived rounds are dim; the same filter
grammar is documented in full under [`relevo history`](commands.md#relevo-history).

## `relevo ui`'s archived bindings: every binding, not just today's

The fleet lists live bindings only. Every binding the database has ever
recorded stays reachable through `:rounds`: its grid carries archived rounds
too, and `enter` on one opens the same tabs (five for a writer round, four
for a reader round), reading the database instead
of files -- `terminal` renders the round's stored transcript rows and does not
follow a tail, since nothing about an archived round is still moving. A
database relevo cannot open shows `no database: <err>` as a sticky notice and
refuses `:rounds`; `relevo ui` never exits over it.
