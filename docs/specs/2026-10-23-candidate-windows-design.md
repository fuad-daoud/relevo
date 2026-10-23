# Candidate windows and concurrency caps -- design

## 1.1 Goal

A candidate may declare when it may be used and how many rounds may run on it at
once, and the pick honours both:

- the owner wants `alibaba-tp/qwen3.8-flash` used only in the Token Plan night
  window (`14:00Z-00:00Z`, 40-50 % of the day rate) and wants the other
  candidates used outside it;
- the owner wants a plan's concurrent-agent allowance respected, so a night
  window that sends every round to one provider does not earn a `429
  concurrency allocated quota exceeded` (and does not trip anti-abuse).

Neither rule may kill a round already in flight: a window edge and a cap are
pick-time facts, not round-time kills.

## 1.2 Config shape

Two new optional keys on a candidate row in the `candidates` section. Both
travel with the candidate item, so both sync (§1.8).

```jsonc
[
  {
    "name": "qwen3.8-flash-tp",
    "harness": "opencode",
    "provider": "alibaba-tp",
    "model": "qwen3.8-flash",
    "roles": ["builder"],
    "max_concurrency": 2,
    "windows": {
      "mode": "block",                                  // optional; "block" (default) | "demote"
      "only":  [{"from": "14:00Z", "to": "00:00Z"}],     // outside these: blocked or demoted
      "avoid": [{"from": "00:30Z", "to": "05:00Z"}]      // inside these: blocked or demoted
    }
  },
  {
    "name": "deepseek-tp",
    "harness": "opencode",
    "provider": "opencode-go",
    "model": "deepseek-v4.1-flash",
    "roles": ["builder"],
    "windows": {"only": [{"from": "00:00Z", "to": "14:00Z"}]}
  },
  {"name": "sonnet-medium", "harness": "claude", "provider": "anthropic",
   "model": "claude-sonnet-5-5#medium", "roles": ["builder"]}
]
```

Types (`internal/candidate`):

- `Windows{Mode string, Only []Window, Avoid []Window}`,
  `Window{From, To string}` — decoded, validated, never re-encoded (the stored
  body is the raw JSON; the struct is a decode view).
- `MaxConcurrency int` on `Candidate`.

`mode` is one word for the whole `windows` object and applies to both lists:

- `block` (default) — a gate, so the candidate is skipped even when nothing
  else is left. This is the owner's Qwen rule.
- `demote` — no gate; the candidate moves to the end of the actor's ranked
  order and is still picked when nothing else is available.

Clock forms accepted in `from`/`to`, and refused otherwise at load:

| form | meaning |
|---|---|
| `"14:00"` | UTC |
| `"14:00Z"` | UTC |
| `"22:00+08:00"`, `"22:00+0800"` | fixed offset |
| `"17:00 Asia/Amman"` (one space) | IANA zone, `time.LoadLocation` |

A named zone is resolved per instant (the window follows that zone's local
clock). `Z` and offsets are recommended in the docs, because a named zone
resolves identically on two hosts only when their tzdata agree — the point of
the key is that every host resolves the same pick.

Refused at load, each naming the field and the value:

- a clock that does not parse, or a zone that does not load;
- `from` equal to `to` (a window covering nothing, or a whole day — say which
  with two windows instead);
- an unknown `mode`;
- `windows` with both lists empty;
- `max_concurrency` below 1 (0 and negative are refused, not read as "no cap" —
  omit the key for no cap).

## 1.3 The gate kind

A window that blocks is a gate synthesised in memory from config plus the
clock, exactly like `rolesMissingGates` (`internal/availability/gates.go:295`):
never written to the ledger, never cleared by `relevo gate --clear`, and never
exported by the gates document.

- New kind `availability.Scheduled Kind = "scheduled"`.
- `Token` — the candidate's canonical ref (so it gates that candidate only,
  never the provider, unlike `RateLimited`).
- `Kind` — `Scheduled`.
- `Note` — `"outside window 14:00Z-00:00Z"` for `only`, `"inside window
  00:30Z-05:00Z"` for `avoid`; the window's own config text, as written.
- `Until` — the next instant the candidate's window state flips, so the gate
  says when it lifts.
