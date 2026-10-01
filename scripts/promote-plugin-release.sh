#!/bin/sh
# scripts/promote-plugin-release.sh -- move plugin-release to a released tag.
#
# Usage: promote-plugin-release.sh TAG [REMOTE]
#   TAG      the release tag (e.g. v1.2.3).
#   REMOTE   the remote holding plugin-release (default: origin).
#
# The plugin directory tracks plugin-release, so the branch may move only after
# the release and its archives are out, and only by fast-forward. The push is
# plain -- no --force, no --force-with-lease -- so a race past the precheck
# cannot move the branch except by fast-forward.
#
# Exit: 0 when the branch was created or moved (already-there is a no-op);
#       1 when TAG cannot be dereferenced to a commit, or when the branch tip
#         is not an ancestor of that commit (refused, the branch left alone);
#       2 on a usage error.
set -eu

usage() {
	echo "usage: promote-plugin-release.sh TAG [REMOTE]" >&2
	exit 2
}

case $# in
	1) tag=$1; remote=origin ;;
	2) tag=$1; remote=$2 ;;
	*) usage ;;
esac

branch=plugin-release

# Dereference an annotated tag to its commit before any remote read. Pushing
# refs/tags/TAG:refs/heads/plugin-release would send the tag object, which
# GitHub rejects ("trying to write non-commit object ... to branch"); a tag
# that does not resolve to a commit fails closed.
commit=$(git rev-parse --verify "${tag}^{commit}" 2>/dev/null) || {
	echo "promote-plugin-release: cannot resolve ${tag} to a commit" >&2
	exit 1
}

# move <commit-id> pushes a plain fast-forward; a rejected push (auth,
# protection, network) leaves the branch where it was and says so.
move() {
	if ! git push "$remote" "$1:refs/heads/$branch"; then
		echo "promote-plugin-release: $branch did not move" >&2
		exit 1
	fi
}

# Probe the remote. --exit-code makes an absent branch exit 2; any other
# nonzero is a real failure whose git output has already reached stderr.
if ls=$(git ls-remote --exit-code --heads "$remote" "refs/heads/$branch"); then
	tip=$(printf '%s\n' "$ls" | awk 'NR == 1 { print $1 }')
	if [ -z "$tip" ]; then
		echo "promote-plugin-release: $branch did not move" >&2
		exit 1
	fi
else
	case $? in
		2) # No branch yet: create it at the tagged commit.
			move "$commit"
			echo "promote-plugin-release: created $branch at $tag ($commit)"
			exit 0 ;;
		*)
			echo "promote-plugin-release: $branch did not move" >&2
			exit 1 ;;
	esac
fi

# ls-remote yields a SHA whose object is not local; fetch the ref so
# merge-base can answer the ancestry question against a real object.
if ! git fetch --quiet "$remote" "refs/heads/$branch"; then
	echo "promote-plugin-release: $branch did not move" >&2
	exit 1
fi

if git merge-base --is-ancestor "$tip" "$commit"; then
	move "$commit"
	echo "promote-plugin-release: $branch is at $tag ($commit)"
	exit 0
fi

# The tag is not a descendant of the branch tip, so the move would not be a
# fast-forward. Refuse and leave the branch alone: the release stays published.
cat >&2 <<EOF
promote-plugin-release: refusing to move $branch
  $branch tip: $tip
  $tag:        $commit
The tag is not a descendant of the branch tip, so the move would not be a
fast-forward. The branch was left alone and the release stays published.
To move the published plugin back on purpose, by hand and after review:
  git push --force $remote $tag:refs/heads/$branch
EOF
exit 1
