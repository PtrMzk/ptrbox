#!/usr/bin/env bash
# =============================================================================
# lint.sh - syntax-check and shellcheck every shell file in the repo.
#
# `bash -n` always runs (it needs nothing but bash). shellcheck runs when it is
# installed and is skipped with a warning otherwise, so a missing optional tool
# never blocks a contributor:
#   macOS:  brew install shellcheck
#   Linux:  apt install shellcheck   (or: npm i -g shellcheck)
#
# Bash 3.2 compatible on purpose - that is what macOS ships as /bin/bash, and
# these scripts run on the host Mac.
# =============================================================================
set -euo pipefail

cd "$(dirname "$0")/.."

# Every tracked shell file: .sh/.bash by extension, plus anything whose first
# line is a sh/bash shebang (that is how bin/ptrbox and the stubs are found).
shell_files() {
  find . -path ./.git -prune -o -path ./tests/tmp -prune -o -type f -print |
    while IFS= read -r f; do
      case "$f" in
        *.sh | *.bash)
          printf '%s\n' "$f"
          continue
          ;;
      esac
      case "$(head -n 1 "$f" 2>/dev/null)" in
        '#!'*sh | '#!'*sh\ *) printf '%s\n' "$f" ;;
      esac
    done
}

files=""
while IFS= read -r f; do
  files="$files $f"
done <<EOF
$(shell_files)
EOF

# shellcheck disable=SC2086  # word splitting is the point: $files is a list
set -- $files
if [ "$#" -eq 0 ]; then
  echo "lint: no shell files found" >&2
  exit 1
fi

status=0

echo "== bash -n ($# files)"
for f in "$@"; do
  bash -n "$f" || status=1
done

if command -v shellcheck >/dev/null 2>&1; then
  echo "== shellcheck ($(shellcheck --version | awk '/version:/ {print $2}'))"
  # -x: follow `source` directives so lib/*.sh sourced by bin/ptrbox is checked
  # in context rather than reported as "not specified as input".
  shellcheck -x "$@" || status=1
else
  echo "== shellcheck SKIPPED (not installed - brew install shellcheck)" >&2
fi

[ "$status" -eq 0 ] && echo "lint: OK"
exit "$status"
