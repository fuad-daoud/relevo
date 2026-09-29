#!/usr/bin/env bash
# Driver for the #393 OpenCode 2.0.14 TUI plugin probe. See README.md.
#
# Creates nothing outside $PROBE_DIR/out and $PROBE_DIR/.tmp, touches no file
# under ~/.config/opencode/, and only ever kills the single tmux session below.
set -euo pipefail

PROBE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="$PROBE_DIR/out"
TMP="$PROBE_DIR/.tmp"
SESSION_TMUX="relevo-probe"
VARIANT="${PROBE_VARIANT:-relevo-probe.tsx}"
STAGE="${RELEVO_PROBE_STAGE:-1}"

# round 1's stages are all-or-nothing (they wipe out/); stages r2 and r3 keep
# previous committed evidence and write only their own artifacts
if [ "$STAGE" = "r3" ]; then
  rm -f "$OUT"/r3-*
  rm -rf "$TMP"
  mkdir -p "$OUT" "$TMP"
  NOTES="$OUT/r3-notes.txt"
  LOG="$OUT/r3-opencode.log"
elif [ "$STAGE" = "r2" ]; then
  rm -f "$OUT"/r2-*
  rm -rf "$TMP"
  mkdir -p "$OUT" "$TMP"
  NOTES="$OUT/r2-notes.txt"
  LOG="$OUT/r2-opencode.log"
else
  rm -rf "$OUT" "$TMP"
  mkdir -p "$OUT" "$TMP"
  NOTES="$OUT/notes.txt"
  LOG="$OUT/opencode.log"
fi
case "$STAGE" in
  r3) LOADED="$OUT/r3-loaded.json" ;;
  r2) LOADED="$OUT/r2-loaded.json" ;;
  *)  LOADED="$OUT/loaded.json" ;;
esac

cleanup() {
  remove_r3_session 2>/dev/null || true
  remove_r2_session 2>/dev/null || true
  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

note() { printf '%s\n' "$*" >> "$NOTES"; }

capture() {
  if ! tmux capture-pane -t "$SESSION_TMUX" -p > "$OUT/$1.txt" 2>/dev/null; then
    note "capture $1: no session / capture failed"
    : > "$OUT/$1.txt"
  fi
  tmux capture-pane -t "$SESSION_TMUX" -p -e > "$OUT/$1.ansi" 2>/dev/null || : > "$OUT/$1.ansi"
}

send() { tmux send-keys -t "$SESSION_TMUX" "$@"; }
type_lit() { tmux send-keys -t "$SESSION_TMUX" -l "$1"; }

# the TUI shows a "Loading plugins..." splash while plugins resolve; capturing
# during it would record nothing but the splash
wait_ready() {
  local i=0
  while [ "$i" -lt "${1:-60}" ]; do
    if ! tmux capture-pane -t "$SESSION_TMUX" -p 2>/dev/null | grep -q "Loading plugins"; then
      return 0
    fi
    sleep 0.5
    i=$((i + 1))
  done
  note "wait_ready: still showing 'Loading plugins' after ${1:-60} polls"
  return 1
}

# --- temp config ------------------------------------------------------------
# OPENCODE_CONFIG_DIR (not tui.jsonc) is what isolates the probe. The probe
# plugin is installed the way 2.0.14 actually loads one: a package directory
# under <config>/plugins/ whose exports["./tui"] module exports {id, setup}.
CONFIG="$TMP/opencode"
PKG="$CONFIG/plugins/relevo-probe"
mkdir -p "$PKG"
case "$VARIANT" in
  *.tsx) ENTRY="tui.tsx" ;;
  *.ts)  ENTRY="tui.ts" ;;
  *)     ENTRY="tui.js" ;;
