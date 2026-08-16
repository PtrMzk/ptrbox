#!/usr/bin/env bash
# =============================================================================
# cmd_allow.sh - manage the egress allowlist.
#
#   ptrbox allow api.example.com      append one or more domains
#   ptrbox allow                      open the file in $EDITOR
#   ptrbox allow --list               print the current domains
#
# The allowlist lives on the HOST (next to ptrbox's config file) and is the
# source of truth; lib/proxy.sh pushes it into the proxy VM, where squid
# validates it and reloads with `squid -k reconfigure` - which drops no live
# tunnels. A restart would sever every VM's connection, including Claude's
# in-flight request.
#
# If squid rejects the result, both the host file and the VM are restored
# and your version is kept alongside - a broken allowlist means squid refuses
# to start, which takes every sandbox offline.
#
# With the proxy VM down, edits still land in the host file and are pushed by
# the next `ptrbox new`/`ptrbox start`.
#
# Every entry is a capability grant to EVERY VM: the proxy is shared and
# cannot tell sandboxes apart.
# =============================================================================

# shellcheck source=lib/proxy.sh
. "$PTRBOX_ROOT/lib/proxy.sh"

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

  allowlist="$(ptrbox_allowlist_path)"
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

  # --- apply to the proxy VM ---------------------------------------------
  if ! ptrbox_proxy_running; then
    rm -f "$backup"
    ptrbox_say "saved. The proxy VM is not running; the change is applied when it next starts."
    return 0
  fi

  ptrbox_proxy_sync
  case "$PTRBOX_PROXY_SYNC" in
    rejected)
      # The VM was already restored by the sync; restore the host file too,
      # keeping the refused version for inspection.
      rejected="$allowlist.rejected"
      mv "$allowlist" "$rejected"
      mv "$backup" "$allowlist"
      ptrbox_say "squid rejected the new allowlist; restored the previous one"
      ptrbox_say "your version is kept at $rejected"
      exit 1
      ;;
    *)
      rm -f "$backup"
      ptrbox_say "allowlist reloaded (no tunnels dropped)"
      ;;
  esac
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
