#!/usr/bin/env bash
# =============================================================================
# _lib.sh - shared plumbing for the command stubs. Sourced by each stub.
#
# The stubs let the whole ptrbox lifecycle run on a machine with no Lima, no
# Squid, no Homebrew and no Keychain: they record how they were called and
# answer from canned state. That is what makes install/provision/deprovision
# testable without a Mac.
#
# State, all under $PTRBOX_STUB_DIR:
#   calls          one line per invocation, in order
#   vms            names of "existing" VMs, one per line
#   stdin.N        stdin captured from a call that read it
#   script.N       any argument too long to log inline (e.g. verify.sh)
# =============================================================================

: "${PTRBOX_STUB_DIR:?stubs need PTRBOX_STUB_DIR}"
mkdir -p "$PTRBOX_STUB_DIR"

STUB_CALLS="$PTRBOX_STUB_DIR/calls"
STUB_VMS="$PTRBOX_STUB_DIR/vms"

# Monotonic counter for stdin/script capture files.
stub_next_id() {
  local n=0
  [ -f "$PTRBOX_STUB_DIR/counter" ] && n="$(cat "$PTRBOX_STUB_DIR/counter")"
  n=$((n + 1))
  printf '%s' "$n" >"$PTRBOX_STUB_DIR/counter"
  printf '%s' "$n"
}

# Log the invocation as one greppable line. Long or multi-line arguments (the
# verification script) are written to script.N and logged as a placeholder, so
# the call log stays line-oriented.
stub_record() {
  local line="" a id
  for a in "$@"; do
    case "$a" in
      *"
"* | ?????????????????????????????????????????????????????????????*)
        id="$(stub_next_id)"
        printf '%s' "$a" >"$PTRBOX_STUB_DIR/script.$id"
        line="$line <script:$id>"
        ;;
      *)
        line="$line $a"
        ;;
    esac
  done
  printf '%s\n' "${line# }" >>"$STUB_CALLS"
}

# Capture stdin to stdin.N (and log that it happened). Used to prove the auth
# token travels on stdin and never through argv.
stub_capture_stdin() {
  local id
  id="$(stub_next_id)"
  cat >"$PTRBOX_STUB_DIR/stdin.$id"
  printf '  <stdin:%s>\n' "$id" >>"$STUB_CALLS"
}

stub_vm_exists() {
  [ -f "$STUB_VMS" ] && grep -qx "$1" "$STUB_VMS"
}