- `Since` — `d.Now()`.
- `Source` — `"relevo"`.
- `Role` — `""` (a window is the candidate's, so it applies to every role the
  candidate serves).
- `Demote bool` — true only for a `demote` window. A demote gate is *not* a
  skip: the walk moves that candidate to the end of the order instead (§1.4).
  A `Demote` gate must be ignored where "gated" means "unusable" — the two
  serve wire views (`internal/serve/candidates.go:26`,
  `internal/serve/actors.go:66`) and doctor's rows.

`GateKindText(Scheduled)` returns `"outside window"`. The new text helper
`GateText(k Kind, until time.Time, note string) string` is the one rendering of
one gate's reason: for `Scheduled` and `ConcurrencyLimit` it is the `Note`
(which is the whole statement), for every other kind
`GateKindText + " " + GateUntilText`. `Gate.UntilText()` is `""` for those two
kinds, because "until cleared" is wrong for both. Every renderer that today
prints `GateKindText(g.Kind) + " " + GateUntilText(g.Until)` moves to these two
helpers (§1.7), so no surface can drift.

Nothing else about the ledger changes: `knownKind`
(`internal/availability/ledger.go:80`) stays as it is (a `Scheduled` entry is
never written), `decodeGatesValue` (`gatedoc.go:179`) still refuses any kind but
`RateLimited` on the wire, and `gateItems` (`gatedoc.go:87`) still filters to
`RateLimited`. The schedule travels with the config, not with the gates
document.

## 1.4 The pick

`resolveRoleBase` (`internal/relevo/candidate.go:310`) is unchanged in shape:
the gates it is handed decide the pick, so a window gate and a concurrency gate
are skipped by the existing `skipsFor` walk. Two additions:

1. **Demote.** The ranked list is filtered: entries whose candidate carries a
   live `Demote` gate move to the end, keeping their relative order. This lives
   in one shared function so the pick and `relevo config` cannot disagree — the
   walk (`resolveRoleBase`), the display (`FormatPolicyFor`,
   `FormatPolicy`/`rankedList`) and the refusal check (`RoleRefusalsFor`) all
   read the same partitioned rows.
2. **The refusal text.** When every serving candidate is skipped, `allGated`
   (`candidate.go:436`) keeps its present words byte for byte when every skip
   is a real gate. When a skip is a window or a cap, it names that reason
   instead of offering `relevo gate --clear` (there is nothing to clear): the
   clause names the window, or the cap with its use (`2/2`), and the next step
   is to wait for the edge, or to wait for a round to finish.

Order is unchanged: the actor's ranked order first, then the unlisted
candidates; off entries still skip; a sole serving candidate that is blocked
still refuses, which is the owner's "Qwen is not wanted outside the window even
when it is the only candidate left".

## 1.5 The re-pick between rounds

A round in flight is never moved (§1.9). Between rounds, the binding follows
the pick.

At the start of a round — `sendPreflight` (`internal/relevo/send.go:325-334`),
which local headless sends, served sends (the server's own `Send(Defer)`,
`internal/serve/rounds.go:413`) and remote sends all share; the chain member's
send (`internal/relevo/chain_send.go:85`); and a repair round
(`internal/relevo/repair.go:153`) — the builder is re-resolved when all of:

1. the round names no token (`--candidate` / `--builder` absent), and
2. the binding is not pinned (§1.5.1), and
3. at least one candidate serving the role carries a window in config.

Then the resolution over the live gates (windows included) is applied to the
binding when it names a different token than the binding holds, and a pick entry
is recorded for the new round. The printed line says what moved and why:
`note: builder <old> is outside its window; <pick text>`.

Condition 3 is what keeps this change window-scoped: on a host with no window
configured, a rate-limited candidate still never moves a binding between rounds,
exactly as today (`sendPreflight`'s gate is advisory,
`internal/relevo/send.go:449-457`).

This covers the two directions the issue asks for:

- off a candidate whose window closed: at `00:00Z` the qwen binding moves to
  deepseek;
- onto a higher-ordered candidate whose window opened: at `14:00Z` the deepseek
  binding moves to qwen, which is order #1 and now unblocked.

