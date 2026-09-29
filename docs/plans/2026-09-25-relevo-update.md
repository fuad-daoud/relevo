# `relevo update`: a release binary updates itself (#293)

## 1. System Overview

relevo can already tell that it is out of date. `internal/release` classifies
the install (`Detect` -> `KindRelease`, `KindGoInstall`, `KindLocalBuild` or
`KindUnknown`), fetches the latest tag (`Fetcher`), compares versions
(`NewerStrings`) and builds the asset URLs (`AssetURLs`). `relevo doctor`
prints the manual update steps. What it cannot do is update itself. This plan
adds a verb:

```
relevo update [--check] [--to vX.Y.Z] [--release]
```

- **Release binary (`KindRelease`):** resolve the target tag (the latest, or
  `--to`). Download `relevo_<tag>_<goos>_<goarch>.tar.gz` and `checksums.txt`
  from the fixed GitHub download base. Verify the archive's SHA-256 against its
  line in `checksums.txt`. Extract the single `relevo` file into a temp file
  beside the running executable. Preflight the candidate: its `version` must
  print the target tag, and its `daemon --preflight` must exit 0. Then
  atomically `rename` it over the running executable.
- **Nothing more is needed after the swap.** A running daemon already watches
  its executable, preflights a replacement and re-execs into it (#371,
  `internal/upgrade`). On every start it also refreshes the shipped agent
  definitions (`refreshRoles`, cmd/relevo/main.go ~3025). Builders run in their
  own scopes, so a round in flight is not interrupted. `relevo update` does not
  restart anything. It says what will happen.
- **`go install` binary:** print the `go install …@<tag>` command and exit 0.
  Nothing runs.
- **Local or unknown build:** refuse with exit 1 and an explanation, unless
  `--release` is given. `--release` on purpose replaces the binary with a
  release binary, which turns the install into a release install.
- **`--check`:** print the decision (running version, install kind, target
  and action) and change nothing.

Decisions already made by the user:
- A local build is refused by default, and `--release` converts it.
- Verification is SHA-256 against `checksums.txt` only, with no signature.
- `go install` prints the command rather than running it.

Out of scope:
- signatures;
- Windows;
- updating a remote `relevo serve` host;
- touching the release-check cache;
- any change to `release.yml`.

## 2. File Structure

```
internal/release/update.go          NEW       DecideUpdate: the pure decision (kind x versions x flags -> action)
internal/release/update_test.go     NEW       table test of DecideUpdate
internal/release/download.go        NEW       Downloader: fetch + verify + extract the binary into a temp file
internal/release/download_test.go   NEW       httptest-only tests of Downloader
internal/release/assets.go          MODIFIED  AssetURLs delegates to new assetURLsFrom(base, …); DownloadBase comment updated
cmd/relevo/update.go                NEW       cmdUpdate: gathers inputs, runs the decision, downloads, preflights, swaps
cmd/relevo/main.go                  MODIFIED  `case "update":` in the verb switch (~line 336-384); one usage line
cmd/relevo/main_test.go             MODIFIED  one test: help lists update; `update -h` prints its usage
internal/doctor/doctor.go           MODIFIED  releaseFix (~549-557): KindRelease names `relevo update` first
internal/doctor/doctor_test.go      MODIFIED  port the two KindRelease expectations (~1091-1097, ~1237)
README.md                           MODIFIED  Install section (~47-51) and Upgrading section (~80-92)
docs/plans/2026-09-25-relevo-update.md NEW    this plan, committed with the change
```

## 3. Data Structures & Type Definitions

All of these are in package `release`.

### `UpdateAction` (int enum, `update.go`)

| value | meaning |
|---|---|
| `UpdateCurrent` | nothing to do: the running version is already the target |
| `UpdateReplace` | download the target and replace the running binary |
| `UpdatePrintGoInstall` | a `go install` binary: print the command, exit 0 |
| `UpdateRefuse` | a local or unknown build without `--release`: exit 1 |
| `UpdateInvalid` | a bad `--to` value: exit 2 (a usage error) |

