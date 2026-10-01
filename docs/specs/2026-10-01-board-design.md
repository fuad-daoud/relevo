# Board: a local Excalidraw whiteboard for diagrams that live in the repo

**Issues:** #795. This spec covers S1 (the local board and the cockpit's `.svg`
open kind) and outlines S2 (the agent leg and the `.excalidraw` cockpit
fallback) and S3 (Mermaid in the page). It replaces the claude.ai artifact
canvas, which lives outside the repo and points at a service.

## 1. Goal

A local whiteboard on Excalidraw for diagrams that live in the repo. Humans
edit them in a browser; agents read and annotate them as plain Go/JSON. The
boards are files: readable by the MasterMind and the builders, viewable from
the cockpit, and carried by PR diffs through the companion `.svg`. Mermaid
stays the builder-readable equivalent; Excalidraw is the human-facing surface.

Today nothing in relevo can hold or show a diagram: the cockpit is a terminal,
round artifacts are read as text, and a round prompt is one markdown file, so a
builder can never see a picture.

## 2. Decisions (owner, 2026-10-01; recorded verbatim)

**D1 Shipping.** Vendored, committed, embedded. A dev-only wrapper app `board/`
(React 18.3.1 + @excalidraw/excalidraw 0.18.1 + @excalidraw/mermaid-to-excalidraw,
all MIT) built by `scripts/board-assets.sh` into committed assets under
`internal/board/assets/`: bundle, CSS, self-hosted fonts (EXCALIDRAW_ASSET_PATH;
the default loads fonts from a CDN). `go:embed` + checksum, like
internal/harness/agents. CI has no node/network: assets are committed;
`make board-assets` is the dev-only regeneration + checksum step.

**D2 Themes.** Data, two built-ins. cockpit (default): dark bg #0f1115, ink
#e6e8ec, muted #9097a3, faint #596070, line #3a4150, panel #1a1f28, accent
#6ea8fe, good #5fd08f, warn #f2b84b, bad #ff6b81, Cascadia (fontFamily 3),
roughness 0. blueprint: bg #0b1220, ink #dbe4f0, muted #8296b3, faint #4c5f7d,
line #2c3c58, panel #111c2f, accent #7dd3fc, good #86efac, warn #fcd34d, bad
#fca5a5, Cascadia, roughness 0. `relevo board --theme <name>`, per-repo default
from config. A theme sets appState defaults for NEW elements (currentItem*), it
does not recolor existing scenes. Mermaid in the page uses the same palette as
themeVariables.

**D3 Boards.** Repo dir `docs/boards/*.excalidraw` by default; `relevo board
[path]` opens any scene under the repo root; the server confines paths (no
traversal). On save the page exports a companion `<name>.svg` (exportToSvg) for
the cockpit and agents; the spec says committed or ignored.

**D4 Lifecycle.** Foreground `relevo board`: binds 127.0.0.1:0, prints and opens
the URL (xdg-open/open, `--no-open`), serves a tiny JSON API (GET/PUT one
scene) with a per-run token required on every call, Ctrl-C stops. Loopback
only; the daemon is untouched (no HTML from it).

**D5 Cockpit.** `.svg` opens in the browser: `artifactOpenKind`
(internal/ui/artifacts.go:29) gains `.svg`; internal/ui/open.go:43 already
xdg-opens "browser". `.excalidraw`: the companion `.svg` when present, else
$EDITOR with a hint (or a short-lived viewer; the spec picks).

**D6 Agent leg.** Pure Go: `relevo board text <file>` lists text elements
(id, x, y, text); `relevo board annotate <file> --text ...` appends one valid
text element. CI-testable. Known trap: text measured before the font loads
renders clipped in Excalidraw; CLI-made text must be safe (autoResize and/or an
explicit width) -- the spec states the rule and the test that pins it.

**D7 Mermaid.** Builders keep reading Mermaid text; the page renders it for
humans and imports via mermaid-to-excalidraw (converted elements carry default
Excalidraw colors; recolor by node role). Round-trip is out of the first cut.

