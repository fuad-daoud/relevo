# Plan: `scripts/test-shard.sh` must reach awk without a newline in `-v` (issue #580)

Facts below are verified against `0c8da537` (this worktree's HEAD, cut from origin/main).
If the text at a named line does not match what this plan quotes, **halt and report** --
do not adapt the plan to a moved line.

## 1. System overview

`scripts/test-shard.sh` splits the repo's tests over N shards. It resolves `SPLIT_PKGS`
(the packages whose individual test functions are sharded) into a newline-separated list
and hands that list to awk as a `-v` assignment (`scripts/test-shard.sh:83`,
`-v sp="$split"`). GNU awk accepts a newline inside a `-v` value; BSD awk -- the
`/usr/bin/awk` on macOS -- rejects it:

```
awk: newline in string github.com/fuad-daou... at source line 1
##[error]Process completed with exit code 2.
```

With the default `SPLIT_PKGS=./internal/relevo` (`scripts/test-shard.sh:70`) the list is one
path with no newline, so CI is green today and the bug is latent. The moment a second
package joins the list (or one pattern matches several packages), every macOS shard dies
before running a single test, instead of failing on a test result.

**The premise holds on current main.** The construct is at `scripts/test-shard.sh:83`; it
is not already fixed. Reproduced on Linux by standing a BSD-awk shim in `PATH`: with
`SPLIT_PKGS="./internal/relevo ./internal/delivery"` the old construct exits 2 with
`awk: newline in string sp=...`, and with the fix below it exits 0.

**The fix.** The resolved list goes to a file under `$work` and awk reads it with `getline`
in `BEGIN`. No newline ever reaches `-v`; the `-v` assignments that remain (`idx`, `total`,
`skip_file`) carry digits or a `mktemp -d` path. This is portable by construction:
`getline line < file` in `BEGIN` is plain POSIX awk, implemented the same way by GNU awk and
by macOS's BSD awk, whereas a newline inside a `-v` value is precisely where the two differ.
The fix does **not** change the assignment: under GNU awk the old and the new script emit
byte-identical `--dry-run` output (verified: 1013 lines each, TOTAL=3 with the two packages
above, `diff` empty).

**Does a macOS CI leg exercise this path? Yes.**
- `.github/workflows/ci.yml:189-243` is the `test-macos` job: `runs-on: macos-latest`,
  matrix `go: ['1.25', 'stable']` x `shard: [0, 1, 2]` (`:204-219`).
- `:227-228` runs `sh scripts/test-shard.sh ${{ matrix.shard }} 3 .shard` on every macOS
  leg -- that is the fixed script running under real BSD awk.
- `:241-243` runs `make check-scripts` on shard 0 only; `Makefile:57` is
  `for t in scripts/*_test.sh; do echo "==> $$t"; sh "$$t" || exit 1; done`, so the
  regression test added here runs on macOS on shard 0 of each macOS leg.
- On `pull_request` the matrix excludes `go: '1.25'` (`ci.yml:218-219`), so a PR still runs
  macOS go stable x 3 shards. On push to main both Go versions run.
- The `changes` gate does not skip any of it: `scripts/ci-code-changed.sh:6-13` treats only
  `docs/plans/`, `docs/specs/` and `docs/superpowers/` as docs-only, and this change touches
  `scripts/`.

**The regression test.** A BSD-awk shim placed first in `PATH` rejects exactly what macOS
awk rejects -- a newline inside a `-v` assignment value -- and passes every other argument
to the real awk. `test-shard_test.sh` then runs `--dry-run` with `SPLIT_PKGS` naming two
packages through the shim. On Linux this fails under the old construct (verified: 4 named
failures, `EXIT=1`) and passes under the fix (verified: `test-shard: ok`, `EXIT=0`). The
shim also self-checks, so it can never pass vacuously.

## 2. File structure

```
scripts/test-shard.sh          modified: the resolved split list reaches awk through
                               $work/split.txt instead of -v (lines 82-98 after the edit)
scripts/test-shard_test.sh     modified: BSD-awk shim + a two-package case, inserted at
                               line 89 (after the TOTAL loop, before "Bad usage")
docs/plans/2026-09-26-test-shard-bsd-awk.md
                               new: this plan, saved verbatim
```

Expected paths touched: exactly those three. Anything else: halt and report.

## 3. Data structures and contracts

**`SPLIT_PKGS`** (environment variable; `scripts/test-shard.sh:9-11`, read at `:70`)
- Type: space-separated list of `go list` import-path patterns. Default `./internal/relevo`.
- It is word-split on purpose (`:79` has `# shellcheck disable=SC2086`).

**The resolved split list** (`split`, `scripts/test-shard.sh:80`)
- One import path per line, in the order `go list` prints, already de-duplicated. A single
  pattern may resolve to several paths, which is the second way the list grows a newline.
- Invariant used later: an import path contains no whitespace, so `for s in $split` (`:99`)
  splits it correctly and a path is never ambiguous.

**`$work/split.txt`** (new file; `$work` from `mktemp -d` at `:72`, removed by the EXIT trap
at `:75`)
- Content: the resolved split list, one path per line, `printf '%s\n' "$split"` -- exactly
  one trailing newline, and one empty line when the list is empty.
- Reader contract: the awk `BEGIN` block ignores empty lines, so an empty list means "skip
  nothing", as before.

**Assignment lines** (the script's output contract, unchanged)
- `pkg <import path>` for a package assigned whole, `test <import path> <TestName>` for a
  test function of a split package.
- `--dry-run` prints them `LC_ALL=C` sorted (`:126`); the script's caller compares them.
- `--dry-run 0 1` is the reference partition (see `scripts/test-shard_test.sh:40-47`).

**The awk shim** (new, inside the test's `$work/bin`, never committed as a script file)
- A program named `awk` on `PATH`. For every argument: if the previous argument was `-v`, or
  the argument itself matches `-v?*=*`, and the value contains a newline, it writes
  `awk: newline in string <arg>` to stderr and exits 2 (BSD awk's behaviour and exit code).
- Otherwise it `exec`s the real awk, whose absolute path the test exports as `real_awk`
  (`command -v awk` before the shim is on `PATH`; an absolute path prevents a recursive
  exec). Everything non-`-v` is passed through untouched, so multi-line awk programs still
  work.

## 4. Interface definitions and component contracts

**`scripts/test-shard.sh`** -- unchanged interface.
- `test-shard.sh [--dry-run] INDEX TOTAL [OUTDIR]`; exit 0 when every `go test` it ran
  passed, 1 when any failed, 2 on bad usage; `--dry-run` needs only `go list` and the
  package sources.
- Dependency: `go list`, POSIX awk, `mktemp`, `grep`, `sed`, `sort`, `cmp`.
- Postcondition of the changed block: `$work/pkgs.txt` holds exactly `go list ./...` minus
  the split paths, in `go list` order, for this shard -- identical to the old construct's.
- Error contract: any failure inside `BEGIN` or an unreadable `skip_file` leaves the shard
  script's `set -e` to abort -- it must never exit 0 with an empty or partial skip list. The
  file is written 4 lines above the awk call, so the only way it is unreadable is a broken
  `$work`, which is not a case to code for.

**`scripts/test-shard_test.sh`** -- the test contract (untouched helpers, one new one).
- `dry_run INDEX TOTAL`: runs `--dry-run` from the repo root with the caller's environment;
  exit status in `$status`, output in `$work/out`. Untouched.
- `dry_run_split INDEX TOTAL` (new): the same, with `PATH="$work/bin:$PATH"` and
  `SPLIT_PKGS="./internal/relevo ./internal/delivery"`. Same contract.
- Failure contract: a failed assertion echoes `FAIL: <what> (got, want)` and sets `fail=1`;
  the file prints `test-shard: ok` and exits 0 only when `fail` is still 0. Do not weaken it.
- Dependency: `go list`, the packages `./internal/relevo` and `./internal/delivery` (both
  exist; by the script's own extraction they hold 879 and 90 test functions), `awk`, and
  `chmod +x` (the shim runs from its shebang `#!/bin/sh`).

## 5. High-level pseudocode

**Fixed `scripts/test-shard.sh` block**

```
read SPLIT_PKGS                       # space-separated patterns, default ./internal/relevo
split := for each pattern: go list pattern          # newline-separated, de-duplicated
write split to $work/split.txt                       # the only transport for the list

go list ./... | awk -v skip_file=$work/split.txt -v idx=INDEX -v total=TOTAL '
  BEGIN:
    while (getline line < skip_file) > 0:
      if line != "": skip[line] = 1                  # empty lines ignored
  for each input line:
    if line not in skip: if (k++ % total == idx) print line
' > $work/pkgs.txt
```

**New test block** (after the existing partition loop, before "Bad usage")

```
mkdir $work/bin; real_awk := command -v awk; export real_awk
write $work/bin/awk                                # the BSD-awk stand-in (section 3)
chmod +x $work/bin/awk

# the shim must reject a newline in -v, or the cases below pin nothing
if PATH=$work/bin:$PATH awk -v x="$(printf 'a\nb')" 'BEGIN{print 1}' </dev/null:
    FAIL "the BSD-awk shim accepted a newline in -v, so it pins nothing"

# reference: SPLIT_PKGS naming two packages, TOTAL=1
dry_run_split 0 1
if status != 0: FAIL "SPLIT_PKGS naming two packages, TOTAL=1 exits status, want 0"
else:
    sort -u out > two-all.txt
    for dir in internal/relevo internal/delivery:
        FAIL if a `pkg .../dir` line exists      # the package was not split
        FAIL if no `test .../dir ` line exists   # it contributed no tests

# CI's shape: TOTAL=3 over the same two packages, under the shim
two-union := empty; bad := 0
for index in 0 1 2:
    dry_run_split index 3
    if status != 0: FAIL "…TOTAL=3 INDEX=index exits status, want 0"; bad := 1
    else: append out to two-union
if bad == 0:                                    # a failed run says nothing about the split
    FAIL if sort two-union has duplicate lines  # shards must be disjoint
    FAIL if sort -u two-union != two-all.txt    # and cover the TOTAL=1 assignment
```

## 6. Error handling strategy

- **BSD-awk rejection (the bug)**: awk exits 2 mid-pipeline; `set -eu` (`:22`) aborts
  `test-shard.sh` before it starts a single `go test`, so the CI job shows awk's message and
  a red shard. Non-recoverable by design -- silence would be worse. The fix removes the
  cause rather than tolerating it.
- **Write-through-a-file failure modes**: `$work` is created 10 lines above and removed only
  by the EXIT trap, so `printf > $work/split.txt` cannot fail on a live `mktemp -d`; if it
  did, `set -e` aborts with the shell's own message. An unreadable `skip_file` in awk would
  skip nothing (`getline` returns -1, the loop body never runs) -- the same failure shape as
  the old empty `-v`, and not a case to add code for.
- **Test failures**: all assertions accumulate in `fail`; the file exits 1 if anything
  failed, 0 with `test-shard: ok` otherwise. The shim's self-check is the guard against a
  vacuous pass -- if the shim ever stops rejecting a newline in `-v`, that assertion fails
  even though the two-package cases would then be green. The `bad` flag keeps a red shard
  run from producing two misleading extra "shards overlap"/"do not cover" messages.
- **Recoverable vs not for the builder**: a red `sh scripts/test-shard_test.sh` after the
  fix, a shellcheck finding, or a `make check` failure is a halt-and-report. Never relax an
  assertion, never add a lint/size/coverage exclusion, never widen `$work/bin` into the real
  `PATH` of another test to get green.
- **Observability**: nothing new to log. Failure text is the assertion line, which names the
  case, the index and the expected status.

## 7. Working efficiently

- The facts above already name every file and line: no searching. Read only
  `scripts/test-shard_test.sh` around lines 86-96 (the insertion anchor) if you want the text
  in the editor; everything else is quoted here.
- Batch independent commands into one step: the two edits are independent, the shim
  self-check and shellcheck are independent.
- One edit per file, applied as one call, with the exact old text quoted in step 1 for
  `scripts/test-shard.sh` and the exact block quoted in step 2 for `scripts/test-shard_test.sh`.
  Both files indent with **tabs**; copy the fenced blocks verbatim, tabs included.
- Focused build/test command (about 5 s, prints `test-shard: ok`):
  `sh scripts/test-shard_test.sh`
- Lint the two files: `shellcheck scripts/test-shard.sh scripts/test-shard_test.sh`
- Full check, once at the end: `make check` (it runs `check-scripts` -> shellcheck +
  every `scripts/*_test.sh`, then `go test -race -count=1 -cover ./...`).
- Also required: `gofmt -l .` (no output) and `sh scripts/check-comments.sh`
  (prints `check-comments: ok`). Neither is affected by a shell-only change, but the task
  requires them green.
- Do **not** run `test-shard.sh` without `--dry-run`: that runs race tests over the whole
  package list and is what CI shards are for. Every verification here uses `--dry-run`.

## 8. Ordered implementation steps

### Step 1 -- fix the transport in `scripts/test-shard.sh`

File: `scripts/test-shard.sh`. Replace lines 82-93 (the block quoted below) with lines 82-98
of the new text. `split=$(...)` at line 80 and everything from `' > "$work/pkgs.txt"` on stay
as they are; no other line of the file changes.

Current text, `scripts/test-shard.sh:82-93` (verified against `0c8da537`; the line numbers
are the file's own):

```sh
# Whole packages: go list ./... minus the split ones, in go list order.
go list ./... | awk -v sp="$split" -v idx="$index" -v total="$total" '
	BEGIN {
		n = split(sp, s, "\n")
		for (i = 1; i <= n; i++) {
			if (s[i] != "") skip[s[i]] = 1
		}
	}
	!($0 in skip) {
		if (k++ % total == idx) print
	}
' > "$work/pkgs.txt"
```

New text, replacing it (`scripts/test-shard.sh:82-98` after the edit):

```sh
# The resolved paths reach awk through a file, never through -v: with two split
# packages the list holds a newline, and while GNU awk accepts a newline inside
# a -v assignment, the BSD awk on macOS rejects it. The file lives under $work,
# which the EXIT trap removes.
printf '%s\n' "$split" > "$work/split.txt"

# Whole packages: go list ./... minus the split ones, in go list order.
go list ./... | awk -v skip_file="$work/split.txt" -v idx="$index" -v total="$total" '
	BEGIN {
		while ((getline line < skip_file) > 0) {
			if (line != "") skip[line] = 1
		}
	}
	!($0 in skip) {
		if (k++ % total == idx) print
	}
' > "$work/pkgs.txt"
```

Deliverable: `scripts/test-shard.sh` carries the list through `$work/split.txt`, the awk
`BEGIN` reads it with `getline`, and the file is 174 lines instead of 169.

Verification of this step alone, before the test file is updated:
```sh
SPLIT_PKGS="./internal/relevo ./internal/delivery" sh scripts/test-shard.sh --dry-run 0 1 > /tmp/two.txt
echo "exit=$?"        # want exit=0 (the old construct also exits 0 on GNU awk)
wc -l < /tmp/two.txt  # want 1013
grep -c '^test .*/internal/delivery ' /tmp/two.txt   # want 90
```
The line count is the two-package TOTAL=1 assignment; 879 of the 1013 lines are
`test …/internal/relevo`, 90 are `test …/internal/delivery`, 44 are `pkg` lines.

Hardest check that the change is behaviour-preserving, worth doing here: run both versions
and compare. In the repo, with `git stash`-free means:
```sh
git show HEAD:scripts/test-shard.sh > /tmp/shard-old.sh
for t in 0 1 2; do SPLIT_PKGS="./internal/relevo ./internal/delivery" sh /tmp/shard-old.sh --dry-run "$t" 3; done > /tmp/old.txt
for t in 0 1 2; do SPLIT_PKGS="./internal/relevo ./internal/delivery" sh scripts/test-shard.sh --dry-run "$t" 3; done > /tmp/new.txt
diff /tmp/old.txt /tmp/new.txt
```
`diff` must print nothing. (Old and new produce the same 1013 lines on GNU awk; this pins
that the file transport changes no assignment.)

Also run `shellcheck scripts/test-shard.sh` -- no output.

### Step 2 -- the regression test in `scripts/test-shard_test.sh`

File: `scripts/test-shard_test.sh`. Insert the block below at line 89: directly after the
`done` that closes the `for total in 1 2 3 4` loop (line 88) and before the blank line and
`# Bad usage: no arguments, INDEX == TOTAL, and a negative INDEX.` (line 90). Use that
comment as the anchor -- it is unique in the file. Nothing else in the file changes.

Current text around the insertion point (`scripts/test-shard_test.sh:87-91`):

```sh
	fi
done

# Bad usage: no arguments, INDEX == TOTAL, and a negative INDEX.
check_usage
```

Block to insert between `done` and the blank line (this is `scripts/test-shard_test.sh:89-193`
after the edit; 105 lines, the file becomes 201 lines):

```sh

# BSD awk rejects a newline inside a -v assignment, where GNU awk accepts it, so
# a shard script that hands awk its newline-separated list through -v passes on
# Linux and fails on macOS. No BSD awk exists here, so the shim below stands in
# for one: it rejects exactly that argument shape and passes the rest to the
# real awk. Every run in this block goes through it.
mkdir -p "$work/bin"
real_awk=$(command -v awk)
export real_awk
cat > "$work/bin/awk" <<'SH'
#!/bin/sh
# A stand-in for BSD awk: a newline inside a -v value is an error there.
nl='
'
prev=
for arg in "$@"; do
	if [ "$prev" = "-v" ]; then
		case $arg in
		*"$nl"*) echo "awk: newline in string $arg" >&2; exit 2 ;;
		esac
	fi
	case $arg in
	-v?*=*)
		case ${arg#-v} in
		*"$nl"*) echo "awk: newline in string $arg" >&2; exit 2 ;;
		esac
		;;
	esac
	prev=$arg
done
exec "$real_awk" "$@"
SH
chmod +x "$work/bin/awk"

# The shim must reject a newline in a -v value, or the cases below would pass
# under the very construct they exist to catch.
if PATH="$work/bin:$PATH" awk -v x="$(printf 'a\nb')" 'BEGIN { print 1 }' </dev/null >/dev/null 2>&1; then
	echo "FAIL: the BSD-awk shim accepted a newline in -v, so it pins nothing"
	fail=1
fi

# SPLIT_PKGS naming two packages is the shape the default list takes once a
# second package joins it. dry_run_split runs --dry-run with both split, under
# the shim.
dry_run_split() {
	status=0
	(cd "$repo" && PATH="$work/bin:$PATH" \
		SPLIT_PKGS="./internal/relevo ./internal/delivery" \
		sh "$here/test-shard.sh" --dry-run "$1" "$2") > "$work/out" 2>&1 || status=$?
}

# TOTAL=1 is the two-package assignment the shards below are compared against.
dry_run_split 0 1
if [ "$status" -ne 0 ]; then
	echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=1 exits $status, want 0"
	fail=1
else
	LC_ALL=C sort -u "$work/out" > "$work/two-all.txt"
	# Each split package must contribute `test` lines rather than a whole `pkg`
	# line: the exit status alone would also hold for a run that skipped nothing.
	for dir in internal/relevo internal/delivery; do
		if grep -q "^pkg .*/$dir\$" "$work/two-all.txt"; then
			echo "FAIL: split package $dir is assigned whole, not split"
			fail=1
		fi
		if ! grep -q "^test .*/$dir " "$work/two-all.txt"; then
			echo "FAIL: split package $dir contributes no tests"
			fail=1
		fi
	done
fi

# TOTAL=3 is the shape CI runs, and two split packages make it the first case
# with more than one split file.
: > "$work/two-union.txt"
bad=0
index=0
while [ "$index" -lt 3 ]; do
	dry_run_split "$index" 3
	if [ "$status" -ne 0 ]; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 INDEX=$index exits $status, want 0"
		fail=1
		bad=1
	else
		cat "$work/out" >> "$work/two-union.txt"
	fi
	index=$((index + 1))
done

# A shard that never reached awk says nothing about the assignment, so the
# partition checks are meaningless when one failed above.
if [ "$bad" -eq 0 ]; then
	dupes=$(LC_ALL=C sort "$work/two-union.txt" | uniq -d)
	if [ -n "$dupes" ]; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 shards overlap:"
		printf '%s\n' "$dupes"
		fail=1
	fi

	LC_ALL=C sort -u "$work/two-union.txt" > "$work/two-union.sorted"
	if ! cmp -s "$work/two-union.sorted" "$work/two-all.txt"; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 shards do not cover the TOTAL=1 assignment"
		fail=1
	fi
fi
```

What each new assertion pins:
- the exit-status assertions -- the shard script reaches awk at all under two split packages;
  on macOS this is the reported `awk: newline in string` exit 2, on Linux the shim's exit 2.
- the `pkg`/`test` line assertions -- both packages are resolved and split, not silently
  skipped (`--dry-run` exiting 0 is not enough: a run that skipped nothing also exits 0).
- the two-package TOTAL=3 disjoint/union checks -- the first case with more than one split
  file, and the first that exercises the `s$n.pkg` numbering with `n > 1`.
- the shim self-check -- the three cases above cannot pass vacuously.

Verification: `sh scripts/test-shard_test.sh` prints `test-shard: ok` and exits 0 (about 5 s),
and `shellcheck scripts/test-shard.sh scripts/test-shard_test.sh` prints nothing. The run now
covers four extra `--dry-run` invocations; each one takes under a second.

### Step 3 -- mutation test (required; run it after steps 1 and 2 are green, and before the
commit in step 5, so `git checkout --` still restores main's version)

The point: prove the new case fails *on Linux* under the construct the issue reports, and
that the shim is not decorative.

1. Break the fix and confirm a named failure:
   ```sh
   cp scripts/test-shard.sh /tmp/shard-fixed.sh
   git checkout -- scripts/test-shard.sh      # back to the -v construct
   sh scripts/test-shard_test.sh; echo "exit=$?"
   cp /tmp/shard-fixed.sh scripts/test-shard.sh
   sh scripts/test-shard_test.sh               # green again: test-shard: ok
   ```
   Expect `exit=1` and these four lines (observed on Linux with GNU awk 5.4.1):
   ```
   FAIL: SPLIT_PKGS naming two packages, TOTAL=1 exits 2, want 0
   FAIL: SPLIT_PKGS naming two packages, TOTAL=3 INDEX=0 exits 2, want 0
   FAIL: SPLIT_PKGS naming two packages, TOTAL=3 INDEX=1 exits 2, want 0
   FAIL: SPLIT_PKGS naming two packages, TOTAL=3 INDEX=2 exits 2, want 0
   ```
   (The `pkg`/`test` line assertions are skipped in that run because the TOTAL=1 run failed
   first; that is the `else` branch doing its job.)

2. Break the shim and confirm the self-check fires:
   ```sh
   cp scripts/test-shard_test.sh /tmp/shard-test.sh
   # delete the `if [ "$prev" = "-v" ]; then ... fi` stanza from the shim body
   sh scripts/test-shard_test.sh; echo "exit=$?"
   cp /tmp/shard-test.sh scripts/test-shard_test.sh
   ```
   Expect `exit=1` and exactly:
   ```
   FAIL: the BSD-awk shim accepted a newline in -v, so it pins nothing
   ```

Name the failing test(s) in the report: `scripts/test-shard_test.sh` is the test file; the
failure text is the assertion message. Restore both files and re-run the focused command
before moving on: `git diff --stat` must show only the two intended script changes.

### Step 4 -- full check

```sh
make check
gofmt -l .
sh scripts/check-comments.sh
```
- `make check` must pass; it runs `check-scripts` (shellcheck + every `scripts/*_test.sh`,
  so the new cases run) and then the Go suite. If a failure names a file this plan did not
  touch, halt and report it -- do not "fix" it.
- `gofmt -l .` prints nothing.
- `sh scripts/check-comments.sh` prints `check-comments: ok`.

### Step 5 -- save the plan and commit

```sh
cp <this plan, verbatim> docs/plans/2026-09-26-test-shard-bsd-awk.md
git add scripts/test-shard.sh scripts/test-shard_test.sh docs/plans/2026-09-26-test-shard-bsd-awk.md
git commit -m "ci: the shard split list reaches awk through a file, not -v (#580)"
```
The plan file must be this text verbatim -- no summary, no "as implemented" edits. The
commit carries the two scripts and the plan; no other path. Do not amend, squash or
force-push anything else.

## 9. Guardrails for the builder

- Do not change the shard arithmetic, the `--dry-run` output format, the split-package
  `grep`/`sed` extraction, or the real (`go test`) run path.
- Do not add a lint, size or coverage exclusion, and do not touch `.golangci.yml`,
  `testdata/coverage-baseline.txt`, or the `*-comments.allow` / `*-filesize.allow` lists.
- No issue or PR numbers in code or test comments; the commit subject carries `(#580)`, the
  docs/plans file name has none.
- `scripts/test-shard_test.sh` must not reach the network beyond `go list`, and must not run
  `go test`; every assertion stays on `--dry-run`.
- If a step is impossible as written or contradicts the code (for example the quoted lines at
  `scripts/test-shard.sh:82-93` are not what is quoted here), halt and report which line
  differs. A red test bent to fit is worse than a halt.
