# Board comment UX, round 2 (#958)

Shell-only round stacked on `origin/relevo/board-ux-build` (base tip `fff1a0c4`,
"fix(board): the toggle no longer dismisses the panel it opens"). No Go API, CLI,
model or protocol change; no new shell file, because a fifth script would need a
`shellScripts` allowlist entry and a route change, which is Go.

## Behaviour

1. **Compact floating panel.** `#comments` sizes to its content with a
   `max-height` cap and an internal scroll. Few comments give a short panel; many
   give a capped panel whose list scrolls. `#frame` never narrows.
2. **Pins only in comment mode.** The board carries no pins until the mode is
   turned on; turning it off takes the layer back off the document. Turning it on
   draws every annotation the shell holds, including notes added from the command
   line, which arrive through the same document.
3. **Thread card.** Entries are grouped by their anchor for presentation only:
   the same selector string is one thread, an empty selector is the board thread.
   One pin, one list row and one card per thread; the card shows the thread's
   entries in file order and carries a reply box that posts onto that thread's own
   anchor. `annotations.json` stays flat and each reply is an ordinary entry
   through the existing `POST api/annotations` shape.
4. **Dot badges.** A pin is a solid accent dot, not a numbered badge, and keeps an
   accessible name carrying its thread's label. The board-level and orphan colour
   variants are kept.

## Seam table (line numbers at the bound base, and after the round)

| File | Base | Final | Seam (base) | Seam (final) |
| --- | --- | --- | --- | --- |
| `shell/index.html` | 166 | 169 | `#frame` L36; `#comments` L39–56; `#thread-card` L79–95; panel markup L152–162 | `#frame` L36 (untouched); `#comments` L42–58; `#thread-card` L82–97; panel markup L155–165 |
| `shell/comments.js` | 303 | 300 | `setMode` L40–56; `postToFrame` L58–63; `onFrameMessage` L73–109; `openDraft` L111–120; `post` L127–170; `adopt` L216–233; `boot` L290–303; document click/Escape L261–288 | `setMode` L37–54; `postToFrame` L56–59; `postPins` (new) L63–67; `requestPoints` L69–72; `onFrameMessage` L77–112; `openDraft` L111–118; `post(anchor, text)` L125–171; `adopt` L213–229; `boot` L287–295; document click/Escape L259–285; `list.setReplyHandler(post)` L257 |
| `shell/commentlist.js` | 231 | 300 | `render` L63–87; `addRow` L103–116; `openCard` L161–179; `placeCard` L203–221; `setPoints` L43–50; `entryById` L51–58 | `threads` (new, replaces `entryById`) L48–63; `threadById` L66–72; `orphan` L75–81; `render` L85–107; `addRow(thread)` L125–139; `countLine` L141–146; `openCard` L188–211; `replyBox` L214–230; `anchorOf` L232–235; `setReplyHandler` L238–240; `clearReply` L243–246; `placeCard` L270–292; `setPoints` L36–44 |
| `shell/overlay.js` | 300 | 300 | `installStyles` L32–48; `setPins` L157–174; `drawPin` L186–211; `placePin` L213–223; `points` L236–250; `setMode` L284–294 | `installStyles` L23–36 (dot style); `setPins` (groups by anchor) L135–152; `draw` (mode gate) L155–176; `drawPin` L188–211; `placePin` L213–223; `reposition` L225–233; `points` L236–249; `setMode` L283–294 |
| `shell/shell.js` | 147 | 147 | untouched | untouched |

Every JS file ends the round at 300 lines or fewer: `comments.js` 303 → 300,
`commentlist.js` 231 → 300, `overlay.js` 300 → 300, `shell.js` 147. The line
budget was met by condensing restating prose, never the reasoning in a comment.

## What the seed said about the code, and what the code said

- The seed budgets JS at ~300 lines each; the base measured `comments.js` 303,
  already three over. Every file is at or under 300 after the round.
- Base `comments.js` referenced `selEl` (`setMode`, `openDraft`, post success)
  with no declaration, which is a `ReferenceError` on toggle-off, on a pick and on
  post-adopt. It is now declared from `#comment-selector` with the other
  element lookups, and pinned by `TestShellGroupsCommentsIntoThreads`.

## Pins

Existing pins kept: `TestShellPostAdoptsTheEntry`, `TestOverlayHasNoScriptCloseTag`,
`TestShellAcceptsOnlyFrameMessages`, `TestShellOverlayRunsUnderTheExistingCSP`,
`TestShellCommentModeInjectsOverlayIntoTheFrame`, the allowlist cross-pins,
`TestShellCommentUIStateText`, `TestShellFloatsTheCommentPanes`,
`TestShellHoverOutlineIsThemeAware`.

Added in `internal/board/shell_test.go`, each shown failing against its broken
behaviour and passing restored:

- `TestShellCommentPanelIsContentSized` -- the panel is capped and scrolls, and
  carries no bottom edge. Breaks when `bottom: 12px` returns.
- `TestShellDrawsPinsOnlyInCommentMode` -- the overlay's draw refuses while mode
  is off and the layer leaves the document; the shell sends annotations from one
  gated place, clears them on mode off, and re-sends on mode on, adopt and the
  frame's re-announce. Breaks when either the gate or the layer removal goes.
- `TestShellGroupsCommentsIntoThreads` -- grouping by selector string, one row per
  thread, the card listing the thread's entries, the reply box posting through the
  handler onto the anchor it was handed, the list writing nothing, and `selEl`
  declared. Breaks on any of those.
- `TestShellPinsAreDotsWithAccessibleNames` -- no ordinal on a pin, a dot rather
  than a badge box, `aria-label` and `title` naming the thread, the colour
  variants kept, one pin per anchor. Breaks when the numbering or the per-entry
  grouping returns.

Two helpers carry the pins: `shellRule` reads one CSS rule out of `index.html`
and `shellFunc` reads one top-level function out of a shell script, so a pin can
name one rule or one function instead of matching around it.

## Deleted behaviour

1. Numbered pin badges and the matching per-entry list numbering.
2. One row and one card per entry, replaced by one per anchor thread.
3. The full-height panel stretch, replaced by content size with a cap and scroll.
4. Pins drawn outside comment mode, replaced by mode-gated draw and clear.

## Invariants kept

Anchor model (`data-board-id`, then unique `#id`, then a live `tag:nth-of-type`
path; `x`/`y` fractions clamped), the orphan rule, the two-second `etag`+`aetag`
poll with its 204, the stale banner and Apply, the `ev.source ===
frame.contentWindow` refusal guard in `comments.js`, the token never entering the
frame, the CSP in `htmlSecurityHeaders`, `ExternalRefs` empty over all shell
files, `GET /api/board` returning file bytes with an in-memory overlay splice
only, and the five UI-state text pins.