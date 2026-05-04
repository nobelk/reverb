#!/usr/bin/env bash
# lint-docs.sh — Guards user-facing docs against stale "known gap" / "TODO" /
# "not yet wired" claims and against pre-stable wording outside the MCP block.
#
# Why: README.md, COMPATIBILITY.md, and CHANGELOG.md make external promises.
# When the code lands but the docs don't follow, users hit advice that lies.
# This script runs in CI on every PR so drift is caught at review time, not
# in a Phase-2 sweep.
#
# Allowed exceptions:
#   - "experimental" is permitted on lines that mention MCP (the only beta-
#     track surface pre-1.0, per COMPATIBILITY.md §Transport Stability).
#   - The CHANGELOG section header "Known gaps" and the bullet under it that
#     reads "(None.)" are tolerated — that line is the explicit empty marker.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FILES=(
  "$ROOT/README.md"
  "$ROOT/COMPATIBILITY.md"
  "$ROOT/CHANGELOG.md"
)

FAIL=0

# Forbidden phrases. Each one must match nothing on a non-allowlisted line.
# "TODO" is matched as a word so things like "todo-list-style" don't fire.
declare -a FORBIDDEN=(
  '\bTODO\b'
  'not yet wired'
  'not yet started'
  'not yet implemented'
)

for file in "${FILES[@]}"; do
  if [[ ! -f "$file" ]]; then
    echo "lint-docs: missing file: $file" >&2
    FAIL=1
    continue
  fi

  for pattern in "${FORBIDDEN[@]}"; do
    if matches=$(grep -nE "$pattern" "$file" 2>/dev/null); then
      echo "lint-docs: forbidden phrase /$pattern/ in $(basename "$file"):" >&2
      echo "$matches" | sed 's/^/  /' >&2
      FAIL=1
    fi
  done

  # "known gap" — allow the explicit "(None.)" marker that follows the
  # CHANGELOG's "Known gaps" section header so the doc can keep declaring
  # "this is the place we'd list gaps, and there are none."
  if matches=$(grep -niE 'known gaps?' "$file" 2>/dev/null); then
    leftover=$(echo "$matches" | grep -v -iE '^[0-9]+:### Known gaps$' || true)
    if [[ -n "$leftover" ]]; then
      # Also tolerate lines where the next line says "(None.)" — but the
      # simpler rule is: header lines are OK, anything else is a violation.
      offenders=$(echo "$leftover" | grep -v -iE 'known gaps$' || true)
      if [[ -n "$offenders" ]]; then
        echo "lint-docs: forbidden phrase /known gap/ in $(basename "$file"):" >&2
        echo "$offenders" | sed 's/^/  /' >&2
        FAIL=1
      fi
    fi
  fi

  # "experimental" outside MCP context.
  if matches=$(grep -niE 'experimental' "$file" 2>/dev/null); then
    offenders=$(echo "$matches" | grep -v -iE 'mcp' || true)
    if [[ -n "$offenders" ]]; then
      echo "lint-docs: 'experimental' used outside MCP context in $(basename "$file"):" >&2
      echo "$offenders" | sed 's/^/  /' >&2
      FAIL=1
    fi
  fi
done

if [[ $FAIL -ne 0 ]]; then
  echo "lint-docs: failed — fix the offending lines or update scripts/lint-docs.sh" >&2
  exit 1
fi

echo "lint-docs: ok"
