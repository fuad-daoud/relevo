# Provenance: a module-versioned build from a checkout is a local build, not a go install (#497)

## 1. System Overview

`release.Detect` (`internal/release/provenance.go`) decides how the running
binary was installed. Its rule 1 says that any binary whose version came from
`debug.ReadBuildInfo().Main.Version`, rather than an ldflags stamp
(`Inputs.FromModule`), is `KindGoInstall`. Since Go 1.24 that is wrong for a
plain `go build ./cmd/relevo` or `go install ./cmd/relevo` inside a checkout.
The toolchain stamps such a build with a pseudo-version such as
`v0.13.1-0.20260925140104-af100abf0ec6+dirty`, so `FromModule` is true, and
the binary is misclassified as a go install. Two things go wrong as a result:
- `relevo update` prints a `go install …@latest` command for it, instead of
  refusing it as a local build;
- `relevo doctor` can call it stale.

A build made with `-buildvcs=false`, or outside any VCS, gets `Main.Version ==
"(devel)"`, which is non-empty, so it is also `FromModule` and also misread as
a go install.

The signal that separates them was verified with `go version -m` on go1.27.1:

| build | Main.Version | `vcs.revision` setting |
|---|---|---|
| `go install github.com/fuad-daoud/relevo/cmd/relevo@v0.13.0` (module cache, no .git) | `v0.13.0` | absent |
| `go build ./cmd/relevo` in a checkout | pseudo-version, maybe `+dirty` | present |
| `go build -buildvcs=false ./cmd/relevo` | `(devel)` | absent |
| `make install` (ldflags `-X main.version=…`) | the stamp wins, `FromModule == false` | present, and irrelevant |

The fix:
- `Inputs` gains `VCS bool`, true when the build info carries a
  `vcs.revision` setting;
- `releaseInputs()` fills it from `debug.ReadBuildInfo().Settings`;
- `Detect`'s rule 1 becomes: `FromModule` and not `VCS` and a version other
  than `(devel)` is `KindGoInstall`, and any other `FromModule` binary is
  `KindLocalBuild`.

Nothing else changes. `make install` builds keep their ldflags stamp and are
unaffected.

## 2. File Structure

```
internal/release/provenance.go       MODIFIED  Inputs.VCS; Detect rule 1; new pure helper HasVCSRevision
internal/release/provenance_test.go  MODIFIED  new Detect rows; HasVCSRevision table test
cmd/relevo/main.go                   MODIFIED  releaseInputs (~line 179-196): set in.VCS from the build info settings
docs/plans/2026-09-25-provenance-vcs.md  NEW   this plan, committed with the change
```

## 3. Data Structures & Type Definitions

### `release.Inputs` (existing struct; add one field after `FromModule`)

| field | type | meaning |
|---|---|---|
| `VCS` | `bool` | the build info carries a `vcs.revision` setting: the binary was built from a VCS checkout, never from the module cache. Only meaningful when `FromModule` is true. The zero value means "no VCS stamp" |

Its doc comment states the table in §1 in one or two sentences.

## 4. Interface Definitions & Component Contracts

### `func HasVCSRevision(settings []debug.BuildSetting) bool` (new, `provenance.go`)

- Pure. True if and only if some setting has `Key == "vcs.revision"` and a
  non-empty `Value`.
- It imports `runtime/debug` for the type only, and never calls
  `ReadBuildInfo`.

### `Detect(in Inputs) Kind`: rule 1 replaced

Old:

```
if in.FromModule { return KindGoInstall }
```

New, at the same position, still first:

```
if in.FromModule {
    if in.VCS || in.Version == "(devel)" { return KindLocalBuild }
    return KindGoInstall
}
```

Update the comment above rule 1 to cite #497 and the §1 table in one sentence.
Rules 2 onward are untouched.

### `releaseInputs()` (`cmd/relevo/main.go`, ~179-196)

Inside the existing `if version == ""` block, where `debug.ReadBuildInfo()`
is already called and `in.FromModule = true` is set, also set
`in.VCS = release.HasVCSRevision(info.Settings)`. Read the build info once,
reusing the `info` already in scope. When `version != ""` (a `make install`
stamp), leave `VCS` false, because `FromModule` is false and `VCS` is not
consulted.

## 5. High-Level Pseudocode

