#!/usr/bin/env bash
# =============================================================================
# cmd_logs.sh - read the proxy log, from inside the proxy VM.
#
# This is the debugging loop for "my build can't reach X": the request shows up
# as TCP_DENIED, `ptrbox allow` grants the domain, and squid reloads without
# dropping anything.
#
# Squid lives in the ptrbox-proxy VM, so every read goes through
# `limactl shell` - which also means the proxy has to be running, and when it
# is not, that IS the answer to "why does my sandbox have no network".
#
# Following is opt-in (-f) rather than the default, so the command is
# predictable in scripts and testable.
# =============================================================================

# shellcheck source=lib/proxy.sh
. "$PTRBOX_ROOT/lib/proxy.sh"

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

  command -v limactl >/dev/null 2>&1 || ptrbox_die "limactl not found - run 'ptrbox install' first"

  if ! ptrbox_proxy_running; then
    ptrbox_die "the proxy VM is not running (no proxy, no VM egress) - 'ptrbox start <vm>' brings it up"
  fi

  if [ "$follow" -eq 1 ]; then
    if [ "$denied" -eq 1 ]; then
      # --line-buffered, or grep sits on a 4KB buffer and the log looks dead.
      ptrbox_proxy_sh tail -n "$lines" -f "$PTRBOX_SQUID_LOG" | grep --line-buffered TCP_DENIED
    else
      ptrbox_proxy_sh tail -n "$lines" -f "$PTRBOX_SQUID_LOG"
    fi
    return 0
  fi

  if ! out="$(ptrbox_proxy_sh tail -n "$lines" "$PTRBOX_SQUID_LOG" 2>/dev/null)"; then
    ptrbox_die "no proxy log at $PTRBOX_SQUID_LOG in the proxy VM - has any request been made?"
  fi

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
    ptrbox_say "to allow one of these: ptrbox allow <domain>"
    ptrbox_say "(reloads without dropping tunnels)"
  fi
}
