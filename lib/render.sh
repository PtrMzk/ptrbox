#!/usr/bin/env bash
# =============================================================================
# render.sh - template rendering for ptrbox. Sourced, never executed.
#
# Two substitutions, applied to the template AND to every included file:
#
#   __KEY__                      replaced by the value of a KEY=VALUE argument
#   __INCLUDE:provision/x.sh__   replaced by that file's contents, with every
#                                line prefixed by the marker's own indentation
#
# The include mechanism is what lets provision logic live in real .sh files
# (lintable, testable) while the generated Lima config stays self-describing:
# you can read exactly what a VM will run. It also avoids depending on any
# Lima-version-specific way of referencing external scripts.
#
# Indentation matters: a YAML literal block (`script: |`) strips the block's
# common indentation before handing the string to the shell, so uniformly
# prefixed lines arrive at the guest un-indented - which is what keeps heredoc
# terminators inside the provision scripts valid.
#
# Bash 3.2 compatible (macOS /bin/bash).
# =============================================================================

# Substitute __KEY__ placeholders in a single line.
# Usage: _ptrbox_subst LINE KEY=VALUE...
_ptrbox_subst() {
  local line="$1"
  shift
  local pair key value
  for pair in "$@"; do
    key="${pair%%=*}"
    value="${pair#*=}"
    line="${line//__${key}__/$value}"
  done
  printf '%s\n' "$line"
}

# Render a template to stdout.
# Usage: ptrbox_render TEMPLATE INCLUDE_DIR KEY=VALUE...
ptrbox_render() {
  local template="$1" include_dir="$2"
  shift 2

  if [ ! -f "$template" ]; then
    printf 'render: no such template: %s\n' "$template" >&2
    return 1
  fi

  local line indent rest inc incpath incline
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      *__INCLUDE:*__*)
        indent="${line%%[! ]*}"
        rest="${line#*__INCLUDE:}"
        inc="${rest%%__*}"
        incpath="$include_dir/$inc"
        if [ ! -f "$incpath" ]; then
          printf 'render: %s: included file not found: %s\n' "$template" "$incpath" >&2
          return 1
        fi
        while IFS= read -r incline || [ -n "$incline" ]; do
          case "$incline" in
            *__INCLUDE:*__*)
              printf 'render: %s: nested includes are not supported\n' "$incpath" >&2
              return 1
              ;;
          esac
          # Keep blank lines blank rather than emitting trailing whitespace.
          if [ -z "$incline" ]; then
            printf '\n'
          else
            printf '%s' "$indent"
            _ptrbox_subst "$incline" "$@"
          fi
        done <"$incpath"
        ;;
      *)
        _ptrbox_subst "$line" "$@"
        ;;
    esac
  done <"$template"
}

# Render to a file, refusing to leave a half-rendered artifact behind.
# Fails if any __PLACEHOLDER__ survived - that means a typo in the template or
# a config key nobody passed, and shipping it would produce a VM that misbehaves
# in a much more confusing way.
# Usage: ptrbox_render_file OUT TEMPLATE INCLUDE_DIR KEY=VALUE...
ptrbox_render_file() {
  local out="$1"
  shift
  local tmp="$out.tmp.$$"

  if ! ptrbox_render "$@" >"$tmp"; then
    rm -f "$tmp"
    return 1
  fi

  # `|| true`: grep exits 1 when it matches nothing, which is the success case
  # here - and would otherwise abort any caller running under `set -e`.
  local leftovers
  leftovers="$(grep -o '__[A-Z][A-Z0-9_]*__' "$tmp" | sort -u | tr '\n' ' ' || true)"
  if [ -n "$leftovers" ]; then
    printf 'render: unsubstituted placeholders: %s\n' "$leftovers" >&2
    rm -f "$tmp"
    return 1
  fi

  mv "$tmp" "$out"
}
