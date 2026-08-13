#!/usr/bin/env bats
# ptrbox logs - reading the egress proxy log.

load lib/harness

setup() {
  harness_setup
  LOG="$PTRBOX_STUB_PREFIX/var/logs/access.log"
  cat >"$LOG" <<'LOGLINES'
1691000001.000 12 127.0.0.1 TCP_TUNNEL/200 5000 CONNECT pypi.org:443 - HIER_DIRECT/1.2.3.4 -
1691000002.000  1 127.0.0.1 TCP_DENIED/403 4000 CONNECT example.com:443 - HIER_NONE/- text/html
1691000003.000  9 127.0.0.1 TCP_TUNNEL/200 7000 CONNECT api.anthropic.com:443 - HIER_DIRECT/5.6.7.8 -
1691000004.000  1 127.0.0.1 TCP_DENIED/403 4000 CONNECT telemetry.example:443 - HIER_NONE/- text/html
LOGLINES
}

@test "logs prints the tail of the proxy log" {
  run "$PTRBOX" logs
  [ "$status" -eq 0 ]
  [[ "$output" == *"pypi.org:443"* ]]
  [[ "$output" == *"example.com:443"* ]]
}

@test "--denied shows only blocked requests" {
  run "$PTRBOX" logs --denied
  [ "$status" -eq 0 ]
  [[ "$output" == *"example.com:443"* ]]
  [[ "$output" == *"telemetry.example:443"* ]]
  [[ "$output" != *"TCP_TUNNEL"* ]]
}

@test "--denied explains how to allow a domain" {
  run "$PTRBOX" logs --denied
  [[ "$output" == *"allowed_domains.txt"* ]]
  [[ "$output" == *"squid -k reconfigure"* ]]
}

@test "-n limits how far back it reads" {
  run "$PTRBOX" logs -n 1
  [ "$status" -eq 0 ]
  [[ "$output" == *"telemetry.example:443"* ]]
  [[ "$output" != *"pypi.org:443"* ]]
}

@test "no denials is not an error" {
  grep -v TCP_DENIED "$LOG" >"$LOG.tmp"
  mv "$LOG.tmp" "$LOG"
  run "$PTRBOX" logs --denied
  [ "$status" -eq 0 ]
  [[ "$output" == *"no matching lines"* ]]
}

@test "a missing log explains itself" {
  rm -f "$LOG"
  run "$PTRBOX" logs
  [ "$status" -ne 0 ]
  [[ "$output" == *"is squid running"* ]]
}

@test "an unknown option is rejected rather than ignored" {
  run "$PTRBOX" logs --tail-everything
  [ "$status" -ne 0 ]
  [[ "$output" == *"unknown option"* ]]
}

@test "-n validates its argument" {
  run "$PTRBOX" logs -n lots
  [ "$status" -ne 0 ]
}

@test "logs reads the configured path" {
  export PTRBOX_SQUID_LOG="$TMP/elsewhere.log"
  printf 'custom line TCP_DENIED\n' >"$PTRBOX_SQUID_LOG"
  run "$PTRBOX" logs
  [[ "$output" == *"custom line"* ]]
}
