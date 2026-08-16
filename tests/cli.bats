#!/usr/bin/env bats
# Tests for bin/ptrbox dispatch.

setup() {
  REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
  PTRBOX="$REPO_ROOT/bin/ptrbox"
  TMP="$BATS_TEST_TMPDIR"
}

@test "help lists the commands" {
  run "$PTRBOX" help
  [ "$status" -eq 0 ]
  [[ "$output" == *"ptrbox new"* ]]
  [[ "$output" == *"ptrbox logs"* ]]
  [[ "$output" == *"start"* ]]
  [[ "$output" == *"stop"* ]]
}

@test "no arguments prints help rather than failing" {
  run "$PTRBOX"
  [ "$status" -eq 0 ]
  [[ "$output" == *"USAGE"* ]]
}

@test "version prints a version" {
  run "$PTRBOX" version
  [ "$status" -eq 0 ]
  [[ "$output" == ptrbox\ [0-9]* ]]
}

@test "an unknown command fails with usage on stderr" {
  run "$PTRBOX" frobnicate
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown command: frobnicate"* ]]
}

@test "resolves its root through a symlink" {
  # People symlink bin/ptrbox into ~/bin; PTRBOX_ROOT must still find lib/.
  mkdir -p "$TMP/bin"
  ln -s "$PTRBOX" "$TMP/bin/ptrbox"
  run "$TMP/bin/ptrbox" version
  [ "$status" -eq 0 ]
}
