#!/bin/sh
# scripts/mutation-secrets.sh -- proves each rule of the secrets document fails
# when its condition is broken.
#
# Round 1 of the sync-secrets plan asks for a mutation check per new rule: what
# was broken, and the name of the test that noticed. Each case here edits one
# condition, runs the one test that pins it, and restores the file. A case that
# still passes has a test that does not reach its rule.
#
# Usage: sh scripts/mutation-secrets.sh [case]
#   case  run only the named case (default: all of them)
#
# Nothing here reaches a network and nothing spawns a harness: each case is a
# `go test` over the packages the round touched.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$here"

# The path a case runs under, and the PATH the tests get: git plus the Go
# toolchain, which is what CI has and what a new test must not need more than.
go_bin=$(command -v go)
test_path="/usr/bin:/bin:$(dirname "$go_bin")"

pass=0
fail=0

# run NAME EXPECTED_FAILING_TEST -- checks the named test passes as the tree
# stands.
run() {
	name=$1
	test=$2
	shift 2
	if PATH="$test_path" go test "$@" -run "$test" -count=1 > "$here/.mutation.out" 2>&1; then
		printf 'ok       %s: %s passes as built\n' "$name" "$test"
		pass=$((pass + 1))
	else
		printf 'BROKEN   %s: %s does not pass as built\n' "$name" "$test"
		sed 's/^/         /' "$here/.mutation.out" | head -20
		fail=$((fail + 1))
	fi
}

# mutate NAME TEST PKGS -- applies the caller's edit, requires the named test to
# FAIL, and restores the file whatever happens.
mutate() {
	name=$1
	test=$2
	pkgs=$3
	shift 3
	backup=$(mktemp -d)
	# Every file a case may edit, so one case cannot leave the next one building
	# against a half-applied mutation.
	for f in internal/config/secretdoc.go internal/config/secretstamp.go \
		internal/config/import.go internal/sync/secretcrypt.go internal/sync/r2.go \
		internal/relevo/syncdocs.go; do
		cp "$f" "$backup/$(basename "$f")"
	done
	"$@"
	# The package list is deliberately several words, so it is not quoted.
	# shellcheck disable=SC2086
	if PATH="$test_path" go test $pkgs -run "$test" -count=1 > "$here/.mutation.out" 2>&1; then
		printf 'NOT CAUGHT %s: %s still passes with the condition broken\n' "$name" "$test"
		fail=$((fail + 1))
	elif ! grep -q -- '--- FAIL: ' "$here/.mutation.out"; then
		# A build failure is not a catch: the case has to break a rule, not the
		# package, or the test that "noticed" never ran.
		printf 'NOT CAUGHT %s: the case does not build, so %s never ran\n' "$name" "$test"
		sed 's/^/         /' "$here/.mutation.out" | head -10
		fail=$((fail + 1))
	else
		printf 'caught   %s: %s fails when the condition is broken\n' "$name" "$test"
		printf '%s\n' "         $(grep -m1 -- '--- FAIL: ' "$here/.mutation.out" | sed 's/^--- FAIL: //;s/ (.*//')"
		pass=$((pass + 1))
	fi
	cp "$backup/secretdoc.go" internal/config/secretdoc.go
	cp "$backup/secretstamp.go" internal/config/secretstamp.go
	cp "$backup/import.go" internal/config/import.go
	cp "$backup/secretcrypt.go" internal/sync/secretcrypt.go
	cp "$backup/r2.go" internal/sync/r2.go
	cp "$backup/syncdocs.go" internal/relevo/syncdocs.go
	rm -rf "$backup"
}

# locked_is_empty answers an empty document instead of refusing. An exporter
# reads an absent key as one this machine no longer holds, so this is the
# mutation that would clear the fleet's secrets.
locked_is_empty() {
	python3 - <<'PY'
p = 'internal/config/secretdoc.go'
s = open(p).read()
old = '''	if !ok {
		return nil, fmt.Errorf("config: the %s document needs the sync key: %w", Secrets, sync.ErrNoKey)
	}'''
new = '''	if !ok {
		return map[string]json.RawMessage{}, nil
	}'''
assert old in s, 'the locked-Items refusal is not where this case expects it'
open(p, 'w').write(s.replace(old, new))
PY
}

# unstamped_client_key_exports drops the guard that keeps a client key no person
# stamped off the log.
unstamped_client_key_exports() {
	python3 - <<'PY'
p = 'internal/config/secretdoc.go'
s = open(p).read()
old = '''		if name == SecretClientKey && !d.stamped(name) {
			continue
		}
'''
assert old in s, 'the client.key stamp guard is not where this case expects it'
open(p, 'w').write(s.replace(old, ''))
PY
}

