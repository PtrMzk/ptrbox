#!/usr/bin/env bash
# =============================================================================
# cmd_install.sh - idempotent host setup.
#
# Re-running this on a configured Mac changes nothing and says so. That
# property is what makes it safe to run before `make smoke`, or after pulling
# a new version of ptrbox.
#
# The egress proxy is a dedicated Lima VM (lib/proxy.sh), so "install" means:
# check dependencies, seed the host-side allowlist, provision/start the proxy
# VM and push the current squid config into it, wire up ssh, and offer a PATH
# symlink. Nothing squid-related is installed on the Mac itself any more.
# =============================================================================

# shellcheck source=lib/proxy.sh
. "$PTRBOX_ROOT/lib/proxy.sh"

ptrbox_cmd_install() {
  local assume_yes=0 no_input=0 arg

  while [ "$#" -gt 0 ]; do
    arg="$1"
    case "$arg" in
      -y | --yes) assume_yes=1 ;;
      --no-input) no_input=1 ;;
      -h | --help)
        cat <<'HELP'
ptrbox install - set up the host

  -y, --yes      answer yes to every prompt
      --no-input never prompt; decline anything that would need an answer
HELP
        return 0
        ;;
      *) ptrbox_die "install: unknown option '$arg'" ;;
    esac
    shift
  done
  PTRBOX_ASSUME_YES="$assume_yes"
  PTRBOX_NO_INPUT="$no_input"

  # shellcheck source=lib/preflight.sh
  . "$PTRBOX_ROOT/lib/preflight.sh"

  ptrbox_preflight_deps || exit 1

  # --- directories -------------------------------------------------------
  mkdir -p "$PTRBOX_REPO_ROOT" "$(ptrbox_generated_dir)" "$HOME/.ssh/config.d"

  # --- ssh include -------------------------------------------------------
  ptrbox_install_ssh_include

  # --- the egress proxy VM -----------------------------------------------
  # Seeds the allowlist (migrating a pre-v2 one from the brew prefix if it
  # exists), creates/starts the proxy VM, and pushes the current config.
  ptrbox_proxy_ensure
  ptrbox_install_allowlist_report
  ptrbox_install_legacy_squid_note

  # --- put ptrbox on PATH ------------------------------------------------
  ptrbox_install_symlink

  # --- report ------------------------------------------------------------
  ptrbox_preflight_report

  if [ "$PTRBOX_PROXY_CHANGED" -eq 0 ]; then
    ptrbox_say "host already set up; nothing to do"
  else
    ptrbox_say "host setup complete"
  fi
  ptrbox_say "next: ptrbox new <repo>"
}

# --- prompting ---------------------------------------------------------------
# Every prompt must be bypassable: an interactive-only question about a
# security-relevant file cannot be covered by the test suite, and an install
# that hangs waiting for input in a script is worse than one that declines.
ptrbox_confirm() {
  local question="$1" answer

  [ "${PTRBOX_ASSUME_YES:-0}" = "1" ] && return 0
  if [ "${PTRBOX_NO_INPUT:-0}" = "1" ] || [ ! -t 0 ]; then
    ptrbox_say "declining (no input available): $question"
    ptrbox_say "re-run with --yes to accept"
    return 1
  fi

  printf 'ptrbox: %s [y/N] ' "$question" >&2
  read -r answer
  case "$answer" in
    y | Y | yes | YES) return 0 ;;
    *) return 1 ;;
  esac
}

# --- ssh ---------------------------------------------------------------------

ptrbox_install_ssh_include() {
  local cfg="$HOME/.ssh/config"

  # grep-guarded so it is idempotent. Prepending is done with a group rather
  # than `printf | cat - "$cfg"`: under `set -o pipefail` a missing config makes
  # cat fail and takes the whole install down. (The sed one-liner both of these
  # replace silently did nothing on a zero-line file.)
  if [ -f "$cfg" ] && grep -qF 'Include config.d/*' "$cfg"; then
    return 0
  fi
  {
    printf 'Include config.d/*\n'
    if [ -f "$cfg" ]; then cat "$cfg"; fi
  } >"$cfg.ptrbox.tmp"
  mv "$cfg.ptrbox.tmp" "$cfg"
  chmod 600 "$cfg"
  ptrbox_say "added 'Include config.d/*' to ~/.ssh/config"
}

# --- PATH --------------------------------------------------------------------

# Offer to symlink bin/ptrbox somewhere on PATH. Asked rather than assumed:
# writing into a directory outside the checkout is the user's call, and the
# default when there is no tty is to decline.
ptrbox_install_symlink() {
  local target="$PTRBOX_BIN_DIR/ptrbox" source="$PTRBOX_ROOT/bin/ptrbox" existing

  if [ -L "$target" ]; then
    existing="$(readlink "$target")"
    if [ "$existing" = "$source" ]; then
      return 0 # already ours; nothing to say
    fi
  fi

  if [ -e "$target" ]; then
    ptrbox_say "$target already exists and is not this checkout's ptrbox"
    if ! ptrbox_confirm "replace it with a symlink to $source?"; then
      return 0
    fi
  else
    if ! ptrbox_confirm "symlink ptrbox into $PTRBOX_BIN_DIR so it is on your PATH?"; then
      ptrbox_say "skipped; run it as $source, or symlink it yourself"
      return 0
    fi
  fi

  mkdir -p "$PTRBOX_BIN_DIR"
  ln -sfn "$source" "$target"
  ptrbox_record_manifest "linked $target"
  ptrbox_say "linked $target -> $source"

  # A symlink in a directory nobody searches is a silent no-op, so check.
  case ":$PATH:" in
    *":$PTRBOX_BIN_DIR:"*) ;;
    *) ptrbox_warn "$PTRBOX_BIN_DIR is not on your PATH - add it to ~/.zshrc" ;;
  esac
}

# --- allowlist reporting -----------------------------------------------------

# The allowlist is the user's living capability list: seeded once by
# lib/proxy.sh, then never overwritten - only reported on.
ptrbox_install_allowlist_report() {
  local target
  target="$(ptrbox_allowlist_path)"
  if ! cmp -s "$PTRBOX_ROOT/host/allowed_domains.txt" "$target"; then
    ptrbox_say "$target differs from the shipped allowlist (yours is kept)."
    ptrbox_say "compare with: diff $target $PTRBOX_ROOT/host/allowed_domains.txt"
  fi
}

# --- migration ---------------------------------------------------------------

# Before v2 the proxy was Homebrew's squid on the host. If install finds a
# config it wrote back then, tell the user that daemon is now dead weight -
# but do not stop or uninstall anything: host services are the user's call.
ptrbox_install_legacy_squid_note() {
  local legacy="$PTRBOX_BREW_PREFIX/etc/squid.conf"
  if [ -f "$legacy" ] && grep -q '^# ptrbox-managed' "$legacy"; then
    ptrbox_say "squid on the host is no longer used - the proxy now runs in the ptrbox-proxy VM"
    ptrbox_say "stop the old one with: brew services stop squid"
  fi
}
