# Plan: Boards v3 slice 2 (#917, part of #915). Comments on HTML boards: a comment mode in the shell, `annotations.json`, and the comment verbs

The builder saves this plan as `docs/plans/2026-10-03-board-s2-comments.md` in step 10, in the same PR as the code.

**Base: `origin/main` at `c138d38e`** (PR #925 merged). Every slice 1 seam this plan names exists there:
- `internal/board/html.go` (188 lines)
- `html_server.go` (128)
- `shell.go`
- `shell/index.html` and `shell/shell.js` (82)
- `cmd/relevo/board_html.go` (130)
- `board_promote.go` (92)

`cmd/relevo/board.go` is **590/600 lines**, so this slice does not edit it. Nothing in it needs to change: `cmdBoard` already sends `comment`, `comments` and `promote` to their own files (`board.go:220-233`).

Bind with `--base origin/main`. The step 0 guard halts if any seam above is missing.

## Decisions in force

These are the eight decisions from round 1, confirmed, plus the two rulings in this round's seed. D7 also adds one new sub-decision for you to confirm.

- **D1 Hover runs inside the board.** The board renders in `<iframe sandbox="allow-scripts">`, an opaque origin the shell cannot reach into.
  - The shell adds an overlay script (`shell/overlay.js`) to the board's HTML text **in memory, in the shell**, just before it builds the blob. Nothing is injected on disk or on the server.
  - The overlay does three things: the hover outline, the click pick, and drawing the pins.
  - It talks to the shell only by `postMessage`.
- **D2 The comment text is typed in the shell.**
  - A board's own script shares the iframe with the overlay, so it can fake a "pick" message.
  - It cannot write a comment: only the shell's draft box posts, and only the shell holds the token.
  - The shell accepts a message only when `ev.source === frame.contentWindow`, and never sends the token into the frame.
- **D3 Anchors.** The overlay builds the selector in this order:
  1. `[data-board-id="…"]` on the nearest element that carries the attribute;
  2. else `#id` when that id is unique in the document;
  3. else a CSS path (`tag:nth-of-type(n)` steps from the nearest ancestor that has an id or `data-board-id`, or from `body`).

  `x` and `y` are fractions in [0,1] of the anchored element's bounding box, so a pin survives a resize.

  A pin whose selector matches nothing, or throws, is an **orphan**. The shell lists orphans; it never drops them.
- **D4 `annotations.json`** sits beside `board.html`.
  - It is a JSON array with one entry per line, and it only ever grows: an append splices the new entry in before the closing `]`.
  - Neither the CLI nor the server rewrites an existing entry or touches `board.html`.
  - A symlinked `annotations.json` is refused.
  - Reads are capped at `maxBody`.
- **D5 Live updates.** The shell polls every 2 s and pauses while the tab is hidden.
  - A clean page applies the change: a new board etag re-renders the frame, and a new annotations etag redraws the pins.
  - When the page is dirty (a draft is open), it shows a banner and keeps the draft. The banner reads "the board changed on disk -- your draft is kept; apply to take the update", with an **Apply** button.
  - A stale post gets a 409 and the same banner.
  - Nothing is ever merged.
- **D6 `board comment` on a missing HTML board refuses** with `refused`, through `ErrNotFound`. `board comments` on a missing board stays `[]` with exit 0 and creates nothing, as today.
- **D7 (changed this round) `promote` copies `annotations.json` when the source has one.** The `--force` rules match `board.html`'s:
  - an existing target `annotations.json` without `--force` is refused;
  - both targets are checked before either file is written;
  - **new decision:** with `--force` and no source `annotations.json`, a stale target `annotations.json` is removed, so a promoted board never inherits another board's pins. It is item 5 of the deleted list. Overrule it before sending if unwanted.
- **D8 A bare `board comment`/`comments`** (no path, with `--board`, or through the pointer) targets the HTML board.
  - An explicit `…/board.html` path does too.
  - An explicit `.excalidraw` path keeps the old flow byte for byte.
  - The old comment tests that relied on a bare call switch to explicit `.excalidraw` paths.

**Concurrency.** `windows/amd64` is a CI target (`.github/workflows/ci.yml:351`), so `syscall.Flock` is out.
- **Within the server process:** a mutex, plus a compare-and-swap. The append re-reads the file and compares its etag with the expected one right before `writeAtomic`.
- **A CLI append** reads the current etag and appends against it.
- **What remains:** a CLI append and a page post landing in the same instant can still lose one of the two. The report states this as a known limit; the slice adds no lock file.

## Behaviour and cases

1. **Entry shape**, in this field order:
   - `id`: `a` + 12 lowercase hex characters from `crypto/rand`, unique within the file;
   - `selector`: a string, empty for a board-level note;
   - `x`, `y`: float64;
   - `text`, `by`;
   - `at`: RFC3339.

   **Validation:**
   - `text`: non-empty after trimming, at most 8192 bytes.
   - `by`: the existing `validateBy` (`internal/board/comment.go:140`).
   - `selector`: at most 1024 bytes, with no control characters.
   - `x` and `y`: finite and within [0,1].
   - An empty selector forces `x = y = 0`.

   **Parse:**
   - Unknown fields are tolerated.
   - A file that is not a JSON array is `ErrInvalid` naming the path.
   - An entry missing `id` or `by`, or with an `at` that is not RFC3339, is `ErrInvalid` naming its index and id.
2. **`relevo board comments [path|--board NAME] [--json]`** on an HTML board:
   - It lists the entries in file order.
   - `--json` prints one compact array of `board.Annotation`, and `[]` (never `null`) when there are none.
   - Text mode prints one row per entry: `id\tselector\tx\ty\ttext\tby\tat`.
   - It is read-only and never writes the pointer.
   - Do not add a `--mastermind` flag.
3. **`relevo board comment [path|--board NAME] --text S [--selector SEL [--x X --y Y]] [--by B]`** on an HTML board:
   - It appends one entry and prints `comment: <id>  <annotations path>`.
   - `--x`/`--y` go together and need `--selector`. `--selector` alone defaults to `0,0`.
   - `--selector` with an explicit `.excalidraw` path is `usage`.
   - It writes the pointer for a live board not read from the pointer, as today (`board_comment.go:127-131`).
   - A missing `board.html` refuses.
   - `board.html` stays byte-identical.
4. **Server** (`HTMLServer`):
   - **`GET /api/board`**:
     - adds `annotations` (`[]`, never null) and `annotationsEtag` (empty when the file is missing);
     - a malformed or symlinked `annotations.json` does not break the board: the board is still served, with `annotationsError` set and `annotations: []`;
     - `html` is exactly the file's bytes; nothing is injected on the server.
   - **`GET /api/board?etag=E&aetag=A`** answers **204** when both etags equal the current ones.
   - **`POST /api/annotations`**:
     - token required;
     - JSON body `{selector,x,y,text}` of at most 64 KiB, else 413;
     - `If-Match` must equal the current annotations etag (missing equals empty, which means no file yet), else 409;
     - `by` is always `human`, set by the server and never read from the body;
     - a missing board is 404, invalid input or a malformed file is 422;
     - a write is 201 with `{annotation, annotationsEtag}`.
   - Every other method on `/api/annotations` is 405 with `Allow: POST`.
   - The Host guard, the CSP and `no-store` come from `boardGuard` unchanged.
5. **Shell**:
   - A **Comment** toggle in the shell bar.
   - In comment mode, hovering any board element outlines it with one class: `relevo-hover`, an `outline` with a negative `outline-offset`, so the layout does not shift.
   - A click is captured (default behaviour and propagation stopped) and opens the shell's draft box.
   - **Post** sends the draft with `If-Match`.
   - A side list shows every entry in order (`by`, `at`, `text`). Orphans are marked "element not found". Board-level notes sit under a "board" heading.
   - Clicking a pin or a list row focuses its counterpart.
   - Pins reposition on scroll and resize.
   - Comment mode off removes the listeners and the class.
6. **Promote**: as D7. When it copied an `annotations.json`, it prints a second line: `board: promoted <src annotations> -> <dst annotations>`.

## Seams

New files:
- **`internal/board/annotations.go`**:
  - the `Annotation` type and `AnnotationRequest`;
  - `AnnotationsPath(boardPath)`;
  - `ReadAnnotations(boardPath) ([]Annotation, etag string, err)`:
    - a missing file means `[]` and `""`;
    - a symlink is refused through `os.Lstat`;
    - reads are capped at `maxBody`.
  - `AppendAnnotation(boardPath, req, ifMatch *string) (Annotation, etag, err)`, where nil `ifMatch` means unconditional (the CLI):
    - refuses a missing `board.html` with `ErrNotFound` and a stale etag with `ErrConflict`;
    - splices before the final `]`;
    - writes through `writeAtomic` (`scene.go:121`).
  - It reuses `validateBy`, `usagef`, `ErrInvalid/ErrConflict/ErrNotFound` (`scene.go:17-24`) and `humanBy`.
- `internal/board/annotations_test.go`.
- **`internal/board/html_annotations.go`**: the `/api/annotations` handler, the conditional-GET helper and the mutex. `html_server.go` keeps only the routing.
- `internal/board/html_annotations_test.go`.
- **`internal/board/shell/overlay.js`**: the in-frame script (D1, D3). It never contains `</script`.
- **`internal/board/shell/comments.js`**: the toggle, the draft box, the list and the banner. It loads through a second `<script>` tag in `index.html`. The builder may fold it into `shell.js` instead; either way each JS file stays under about 300 lines.
- **`cmd/relevo/board_comment_html.go`**: `cmdBoardCommentsHTML` and `cmdBoardCommentHTML`, each function at most 70 lines.
- `cmd/relevo/board_comment_html_test.go`.

Edited files:
- **`internal/board/html_server.go`**:
  - `boardDoc` (36-45) gains `Annotations`, `AnnotationsEtag` and `AnnotationsError`;
  - `handleBoard` (62-90) gains the 204 on `?etag&aetag`;
  - `Handler` (50-57) registers `/api/annotations`;
  - `handleShellScript` (111-119) serves the allowlist `{shell.js, comments.js, overlay.js}`.
- **`internal/board/html.go`**: `Promote` (164-188) per D7:
  - check both targets first;
  - copy `board.html`, then `annotations.json`, refusing a symlinked source;
  - under `--force` with no source `annotations.json`, remove a stale target one;
  - at most 70 lines per function.
- **`internal/board/shell/shell.js`**:
  - `render` (39-44) adds `overlay.js` before the last `</body>` (else appends it at the end) and revokes the previous blob URL;
  - `load` gains the 2 s poll and the clean/dirty rule.
- **`internal/board/shell/index.html`**: the toggle, the draft box, the list pane, and the `.relevo-*` styles. `#frame` leaves room for the list.
- **`cmd/relevo/board_comment.go`**:
  - `cmdBoardComments` (56-94) and `cmdBoardComment` (99-152) branch: an explicit `.excalidraw` argument takes the existing body unchanged, and everything else goes through `resolveBoardHTML` (`board_html.go:24`) to the HTML branch;
  - `boardCommentFlagSet` (42-50) gains `--selector`.
- **`cmd/relevo/registry_rows.go`**:
  - `board comment` (38-43): `Args` and `Flags` gain `--selector`, and the summary changes;
  - `board comments` (44-49): `Output` becomes `json:[]board.Annotation`;
  - `board promote` (50-57): the summary mentions `annotations.json`.
- **`cmd/relevo/testdata/contract/help-json.golden`**: regenerate.
- **`cmd/relevo/board_comment_test.go`**: these tests switch from a bare call to an explicit live `.excalidraw` path. Their assertions do not change.
  - `TestBoardCommentsJSONShape` (50)
  - `TestBoardCommentsTextRows` (87)
  - `TestBoardCommentWritesPointer` (129)
  - `TestBoardCommentByDefaultsToMasterMind` (155)
  - `TestBoardCommentPrintsLineAndAppends` (178)
  - `TestBoardCommentRefusalCodes` (212), only its bare cases
- **`internal/board/external_test.go:141-151`**: `TestExternalRefsOnOurOwnShell` iterates over every shell file.
- **`README.md`**:
  - the comment rows (286-290);
  - the `### relevo board` section (around 1253-1300).

Untouched: `cmd/relevo/board.go`, `board_resolve.go`, `server.go`, `comment.go`, `splice.go`, and the Excalidraw assets.

## Steps (each is a new commit; never amend or rebase a pushed commit)

0. **Guard.** Check that the slice 1 seams exist and that `wc -l cmd/relevo/board.go` is at most 590. If not, **halt**.
1. **Storage.** Add `annotations.go` and its tests. Done when these pass:
   - `TestReadAnnotationsMissingIsEmpty`
   - `TestReadAnnotationsRefusesNonArray`
   - `TestReadAnnotationsRefusesEntryWithoutBy`
   - `TestReadAnnotationsRefusesBadAt`
   - `TestReadAnnotationsToleratesUnknownFields`
   - `TestReadAnnotationsRefusesSymlink`
   - `TestAppendAnnotationKeepsPriorBytes`
   - `TestAppendAnnotationLeavesBoardHTMLByteIdentical`
   - `TestAppendAnnotationRefusesMissingBoard`
   - `TestAppendAnnotationStaleEtagConflicts`
   - `TestAppendAnnotationValidation`, a table
   - `TestAppendAnnotationIDsAreUnique`
2. **Server API.** Done when these pass:
   - `TestHTMLAnnotationsRequiresToken`
   - `TestHTMLAnnotationsForeignHostForbidden`
   - `TestHTMLAnnotationsPostAppends` (201, the etag moves, `by` is `human` even when the body sends `by`)
   - `TestHTMLAnnotationsStaleIfMatchIs409`
   - `TestHTMLAnnotationsMissingBoardIs404`
   - `TestHTMLAnnotationsInvalidIs422`
   - `TestHTMLAnnotationsOversizedIs413`
   - `TestHTMLAnnotationsMethodNotAllowed`
   - `TestHTMLBoardCarriesAnnotations`
   - `TestHTMLBoardConditionalGetIs204` (both etags match gives 204; either one moved gives 200)
   - `TestHTMLBoardMalformedAnnotationsStillServesBoard`
   - `TestHTMLBoardHTMLIsFileBytesExactly`

   Every existing `html_server_test.go` test stays green and unedited, including `TestHTMLBoardIsGetOnly`.
3. **Shell assets.** Done when these pass:
   - `TestHTMLShellAssetsNeedNoToken`, extended to `/comments.js` and `/overlay.js`;
   - `TestHTMLShellUnknownScriptIs404`;
   - `TestExternalRefsOnOurOwnShell`, covering every shell file;
   - `TestOverlayHasNoScriptCloseTag`;
   - `TestShellAcceptsOnlyFrameMessages`, a text pin on the `ev.source` guard;
   - `TestShellRendersBoardsAsUTF8`, still green.
4. **CLI.** Done when these pass:
   - `TestBoardCommentsHTMLJSONShape`
   - `TestBoardCommentsHTMLTextRows`
   - `TestBoardCommentsHTMLMissingBoardIsEmpty`
   - `TestBoardCommentsHTMLNeverWritesPointer`
   - `TestBoardCommentHTMLAppendsAndPrintsLine`
   - `TestBoardCommentHTMLRefusesMissingBoard`, exit 2
   - `TestBoardCommentHTMLSelectorFallbackDefaults`
   - `TestBoardCommentHTMLXYNeedSelector`
   - `TestBoardCommentSelectorOnExcalidrawIsUsage`
   - `TestBoardCommentHTMLRepoNeverWritesPointer`
   - `TestBoardCommentHTMLLeavesBoardByteIdentical`
   - every pre-existing `TestBoardComment*` test, green

   **CI has neither a harness nor network access.** These tests call `cmdBoardComment`/`cmdBoardComments` in-process with `boardStateRoot(t)`, `stubRepoSeamAbsent(t)` and `stubBoardClock`. They never spawn a harness, open a browser or start a daemon.
5. **Registry, golden and peek.** Make the row edits and regenerate the golden. Done when these pass:
   - the registry parity test (flags match the flag sets);
   - the `help-json` contract test;
   - `TestBoardPromoteIsAPeekVerb`;
   - `TestBoardResolutionNeverStartsTheDaemon`.
6. **Promote copies annotations.** Done when these pass:
   - `TestPromoteCopiesAnnotations`
   - `TestPromoteWithoutAnnotationsWritesNone`
   - `TestPromoteRefusesExistingAnnotationsWithoutForce` (neither file written)
   - `TestPromoteForceReplacesAnnotations`
   - `TestPromoteForceRemovesStaleAnnotations`
   - `TestPromoteRefusesSymlinkedAnnotationsSource`
   - `TestBoardPromotePrintsAnnotationsLine`
   - every existing `TestPromote*` and `TestBoardPromote*` test, green
7. **README.** Done when it covers comment mode, `annotations.json`, `--selector`, the missing-board refusal and promote; and `grep -n '^>>>>>>>\|^<<<<<<<' README.md` is empty.
8. **Checks.**
   - Focused, iterate until green: `go test ./internal/board/ ./cmd/relevo/ -run 'Annotation|HTML|Comment|Promote|Shell|Overlay|External|Registry|Peek|HelpJSON|Daemon'`
   - Then once: `make check`
   - Then: `gofmt -l $(git ls-files '*.go')` locally, because dev-run gofmt is vacuous in a worktree.
   - Export `TMPDIR`/`GOTMPDIR` off `/tmp`.
   - Do not regenerate the coverage baseline. Code moves only into new files of the same packages; on a coverage failure, add tests and never lower the baseline.
9. **Hand check (not CI).** Set up a scratch repo:
   - write `docs/boards/demo/board.html` with inline CSS, a `data-board-id="hero"` section, a list without ids, and a script that adds text;
   - run `relevo board --no-open docs/boards/demo/board.html` and open the URL.

   Check in the browser:
   - **Comment toggle**: on, then off. Hover outlines appear only while it is on.
   - **Hover outline**: moving across elements outlines one at a time, with no reflow. A sibling's `getBoundingClientRect` is unchanged.
   - **Page to CLI**: click the hero, type a comment, Post. `relevo board comments … --json` shows selector `[data-board-id="hero"]`, plausible x/y, and `by` `human`. Clicking a list item gives a CSS path selector.
   - **CLI to page**: `relevo board comment … --text hi --selector '[data-board-id="hero"]' --x 0.5 --y 0.5`. The pin appears within about 2 s without a reload.
   - **Dirty banner**: open a draft, then add a CLI comment. The banner shows and the draft is kept; Apply brings in the new pin. A stale Post gives 409 and the banner.
   - **Orphan**: delete the hero section on disk. Its pins are listed as "element not found", not dropped.
   - `board.html` is byte-identical apart from your own deletion.
   - `fetch('/api/annotations', {method:'POST'})` from inside the iframe fails.

   If no browser is available, report "hand check not run" and do not claim it. If the overlay cannot run under the existing CSP, **halt** and report. Do not loosen the CSP.
10. **Save this plan** as `docs/plans/2026-10-03-board-s2-comments.md`, in its own final commit.

## Mutations (each must fail the named test)

| Mutation | Test that must fail |
|---|---|
| Append by re-marshalling the whole array | `TestAppendAnnotationKeepsPriorBytes` |
| Drop the `Lstat` symlink refusal | `TestReadAnnotationsRefusesSymlink` |
| Drop the `If-Match` compare | `TestHTMLAnnotationsStaleIfMatchIs409` |
| Take `by` from the request body | `TestHTMLAnnotationsPostAppends` |
| Drop the token check on `/api/annotations` | `TestHTMLAnnotationsRequiresToken` |
| Make the 204 compare only the board etag | `TestHTMLBoardConditionalGetIs204` |
| Let the append create a missing board | `TestAppendAnnotationRefusesMissingBoard` and `TestBoardCommentHTMLRefusesMissingBoard` |
| Route a bare `board comment` back to `resolveBoard` | `TestBoardCommentHTMLAppendsAndPrintsLine` |
| Skip the target `annotations.json` existence check in `Promote` | `TestPromoteRefusesExistingAnnotationsWithoutForce` |
| Remove the `ev.source` guard in the shell | `TestShellAcceptsOnlyFrameMessages` |

## What is deleted or changed (closed list)

1. A bare `board comment` (no path, `--board` or the pointer) no longer appends to `<id>/<name>.excalidraw`, and no longer creates that scene when it is missing. It appends to `<id>/<name>/annotations.json` and refuses when `board.html` is missing.
2. A bare `board comments` no longer reads `<id>/<name>.excalidraw`. It reads `annotations.json` beside the HTML board.
3. The `board comments` registry `Output` changes from `json:[]Comment` to `json:[]board.Annotation`, and the `board comment` row's `Args`, `Flags` and summary change. The `help-json.golden` changes with them.
4. The `TestBoardComment*` tests listed under Seams pass an explicit `.excalidraw` path instead of relying on a bare call. Their assertions are unchanged.
5. `promote --force` from a source without `annotations.json` removes the target's `annotations.json` (D7, new).
6. `handleShellScript` serves a fixed allowlist of three scripts, not only `shell.js`.

Nothing else is removed. The Excalidraw comment flow for explicit `.excalidraw` paths, `board text`/`annotate`, the Excalidraw server and `board.go` stay.

## The report must include

- `git diff --stat`, compared against the seams. Name any file outside them and say why.
- For each step, the tests that pin it, and that they pass.
- Every mutation, with the test that failed. A mutation where no test failed is named, not counted as a pass.
- The hand-check results from step 9, item by item, or "not run".
- The tail of `make check`, the local `gofmt -l` result (empty), and confirmation that the coverage baseline was not touched.
- Line counts:
  - `board.go` (still 590);
  - `board_comment.go`;
  - each new Go file: under 600, every function at most 70 lines.
- Confirmation that `go.mod` gained no dependency and that no lint, size or comment exclusion was added.
- The known limit: a CLI append and a page post at the same instant can lose one of the two. No cross-process lock exists, and Windows is a build target.