# off_list_names_land removes the allow-list check from the import path.
off_list_names_land() {
	python3 - <<'PY'
p = 'internal/config/secretdoc.go'
s = open(p).read()
old = '''	if !sync.AllowSecret(key) {
		return fmt.Errorf("config: %s: %w", key, ErrNotSharedSecret), nil
	}
'''
assert old in s, 'the import allow-list check is not where this case expects it'
open(p, 'w').write(s.replace(old, ''))
PY
}

# envelope_version_ignored accepts any envelope version, which is what would let a
# rekey writer's epoch field be read under this build's rules.
envelope_version_ignored() {
	python3 - <<'PY'
p = 'internal/sync/secretcrypt.go'
s = open(p).read()
old = '''	if env.V != secretEnvelopeVersion {'''
new = '''	if false && env.V != secretEnvelopeVersion {'''
assert old in s, 'the version check is not where this case expects it'
open(p, 'w').write(s.replace(old, new))
PY
}

# unknown_field_ignored drops the unknown-field refusal, so a v1 envelope
# carrying a field this build has no reader for would be read on this build's
# terms.
unknown_field_ignored() {
	python3 - <<'PY'
p = 'internal/sync/secretcrypt.go'
s = open(p).read()
old = '''		if field != "v" && field != "n" && field != "c" {'''
new = '''		if false && field != "v" && field != "n" && field != "c" {'''
assert old in s, 'the unknown-field check is not where this case expects it'
open(p, 'w').write(s.replace(old, new))
PY
}

# associated_data_dropped seals and opens with no associated data, so a
# ciphertext could be moved from one secret's name to another's.
associated_data_dropped() {
	python3 - <<'PY'
p = 'internal/sync/secretcrypt.go'
s = open(p).read()
s = s.replace('aead.Seal(nil, nonce, value, []byte(name))', 'aead.Seal(nil, nonce, value, nil)')
s = s.replace('aead.Open(nil, nonce, body, []byte(name))', 'aead.Open(nil, nonce, body, nil)')
open(p, 'w').write(s)
PY
}

# cap_ignored lets a value of any size be sealed, which is what would put a
# large secret on the log as a payload rather than a secret.
cap_ignored() {
	python3 - <<'PY'
p = 'internal/sync/secretcrypt.go'
s = open(p).read()
old = '''	if len(env) > SecretEnvelopeMax {
		return nil, fmt.Errorf("secrets %s: sealed value is %d bytes, over the %d byte cap: %w: %w",
			name, len(env), SecretEnvelopeMax, ErrSecretEnvelope, db.ErrInvalid)
	}
'''
assert old in s, 'the export cap is not where this case expects it'
open(p, 'w').write(s.replace(old, ''))
PY
}

# applied_secrets_stamp makes the arrival path stamp, which is what would hand the
# peer's own value straight back.
applied_secrets_stamp() {
	python3 - <<'PY'
p = 'internal/config/secretstamp.go'
s = open(p).read()
old = '''	return s.stampAndRecord(t, name, "applied", s.As(syncedSourcePrefix+label, "secret from "+label), false)'''
new = '''	return s.stampAndRecord(t, name, "applied", s.As(syncedSourcePrefix+label, "secret from "+label), true)'''
assert old in s, 'the applied write is not where this case expects it'
open(p, 'w').write(s.replace(old, new))
PY
}

# import_secrets_unstamped stops the config import from stamping the secret files
# it carries, which is how an imported client key would never reach a joiner.
import_secrets_unstamped() {
	python3 - <<'PY'
p = 'internal/config/import.go'
s = open(p).read()
old = '''		if err := sync.StampSecretWrites(t, []string{secret.name}, s.now); err != nil {
			return nil, err
		}
'''
new = '''		if err := sync.StampSecretWrites(t, nil, s.now); err != nil {
			return nil, err
		}
'''
assert old in s, 'the import stamping is not where this case expects it'
open(p, 'w').write(s.replace(old, new))
PY
}

# r2_flags_unstamped stops the enable from stamping the R2 credentials a person
# typed, which is how a joiner never receives them.
r2_flags_unstamped() {
	python3 - <<'PY'
p = 'internal/sync/r2.go'
s = open(p).read()
old = '''		return StampSecretWrites(t, r2Names(), func() time.Time { return now })'''
new = '''		return nil'''
assert old in s, 'the R2 stamping is not where this case expects it'
open(p, 'w').write(s.replace(old, new))
PY
}

