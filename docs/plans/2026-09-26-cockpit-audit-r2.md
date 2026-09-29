# Cockpit `:audit`, round 2: quoted keys and the breadcrumb (2026-09-26)

## 1. System overview

Round 1 (commit "feat(cockpit): :audit …", now rebased onto `origin/main` `fa2fea0`) shipped `:audit`. Its report
found two defects, and this round fixes both. Nothing else changes.

1. **Quoted path keys.** `config.joinPath` (`internal/config/diff.go:217-226`) writes a key that is not a bare
   identifier as `["<json string>"]`. So an agent or actor named `plan-executor` appears in a change path as
   `agents["plan-executor"]` or `actors["plan-executor"].tier`. `pathUnder` (`internal/relevo/configaudit.go`, below
   `candidatePath`) only understands `.<ident>`, so such a change falls through to rule 7 and prints the raw path. The
   shipped agents are `plan-executor`, `reviewer`, `researcher` and `architect`, so this happens in real data.
2. **The double breadcrumb.** `auditRevView.Crumbs()` returns `["audit", "#N"]`. The shell joins every stacked view's
   crumbs (`internal/ui/frame.go:143-156`), so the header reads `audit › audit › #2`. The pushed view must return
   only its own segment, as `actorView` and `agentView` do.

## 2. Files

```
internal/relevo/configaudit.go       pathUnder accepts a quoted key
internal/relevo/configaudit_test.go  + the cases of §4
internal/ui/view_audit.go            auditRevView.Crumbs
internal/ui/testdata/audit-rev-132.golden  regenerated (the header only)
docs/plans/2026-09-26-cockpit-audit-r2.md  this plan
```

## 3. Contracts

### 3.1 `pathUnder(path, prefix string) (a, rest string, ok bool)`

**Change its contract** to take the section name without the dot: `pathUnder(path, "actors")`. Update its two
callers in `changeSubject`. After the section name it accepts either form of key:
- `.<key>`: the key runs to the next `.` or `[`;
- `["…"]`: the quoted part is decoded with `json.Unmarshal` into a string. A decode failure returns `ok = false`.

After the key:
- nothing → `rest = ""`;
- `.` → `rest` is everything after that dot;
- `[` → `rest` is everything from the `[` on, e.g. `["x"]`;
- anything else → `ok = false`.

`rest` stays as the raw path text. Field shows it as is.

### 3.2 `auditRevView.Crumbs()`

Returns `[]string{fmt.Sprintf("#%d", rev)}`.

## 4. Steps

### 0. Working efficiently

**How to work:**
- In one batch, read `internal/relevo/configaudit.go`, `internal/relevo/configaudit_test.go` (the `TestDescribeChange`
  table), `internal/ui/view_audit.go` (search `Crumbs`) and `internal/config/diff.go:200-245`.
- Make each file's change in one edit.

**Commands:**
- Focused loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ ./internal/ui/ -run 'DescribeChange|Audit|Golden' -count=1`
- Golden: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`.
  Only `audit-rev-132.golden` may change, and only on line 1.
- Final checks:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/config/ ./internal/relevo/ ./internal/ui/ -count=1`
  - `go vet ./internal/...`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-name.sh`
- `make check` is refused here, so do not run it.

### 1. `pathUnder` (§3.1)

### 2. Test cases added to `TestDescribeChange`

| path, op | expected Subject | expected Field |
|---|---|---|
| `actors["plan-executor"].tier`, change `edit` → `yolo` | `actor plan-executor` | `tier` |
| `agents["plan-executor"]`, add `{"shape":"writer"}` | `agent plan-executor` | `""` |
| `actors.builder.candidates`, change | `actor builder` | `candidates` |
| `actors["bad`, change | `actors["bad` (rule 7) | `""` |

The existing cases must pass unchanged.

### 3. `auditRevView.Crumbs` (§3.2), then regenerate the golden

The header must read `audit › #2`.

### 4. Required mutation

- **M1:** make the quoted branch of `pathUnder` return `ok = false` → the first two rows of step 2 fail.

Report the failing lines, then revert.

### 5. Checks, the plan, the commit

1. Run the final checks listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-cockpit-audit-r2.md`.
3. `git add -A && git commit --amend --no-edit`. One commit stays on the branch.

## 5. Deletions

None.

## 6. Stop rather than improvise

Halt and report if any of these happens:
- a golden other than `audit-rev-132` changes;
- an existing `TestDescribeChange` case fails;
- `auditRevView` is reached some way other than by being pushed onto `auditView`.
