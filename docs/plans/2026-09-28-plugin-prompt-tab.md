# The plugin's first tab is `prompt`, and the smoke stops hiding the retired `--plan`

Issue #644. One builder commit on top of this tree's HEAD `71731989`: the plugin, its smoke fixtures, the smoke driver, and this plan — copied byte for byte from the round's staged plan — as `docs/plans/2026-09-28-plugin-prompt-tab.md`.

Line numbers are this tree's HEAD; if one moved, find the named function. Halt and report instead of improvising if a step is impossible as written, if a smoke assertion fails for a reason this plan does not name, if the `plan` residue sweep finds a hit this plan does not list, or if a file outside §2 needs to change. Never delete a test or an assertion to get green; retarget it.

## 1. Behaviour and the cases

The defect. The plugin's first tab is named `plan` and its body is fetched with `relevo show <name> --json --plan`. `cmd/relevo/show.go:96` registers `--prompt` (the default) and never `--plan`; `cmd/relevo/show_test.go`'s `TestShowRetiredFlagsAreUnknown` pins `flag provided but not defined: -plan`. Against the real binary the spawn fails and the tab body stays empty, while the cockpit's same tab is titled `prompt` (`internal/ui/fetch.go:31`). The smoke stayed green because the fake answers `--plan` and `show-plan.json` pins `"Section": "plan"`; the status fixtures pin the retired phase `plan sent` / `last_kind: plan`, whose live words are `prompt sent` / `prompt` (`internal/view/statusline.go:162-168`).

After this round:

- The binding page's first tab is `prompt`; the tab row, the tab order (`prompt, report, diff, log, transcript`) and the other four tabs and their flags are otherwise unchanged.
- A binding page whose stored tab is empty, or is not one of the five tab words, opens on `prompt`. This covers a tab word stored before this change (`"plan"`): it must not spawn `--plan` and must not render `(no plan)`.
- The prompt tab's body is one `relevo show <name> --json --prompt [--round N]` spawn, cached per (name, round, tab) and markdown-rendered like the report tab; a missing or empty prompt still renders `(no prompt)`.
- The status fixtures speak the live words; the plugin renders `prompt sent` wherever those rows surface.
- The smoke answers, sends and asserts only the live flag and word.

Cases the smoke pins after retargeting:

- the tab row's first word and the round keys' `--prompt` argv in the fake log (assertions 6 and 35);
- the renamed prompt fixture rendered as markdown on the prompt tab: heading, `##` heading, `---` rule, link, one rule line (assertions 25, 29, 30, 31, 42);
- the selected-tab extraction still finds the tab row with the new word (assertion 48);
- every assertion about a plan *file* or `REPORT IN` is untouched (4, 22, 23, 39, 47, and the rest). No assertion is added, deleted or renumbered; the final count stays 49.

