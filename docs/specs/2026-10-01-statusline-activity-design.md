# Status line: the row says what the runner is doing, and the round's live diff

**Amends:** docs/specs/2026-09-24-statusline-redesign-design.md, §2 (the status
column vocabulary) and the middle's token cell; it adds a word and a segment, it
removes nothing.
**Scope:** `relevo status --line` (Claude Code), the `relevo ui` cockpit row and
the OpenCode sidebar row. `relevo status` (human) is unchanged.
**Status:** approved 2026-10-01; plan at
`docs/plans/2026-10-01-statusline-activity.md`.

## 1. The one rule

`view.ActivityWord(b BindingStatus) string` is the single vocabulary for what a
runner is doing, beside `phase()`:

- `working` → `working`, or `quiet X` when `QuietFor` is set: the progress
  sampler has gone silent, so "working" would be a claim the row cannot support.
- The definite words pass through verbatim: `stalled X`, `exploring X`,
  `gating X`, `exited N`, the bare `exited`, `running`, `queued (...)` and the
  bare `queued`.
- Every other word returns `""`: `idle`, `unknown`, `""`, and words a row must
  not claim as its own activity (`unreachable`, a credential word, `cert`,
  `closed`, `gone`). The caller keeps its phase.

## 2. Where it shows

In `rowStatus`, while a round is in flight (`RoundStart` non-zero **and**
`RoundEnd` zero) and the word is non-empty, the word takes the phase slot; the
tone stays `phase` (dim). The precedence above it is untouched: `NEEDS YOU`
(including a stalled pending report), `REPORT IN`, `QUESTION IN`, `PAUSED`,
`HELD`, `DONE` and the `Display` words still win. A closed round, a row with no
round, or an empty word still reads its phase (`prompt sent`, `report in`,
`answered`, `no prompt yet`).

## 3. The live diff

`StatusLineRow` gains `Live *LiveDiff` (`json:"live,omitempty"`), copied from
`BindingStatus.Live`. When non-nil the middle appends `· +A/-R in F`
(` (shared)` when `LiveDiff.Shared`) before the token cell. No round open, no
baseline recorded, or a failed git read leaves `Live` nil, and the row is
byte-identical to the row before this change.

The JSON is additive only: `rows[].live` appears only when carried, and every
existing key is unchanged. The `cmd/relevo` contract goldens stay byte-identical
(their fixture carries no live diff); the live cases are pinned in
`internal/view`.

## 4. Surfaces

- **`internal/view`**: `ActivityWord`, the `rowStatus` gate and the middle's
  live segment.
- **`internal/ui`**: `rowNow` takes its working word from `view.ActivityWord`,
  falling back to `b.BuilderStatus` when the rule is empty, so an `unknown` row
  still says `unknown`; the private copy of the rule is gone.
- **OpenCode sidebar**: line B appends the same diff segment before the tokens,
  mirroring the statusline; line A already takes its word from the document.
- **`relevo status` (human)**: byte-identical. Its runner line already prints
  `b.BuilderStatus`, which is the same word for every definite word; a quiet
  working row keeps `quiet X` on the round line, where the round's progress
  belongs, so the word is not printed twice.

## 5. What stayed

The clock (whole minutes, no seconds), the dot, the precedence, the column
layout, `RELEVO_STATUSLINE_MARGIN`, the contract goldens and every `internal/ui`
golden. No `BindingStatus` field is removed and no surface loses a fact.
