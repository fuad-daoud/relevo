# Plan: Boards v3 slice 1 (#916, part of #915). `relevo board` serves a single-file `board.html`

Saved by the builder as `docs/plans/2026-10-03-board-s1-html-preview.md` (step 9). I could not read the #915/#916 issue text, because `gh` needed approval in this round. I also could not read the reference artifact under `/tmp/claude-1000/.../relevo-event-delivery.html`, because the sandbox blocked it. This plan rests on the seed, the code and the two board specs. Before sending, the MasterMind should check it against #916 on two points: the three **Decisions taken here** below, and the live layout.

## Decisions taken here (the seed is silent; overrule before sending if wrong)

- **H1 — Additive, not a replacement.** Slice 1 adds the HTML format. The Excalidraw page, `/api/scene`, the embedded `assets/`, `--theme`, and `board text/annotate/comment/comments` all stay. An explicit path ending in `.excalidraw` still opens the Excalidraw server, unchanged. Removing Excalidraw is a later #915 slice. Reason: `board comment`/`comments` resolve through `resolveBoard` (`cmd/relevo/board_comment.go:74,123`) to `.excalidraw` files, and they would break if the shared resolver changed.
- **H2 — Layout.** A repo board is `docs/boards/<slug>/board.html`. A live board is `<state root>/boards/<mastermind-id>/<slug>/board.html`. The slug follows the existing rule (`sceneNameRe`, `internal/board/live.go:35`). An explicit path argument must end in `/board.html` (or `.excalidraw` for the old format). A bare slug directory is not accepted as a path, so a path can never collide with a subverb name (`url`, `comment`, `promote`, …).
- **H3 — How the token gates a static page.** `/` (and `/<owner>/`) serves a small embedded **shell** page that needs no token. The shell reads `#t=` from the fragment and calls `GET /api/board` with `X-Relevo-Board-Token`. It gets JSON back: `{html, etag, isNew, scope, name, external[]}`. It renders `html` from a `blob:` URL inside `<iframe sandbox="allow-scripts">`, which runs the board's scripts in an opaque origin without `allow-same-origin`, so the board can never read the token or call the API. A blob document inherits the shell's CSP, so the shell's CSP must allow inline script and style. The CSP also enforces "offline": `default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'self'; frame-src blob:; base-uri 'none'; form-action 'none'`. Nothing external is allowed. Whether this CSP works is the hand check (step 8).

Out of scope for slice 1:
- live polling and auto-apply (the shell has a Reload button)
- comments on HTML boards
- `@docs` (`internal/pathscope/scope.go:31`) gaining `**/*.html`
- the cockpit opening `board.html`

## Behaviour and cases

1. `relevo board [path] [--board NAME] [--mastermind M] [--no-open] [--theme T]` prints `board: <url>  (Ctrl-C to stop)` exactly as today.
2. Resolution keeps today's order (path, `--board`, pointer, default `board`), with these targets:
   - bare, `--board` or pointer → live `<id>/<slug>/board.html`;
   - a path ending in `board.html` → live when it sits under the live root, repo when it sits under the repo root;
   - a path ending in `.excalidraw` → the old flow, unchanged.
   - The pointer, `--mastermind`, `RELEVO_MASTERMIND` and the single-MasterMind fallback behave as today. The pointer is written for a live board not read from it, and never for a repo board.
3. Confinement uses the existing symlink rules:
   - Repo: `Resolve`, `splitExisting`, `underRoot` (`internal/board/path.go:23-113`).
   - Live: `ResolveLiveArg`, `evalExisting` (`live.go:68-114`).
   - Shape: a live path must be exactly `<live>/<id>/<slug>/board.html`, and a repo path must be `.../<slug>/board.html` under the git top level.
   - Refused: anything else, a bad slug, `..`, a symlinked parent that escapes, and nested scopes (`DisjointScopes`).
4. A missing `board.html` is `isNew: true` with `html: ""` and `etag: ""`. The shell shows "no board yet at <path>". Nothing is created on disk except the pointer and live dir that are written today.
5. The server is GET only:
   - `GET /api/board`: token required (constant time), `Cache-Control: no-store`. Any other method is 405 with `Allow: GET`.
   - The Host header must equal the bound `127.0.0.1:<port>`, or the response is 403.
   - Every response carries the CSP above, `nosniff` and `no-referrer`.
   - A path deeper than one owner segment is 404.
   - The shell JS asset needs no token.
   - A board file larger than a cap (reuse `maxBody`, 32 MiB) is a 500 with a clear message, never a partial read.
6. Offline check: a pure function lists external references in the HTML. A reference counts as external if it is `http:`, `https:`, `ws:`, `wss:`, `ftp:` or protocol-relative `//host`, found in any of:
   - `src`, `href`, `srcset`, `action`, `poster` or `xlink:href` attributes;
   - CSS `url(...)` or `@import`;
   - `fetch(`/`import(` string literals, if cheap.
   
   It ignores `xmlns`/`xmlns:*` namespace URIs (inline SVG carries `http://www.w3.org/2000/svg`), `data:`, `blob:` and `#frag`. The list rides `external[]` in the GET response, where the shell shows a banner. `relevo board` prints one stderr warning at startup naming the first reference and the count. The board is still served, because the CSP blocks the loads anyway.
