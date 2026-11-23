#!/bin/sh
# Says whether a change touches more than documentation.
#
# Documentation alone needs the tests (they check the vocabulary and the
# examples), not the builds, the end-to-end run or the linters. Anything
# else, or a base that cannot be found, counts as code: never skip blind.
#
#   BASE=<commit> scripts/ci-changes.sh     prints "code=true|false";
#                                           also to $GITHUB_OUTPUT when set
set -eu

code=true
base="${BASE:-}"
if [ -n "$base" ] && git cat-file -e "$base^{commit}" 2>/dev/null; then
	files="$(git diff --name-only "$base" HEAD)"
	if [ -n "$files" ] && ! printf '%s\n' "$files" | grep -qvE '^(docs/|[^/]*\.md$|LICENSE$)'; then
		code=false
	fi
fi

echo "code=$code"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
	echo "code=$code" >>"$GITHUB_OUTPUT"
fi
