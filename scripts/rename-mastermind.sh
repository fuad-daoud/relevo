#!/bin/sh
# scripts/rename-mastermind.sh -- the one-shot mechanical rename planner -> MasterMind.
#
# Run once, from the repository root, on a clean tree:
#   sh scripts/rename-mastermind.sh
# It moves the planner-named paths, then rewrites planner/Planner tokens in every
# tracked text file outside the historical records and the generated goldens. It
# is kept in the tree as the record of what the rename did; it is not meant to be
# run twice (it refuses).
#
# The word rule (D1): human- and model-facing text writes MasterMind; compile-time
# and wire identifiers are lowercase mastermind. The actor names planner,
# lite-planner and the agent architect do not change: after this rename `planner`
# means the actor only. setup.go/setup_test.go are skipped entirely because they
# seed those actors (D7).
#
# Token rules, applied in this order to each swept file:
#   1. RELEVO_PLANNER -> RELEVO_MASTERMIND       (env, written and read)
#   2. --all-planners -> --all-masterminds        (before --planner)
#   3. --planner      -> --mastermind
#   4. internal/planner -> internal/mastermind    (import path)
#   5. Planner -> MasterMind                      (no word boundary: testPlannerName too)
#   6. planner -> mastermind                      (no word boundary: testPlannerName too)
# The restore manifest puts the stored spellings back: bytes already written keep
# their old spelling (D2), and the actor name stays (D7). The one exception is the
# kv registry prefix, which migration 008 rewrites (D3).
#
# SC2016: every perl expression below is a literal pattern, not a shell
# expansion, so the single quotes are deliberate.
# shellcheck disable=SC2016
set -eu

[ -d internal/planner ] || { echo "rename-mastermind: internal/planner is gone; the rename already ran" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "rename-mastermind: tree is not clean" >&2; exit 1; }

# 1. Moves.
git mv internal/planner internal/mastermind
git mv internal/mastermind/planner.go internal/mastermind/mastermind.go
git mv internal/mastermind/planner_test.go internal/mastermind/mastermind_test.go
git mv cmd/relevo/planner.go cmd/relevo/mastermind.go
git mv cmd/relevo/planner_test.go cmd/relevo/mastermind_test.go
git mv internal/doctor/planner.go internal/doctor/mastermind.go
git mv internal/doctor/planner_test.go internal/doctor/mastermind_test.go
git mv internal/db/planner_test.go internal/db/mastermind_test.go
git mv internal/relevo/statusline_planner_test.go internal/relevo/statusline_mastermind_test.go
git mv cmd/relevo/testdata/contract/planner-list.golden cmd/relevo/testdata/contract/mastermind-list.golden
git mv cmd/relevo/testdata/contract/history-planner.golden cmd/relevo/testdata/contract/history-mastermind.golden
git mv internal/classify/testdata/injection/negative/06-question-to-planner.md internal/classify/testdata/injection/negative/06-question-to-mastermind.md

# 2. Tokens, text files only (git grep -I skips binaries) outside the historical
#    records, the generated goldens, the append-only migration history (008 does
#    the rename, so 001-007 must keep creating `planner`) and the two setup files
#    that seed actors (D7). The file list is read before any edit: no filename in
#    this tree has a space.
swept=$(git grep -Il -e planner -e Planner -- . \
	':(exclude)docs/plans/**' \
	':(exclude)docs/specs/**' \
	':(exclude)docs/superpowers/**' \
	':(exclude)go.sum' \
	':(exclude)internal/legacy/**' \
	':(exclude)scripts/rename-relevo.sh' \
	':(exclude)scripts/check-name.sh' \
	':(exclude)scripts/check-name_test.sh' \
	':(exclude)scripts/rename-mastermind.sh' \
	':(exclude)internal/harness/install_test.go' \
	':(exclude)internal/harness/agents/shipped.sha256' \
	':(exclude)*.golden' \
	':(exclude)internal/db/migrations/**' \
	':(exclude)internal/setup/setup.go' \
	':(exclude)internal/setup/setup_test.go' || true)
for f in $swept; do
	perl -pi -e '
		s/RELEVO_PLANNER/RELEVO_MASTERMIND/g;
		s/--all-planners/--all-masterminds/g;
		s/--planner/--mastermind/g;
		s{internal/planner}{internal/mastermind}g;
		s/Planner/MasterMind/g;
		s/planner/mastermind/g;
	' "$f"
done

# 3. Restore manifest: the stored spellings (D2) and the actor name (D7).
#    Global values already written on disk.
restored=$(git grep -Il -e mastermind -- . \
	':(exclude)docs/plans/**' \
	':(exclude)docs/specs/**' \
	':(exclude)docs/superpowers/**' \
	':(exclude)go.sum' \
	':(exclude)internal/legacy/**' \
	':(exclude)scripts/rename-relevo.sh' \
	':(exclude)scripts/rename-mastermind.sh' \
	':(exclude)internal/harness/install_test.go' \
	':(exclude)*.golden' || true)
