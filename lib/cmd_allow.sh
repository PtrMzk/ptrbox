#!/usr/bin/env bash
# =============================================================================
# cmd_allow.sh - manage the egress allowlist.
#
#   ptrbox allow api.example.com      append one or more domains
#   ptrbox allow                      open the file in $EDITOR
#   ptrbox allow --list               print the current domains
#
# Either way the file is validated by squid and then reloaded with
# `squid -k reconfigure`, which reloads without dropping live tunnels. A
# restart would sever every VM's connection, including Claude's in-flight
# request.
#
# If the result does not parse, the previous file is restored automatically and
# your version is kept alongside it - a broken allowlist means squid refuses to
# start, which takes every sandbox offline.
#
# Every entry is a capability grant to EVERY VM: the proxy is shared and cannot
# tell sandboxes apart.
# =============================================================================

ptrbox_cmd_allow() {
  local allowlist arg list=0 editor backup rejected
  local domains="" added=0

  while [ "$#" -gt 0 ]; do
    arg="$1"
    case "$arg" in
      --list) list=1 ;;
      -h | --help)
        cat <<'HELP'
ptrbox allow - manage the egress allowlist

  ptrbox allow <domain>...   append domains (a leading dot covers subdomains)
  ptrbox allow               open the allowlist in $EDITOR
  ptrbox allow --list        print the current domains

Changes are validated and reloaded without dropping live connections.
HELP
        return 0
        ;;
      -*) ptrbox_die "allow: unknown option '$arg'" ;;
      *) domains="$domains $arg" ;;
    esac
    shift
  done

  command -v squid >/dev/null 2>&1 || ptrbox_die "squid not found - run 'ptrbox install' first"

  allowlist="$PTRBOX_BREW_PREFIX/etc/squid/allowed_domains.txt"
  [ -f "$allowlist" ] || ptrbox_die "no allowlist at $allowlist - run 'ptrbox install' first"

  if [ "$list" -eq 1 ]; then
    # Strip comments and blanks: just the capabilities.
    grep -vE '^\s*(#|$)' "$allowlist" | awk '{print $1}'
    return 0
  fi

  backup="$allowlist.ptrbox-prev"
  cp "$allowlist" "$backup"

  if [ -n "$domains" ]; then
    for arg in $domains; do
      ptrbox_assert_domain "$arg"
      if ptrbox_allow_contains "$allowlist" "$arg"; then
        ptrbox_say "$arg is already allowed"
        continue
      fi
      printf '%s\n' "$arg" >>"$allowlist"
      ptrbox_say "added $arg"
      added=$((added + 1))
    done
    if [ "$added" -eq 0 ]; then
      rm -f "$backup"
      return 0
    fi
  else
    editor="${VISUAL:-${EDITOR:-vi}}"
    "$editor" "$allowlist"
    if cmp -s "$backup" "$allowlist"; then
      ptrbox_say "unchanged"
      rm -f "$backup"
      return 0
    fi
  fi

  # Validate against the live squid.conf, which references this file.
  if ! squid -k parse >/dev/null 2>&1; then
    rejected="$allowlist.rejected"
    mv "$allowlist" "$rejected"
    mv "$backup" "$allowlist"
    ptrbox_say "squid rejected the new allowlist; restored the previous one"
    ptrbox_say "your version is kept at $rejected"
    squid -k parse >&2 || true
    exit 1
  fi

  rm -f "$backup"
  squid -k reconfigure
  ptrbox_say "allowlist reloaded (no tunnels dropped)"
}

# A domain here becomes a squid ACL entry, so anything that is not plainly a
# hostname is refused rather than written into the config.
ptrbox_assert_domain() {
  case "$1" in
    *[!a-zA-Z0-9.-]*) ptrbox_die "'$1' is not a domain (letters, digits, dots and hyphens only)" ;;
    '' | .) ptrbox_die "empty domain" ;;
    -*) ptrbox_die "'$1' starts with a hyphen" ;;
    *.) ptrbox_die "'$1' ends with a dot" ;;
    *.*) ;;
    *) ptrbox_die "'$1' has no dot - a bare name is not a domain" ;;
  esac
}

# Present already? Compares the first field so comments after the domain and a
# trailing-dot difference do not produce duplicates.
ptrbox_allow_contains() {
  awk -v d="$2" '$1 == d { found = 1 } END { exit !found }' "$1"
}