## 3. Findings: where the seed disagrees with the code

Each finding is recorded here and is the spec's rule. The MasterMind confirmed
A.1–A.5 on 2026-10-01 (see also §4, §8 and §9).

**A.1 -- "Per-repo default from config" has nowhere to live.** The relevo
config is the machine database (`internal/config/config.go:22-38`: one global
document; the sections are `candidates`, `agents`, `actors`, `accounts`,
`policy`, `roles`, `prices`, `servers` and `hooks`). There is no per-repo
section, and reading the database would make `board` dial the daemon (A.2).
**Rule:** the per-repo default is the repo-local git config key
`relevo.boardTheme`, read through `git config --get` in the board's working
directory. The pattern already exists at `internal/git/repo.go:48-73`, where an
unset key is `("", nil)`. Precedence: `--theme`, then `relevo.boardTheme`, then
`cockpit`. A new config section was considered and rejected for the first cut;
the MasterMind may overrule this. **Confirmed by the MasterMind 2026-10-01.**

**A.2 -- "Daemon untouched" requires the peek route.**
`cmd/relevo/main.go:199-201` runs `captureAgyEnv()` and dials the database owner
for every non-peek verb. **Rule:** `board` joins `isPeekArgs`
(`cmd/relevo/machinedb.go:122-130`) so it gets `routeNone`: no database, no
daemon start, no agy capture. The precedent and its test are bugreport
(`machinedb_routes_test.go:203-211`). **Confirmed as written.**

**A.3 -- "All MIT" covers the npm packages but not the fonts.** Cascadia Code,
Assistant and Excalifont are SIL OFL 1.1. **Rule:** every licence text ships
inside `internal/board/assets/` (`LICENSES.txt`, written by the assets script).
**Confirmed as written.**

**A.4 -- check-name will scan the vendored bundle.** `scripts/check-name.sh`
greps every tracked text file. If the minified bundle contains a `relay` token,
**rule:** S1 adds one exclude pathspec for `internal/board/assets/**`, next to
`go.sum` (the list at `scripts/check-name.sh:31-41`). That is a generated-file
exemption, not a lint exclusion, and S1's report must say so. **Confirmed as
written.**

**A.5 -- Excalidraw's dark mode may not show stored colours.** Its dark mode is
believed to invert the canvas with a CSS filter; this is unverified for 0.18.1.
If so, a stored #0f1115 is not what the user sees. **Rule:** the page runs
Excalidraw `theme:"light"`, sets `viewBackgroundColor` to the palette
background, and calls `exportToSvg` with `exportWithDarkMode:false`, so what is
stored, shown and exported is the same colour. S1's hand check confirms this;
if it proves false, the hand check records it and the spec is corrected in S1.
**Confirmed by the MasterMind 2026-10-01.**

## 4. Shipping (D1)

- `board/` holds `package.json` and `package-lock.json`, pinning `react` and
  `react-dom` 18.3.1, `@excalidraw/excalidraw` 0.18.1 and
  `@excalidraw/mermaid-to-excalidraw` at an exact version, plus `esbuild` as a
  pinned devDependency. Node's own `engines` records the version the lock was
  made with.
- `board/` also holds `src/` (the wrapper) and `index.html`.
  `board/node_modules` is git-ignored.
- `scripts/board-assets.sh` does: `npm ci`, the esbuild bundle, a copy of
  `dist/prod/index.css` and of the `dist/prod/fonts/` tree (structure
  preserved), and writes `LICENSES.txt` (all MIT and OFL notices) and
  `assets.sha256`.
- `make board-assets` runs that script. It is dev-only: CI never runs node.
- The page sets `window.EXCALIDRAW_ASSET_PATH` to the server's asset prefix, so
  no CDN is used.