Not covered on purpose: a round that is already sent and queued (waiting for a
server's `serve.max_builders` slot) keeps the candidate its send picked; the
pick is a property of the send, and a queued round is the same round.

### 1.5.1 The pin

`--candidate` / `--builder` is honoured exactly as given and is never
re-picked. Because the token is stored on the binding and stays there (a
`send --candidate X` re-points the binding today), the *binding* must record
that its candidate was named, or a window edge would move a candidate a person
chose. So:

- `store.Binding.BuilderPinned bool` (`json:"builder_pinned,omitempty"`) — a
  stored fact that `BindingFormat` does not stamp: an older relevo that drops
  it never re-picks for a window, so losing it is harmless. It joins the
  never-stamped list in `internal/store/format.go`'s doc comment, and
  `BindingFormat` stays 18.
- Set to true wherever an explicitly named token becomes the binding's
  candidate: `applyBuilder` (from `Resolution.How == HowExplicit`), the bind
  and rebind paths, `add`, the served create, and the chain member creates that
  decide their own pick. A pick that is not explicit (a switch, a re-pick) sets
  it false. The client-side mirrors of a *served* binding need no pin: a remote
  binding is never re-picked locally (`staleBuilder` returns false for one,
  `internal/relevo/stale.go:13`), so the server's record is the one that
  matters.

## 1.6 The concurrency cap

`max_concurrency` on a candidate: at pick time a candidate at its cap is
skipped exactly like a gate, and *without* gating the provider — the gate's
token is the candidate, no ledger entry is written, and every other candidate
of the same provider stays pickable.

**The count.** The number of rounds in flight on a candidate, over every
binding the resolving daemon or server knows:

- a round is in flight while its builder *process is live* — the same test the
  server's own builder census uses (`internal/serve/admit.go:44-100`): the
  builder is headless, its PID is non-zero, and the binding is not DONE or
  PAUSED. A queued round holds no slot yet, and a halted round (NEEDS YOU) has
  no process, so neither counts. This is deliberately not `store.RoundOpen`: a
  round whose process died but whose report has not landed holds no slot at the
  provider, and counting it would let a halted binding block its own plan
  forever;
- the binding being resolved does not count against itself (its own round is
  being replaced, not started) — the one caller that needs this is the
  mid-round switch;
- on a server the count spans **every owner's bindings**, because the server's
  candidates, accounts and policy are the server's (`s.live()`,
  `internal/serve/serve.go:366-390`) while a per-owner runtime's `Store` is only
  that owner's. Two owners sharing one Token Plan must share one count;
- on a machine with no server the count spans that machine's store.

**The pool.** The count is per *pool*, and a pool is the candidate's
harness+provider — the plan, not the model. So a candidate's use is the sum of
the live rounds over every candidate that shares its harness and provider.
Reasons and the rejected alternative: the physical allowance is per plan (the
Token Plan's concurrent agents), and a per-candidate count would let two models
of one plan run 2+2 against an allowance of 2. A provider-level key was
rejected because it would add a section for one number and need a precedence
rule between it and the candidate's own; the same number written on each
candidate of the plan is the config that says it, and `relevo config` warns
when two candidates of one pool declare different numbers. With accounts, a
second login is a second plan and therefore a second allowance in principle;
this round counts logins together (conservative, never over-subscribes) and
says so rather than half-implementing a per-account key.

**The gate.** Kind `availability.ConcurrencyLimit Kind = "concurrency_limit"`,
`GateKindText` → `"at concurrency limit"`, `Token` = the candidate,
`Note` = `"2/2"` (use over cap), `Until` zero, `UntilText()` `""`,
`Source` `"relevo"`. One gate per candidate at or over its cap, synthesised in
`GatesFrom` beside the window gates.

**The count source.** `availability.Deps.InFlight func() map[string]int` — nil
means no count is available and every candidate reads as under its cap (every
test that does not set one, and a caller with no store). Keyed by candidate
token. `InFlightExcept string` names the binding whose own round must not count
against itself. `AvailabilityDeps` (`internal/relevo/runtime.go:395`) fills
both: `InFlight` from an optional `Runtime.InFlight`, else from a walk over
`rt.Store`; the server sets `Runtime.InFlight` to its machine-wide walk. The
walk runs only when some configured candidate declares `max_concurrency`, so a
host with no caps pays nothing.

The mid-round switch (`internal/relevo/switch.go:234`) passes the binding's
name as `InFlightExcept`, so a switch that lands back on the binding's own
candidate does not halt it on its own slot.

## 1.7 Display

- `relevo config` — the pick block's row tail (`formatPolicyRole`,
  `internal/relevo/policy_view.go:196-251`) uses `GateText`, so a blocked
  candidate reads `outside window 14:00Z-00:00Z`, and one at its cap reads
  `at concurrency limit 2/2`. A demoted candidate shows its window text at its
  demoted position, and the `<- would pick` marker follows the pick.
