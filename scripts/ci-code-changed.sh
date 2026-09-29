#!/bin/sh
# scripts/ci-code-changed.sh -- check whether changed paths touch code outside docs.
#
# Reads repo-relative paths from stdin (one per line, e.g. from git diff --name-only).
# Blank lines are ignored.
#
# Prints "false" iff stdin contains at least one non-blank path AND every
# non-blank path starts with one of the docs-only prefixes:
#   docs/plans/
#   docs/specs/
#   docs/superpowers/
#
# Prints "true" in every other case, including empty input (fail open).
# Exits 0 on success.
set -eu

saw_any=0

while IFS= read -r line || [ -n "$line" ]; do
	# Ignore blank lines
	case "$line" in
		*[![:space:]]*) ;;
		*) continue ;;
	esac

	saw_any=1
	case "$line" in
		docs/plans/*|docs/specs/*|docs/superpowers/*)
			;;
		*)
			echo "true"
			exit 0
			;;
	esac
done

if [ "$saw_any" -eq 1 ]; then
	echo "false"
else
	echo "true"
fi