```
releaseInputs():
    in := {Version: buildVersion(), Distribution: distribution}
    ... ExeDir unchanged ...
    if version == "":
        if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "":
            in.FromModule = true
            in.VCS = release.HasVCSRevision(info.Settings)
    return in
```

## 6. Error Handling Strategy

None: pure classification. A build info with no settings means `VCS` is
false, which is today's behaviour for a real go install.

## 7. Working Efficiently

- Read in one step, as parallel reads: `internal/release/provenance.go`,
  `internal/release/provenance_test.go`, and `cmd/relevo/main.go` lines
  160-200.
- Make each file's changes in one edit call.
- Focused tests: `go test ./internal/release/ ./internal/doctor/ -count=1`.
- Full check at the end: `make check`.
- The tests are pure table tests, with no network and no harness.

If any step is impossible as written or contradicts the code, stop and report.
Do not bend the plan, or a test, to fit.

## 8. Ordered Implementation Steps

**Step 0: sync and confirm the premises.** Run
`git fetch origin && git merge --ff-only origin/main`. Halt if any of these is
false:
- `Detect` begins with `if in.FromModule { return KindGoInstall }`;
- `releaseInputs` sets `in.FromModule = true` inside
  `if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != ""`;
- `Inputs` has no `VCS` field.

**Step 1: tests first.** In `provenance_test.go`:
- Add these rows to the Detect table:
  1. `"go build in a checkout: module pseudo-version with a VCS stamp is a local build"`:
     `Inputs{Version: "v0.13.1-0.20260925140104-af100abf0ec6+dirty", FromModule: true, VCS: true}`
     gives `KindLocalBuild`.
  2. `"go build in a checkout at a clean tree is still a local build"`:
     `Inputs{Version: "v0.13.1-0.20260925140104-af100abf0ec6", FromModule: true, VCS: true}`
     gives `KindLocalBuild`.
  3. `"module (devel) without VCS (-buildvcs=false) is a local build"`:
     `Inputs{Version: "(devel)", FromModule: true}` gives `KindLocalBuild`.
  4. `"a VCS stamp beats the release distribution stamp too"`:
     `Inputs{Version: "v0.9.0", FromModule: true, VCS: true, Distribution: "release"}`
     gives `KindLocalBuild`.
  5. The existing rows `go install` (`v0.7.0`, `FromModule: true`) and
     `"go install wins over the release stamp"` must stay `KindGoInstall`,
     unchanged. They are the regression guard for the module-cache case.
- Add `TestHasVCSRevision`, a table over:
  - nil gives false;
  - `[{vcs, git}]` gives false;
  - `[{vcs.revision, ""}]` gives false;
  - `[{vcs.revision, "af100ab…"}]` gives true;
  - `[{-ldflags, …}, {vcs.revision, "x"}, {vcs.modified, "true"}]` gives true.

Run the focused tests. They fail to compile (`VCS`, `HasVCSRevision`).
Record that.

**Step 2: implement** §4 in `provenance.go` and `main.go`. Verification: the
focused tests pass.

**Step 3: prove the rows pin the rule.** Temporarily change the new rule-1
condition to `if false && (in.VCS || in.Version == "(devel)")` and confirm
that rows 1-4 FAIL. Revert, and confirm with `git diff` that only the
intended changes remain. If any of rows 1-4 passes under the mutation, halt
and report.

**Step 4: smoke on this machine**, not in CI. In a temp dir `$T`:
1. `go build -o $T/local ./cmd/relevo`, then `$T/local update --check`. The
   first line must read `running  <pseudo-version> (local-build)`, and the
   action must be `refuse`.
2. `go build -buildvcs=false -o $T/novcs ./cmd/relevo`, then
   `$T/novcs update --check`. It must show `((devel)` … `local-build)` and
   the action `refuse`.
3. `go version -m $T/local | grep vcs.revision` must print one line.

Paste all three outputs into the report.

**Step 5: full check and ship.** `make check` must pass. Copy this plan to
`docs/plans/2026-09-25-provenance-vcs.md`. Commit as
`fix(release): a module-versioned build from a checkout is a local build, not a go install (#497)`,
with `Fixes #497` in the body. Push, and open a PR against `main` with the
same title and `Fixes #497` in the body. Don't wait for CI and don't merge.

The report states the step 1 failure, the step 3 mutation failures, the step
4 outputs, the `make check` result, the PR number, and
`git diff --stat origin/main`.