Give it a `String()` returning `current`, `replace`, `go-install`, `refuse`
and `invalid`, used in `--check` output.

### `UpdateRequest` (struct, `update.go`)

| field | type | meaning / constraint |
|---|---|---|
| `Kind` | `Kind` | from `Detect(releaseInputs())` |
| `Running` | `string` | `buildVersion()`; may be `(devel)` or a describe string |
| `Latest` | `string` | the latest tag from the Fetcher; `""` when not fetched (see §5, the fetch is skipped when it is not needed) |
| `To` | `string` | the `--to` value; `""` when not given. Accepted with or without a leading `v` |
| `ForceRelease` | `bool` | `--release` |

### `UpdateDecision` (struct, `update.go`)

| field | type | meaning |
|---|---|---|
| `Action` | `UpdateAction` | as above |
| `Target` | `string` | the normalised tag (`vX.Y.Z`) to install or print. `""` for `UpdateRefuse` without a target and for `UpdateInvalid` |
| `Message` | `string` | one human sentence, printed by the CLI as is. Exact texts are in §4 |

### `Downloader` (struct, `download.go`)

| field | type | meaning |
|---|---|---|
| `Base` | `string` | download base URL. The CLI always passes `DownloadBase` and tests pass an httptest URL. There is no env override (see §6) |
| `Client` | `*http.Client` | nil -> `&http.Client{Timeout: DownloadTimeout}` |

Package constants in `download.go`:
- `DownloadTimeout = 5 * time.Minute`, the budget for one whole HTTP request.
- `maxChecksums = 1 << 20`.
- `maxArchive = 256 << 20`.
- `maxBinary = 256 << 20`.

Sentinel errors in `download.go`, each wrapped with context by the caller
side:
- `ErrNoChecksum` (no line for the archive in checksums.txt, or more than one);
- `ErrChecksumMismatch`;
- `ErrNoBinary` (no regular file named `relevo` at the archive root);
- `ErrTooLarge`.

A transport failure wraps the existing `ErrOffline`. A non-200 status is a
plain wrapped error naming the URL and the status.

## 4. Interface Definitions & Component Contracts

### `func DecideUpdate(req UpdateRequest) UpdateDecision` (`update.go`, pure)

The rules are ordered, and the first match wins:

1. `To != ""`: normalise it (add `v` if missing). It must `ParseVersion` with
   `Suffix == ""`. Otherwise it is `UpdateInvalid`, with Message
   `--to must be a release tag like v0.13.0, got "<To>"`. The target is the
   normalised To.
   `To == ""`: the target is `Latest`. It may be `""` only for rules 2 and
   3a, which do not need it.
2. `Kind == KindGoInstall`: `UpdatePrintGoInstall`. Target = the target, or
   the literal `latest` when the target is `""`. Message
   `go install github.com/fuad-daoud/relevo/cmd/relevo@<Target>`.
3. `Kind == KindLocalBuild || Kind == KindUnknown`:
   a. `!ForceRelease`: `UpdateRefuse`, Target `""`, Message
      `relevo <Running> is a local build; relevo update replaces only release binaries. relevo update --release replaces it with the latest release binary.`
   b. `ForceRelease`: `UpdateReplace` to the target, whatever the version
      comparison says. The user asked to convert.
4. `Kind == KindRelease`:
   - With `To` given: `UpdateCurrent` when `ParseVersion(Running)` equals
     `ParseVersion(target)`, comparing numbers and suffix. Otherwise
     `UpdateReplace`. A downgrade is allowed only through an explicit `--to`.
   - Without `To`: `UpdateReplace` when `NewerStrings(Running, target)`,
     otherwise `UpdateCurrent`.
   - The `UpdateCurrent` Message is `relevo <Running> is current (latest <target>)`.
   - The `UpdateReplace` Message is `relevo <Running> -> <target>`.