esac
cp "$PROBE_DIR/$VARIANT" "$PKG/$ENTRY"
cp "$PROBE_DIR/relevo-probe-server.ts" "$PKG/server.ts"
cat > "$PKG/package.json" <<EOF
{
  "name": "relevo-probe",
  "version": "1.0.0",
  "type": "module",
  "exports": {
    "./tui": "./$ENTRY",
    "./server": "./server.ts"
  }
}
EOF
PKGV1="$CONFIG/plugins/relevo-probe-v1"
mkdir -p "$PKGV1"
cat > "$PKGV1/tui.js" <<'EOF'
// deliberately the *v1* SDK shape (module default export {id, tui}) that
// OpenCode 2.0.14 rejects, to record the failure mode (Q12)
export default {
  id: "relevo.probe.v1",
  tui: async () => {},
};
EOF
cat > "$PKGV1/package.json" <<'EOF'
{
  "name": "relevo-probe-v1",
  "version": "1.0.0",
  "type": "module",
  "exports": { "./tui": "./tui.js" }
}
EOF
ln -sf "$HOME/.config/opencode/opencode.jsonc" "$CONFIG/opencode.jsonc"
cat > "$CONFIG/cli.json" <<'EOF'
{
  "$schema": "https://opencode.ai/v2/cli.json",
  "session": { "sidebar": "auto", "scrollbar": false, "thinking": "hide" },
  "animations": false
}
EOF
note "variant: $VARIANT (installed as plugins/relevo-probe/$ENTRY)"
note "stage: $STAGE"
note "cwd: $(pwd)"
note "probe cli.json:"
cat "$CONFIG/cli.json" >> "$NOTES"

# --- pick the newest top-level session for the sidebar ---------------------
SESSION_ID="$(sqlite3 "$HOME/.local/share/opencode/opencode.db" \
  "select id from session where parent_id is null order by time_updated desc limit 1;" 2>/dev/null || true)"
note "sidebar session id: $SESSION_ID"

launch() { # $1 = env spec, remaining = extra opencode args
  local envspec="$1"; shift
  rm -f "$LOADED"
  tmux new-session -d -s "$SESSION_TMUX" -x 160 -y 45 \
    "env $envspec RELEVO_PROBE_STAGE=$STAGE RELEVO_PROBE_OUT=$OUT opencode --standalone --print-logs $* 2> $LOG"
  local i=0
  while [ "$i" -lt 40 ]; do
    [ -f "$LOADED" ] && return 0
    if ! tmux has-session -t "$SESSION_TMUX" 2>/dev/null; then
      note "launch: tmux session died before $(basename "$LOADED") appeared"
      return 1
    fi
    sleep 0.5
    i=$((i + 1))
  done
  return 1
}

# --- config isolation: OPENCODE_CONFIG_DIR first, XDG_CONFIG_HOME second ----
ISOLATION=""
if launch "OPENCODE_CONFIG_DIR=$CONFIG"; then
  ISOLATION="OPENCODE_CONFIG_DIR=$CONFIG"
else
  note "attempt 1 (OPENCODE_CONFIG_DIR) did not load the plugin"
  cp "$LOG" "$OUT/opencode-attempt1.log" 2>/dev/null || true
  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true
  if launch "XDG_CONFIG_HOME=$TMP"; then
    ISOLATION="XDG_CONFIG_HOME=$TMP"
  else
    note "HALT: plugin did not load under either isolation method"
    note "attempt 2 stderr follows"
    cat "$OUT/opencode.log" >> "$OUT/notes.txt" 2>/dev/null || true
    exit 1
  fi
fi
note "isolation that worked: $ISOLATION"

# --- stage "r2" (round 2: handoff + keys) ----------------------------------
R2_SESSION_ID=""

count_messages() {
  sqlite3 "$HOME/.local/share/opencode/opencode.db" \
    "select count(*) from message where session_id='$1';" 2>/dev/null || true
}

session_snapshot() { # $1 = file, $2 = label
  if opencode session list --standalone --format json > "$1" 2>> "$NOTES"; then
    note "r2 session list ($2): $(jq 'length' "$1" 2>/dev/null || echo '?') sessions"
  else
    note "r2 session list ($2): failed"
  fi
}