- Integrity: `internal/board` embeds `assets/` and a Go test checks the
  manifest both ways. Every embedded file must be listed with a matching
  sha256, and every listed file must exist. This mirrors
  `internal/harness/shipped.go`, but the manifest is a plain integrity list,
  not a ship history.
- The spec states the expected binary-size growth, measured in S1. The figure
  is not fixed here: the assets are committed megabytes, and S1 measures the
  built binary with and without them and records both numbers in its report.

## 5. Lifecycle (D4)

- Command: `relevo board [path] [--theme NAME] [--no-open]`.
- The repo root is `git rev-parse --show-toplevel` of the cwd; outside a repo
  the verb is refused with `usage`, exit 2.
- It binds `127.0.0.1:0` and prints one line,
  `board: <url>  (Ctrl-C to stop)`. It opens the URL with `xdg-open` (Linux) or
  `open` (macOS) unless `--no-open`; a failed opener is a warning, not a
  failure.
- It stops on SIGINT/SIGTERM through `http.Server.Shutdown`, so an in-flight
  save finishes.
- No daemon contact: `board` is a peek verb (A.2), so it neither opens the
  machine database nor captures the agy environment.

## 6. Paths (D3)

- With no argument the scene is `docs/boards/board.excalidraw`.
- Any path is resolved against the cwd and must:
  - end in `.excalidraw`;
  - after `EvalSymlinks` on the deepest existing ancestor, sit under the repo
    root.
- Anything else is `usage`, exit 2.
- A missing file is a new scene. It and its parent directories are created on
  the first save, never at startup.
- The API takes **no path at all**: the scene is fixed at startup, which is
  stricter than confining paths per request.
- The companion `<name>.svg` sits next to the scene and **is committed**, so the
  cockpit, agents and PR diffs can read it.

## 7. API (D4)

Three routes:

- `GET /` and `GET /assets/*` are the embedded files, with no token: the code is
  public and the page cannot hold a token before it loads.
- `GET /api/scene` returns `{scene, etag, theme, isNew}`.
- `PUT /api/scene` takes `{scene, svg}` and an `If-Match: <etag>` header.

Rules:

- The token is 32 random bytes in hex. The printed URL carries it in the
  **fragment** (`/#t=…`), so it is never sent in a request line or logged. Every
  `/api/*` call must send `X-Relevo-Board-Token`, compared in constant time;
  otherwise 401.
- `Host` must equal the bound `127.0.0.1:<port>`; otherwise 403. This defends
  against DNS rebinding.
- The PUT body is capped at 32 MiB (413).
- The scene must be a JSON object with `type == "excalidraw"` and an `elements`
  array (422).
- The svg must start with `<svg` after optional XML prolog and whitespace (422).
- An `If-Match` that differs from the sha256 of the bytes on disk returns 409,
  and the page tells the user to reload. This guards against an agent
  annotation (S2) or a `git checkout` made while the page is open.
- Writes are atomic: a temp file in the same directory, then rename. The scene
  is written first, then the svg, and the response carries the new etag.
