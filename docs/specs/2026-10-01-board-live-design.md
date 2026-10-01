# Board v2: a live board per MasterMind, with comments

**Issues:** extends #795 (S1 shipped `relevo board`). A dedicated issue is to
be filed; this spec is the design.

## 1. Goal

Every MasterMind gets a board of its own. The agent explains a design on it --
states, flows, architecture -- instead of ASCII art or long prose; the human
answers by commenting on the board instead of writing paragraphs; neither side
pastes screenshots, and nothing lives outside the repo or the state root.

> "I have tried doing this for explaining the chaining feature: I went to the
> Excalidraw board, started drawing, and gave screenshots. Instead of giving
> screenshots I just tell you to look at the board -- each MasterMind has its
> own board." (owner, 2026-10-01)

## 2. What S1 already gives us

`relevo board [path] [--theme NAME] [--no-open]`: a foreground `127.0.0.1`
server, a per-run token, one repo scene, `GET`/`PUT` with `If-Match`, a
companion `.svg`, two themes, path confinement, no daemon contact, and a page
that is vendored and offline.

It has no notion of an owner, no live update, no comment layer, and no
agent-side read or write (S2), and no Mermaid (S3). This spec adds the first
three; the fourth stays S3's.

## 3. Decisions (owner, 2026-10-01)

**D1 -- a board per MasterMind.** Each MasterMind has its own live board.
Sessions do not share one by default, and a board never blurs between them.

**D2 -- two scopes.** *Live* boards live under the state root, are scratch, and
are not committed. *Repo* boards are the S1 `docs/boards/*.excalidraw`
artifacts, committed with specs, plans and PRs. Promotion from live to repo is
explicit.

**D3 -- comments are a layer.** A comment is a marked text element, anchored
where it is placed. The CLI only appends comments and never rewrites the rest of
the scene; `relevo board comments` is how the agent reads the human's feedback.

**D4 -- the agent authors text, the human draws.** The agent's drawing leg is
Mermaid (S3) plus the comment and annotate verbs (S2); the human draws freehand
in the page.

**D5 -- live means no manual reload.** An open page applies a change made by the
other writer when it has no unsaved edits. When it does, it shows a "board
changed" banner and never merges silently.

**D6 -- one human and one agent.** No CRDT/OT, no multi-user, no presence.
Conflicts are surfaced (409, banner), not resolved.

**D7 -- the live board is visible in the session's statusline.** While a board
server runs for a MasterMind, the statusline carries its URL, so neither side
has to hunt the terminal that started it.

## 4. Identity and addressing

- Live directory: `<state root>/boards/<mastermind-id>/` (the MasterMind record
  and its id already exist).
- Scene names are slugs; the default is `board`, so the default live scene is
  `<state root>/boards/<mastermind-id>/board.excalidraw`.
- A pointer file `<state root>/boards/<mastermind-id>/current` holds the
  current scene name. It is written when a board is opened or selected. A plain
  file keeps `board` a peek verb: no daemon, no database.
- Resolution for `relevo board`, in order:
  1. an explicit path argument -- scope *repo* when it resolves under the repo
     root, scope *live* when it resolves under the live directory; a path under
     neither is refused;
  2. `--board <name>` -- the live scope of the resolved MasterMind;
  3. the pointer file;
  4. create `board` and write the pointer.
- `--mastermind <id|name>` selects whose live board. Without it, the
  `RELEVO_MASTERMIND` of the calling environment is used; with neither, the
  single registered MasterMind of the state root if there is exactly one, else a
  usage error listing them.
- The human opens a session's board with
  `relevo board --mastermind <id> [--board <name>]`. When the agent starts a
  board it prints the URL, and that URL is always the thing to hand over -- both
  sides may run a server on the same file; the etag rules in section 6 apply.
- "Look at the board" means the resolved current board of *this* MasterMind. If
  several boards are in play, the MasterMind names which one it means.

## 5. Comments

The marker: a text element carrying

```
customData: { "relevo": { "comment": true, "by": "human" | "<mastermind>",
                          "at": "<RFC3339>" } }
```

- One comment colour per theme for both writers; `by` distinguishes them. New
  palette role: `comment` -- cockpit `#b48cf2`, blueprint `#c4b5fd`. A comment
  is a text element in that colour; no sticky container in the first cut.
- The page's Comment tool creates one at the click point in that colour.
- Verbs, pure Go, under S1's confinement rules:
  - `relevo board comments [<file>|--board NAME] [--json]` -- every comment in
    scene order: id, x, y, text, by, at.
  - `relevo board comment [<file>|--board NAME] --text S [--x X --y Y] [--by B]`
    -- append one comment element. The rest of the scene is byte-identical (the
    S2 rule), and the text follows the S2 font-safety rule (`autoResize`, an
    explicit width, `fontFamily` 3).
  - Without `--x/--y`, the comment lands below the scene's current bounds, so an
    agent's comment never lands on a drawing.
- The human's comments come from the page or from `board comment` in their
  shell; the agent's from `board comment`. Both are read by `board comments`.
- First cut: flat comments -- no replies, resolve, delete, or thread. Removing a
  comment is an edit in the page like any other, and the CLI never deletes.
  Replies are a non-goal (section 13); a `--wait` for new comments is a later,
  small addition.

## 6. Live updates

- The page polls `GET /api/scene?etag=<current>` every 2 seconds, paused while
  the tab is hidden. The server answers `204` when the file is unchanged.
- When the response carries a new scene and the page has no unsaved edits, the
  page applies it (`updateScene`) and moves its etag.
- When the page is dirty, it shows the banner "the board changed on disk -- save
  to keep yours, reload to take theirs" and pauses applying until the user
  chooses. It never merges.
