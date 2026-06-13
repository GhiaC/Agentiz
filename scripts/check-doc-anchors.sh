#!/usr/bin/env bash
#
# check-doc-anchors.sh — verify the `path:line` references sprinkled through the
# Markdown docs still resolve to a real file that is at least that many lines
# long. These anchors rot silently when code is refactored; this surfaces them.
#
# Usage:
#   scripts/check-doc-anchors.sh           # report-only, always exits 0
#   scripts/check-doc-anchors.sh --strict  # exit 1 if any anchor is broken/stale
#
# Portable to macOS's bash 3.2 (no mapfile / globstar / associative arrays).

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

STRICT=false
[ "${1:-}" = "--strict" ] && STRICT=true

# Extract tokens like `routes.go:225` or `metrics/metrics.go:18-34` from every
# Markdown file under docs/. The extension list keeps version strings and URLs
# (e.g. v1.23.2, host:8080) from matching.
refs="$(
  find docs -name '*.md' -print0 2>/dev/null \
    | xargs -0 grep -hoE '[A-Za-z0-9_./-]+\.(go|json|sh|ya?ml|mod|templ):[0-9]+(-[0-9]+)?' 2>/dev/null \
    | sort -u
)"

checked=0
broken=0

while IFS= read -r ref; do
  [ -z "$ref" ] && continue
  path="${ref%%:*}"      # everything before the first ':'
  span="${ref#*:}"       # line number or "start-end"
  end="${span#*-}"       # end line of a range, or the single line
  checked=$((checked + 1))

  # Resolve the path relative to the repo root first, then relative to docs/.
  file=""
  if [ -f "$path" ]; then
    file="$path"
  elif [ -f "docs/$path" ]; then
    file="docs/$path"
  elif [ "$path" = "$(basename "$path")" ]; then
    # Bare filename (no directory) — resolve by a unique basename match so
    # anchors like `core.go:144` (really core/core.go) still verify.
    matches="$(find . -path ./.git -prune -o -type f -name "$path" -print 2>/dev/null)"
    count="$(printf '%s\n' "$matches" | grep -c .)"
    if [ "$count" -eq 1 ]; then
      file="$(printf '%s\n' "$matches" | head -1)"
    elif [ "$count" -gt 1 ]; then
      echo "AMBIG   $ref  ($count files named $path; qualify the anchor with a directory)"
      broken=$((broken + 1))
      continue
    fi
  fi

  if [ -z "$file" ]; then
    echo "BROKEN  $ref  (file not found)"
    broken=$((broken + 1))
    continue
  fi

  total="$(wc -l < "$file" | tr -d ' ')"
  if [ "$end" -gt "$total" ]; then
    echo "STALE   $ref  (file has only $total lines)"
    broken=$((broken + 1))
  fi
done <<EOF
$refs
EOF

echo "---"
echo "checked $checked doc anchor(s); $broken problem(s)"

if $STRICT && [ "$broken" -gt 0 ]; then
  exit 1
fi
exit 0