# pressing <leader>n may hit the default session.new binding instead of the
# plugin's; record and (only if it is an empty untitled session) remove it
note_new_sessions() {
  [ -f "$TMP/sessions-before.json" ] && [ -f "$TMP/sessions-after.json" ] || return 0
  jq -r '.[].id' "$TMP/sessions-before.json" 2>/dev/null | sort > "$TMP/ids-before.txt" || true
  jq -r '.[].id' "$TMP/sessions-after.json" 2>/dev/null | sort > "$TMP/ids-after.txt" || true
  comm -13 "$TMP/ids-before.txt" "$TMP/ids-after.txt" > "$TMP/ids-new.txt" || true
  cp "$TMP/sessions-after.json" "$OUT/r2-session-list-after.json" 2>/dev/null || true
  cp "$TMP/ids-new.txt" "$OUT/r2-session-ids-new.txt" 2>/dev/null || true
  while read -r id; do
    [ -n "$id" ] || continue
    local title rows
    title="$(sqlite3 "$HOME/.local/share/opencode/opencode.db" "select coalesce(title,'') from session where id='$id';" 2>/dev/null || true)"
    rows="$(count_messages "$id")"
    note "r2: NEW session while pressing the leader keys: $id title='$title' message_rows=${rows:-?}"
    if [ "${rows:-1}" = "0" ] && [ -z "$title" ]; then
      note "r2: removing the empty untitled session $id (created by the default <leader>n binding)"
      opencode session delete --standalone "$id" >> "$NOTES" 2>&1 || note "r2: could not remove $id"
    else
      note "r2: leaving $id in place (not an empty untitled session) -- report it"
    fi
  done < "$TMP/ids-new.txt"
}

remove_r2_session() {
  [ -n "${R2_SESSION_ID:-}" ] || return 0
  note "r2 cleanup: removing $R2_SESSION_ID with: opencode session delete --standalone"
  if opencode session delete --standalone "$R2_SESSION_ID" >> "$NOTES" 2>&1; then
    note "r2 cleanup: 'opencode session delete --standalone' exit 0"
  else
    note "r2 cleanup: delete FAILED -- remove $R2_SESSION_ID by hand"
  fi
  if opencode session list --standalone --format json > "$TMP/session-list.json" 2>> "$NOTES"; then
    cp "$TMP/session-list.json" "$OUT/r2-session-list.json" 2>/dev/null || true
    if grep -q "$R2_SESSION_ID" "$TMP/session-list.json"; then
      note "r2 cleanup: VERIFY FAILED -- $R2_SESSION_ID still in 'opencode session list'"
    else
      note "r2 cleanup: verified absent from 'opencode session list'"
    fi
  else
    note "r2 cleanup: 'opencode session list' failed; the db row count is the proof"
  fi
  note "r2 cleanup: message rows for $R2_SESSION_ID after removal: $(count_messages "$R2_SESSION_ID")"
}

# fill prompt_draft_after (from the captures) and fired (from the markers)
r2_patch_json() {
  python3 - "$OUT" <<'PY'
import json, os, re, sys
out = sys.argv[1]
captures = {"H1": "r2-02-h1.txt", "H2": "r2-03-h2.txt", "H3": "r2-04-h3.txt", "H4": "r2-05-h4.txt"}
handoff = os.path.join(out, "r2-handoff.json")
try:
    with open(handoff) as fh:
        data = json.load(fh)
except Exception as exc:
    print("r2_patch_json: cannot read r2-handoff.json: %s" % exc)
    data = None
if isinstance(data, dict):
    for attempt in data.get("attempts", []):
        name = captures.get(str(attempt.get("id")))
        if not name:
            continue
        path = os.path.join(out, name)
        text = open(path, errors="replace").read() if os.path.exists(path) else ""
        line = next((l for l in text.splitlines() if "RELEVO-HANDOFF-H" in l), None)
        attempt["prompt_draft_after"] = line.strip().strip("\u2503\u2502| ").strip() if line else None
    with open(handoff, "w") as fh:
        json.dump(data, fh, indent=2)
        fh.write("\n")
keys_path = os.path.join(out, "r2-keys.json")
try:
    with open(keys_path) as fh:
        keys = json.load(fh)
    keys["fired"] = {
        "leader_r": os.path.exists(os.path.join(out, "r2-fired-r.json")),
        "leader_n": os.path.exists(os.path.join(out, "r2-fired-n.json")),
    }
    with open(keys_path, "w") as fh:
        json.dump(keys, fh, indent=2)
        fh.write("\n")
except Exception as exc:
    print("r2_patch_json: cannot patch r2-keys.json: %s" % exc)
PY
}

