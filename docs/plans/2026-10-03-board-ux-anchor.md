# Board comment anchoring and human labels (#993)

Shell-only round stacked on `relevo/board-ux4-build` (base tip `3884f1bd`, "test(board):
pins follow the keyboard-only post paths"). No Go API, CLI, model or protocol change; no
new shell file, because a fifth script would need a `shellScripts` allowlist entry and a
route change, which is Go. The only Go change is the pins.

## Behaviour

1. **The composer opens beside the pick.** The pick carries the click's own point in the
   frame's viewport (`vx`, `vy`), clamped to it at both ends, beside the fractions the dot
   is placed from. The two are different questions: the fractions have to survive a
   resize, the viewport point names where the reader is looking now. The dialog is
   placed by `placeBeside`, the thread card's own placement, so the two floating surfaces
   cannot disagree about which edge of the window has room; a point with no coordinates
   places nothing and leaves the box on its own rule rather than writing `NaN` into a
   style.
2. **A human name for the anchor.** The name is derived in the frame, which is the only
   place the DOM is: the `data-board-id` value when the anchor has one, otherwise the
   element's tag with a few words of its own text, whitespace collapsed and cut at a word.
   The pick carries it, so the composer's readout says what was clicked. A `relevo.labels`
   request derives the names of every drawn thread, so a thread stored before this existed
   is named live from the element it still resolves in. The thread card heads itself with
   the name, rewritten in place when a label arrives rather than redrawn, so a label
   turning up cannot empty a reply already being typed.
3. **The selector stays put.** `annotations.json` carries the raw selector for the agent
   and the CLI, and the POST body is the shape the API already took: `selector`, `x`,
   `y`, `text`. Nothing is stored with a label. A card with no name yet says `element`,
   and a board-level thread says `the board`; neither falls back to the selector.

## Seam table (line numbers after the round)

| File | Base | Final | Seam (final) |
| --- | --- | --- | --- |
| `shell/index.html` | 227 | 237 | `#thread-card` L85; `#comment-dialog` L116; `#comment-selector, .relevo-anchor` L145 (new, replaces the readout's monospace rule); `.relevo-anchor` L150 (new) |
| `shell/comments.js` | 310 | 325 | `setMode` L37; `postPins` L60; `requestPoints` L66; `requestLabels` L73 (new); `onFrameMessage` L80; `openDraft(msg)` L111; `closeDialog` L127; `post` L141; `adopt` L220; `boot` L311 |
| `shell/commentlist.js` | 306 | 344 | `setPoints` L36; `setLabels` L47 (new); `labelFor` L62 (new); `openCard` L190; `closeCard` L289; `placeCard` L302; `placeBeside` L310 (new, the card's placement generalised); `window.__relevoList` L329 |
| `shell/overlay.js` | 338 | 394 | `selectorFor` L63; `labelFor` L124 (new); `snippet` L138 (new); `pick` L150; `clamp` L160; `inView` L168 (new); `setPins` L176; `draw` L198; `drawPin` L232; `points` L324; `labels` L338 (new); `outline` L352; `setMode` L378 |
| `shell/shell.js` | 147 | 147 | untouched |

## What the seed said about the code, and what the code said

- **The overlay did not parse.** `overlay.js` at the base tip carried
  `document.addEventListener("mouseover", funner("mouseleave", function () { outline(null); hidePreview(); }, true);`
  -- one unclosed call and an undefined `funner`. The whole script failed to parse, so
  every crossing was dead: no `relevo.overlayReady`, no mode message, no dots, no hover
  outline, and none of this round's work would have run. It is restored as the two
  handlers it was written as (the `mouseover` that outlines the target, the
  `mouseleave` that clears it and hides the preview), which is what the previous tip
  carried. This repair is outside the two changes the seed asked for and is called out
  in the report.
- **The ~300-line budget was already over at the base tip**: `comments.js` 310,
  `commentlist.js` 306, `overlay.js` 338. The round adds what the seed asks for on top,
  so the files end at 325, 344 and 394. No file was cut back by deleting reasoning: the
  additions are the new seams and their comments, and nothing else was touched. Reaching
  300 would mean rewriting comments that are unrelated to this round.
- The pick message already had the two shapes the round needs to extend -- an anchor and
  a place -- so the pick grew two fields and a label rather than becoming an object.

## Pins

Existing pin kept, one line of it superseded:
`TestShellClickOpensTheInputDialog` pinned
`openDraft(msg.selector || "", msg.x, msg.y)`, which the round changes to carry the whole
pick; its pin now names `openDraft(msg)`.

Added in `internal/board/shell_test.go`, each shown failing against its broken behaviour
and passing restored:

- `TestShellAnchorsTheComposerBesideThePick` -- the pick carrying both clamped viewport
  coordinates, the dialog placed through `placeBeside`, the card's own placement routing
  through the same function, the flip and both viewport clamps, and the guard that places nothing without a point. Four
  mutations break it: dropping the placement call, dropping `vx`/`vy` from the pick,
  unclamping `inView`, and dropping the bottom clamp.
- `TestShellNamesTheAnchorInsteadOfTheSelector` -- the `data-board-id` branch ahead of
  the tag-and-snippet fallback, the snippet collapsed and cut at a word, the label in the
  pick, and the labels request answered from each anchor's resolved element. Four
  mutations break it: the `data-board-id` branch, the labels handler, the labels report,
  and the snippet's word cut.
- `TestShellShowsTheAnchorNameNotTheSelector` -- the composer showing the pick's label
  and never the selector, the card headed with the name, `element` and `the board` rather
  than the selector as fallbacks, the labels request made and handed to the list, the
  open card rewritten in place rather than redrawn, the readout not in a monospace, and
  the POST body carrying no label. Seven mutations break it: the readout showing the
  selector, the open card redrawn, the selector fallback, the missing labels request,
  the monospace, and a label added to the POST body.

The two halves are separate tests because `gocognit` refuses one function holding both
halves at a complexity of 31.

Verification was text pins over the shell bytes, `node --check` on all four scripts, and
the focused Go suite. No browser was driven.

## Deleted behaviour

1. The composer a fresh pick opens, fixed at the top left of the window.
2. The raw CSS selector in the composer's readout, and the monospace it was set in.
3. The unbalanced `mouseover` line in `overlay.js` that stopped the script parsing.

## Invariants kept

One open surface, reply focus, no-resize plus wrapping, hover preview, keyboard-only post
with its hint, mode-only dots, the compact panel, thread grouping as presentation only,
the anchor model (`data-board-id`, then unique `#id`, then a live `tag:nth-of-type` path;
`x`/`y` fractions clamped) and the orphan rule, the two-second `etag`+`aetag` poll with
its 204, the dirty banner and Apply, 409 keeping the draft, the `ev.source ===
frame.contentWindow` guard, the token never entering the frame, the CSP in
`htmlSecurityHeaders`, `ExternalRefs` empty over all shell files, and
`GET /api/board` returning the file's bytes with the overlay spliced in memory only.