# document_unbound drops the document from sharedDocs, which is the mutation the
# construction-versus-bind audit exists for.
document_unbound() {
	python3 - <<'PY'
p = 'internal/relevo/syncdocs.go'
s = open(p).read()
old = '''		machineConfig(shared).SecretsDoc(),
'''
assert old in s, 'the binding is not where this case expects it'
open(p, 'w').write(s.replace(old, ''))
PY
}

# supersede_carries_the_value makes the losing-value record value-bearing, the
# path this document must not use.
supersede_carries_the_value() {
	python3 - <<'PY'
p = 'internal/config/secretdoc.go'
s = open(p).read()
old = '''	changes, err := json.Marshal([]Change{{Path: "secret." + key, Op: "superseded"}})'''
new = '''	changes, err := json.Marshal([]Change{{Path: "secret." + key, Op: "add", After: value}})'''
assert old in s, 'the supersede change is not where this case expects it'
open(p, 'w').write(s.replace(old, new))
PY
}

only=${1:-all}

case "$only" in
all | locked)
	run "a locked machine refuses rather than answering empty" \
		TestItemsRefusesOnALockedMachine ./internal/config/
	mutate "a locked machine answers an empty document" \
		TestItemsRefusesOnALockedMachine ./internal/config/ locked_is_empty
	;;
esac

case "$only" in
all | stamp)
	run "an unstamped client key is not exported" \
		TestAnUnstampedClientKeyNeverCrossesTheLog ./internal/relevo/
	mutate "an unstamped client key is exported" \
		TestAnUnstampedClientKeyNeverCrossesTheLog ./internal/relevo/ unstamped_client_key_exports
	run "a person's edit stamps and signals" \
		TestAPersonsEditStampsAndSignals ./internal/config/
	mutate "an arrival stamps" \
		TestAnAppliedSecretIsNotStamped ./internal/config/ applied_secrets_stamp
	mutate "an import does not stamp the secret files it carries" \
		TestAnImportedSecretFileStamps ./internal/config/ import_secrets_unstamped
	run "the R2 flags a person typed reach a peer" \
		TestTheR2CredentialsAPersonTypedReachAPeer ./internal/relevo/
	mutate "the R2 flags an enable was given do not stamp" \
		TestTheR2CredentialsAPersonTypedReachAPeer ./internal/relevo/ r2_flags_unstamped
	run "a joiner receives the R2 credentials" \
		TestAJoinerReceivesTheR2Credentials ./internal/relevo/
	;;
esac

case "$only" in
all | allowlist)
	run "an off-list name is refused on import" \
		TestApplyRefusesAnOffListName ./internal/config/
	mutate "an off-list name lands" \
		TestApplyRefusesAnOffListName ./internal/config/ off_list_names_land
	run "a pre-existing allow-listed secret travels unstamped" \
		TestPreExistingAllowListedSecretsTravelUnstamped ./internal/config/
	;;
esac

case "$only" in
all | envelope)
	run "an unknown envelope version is refused" \
		TestOpenRefusesAnUnknownEnvelopeVersion ./internal/sync/
	mutate "the envelope version is not checked" \
		TestOpenRefusesAnUnknownEnvelopeVersion ./internal/sync/ envelope_version_ignored
	mutate "an unknown envelope field is ignored" \
		TestOpenRefusesAnUnknownEnvelopeField ./internal/sync/ unknown_field_ignored
	run "a sealed value is bound to its own name" \
		TestSealedValueIsBoundToItsOwnName ./internal/sync/
	mutate "the sealed value carries no associated data" \
		TestSealedValueIsBoundToItsOwnName ./internal/sync/ associated_data_dropped
	run "an over-cap value is refused on export" \
		TestSealRefusesAValueOverTheCap ./internal/sync/
	mutate "the export cap is not checked" \
		TestSealRefusesAValueOverTheCap ./internal/sync/ cap_ignored
	;;
esac

case "$only" in
all | wiring)
	run "the document is bound in sharedDocs" \
		TestSteadyAttemptCarriesASharedSecretToAPeer ./internal/relevo/
	mutate "the document is not bound in sharedDocs" \
		TestSteadyAttemptCarriesASharedSecretToAPeer ./internal/relevo/ document_unbound
	;;
esac

case "$only" in
all | redact)
	run "no surface prints a secret value" \
		TestNoSecretValueIsPrinted ./cmd/relevo/
	mutate "the superseding revision carries the value" \
		TestSupersedeInRecordsTheKeyAndTheReasonOnly ./internal/config/ supersede_carries_the_value
	;;
esac

rm -f "$here/.mutation.out"

printf '\nmutation-secrets: %d ok, %d not caught\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