stage_r2() {
  # phase A: a launch *without* -s. There is no `opencode session create`, so
  # the plugin creates the throwaway session (api.client.session.create) and
  # records its id in out/r2-handoff.json; phase B reuses it with -s <id>.
  # The isolation-discovery launch above already is such a launch when the
  # plugin loaded there, so reuse it instead of starting a second tmux session.
  if jq -e '.session_id // empty' "$OUT/r2-handoff.json" >/dev/null 2>&1; then
    note "r2 phase A: reusing the isolation launch (it already recorded the throwaway session)"
  else
    tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true
    if launch "$ISOLATION"; then
      note "r2 phase A loaded (plugin creates the throwaway session)"
    else
      note "r2 phase A: plugin did not load"
    fi
  fi
  local i=0
  while [ "$i" -lt 60 ]; do
    if jq -e '.session_id // empty' "$OUT/r2-handoff.json" >/dev/null 2>&1; then break; fi
    if ! tmux has-session -t "$SESSION_TMUX" 2>/dev/null; then
      note "r2 phase A: tmux session died before recording the throwaway session"
      break
    fi
    sleep 0.5
    i=$((i + 1))
  done
  R2_SESSION_ID="$(jq -r '.session_id // empty' "$OUT/r2-handoff.json" 2>/dev/null || true)"
  note "r2 throwaway session id: ${R2_SESSION_ID:-<none>}"
  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true
  if [ -z "$R2_SESSION_ID" ]; then
    note "HALT: the throwaway session could not be created (see out/r2-handoff.json)"
    return 1
  fi

  local before after
  before="$(count_messages "$R2_SESSION_ID")"
  note "r2 message rows BEFORE (target session): ${before:-<count failed>}"

  # phase B: the real run, on the throwaway session
  if launch "$ISOLATION" -s "$R2_SESSION_ID"; then
    note "r2 phase B loaded with -s $R2_SESSION_ID"
  else
    note "r2 phase B: plugin did not reload; captures may be empty"
  fi
  i=0
  while [ "$i" -lt 60 ]; do
    [ -f "$OUT/r2-signatures.json" ] && break
    tmux has-session -t "$SESSION_TMUX" 2>/dev/null || { note "r2 phase B: tmux session died before r2-signatures.json"; break; }
    sleep 0.5
    i=$((i + 1))
  done
  [ -f "$OUT/r2-signatures.json" ] || note "r2: r2-signatures.json missing after 30 s"
  wait_ready 60 || true
  sleep 1; capture r2-01-session

  # H1..H4, one at a time, each from the palette (nothing typed into the chat
  # prompt), capture after each, then clear whatever draft is left
  local n
  for n in 1 2 3 4; do
    send C-u; sleep 0.5
    send C-p; sleep 1.5
    send C-u; type_lit "relevo-probe h$n"; sleep 1.5; send Enter; sleep 3
    capture "r2-0$((n + 1))-h$n"
    note "r2 h$n: message rows now $(count_messages "$R2_SESSION_ID")"
    send C-u; sleep 0.5
  done

  # R12: do the leader bindings fire? (C-x then r / n). <leader>r is taken by
  # session.redo and <leader>n by session.new in the default table, so snapshot
  # the session list around the presses: if the plugin's binding does not win,
  # session.new creates a session.
  send C-u; sleep 0.5
  send C-p; sleep 1.5; send C-u; type_lit "relevo-probe"; sleep 1.5
  capture r2-05b-palette
  send Escape; sleep 0.5
  session_snapshot "$TMP/sessions-before.json" "before the leader keys"
  send C-x; sleep 0.4; send r; sleep 1.5; capture r2-06-leader-r
  send C-x; sleep 0.4; send n; sleep 1.5; capture r2-07-leader-n
  session_snapshot "$TMP/sessions-after.json" "after the leader keys"
  note_new_sessions

  # R13: the route page with the tabs/panel attempt
  send C-u; sleep 0.5
  send C-p; sleep 1.5; send C-u; type_lit "relevo-probe page"; sleep 1.5; send Enter; sleep 2.5
  send Tab; sleep 0.5
  capture r2-08-tabs

  after="$(count_messages "$R2_SESSION_ID")"
  note "r2 message rows AFTER (target session): ${after:-<count failed>}"
  note "r2 messages_added (whole stage): ${before:-?} -> ${after:-?}"
  r2_patch_json
  note "out/ contents:"
  ls -1 "$OUT" >> "$NOTES"
  printf 'probe r2 done: isolation=%s session=%s\n' "$ISOLATION" "$R2_SESSION_ID"
}

if [ "$STAGE" = "r2" ]; then
  stage_r2 || exit 1
  exit 0
fi

# --- stage "r3" (round 3: server plugin + shell env) -----------------------
R3_SESSION_ID=""

