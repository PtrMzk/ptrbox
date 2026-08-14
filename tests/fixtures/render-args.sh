#!/usr/bin/env bash
# Fixed template values used by both the golden-file test and the script that
# regenerates the golden file. Deliberately not the real defaults - obviously
# fake host paths and identity keep personal data out of the checked-in golden
# file, and make an accidental "regenerated on my machine" diff obvious.
fixture_render_args() {
  cat <<'ARGS'
REPO_DIR=/Users/example/code/demo
IMAGE_URL=https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2
CPUS=4
MEMORY=8GiB
DISK=50GiB
PORT_MIN=3000
PORT_MAX=9000
DNS_NFT_SET=9.9.9.9, 1.1.1.1
DNS_LIST=9.9.9.9 1.1.1.1
PROXY_HOST=192.168.5.2
PROXY_PORT=8888
GIT_USER_NAME=Example Dev
GIT_USER_EMAIL=dev@example.com
CLAUDE_MODEL=opus
ARGS
}

# Read the fixture into the caller's `args` array (bash 3.2: no mapfile).
fixture_load_args() {
  args=()
  local line
  while IFS= read -r line; do
    args+=("$line")
  done < <(fixture_render_args)
}