- `relevo gate` — `RenderGates` (`internal/serve/admin.go:467`) lists the
  synthesised gates beside the ledger's, with the window or the cap as the note.
  `relevo gate --clear <provider>` removes only the ledger's rate-limit entries
  and says so when nothing was gating; a window gate survives, and clearing a
  window gate is not a thing to ask for.
- The pick explanation — `skipText` (`internal/relevo/candidate.go:198`) uses
  `GateText`, so `relevo log`, the bind/send stderr line and the pick entry all
  read `skipped qwen3.8-flash-tp (outside window 14:00Z-00:00Z)`.
- `relevo send --dry-run` — the gate note (`dryRunGateNote`,
  `internal/relevo/send.go:876`) must not claim a switch for a window or a cap:
  those say `outside window 14:00Z-00:00Z; this round runs here, and the next
  round re-picks` and `at concurrency limit 2/2`. `RateLimited` and
  `RolesMissing` keep today's words ("the daemon would switch after start").
- `relevo doctor` — `ledgerChecks` (`cmd/relevo/doctor_checks.go:110`) keeps
  its rows for the ledger's own kinds only: a schedule is not a fault, and a
  warning per window-blocked candidate would be noise. A refusal caused by a
  window or a cap gets a fix that fits it (`refusalChecks`,
  `cmd/relevo/doctor_checks.go:248`): wait for the edge, or wait for a round to
  finish — never `relevo gate --clear`, which would do nothing.
- `relevo status` and the status-line document keep listing every live gate,
  including the synthesised ones: "why did the pick move" is exactly what a
  reader of status is asking. The two wire candidate views
  (`internal/serve/candidates.go:26`, `internal/serve/actors.go:66`) mark a
  candidate gated only for a gate that means unusable, so a `Demote` gate does
  not mark one.

## 1.8 Sync and validation

- **What travels.** `windows` and `max_concurrency` are fields of a candidate
  row, and `candidates` is a per-item section
  (`internal/config/localkeys.go:18-27`): one key per candidate
  (`candidates/<name>`). Nothing extra is needed for them to sync, and the
  arriving item is validated by the same `candidate.Parse` every other reader
  uses (`internal/config/sharedoc.go:210-292` → `validateProspective` →
  `Validate` → `Parse`, `internal/config/config.go:375-381`). A window or a cap
  this build cannot read is refused as a value, so a peer cannot land a
  schedule that resolves differently here.
- **A bad value is refused at load**, naming the candidate and the field
  (§1.2): this is the "validate" half of the seed's item 9.
