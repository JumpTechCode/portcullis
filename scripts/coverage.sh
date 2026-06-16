#!/usr/bin/env bash
#
# Run the test suite with the race detector and enforce a minimum total
# statement coverage across the library packages (internal/...). The command
# entrypoint (cmd/...) is exercised by integration tests rather than unit
# coverage and is excluded from the denominator.
#
# Usage: scripts/coverage.sh [threshold-percent]   (default: 80)
set -euo pipefail

threshold="${1:-80}"
profile="${COVER_PROFILE:-coverage.out}"

go test -race -covermode=atomic -coverprofile="$profile" ./internal/... >/dev/null

# Skip the threshold while the tree has no measurable statements yet (the
# profile holds only its "mode:" header). Once real code lands, this enforces.
stmt_lines="$(grep -v -c '^mode:' "$profile" 2>/dev/null || true)"
if [ "${stmt_lines:-0}" -eq 0 ]; then
  echo "coverage: no statements measured yet — skipping ${threshold}% threshold"
  exit 0
fi

# "total: (statements) 87.5%" -> 87.5
total="$(go tool cover -func="$profile" | awk '/^total:/ {print $NF}' | tr -d '%')"
echo "coverage: ${total}% (threshold ${threshold}%)"

awk -v have="$total" -v want="$threshold" 'BEGIN { exit (have + 0 >= want + 0) ? 0 : 1 }' || {
  echo "error: coverage ${total}% is below the required ${threshold}%" >&2
  exit 1
}
