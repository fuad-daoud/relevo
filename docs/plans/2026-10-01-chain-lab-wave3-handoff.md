# Chain lab: wave 3 (2026-10-01, MasterMind opencode-116)

Waves 1-2 handoffs are the method and lab layout; this is what wave 3 did on
top. Evidence: `~/.cache/chain-lab/evidence/w3/` (`INDEX.md` + `NOTES.md`),
lab project `~/.cache/chain-lab`.

## State

- Servers run `v0.15.0-33-g78bd97fd`; the laptop moved to
  `v0.15.0-34-g0bb6fd7a` while the wave ran (another session's #825
  statusline build). The delta touches no chain, store or wire code; each
  scenario's build is recorded in the evidence. The wave closed a delivery
  defect found by its own late deliveries (see Follow-up); every box's daemon
  now runs `v0.15.0-36-gd5d86b61`.
- Positive paths all green on this build: `w3-a` (2-plan chain, builder on
  zen), `w3-s` (security phase, server chain on contabo, `no findings`),
  `w3-stop2` (stop mid-build -> resume -> done).
- Plain readers vs #818: a block at the tail saves the block-carrying message
  minus the block, byte-identical (`w3g-sec`); a same-message recap after the
  block keeps the block-carrying message as the artifact but closes
  `unstructured` (`w3e-rev` r1) -- the recap no longer replaces the artifact,
  which was the #818 bug. The separate-message recap shape did not reproduce
  live in three tries (models kept the recap in the same message or wrote no
  block); it is pinned by `TestReaderCloseOfARecapRoundIsNotUnstructured`.
- lab-h2 retry, raw streams captured before release:
  - r1: the mimo reviewer created the marker, then its session died with a
    Transport error and sent no text -> `reviewer gave no verdict` (accurate
    failure, no artifact). Not the block+recap shape.
  - r2: the same reviewer put its `Recap:` line *before* the verdict block
    (it reasoned the block must be final) -> `verdict: pass`, chain done.
  - Settlement: the strict "block at the tail" parse is pinned
    (`TestParseVerdictRejectsMissingBlock`); a recap after the block in the
    same message is a contract-violating output and halts fail-closed. The
    original lab-h2 event was the model following the plan's reviewer note
    over its chain prompt, not a parse bug.
- Negatives: duplicate `--plan` is accepted and the chain ran the same plan
  twice cleanly (an idea to refuse, not a defect); bad `--ticket` is
  `refused` exit 2; `--resume --gate` on a remote builder is `refused` exit 2
  (the #812 folded case); `done` while running is `conflict`; `done` on a
  stopped chain is accepted by design (`ChainDone`: the human is finished).
- Correction loop: not induced live this wave -- five attempts (a prose-trap
  set the builder satisfied; a contradiction with no guidance the builder
  wandered on; the same contradiction with guidance the reviewer sanctioned;
  a reviewer checklist the builder satisfied anyway; a tiny contradiction the
  builder halted on before review). The builder (gemini-3.8-flash-high) is
  thorough; the reviewer passes sanctioned deviations. A subtle unsanctioned
  miss (a format detail the builder paraphrases) is the next lever.

## Findings / issues

- No new issues filed. #804 got a comment: the artifact side is fixed, but
  every chain reviewer/security close still records `outcome=unstructured`
  (and the new statusline shows `ARTIFACT IN · unstructured` on passing
  rounds) because the chain-reader artifact is stripped before the close's
  tail parse.
- Observations, not filed: duplicate `--plan` accepted; the statusline's
  `unstructured` member rows above; a same-message recap leaves the close
  unstructured (tail contract).
- #799 (no unread member rows) and #805 (done on a finished server chain)
  re-verified green here (95/9a/9b, b10/b11); #797 and #800 were re-verified on
  `lab-h3` in wave 2 and were not repeated. #797/#799/#800/#804/#805 are still
  open and can likely be closed by their owners.

## Follow-up: delivery gap found by a late delivery (#830)

After the scenarios, two lab payloads were pushed to the MasterMind after
their binding was done and stayed unconfirmed (`confirmed=0, route=NULL`):
`w3g-sec` round 1's report (whose status still read `pending`) and `w3-c4`'s
chain end. Root cause: a done/paused binding's tick returns before delivery,
and an admitted entry is not claimable, so nothing ever reads it back.

Filed #830; fix PR #831 (`delivery.ConfirmAdmitted`: read-back only, never a
push; called from the done/paused branch of reconcile) merged as `d5d86b61`.
`make check` green; CI green after two re-runs of the known #828 macOS e2e
flake (which also reproduces on the Linux laptop, at the base commit). Deployed:
the laptop and zen daemons re-exec'd onto `v0.15.0-36-gd5d86b61` (zen's
`relevo-serve` left on the previous binary while another session's two runners
were live -- the fix is daemon-side), contabo via `srv.fish deploy
relevo-serve`. Re-verified live: both stuck rows confirmed themselves
(`delivered_at=16:11:03Z`, route `deliverer:opencode`) with no new push.

## Lap 4 (same session, right after wave 3)

- The correction loop from wave 3 is now exercised: `w3-c8` on
  `v0.15.0-38-g1b60f9a6` ran build -> review `changes` -> correction plan ->
  builder correction -> review `pass` (`corrections: 1`). Evidence
  `~/.cache/chain-lab/evidence/w4/`. Seven earlier attempts failed to induce
  `changes`; the working trigger is a plan whose reviewer acceptance is about
  the closing round kind (deterministic, not model-dependent).
- #804 leftover fixed (PR #845): a chain reader's parsed block records the
  member round `done` instead of `unstructured`.
- Leftovers PR #848: the OOM requeue stores the killed scope's peak; `--regate`
  travels to a plain remote bind (wire + client resolution + mirror + server);
  `RemoveScratch` skips quietly when a shared tree is gone (#821).
- Issues: closed #797/#799/#800/#805 (re-verified) and #798 (fixed by #806;
  each refusal class now pinned by a test); filed #847 for the
  `ErrReportPending` resume halt (a naive wait-for-close would hang: the round
  is not open in that state; the issue carries the analysis).
- One deploy of accumulated main follows both PRs' CI; the re-verify set is in
  `evidence/w4/INDEX.md`.

## Method notes

- Raw streams are copied from the server (`/srv/data/relevo-serve/serve/
  bindings/<owner>/<member>/NNN-runner.jsonl`) or from
  `~/.local/state/relevo/<member>/` before any `done`; round inputs are swept
  after close locally and after release on the server.
- `relevo db query` still needed for old records only; plain `sqlite3` cannot
  open the store.
- Builds move under the lab: check `relevo --version` on all three boxes
  before trusting a scenario's build.

## Housekeeping

- All wave-3 bindings are done: w3-a, w3-n1, w3-s, w3-stop, w3-stop2, w3-c,
  w3-c2, w3-c3, w3-c4, w3-c5, w3h2, w3e-rev, w3g-sec. `relevo unbind --done`
  when the records are no longer needed; the evidence in
  `~/.cache/chain-lab/evidence/w3/` is the record.
- One timing note: `w3-c5`'s server-side builder finished (marker + gate) just
  after the client stopped and abandoned the chain; the server dir closes
  late, the client chain is done. No late delivery had arrived at writing
  time; watch for a server-close delivery after the done.
- Still open from earlier waves: sub-chains (#754, now folded into #792),
  the `ErrReportPending` resume halt, `--regate` on a plain remote bind, the
  OOM peak.
