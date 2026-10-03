# Plan: floating comment UX on HTML boards (#950), stacked on the S2 comments slice

**Base: `relevo/board-s2-build` at `c763d8b8`** (the S2 comments slice). The shell
this round restyles is the one S2 shipped: four files under `internal/board/shell/`
plus the allowlist and route in `html_server.go`.

The slice's own decisions (D1-D8, in
`docs/plans/2026-10-03-board-s2-comments.md`) stay in force. This round changes
the *shape* of the comment UI and nothing about the trust model, the anchor
model, or the update protocol.

## Where the seed's numbers were wrong

The seed said `comments.js` is already 348 lines. The base measures **352**:
`index.html` 108, `overlay.js` 293, `shell.js` 147, `comments.js` 352. Every other
seam in the plan's table matched the base exactly, so the guard passed on
substance and the 348 was simply a stale figure.

The seed also said "shell assets ONLY, no Go change" while ordering a split of
`comments.js` into a new file. Those cannot both hold: a new shipped script has
to be named in the `shellScripts` allowlist and routed in `Handler`, or it 404s
on every load. This round therefore touches Go — wiring only, one map entry, one
route, four test enumerations — and no API, model, or protocol surface. The plan
anticipates exactly this in case 9 and step 7.

## Decisions

- **D9 The canvas is the whole page.** Comment mode floats over the board; it
  never takes room from it. The old `#comments.open ~ #frame { width:
  calc(100% - 20rem) }` rule is deleted, so opening the panel cannot move the
  board out from under the pointer. `#frame` is full width in every state.
- **D10 Two floating surfaces, never a dock.** `#comments` is the All-comments
  panel; `#thread-card` is a single thread shown beside its pin. One card at a
  time: a board can carry dozens of notes, and two cards at once would not say
  which one the reader was meant to be reading. Escape unwinds one layer at a
  time, card first. Focus returns to the toggle, so a reader who closes the
  panel is never left focused on nothing.
- **D11 The list is a view, not a writer.** `comments.js` splits into
  `commentlist.js` (drawing) and itself (draft, post, mode, poll). The new file
  holds no token and posts nothing that writes; the only message out of it is a
  focus request. `comments.js` keeps the `ev.source === frame.contentWindow`
  guard, which is why it loads the renderer first and reads `window.__relevoList`.
- **D12 The hover accent is theme-aware.** The fixed blue outline read as a bug
  on a board that is itself dark, so the accent is the system `Highlight` colour.
  The negative `outline-offset` is kept, because it is what keeps the outline out
  of layout, and the pointer cursor rides the same rule.
- **D13 A card opened from the list asks for its coordinates.** Only the frame can
  see its own pins, across the sandbox boundary, so the shell sends
  `relevo.points` and the frame answers with where each pin sits. A pin clicked
  on the board carries its own point, so that path needs no round trip.

## Kept byte-for-byte

The anchor model (`data-board-id` > unique `#id` > `tag:nth-of-type` path, `x`/`y`
fractions clamped to [0,1]); the orphan rule (listed, marked, never dropped);
polling every 2s with `etag`+`aetag` and a 204 when nothing moved; the stale
banner with Apply; 409 keeping the draft; the `ev.source === frame.contentWindow`
guard; the token never entering the frame; the CSP; `ExternalRefs` empty over
every shell file; and `GET /api/board` returning the file's bytes exactly with the
overlay spliced only in memory.

## Sizes

Every shell JS file ends at or under 300 lines: `comments.js` 300,
`commentlist.js` 231, `overlay.js` 300, `shell.js` 147. Reaching that took
condensing prose that restated the code and dropping the section-divider comment
lines; the reasoning in each file survives.

## Pins added

- `TestShellShipsEveryScriptItLoads` reads the `src` tags out of `index.html` and
  checks each against the allowlist, so a file added to the page and forgotten in
  the server is caught from the page's side rather than only the server's.
- `TestShellCommentUIStateText` pins the five states a reader can be stranded in:
  empty list, annotations error, dirty banner, no board, failed read.
- `TestShellFloatsTheCommentPanes` pins D9 and D10: no rule narrows `#frame`, both
  panes float, both have a way out.
- `TestShellHoverOutlineIsThemeAware` pins D12 including the single-outlined
  element rule.

Each was verified to fail when its behaviour is broken and pass when restored.

## Known limit carried from S2

A CLI `board comment` append and a page post landing in the same instant can still
lose one of the two. The server mutex covers posts inside the process and a
compare-and-swap guards the file, but there is no lock spanning the two. This
round does not change it.