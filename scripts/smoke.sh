#!/bin/sh
# End-to-end smoke: a tiny repository goes from `barnraise init` to one item
# integrated into main, and the usage per step is printed.
#
#   scripts/smoke.sh fake      scripted provider, no account, no tokens spent
#   scripts/smoke.sh claude    real Claude Code (spends tokens of the account)
#
# With `claude` the account is the one the `claude` CLI is already logged in
# to (in CI: the CLAUDE_CODE_OAUTH_TOKEN environment variable).
#
# Environment:
#   BARNRAISE_BIN   binary to test (default ./barnraise)
#   SMOKE_TIMEOUT   seconds to wait for the item (default 120 fake, 1800 claude)
#   SMOKE_CLAUDE_CONFIG_DIR  logged-in Claude config dir (default: the CLI's own)
#   SMOKE_MODEL     model for every class with `claude` (default haiku)
#   SMOKE_REVIEW_MODEL  second model, for the review (default sonnet@low)
#   SMOKE_KEEP=1    keep the work directory for inspection
set -eu

provider="${1:-fake}"
case "$provider" in
fake) default_timeout=120 ;;
claude) default_timeout=1800 ;;
*)
	echo "usage: $0 <fake|claude>" >&2
	exit 2
	;;
esac
timeout="${SMOKE_TIMEOUT:-$default_timeout}"

bin="${BARNRAISE_BIN:-./barnraise}"
[ -x "$bin" ] || {
	echo "smoke: $bin not found; run \`make build\` first" >&2
	exit 2
}
bin="$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")"

work="$(mktemp -d "${TMPDIR:-/tmp}/barnraise-smoke.XXXXXX")"
daemon_pid=""
cleanup() {
	code=$?
	if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null; then
		kill "$daemon_pid" 2>/dev/null || true
		wait "$daemon_pid" 2>/dev/null || true
	fi
	if [ "$code" -ne 0 ] && [ -f "$work/daemon.log" ]; then
		echo "--- daemon log (last 60 lines)" >&2
		tail -n 60 "$work/daemon.log" >&2
	fi
	if [ "${SMOKE_KEEP:-0}" = 1 ]; then
		echo "smoke: kept $work" >&2
	else
		rm -rf "$work"
	fi
	exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

export BARNRAISE_CONFIG_DIR="$work/cfg" BARNRAISE_DATA_DIR="$work/data" XDG_RUNTIME_DIR="$work"
export BARNRAISE_NO_TAILSCALE=1 BARNRAISE_NO_AUTOSTART=1 BARNRAISE_PROJECT="" BARNRAISE_TOKEN=""
export GIT_AUTHOR_NAME="${GIT_AUTHOR_NAME:-barnraise-smoke}" GIT_AUTHOR_EMAIL="${GIT_AUTHOR_EMAIL:-smoke@barnraise.invalid}"
export GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME" GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"
if [ "$provider" = fake ]; then
	export BARNRAISE_FAKE_PROVIDER=1
fi

remote="$work/remote.git"
repo="$work/repo"
mkdir -p "$BARNRAISE_CONFIG_DIR"
chmod 700 "$BARNRAISE_CONFIG_DIR"
git init -q --bare -b main "$remote"
git clone -q "$remote" "$repo" 2>/dev/null
cd "$repo"
git checkout -q -B main

printf '# smoke\n\nA tiny repository for the Barnraise smoke test.\n' >README.md
"$bin" init -y --force --no-accounts --template blank --name smoke --approves analyst --check "test -f README.md" >/dev/null
if [ "$provider" = claude ]; then
	# Cheap models for every class keep the nightly run small. Two of them,
	# because a review must run on a different model from the implementation.
	m="claude/${SMOKE_MODEL:-haiku}, claude/${SMOKE_REVIEW_MODEL:-sonnet@low}"
	printf '\nmodels:\n  classes:\n    simple: [%s]\n    normal: [%s]\n    complex: [%s]\n    native: [%s]\n    cheap: [%s]\n' "$m" "$m" "$m" "$m" "$m" >>barnraise.yaml
	"$bin" check >/dev/null
fi
git add -A
git commit -qm "init"
git push -q origin main

printf 'machine: smoke\nlisten: "off"\nnats_port: -1\nprojects: [%s]\n' "$repo" >"$BARNRAISE_CONFIG_DIR/daemon.yaml"
chmod 600 "$BARNRAISE_CONFIG_DIR/daemon.yaml"

"$bin" daemon >"$work/daemon.log" 2>&1 &
daemon_pid=$!
i=0
until "$bin" status >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 100 ] || ! kill -0 "$daemon_pid" 2>/dev/null; then
		echo "smoke: the daemon did not start" >&2
		exit 1
	fi
	sleep 0.2
done

if [ "$provider" = claude ]; then
	"$bin" account add claude --id claude-1 --config-dir "${SMOKE_CLAUDE_CONFIG_DIR:-default}" --skip-login
fi

"$bin" item add "Add a greeting file" --area docs \
	-c "WHEN the repository is checked out, THE SYSTEM SHALL contain GREETING.txt with one line of greeting | main" \
	--body "Create GREETING.txt at the repository root with exactly one line: Hello from Barnraise.
fake: write GREETING.txt Hello from Barnraise." >/dev/null

json_field() { # json_field <name>: first string value of a field in the JSON on stdin
	tr -d '\n' | sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p"
}

start=$(date +%s)
last=""
while :; do
	out="$("$bin" item show I-1 --json 2>/dev/null || true)"
	status="$(printf '%s' "$out" | json_field status)"
	step="$(printf '%s' "$out" | json_field step)"
	if [ "$status/$step" != "$last" ]; then
		echo "smoke: I-1 is ${status:-unknown} at ${step:-none} ($(($(date +%s) - start))s)"
		last="$status/$step"
	fi
	case "$status" in
	integrated) break ;;
	blocked | waiting_owner | rejected | cancelled)
		echo "smoke: I-1 stopped as $status" >&2
		"$bin" item timeline I-1 >&2 || true
		exit 1
		;;
	esac
	if [ $(($(date +%s) - start)) -ge "$timeout" ]; then
		echo "smoke: I-1 not integrated after ${timeout}s" >&2
		"$bin" item timeline I-1 >&2 || true
		exit 1
	fi
	sleep 1
done

log="$(git --git-dir "$remote" log --format='%s%n%b' main)"
printf '%s\n' "$log" | grep -q '^I-1: Add a greeting file' || {
	echo "smoke: main lacks the item's commit" >&2
	exit 1
}
printf '%s\n' "$log" | grep -q '^Barnraise-Item: I-1' || {
	echo "smoke: the commit lacks the Barnraise-Item trailer" >&2
	exit 1
}
files="$(git --git-dir "$remote" ls-tree -r --name-only main)"
printf '%s\n' "$files" | grep -qx 'GREETING.txt' || {
	echo "smoke: GREETING.txt is not on main" >&2
	exit 1
}
if printf '%s\n' "$files" | grep -qx -e 'plan.md' -e 'criteria.json'; then
	echo "smoke: the item's memory files landed on main" >&2
	exit 1
fi

echo "smoke: integrated in $(($(date +%s) - start))s. Usage:"
"$bin" metrics --window 24h --json