7. `server.json` and the statusline: a live HTML board writes `server.json` with `scene` = the slug, so `boardBlockFor` (`cmd/relevo/status.go:221-235`) and `board url` (`board.go:139-184`) work unchanged. Today `runBoard` derives the scene name from the file's base minus `.excalidraw` (`board.go:473`), which gives `board.html` for every HTML board. It must take the resolved slug instead.
8. `relevo board promote [--board NAME] [--mastermind M] [--to SLUG] [--force]`:
   - Copies live `<id>/<name>/board.html` to `<repo>/docs/boards/<SLUG|name>/board.html` atomically (`writeAtomic`, `scene.go:121`).
   - Refusals:
     - a missing source is `refused`, through `ErrNotFound` → `boardRefusal`;
     - an existing target without `--force` is `refused`, naming the target and `--force`;
     - a bad slug or running outside a repository is `usage`.
   - It is a peek verb: no DB beyond the registry read that `boardMasterMind` already does, no daemon, no server, no pointer write.
   - It prints `board: promoted <src> -> <dst>`.

## Seams

- `internal/board/html.go` (new):
  - constant `htmlBoardFile = "board.html"`;
  - `ResolveHTML(repoRoot, cwd, arg)` (repo scope; mirrors `Resolve` but with the `<slug>/board.html` shape);
  - `ResolveLiveHTMLArg(liveRoot, cwd, arg)` (mirrors `ResolveLiveArg`; `ok=false` when not under the live root);
  - `ResolveLiveHTMLDir(repoRoot, liveDir, name)` (mirrors `ResolveLiveDir`);
  - `LoadHTML(path)`, built on `Load` (`scene.go:34`) plus the size cap;
  - `ExternalRefs(html []byte) []string`;
  - `Promote(src, dst string, force bool) error`.
  - Each returns `Resolved` (`live.go:49`), with `Scene` = slug.
- `internal/board/html_server.go` (new): `HTMLServer{Token, BoardPath, Scope, Name, Host, Shell fs.FS}` and its `Handler()`. Factor the Host check, the headers and `authorized` out of `Server.Handler` (`server.go:71-114`) into a shared helper both servers use. Do not copy them. `securityHeaders` stays as it is for the Excalidraw server; the HTML server gets its own constant.
- `internal/board/shell/` (new embedded dir, with its own `//go:embed`): `index.html` and `shell.js`. Hand-written, no build step, no external URL. It stays outside `assets/`, so `assets.sha256` and `scripts/board-assets.sh` are not involved.
- `cmd/relevo/board_html.go` (new): `resolveBoardHTML(cwd, ref, boardName, arg)`, a sibling of `resolveBoard` (`board_resolve.go:131-202`) using the same MasterMind and repo-root helpers; `runBoardHTML`; and `cmdBoardPromote` plus `boardPromoteFlagSet`. `board.go` is already 511 lines; keep it under 600.
- `cmd/relevo/board.go`:
  - `cmdBoard` (199-254): add the `promote` subverb case. Route an explicit `.excalidraw` arg to the existing `resolveBoard`/`runBoard`, and everything else to `resolveBoardHTML`/`runBoardHTML`.
  - `runBoard` (451-511): take the scene name from `boardOptions` instead of `filepath.Base` (line 473). Share the listener, `server.json` and signal lifecycle between the two servers through one function that takes an `http.Handler`. Do not duplicate it.
  - Comment at 196-198: update the collision rationale.
- `cmd/relevo/registry_rows.go:23-29`: update the `board` row's summary and args, and add a `board promote` row whose flags match `boardPromoteFlagSet` (the parity test is in `registry_test.go`).
- `cmd/relevo/machinedb.go:117-130`: `isPeekArgs` already covers `board *`. Only update the flag list in the doc comment.
- `README.md`: rewrite the `relevo board` row (277-291) and `### relevo board` (1235-~1300) for the HTML format, `promote` and the legacy `.excalidraw` path. Remove the "Promotion … is a later cut" sentence. Remove the stray conflict marker at line 291 (`>>>>>>> 40c2bb9a …`), which is live on main today.

## Steps (new commits only; never amend or rebase a pushed commit)

1. **Board-side resolution and promote.** Add `html.go`: the three resolvers, `LoadHTML`, `Promote`, and `html_test.go`. It worked when tests pin:
   - repo and live confinement (`..`, symlinked parent, wrong file name, nested dirs, bad slug, nested scopes);
   - missing file → isNew;
   - `Promote` refusing an existing target without force, overwriting with force, refusing a missing source.
