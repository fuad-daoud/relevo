#!/bin/sh
# scripts/check-name.sh -- refuses the old name "relay" in the live tree.
#
# relevo was called relay. This guard fails when that name appears in a
# tracked file, so a branch written against the old name cannot bring it back.
#
# Only the index and the working tree are checked (`git grep` reads tracked
# files), and the generated files and this guard's own files are exempt (see
# the pathspecs below). A line is allowed in full
# when it names the site hostname or carries the marker "name-guard: legacy";
# in Markdown, everything between a "<!-- name-guard: off -->" marker and the
# next "<!-- name-guard: on -->" is allowed too.
set -eu

# RELAY and Relay are names, a bare `relay` is a name, but the verb
# forms (relays, relayed, relaying) are English and never matched.
pattern='(RELAY|Relay)([^a-z]|$)|(^|[^A-Za-z]|\\[nt])relay([^a-z]|$)'
marker='name-guard: legacy'
host='relay-site.fuad-daoud.com'

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# 1. Every tracked hit outside the exempt paths, as path:line:text. git grep
#    exits 1 when nothing matches, which is a clean tree, not a failure.
git grep -nIE "$pattern" -- . \
	':(exclude)go.sum' \
	':(exclude)internal/shipped/agents.sha256' \
	':(exclude)scripts/check-name.sh' \
	':(exclude)scripts/check-name_test.sh' \
	':(exclude)internal/harness/install_test.go' \
	> "$work/hits" || true

# 2. Every Markdown line inside an off/on block, as path:line. A single pass
#    per file is enough because the markers nest nowhere.
: > "$work/off"
git ls-files -- '*.md' | while IFS= read -r f; do
	awk -v path="$f" '
		/<!-- name-guard: off -->/ { off = 1; next }
		/<!-- name-guard: on -->/  { off = 0; next }
		off { print path ":" FNR }
	' "$f"
done >> "$work/off"

# 3. Drop the allowed lines, print the rest. The off/on lookup keys on the
#    path and line the hit names, so it never swallows a hit in another file.
awk -v offfile="$work/off" -v marker="$marker" -v host="$host" '
	FILENAME == offfile { off[$0] = 1; next }
	index($0, host) > 0 || index($0, marker) > 0 { next }
	{
		p = index($0, ":")
		q = index(substr($0, p + 1), ":")
		if (substr($0, 1, p + q - 1) in off) next
		print
	}
' "$work/off" "$work/hits" > "$work/bad"

if [ -s "$work/bad" ]; then
	cat "$work/bad"
	n=$(wc -l < "$work/bad" | tr -d ' ')
	echo "check-name: $n relay name(s) outside the legacy allowlist"
	exit 1
fi
echo "check-name: ok"
