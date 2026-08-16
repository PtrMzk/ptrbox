#!/usr/bin/env bats
# Tests for lib/render.sh and the rendered VM config.

setup() {
  REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
  cd "$REPO_ROOT"
  # shellcheck source=lib/render.sh
  . lib/render.sh
  # shellcheck source=tests/fixtures/render-args.sh
  . tests/fixtures/render-args.sh
  TMP="$BATS_TEST_TMPDIR"
}

# --- placeholder substitution ------------------------------------------------

@test "substitutes placeholders" {
  printf 'hello __NAME__, port __PORT__\n' >"$TMP/t.yaml"
  run ptrbox_render "$TMP/t.yaml" "$TMP" NAME=world PORT=8888
  [ "$status" -eq 0 ]
  [ "$output" = "hello world, port 8888" ]
}

@test "substitutes values containing spaces and commas" {
  printf '__SET__\n' >"$TMP/t.yaml"
  run ptrbox_render "$TMP/t.yaml" "$TMP" "SET=9.9.9.9, 1.1.1.1"
  [ "$output" = "9.9.9.9, 1.1.1.1" ]
}

@test "leaves unknown placeholders for the caller to catch" {
  printf '__UNSET__\n' >"$TMP/t.yaml"
  run ptrbox_render "$TMP/t.yaml" "$TMP" NAME=world
  [ "$output" = "__UNSET__" ]
}

# --- includes ----------------------------------------------------------------

@test "inlines an include at the marker's indentation" {
  printf 'a: |\n  __INCLUDE:inc.sh__\n' >"$TMP/t.yaml"
  printf '#!/bin/bash\nset -eux\n' >"$TMP/inc.sh"
  run ptrbox_render "$TMP/t.yaml" "$TMP"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "a: |" ]
  [ "${lines[1]}" = "  #!/bin/bash" ]
  [ "${lines[2]}" = "  set -eux" ]
}

@test "substitutes placeholders inside included files" {
  printf '  __INCLUDE:inc.sh__\n' >"$TMP/t.yaml"
  printf 'host=__PROXY_HOST__\n' >"$TMP/inc.sh"
  run ptrbox_render "$TMP/t.yaml" "$TMP" PROXY_HOST=192.168.5.2
  [ "$output" = "  host=192.168.5.2" ]
}

@test "keeps blank lines in includes free of trailing whitespace" {
  printf '  __INCLUDE:inc.sh__\n' >"$TMP/t.yaml"
  printf 'one\n\ntwo\n' >"$TMP/inc.sh"
  ptrbox_render "$TMP/t.yaml" "$TMP" >"$TMP/out"
  # A YAML literal block tolerates it, but trailing whitespace makes every
  # future diff noisy, so assert none is emitted.
  run grep -n ' $' "$TMP/out"
  [ "$status" -ne 0 ]
}

@test "fails on a missing include" {
  printf '  __INCLUDE:nope.sh__\n' >"$TMP/t.yaml"
  run ptrbox_render "$TMP/t.yaml" "$TMP"
  [ "$status" -ne 0 ]
  [[ "$output" == *"included file not found"* ]]
}

@test "fails on nested includes" {
  printf '  __INCLUDE:a.sh__\n' >"$TMP/t.yaml"
  printf '__INCLUDE:b.sh__\n' >"$TMP/a.sh"
  printf 'x\n' >"$TMP/b.sh"
  run ptrbox_render "$TMP/t.yaml" "$TMP"
  [ "$status" -ne 0 ]
  [[ "$output" == *"nested includes"* ]]
}

@test "fails on a missing template" {
  run ptrbox_render "$TMP/nope.yaml" "$TMP"
  [ "$status" -ne 0 ]
}

# --- render to file ----------------------------------------------------------

@test "render_file refuses to write a config with leftover placeholders" {
  printf 'a: __GIT_USER_NAME__\n' >"$TMP/t.yaml"
  run ptrbox_render_file "$TMP/out.yaml" "$TMP/t.yaml" "$TMP"
  [ "$status" -ne 0 ]
  [[ "$output" == *"unsubstituted placeholders"* ]]
  [[ "$output" == *"__GIT_USER_NAME__"* ]]
  [ ! -f "$TMP/out.yaml" ]
}

@test "render_file leaves no temp file behind on failure" {
  printf '  __INCLUDE:nope.sh__\n' >"$TMP/t.yaml"
  run ptrbox_render_file "$TMP/out.yaml" "$TMP/t.yaml" "$TMP"
  [ "$status" -ne 0 ]
  run bash -c "ls $TMP/out.yaml.tmp.* 2>/dev/null"
  [ "$status" -ne 0 ]
}

@test "render_file writes the output when everything resolves" {
  printf 'a: __NAME__\n' >"$TMP/t.yaml"
  ptrbox_render_file "$TMP/out.yaml" "$TMP/t.yaml" "$TMP" NAME=ok
  [ "$(cat "$TMP/out.yaml")" = "a: ok" ]
}

# --- the real template -------------------------------------------------------

@test "the VM template renders with no leftover placeholders" {
  args=()
  fixture_load_args
  run ptrbox_render_file "$TMP/rendered.yaml" vm/claude-repo.yaml vm "${args[@]}"
  [ "$status" -eq 0 ]
}

@test "rendered VM config matches the golden file" {
  args=()
  fixture_load_args
  ptrbox_render_file "$TMP/rendered.yaml" vm/claude-repo.yaml vm "${args[@]}"
  # If this fails on an intentional change: tests/golden/regen.sh, then review
  # the diff - it shows exactly how the sandbox changed.
  run diff -u tests/golden/claude-repo.rendered.yaml "$TMP/rendered.yaml"
  [ "$status" -eq 0 ]
}

@test "the proxy VM template renders with no leftover placeholders" {
  proxy_args=()
  fixture_load_proxy_args
  run ptrbox_render_file "$TMP/proxy.yaml" vm/proxy.yaml vm "${proxy_args[@]}"
  [ "$status" -eq 0 ]
}

@test "rendered proxy VM config matches the golden file" {
  proxy_args=()
  fixture_load_proxy_args
  ptrbox_render_file "$TMP/proxy.yaml" vm/proxy.yaml vm "${proxy_args[@]}"
  run diff -u tests/golden/proxy.rendered.yaml "$TMP/proxy.yaml"
  [ "$status" -eq 0 ]
}