2. **Offline check.** Add `ExternalRefs` plus table tests: http/https/`//` in src/href/url()/@import are caught; an `xmlns` SVG namespace, `data:`, `blob:` and `#x` are not. It worked when a mutation (drop the xmlns exclusion) fails a named test.
3. **HTML server.** Add `html_server.go` plus the shell dir, with the shared guard factored out of `server.go`. It worked when `html_server_test.go` pins:
   - API without the token → 401; wrong token → 401;
   - foreign Host → 403 on every route;
   - missing file → `isNew` with null html;
   - POST/PUT → 405;
   - the CSP header on every response, with no `http`/`https` host in it;
   - shell served under `/` and `/<owner>/`, and 404 deeper;
   - `external[]` populated;
   - the embedded shell itself has `ExternalRefs` == empty (the offline check applied to our own page).
   
   All existing `server_test.go` tests stay green, unedited.
4. **CLI resolution.** Add `resolveBoardHTML` and the dispatch in `cmdBoard`. It worked when new `board_test.go` cases pass:
   - bare/`--board`/pointer → `<id>/<slug>/board.html`;
   - explicit live and repo `board.html` paths;
   - `.excalidraw` still resolves through the old path;
   - path plus `--board` → usage;
   - the pointer is written for live and not for repo;
   - resolution never starts the daemon (extend `TestBoardResolutionNeverStartsTheDaemon`).
   
   CI has no harness or network, so tests call the resolver and `runBoard*` seams only, with `--no-open`, the `boardListen` seam and a `t.TempDir()` XDG root. Never exec a browser.
5. **Lifecycle and statusline.** Share the serve lifecycle, so the scene name comes from the resolved slug. It worked when a test shows `boardServerInfo` for an HTML live board carries `scene` = slug, and `boardBlockFor` and `board url` (existing tests `TestBoardURLAlive`, `TestBoardURLBoardFlag`, `TestBoardURLNamesTheOwner`) pass, plus one new case that runs them against an HTML live board's `server.json`.
6. **Promote verb.** Add `cmdBoardPromote`, the registry row and the flag installer. It worked when the CLI tests pass: refusals and exit codes, a successful copy, the peek route (extend `TestBoardIsAPeekVerb` with `board promote`), and the registry parity test.
7. **README.** Update the README and remove the line-291 marker. It worked when `grep -n '^>>>>>>>\|^<<<<<<<' README.md` is empty.
8. **Hand check (not CI; record it in the report).** Write `docs/boards/demo/board.html` in a scratch tree, with inline CSS, SVG and a script that sets some text, and run `relevo board --no-open <path>`. In a browser:
   - the page renders;
   - the board's script runs;
   - `fetch('/api/board')` from inside the iframe fails;
   - a board with `<img src="https://…">` shows the banner and the console shows the CSP block;
   - a missing file shows the new-board message;
   - `relevo status --line` shows the board URL for a live HTML board.
   
   If the blob iframe does not render under the CSP, **halt** and report. Do not loosen the CSP to an external source.
9. **Save this plan.** Save it as `docs/plans/2026-10-03-board-s1-html-preview.md`, in its own new commit at the end.

Focused loop: `go test ./internal/board/ ./cmd/relevo/ -run 'Board|HTML|Promote|External|Shell|Peek|Registry'`. Fix every error before rerunning. Run `make check` once at the end, plus `gofmt -l $(git ls-files '*.go')` locally (dev-run gofmt is vacuous in worktrees). If coverage moves between packages, regenerate it with `sh scripts/check-coverage.sh --write` and say so. Never lower `internal/board` below 81.7.

## What is deleted or changed (closed list)

1. A bare `relevo board`, `--board NAME` and the pointer no longer open `<id>/<name>.excalidraw` in the Excalidraw page. They open `<id>/<name>/board.html`. The old live scene can still be opened by its explicit `.excalidraw` path.
2. `runBoard`'s derivation of the `server.json` scene from the file name (`board.go:473`).
3. The README sentence "Promotion of a live board into the repo is a later cut." and the stray conflict marker at README line 291.
4. The `board` registry summary "open a live or repo Excalidraw whiteboard for a scene" is reworded.

Nothing else is removed: the Excalidraw server, assets, `--theme`, text/annotate/comment/comments and their tests stay as they are.

## Known gap to state in the report

After this slice, `relevo board comment` with no path still resolves the pointer to `<id>/<name>.excalidraw`. So a comment does not land on the HTML board the user is looking at. That belongs to a later #915 slice; open an issue if #915 does not already cover it.

## The report must include

- `git diff --stat` against this plan's seams.
- The test names that pin each item: confinement, token/Host, missing → isNew, offline check, promote, the statusline and `board url` on an HTML board.
- One mutation per guard, with the test that failed: drop the Host check, drop the token compare, drop the xmlns exclusion, drop the symlink resolution in `ResolveLiveHTMLArg`.
- The hand-check results from step 8, including whether the blob iframe rendered under the CSP.
- Whether the coverage baseline was regenerated.
- `make check` output (tail).
- Confirmation that no `go.mod` dependency was added (stdlib only) and no lint, size or comment exclusion was added.
