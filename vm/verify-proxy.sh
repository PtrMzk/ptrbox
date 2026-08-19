#!/bin/bash
# =============================================================================
# verify-proxy.sh - assert that the egress path works. Runs INSIDE the
# ptrbox-proxy VM (ptrbox pipes it in during `install`); never run this on the
# host.
#
# The sandbox equivalent is verify.sh, and this exists for the same reason.
# `ptrbox install` used to print "host setup complete" knowing only that
# squid's binary existed (Lima's readiness probe) and that squid had PARSED a
# config. A proxy that parses its config and then dies on start satisfies both
# of those, so the first sign of trouble was an agent with no network and no
# idea why.
#
# Exits non-zero if any check fails. That is the point: install's success
# message is gated on it, the way verify.sh gates token injection.
#
# Written against bash's /dev/tcp rather than curl on purpose. This VM installs
# squid and nothing else - that minimalism is the reason it exists - so a check
# needing a package the VM does not promise is a check that quietly stops
# running the day the image changes.
# =============================================================================
set -u

# The two files squid is serving. Arguments with the real paths as defaults,
# purely so the test suite can run this for real against a stub squid instead
# of only syntax-checking it - ptrbox passes neither.
conf="${1:-/etc/squid/squid.conf}"
allowlist="${2:-/etc/squid/allowed_domains.txt}"

# RFC 2606 reserves .invalid, so this name can never be a destination anyone
# would legitimately allowlist - which is what makes it safe to assert a
# denial against, and why the check needs no cooperation from the user's list.
denied=blocked.ptrbox.invalid

fails=0

ok() { printf '  %-22s OK\n' "$1"; }
bad() {
  printf '  %-22s FAIL - %s\n' "$1" "$2"
  fails=$((fails + 1))
}

# --- squid is up -------------------------------------------------------------

if systemctl is-active --quiet squid; then
  ok "squid running"
else
  bad "squid running" "squid.service is not active"
fi

# The port is read from the config rather than passed in, so this asserts what
# squid was actually told to serve instead of what the host meant to tell it.
port="$(awk '$1 == "http_port" { print $2; exit }' "$conf" 2>/dev/null)"
if [ -n "$port" ]; then
  ok "ptrbox config live"
else
  bad "ptrbox config live" "no http_port in $conf - the host never pushed its config"
  printf '\n%s check(s) FAILED - the sandboxes have no working egress\n' "$fails" >&2
  exit 1
fi

# The brace group is not decoration. `exec` with no command applies its
# redirections to the shell PERMANENTLY, so a bare `exec 3<>... 2>/dev/null`
# sends every later error - including the FAILED summary at the bottom of this
# script - to /dev/null. Grouping scopes the silence to the attempt while fd 3
# stays open afterwards, which is the half we actually want to keep.
if { exec 3<>"/dev/tcp/127.0.0.1/$port"; } 2>/dev/null; then
  exec 3<&- 3>&-
  ok "squid listening"
else
  bad "squid listening" "nothing accepts connections on port $port in the VM"
fi

# --- the egress rules --------------------------------------------------------

# connect speaks one CONNECT to squid - the exact request a sandbox makes,
# since squid serves HTTPS tunnels and nothing else - and prints the status
# code that came back. An empty answer means squid never replied.
connect() {
  local host="$1" code=""
  { exec 3<>"/dev/tcp/127.0.0.1/$port"; } 2>/dev/null || return 1
  printf 'CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n' "$host" "$host" >&3
  # Bounded: on an allowed domain squid dials the origin before answering, so
  # a hung origin must not become a hung install.
  read -r -t 20 _ code _ <&3
  exec 3<&- 3>&-
  printf '%s' "$code"
}

# The domain to prove works comes from the allowlist squid is serving, not from
# a name baked in here: the allowlist is the user's to curate, and a check
# against a domain they removed would fail for being out of date rather than
# for being broken. Leading-dot entries cover subdomains and are not
# themselves fetchable, so they are skipped.
allowed="$(awk '
  /^[[:space:]]*#/ { next }
  NF == 0          { next }
  $1 ~ /^\./       { next }
  { print $1; exit }
' "$allowlist" 2>/dev/null)"

if [ -z "$allowed" ]; then
  bad "allowed domain tunnels" "no fetchable domain in $allowlist"
else
  code="$(connect "$allowed")"
  if [ "$code" = "200" ]; then
    ok "allowed domain tunnels"
  else
    bad "allowed domain tunnels" "CONNECT $allowed:443 answered ${code:-nothing}, want 200"
  fi
fi

code="$(connect "$denied")"
if [ "$code" = "403" ]; then
  ok "denied domain refused"
else
  bad "denied domain refused" "CONNECT $denied:443 answered ${code:-nothing}, want 403"
fi

if [ "$fails" -ne 0 ]; then
  printf '\n%s check(s) FAILED - the sandboxes have no working egress\n' "$fails" >&2
  exit 1
fi
