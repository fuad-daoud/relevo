#!/bin/sh
# The S2 mutation pass: one mutation per behaviour test, each restored
# afterwards, so every named test is shown to fail on the condition it turns on.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(dirname -- "$here")
cd "$root"

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
	log=${MUTATION_LOG:-mutation.log}
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

mutate "accept an unknown field in the sync body" \
	internal/sync/settings.go \
	'	dec.DisallowUnknownFields()' \
	'' \
	./internal/config/ TestSyncSectionValidation

mutate "echo the token into a refusal" \
	internal/sync/token.go \
	'return fmt.Errorf("sync: set %s: %w", SecretToken, err)' \
	'return fmt.Errorf("sync: set %s (%s): %w", SecretToken, value, err)' \
	./internal/sync/ 'TestTokenNeverReachesAnError|TestTokenNeverLeavesMachine'

mutate "route one section into the shared file" \
	internal/config/never_syncs_test.go \
	'return path, shared, local, Open(local)' \
	'return path, shared, local, Open(shared)' \
	./internal/config/ 'TestConfigNeverSyncs|TestNoSectionBodyReachesTheSharedFile'

mutate "make the mapper read the wrong tick state" \
	internal/sync/sync.go \
	'case !s.LastTickOK:
		return TokenBehind' \
	'case !s.LastTickOK:
		return TokenOK' \
	./internal/sync/ 'TestStatuslineReadsLocalOnly|TestTokenIsTheWholeMapping'

mutate "reorder the fake's recorded calls" \
	internal/sync/fake.go \
	'f.Calls = append(f.Calls, "pull")' \
	'f.Calls = append(f.Calls, "push")' \
	./internal/sync/ TestSyncClientFake

echo "=== every mutation failed the test that turns on it, and restored"