- Security headers on every response: a CSP of
  `default-src 'self'; img-src 'self' data: blob:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:; script-src 'self' 'wasm-unsafe-eval'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and
  `Cache-Control: no-store` on `/api`.
- The CSP relaxations have two causes: Excalidraw uses inline styles
  (`style-src 'unsafe-inline'`) and its font subsetter runs as wasm inside a
  worker (`script-src 'wasm-unsafe-eval'`, `worker-src 'self' blob:`).
  `img-src` and `font-src` allow `data:` and `blob:` for exported images and
  the in-page font blobs.

## 8. Themes (D2)

Both palettes are a Go table, with every hex value from the seed:

| name | bg | ink | muted | faint | line | panel | accent | good | warn | bad |
|---|---|---|---|---|---|---|---|---|---|---|
| cockpit | #0f1115 | #e6e8ec | #9097a3 | #596070 | #3a4150 | #1a1f28 | #6ea8fe | #5fd08f | #f2b84b | #ff6b81 |
| blueprint | #0b1220 | #dbe4f0 | #8296b3 | #4c5f7d | #2c3c58 | #111c2f | #7dd3fc | #86efac | #fcd34d | #fca5a5 |

Both use Cascadia (fontFamily 3) and roughness 0.

- A theme maps to `currentItemStrokeColor` = ink, `currentItemBackgroundColor`
  = transparent, `currentItemFontFamily` 3, `currentItemRoughness` 0, the
  `currentItemStrokeColor` used for lines = line, plus the page's own chrome
  colours (bg, muted, faint, panel, accent, good, warn, bad).
- `viewBackgroundColor` is set **only when `isNew`**. An existing scene's
  appState and elements are never rewritten.
- Unknown names are `usage`, exit 2, and list the valid names.
- Mermaid uses the same palette as `themeVariables` (S3).
- Precedence: `--theme`, then the repo-local `relevo.boardTheme` (A.1), then
  `cockpit`.

## 9. Font safety (D6 trap)

- The page awaits `document.fonts.load` for Cascadia, Excalifont and Assistant
  before it hands `initialData` to Excalidraw.
- Rule for CLI-made text (S2): `autoResize:true`, an explicit `width` ≥ the
  measured estimate, and `fontFamily` 3.
- S2 pins this rule with a Go test on the emitted element's fields.

## 10. Cockpit (D5)

- S1: `.svg` opens in the browser. `artifactOpenKind`
  (`internal/ui/artifacts.go:29-38`) maps `.svg` to `"browser"`, and
  `internal/ui/artifacts_test.go:157-161` gains an `.svg` row.
- S2: `.excalidraw` opens its companion `.svg` when it exists, else `$EDITOR`
  with a hint line (`"run relevo board <path> to edit"`). The spec picks this
  over a short-lived viewer: no second server lifecycle.

## 11. Agent leg (D6), S2

- `relevo board text <file> [--json]` lists (id, x, y, text).
- `relevo board annotate <file> --text S [--x --y]` appends one text element and
  keeps the scene otherwise byte-for-byte equal.
- Both are pure Go and CI-tested, under the same path confinement.
- `text` and `annotate` are subverbs. A scene path must end in `.excalidraw`, so
  it cannot collide with them.

## 12. Mermaid (D7), S3

- The page renders mermaid code blocks and offers "import" through
  mermaid-to-excalidraw.
- Imported nodes are recoloured by role (node → panel/ink, edge → line,
  cluster → faint).
- Round-trip back to Mermaid text is a non-goal.

## 13. Testing

CI covers the Go parts only: themes, path confinement, the API under `httptest`
(token, Host, If-Match, caps, atomic writes, svg companion), the asset manifest,
the registry and peek route, and the `.svg` open kind.

The page is checked by hand with the list in S1 step 10 of the plan
(`docs/plans/2026-10-01-board.md`).

## 14. Slices

1. **S1 -- the local board and the `.svg` cockpit.** Cockpit `.svg` open kind;
   `relevo board` foreground with the vendored page; one-scene GET/PUT; the
   companion SVG; the theme table and defaults; the asset pipeline and manifest.
   Independently mergeable and the first PR for this spec.
2. **S2 -- the agent leg and the `.excalidraw` fallback.** `board text` and
   `board annotate` (subverb dispatch plus registry rows); the font-safety test;
   the `.excalidraw` cockpit fallback (an open kind and a hint line).
3. **S3 -- Mermaid in the page.** The render and import in the page; the
   recolouring by role; `themeVariables` built from the theme table; a
   regenerated asset manifest.

## 15. Non-goals

- Collaboration or multi-user editing.
- Any daemon or wire change.
- Remote binds.
- An HTML page served by the daemon.
- Mermaid round-trip.

Adjacent choices this spec also rejects for the first cut: a per-repo config
section for the default theme (A.1, the repo-local git key instead), and a
short-lived `.excalidraw` viewer process (§10, the companion `.svg` instead).