- **An unknown key is refused by `config set`** (#1318's repro), and *warned
  about* when it arrives from anywhere else. This is where the seed meets the
  code, and the two halves are split on purpose:
  - `cmd/relevo/config set candidates.<name>.<key>` (and `unset`) refuses a
    key path the candidate shape does not declare, with `config_invalid` and
    the known keys listed — `guardUnknownFields`
    (`cmd/relevo/config_unknown.go:13-52`), which today covers `agents.<n>` and
    `actors.<n>` only, gains the `candidates` case. It refuses only keys the
    write *adds*, so a body that already carries a stray key is still
    repairable, exactly as the existing two sections behave.
  - `candidate.Parse` returns a **warning** (not an error) for a key the
    candidate shape does not declare, mirroring
    `policyUnknownKeyWarnings` (`internal/policy/validate.go:122-176`) and its
    wording. Warnings reach `relevo doctor`'s one `config` row
    (`internal/doctor/doctor.go:134`) and the config warnings block, and the
    doctor row's claim about candidates then becomes true.
  - Why not refuse in `Parse`: a stored body that carries a key this build does
    not read must survive a rollback — that is a pinned property
    (`internal/config/names_test.go:49`, `TestEnsureCandidateNamesWritesOnce`
    asserts `"colour":"red"` survives a write), and the same rule the ledger's
    `Other` follows (`internal/availability/ledger.go:66-82`). Refusing in
    `Parse` would overturn it and would make an older relevo unable to load a
    peer's newer config at all. The consequence is stated: a key that arrives
    by sync or by `config import` is preserved and only warned about, while a
    key typed through `config set` is refused. Rollback safety wins over a
    silent typo when the writer is another machine that knows more keys.

## 1.9 Edge cases

| case | behaviour |
|---|---|
| window crossing midnight (`14:00Z`→`00:00Z`) | supported: `from` after `to` means the window wraps midnight; `from` inclusive, `to` exclusive |
| `from == to` | refused at load |
| a round started at `13:59Z` on qwen | it finishes on qwen. A window gate is not a rate limit, `gatedBuilder` (`internal/relevo/switch.go:29-36`) reads `LedgerGates` and matches `RateLimited` only, so the closing edge triggers no switch. Pinned by a test |
| a real rate limit mid-round, window live | unchanged: the round switches (the limit is real), and the switch resolves over the live gates, so it may land on a candidate that is inside its window |
| every candidate gated | the round refuses with `ErrAllGated`, naming each reason: `outside window 14:00Z-00:00Z` for a window, `at concurrency limit 2/2` for a cap, `rate-limited until ...` for a limit. For the builder role this reaches `relevo doctor`'s policy row (SevWarn, not Fail) and `relevo config`'s warnings |
| every candidate at its cap | same refusal, and the next step names waiting or raising the cap — not `gate --clear` |
| a capped candidate is the sole candidate | refused, like a window-blocked sole candidate: the cap is a physical limit, and this round has no other place to go. A served binding may be queued instead by the server's own `serve.max_builders`; queueing at the cap is out of scope |
| the binding's own round and its cap | never counted against itself; the mid-round switch passes `InFlightExcept` |
| a queued round | holds no slot (no process) and keeps its candidate across the wait |
| a halted round (NEEDS YOU) | holds no slot (PID cleared) |
| two owners on one server | one count, because the plan is the server's |
| clock skew between hosts | the window is evaluated on the host that *resolves* — the server for a served binding, the local daemon otherwise — so a skewed client cannot pick outside the server's window. NTP is assumed; a skew shorter than the window moves only the moment of the pick, never a round in flight |
| a named zone on two hosts | identical only when both tzdata agree; `Z` and offsets are the recommendation |
| `--candidate` / `--builder` | honoured as given, never re-picked; the binding's pin keeps it so across rounds |
| a binding whose candidate was edited away | unchanged: `repickStale`'s path runs first, and the window re-pick is a second reason on the same seam |
| a window whose window-blocked candidate is the only one in the order | the round refuses rather than run it (block mode), which is the owner's rule; `demote` mode runs it, last |

## 1.10 Tests that pin each rule

Each name is the test to write; the clause is what it must fail on when the
rule is broken (the mutation to try).

Config (§1.2), `internal/candidate/candidate_test.go`:

- `TestParseWindowsAndConcurrencyDecode` — the example row decodes with both
  windows and the cap.
- `TestParseRefusesBadWindow` — table: unparseable clock, unknown zone,
  `from == to`, unknown mode, both lists empty, `max_concurrency` 0.
- `TestWindowClockFormsAgree` — `"14:00Z"`, `"14:00"`, `"22:00+08:00"` resolve
  to the same instant.
- `TestParseWarnsOnUnknownCandidateKey` — a stray row key is a warning naming
  it, and the row still loads.
- `TestUnknownKeysNamesThePath` — a key inside `windows` is reported as
  `<name>.windows.<key>`.

Window evaluation and the gate (§1.3), `internal/availability/window_test.go`:

- `TestWindowCoversAcrossMidnight` — `13:59Z` outside, `14:00Z` inside,
  `23:59Z` inside, `00:00Z` outside.
- `TestWindowGatesOnlyOutsideTheWindow` — a `block` `only` gate exists outside
  and not inside; an `avoid` gate exists inside and not outside.
- `TestWindowGateUntilIsTheNextEdge` — `Until` is the next flip, not the config
  text and not zero.
- `TestWindowGateIsNeverInTheLedger` — after `Gates`, the ledger row holds no
  entry, and `relevo gate --clear <provider>` removes nothing while the window
  gate stays; and a real rate-limit entry survives a clear taken while a window
  gate is live.
- `TestGateTextNamesTheWindow` — `GateText` returns the window text, and
  `UntilText` is empty, for `Scheduled`.

Demote (§1.4), `internal/relevo/candidate_test.go`:

- `TestDemotedCandidateMovesLast` — order `[a,b,c]`, `b` demoted, the pick is
  `a`; with `a` gated the pick is `c`, not `b`.
- `TestDemoteNeverRefusesTheSoleCandidate` — a sole candidate with a `Demote`
  gate is picked.
- `TestDemoteRowShowsAtTheDemotedPosition` — `FormatPolicyFor` lists it last and
  marks the pick.

The refusal text (§1.4), `internal/relevo/candidate_test.go`:

- `TestAllGatedNamesTheWindow` / `TestAllGatedNamesTheCap` — the text names the
  reason and does not offer `gate --clear`; the pure-gate text is unchanged.

The pick and the re-pick (§1.5), `internal/relevo/send_test.go`,
`internal/relevo/repick_test.go`:

- `TestSendRepicksOffAClosedWindow` — a binding on qwen at `00:01Z` sends to
  deepseek, and the pick entry records it.
- `TestSendRepicksOntoAnOpenedWindow` — a binding on deepseek at `14:00Z` sends
  to qwen.
- `TestNoWindowConfiguredNeverMovesARateLimitedBinding` — the same binding with
  no window keeps its candidate.
- `TestPinnedBindingDoesNotRepick` — `--candidate` at bind, then a window edge:
  the binding keeps its candidate.
- `TestChainMemberAndRepairRepickForWindows` — the two other seams.
- `TestServedSendRepicksOnTheServer` — the server's `Send(Defer)` path.

The pin (§1.5.1), `internal/store/format_test.go` +
`internal/relevo/builder_change_test.go`:

- `TestBindingShapeMatchesFormat` — green with `BindingFormat` still 18.
- `TestApplyBuilderPinsAnExplicitPick` — an explicit resolution pins; an order
  resolution clears the pin.

The cap (§1.6), `internal/availability/concurrency_test.go`,
`internal/relevo/inflight_test.go`, `internal/serve/*_test.go`:

- `TestInFlightCountsOnlyLiveBuilders` — a live builder counts; a queued round
  and a NEEDS YOU round do not.
- `TestInFlightSpansThePool` — two candidates of one provider share one count.
- `TestInFlightExceptSkipsTheResolvingBinding` — the mid-round switch case.
- `TestConcurrencyGateAtTheCap` — under the cap no gate; at the cap one gate
  with note `2/2`.
- `TestConcurrencySkipDoesNotGateTheProvider` — no ledger entry, the sibling
  candidate of the same provider is still picked, `relevo gate` names the
  candidate and not the provider.
- `TestServerCountsInFlightAcrossOwners` — the server's walk covers two owners.

Display (§1.7), `internal/relevo/policy_view_test.go`,
`cmd/relevo/*_test.go`, `internal/view/*_test.go`:

- `TestConfigShowsWindowStateAndConcurrencyUse` — the row tails.
- `TestDryRunWindowNoteDoesNotClaimASwitch` — and `RateLimited` still does.
- `TestDoctorHidesSynthesisedGates` — no ledger row for a window or a cap.
- `TestRefusalFixNamesTheWindow` — not `relevo gate --clear`.

Sync and validation (§1.8), `internal/config/*_test.go`,
`cmd/relevo/config_unknown_test.go`:

- `TestCandidatesItemCarriesWindows` — the changed-body table gains a `windows`
  key and the peer applies it.
- `TestConfigSetRefusesUnknownCandidateKey` — `candidates.<n>.maxx` and
  `candidates.<n>.windows.onlyy` are refused, naming the path and the known
  keys, and the stored body is unchanged.
- `TestArrivingCandidateWithABadWindowIsRefused` — a peer's item with
  `"to": "25:00"` does not land.

`#1304` (§2, plan 8), `internal/availability/limit_test.go`:

- `TestParseClockResetAtNowIsLifted` — a reset equal to `now` is lifted now, not
  tomorrow; and one a minute past is lifted too.
- `TestParseClockResetWellPastIsTomorrow` — an hour and a half past still rolls
  to tomorrow (this keeps `clock 7pm rolls to tomorrow` honest).
- `TestGateWithAnImmediateResetKeepsTheLedgerValid` — the recorded entry's
  `Until` is never before its `At`, the next `LoadLedger` succeeds, and the
  gate does not appear as live.
- `TestReasonResetAtNowLiftsTheGate` — `relevo gate --reason "resets 9:50pm"`
  at `21:50` records a gate that is already over, and the ledger still loads.

CLI tests must not spawn a harness: every test above drives the library seams
(`resolveRole`, `Gates`, `Send` with a fake runner) or the CLI's own parse and
refusal paths.

## 1.11 Seams

| seam | file:line | what changes |
|---|---|---|
| candidate row shape | `internal/candidate/candidate.go:44-68`, `:353-369` | `Windows`, `MaxConcurrency`, their validation |
| candidate parse | `internal/candidate/candidate.go:261-295` | the unknown-key warning |
| unknown keys | new `internal/candidate/unknown_keys.go` | `UnknownKeys`, `KnownFields` |
| gate kinds | `internal/availability/ledger.go:23-44` | `Scheduled`, `ConcurrencyLimit` |
| gate type | `internal/availability/ledger.go:217-234` | `Demote bool` |
| gate text | `internal/availability/gates.go:458-511` | `GateKindText`, `GateText`, `Gate.UntilText` |
| window synthesis | new `internal/availability/window.go` | `windowGates`, the covering and next-edge rules |
| cap synthesis | new `internal/availability/concurrency.go` | `concurrencyGates`, the pool rule |
| deps | `internal/availability/deps.go:18-48` | `InFlight`, `InFlightExcept` |
| where gates are assembled | `internal/availability/gates_projection.go:66-93` | append both syntheses |
| the advisory note | `internal/availability/gates.go:516-535` | append the window gates |
| the walk | `internal/relevo/candidate.go:310-404` | the demote partition, `skipsFor`/`skipGates` |
| the ranked rows | `internal/relevo/candidate.go:133-167`, `policy_view.go:285,331,426` | one shared partition |
| the skip text | `internal/relevo/candidate.go:170-203` | `Skip.Note`, `GateText` |
| the refusal | `internal/relevo/candidate.go:434-442` | reason-aware text |
| the re-pick | `internal/relevo/stale.go:10-38` | `repickForRound` |
| the send seams | `internal/relevo/send.go:325-334`, `chain_send.go:85`, `repair.go:153` | call it |
| the dry-run note | `internal/relevo/send.go:449-457`, `:876` | kind-aware |
| the pin | `internal/store/binding.go:225`, `format.go:37`, golden | `BuilderPinned`, never stamped |
| the pin sites | `internal/relevo/builder_change.go:55-66`, `bind.go:496,736`, `add.go:440`, `serve/bindings.go:294` | set from the resolution |
| the count | new `internal/relevo/inflight.go`, `internal/relevo/runtime.go:148,395`, `internal/serve/serve.go:366`, `admin.go:441` | the walk and its wiring |
| the switch | `internal/relevo/switch.go:234` | `InFlightExcept` |
| config set guard | `cmd/relevo/config_unknown.go:13-52` | the candidates case |
| doctor rows | `cmd/relevo/doctor_checks.go:110-137`, `:248-272` | filter, and the fix text |
| wire views | `internal/serve/candidates.go:20-30`, `actors.go:64-70` | ignore `Demote` |
| the clock reset | `internal/availability/limit.go:237-262`, `gates.go:151-189` | the grace, and the `Until >= At` clamp |

## 1.12 Deliberately not done

Time-of-day prices, learned speed and account rotation (#485) stay out. Also
out: a provider- or account-level key; a per-account concurrency allowance; a
queue instead of a refusal at the cap; a live `1/2` shown everywhere rather than
only at the cap; and Alibaba's quota-exhausted limit pattern (#1313's other
related gap) — that one is a separate round and needs the provider's exact text.

---