5. Any other Kind value: `UpdateRefuse`, with the 3a message.

Precondition for rules 3b and 4: the target is non-empty. The CLI guarantees
this by fetching `Latest` before calling whenever `To == ""` and the kind is
not `KindGoInstall`, and not (local/unknown and not `ForceRelease`). If the
target is still `""` there, return `UpdateRefuse` with Message
`no release tag to update to`. That is defensive and never expected.

### `func (d *Downloader) FetchBinary(ctx context.Context, tag, goos, goarch, destDir string) (string, error)` (`download.go`)

- Responsibility: produce a verified, executable `relevo` binary for `tag` as
  a temp file in `destDir`, and return its path.
- Steps:
  1. Build the archive and checksums URLs with `assetURLsFrom(d.Base, tag, goos, goarch)`.
  2. GET checksums.txt (at most `maxChecksums`). Find the line whose second
     field, after splitting on whitespace, equals the archive's base name
     `relevo_<tag>_<goos>_<goarch>.tar.gz`. The first field is 64 lowercase
     hex characters. Zero or several matching lines is `ErrNoChecksum`.
  3. GET the archive into memory through an `io.LimitReader` of
     `maxArchive+1`. More than `maxArchive` bytes is `ErrTooLarge`. SHA-256 it
     and compare against step 2 with `subtle.ConstantTimeCompare` or a plain
     equality on the hex; either is fine. A mismatch is `ErrChecksumMismatch`.
     Nothing is written to disk before this check passes.
  4. gunzip + tar-read the verified bytes. The first header whose
     `path.Clean(Name) == "relevo"` and whose `Typeflag == tar.TypeReg` is the
     binary. Every other entry is skipped, and no entry name ever becomes a
     filesystem path. If none is found, it is `ErrNoBinary`. More than
     `maxBinary` bytes is `ErrTooLarge`.
  5. `os.CreateTemp(destDir, ".relevo-update-*")`, write the binary, `Sync`,
     `Close`, `Chmod 0o755`. Return the path.
- Postcondition on error: no temp file is left in `destDir`. Remove it on
  every failure after step 5 begins.
- Requests send no auth header, and no retries.

### `func assetURLsFrom(base, tag, goos, goarch string) (archive, checksums string)` (`assets.go`)

This is today's `AssetURLs` body with `DownloadBase` replaced by `base`.
`AssetURLs` becomes a one-line call to `assetURLsFrom(DownloadBase, …)`. The
`DownloadBase` comment changes from "nothing downloads from it, it is only
printed" to say that `relevo update` downloads from it, and that it stays a
constant so that no environment variable can redirect a binary download.

### `func cmdUpdate(args []string) error` (`cmd/relevo/update.go`)

- Flags:
  - `--check` (bool): "print what update would do; change nothing";
  - `--to` (string): "install this release tag instead of the latest; allows a downgrade";
  - `--release` (bool): "replace a local build with the release binary".
- Usage line: `usage: relevo update [--check] [--to vX.Y.Z] [--release]`.
- Exit codes:
  - 0: current, replaced, go-install printed, or `--check`;
  - 1: refused, or any download/verify/preflight/swap error;
  - 2: `UpdateInvalid` or a flag error.

  Use the existing `exitCodeErr{code: N}` pattern, with the message on stderr
  for 1 and 2.
- The whole flow is in §5.

### `cmd/relevo/main.go`

- In the verb switch (~336-384), add `case "update": return cmdUpdate(args[1:])`
  after the `"doctor"` case.
- In `usage` (~61), add a line after `doctor`:
  `  update    replace this release binary with the latest release, checksum-verified [--check] [--to vX.Y.Z] [--release]`

### `internal/doctor/doctor.go` `releaseFix` (~549-557)

The `KindRelease` case returns
`relevo update (or download <archive>, check it against <checksums>, and replace this relevo binary with the one inside)`.
The `KindGoInstall` case and the default are unchanged. Port the two
expectations in `doctor_test.go` (`releaseArchiveFix` helper ~1091-1097, and
the literal `want` ~1237) to the new text. That is an assertion change only,
and no test is deleted.

