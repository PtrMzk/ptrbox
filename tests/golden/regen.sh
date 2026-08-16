#!/usr/bin/env bash
# Regenerate the golden rendered config. Run this deliberately after changing
# vm/claude-repo.yaml or vm/provision/*.sh, then READ THE DIFF - it is the
# review surface for every change to what a sandbox VM contains.
set -euo pipefail

cd "$(dirname "$0")/../.."

# shellcheck source=lib/render.sh
. lib/render.sh
# shellcheck source=tests/fixtures/render-args.sh
. tests/fixtures/render-args.sh

args=()
fixture_load_args

ptrbox_render_file tests/golden/claude-repo.rendered.yaml \
  vm/claude-repo.yaml vm "${args[@]}"

echo "regenerated tests/golden/claude-repo.rendered.yaml"

proxy_args=()
fixture_load_proxy_args

ptrbox_render_file tests/golden/proxy.rendered.yaml \
  vm/proxy.yaml vm "${proxy_args[@]}"

echo "regenerated tests/golden/proxy.rendered.yaml"
