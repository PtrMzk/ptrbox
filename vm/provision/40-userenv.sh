#!/bin/bash
# =============================================================================
# 40-userenv.sh - shell environment, git identity, Claude Code defaults.
# Runs as the unprivileged agent user.
# =============================================================================
set -eux

if [ -f "$HOME/.ptrbox/userenv.done" ]; then
  exit 0
fi

# Steady-state environment. Goes in ~/.profile, NOT ~/.bashrc: Debian's stock
# .bashrc begins with an interactive-shell guard that returns early for
# scripts, so exports appended there are invisible to non-interactive shells
# (ssh host cmd, bash -lc, ptrbox's own verification run). .profile is read by
# every login shell.
cat >>"$HOME/.profile" <<'RC'
# Load nvm (node). The nvm installer only wires itself into .bashrc, below the
# interactive guard - invisible to scripts. Loading it here puts node on PATH
# for every login shell, interactive or not.
export NVM_DIR="$HOME/.nvm"
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"
# Route cooperative tools (curl, npm, pip, git, claude) through the host Squid
# proxy. This is the polite/routing layer; nftables is the enforcement layer
# that makes the proxy the ONLY physical way out.
export HTTPS_PROXY="http://__PROXY_HOST__:__PROXY_PORT__"
export HTTP_PROXY="$HTTPS_PROXY"
# Don't proxy the VM's own local traffic (tests hitting localhost, etc.)
export NO_PROXY="localhost,127.0.0.1"
# Don't even attempt telemetry calls - their domains are deliberately off the
# allowlist, so attempts would just spam TCP_DENIED in the squid log.
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
# Where the claude binary landed.
export PATH="$HOME/.local/bin:$PATH"
RC

# Interactive-only quality of life. This one goes in ~/.bashrc, BELOW the
# stock interactive guard: prompts, aliases and banners are meaningless to
# scripts, and here the early return works in our favor - none of this can
# leak into `bash -lc` output or the verification run.
cat >>"$HOME/.bashrc" <<'RC'

# --- ptrbox ------------------------------------------------------------------
# Make this shell unmistakably the sandbox: VM name in the prompt and in the
# terminal title, so a host terminal and a sandbox terminal never look alike.
if [ -f /usr/lib/git-core/git-sh-prompt ]; then
  . /usr/lib/git-core/git-sh-prompt
fi
# The name's color is derived from the name on the host (ptrbox_vm_color),
# so every sandbox keeps its own hue across recreations.
if declare -F __git_ps1 >/dev/null; then
  # Branch only - no dirty-state markers, which cost a status walk per prompt.
  PS1='\[\e[__VM_COLOR__m\][__VM_NAME__]\[\e[0m\] \[\e[1;34m\]\w\[\e[0m\]\[\e[0;36m\]$(__git_ps1 " (%s)")\[\e[0m\]\$ '
else
  PS1='\[\e[__VM_COLOR__m\][__VM_NAME__]\[\e[0m\] \[\e[1;34m\]\w\[\e[0m\]\$ '
fi
case "$TERM" in
xterm* | rxvt* | tmux* | screen*)
  PS1="\[\e]0;__VM_NAME__ \w\a\]$PS1"
  ;;
esac

alias ls='ls --color=auto'
alias grep='grep --color=auto'

# Once per ssh login (tmux panes are non-login shells, so they stay quiet):
# start where the work is, and say what this box is.
if shopt -q login_shell; then
  [ "$PWD" = "$HOME" ] && cd /workspace
  printf '\e[__VM_COLOR__m[__VM_NAME__]\e[0m ptrbox sandbox - repo: /workspace, egress: allowlist proxy, sudo: off\n'
fi
RC

# Git identity for agent commits - set globally in the VM so the agent never
# has to guess one (it will otherwise copy whatever it finds in git log, or
# invent something). ptrbox fills these in from the HOST's global git config;
# empty means the host had none, in which case leave git to complain rather
# than commit under a made-up name. Global-in-VM lives outside the mount, so it
# covers every repo in this VM without touching host repos.
git_name="__GIT_USER_NAME__"
git_email="__GIT_USER_EMAIL__"
if [ -n "$git_name" ]; then
  git config --global user.name "$git_name"
fi
if [ -n "$git_email" ]; then
  git config --global user.email "$git_email"
fi

# Claude Code defaults, pre-seeded so fresh VMs go straight to work:
# - settings.json: the model to default to ("opus" is an alias tracking the
#   latest Opus-class model, so no updates needed on releases)
# - .claude.json: skip the first-run onboarding wizard AND the per-project
#   trust dialog. Auto-trust is a deliberate call: the dialog gates loading
#   repo-provided config (CLAUDE.md, hooks), and in this design the VM itself
#   is the trust boundary - a malicious repo config can't reach anything the
#   sandbox doesn't already concede.
#   NOTE: both files are undocumented internals; format drift shows up as the
#   wizard/dialog reappearing - safe, and the fix is to update this block
#   rather than remove it.
mkdir -p "$HOME/.claude"
printf '{"model": "__CLAUDE_MODEL__"}\n' >"$HOME/.claude/settings.json"
cat >"$HOME/.claude.json" <<'JSON'
{
  "hasCompletedOnboarding": true,
  "projects": {
    "/workspace": {
      "hasTrustDialogAccepted": true,
      "hasCompletedProjectOnboarding": true
    }
  }
}
JSON

mkdir -p "$HOME/.ptrbox"
touch "$HOME/.ptrbox/userenv.done"