for f in $restored; do
	perl -pi -e '
		s/to_mastermind/to_planner/g;
		s/lite-mastermind/lite-planner/g;
		s/mastermind\.pruned_at/planner.pruned_at/g;
		s/mastermind::/planner::/g;
	' "$f"
done

#    Per-file stored spellings whose bare word is ambiguous.
restore() { [ -f "$1" ] || return 0; perl -pi -e "$2" "$1"; }

for f in internal/availability/*.go; do
	restore "$f" 's/"mastermind"/"planner"/g'
done
restore internal/db/types.go 's/(OwnerMasterMind = )"mastermind"/${1}"planner"/'
restore internal/delivery/channel.go 's/json:"mastermind"/json:"planner"/g'
restore internal/store/binding.go 's/json:"mastermind/json:"planner/g'
restore internal/store/paths.go 's/"masterminds"/"planners"/g'
restore internal/store/store_test.go 's/"mastermind":/"planner":/g'
restore internal/relevo/policy_view_test.go 's/"mastermind"/"planner"/g'
restore internal/relevo/unused_gates_test.go 's/"mastermind"/"planner"/g'
restore internal/ui/view_candidates_test.go 's/"mastermind"/"planner"/g'
# The ingest fixtures are stored bind.json bytes (D2): the "planner" key and the
# fixture agent name keep their spelling.
for f in internal/ingest/testdata/binding-three-rounds/*.json; do
	restore "$f" 's/"mastermind"/"planner"/g; s/"mastermind1"/"planner1"/g'
done

# The actor name stays `planner` (D7): config-init seeds it, and the fixtures
# that name that actor keep the word.
restore cmd/relevo/init_test.go 's/"mastermind", "lite-planner"/"planner", "lite-planner"/'
restore cmd/relevo/init.go 's/builder: a, b; mastermind: c/builder: a, b; planner: c/'
for f in internal/relevo/configaudit_test.go internal/ui/golden_test.go internal/ui/view_audit_test.go; do
	restore "$f" 's/actor mastermind/actor planner/g; s/mastermind actor/planner actor/g; s/actors\.mastermind/actors.planner/g'
done
# The README's actor examples keep the word (D7, step 10).
restore README.md 's{`mastermind` actor}{`planner` actor}g; s{a `mastermind` and a `lite-planner`}{a `planner` and a `lite-planner`}g; s{mastermind: opus; lite-planner:}{planner: opus; lite-planner:}g; s{### MasterMind actors}{### Planner actors}g; s{A mastermind actor is a reader binding too}{A planner actor is a reader binding too}g; s{\(or mastermind-session\) step}{(or planner-session) step}g'

gofmt -w cmd internal

# 4. The residue: every remaining planner/Planner line outside the exempt paths.
#    It must be exactly the protect manifest of §3.3/§4.3 plus the actor name;
#    anything else is a token the sweep got wrong and exits 1.
residue=$(git grep -nIE '(planner|Planner)' -- . \
	':(exclude)docs/plans/**' \
	':(exclude)docs/specs/**' \
	':(exclude)docs/superpowers/**' \
	':(exclude)go.sum' \
	':(exclude)internal/legacy/**' \
	':(exclude)scripts/rename-relevo.sh' \
	':(exclude)scripts/check-name.sh' \
	':(exclude)scripts/check-name_test.sh' \
	':(exclude)scripts/rename-mastermind.sh' \
	':(exclude)internal/harness/install_test.go' \
	':(exclude)internal/harness/agents/shipped.sha256' \
	':(exclude)*.golden' \
	|| true)

printf '%s\n' "$residue"

allowed='to_planner|"planner"|planner1|planner_id|planner_screen|planner_harness_session_uidx|binding_planner_id_idx|builder_to_planner|planner\.pruned_at|planner::|"planner/"|"planner": "relevo mastermind"|^README\.md:|^cmd/relevo/init\.go:|^cmd/relevo/init_test\.go:|^cmd/relevo/main\.go:|^cmd/relevo/mastermind_test\.go:|^internal/setup/|^internal/db/migrations/001_initial\.sql:|^internal/db/mastermind_test\.go:|^internal/relevo/configaudit_test\.go:|^internal/relevo/policy_view_test\.go:|^internal/relevo/unused_gates_test\.go:|^internal/ui/golden_test\.go:|^internal/ui/view_audit_test\.go:|^internal/ui/view_candidates_test\.go:|^internal/db/types\.go:|^internal/availability/|^internal/delivery/channel\.go:|^internal/store/binding\.go:|^internal/store/paths\.go:|^internal/store/store_test\.go:|^internal/ingest/testdata/|^internal/mastermind/registry\.go:'
bad=$(printf '%s\n' "$residue" | grep -vE "$allowed" || true)
if [ -n "$bad" ]; then
	echo "rename-mastermind: residue outside the protect manifest:" >&2
	printf '%s\n' "$bad" >&2
	exit 1
fi

echo "rename-mastermind: done; next: go build ./... && go vet ./... && make check"