Not cases here: the toast and the `stateWord` fallback (#642); the spec's tab/flag prose (#645); a persisted old tab word's *repair* beyond falling back to `prompt` (§2.2, §5.6).

## 2. Seams

### 2.1 `internal/harness/opencodeplugin/tui.tsx` (four words, one comment)

| line | today | becomes |
|---|---|---|
| 548 | store initial `bindingTab: "plan"` | `"prompt"` |
| 1590 | `tabs = ["plan", "report", "diff", "log", "transcript"]` | `["prompt", …]` |
| 1623 | `const currentTab = store.bindingTab \|\| "plan"` | the stored value when it names one of the five tab words, else `"prompt"` |
| 1784 | `v.currentTab === "plan" \|\| v.currentTab === "report"` | `"prompt" \|\| "report"` |
| 1193 | comment "The plan and report tabs call it…" | "The prompt and report tabs…" (the comment names the tab) |

Deliberately untouched: `:470`'s argv (it interpolates the tab word, so it starts sending `--prompt` with no edit — verify by inspection, do not edit); `:1684`'s `tabs.indexOf(store.bindingTab || "report")` (an index lookup, not a word; a stale word already self-heals to the first tab on Tab); `:403`'s `(plan sent, report in)` phase-word comment (#642/#645 ground); `:619`, `:651-652` ("Send plan file…", "a plan file is what is sent"); `:381-391`'s `initialTab` (returns report/transcript only).

*Why 1623 is a membership test.* `api.storage.store`'s value may outlive the process (the probe did not settle it), and `openBinding` never sets the first tab (`initialTab` returns report/transcript), so a profile that tabbed to the first tab before this change can hold `bindingTab: "plan"`. A literal `|| "prompt"` leaves that profile with no highlighted tab, a `--plan` spawn and `(no plan)`; a tab-membership fallback heals it. Say in the report that this is one word wider than the issue's literal edit, and why.

### 2.2 `scripts/testdata/opencode-plugin/`

- `relevo:59` — the `--plan) TAB="plan"` arm becomes `--prompt) TAB="prompt"`; the fake no longer answers a flag the CLI rejects. `show`'s `cat "$DIR/show-$TAB.json"` then reads the renamed fixture with no further edit.
- `git mv show-plan.json show-prompt.json`, and line 8 `"Section": "plan"` → `"prompt"`. `Text` stays byte for byte (its content is a plan document: `# Plan for webshop r4`, `## Scope`, the rule and the link — assertions 25/29/30/31/42 keep grepping those words).
- `status-1.json` lines 36/39/43 (ledger) and 55/58/62 (landing), and `status-2.json` lines 56/59/63 (landing): `waiting` and `status` become `prompt sent`, `last_kind` becomes `prompt`, in each row together. No row keeps the legacy word: every row models a live round, so there is no historical-state row to exempt — say this in the report.

### 2.3 `scripts/opencode-plugin-smoke.sh`

- 101 comment, 142 comment, 147 comment, 149 `capture "05d-plan"` → `"05d-prompt"`;
- assertion 6 (256-261): `grep -q "prompt"` on `04-binding.txt`;
- assertion 25 (403-414): `05d-prompt.ansi`/`05d-prompt.txt`, the fixture comment's word; the greps keep `Plan for webshop`, `Implement cart`;
- assertions 29, 30, 31 (455-477) and 42 (600-610): capture name and description;
- assertion 35 (508-515): `show webshop --json --prompt --round 4` and `--prompt --round 2`;
- assertion 48 (653-661): `\bprompt\b.*\breport\b` in both greps, the `[ … ]` extraction unchanged.

Every renamed capture's `.txt` and `.ansi` references move together. Unchanged: the navigation steps and their timings (the tab order did not change), the `plan` file paths `a[b]/plan a.md` / `a/fleet [plan] a.md` (174, 203) and assertions 39/47, and the `REPORT IN` assertions.

### 2.4 New

`docs/plans/2026-09-28-plugin-prompt-tab.md` — this plan, committed in the same commit. No Go file, no `docs/specs/*` (#645), no README, no `docs/design.md`.

## 3. Ordered steps

1. **Baseline smoke.** On the unmodified tree run `bash scripts/opencode-plugin-smoke.sh`. Done when: it prints `all 49 assertions passed`, so a later failure is not the environment's.
2. **`tui.tsx` in one edit pass** (§2.1). Done when: `git grep -n '\bplan\b' internal/harness/opencodeplugin/tui.tsx` prints only 403, 619, 651, 652 (1548/1566 are `to_planner`), and `git diff` for the file is exactly those hunks.
3. **Fake and fixture rename** (§2.2, first two items). Done when: `git status` shows `show-prompt.json` as a rename; `grep -rn '"Section": "plan"\|--plan' scripts/testdata/opencode-plugin/` prints nothing; the fake holds no `plan`.
4. **Status fixtures** (§2.2, third item). Done when: `grep -rn 'plan sent\|"last_kind": "plan"' scripts/testdata/opencode-plugin/` prints nothing and the diff is only those nine field values.
5. **Smoke driver** (§2.3). Done when: `bash -n scripts/opencode-plugin-smoke.sh` is silent; the `plan` residue is only lines 174, 203, 574, 577, 580, 647, 649, 651; the PASSED line still says 49.
6. **Smoke and mutations** (§4). Done when: the post-change run is green; M1 fails assertions 25 and 29; M2 fails 6, 25, 29 and 35; after each restore the run is green again and `git diff` is back to §2.
7. **Full check, plan, one commit.** Run `make check`; copy the round's staged plan to `docs/plans/2026-09-28-plugin-prompt-tab.md`; commit code, fixtures, smoke and plan together. Done when: `make check` is green and the commit's `git diff --stat` lists only §2's files.

## 4. Verification

- Focused loop: `bash scripts/opencode-plugin-smoke.sh` — the only end-to-end check of the plugin. It needs `tmux`, `opencode`, `sqlite3`, `jq` and a top-level session in the user's `opencode.db`; all were present when this plan was written (63 top-level sessions), and the script exits 1 with `Error: no top-level OpenCode session found` when it cannot run. About 90 s per run; captures land in the directory it prints.
- After the edits the same command must print `all 49 assertions passed`. Read `05d-prompt.txt` and the fake log for evidence of `show webshop --json --prompt --round 4`.
- Mutations, each a full smoke run, each restored and re-verified green:
  - **M1** — put the fake's `--plan) TAB="plan"` arm back (so it no longer answers `--prompt`): assertions 25 and 29 must fail, because the prompt tab then serves the report fixture.
  - **M2** — run with `tui.tsx` at HEAD (`git stash push -- internal/harness/opencodeplugin/tui.tsx`, then `git stash pop`): assertions 6, 25, 29 and 35 must fail — the tab row still says `plan` and the log still carries `--plan`.
- Full check once at the end: `make check`. It does not run the smoke and covers no `.tsx`; its only Go-visible effect here is the embedded plugin bytes, pinned by `internal/harness/files_test.go`.
- Not run, and say so: `make e2e` (no Go behaviour changes).
- If the smoke cannot run at all: report the exact command and its output, then the plugin half is verified by inspection only — say that plainly and run the residue sweeps of steps 2-5 as the substitute evidence.

## 5. Closed list: what goes

No behaviour is deleted — the same five tabs, bodies and order remain. What goes is the retired word and the fixtures that hid it. Everything not on this list stays:

1. The `plan` tab word in `tui.tsx` (the `tabs` list, the store's `bindingTab` default, the `currentTab` fallback, the tab-body condition), replaced by `prompt`; no alias for the old word is kept in the plugin.
2. The fake `relevo`'s `--plan) TAB="plan"` arm, replaced by `--prompt) TAB="prompt"`.
3. The file name `show-plan.json` (moved to `show-prompt.json`) and its `"Section": "plan"` value; its `Text` is kept.
4. `plan sent` / `plan` in the three live status-fixture rows, replaced by `prompt sent` / `prompt`.
5. The smoke's `--plan`-mapped assertions and the `05d-plan` capture name, replaced by `--prompt` and `05d-prompt`; no assertion is deleted, added or renumbered (49).
6. Nothing else: not `:470`'s argv, not `initialTab`, not `:1684`'s `|| "report"`, not the "Send plan file…" dialog, not the fixture's plan body, not the smoke's plan-file paths or assertions 39/47, not the toast or `stateWord`'s fallback (#642), not `docs/specs` (#645), not README or `docs/design.md`.

## 6. Report must include

- `git diff --stat` and `git status` (the fixture must show as a rename), and the commit sha.
- `make check`: pass/fail, and the statement that it neither runs the smoke nor reads `.tsx`.
- The smoke result for every run: baseline, post-change, M1, M2, each restore — with the named failing assertions; or, if it could not run, the exact output that shows why and the explicit statement that the plugin change is then inspection-only.
- The residue sweeps of steps 2-5, one line per remaining `plan` hit and why it stays.
- The §2.1 fallback decision (membership test rather than the issue's literal `|| "prompt"`) and its upgrade case; and that the smoke reaches the prompt tab by Tab, so the store default and that fallback are inspection-only.
- The status-fixture decision: all rows are live rounds, so none keeps the legacy word.
- Anything the code contradicted.

**Riskiest step:** step 6 — the smoke is the only end-to-end proof, it drives a real OpenCode TUI against the user's opencode database, it costs ~90 s per run, and if it cannot run the plugin half ships on inspection alone.

## Round 2 addendum

Round 1 halted at its step-1 gate, before any edit: on the unmodified tree the smoke failed assertions **1, 4 and 23** for pre-existing reasons, not for anything the retired `plan` tab word touches. Round 2 repairs those two seams first.

- The baseline was red at HEAD on assertions 1, 4 and 23: assertion 1 grepped the retired `mastermind init` registration verb (the plugin has sent `mastermind guide` since #634), and assertions 4 and 23 read the `02-after-toast` frame that the fixed `sleep 6` captured after the 6 s toast had expired.
- The fake `relevo` now answers `guide` with one JSON line (`state`, `text`, `id`, `name`), so assertion 1 is retargeted to `mastermind guide --json --kind opencode --session $SESSION_ID`. `state` is `disabled` with `id`/`name` kept: `enabled` makes the plugin register the relevo MCP server, and the run then spawns `relevo mcp --kind opencode`, which the fake does not answer and which fails assertion 11.
- The toast capture waits for the frame: `wait_toast` captures `02-after-toast` in 0.5 s steps (up to 30 s) and stops at the first frame that carries `report in, delivered to chat`, instead of the fixed `sleep 6`. Assertions 4 and 23 are unchanged and run on the captured frame.
- With those two seams repaired the baseline is `all 49 assertions passed`, and the plan above is then executed as its steps 2-5 and 7, with its step 6 mutations (M1, M2, M3).
