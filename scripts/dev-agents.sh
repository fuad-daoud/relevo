#!/bin/sh
# scripts/dev-agents.sh -- installs the agents relevo's own development runs on
# (dev/agents/*.md) into this machine's relevo config, and renders them for every
# harness. Each gets an actor of the same name whose candidates are the machine's
# own for the generic role it stands in for, so nothing machine-specific lives in
# the repository. Re-run it after editing a file under dev/agents.
#
# Usage: dev-agents.sh [--dry-run]
#   --dry-run  print the relevo commands instead of running them
# Needs: relevo and jq on PATH.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
src="$here/../dev/agents"

dry=0
case "${1:-}" in
	"") ;;
	--dry-run) dry=1 ;;
	*)
		echo "usage: dev-agents.sh [--dry-run]" >&2
		exit 2
		;;
esac

for tool in relevo jq; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "dev-agents: $tool is not on PATH" >&2
		exit 1
	fi
done

# run prints a write under --dry-run and performs it otherwise.
run() {
	if [ "$dry" -eq 1 ]; then
		printf 'relevo'
		printf ' %s' "$@" | cut -c1-200
		echo
	else
		relevo "$@" >/dev/null
	fi
}

# candidates prints the generic actor's candidate list as compact JSON.
candidates() {
	relevo config get "actors.$1.candidates" | jq -c .
}

# agent NAME STANDS_IN_FOR OUTPUTS registers the source, then its actor.
agent() {
	name=$1 generic=$2 outputs=$3
	file="$src/$name.md"
	if [ ! -f "$file" ]; then
		echo "dev-agents: no $file" >&2
		exit 1
	fi
	run config set "agents.$name" "$(jq -Rsc '{source: .}' "$file")"
	actor=$(jq -nc --arg agent "$name" --argjson c "$(candidates "$generic")" \
		--argjson o "$outputs" '{agent: $agent, candidates: $c, tier: "yolo"} + (if $o == null then {} else {outputs: $o} end)')
	run config set "actors.$name" "$actor"
	run config agents --agent "$name"
}

agent relevo-builder builder null
agent relevo-reviewer reviewer '{"findings":"artifact","verdict":{"one-of":["pass","changes"]}}'
agent relevo-lite-planner lite-planner '{"plan":"artifact"}'
agent relevo-planner lite-planner '{"design":"artifact"}'

if [ "$dry" -eq 0 ]; then
	echo "dev-agents: installed relevo-builder, relevo-reviewer, relevo-lite-planner, relevo-planner"
	echo "dev-agents: a served machine loads new actors when relevo serve restarts"
fi