## 5. High-Level Pseudocode

```
cmdUpdate(args):
    parse flags (exit 2 on error; -h prints usage, exit 0 per parseFlags)
    if positional args remain: usage error, exit 2
    in   := releaseInputs(); kind := release.Detect(in); running := in.Version
    exe, err := upgrade.ResolveExe()            -- symlinks resolved; error -> exit 1 "cannot resolve the running executable: …"

    needLatest := *to == "" && kind != KindGoInstall && !((kind == KindLocalBuild || kind == KindUnknown) && !*release)
    latest := ""
    if needLatest:
        ctx5s; latest, err = release.NewHTTPFetcher("", 0).Latest(ctx)
        err -> exit 1 "cannot learn the latest release: <err>"

    dec := release.DecideUpdate({kind, running, latest, *to, *release})

    if *check:
        print "running  <running> (<kind>)"
        print "exe      <exe>"
        print "action   <dec.Action>"
        print "         <dec.Message>"
        return exit 0
    switch dec.Action:
        UpdateInvalid        -> stderr Message, exit 2
        UpdateRefuse         -> stderr Message, exit 1
        UpdateCurrent        -> stdout Message, exit 0
        UpdatePrintGoInstall -> stdout "relevo was installed with go install; run:" then "  " + Message, exit 0
        UpdateReplace        -> continue

    print "relevo <running> -> <target>: downloading <archive base name>"
    ctx5m; tmp, err := (&release.Downloader{Base: release.DownloadBase}).FetchBinary(ctx, target, runtime.GOOS, runtime.GOARCH, filepath.Dir(exe))
        err -> exit 1 "update failed: <err>"; a permission error on the dir adds
               " (relevo update replaces the binary in place and needs write access to <dir>)"
    defer: if tmp still exists (swap did not happen) remove it

    preflight (each with a context of upgrade.PreflightTimeout):
        out := run tmp "version"            -- must equal "relevo <target>" after TrimSpace
        run tmp "daemon" "--preflight"      -- must exit 0
        any failure -> exit 1 "the downloaded <target> failed its preflight: <first line of output or err>; <exe> is unchanged"

    os.Rename(tmp, exe)                     -- same directory, so atomic
        err -> exit 1 "cannot replace <exe>: <err>"

    print "relevo updated to <target> (<exe>)"
    if kind != KindRelease: print "this install is now a release binary; relevo update keeps it current"
    print "A running daemon moves onto it by itself within a few seconds; rounds in flight keep running. relevo doctor shows what the daemon runs."
    exit 0
```

Children are started with `exec.CommandContext`, stdin nil, and the output is
captured. They run only in the `UpdateReplace` path.

## 6. Error Handling Strategy

- Every failure before the rename leaves the running binary untouched, and
  the message says so for preflight failures. The temp file is always
  removed.
- Download errors are not retried. They are reported with the URL and status,
  and the user re-runs the command.
- The integrity guard is the checksum, verified before anything touches disk.
  Neither the download base nor the checksum source can be redirected by the
  environment. `RELEVO_RELEASE_API`, which already exists, can redirect only
  the *latest-tag lookup*, so at worst it picks which genuine release tag
  gets downloaded. Keep it that way.
- No logging through slog: it is a one-shot CLI, and stdout/stderr are the
  record.

## 7. Working Efficiently

Each model step costs a round trip, so:

- Batch the reads into one step, as parallel reads:
  - `internal/release/{assets,fetch,provenance,version}.go` and `internal/release/fetch_test.go`;
  - `internal/upgrade/exe.go`;
  - `cmd/relevo/main.go` lines 50-120 (the stamps and usage), 160-215
    (buildVersion, releaseInputs, statusNotice) and 330-390 (the verb switch);
  - `cmd/relevo/main_test.go` around `TestHelpListsServeVerbs` (~140);
  - `internal/doctor/doctor.go` lines 520-560 and `internal/doctor/doctor_test.go` lines 1085-1100 and 1225-1275;
  - `README.md` lines 30-95;
  - one existing `cmd/relevo` verb that uses `exitCodeErr` (grep `exitCodeErr{code: 2}`).
