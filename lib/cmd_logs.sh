#!/usr/bin/env bash
# =============================================================================
# cmd_logs.sh - read the proxy log.
#
# This is the debugging loop for "my build can't reach X": the request shows up
# as TCP_DENIED, you add the domain to the allowlist, and squid reloads without
# dropping anything.
#
# Following is opt-in (-f) rather than the default, so the command is
# predictable in scripts and testable.
# =============================================================================

ptrbox_cmd_logs() {
  local denied=0 follow=0 lines=50 arg out

  while [ "$#" -gt 0 ]; do
    arg="$1"
    case "$arg" in
      --denied) denied=1 ;;
      -f | --follow) follow=1 ;;
      -n)
        shift
        lines="${1:-}"
        ptrbox_assert_number LOG_LINES "$lines"
        ;;
      -h | --help)
        cat <<'HELP'
ptrbox logs - read the egress proxy log

      --denied   only blocked requests (TCP_DENIED)
  -f, --follow   keep printing as new requests arrive
  -n <count>     how many lines to start from (default 50)
HELP
        return 0
        ;;
      *) ptrbox_die "logs: unknown option '$arg'" ;;
    esac
    shift
  done

  if [ ! -f "$PTRBOX_SQUID_LOG" ]; then
    ptrbox_die "no proxy log at $PTRBOX_SQUID_LOG - is squid running? (ptrbox install)"
  fi

  if [ "$follow" -eq 1 ]; then
    if [ "$denied" -eq 1 ]; then
      # --line-buffered, or grep sits on a 4KB buffer and the log looks dead.
      tail -n "$lines" -f "$PTRBOX_SQUID_LOG" | grep --line-buffered TCP_DENIED
    else
      tail -n "$lines" -f "$PTRBOX_SQUID_LOG"
    fi
    return 0
  fi

  out="$(tail -n "$lines" "$PTRBOX_SQUID_LOG")"
  if [ "$denied" -eq 1 ]; then
    # `|| true`: no denials is a good outcome, not an error.
    out="$(printf '%s\n' "$out" | grep TCP_DENIED || true)"
  fi

  if [ -z "$out" ]; then
    ptrbox_say "no matching lines in $PTRBOX_SQUID_LOG"
    return 0
  fi

  printf '%s\n' "$out"

  if [ "$denied" -eq 1 ]; then
    ptrbox_say "to allow one of these: add the domain to"
    ptrbox_say "  $PTRBOX_BREW_PREFIX/etc/squid/allowed_domains.txt"
    ptrbox_say "then: squid -k reconfigure   (reloads without dropping tunnels)"
  fi
}
