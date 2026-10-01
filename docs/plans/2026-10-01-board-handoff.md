# Board: handoff (2026-10-01, session "board-1", MasterMind opencode-110)

Untracked on purpose. Read this first, then the two specs on `main`:

- `docs/specs/2026-10-01-board-design.md` -- S1 design and slices S1-S3 (#795).
- `docs/specs/2026-10-01-board-live-design.md` -- board v2: a live board per
  MasterMind, with comments (#838), slices L1-L4.
- `docs/plans/2026-10-01-board.md` -- the slice plan (S1 at builder precision,
  S2/S3 outlines).
- `docs/plans/2026-10-01-board-s1.md` -- the S1 round's plan.

## State: S1 merged, spec merged, nothing in flight

`main` carries:

- `e2e1c3b3 feat(board): relevo board S1 -- local Excalidraw board, cockpit
  .svg, spec (#795) (#826)` -- the whole S1 line, including two fixes:
  - `dad79b5e` rewrites the CDN fallback Excalidraw compiles into the bundle to
    `/assets/` and asserts it (`scripts/board-assets.sh`); the bundle must
    contain zero `esm.sh`.
  - `db1ebd3b` makes Ctrl-S save through a window-**capture** listener:
    Excalidraw binds Ctrl-S on `document` in the capture phase, so a bubble
    listener never sees it. The regression check must be a real CDP
    `Input.dispatchKeyEvent` **with the canvas focused**; a bare page load has
    focus on `<body>` and hides the bug.
- `5b1af52f docs(board): the live per-MasterMind board spec (#838) (#844)`.

S1 shipped: `relevo board [path] [--theme NAME] [--no-open]` (foreground,
loopback + per-run token, one repo scene, GET/PUT with `If-Match`, companion
`.svg`, atomic writes, peek route -- no daemon), themes `cockpit` (default) and
`blueprint`, the cockpit's `.svg` open kind, the vendored/off-site page with
self-hosted fonts, `make board-assets` (node on the dev machine; CI never runs
node), and the README section.

No bindings are left on `opencode-110`; the `board` binding was unbound after
the merge.

## Open / next

1. **S2 + S3 as a chain** (owner's call: chains fit here now that S1 landed).
   - S2: `relevo board text <file> [--json]` and `board annotate <file> --text
     ... [--x --y]` (append one valid text element; the rest of the scene
     byte-identical; the font-safety rule: `autoResize:true`, an explicit width
     >= the measured estimate, `fontFamily` 3); the `.excalidraw` cockpit
     fallback (companion `.svg` when present, else `$EDITOR` + a hint).
   - S3: Mermaid render/import in the page, recolour by role (node ->
     panel/ink, edge -> line, cluster -> faint), `themeVariables` from the
     theme table, regenerated asset manifest.
   - Write the two plans first (lite-planner seeds, review with `show --output`),
     then `relevo chain --name board-23 --plan s2.md --plan s3.md --feature
     board [--server zen]`. S3 needs node on the runner for `make board-assets`
     (zen has it).
2. **Live board L1 + L2** (#838, pure Go; can run in parallel with S2/S3):
   - L1 the live scope: `<state root>/boards/<mastermind-id>/`, default scene
     `board`, a `current` pointer file, resolution order (path -> `--board` ->
     pointer -> create), `--mastermind`, confinement, `server.json`, the
     status document's `board` block + the plugin's statusline segment,
     `relevo board url`.
   - L2 comments: the `customData.relevo.comment` marker (`by`, `at`) on text
     elements, `relevo board comments` / `board comment`, flat comments, colour
     cockpit `#b48cf2` / blueprint `#c4b5fd`.
   - L3 (the live page + 2 s poll + dirty banner) needs L1/L2; L4 (promotion +
     Mermaid authoring) needs S3.
3. **Owner feedback pending.** S1 was tested only partly: save works and both
   files land, Ctrl-S works after the fix. The themes, the cockpit `.svg`, the
   409, and the refusal cases were not reported. The test recipe:
   ```sh
   git worktree add /tmp/board-test origin/main
   cd /tmp/board-test && go build -o /tmp/relevo-board ./cmd/relevo
   /tmp/relevo-board board            # draw, Ctrl-S; both files appear
   /tmp/relevo-board board --theme blueprint
   git config relevo.boardTheme blueprint
   ```
   then: edit the scene on disk while the page is open -> 409 notice; `relevo
   ui` artifacts tab -> the `.svg` opens; run outside a repo / bad path / bad
   theme -> usage errors; devtools Network -> nothing leaves `127.0.0.1`.

## Traps and environment

- **#846: a remote round that finished can sit "open" client-side for hours.**
  The sync is lazy; `relevo stop` says "nothing to stop" while `status` says
  ACTIVE; the statusline shows "prompt sent". The work is safe on the server
  worktree: `git fetch zen:<serve worktree> relevo/<branch>` brings the commit,
  and the close usually lands later. Do not conclude "stuck" from one look.
- Runners: `zen` (serve host, 192.168.100.11, `ssh zen`, fish shell) and
  `contabo`. The serve worktrees live at
  `~/.local/state/relevo-serve/serve/bindings/8ef156ea.../.worktrees/<name>` on
  zen.
- CI (`ci.yml`) has path filters: docs-only PRs skip the heavy jobs. Branch
  protection is strict: when `mergeStateStatus` is `BEHIND`, rebase on `main`
  and push again; merge when `CLEAN`. `--auto` is not enabled on the repo.
- After merging `main` into a branch that touched `cmd/relevo`, regenerate:
  - `go test ./cmd/relevo -run TestHelpJSONDocumentsTheSurface -update`
  - `TMPDIR=~/.cache/rel-tmp go test -race -count=1 -cover ./... > .coverage.txt
    && sh scripts/check-coverage.sh --write` (the laptop's `/tmp` is a 3 GB
    tmpfs that fills).
- The board assets are committed (238 files); `check-name.sh` passes without an
  exclude; `make board-assets` must be byte-identical on a second run.
- Theme preview (throwaway): `/tmp/opencode/board-themes/preview.html`.
- relevo upgrades often; this session's MCP server needs `/mcp` reconnect after
  an upgrade.

## How this session worked

MasterMind wrote detailed seeds; `planner` (opus) turned the board spec seed
into a plan; builders on zen ran the rounds; verification was local focused
tests plus `make check`; commits were fetched by ssh when the round close
lagged; `main` conflicts (the `db` verb vs `board`) were resolved by combining
both sides and regenerating the golden + coverage baseline; PRs merged with CI
green.