- Create each new file in one write, and make each file's modifications in one
  edit call.
- Focused tests:
  - `go test ./internal/release/ ./internal/doctor/ -count=1`
  - `go test ./cmd/relevo/ -run 'Update|Help' -count=1`
- Full check once at the end: `make check`.
- CI has no harness binary and no network. Every test in this plan is
  httptest-only or a pure function. The cmd/relevo test may run only `update
  -h` and help. It must not run `cmdUpdate` down any path that fetches,
  downloads or spawns (see CLAUDE.md, "Merging and CI").

If any step is impossible as written or contradicts the code, stop and report.
Do not bend the plan, or a test, to fit.

## 8. Ordered Implementation Steps

**Step 0: sync and confirm the premises.** Run
`git fetch origin && git merge --ff-only origin/main`. Halt if any of these
does not hold:
- `internal/release/assets.go` has `AssetURLs` building from `DownloadBase`;
- `release.Detect` and the four Kind constants exist as described in §1;
- `upgrade.ResolveExe()` exists;
- `cmdDaemon` accepts `--preflight` and prints `ok <version>`;
- `releaseFix` in `internal/doctor/doctor.go` has a `KindRelease` case;
- no `"update"` verb or `removedVerbs["update"]` entry exists yet.

**Step 1: DecideUpdate, tests first.** Write `update_test.go`, a table over
every rule in §4:
- go-install with and without `--to`;
- local and unknown, each with and without `--release`;
- release: newer latest, equal, older latest, `--to` newer, `--to` older (a
  downgrade replaces), `--to` equal (current);
- `--to` without a `v`, `--to` with a suffix (invalid) and `--to` garbage
  (invalid);
- a describe-suffixed running version on a release kind with an equal latest
  (current).

Assert Action, Target and the exact Message. Run it and watch it fail to
compile or fail. Then write `update.go` until it passes.

**Step 2: Downloader, tests first.** Write `download_test.go` with an
httptest server serving
`/<tag>/relevo_<tag>_linux_amd64.tar.gz` and `/<tag>/checksums.txt`. The
test builds the tar.gz in memory: `relevo` holding the bytes
`#!/bin/sh\necho fake\n`, plus `README.md`. Cases:
1. Happy path: the returned file sits in `destDir`, has mode 0755, and has
   exactly the `relevo` bytes.
2. A checksum mismatch returns `ErrChecksumMismatch`, and `destDir` is empty
   afterwards.
3. A checksums.txt without the line returns `ErrNoChecksum`.
4. An archive without `relevo` returns `ErrNoBinary`.
5. An archive whose only entry is `../relevo` or `sub/relevo` returns
   `ErrNoBinary`. This pins the root-only rule.
6. A 404 on the archive returns an error naming the status, and `destDir` is
   empty.
7. A symlink entry named `relevo` returns `ErrNoBinary`.

Implement `download.go`, and refactor `assets.go` per §4.
`TestAssetURLs*` in `assets_test.go` must still pass unchanged.

**Step 3: the verb.** Write `cmd/relevo/update.go` per §5. Wire the switch
and the usage line in `main.go`. Add `TestUpdateHelp` to `main_test.go`: the
`usage` constant contains `update` and `--release`, and running the CLI entry
with `update -h` exits 0 and prints the usage line. Follow how
`TestDiffHelp` / `TestAddHelp` invoke a verb's `-h`.

**Step 4: doctor and README.** Change `releaseFix` and port its two test
expectations (§4). In README:
- In the Install paragraph (~47-51), add that a release binary updates itself
  with `relevo update` (`--check` to see what it would do), that a `go
  install` gets the command printed, and that a local build is left alone
  unless `relevo update --release` converts it.
