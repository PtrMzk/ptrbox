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
# Where the claude binary landed - and, when PTRBOX_GO is on, the go symlink
# 30-toolchain.sh puts beside it.
export PATH="$HOME/.local/bin:$PATH"
# Go's default GOBIN, where `go install` drops binaries. Named unconditionally,
# like the nvm block above: a PATH entry for a directory that does not exist
# costs nothing, and branching here would need this script to know the
# toolchain.
export PATH="$HOME/go/bin:$PATH"
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

# Silence the stock login banner, so the ptrbox line above is the only thing
# an `ssh lima-<vm>` prints. Two things sit under it, neither ours:
#   - "Last login: <date> from UNKNOWN". UNKNOWN is literal, not a rendering
#     of an empty field: sshd cannot see a peer address for a connection that
#     arrives through lima's relay, so it writes the string into wtmpdb's
#     RemoteHost column and reads it back on the next login, forever. Nothing
#     host-side or guest-side can make it say something true.
#   - Debian's /etc/motd boilerplate.
# ~/.hushlogin suppresses exactly those two (sshd's PrintLastLog and
# PrintMotd); a Banner, were one configured, would still be shown. It costs no
# root, unlike the sshd_config drop-in that would drop only the first line.
touch "$HOME/.hushlogin"

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
# - statusline-command.sh + settings.json: the model to default to ("opus" is
#   an alias tracking the latest Opus-class model, so no updates needed on
#   releases), the statusline that renders above the prompt, and the
#   permission mode a session starts in
# - permissions.defaultMode "auto": Claude Code's own default, which classifies
#   each tool call and asks only about the ones it will not auto-approve. It is
#   a user-level setting on purpose - repo-level settings cannot grant it, and
#   a repo that could would be choosing its own leash. Approvals are a
#   convenience layer here, not the containment: the VM holds under any mode,
#   which is why this can be a default rather than a decision per sandbox. A
#   plan or a model that does not offer auto mode falls back on its own; to opt
#   out, edit this line and re-create.
# - .claude.json: skip the first-run onboarding wizard AND the per-project
#   trust dialog. Auto-trust is a deliberate call: the dialog gates loading
#   repo-provided config (CLAUDE.md, hooks), and in this design the VM itself
#   is the trust boundary - a malicious repo config can't reach anything the
#   sandbox doesn't already concede.
#   NOTE: both files are undocumented internals; format drift shows up as the
#   wizard/dialog reappearing - safe, and the fix is to update this block
#   rather than remove it.
mkdir -p "$HOME/.claude"

# The statusline. Quoted heredoc: every $var and $(...) below belongs to the
# script at render time inside the guest, not to this provisioning shell.
#
# It shells out to jq (in the base package list), git, date and hostname, all
# present. `git -C "$cwd"` reads the repo mount at runtime - which is fine
# here in a way it would not be on the host: the agent already has execution
# inside this VM, so a repo-triggered git exec is no escalation. The host's
# clone is protected separately, by core.hooksPath=/dev/null.
cat >"$HOME/.claude/statusline-command.sh" <<'STATUSLINE'
#!/bin/sh
# Claude Code statusLine command - styled after the Oh My Zsh "ys" theme.
# Provisioned by ptrbox; edit vm/provision/40-userenv.sh and re-create the VM.

input=$(cat)
cwd=$(echo "$input" | jq -r '.workspace.current_dir')
model=$(echo "$input" | jq -r '.model.display_name')
remaining=$(echo "$input" | jq -r '.context_window.remaining_percentage // empty')
transcript=$(echo "$input" | jq -r '.transcript_path // empty')

# Context used % (color-coded: green <50, yellow 50-75, red >75)
if [ -n "$remaining" ]; then
    used=$(echo "$remaining" | awk '{printf "%.0f", 100 - $1}')
    if [ "$used" -ge 75 ] 2>/dev/null; then
        ctx_color="\033[1;31m"
    elif [ "$used" -ge 50 ] 2>/dev/null; then
        ctx_color="\033[1;33m"
    else
        ctx_color="\033[1;32m"
    fi
fi

user=$(whoami)
host=$(hostname -s)

# Git branch (skip optional locks to avoid blocking)
git_branch=$(git -C "$cwd" --no-optional-locks branch --show-current 2>/dev/null)

# Git dirty indicator
git_dirty=""
if [ -n "$git_branch" ]; then
    if [ -n "$(git -C "$cwd" --no-optional-locks status --porcelain 2>/dev/null)" ]; then
        git_dirty="\033[1;31m*\033[0m"
    fi
fi

# Message count from transcript (count assistant turns)
msg_count=""
if [ -n "$transcript" ] && [ -f "$transcript" ]; then
    count=$(grep -c '"role":"assistant"' "$transcript" 2>/dev/null || echo "0")
    if [ "$count" -gt 0 ] 2>/dev/null; then
        msg_count="$count"
    fi
fi

# Timestamp (statusline refreshes after each response, so this is ~last response time)
time_str=$(date +%H:%M:%S)

# Bold blue "#"
part_hash="\033[1;34m#\033[0m"
# Cyan user
part_user="\033[0;36m${user}\033[0m"
# Green host
part_host="\033[0;32m${host}\033[0m"
# Bold yellow cwd
part_cwd="\033[1;33m${cwd}\033[0m"
# Bold magenta model
part_model="\033[1;35m${model}\033[0m"

line="${part_hash} ${part_user} @ ${part_host} in ${part_cwd}"

if [ -n "$git_branch" ]; then
    part_git="on \033[0;34mgit\033[0m:\033[0;36m${git_branch}\033[0m${git_dirty}"
    line="${line} ${part_git}"
fi

line="${line} [${time_str}] | ${part_model}"

if [ -n "$used" ]; then
    line="${line} | ${ctx_color}${used}%\033[0m"
fi

if [ -n "$msg_count" ]; then
    line="${line} | \033[0;36m${msg_count} msgs\033[0m"
fi

printf '%b' "$line"
STATUSLINE
chmod 755 "$HOME/.claude/statusline-command.sh"

# $HOME rather than a literal path: Lima's guest home carries a
# version-dependent suffix and must never be hardcoded.
printf '{"model": "__CLAUDE_MODEL__", "permissions": {"defaultMode": "auto"}, "statusLine": {"type": "command", "command": "%s/.claude/statusline-command.sh"}}\n' \
  "$HOME" >"$HOME/.claude/settings.json"

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