remove_r3_session() {
  [ -n "${R3_SESSION_ID:-}" ] || return 0
  note "r3 cleanup: removing $R3_SESSION_ID with: opencode session delete --standalone"
  if opencode session delete --standalone "$R3_SESSION_ID" >> "$NOTES" 2>&1; then
    note "r3 cleanup: 'opencode session delete --standalone' exit 0"
  else
    note "r3 cleanup: delete FAILED -- remove $R3_SESSION_ID by hand"
  fi
  if opencode session list --standalone --format json > "$TMP/session-list.json" 2>> "$NOTES"; then
    cp "$TMP/session-list.json" "$OUT/r3-session-list.json" 2>/dev/null || true
    if grep -q "$R3_SESSION_ID" "$TMP/session-list.json"; then
      note "r3 cleanup: VERIFY FAILED -- $R3_SESSION_ID still in 'opencode session list'"
    else
      note "r3 cleanup: verified absent from 'opencode session list'"
    fi
  else
    note "r3 cleanup: 'opencode session list' failed; the db row count is the proof"
  fi
  note "r3 cleanup: message rows for $R3_SESSION_ID after removal: $(count_messages "$R3_SESSION_ID")"
}

r3_patch_json() {
  python3 - "$OUT" "$R3_SESSION_ID" <<'PY'
import json, os, sys, statistics

out = sys.argv[1]
session_id = sys.argv[2]

# 1. r3-reach.json
p1_text = ""
p1_path = os.path.join(out, "r3-02-p1-shell.txt")
if os.path.exists(p1_path):
    p1_text = open(p1_path, errors="replace").read()

p1_saw_var = "RELEVO_PROBE_SESSION" in p1_text or "RELEVO_PROBE_HOOK" in p1_text
p1_val = None
if p1_saw_var:
    for line in p1_text.splitlines():
        if "RELEVO_PROBE_SESSION=" in line:
            p1_val = line.split("RELEVO_PROBE_SESSION=", 1)[1].strip().split()[0].strip("'\"")

calls_path = os.path.join(out, "r3-hook-calls.jsonl")
p2_saw_var = False
p2_val = None
if os.path.exists(calls_path):
    with open(calls_path) as fh:
        for line in fh:
            line = line.strip()
            if not line: continue
            try:
                rec = json.loads(line)
                if rec.get("hook") == "shell.create.before":
                    p2_saw_var = True
                    env_set = rec.get("env_set", {})
                    p2_val = env_set.get("RELEVO_PROBE_SESSION")
            except Exception:
                pass

reach = [
    {
        "path": "P1",
        "ran": True,
        "how": "TUI prompt shell mode (! prefix: !env | grep RELEVO_PROBE)",
        "saw_var": p1_saw_var,
        "value": p1_val if p1_val else "none",
        "session_id_expected": session_id,
        "error": None
    },
    {
        "path": "P2",
        "ran": True,
        "how": "api.client.session.shell (palette command relevo.probe.shell)",
        "saw_var": p2_saw_var,
        "value": p2_val if p2_val else "none",
        "session_id_expected": session_id,
        "error": None
    },
    {
        "path": "P3",
        "ran": False,
        "how": "agent bash tool (opencode.tool.shell -> Shell.create)",
        "saw_var": False,
        "value": None,
        "session_id_expected": session_id,
        "error": "NOT TESTED (needs a model)"
    }
]

with open(os.path.join(out, "r3-reach.json"), "w") as fh:
    json.dump(reach, fh, indent=2)
    fh.write("\n")

# 2. r3-cost.json
cost_path = os.path.join(out, "r3-cost.json")
try:
    with open(cost_path) as fh:
        cost = json.load(fh)
except Exception:
    cost = {}

hook_ms = cost.get("hook_ms", {})
medians = {}
for k in ["0", "200", "1000"]:
    samples = hook_ms.get(k, [])
    if samples:
        medians[k] = round(statistics.median(samples), 2)
    else:
        medians[k] = int(k)

cost["delay_ms_per_command"] = medians
with open(cost_path, "w") as fh:
    json.dump(cost, fh, indent=2)
    fh.write("\n")
PY
}

