#!/usr/bin/env bash
# Smoke driver for relevo OpenCode plugin (#393)
# See docs/plans/2026-09-24-opencode-tui-b1-plugin.md §5.2
#
# SC2329 (shellcheck >= 0.10) and SC2317 (older, e.g. CI's Ubuntu runner):
# `cleanup` runs from `trap ... EXIT`, and each `check_assertion_*` helper is
# invoked indirectly, as an argument to `assert`.
# shellcheck disable=SC2317,SC2329
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d /tmp/relevo-smoke-XXXXXX)"
CONFIG="$TMP/config"
OUT="${OUT:-$TMP/out}"
STATE="$TMP/state"
LOG="$TMP/fake.log"
SESSION_TMUX="relevo-plugin-smoke"

mkdir -p "$CONFIG/plugins/relevo" "$OUT" "$STATE"
touch "$LOG"

cleanup() {
  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

# 1. Copy (not symlink) internal/harness/opencodeplugin/ to $CONFIG/plugins/relevo/
cp "$REPO/internal/harness/opencodeplugin/package.json" "$CONFIG/plugins/relevo/"
cp "$REPO/internal/harness/opencodeplugin/tui.tsx" "$CONFIG/plugins/relevo/"
cp "$REPO/internal/harness/opencodeplugin/server.ts" "$CONFIG/plugins/relevo/"

# Config isolation
ln -sf "$HOME/.config/opencode/opencode.jsonc" "$CONFIG/opencode.jsonc"
cat > "$CONFIG/cli.json" <<'EOF'
{
  "$schema": "https://opencode.ai/v2/cli.json",
  "session": { "sidebar": "auto", "scrollbar": false, "thinking": "hide" },
  "animations": false
}
EOF

# Find newest top-level session
SESSION_ID="$(sqlite3 -readonly "$HOME/.local/share/opencode/opencode.db" \
  "select id from session where parent_id is null order by time_updated desc limit 1;" 2>/dev/null || true)"

if [ -z "$SESSION_ID" ]; then
  echo "Error: no top-level OpenCode session found" >&2
  exit 1
fi

send() { tmux send-keys -t "$SESSION_TMUX" "$@"; }
type_lit() { tmux send-keys -t "$SESSION_TMUX" -l "$1"; }

capture() {
  local name="$1"
  tmux capture-pane -t "$SESSION_TMUX" -p > "$OUT/$name.txt" 2>/dev/null || : > "$OUT/$name.txt"
  tmux capture-pane -t "$SESSION_TMUX" -p -e > "$OUT/$name.ansi" 2>/dev/null || : > "$OUT/$name.ansi"
}

# Wait for the ledger report-in toast: it lives 6 s and its poll is not at a
# fixed offset from the navigation above, so capture in short steps and stop at
# the first frame that carries it.
wait_toast() {
  local i=0
  while [ "$i" -lt 60 ]; do
    capture "02-after-toast"
    if grep -q "report in, delivered to chat" "$OUT/02-after-toast.txt"; then
      return 0
    fi
    sleep 0.5
    i=$((i + 1))
  done
}

wait_ready() {
  # The TUI draws a "Loading plugins..." splash while plugins resolve. Wait for
  # that splash to clear *and* for the session view to have real content: an
  # empty pane (before the first draw) also lacks "Loading plugins", so the
  # splash test alone can return early and capture nothing.
  local i=0
  while [ "$i" -lt "${1:-60}" ]; do
    pane="$(tmux capture-pane -t "$SESSION_TMUX" -p 2>/dev/null || true)"
    if ! printf '%s' "$pane" | grep -q "Loading plugins"; then
      if [ "$(printf '%s\n' "$pane" | grep -c '[^[:space:]]')" -ge 5 ]; then
        return 0
      fi
    fi
    sleep 0.5
    i=$((i + 1))
  done
  echo "wait_ready: TUI did not settle after ${1:-60} polls" >&2
  return 1
}

tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true

# Launch standalone opencode in tmux
tmux new-session -d -s "$SESSION_TMUX" -x 160 -y 45 \
  "env PATH=$REPO/scripts/testdata/opencode-plugin:$PATH \
       OPENCODE_CONFIG_DIR=$CONFIG \
       RELEVO_FAKE_LOG=$LOG \
       RELEVO_FAKE_STATE=$STATE \
       XDG_STATE_HOME=$STATE \
       opencode --standalone --print-logs -s $SESSION_ID 2> $TMP/opencode.log"

wait_ready 60

# Let initial registration and 1st poll complete
sleep 3
capture "01-session"

# Open ledger's report tab before status-2 (returns Missing: true -> (no report))
send C-x; sleep 0.4; send o; sleep 0.8
send Down; sleep 0.3; send Enter; sleep 1.0
# ledger starts on transcript tab; cycle tab twice to reach report tab (tabs: prompt, report, diff, log, transcript)
send Tab; sleep 0.3; send Tab; sleep 1.0
capture "01b-ledger-stale"
send Escape; sleep 0.4
# Reset fleet cursor to webshop (index 0) so subsequent fleet navigations start at webshop
send C-x; sleep 0.4; send o; sleep 0.8
send Up; sleep 0.3; send Escape; sleep 0.4

wait_toast

# 03-fleet (C-x o)
send C-x; sleep 0.4; send o; sleep 1.5
capture "03-fleet"

# 04-binding: enter on webshop. Rule 1 opens the report -- webshop needs you
# and has report_round 3 -- on that round, with no key pressed.
send Enter; sleep 1.5
capture "04-binding"

# 04b-landing: escape back to the session, reopen the fleet, down to landing
# (no report -> the transcript tab), enter.
send Escape; sleep 0.5
send C-x; sleep 0.4; send o; sleep 1.0
send Down; sleep 0.3; send Down; sleep 0.3; send Enter; sleep 1.5
capture "04b-landing"

# 05-binding-report: back to the fleet, up to webshop, enter -> the report tab.
send Escape; sleep 0.5
send C-x; sleep 0.4; send o; sleep 1.0
send Up; sleep 0.3; send Up; sleep 0.3; send Enter; sleep 1.5
capture "05-binding-report"

# 05b/05c: two PageDowns, then two polls (>= 12 s) with the body scrolled; the
# first body line must be unchanged and must not be line 01.
send PageDown; sleep 0.4; send PageDown; sleep 0.8
capture "05b-scrolled"
sleep 12
capture "05c-after-polls"

# 05d/05e/05f: transcript, prompt as markdown, and stale report refetch
# From report tab (index 1), tab 3 times to transcript tab (index 4)
send Tab; sleep 0.3; send Tab; sleep 0.3; send Tab; sleep 1.5
capture "05e-transcript"

# Tab once more to prompt tab (index 0)
send Tab; sleep 2.5
capture "05d-prompt"

# Round keys: the page is webshop, which opened on r3. ] moves to r4; then [ [
# moves back to r2 ([ takes the largest round below the current one).
type_lit "]"; sleep 1.5
capture "05g-round-next"
type_lit "["; sleep 0.5; type_lit "["; sleep 1.5
capture "05h-round-prev"

# Back to fleet, open ledger (now report_in with real report body)
send Escape; sleep 0.5
send C-x; sleep 0.4; send o; sleep 1.0
send Down; sleep 0.3; send Enter; sleep 1.5
capture "05f-ledger-after"

# Return to webshop for 06-dialog
send Escape; sleep 0.5
send C-x; sleep 0.4; send o; sleep 1.0
send Up; sleep 0.3; send Enter; sleep 1.5
capture "05i-reenter"

# S1
send a; sleep 1.5
send Down; sleep 0.5
send Enter; sleep 7
type_lit "a[b]/plan a.md"; sleep 0.8
send Enter; sleep 2
capture "05j-sent"
send Tab; sleep 1.0; capture "05k-after-dialog-tab"
send BTab; sleep 1.0

# 06-dialog (a)
send a; sleep 1.5
capture "06-dialog"

# 07-dialog-actions (step into the option list), 08-confirm-done (down to
# Mark done, enter), then enter on confirm
send Down; sleep 1.5
capture "07-dialog-actions"
send Down; sleep 0.3; send Down; sleep 0.3; send Enter; sleep 1.5
capture "08-confirm-done"
send Enter; sleep 1.5
capture "09-after-done"

# 10-palette (ctrl+p, type relevo)
send Escape; sleep 0.5
send C-p; sleep 1.0; send C-u; type_lit "relevo"; sleep 1.5
capture "10-palette"
send Escape; sleep 0.5

# S2
send C-x; sleep 0.4; send o; sleep 1.0
send Up; sleep 0.3; send Up; sleep 0.3
send a; sleep 1.5; send Down; sleep 0.3; send Enter; sleep 7
type_lit "a/fleet [plan] a.md"; sleep 0.8; send Enter; sleep 2
capture "11-fleet-after-dialog"

tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true

# Assertions
FAILED=0
assert() {
  local num="$1"
  local desc="$2"
  shift 2
  if "$@"; then
    echo "assertion $num: PASS -- $desc"
  else
    echo "assertion $num: FAIL -- $desc"
    FAILED=1
  fi
}

echo "=== Running Assertions ==="

# 1. log has mastermind guide --json --kind opencode --session <that session id>
assert 1 "log has mastermind guide --json --kind opencode --session $SESSION_ID" \
  grep -q "mastermind guide --json --kind opencode --session $SESSION_ID" "$LOG"

# 2. 01-session shows relevo · oc-smoke, webshop, NEEDS YOU
check_assertion_2() {
  grep -q "relevo · oc-smoke" "$OUT/01-session.txt" && \
  grep -q "webshop" "$OUT/01-session.txt" && \
  grep -q "NEEDS YOU" "$OUT/01-session.txt"
}
assert 2 "01-session shows relevo · oc-smoke, webshop, NEEDS YOU" check_assertion_2

# 3. 01-session shows relevo 1 need you
assert 3 "01-session shows relevo 1 need you" \
  grep -q "relevo 1 need you" "$OUT/01-session.txt"

# 4. 02-after-toast shows ledger r … report in, delivered to chat (the toast) and relevo 1 need you
check_assertion_4() {
  grep -qE "ledger r.*report in, delivered to chat" "$OUT/02-after-toast.txt" && \
  grep -q "relevo 1 need you" "$OUT/02-after-toast.txt"
}
assert 4 "02-after-toast shows ledger report toast and relevo 1 need you" check_assertion_4

# 5. 03-fleet shows relevo › fleet and the header NAME … TOKENS
check_assertion_5() {
  grep -q "relevo › fleet" "$OUT/03-fleet.txt" && \
  grep -q "NAME" "$OUT/03-fleet.txt" && \
  grep -q "TOKENS" "$OUT/03-fleet.txt"
}
assert 5 "03-fleet shows relevo › fleet and column header" check_assertion_5

# 6. 04-binding shows relevo › fleet › webshop and the tab row
check_assertion_6() {
  grep -q "relevo › fleet › webshop" "$OUT/04-binding.txt" && \
  grep -q "prompt" "$OUT/04-binding.txt" && \
  grep -q "report" "$OUT/04-binding.txt"
}
assert 6 "04-binding shows relevo › fleet › webshop and tab row" check_assertion_6

# 7. log has show webshop … --report after step 05, and 05-binding-report
#    draws the report body the fake served
check_assertion_7() {
  grep -qE "show webshop.*--report" "$LOG" && \
  grep -q "checkout flow implementation is ready" "$OUT/05-binding-report.txt"
}
assert 7 "log has show webshop … --report and 05-binding-report draws it" check_assertion_7

# 8. 06-dialog shows tell the mastermind (or, with the fallback, Tell the mastermind…)
assert 8 "06-dialog shows tell the mastermind" \
  grep -qi "tell the mastermind" "$OUT/06-dialog.txt"

# 9. log has done webshop after step 08
assert 9 "log has done webshop after step 08" \
  grep -q "done webshop" "$LOG"

# 10. 10-palette lists Open relevo
assert 10 "10-palette lists Open relevo" \
  grep -q "Open relevo" "$OUT/10-palette.txt"

# 11. no line in the log starts with anything but a relevo verb (sanity)
check_assertion_11() {
  local bad_lines
  bad_lines="$(grep -vE "^(mastermind|status|history|show|send|stop|done|gate)\b" "$LOG" | grep -v "^$" || true)"
  [ -z "$bad_lines" ]
}
assert 11 "no line in fake log starts with anything but a relevo verb" check_assertion_11

# 12. 03-fleet shows a recent row below ── recent. history.json lists webshop
#     r4 twice (a builder switch), webshop r3, ledger r2, landing r1, so
#     ledger r2 is one of the rows shown.
check_assertion_12() {
  awk '/── recent/{after = 1; next} after' "$OUT/03-fleet.txt" | grep -qE "ledger +r2"
}
assert 12 "03-fleet shows the third recent row (ledger r2)" check_assertion_12

# 13. the §2 item 3 grep prints nothing. -r because that grep names a
#     directory, which plain grep refuses to descend into.
check_assertion_13() {
  ! grep -rn -E "oc-smoke|pl_smoke|webshop|ledger|landing" \
    "$REPO/internal/harness/opencodeplugin/" >/dev/null 2>&1
}
assert 13 "no fixture names in internal/harness/opencodeplugin/" check_assertion_13

# 14. in 03-fleet webshop's NEEDS YOU and ledger's REPORT IN start at one column.
#     LC_ALL makes awk count characters, so the › marker counts as one column.
check_assertion_14() {
  local c1 c2
  c1="$(LC_ALL=C.UTF-8 awk '/NEEDS YOU/{print index($0, "NEEDS YOU"); exit}' "$OUT/03-fleet.txt")"
  c2="$(LC_ALL=C.UTF-8 awk '/REPORT IN/{print index($0, "REPORT IN"); exit}' "$OUT/03-fleet.txt")"
  [ -n "$c1" ] && [ "$c1" = "$c2" ]
}
assert 14 "03-fleet webshop NEEDS YOU and ledger REPORT IN share the STATE column" check_assertion_14

# 15. webshop's status-1 row is display ACTIVE but needs_you true with
#     report_round 3 and round 4: the sidebar must follow needs_you (NEEDS YOU)
#     and the report's round (r3), not the display word or the current round.
check_assertion_15() {
  grep -q "webshop" "$OUT/01-session.txt" && \
  grep -q "NEEDS YOU" "$OUT/01-session.txt" && \
  grep -q "r3 · builder on opencode" "$OUT/01-session.txt" && \
  ! grep -q "r4 · builder on opencode" "$OUT/01-session.txt"
}
assert 15 "01-session shows webshop NEEDS YOU and r3, not r4" check_assertion_15

# 16. the ledger status-1 -> status-2 transition is a delivered report, not a
#     new NEEDS YOU: it raises no "ledger needs you" toast and the badge count
#     stays at 1.
check_assertion_16() {
  ! grep -qi "ledger needs you" "$OUT/02-after-toast.txt" && \
  grep -q "relevo 1 need you" "$OUT/02-after-toast.txt"
}
assert 16 "02-after-toast shows no ledger NEEDS YOU toast and relevo 1 need you" check_assertion_16

# 17. rule 1: 04-binding (enter on webshop, needs_you with report_round 3)
#     arrives on the report tab with no Tab pressed; 04b-landing (no report)
#     arrives on the transcript tab.
check_assertion_17() {
  grep -q "\[ report \]" "$OUT/04-binding.txt" && \
  grep -q "\[ transcript \]" "$OUT/04b-landing.txt"
}
assert 17 "04-binding selects report and 04b-landing selects transcript" check_assertion_17

# 18. the body fills the height under the header rows: on a 45-row terminal the
#     tall report fixture shows line 30 (>= 30 body lines).
assert 18 "05-binding-report shows line 30 (the body fills the height)" \
  grep -q "line 30" "$OUT/05-binding-report.txt"

# 19. scrolling survives two polls: 05b and 05c share their first body line and
#     it is not line 01.
check_assertion_19() {
  local b c
  b="$(grep -oE "line [0-9]+" "$OUT/05b-scrolled.txt" | head -1)"
  c="$(grep -oE "line [0-9]+" "$OUT/05c-after-polls.txt" | head -1)"
  [ -n "$b" ] && [ "$b" = "$c" ] && [ "$b" != "line 01" ]
}
assert 19 "05b-scrolled and 05c-after-polls keep the same scrolled position" check_assertion_19

# 20. the binding page header follows needs_you: webshop's fixture display is
#     ACTIVE but its row needs you, so the header reads NEEDS YOU.
check_assertion_20() {
  grep -qE "webshop › r3.*NEEDS YOU" "$OUT/05-binding-report.txt"
}
assert 20 "05-binding-report header shows NEEDS YOU for webshop" check_assertion_20

# 21. the round row lists each round once, ascending: r1 r2 r3 r4, with no
#     repeated number (history.json carries two webshop r4 rows).
check_assertion_21() {
  local row
  row="$(grep -oE "\[ \] round.*" "$OUT/05-binding-report.txt" | head -1)"
  [ -n "$row" ] || return 1
  printf '%s\n' "$row" | grep -qE "r1 r2 r3 r4" && \
  [ -z "$(printf '%s\n' "$row" | grep -oE "r[0-9]+" | sort | uniq -d)" ]
}
assert 21 "05-binding-report round row is r1 r2 r3 r4 with no repeat" check_assertion_21

# 22. the delivered report reads REPORT IN, not NEEDS YOU, and the badge counts
#     only webshop: relevo 1 need you.
check_assertion_22() {
  grep -qE "ledger +REPORT IN" "$OUT/02-after-toast.txt" && \
  ! grep -qE "ledger +NEEDS YOU" "$OUT/02-after-toast.txt" && \
  grep -q "relevo 1 need you" "$OUT/02-after-toast.txt"
}
assert 22 "02-after-toast shows ledger REPORT IN and relevo 1 need you" check_assertion_22

# 23. ledger's delivered report raises the report-in toast with its round.
check_assertion_23() {
  grep -q "ledger r2 report in, delivered to chat" "$OUT/02-after-toast.txt"
}
assert 23 "02-after-toast shows the ledger r2 report-in toast" check_assertion_23

# 24. the sidebar row for landing (display ACTIVE, no word) shows no ACTIVE
#     word: §2 drops the word for ACTIVE, so landing's row carries only its dot
#     and name.
check_assertion_24() {
  grep -q "landing" "$OUT/01-session.txt" && \
  ! grep -qE "landing.*ACTIVE" "$OUT/01-session.txt"
}
assert 24 "01-session landing row shows no ACTIVE word" check_assertion_24

# 25. In 05d-prompt.ansi, the prompt fixture's heading line carries a non-empty
#     SGR sequence that differs from the one on a plain paragraph line. The
#     plain capture shows the heading words without the leading "# ".
check_assertion_25() {
  local head_sgr body_sgr
  head_sgr="$(grep -a -m1 "Plan for webshop" "$OUT/05d-prompt.ansi" | grep -a -oE $'\x1b\\[[0-9;]*m' | head -1 || true)"
  body_sgr="$(grep -a -m1 "Implement cart" "$OUT/05d-prompt.ansi" | grep -a -oE $'\x1b\\[[0-9;]*m' | head -1 || true)"
  [ -n "$head_sgr" ] && [ -n "$body_sgr" ] && \
  [ "$head_sgr" != "$body_sgr" ] && \
  grep -qE "^[[:space:]]*Plan for webshop r4" "$OUT/05d-prompt.txt"
}
assert 25 "05d-prompt heading carries different SGR sequence from plain body" check_assertion_25

# 25b. In 05-binding-report.ansi, the report fixture's heading line carries a
#      non-empty SGR sequence that differs from the one on a plain paragraph
#      line. The plain capture shows the heading words without the leading "# ".
check_assertion_25b() {
  local head_sgr body_sgr
  head_sgr="$(grep -a -m1 "Report" "$OUT/05-binding-report.ansi" | grep -a -oE $'\x1b\\[[0-9;]*m' | head -1 || true)"
  body_sgr="$(grep -a -m1 "checkout flow" "$OUT/05-binding-report.ansi" | grep -a -oE $'\x1b\\[[0-9;]*m' | head -1 || true)"
  [ -n "$head_sgr" ] && [ -n "$body_sgr" ] && \
  [ "$head_sgr" != "$body_sgr" ] && \
  grep -qE "^[[:space:]]*Report([[:space:]]|$)" "$OUT/05-binding-report.txt"
}
assert 25b "05-binding-report heading carries different SGR sequence from plain body" check_assertion_25b

# 26. In 05e-transcript (.ansi capture), the tool-name line and the
#     ⎿ error: line carry different colour escapes (compare the SGR codes
#     before each; they must differ), and neither is the default.
check_assertion_26() {
  local tool_sgr err_sgr
  tool_sgr="$(grep -a -m1 "●" "$OUT/05e-transcript.ansi" | grep -a -oE $'\x1b\\[[0-9;]*m' | head -1 || true)"
  err_sgr="$(grep -a -m1 "error:" "$OUT/05e-transcript.ansi" | grep -a -oE $'\x1b\\[[0-9;]*m' | head -1 || true)"
  [ -n "$tool_sgr" ] && [ -n "$err_sgr" ] && \
  [ "$tool_sgr" != "$err_sgr" ] && \
  [ "$tool_sgr" != $'\x1b[0m' ] && [ "$tool_sgr" != $'\x1b[39m' ] && \
  [ "$err_sgr" != $'\x1b[0m' ] && [ "$err_sgr" != $'\x1b[39m' ]
}
assert 26 "05e-transcript tool line and error line carry different non-default colour escapes" check_assertion_26

# 27. Stale report: in the fake relevo, make show <name> --report for ledger
#     return Missing: true / empty Text on its first call and the real report
#     afterwards; open ledger's report tab before status-2 (capture shows
#     (no report)), then again after status-2 has flipped ledger to
#     report_in -> the report text appears (capture 05f-ledger-after).
check_assertion_27() {
  grep -q "(no report)" "$OUT/01b-ledger-stale.txt" && \
  grep -q "checkout flow implementation is ready" "$OUT/05f-ledger-after.txt" && \
  ! grep -q "(no report)" "$OUT/05f-ledger-after.txt"
}
assert 27 "ledger shows (no report) before status-2 and real report after status-2" check_assertion_27

# 29. The fixture's "## Scope" heading also loses its marker: the plain capture
#     shows "Scope" on its own and nowhere shows "## Scope".
check_assertion_29() {
  grep -qE "^[[:space:]]*Scope([[:space:]]|$)" "$OUT/05d-prompt.txt" && \
  ! grep -qF "## Scope" "$OUT/05d-prompt.txt"
}
assert 29 "05d-prompt shows the ## heading without its marker" check_assertion_29

# 30. The fixture's "---" line renders as a rule of ─ characters, not as raw
#     dashes.
check_assertion_30() {
  grep -qE "─{3,}" "$OUT/05d-prompt.txt" && \
  ! grep -qE "^[[:space:]]*---[[:space:]]*$" "$OUT/05d-prompt.txt"
}
assert 30 "05d-prompt shows the --- line as a ─ rule" check_assertion_30

# 31. The fixture's link renders as its text followed by the url in
#     parentheses; the raw "](url)" marker appears nowhere.
check_assertion_31() {
  grep -qF "design notes (https://example.com/design)" "$OUT/05d-prompt.txt" && \
  ! grep -qF "](" "$OUT/05d-prompt.txt"
}
assert 31 "05d-prompt shows the link text then (url) and no ](" check_assertion_31

# 32. The ledger report's markdown table renders as a table: a header row, a
#     ─┼─ separator and a body row, with no raw pipe left.
check_assertion_32() {
  grep -qE "Step +│ +Text +│ +Status" "$OUT/05f-ledger-after.txt" && \
  grep -q "─┼─" "$OUT/05f-ledger-after.txt" && \
  grep -qE "1 +│ +sleep 120 +│ +done" "$OUT/05f-ledger-after.txt" && \
  ! grep -qE "^[[:space:]]*\|" "$OUT/05f-ledger-after.txt"
}
assert 32 "05f-ledger-after renders the markdown table" check_assertion_32

# 33. The closing ```relevo block renders styled: a muted header rule, the key
#     lines, and a blank value as an em dash; no triple backtick is left.
check_assertion_33() {
  grep -q "── relevo" "$OUT/05f-ledger-after.txt" && \
  grep -q "status: done" "$OUT/05f-ledger-after.txt" && \
  grep -q "halted_at: —" "$OUT/05f-ledger-after.txt" && \
  ! grep -qF '```' "$OUT/05f-ledger-after.txt"
}
assert 33 "05f-ledger-after styles the relevo block" check_assertion_33

# 34. The builder's own transcript text is styled: the new line's markers are
#     gone and its words show.
check_assertion_34() {
  grep -q "Step 1: sleep 120 done" "$OUT/05e-transcript.txt" && \
  ! grep -qF '**' "$OUT/05e-transcript.txt" && \
  ! grep -qF '`' "$OUT/05e-transcript.txt"
}
assert 34 "05e-transcript styles the builder's markdown text" check_assertion_34

# 35. ] and [ move the page's round and refetch that round's body.
check_assertion_35() {
  grep -q "webshop › r4" "$OUT/05g-round-next.txt" && \
  grep -q "webshop › r2" "$OUT/05h-round-prev.txt" && \
  grep -q "show webshop --json --prompt --round 4" "$LOG" && \
  grep -q -- "--prompt --round 2" "$LOG"
}
assert 35 "] and [ switch the round and refetch its body" check_assertion_35

# 36. Re-entering webshop drops the round picked on the earlier visit.
check_assertion_36() {
  grep -q "webshop › r3" "$OUT/05i-reenter.txt"
}
assert 36 "05i-reenter returns to webshop's route round r3" check_assertion_36

# 37. A PAUSED row with a delivered report shows PAUSED, not REPORT IN, and the
#     two words carry different colours in the sidebar. Take the last escape
#     sequence that precedes the word on its line (cut the line at the word
#     first, so the reset after it does not count); assert only that the two
#     sequences differ, not what they are.
check_assertion_37() {
  local parked_prefix parked_sgr ledger_prefix ledger_sgr
  grep -qE "parked.*PAUSED" "$OUT/03-fleet.txt" && \
  ! grep -qE "parked.*REPORT IN" "$OUT/03-fleet.txt" && \
  parked_prefix="$(grep -a -m1 "PAUSED" "$OUT/02-after-toast.ansi" | sed 's/PAUSED.*//' || true)" && \
  ledger_prefix="$(grep -a -m1 "REPORT IN" "$OUT/02-after-toast.ansi" | sed 's/REPORT IN.*//' || true)" && \
  parked_sgr="$(printf '%s' "$parked_prefix" | grep -a -oE $'\x1b\\[[0-9;]*m' | tail -1 || true)" && \
  ledger_sgr="$(printf '%s' "$ledger_prefix" | grep -a -oE $'\x1b\\[[0-9;]*m' | tail -1 || true)" && \
  [ -n "$parked_sgr" ] && [ -n "$ledger_sgr" ] && [ "$parked_sgr" != "$ledger_sgr" ]
}
assert 37 "03-fleet parked shows PAUSED in a different colour from REPORT IN" check_assertion_37

# 38. Every table line (header, separator, body rows -- the lines carrying │
#     or ┼) has its first │/┼ at the same character column as every other
#     such line, and its second │/┼ likewise. A cell that exactly fills its
#     column must not shift the rest of its row. Character positions, not
#     byte offsets: │ and ┼ are multi-byte in UTF-8.
check_assertion_38() {
  python3 - "$OUT/05f-ledger-after.txt" <<'PYEOF'
import sys

path = sys.argv[1]
positions = []
with open(path, encoding="utf-8") as f:
    for line in f:
        line = line.rstrip("\n")
        if "│" not in line and "┼" not in line:
            continue
        cols = [i for i, ch in enumerate(line) if ch in "│┼"]
        if len(cols) < 2:
            continue
        positions.append((cols[0], cols[1]))

if not positions:
    sys.exit(1)

first_col, second_col = positions[0]
for first, second in positions:
    if first != first_col or second != second_col:
        sys.exit(1)

sys.exit(0)
PYEOF
}
assert 38 "05f-ledger-after keeps every table column aligned" check_assertion_38

# 39. (F1) The fake log has a line exactly send --name webshop --file a[b]/plan a.md,
#     and 05j-sent.txt's header still contains webshop › r3.
check_assertion_39() {
  grep -qxF "send --name webshop --file a[b]/plan a.md" "$LOG" && \
  grep -q "webshop › r3" "$OUT/05j-sent.txt"
}
assert 39 "send plan file ran and 05j-sent header contains webshop › r3" check_assertion_39

# 40. (F1) In 11-fleet-after-dialog.txt, the selected row (the line starting with
#     optional space then › ) is webshop.
check_assertion_40() {
  grep -qE "^[[:space:]]*›[[:space:]]+webshop\b" "$OUT/11-fleet-after-dialog.txt"
}
assert 40 "11-fleet-after-dialog selected row is webshop" check_assertion_40

# 41. (F2) The round row in 05-binding-report.txt has no r7. Assertion 21's
#     r1 r2 r3 r4 still holds.
check_assertion_41() {
  local row
  row="$(grep -oE "\[ \] round.*" "$OUT/05-binding-report.txt" | head -1)"
  [ -n "$row" ] || return 1
  ! printf '%s\n' "$row" | grep -q "\br7\b" && \
  printf '%s\n' "$row" | grep -qE "r1 r2 r3 r4"
}
assert 41 "05-binding-report round row has no r7 and r1 r2 r3 r4 holds" check_assertion_41

# 42. (F3) 05d-prompt.txt has exactly one line made only of ─ and spaces. In
#     05f-ledger-after.txt, the line after the one containing ── relevo is not a
#     line made only of ─.
check_assertion_42() {
  local rule_count next_line
  rule_count="$(grep -cE '^[[:space:]]*─+[[:space:]]*$' "$OUT/05d-prompt.txt" || true)"
  [ "$rule_count" -eq 1 ] || return 1
  next_line="$(awk '/── relevo/{getline; print; exit}' "$OUT/05f-ledger-after.txt")"
  ! printf '%s\n' "$next_line" | grep -qE '^[[:space:]]*─+[[:space:]]*$'
}
assert 42 "05d-prompt has one rule line and relevo rule does not wrap in 05f-ledger-after" check_assertion_42

# 43. (F4) 05f-ledger-after.txt has a line matching commands_run:[[:space:]]*$
#     (no —) and a line containing - git status.
check_assertion_43() {
  grep -qE "commands_run:[[:space:]]*$" "$OUT/05f-ledger-after.txt" && \
  grep -qF -- "- git status" "$OUT/05f-ledger-after.txt"
}
assert 43 "05f-ledger-after shows commands_run without em-dash and - git status" check_assertion_43

# 44. (F6) 06-dialog.txt contains webshop · r3 · NEEDS YOU and does not contain
#     needs you ·.
check_assertion_44() {
  grep -q "webshop · r3 · NEEDS YOU" "$OUT/06-dialog.txt" && \
  ! grep -q "needs you ·" "$OUT/06-dialog.txt"
}
assert 44 "06-dialog contains webshop · r3 · NEEDS YOU and does not contain needs you ·" check_assertion_44

# 45. (F7) The fake log has at least 2 lines starting history --json --mastermind,
#     because status-2 changes rows and the recent list refetches.
check_assertion_45() {
  local count
  count="$(grep -cE "^history --json --mastermind" "$LOG" || true)"
  [ "$count" -ge 2 ]
}
assert 45 "fake log has at least 2 lines starting history --json --mastermind" check_assertion_45

# 46. (F8) 03-fleet.txt's recent list contains the local time of
#     2026-09-24T20:00:00Z, computed in the script with
#     date -d 2026-09-24T20:00:00Z +%H:%M.
check_assertion_46() {
  local local_time
  local_time="$(date -d 2026-09-24T20:00:00Z +%H:%M)"
  awk '/── recent/{after = 1; next} after' "$OUT/03-fleet.txt" | grep -q "$local_time"
}
assert 46 "03-fleet recent list contains local time of 2026-09-24T20:00:00Z" check_assertion_46

# 47. The fake log has a line exactly send --name webshop --file a/fleet [plan] a.md.
check_assertion_47() {
  grep -qxF "send --name webshop --file a/fleet [plan] a.md" "$LOG"
}
assert 47 "fake log has send --name webshop --file a/fleet [plan] a.md" check_assertion_47

# 48. In 05k-after-dialog-tab.txt, the selected tab (the [ … ] word in the tab
#     row) differs from the selected tab in 05j-sent.txt.
check_assertion_48() {
  local tab_j tab_k
  tab_j="$(grep -E '\bprompt\b.*\breport\b' "$OUT/05j-sent.txt" | grep -oE '\[ [^]]+ \]' | head -1 || true)"
  tab_k="$(grep -E '\bprompt\b.*\breport\b' "$OUT/05k-after-dialog-tab.txt" | grep -oE '\[ [^]]+ \]' | head -1 || true)"
  [ -n "$tab_j" ] && [ -n "$tab_k" ] && [ "$tab_j" != "$tab_k" ]
}
assert 48 "05k-after-dialog-tab selected tab differs from 05j-sent" check_assertion_48

# 49. (F9) 06-dialog.txt contains builder on opencode · question in and does
#     not contain webshop · r3 · question in.
check_assertion_49() {
  grep -q "builder on opencode · question in" "$OUT/06-dialog.txt" && \
  ! grep -q "webshop · r3 · question in" "$OUT/06-dialog.txt"
}
assert 49 "06-dialog hint names the actor and reason, not the name and round again" check_assertion_49

# Mouse is not driven here: tmux send-keys cannot deliver SGR mouse events
# reliably, so the clickable-row behaviour (onMouseDown on the sidebar and
# fleet rows) is verified by inspection of tui.tsx, not by this smoke.
echo "Captures written to: $OUT"

if [ "$FAILED" -ne 0 ]; then
  echo "Smoke test FAILED"
  exit 1
fi

echo "Smoke test PASSED (all 49 assertions passed)"
exit 0
