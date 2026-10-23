# Runner liveness, plan 2 of 7 (#1310, #1330, #1311)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

**Behaviour.** After relevo resumes a session (the nudge today; plan 4 will use
the same hook), a builder that goes 10 minutes with no worktree change is shown
as `exploring <age>` in status, and the round log gets one note line once per
episode. Label only, no action. A round that was never resumed keeps today's
`explore_after` threshold (20 m).

**Seams.**
- `internal/store/binding.go:84-92` `Progress`: add `ResumedAt time.Time`
  (`json:"resumed_at,omitempty"`). Additive and omitempty, so older relevo loads
  the row; do not bump `BindingFormat` (`internal/store/format.go:49`) unless a
  test says the shape change needs it. Check the comment above it and say in the
  report what you decided.
- `internal/relevo/nudge.go:109-148` `nudgeResume`: after the resume succeeds
  set `next.Progress` to a fresh `&store.Progress{SampledAt: now, TreeAt: now,
  OutputAt: now, ResumedAt: now}` (empty `Tree` so the next sample sets it).
  Add the same line to no other path in this plan.
- `internal/relevo/progress.go:112-118` `progressStep`: when `p.ResumedAt` is set
  use `min(ExploreAfter(), resumedIdleAfter)` as the threshold. Add the constant
  `resumedIdleAfter = 10 * time.Minute` with a comment saying where 10 comes from
  (about the length of one full `make check` plus a retry; the 3 h round polled
  for 188 min). Keep `progressStep` under 70 lines: extract the threshold choice
  into a small helper in the same file.
- Log line: the `ExploringSince` zero-to-set edge is already observed in
  `internal/relevo/reconcile.go:54-65` (where the `builder_stalled` hook edge is).
  Add an edge there, in a new helper in a new file `resumed_idle.go`, that
  appends one `store.KindSwitch` entry (Confirmed, `DirToMasterMind`) with note
  prefix `resumed session idle:` when `next.Progress.ResumedAt` is set. The note
  says how long the tree has been unchanged since the resume. `KindSwitch` is the
  kind the nudge entry already uses; it does not count against `max_switches`
  (`RoundSwitches` is a field, not a log count), and it renders as a switch line
  in the round's report, which is the intended trace. If appending a log entry
  needs a `tx` that reconcile's edge section does not have, put the append where
  `reconcileHeadless` holds `tx` (the alive branch, `headless.go:~942-947`, right
  after `progressStep`) with a three-line call, comparing `next` against `b`.
- Status: no change; `labelsOf` (`progress.go:134`) already prints
  `exploring <age>`.

**Steps.**
1. Add `Progress.ResumedAt`; set it in `nudgeResume`. Done when a nudge test
   shows `Progress.ResumedAt == now` on the returned binding.
2. Threshold helper and `progressStep` change. Done when the new progress tests
   pass.
3. The one-per-episode log edge. Done when the log test passes.
4. Tests in `internal/relevo/progress_test.go` (existing file) and a new
   `resumed_idle_test.go`: `TestProgressStepLabelsResumedSessionAfterTenMinutes`
   (resumed, tree still, output moving: label at 10 m, not at 9 m),
   `TestProgressStepKeepsExploreAfterWhenNeverResumed` (same facts, no
   `ResumedAt`: no label at 10 m, label at 20 m), `TestResumedIdleNoteWrittenOncePerEpisode`
   (two consecutive ticks with the label set write one entry; label clearing and
   re-arming writes a second). Pure functions in `internal/relevo`; no CLI test.
   Mutation check: set the resumed threshold to the plain `ExploreAfter()`, the
   first test must fail.
5. Run `make check-affected`.
6. Commit.

**Report must include:** whether `BindingFormat` was bumped and why or why not;
the log note text; confirmation that no new `LiveFacts`/remote field was added.