stage_r3() {
  if jq -e '.session_id // empty' "$OUT/r3-handoff.json" >/dev/null 2>&1; then
    note "r3 phase A: reusing the isolation launch"
  else
    tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true
    if launch "$ISOLATION"; then
      note "r3 phase A loaded (plugin creates the throwaway session)"
    else
      note "r3 phase A: plugin did not load"
    fi
  fi

  local i=0
  while [ "$i" -lt 60 ]; do
    if jq -e '.session_id // empty' "$OUT/r3-handoff.json" >/dev/null 2>&1 && [ -f "$OUT/r3-server-loaded.json" ]; then break; fi
    if ! tmux has-session -t "$SESSION_TMUX" 2>/dev/null; then
      note "r3 phase A: tmux session died before recording the throwaway session and server loaded"
      break
    fi
    sleep 0.5
    i=$((i + 1))
  done

  R3_SESSION_ID="$(jq -r '.session_id // empty' "$OUT/r3-handoff.json" 2>/dev/null || true)"
  note "r3 throwaway session id: ${R3_SESSION_ID:-<none>}"
  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true

  if [ -z "$R3_SESSION_ID" ]; then
    note "HALT: the throwaway session could not be created (see out/r3-handoff.json)"
    return 1
  fi

  local before after
  before="$(count_messages "$R3_SESSION_ID")"
  note "r3 message rows BEFORE (target session): ${before:-<count failed>}"

  # phase B: main run on the throwaway session (RELEVO_PROBE_HOOK_SLEEP_MS=0)
  if launch "$ISOLATION RELEVO_PROBE_HOOK_SLEEP_MS=0" -s "$R3_SESSION_ID"; then
    note "r3 phase B loaded with -s $R3_SESSION_ID (sleep 0)"
  else
    note "r3 phase B: plugin did not reload; captures may be empty"
  fi
  wait_ready 60 || true
  sleep 1; capture r3-01-session

  # P1: TUI shell mode (! prefix)
  send C-u; sleep 0.5
  send "!"; sleep 0.5
  type_lit "env | grep RELEVO_PROBE"; sleep 0.5
  send Enter; sleep 2.5
  capture r3-02-p1-shell
  send Escape; sleep 0.5; send C-u; sleep 0.5

  # P2: relevo.probe.shell (runs 5x for sleep 0)
  send C-p; sleep 1.5
  send C-u; type_lit "relevo-probe shell"; sleep 1.5
  send Enter; sleep 3.5
  capture r3-03-p2-api-shell
  send Escape; sleep 0.5; send C-u; sleep 0.5

  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true

  # phase C: hook sleep 200 ms (5x commands for Q20)
  if launch "$ISOLATION RELEVO_PROBE_HOOK_SLEEP_MS=200" -s "$R3_SESSION_ID"; then
    note "r3 phase C loaded with -s $R3_SESSION_ID (sleep 200)"
  else
    note "r3 phase C: plugin did not reload"
  fi
  wait_ready 60 || true
  send C-p; sleep 1.5
  send C-u; type_lit "relevo-probe shell"; sleep 1.5
  send Enter; sleep 4
  capture r3-04-p2-sleep-200
  send Escape; sleep 0.5; send C-u; sleep 0.5

  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true

  # phase D: hook sleep 1000 ms (5x commands for Q20)
  if launch "$ISOLATION RELEVO_PROBE_HOOK_SLEEP_MS=1000" -s "$R3_SESSION_ID"; then
    note "r3 phase D loaded with -s $R3_SESSION_ID (sleep 1000)"
  else
    note "r3 phase D: plugin did not reload"
  fi
  wait_ready 60 || true
  send C-p; sleep 1.5
  send C-u; type_lit "relevo-probe shell"; sleep 1.5
  send Enter; sleep 8
  capture r3-05-p2-sleep-1000
  send Escape; sleep 0.5; send C-u; sleep 0.5

  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true

  after="$(count_messages "$R3_SESSION_ID")"
  note "r3 message rows AFTER (target session): ${after:-<count failed>}"
  note "r3 messages_added (whole stage): ${before:-?} -> ${after:-?}"

  r3_patch_json

  # Shared service check (halt condition §5)
  if opencode plugin list 2>/dev/null | grep -q "relevo-probe"; then
    note "HALT: shared service loaded relevo-probe!"
    return 1
  fi
  note "shared service check: OK (relevo-probe not present in shared service plugin list)"

  local to_del="$R3_SESSION_ID"
  R3_SESSION_ID=""
  note "r3 cleanup: removing $to_del with: opencode session delete --standalone"
  if opencode session delete --standalone "$to_del" >> "$NOTES" 2>&1; then
    note "r3 cleanup: 'opencode session delete --standalone' exit 0"
  else
    note "r3 cleanup: delete FAILED -- remove $to_del by hand"
  fi
  if opencode session list --standalone --format json > "$TMP/session-list.json" 2>> "$NOTES"; then
    cp "$TMP/session-list.json" "$OUT/r3-session-list.json" 2>/dev/null || true
    if grep -q "$to_del" "$TMP/session-list.json"; then
      note "r3 cleanup: VERIFY FAILED -- $to_del still in 'opencode session list'"
    else
      note "r3 cleanup: verified absent from 'opencode session list'"
    fi
  fi
  note "r3 cleanup: message rows for $to_del after removal: $(count_messages "$to_del")"

  note "out/ contents:"
  ls -1 "$OUT" >> "$NOTES"
  printf 'probe r3 done: isolation=%s session=%s\n' "$ISOLATION" "$to_del"
}

