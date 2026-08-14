#!/usr/bin/env bash
# =============================================================================
# cmd_install.sh - idempotent host setup.
#
# Re-running this on a configured Mac changes nothing and says so. That
# property is what makes it safe to run before `make smoke`, or after pulling
# a new version of ptrbox.
#
# The squid config is marker-driven: our template carries "# ptrbox-managed
# v<N>", so install can tell a config it owns (silently updatable) from a stock
# or hand-rolled one (never overwritten without consent). Any existing file is
# backed up first, and the config is validated by squid before being activated.
# =============================================================================

ptrbox_cmd_install() {
  local assume_yes=0 no_input=0 arg
  local squid_conf allowlist changed_conf=0 changed_allowlist=0

  while [ "$#" -gt 0 ]; do
    arg="$1"
    case "$arg" in
      -y | --yes) assume_yes=1 ;;
      --no-input) no_input=1 ;;
      -h | --help)
        cat <<'HELP'
ptrbox install - set up the host

  -y, --yes      answer yes to every prompt (overwrites an unmanaged squid
                 config, after backing it up)
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
  # shellcheck source=lib/render.sh
  . "$PTRBOX_ROOT/lib/render.sh"

  ptrbox_preflight_deps || exit 1

  # --- directories -------------------------------------------------------
  mkdir -p "$PTRBOX_REPO_ROOT" "$(ptrbox_generated_dir)" "$HOME/.ssh/config.d"

  # --- ssh include -------------------------------------------------------
  ptrbox_install_ssh_include

  # --- squid -------------------------------------------------------------
  squid_conf="$PTRBOX_BREW_PREFIX/etc/squid.conf"
  allowlist="$PTRBOX_BREW_PREFIX/etc/squid/allowed_domains.txt"
  mkdir -p "$PTRBOX_BREW_PREFIX/etc/squid"

  # Allowlist first: squid.conf references it, so validating the candidate
  # config requires the file to already exist.
  ptrbox_install_allowlist "$allowlist" && changed_allowlist=1
  ptrbox_install_squid_conf "$squid_conf" && changed_conf=1

  # --- activate ----------------------------------------------------------
  ptrbox_activate_squid "$changed_conf" "$changed_allowlist"

  # --- put ptrbox on PATH ------------------------------------------------
  ptrbox_install_symlink

  # --- report ------------------------------------------------------------
  ptrbox_preflight_report

  if [ "$changed_conf" -eq 0 ] && [ "$changed_allowlist" -eq 0 ]; then
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

# --- squid config ------------------------------------------------------------

# Version of the ptrbox marker in a file, or empty if it has none.
ptrbox_marker_version() {
  [ -f "$1" ] || return 0
  sed -n 's/^# ptrbox-managed v\([0-9][0-9]*\).*/\1/p' "$1" | head -1
}

# Returns 0 (success) if the config was changed, 1 if it was left alone.
ptrbox_install_squid_conf() {
  local target="$1" rendered="$1.ptrbox-new" ours theirs backup

  ptrbox_render_file "$rendered" "$PTRBOX_ROOT/host/squid.conf.in" "$PTRBOX_ROOT/host" \
    "PREFIX=$PTRBOX_BREW_PREFIX" \
    "PROXY_PORT=$PTRBOX_PROXY_PORT" \
    "VM_SUBNET=$PTRBOX_VM_SUBNET"

  ours="$(ptrbox_marker_version "$rendered")"
  theirs="$(ptrbox_marker_version "$target")"

  if [ -f "$target" ] && cmp -s "$rendered" "$target"; then
    rm -f "$rendered"
    return 1 # identical; nothing to do
  fi

  # Validate the CANDIDATE, before it goes anywhere near the live path. Parsing
  # after installing would leave a config squid refuses to load sitting in
  # place - and the proxy is every VM's only way out.
  if ! squid -f "$rendered" -k parse >/dev/null 2>&1; then
    ptrbox_say "squid rejected the generated config:"
    squid -f "$rendered" -k parse >&2 || true
    rm -f "$rendered"
    ptrbox_die "not installing a config squid cannot parse"
  fi

  if [ -f "$target" ] && [ -z "$theirs" ]; then
    # Not ours: stock Homebrew config, or something hand-written.
    ptrbox_say "$target is not managed by ptrbox. Proposed changes:"
    diff -u "$target" "$rendered" >&2 || true
    if ! ptrbox_confirm "replace $target? (a timestamped backup is kept)"; then
      rm -f "$rendered"
      ptrbox_say "left $target alone; VMs will not have egress until it is replaced"
      return 1
    fi
  elif [ -n "$theirs" ] && [ "$theirs" != "$ours" ]; then
    ptrbox_say "updating ptrbox-managed squid config (v$theirs -> v$ours)"
  fi

  if [ -f "$target" ]; then
    backup="$target.pre-ptrbox.$(date +%Y%m%d%H%M%S)"
    cp "$target" "$backup"
    ptrbox_say "backed up $target -> $backup"
    ptrbox_record_manifest "backup $backup"
  fi

  mv "$rendered" "$target"
  ptrbox_record_manifest "wrote $target"
  ptrbox_say "installed $target"
  return 0
}

# The allowlist is the user's living capability list: installed once, then
# never overwritten - only reported on. Returns 0 if it was created.
ptrbox_install_allowlist() {
  local target="$1" source="$PTRBOX_ROOT/host/allowed_domains.txt"

  if [ ! -f "$target" ]; then
    cp "$source" "$target"
    ptrbox_record_manifest "wrote $target"
    ptrbox_say "installed $target"
    return 0
  fi

  if ! cmp -s "$source" "$target"; then
    ptrbox_say "$target differs from the shipped allowlist (yours is kept)."
    ptrbox_say "compare with: diff $target $source"
  fi
  return 1
}

# --- activation --------------------------------------------------------------

ptrbox_activate_squid() {
  local changed_conf="$1" changed_allowlist="$2" running=""

  if [ "$changed_conf" -eq 0 ] && [ "$changed_allowlist" -eq 0 ]; then
    return 0
  fi

  running="$(brew services list 2>/dev/null | awk '/^squid/ {print $2}')"

  if [ "$changed_conf" -eq 0 ] && [ "$running" = "started" ]; then
    # Allowlist-only change: reconfigure reloads without dropping the listener.
    # A restart severs every live VM tunnel, including Claude's in-flight
    # request, which looks like a permanent failure but is not.
    squid -k reconfigure
    ptrbox_say "reloaded the allowlist (no tunnels dropped)"
  else
    brew services restart squid
    ptrbox_say "restarted squid"
  fi
}

# --- manifest ----------------------------------------------------------------
# What install touched, so a future `ptrbox uninstall` does not have to guess.

ptrbox_record_manifest() {
  local dir
  dir="$(dirname "$(ptrbox_config_path)")"
  mkdir -p "$dir"
  printf '%s\n' "$1" >>"$dir/install-manifest"
}
