# Board comment dialog UX (#970)

Shell-only round stacked on `origin/relevo/board-ux2-local` (base tip `4c0b0a4a`,
"docs: save the board comment UX round 2 plan"). No Go API, CLI, model or
protocol change; no new shell file, because a fifth script would need a
`shellScripts` allowlist entry and a route change, which is Go.

## Behaviour

1. **Ctrl+Enter posts.** Both boxes post from the keyboard through the same call
   their button makes: `submitDraft` for the dialog's draft, `sendReply` for the
   card's reply. `Enter` alone stays a newline. The guard lives in one place,
   `submitOnCtrlEnter` in commentlist.js, exported so the shell can use it too.
2. **The all-comments panel is read-only.** The composer moved out of it into
   the input dialog, so the panel holds only its heading, its close control and
   the list. A row navigates: it opens that thread's card, which focuses the row
   it came from.
3. **Bigger dots.** A dot is drawn at `1.3em` with a margin of `-0.65em`, half
   its width, so it is easier to hit and still sits on the point it names. Same
   placement, same board-level and orphan colour variants, same accessible names.
4. **A click opens the right thing.** A fresh pick on the board opens the input
   dialog, carrying the selector it is anchored to, the box, Post and a cancel
   control. A click on a dot opens that thread's card, with the thread's entries
   and the box that answers them. Escape unwinds card, then composer, then panel.
5. **The card is a column.** The thread's entries stacked, the reply box under
   them, and Reply and the close in a row beneath that. The close was floated
   beside the box, which is what let the textarea cover the controls under it.
6. **A reply keeps the card.** A successful post appends the entry to the
   document and the card is drawn again from it, so the conversation on screen
   grows, the box comes back empty and the card stays where the reader left it.
   A post never tears the card down. A first post still puts the composer away,
   because it consumed the pick.

## Seam table (line numbers at the bound base, and after the round)

| File | Base | Final | Seam (base) | Seam (final) |
| --- | --- | --- | --- | --- |
| `shell/index.html` | 169 | 215 | close-control rule L69–80; `#thread-card` L82–97; `#thread-close { float: right; }` L98; `#comment-draft` L117–124; panel markup L155–164 | close-control rule L69–81 (gains the dialog's cancel and the card's Reply); `#thread-card` L83–98; `.relevo-card-actions` L103–108 (new); `#comment-dialog` L113–136 (new); `#comment-draft, #thread-reply` L155–161; panel markup L193–199 (composer gone), dialog markup L200–210, card L211 |
| `shell/comments.js` | 300 | 300 | `setMode` L37–54; `onFrameMessage` L77–109; `openDraft` L111–118; `post` L125–171; `adopt` L213–229; document click/Escape L259–285; `boot` L287–295 | `setMode` L37–52; `onFrameMessage` L73–92; `openDraft` L98–108; `closeDialog` (new) L110–117; `post` L124–164; `adopt` L203–219; `submitDraft` (new) L241–243; listeners L245–254; Escape L256–269; document click L271–285; `boot` L287–296 |
| `shell/commentlist.js` | 300 | 298 | `render` L85–108; `note`/`heading`/`countLine`/`textLine` L110–168; `addRow` L125–138; `openCard` L188–211; `replyBox` L214–229; `clearReply` L243–246; `placeCard` L270–292 | `render` L80–105; `line` (replaces `note`, `heading`, `countLine`, `textLine`) L107–113; `byLine` L115–117; `orphanMark` L119–120; `addRow` L125–143; `openCard` L161–181; `replyBox` (box plus the controls row) L188–203; `sendReply` (new) L208–211; `submitOnCtrlEnter` (new) L215–222; `clearReply` L238–243; `placeCard` L267–285 |
| `shell/overlay.js` | 300 | 300 | `installStyles` L23–34; `drawPin` L188–211; `placePin` L213–223 | `installStyles` L25–36 (dot at 1.3em, margin -0.65em); `drawPin` L189–212; `placePin` L214–224 |
| `shell/shell.js` | 147 | 147 | untouched | untouched |

Every JS file ends the round at 300 lines or fewer: `comments.js` 300 → 300,
`commentlist.js` 300 → 298, `overlay.js` 300 → 300, `shell.js` 147. The budget
was met by condensing restating prose, never the reasoning in a comment, and by
folding the five one-purpose text builders in commentlist.js into the one
`line(tag, className, text)` they all were.