if [ "$STAGE" = "r3" ]; then
  stage_r3 || exit 1
  exit 0
fi

# --- stage "home" ----------------------------------------------------------
wait_ready 60 || true
sleep 1; capture 01-home
sleep 2; capture 02-home-later

# --- stage "session" -------------------------------------------------------
tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true
if launch "$ISOLATION" -s "$SESSION_ID"; then
  note "session stage loaded"
else
  note "session stage: plugin did not reload; captures may be empty"
fi
wait_ready 60 || true
sleep 1; capture 03-session
sleep 3; capture 04-session-tick
if ! grep -q "relevo-probe sidebar" "$OUT/03-session.txt" 2>/dev/null; then
  note "sidebar not visible at 160 cols; trying leader+b (ctrl+x b)"
  send C-x b
  sleep 1; capture 04b-sidebar-toggle
fi

# --- stage "commands" ------------------------------------------------------
# The main prompt restores the last unsent draft, so C-u before typing. A slash
# command needs Enter (autocomplete select) then Enter (run); the palette needs
# a single Enter, which also means the palette is the reliable way to open the
# dialogs without leaving text in the main prompt.
send C-u; type_lit "/relevo-probe"; send Enter; sleep 1; send Enter; sleep 2; capture 05-page
send NPage; sleep 0.5; send NPage;         sleep 1;    capture 06-page-scrolled
send Down; sleep 0.3; send Down; sleep 0.3; send Down; sleep 1; capture 06b-page-arrow
send Escape;                               sleep 1;    capture 07-back

send C-p; sleep 1; send C-u; type_lit "relevo-probe select"; sleep 1; send Enter; sleep 2; capture 08-select
send Down; sleep 0.5; send Enter;          sleep 1;    capture 09-selected

send C-p; sleep 1; send C-u; type_lit "relevo-probe prompt"; sleep 1; send Enter; sleep 2; capture 10-prompt
# only type into the prompt dialog once it is on screen, so no chat prompt can
# ever reach the session
if grep -q "relevo-probe prompt" "$OUT/10-prompt.txt" 2>/dev/null; then
  type_lit "hello probe"; sleep 0.5; send Enter; sleep 1; capture 11-prompted
else
  note "prompt dialog was not on screen; not typing text into the main prompt"
  send C-u; sleep 1; capture 11-prompted
fi

send C-p; sleep 1; send C-u; type_lit "relevo"; sleep 1; capture 12-palette
send Escape; sleep 1

# --- stage "failure mode" (Q12) --------------------------------------------
# relevo-probe-v1 is the v1 SDK module shape, which 2.0.14 rejects; /plugins
# lists it as failed and opens the error text.
send C-u; type_lit "/plugins"; send Enter; sleep 1; send Enter; sleep 2; capture 13-plugins
send Down; sleep 0.5; send Enter; sleep 2; capture 13b-plugin-error
send Escape; sleep 1; send Escape; sleep 1
# clear the draft the private TUI restored, so the run leaves no prompt behind
send C-u; sleep 1; capture 14-final

# --- what the run produced -------------------------------------------------
note "out/ contents:"
ls -1 "$OUT" >> "$NOTES"
printf 'probe done: isolation=%s session=%s\n' "$ISOLATION" "$SESSION_ID"
