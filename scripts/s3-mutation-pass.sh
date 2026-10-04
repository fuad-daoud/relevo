#!/bin/sh
# The push/pull mutation pass: one mutation per behaviour test the wiring adds,
# each restored afterwards, so every named test is shown to fail on the
# condition it turns on.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(dirname -- "$here")
cd "$root"

# The log is scratch: a run that left it behind would show up as an untracked
# file in the round it is verifying.
log=${MUTATION_LOG:-}
if [ -z "$log" ]; then
	log=$(mktemp)
	trap 'rm -f "$log"' EXIT
fi

mutate() {
	label=$1
	file=$2
	from=$3
	to=$4
	pkg=$5
	run=$6
	printf '=== %s\n' "$label"
	cp "$file" "$file.orig"
	if ! grep -qF "$from" "$file"; then
		echo "MUTATION NOT APPLIED: anchor not found in $file" >&2
		mv "$file.orig" "$file"
		exit 1
	fi
	python3 - "$file" "$from" "$to" <<-'PY'
	import sys
	path, frm, to = sys.argv[1], sys.argv[2], sys.argv[3]
	src = open(path).read()
	open(path, "w").write(src.replace(frm, to, 1))
	PY
	if go test "$pkg" -run "$run" -count=1 >"$log" 2>&1; then
		echo "NOT PINNED: $label still passes"
		mv "$file.orig" "$file"
		exit 1
	fi
	if grep -qE 'build failed|setup failed' "$log"; then
		echo "MUTATION DID NOT COMPILE: $label" >&2
		head -5 "$log" >&2
		mv "$file.orig" "$file"
		exit 1
	fi
	grep -E '^(--- FAIL|FAIL|ok)' "$log" | head -5
	mv "$file.orig" "$file"
	echo "restored"
}

mutate "pull before push" \
	internal/sync/runner.go \
	'	if err := r.Client.Push(ctx); err != nil {' \
	'	if _, err := r.Client.Pull(ctx); err != nil {' \
	./internal/sync/ TestSyncPushThenPullOrder

mutate "a failure never records itself" \
	internal/sync/runner.go \
	'r.put(KeyLastTick, tick{At: now, OK: out.OK})' \
	'r.put(KeyLastTick, tick{At: now, OK: true})' \
	./internal/sync/ TestSyncRetryAfterFailure

mutate "drop the attention marker" \
	internal/sync/runner.go \
	'r.put(KeyAttention, attention{At: now, Message: authRefusedMessage})' \
	'' \
	./internal/sync/ 'TestSyncMarkersOnEveryOutcome|TestSyncAuthRefusalClearsOnTheNextGoodAttempt'

mutate "leave the stats snapshot unwritten" \
	internal/sync/runner.go \
	'r.put(KeyStats, out.Stats)' \
	'' \
	./internal/sync/ TestSyncSurfacesStatsToLocalKV

mutate "wait for the sync on the seal path" \
	internal/relevo/daemon.go \
	'		d.queueSync(ctx)' \
	'		d.runSync(ctx)' \
	./internal/relevo/ TestSyncNeverBlocksSeal

mutate "drive the network even when the seal moved nothing" \
	internal/relevo/daemon.go \
	'	if sealed > 0 {' \
	'	if sealed >= 0 {' \
	./internal/relevo/ TestSyncSealIsQuietWhenItSealedNothing

mutate "drop the idle window" \
	internal/relevo/sync_tick.go \
	'	closed := !d.syncLast.IsZero() && now.Sub(d.syncLast) < syncWindow' \
	'	closed := false' \
	./internal/relevo/ TestSyncIdleTickRunsOncePerWindow

echo "=== every mutation failed the test that turns on it, and restored"