## What the seed said about the code, and what the code said

- The seed moves the composer but keeps a draft textarea; those only reconcile
  as one textarea in the dialog. `#comment-draft`, `#comment-post` and
  `#comment-selector` keep their ids, so comments.js's lookups and the
  `selEl` pin stay true.
- `setMode` focused the draft box when the mode came on. With the composer in a
  dialog that is a focus call on a hidden element, so it is gone: mode-on has
  nothing to focus until a pick opens the dialog.
- The click-outside handler would have dismissed the panel on the first click
  inside the dialog -- the dialog is not inside `#comments` -- so it excludes it
  the way it already excluded the toggle.
- `openCard` toggles closed on the handle it is already showing, so
  `clearReply` drops `cardId` before re-opening the same thread: the redraw is
  an open, not a close.
- The overlap the round removed was structural, not cosmetic: `#thread-close`
  was floated, and a floated control sits beside a block box's inline content,
  which is where the textarea went.

## Pins

Existing pins kept and untouched: `TestShellPostAdoptsTheEntry`,
`TestOverlayHasNoScriptCloseTag`, `TestShellAcceptsOnlyFrameMessages`,
`TestShellOverlayRunsUnderTheExistingCSP`, `TestShellCommentModeInjectsOverlayIntoTheFrame`,
the allowlist cross-pins, `TestShellFloatsTheCommentPanes`,
`TestShellHoverOutlineIsThemeAware`, `TestShellCommentPanelIsContentSized`,
`TestShellDrawsPinsOnlyInCommentMode`, `TestShellGroupsCommentsIntoThreads`.

Extended in `internal/board/shell_test.go`:

- `TestShellCommentUIStateText` -- the card's box placeholder, the dialog's
  title, its box's placeholder and the cancel control's accessible name.
- `TestShellPinsAreDotsWithAccessibleNames` -- the dot's new size and margin,
  the old `0.9em` banned, `placePin` still placing on the point the thread
  named, and both colour variants still distinct.

Added in `internal/board/shell_test.go`, each shown failing against its broken
behaviour and passing restored:

- `TestShellCommentPanelIsReadOnly` -- the aside carries the title, the close
  control and the list, and none of the composer; a row's click opens the
  thread's card and the card focuses the row. Breaks when the composer returns
  to the panel or the row stops navigating.
- `TestShellClickOpensTheInputDialog` -- the dialog's markup, its floating rule,
  its open state, the pick opening it, the dot opening the card instead,
  Escape closing the composer and a click inside it not counting as outside.
  Breaks on any of those.
- `TestShellCtrlEnterPostsFromBothTextareas` -- the key guard and its
  `preventDefault`, the export, both boxes wired, and two callers of
  `sendReply` so the key and the button cannot differ. Breaks when either box
  stops taking the key.
- `TestShellThreadCardStacksItsEntriesBoxAndActions` -- no float anywhere in the
  page, the reply box sized with the draft box as a full-width block, the
  actions a spread flex row, and the entries stacked above the box. Breaks when
  the float returns or the order inverts.
- `TestShellReplyKeepsTheCardOpen` -- `clearReply` re-opens the same thread
  without closing one, and the post path itself closes no card. Breaks when a
  reply tears the card down.

Two helpers carry the pins: `shellMarkup` reads one element's markup out of
`index.html`, and `shellInjectedRule` reads one rule of the styles overlay.js
injects, whose selectors run straight into their brace.

## Deleted behaviour

1. The all-comments panel's composer, with its selector readout, its box and
   its Post button.
2. The floated close control on the thread card.
3. `clearReply`'s direct clearing of the card's box by id.

## Invariants kept

Anchor model (`data-board-id`, then unique `#id`, then a live `tag:nth-of-type`
path; `x`/`y` fractions clamped), the orphan rule, the two-second `etag`+`aetag`
poll with its 204, the stale banner and Apply, 409 keeping the draft, the
`ev.source === frame.contentWindow` refusal guard in `comments.js`, the token
never entering the frame, the CSP in `htmlSecurityHeaders`, `ExternalRefs` empty
over all shell files, `GET /api/board` returning file bytes with an in-memory
overlay splice only, mode-only dots, the compact content-sized panel, and the
dot's accessible names.
