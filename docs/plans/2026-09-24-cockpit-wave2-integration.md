# Cockpit wave 2: integration round

The branch `cockpit/wave-2` is current `main` plus three verified slices, replayed in
this order:

1. C2b: the `:stats` view.
2. B2: cockpit actions, rounds 1 and 2.
3. A2: actors and agents, rounds 1–3.

Their plans are in `docs/plans/2026-09-24-cockpit-*.md`, on the `docs/cockpit-spec`
branch, and the spec is `docs/specs/2026-09-24-cockpit-design.md` on this branch. The
B2×C2b conflicts in `internal/ui/cmdline.go` and `view_rounds.go` were resolved by the
planner as a union: both command rows, and both dispatch cases.

This round makes the slices work together and closes the gaps the reviews found.
**Do only these steps.**

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise.** CI has no harness and no network. No `cmd/relevo` test may run a verb
that spawns a harness or reaches a server, or run `relevo ui` or bare `relevo`.
**Plain text only:** no ANSI escapes in `internal/relevo` or `cmd/relevo` output.

## W1. Two goldens that changed for known reasons

`go test ./internal/ui/ -run TestGoldenViews` fails only on:

- **`cmdline-open`:** the completion list now has C2b's `stats` and B2's `log` and
  `ungate` rows.
- **`stats-narrow`:** B2's footer rule now drops whole keys on the stats view.

Regenerate both with `-update`. **Read each diff:**

- `cmdline-open` must differ only in its completion lines.
- `stats-narrow` must differ only in its footer line, where no key is cut and
  `? help` is present.

Paste both diffs into the report. If either differs anywhere else, stop and report.

## W2. Sort gets its own key (`internal/ui/view_fleet.go`, around `:234` and `:246`)

- B2 gave `s` to send whenever `Actions` is set, so a planner cockpit has no sort key.
- Move sort to `a` ("attention"), everywhere: the key handling and `Keys()`, which
  shows `a sort` whether or not `Actions` is set. `s` is send only when `Actions` is
  set, and does nothing otherwise.
- Update the tests that press `s` to sort (grep `Runes: []rune{'s'}` and `press(…"s")`
  in `internal/ui/*_test.go`) and the goldens whose footers change. Read them.

## W3. The send confirm names the round `Send` will open

- B2 round 2 labels the confirm `as round <b.Round+1>`, as its plan said. That was
  wrong: `relevo.Send` opens round `b.Round`, because the daemon advances `b.Round`
  when a round closes (`internal/relevo/reconcile.go:530`).
- Use `BindingStatus.Round` for the label. For a binding with a round **open**, the
  send is refused by `Send` anyway, so keep the label and let the refusal come back as
  the action's error.
- Fix the text and its test (grep `as round` in `internal/ui`).

## W4. `config init` writes no `"roles": null`

- `internal/candidate/candidate.go:59`: `Roles []string json:"roles"` becomes
  `json:"roles,omitempty"`.
- Check that nothing reads the field's absence as meaningful. Grep `.Roles` in
  `internal/candidate` and `internal/roles`. Legacy mode reads `c.Roles` only when
  there is no actors section, and a nil slice behaves the same either way.
- Test: `setup.Plan`'s encoded candidates contain no `"roles"` key.

## W5. Few days get wider spend bars (`internal/ui/view_stats.go`)

- With fewer days than panel columns, each day is one column wide and the bars bunch
  at the left.
- Give each day `max(1, min(3, columns / days))` columns, where the gap between days
  is part of that width: a bar of width-1 blocks, then one space.
- Test: a 4-day report at width 160 gives bars three columns wide.
- Regenerate `stats-wide` and read it.

## W6. Check

- `make check` and `make e2e` must pass.
- Mutation check: press `s` on a fleet with **no** `Actions` and assert nothing sorts
  (this is W2's rule). Then temporarily map `s` back to sort in that case: the test
  must fail. Report it, then revert.
- Report:
  - each step's status;
  - every changed test with the reason;
  - the pasted golden diffs;
  - `git diff --stat`, which must touch only `internal/ui/**`,
    `internal/candidate/candidate.go`, `internal/setup` (test) and the goldens.

## Working efficiently

Read `internal/ui/view_fleet.go`, `view_stats.go`, `confirm.go` and `actions.go` (for
the send confirm), `golden_test.go` and `internal/candidate/candidate.go` once. Make
each file's edits in one call. Iterate on
`go test ./internal/ui/... ./internal/candidate/... ./internal/setup/...`. Run
`make check` and `make e2e` once at the end.