- The page's own `save` and comment actions clear the dirty flag and refresh the
  etag.
- **Live boards write the scene only.** `GET /api/scene` returns the scope, and
  the page sends the companion `svg` only for a repo board; the PUT of a live
  board takes no svg. The `.svg` stays a repo-board concern.

## 7. Promotion and the repo scope

- `relevo board promote [--board NAME] [--to docs/boards/<slug>.excalidraw]`
  copies the live scene into the repo and refuses an existing target unless
  `--force`. It writes the scene only; the companion `.svg` is produced the next
  time the repo board is saved from the page, and the verb says so.
- Repo boards keep S1's semantics exactly: committed, companion `.svg`, path
  confinement, themes.
- Rule of thumb: live boards are for talking; the moment a sketch belongs to a
  spec, a plan, or a PR, promote it.

## 8. The agent's authoring leg (S3 dependency)

- The agent does not move a mouse. It writes text: a `<scene>.mermaid` companion
  beside the scene, and `board comment` callouts.
- The page renders or imports the companion on demand (S3), placing the diagram;
  the human's freehand layer stays untouched, and comments anchor to either.
- For a flow or a state machine the loop is: the agent writes the Mermaid, the
  board renders it, the human comments on the board, the agent revises the
  Mermaid. The scene stays the human surface; the `.mermaid` file stays the text
  source. Import-only, as S3 says.

## 9. Rules and safety

- The live scope is confined to `<state root>/boards/<mastermind-id>/` by the
  same `EvalSymlinks` rule S1 uses for the repo scope: a live path never
  resolves into the repo, and a repo path never into the live directory.
- The server keeps S1's shape: loopback, per-run token, `Host` check, size caps,
  atomic writes, `If-Match`. A second writer is a 409 or a banner, never a
  silent merge.
- `board` stays a peek verb: the pointer and the scene are plain files, and
  neither the database nor the daemon is touched.
- No new config section, no daemon change, no wire change.

## 10. Surfaces

- `relevo board [path] [--board NAME] [--mastermind M] [--theme T] [--no-open]`
  -- the resolution and scope rules above.
- `relevo board comments`, `relevo board comment` (section 5);
  `relevo board promote` (section 7).
- README: a "live boards" paragraph under `### relevo board`, plus the new verb
  rows.

### The statusline (D7)

- While a board server runs for a MasterMind, it writes
  `<state root>/boards/<mastermind-id>/server.json` (`scene`, `url`, `port`,
  `pid`, `started_at`) and removes it on shutdown.
- `relevo status --line` and `--json` gain an optional `board` block for the
  calling MasterMind: `{name, scope, url}`. It comes from the pointer file and
  `server.json` -- a file read and a pid liveness check, no daemon and no
  database -- so the statusline keeps its short budget.
- The plugin (`internal/harness/opencodeplugin/tui.tsx`) renders a `board` segment
  when the block is present, carrying the full URL (its token included) so it can
  be copied straight into a browser. The token is per-run, loopback-only, dies
  with the server, and is already readable under the state root, so the status
  display adds no new exposure.
- If several servers run on one board -- the agent's and the human's -- `server.json`
  holds the most recent; all of them serve the same file.
- `relevo board url [--board NAME]` prints the same URL for a shell copy when the
  statusline is not at hand.

## 11. Testing

- Pure Go, CI: resolution order and refusals; live-dir confinement (including a
  symlinked parent); the pointer's read, write and precedence; `comments`
  parsing (marker, fields, order); `comment` append (byte-stability of the rest,
  the font-safety rule); `promote` (refuses, `--force`, no svg).
- Server, CI: the live scope's svg-optional PUT, and `204` on an unchanged
  etag; every S1 case unchanged.
- Hand check, not CI: the poll applies an agent write to an open page within
  ~2 seconds; a dirty page shows the banner and never merges; a comment added in
  the page is read by `relevo board comments`; the Comment tool's element
  carries the marker.
- Statusline, CI: the `board` block appears only when a live board exists and its
  pid is alive, and the statusline path never dials the daemon (the existing
  statusline route test extends).

## 12. Slices

- **L1 -- the live scope (pure Go).** The live directory, the default name, the
  pointer, the resolution order, `--mastermind`, `--board`, and confinement;
  `server.json`, the status document's `board` block, `relevo board url`, and the
  plugin's statusline segment. Mergeable alone.
- **L2 -- comments (pure Go).** The marker, `comments`, `comment`, the S2 font
  rule, and the tests.
- **L3 -- the live page (wrapper JS + the poll).** The Comment tool; the 2-second
  poll; the clean-page apply and the banner; the svg-optional PUT for live
  boards; the hand check with two writers.
- **L4 -- promotion and Mermaid authoring.** `promote`; the `<scene>.mermaid`
  companion and the page's render/import (with S3); the README's agent
  workflow.

L1 and L2 can run together; L3 needs both; L4 needs S3.

## 13. Non-goals

- Multi-user collaboration or more than one browser (the #203 party is a
  separate design).
- CRDT/OT, presence, cursors, and chat threads.
- A daemon-hosted board, or a board served over the wire.
- Autosave of drawing; explicit save stays.
- Replies, resolution, deletion, or threading of comments: the first cut is flat
  comments, and the CLI never deletes (a later addition).
- Replacing the repo scope: committed boards stay the durable form.

## 14. Resolved in review (owner, 2026-10-01)

1. **Comment colour.** One role colour per theme for both writers, with `by`
   distinguishing them: cockpit `#b48cf2`, blueprint `#c4b5fd` (the MasterMind
   chose; accepted).
2. **Replies.** Flat comments for the first cut; replies are a non-goal here.
3. **Visibility.** The live board's URL is carried in the session's statusline,
   with `relevo board url` as the shell copy (section 10).