- In Upgrading (~80), add that `relevo update` lands the new binary the same
  atomic way, so the daemon follows it on its own.

**Step 5: focused tests, then the full check.** Run the focused tests (§7),
then `make check`. Both must pass.

**Step 6: manual smoke against the real release.** This runs on this machine,
not in CI. In a temp dir `$T`:
1. Build a fake old release binary:
   `go build -ldflags "-X main.version=v0.12.0 -X main.distribution=release" -o $T/relevo ./cmd/relevo`.
2. Run `$T/relevo update --check`. It must say action `replace` and
   `relevo v0.12.0 -> v0.13.0`, or the current latest.
3. Run `$T/relevo update`. It must exit 0, and `$T/relevo version` must then
   print the latest tag.
4. Run `$T/relevo update` again. It must say `is current` and exit 0.
5. Build a plain `go build -o $T/local ./cmd/relevo` and run `$T/local update`.
   It must exit 1 with the local-build message, or with the go-install
   message if the toolchain stamped a module version. Record which in the
   report.

This never touches `~/.local/bin/relevo` or the running daemon. Paste the four
outputs into the report. If step 3 fails at the preflight, report the output
and halt. Do not weaken the preflight.

**Step 7: ship.** Copy this plan to `docs/plans/2026-09-25-relevo-update.md`.
Commit everything as one commit,
`feat(update): relevo update replaces a release binary with a checksum-verified release (#293)`,
with `Fixes #293` in the body. Push, and open a PR against `main` with the
same title, whose body summarises §1 and includes `Fixes #293`. Don't wait for
CI and don't merge.

The report states the step 1 and step 2 initial failures, the focused and full
check results, the step 6 outputs, the PR number, and `git diff --stat
origin/main`.

## Round 2: strict tags and the one-checksum-line rule

### 1. System Overview

Round 1 (commit 38419ac on `relevo/update293`, PR #495) is verified except
for two gaps.

**Gap A: the latest tag is not validated before it is used in a URL.**
`DecideUpdate` (`internal/release/update.go`) checks a `--to` value with
`ParseVersion` + `Suffix == ""`, but it uses `req.Latest` as the target with
no check at all. `FetchBinary` then builds URLs from it with
`assetURLsFrom(base, tag, …)`, as `base + "/" + tag + "/relevo_" + tag + …`.
`ParseVersion` accepts any suffix after `-`, so a latest tag such as
`v9.9.9-x/../../../other/repo/releases/download/v1` would be treated as newer.
It would then walk the download path to another repository's release on
github.com, with that release's own checksums.txt. The tag comes from the
GitHub API, or from `RELEVO_RELEASE_API`, which is local. So this is defence
in depth, but round 1's own plan (§6) promised that the tag lookup can at
worst pick a *genuine relevo release tag*. `AssetURLs`'s doc comment says the
same thing ("callers pass only a tag ParseVersion accepted"). The code does not
keep that promise.

Fix: one strict predicate, `IsReleaseTag(s string) bool`, true exactly when
`s` matches `^v[0-9]+\.[0-9]+\.[0-9]+$`, checked byte by byte with no regexp
package needed. `DecideUpdate` requires the target, from either source, to
pass it before any `UpdateReplace`. `FetchBinary` also refuses a tag that fails
it, as a second guard at the point of use.

**Gap B: the "exactly one line" checksum rule is untested.** The planner
mutated `fetchChecksum`'s `if matches != 1 || !isLowerHex64(sum)` to
`if matches == 0`, and every test still passed. There is no test for a
checksums.txt that names the archive twice, or whose hash is not 64 lowercase
hex characters.

No other behaviour changes.

> Round 1 step 6.4 could not pass as written: after 6.3 the file is the
> published v0.13.0 binary, which predates `relevo update`. The
> "is current" path is covered by the `DecideUpdate` table instead.

