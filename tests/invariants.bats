#!/usr/bin/env bats
# =============================================================================
# The sandbox's security invariants, as executable assertions.
#
# These are the rules from CLAUDE.md - one mount, no root, default-deny egress,
# no credentials in the VM. Written as tests so that weakening one fails the
# suite instead of relying on somebody noticing during review. If a change here
# is deliberate, the diff to this file is the argument for it.
# =============================================================================

setup() {
  REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
  cd "$REPO_ROOT"
  TMP="$BATS_TEST_TMPDIR"

  # shellcheck source=lib/render.sh
  . lib/render.sh
  # shellcheck source=tests/fixtures/render-args.sh
  . tests/fixtures/render-args.sh

  args=()
  fixture_load_args
  RENDERED="$TMP/rendered.yaml"
  ptrbox_render_file "$RENDERED" vm/claude-repo.yaml vm "${args[@]}"

  # Comments stripped: "must not appear" assertions have to be about what the
  # config DOES, not about prose. The header comment legitimately mentions
  # ~/.ssh and secrets while explaining why neither is present.
  STRIPPED="$TMP/stripped.yaml"
  sed 's/#.*//' "$RENDERED" >"$STRIPPED"

  # Just the mounts block: `- location:` also appears under images:.
  MOUNTS="$TMP/mounts"
  awk '/^mounts:/{f=1;next} /^[a-zA-Z]/{f=0} f' "$STRIPPED" >"$MOUNTS"

  NFT="$STRIPPED"
}

# --- one mount ---------------------------------------------------------------

@test "invariant: exactly one mount" {
  [ "$(grep -c '^  - location:' "$MOUNTS")" -eq 1 ]
}

@test "invariant: the only mount is the project, at /workspace" {
  grep -q 'location: "/Users/example/code/demo"' "$RENDERED"
  [ "$(grep -c 'mountPoint: "/workspace"' "$RENDERED")" -eq 1 ]
}

@test "invariant: no home directory or credential paths are mounted" {
  # Lima's historic default mounted the whole home directory, which would hand
  # the agent ~/.ssh, ~/.aws and Documents.
  run grep -E '\$HOME|~/|\.ssh|\.aws|\.config/gh|Documents' "$MOUNTS"
  [ "$status" -ne 0 ]
  run grep -E 'location: "/Users/[^/]*"' "$MOUNTS"
  [ "$status" -ne 0 ]
}

@test "invariant: the template does not inherit a Lima base config" {
  # `base:` would pull in Lima's defaults, including the home-directory mount
  # this whole design exists to exclude.
  run grep -E '^base:' "$RENDERED"
  [ "$status" -ne 0 ]
}

# --- no root -----------------------------------------------------------------

@test "invariant: passwordless sudo is removed" {
  grep -q 'rm -f /etc/sudoers.d/90-cloud-init-users' "$RENDERED"
  grep -q "grep -rl 'NOPASSWD' /etc/sudoers.d/" "$RENDERED"
}

@test "invariant: nothing grants NOPASSWD back" {
  # Any line that WRITES a NOPASSWD rule, as opposed to removing one.
  run grep -E '(echo|printf|cat).*NOPASSWD' "$RENDERED"
  [ "$status" -ne 0 ]
  run grep -E 'visudo|sudoers\.d/[a-z0-9-]+ *<<' "$RENDERED"
  [ "$status" -ne 0 ]
}

@test "invariant: sudo removal is not skipped on later boots" {
  # It deliberately has no done-marker guard, so it re-asserts every boot.
  run grep -E 'ptrbox/nosudo\.done|sudo\.done' "$RENDERED"
  [ "$status" -ne 0 ]
}

# --- default-deny egress -----------------------------------------------------

@test "invariant: the firewall's default verdict is drop" {
  grep -q 'policy drop;' "$NFT"
}

@test "invariant: exactly five egress allowances, and no more" {
  # Loopback, established, DNS over udp and tcp, and the proxy. Anything else
  # is a new hole in the wall.
  [ "$(grep -cE '[[:space:]]accept[[:space:]]*$' "$NFT")" -eq 5 ]
}

@test "invariant: the only route out is the configured proxy" {
  grep -qE 'ip daddr 192\.168\.5\.2 tcp dport 8888 accept' "$NFT"
  # No blanket HTTPS egress, and no second address.
  run grep -E 'tcp dport (443|80) accept' "$NFT"
  [ "$status" -ne 0 ]
}

@test "invariant: DNS is pinned to the configured resolvers" {
  grep -qE 'ip daddr \{ 9\.9\.9\.9, 1\.1\.1\.1 \} udp dport 53 accept' "$NFT"
  grep -qE 'ip daddr \{ 9\.9\.9\.9, 1\.1\.1\.1 \} tcp dport 53 accept' "$NFT"
  # An unpinned port 53 would be a covert exfiltration channel.
  run grep -E '^[^i]*dport 53 accept' "$NFT"
  [ "$status" -ne 0 ]
}

@test "invariant: the resolver cannot be rewritten back to DHCP's" {
  grep -q 'chattr +i /etc/resolv.conf' "$RENDERED"
}

@test "invariant: the firewall starts on every boot" {
  grep -q 'systemctl enable sandbox-firewall.service' "$RENDERED"
  grep -q 'WantedBy=multi-user.target' "$RENDERED"
}

@test "invariant: the firewall ruleset is not agent-readable" {
  grep -q 'chmod 600 /etc/nftables-sandbox.nft' "$RENDERED"
}

# --- no credentials, no host reach -------------------------------------------

@test "invariant: ssh agent forwarding is off" {
  grep -q 'forwardAgent: false' "$RENDERED"
}

@test "invariant: no credentials are baked into the VM config" {
  # The token reaches a VM over stdin at creation time; it must never be part
  # of the config, which persists on disk under ~/.lima/_generated.
  run grep -iE 'oauth|token|password|api[_-]?key|secret' "$STRIPPED"
  [ "$status" -ne 0 ]
}

@test "invariant: proxy environment points at the configured proxy only" {
  grep -q 'export HTTPS_PROXY="http://192.168.5.2:8888"' "$RENDERED"
  [ "$(grep -c 'HTTPS_PROXY=' "$RENDERED")" -eq 1 ]
}

# --- provisioning safety -----------------------------------------------------

@test "invariant: every network-dependent provision step is guarded" {
  # Lima re-runs provision scripts on EVERY boot. Unguarded, a post-firewall
  # boot hangs on network calls until cloud-init gives up ten minutes later.
  local f
  for f in vm/provision/10-base.sh vm/provision/20-firewall.sh \
    vm/provision/30-toolchain.sh vm/provision/40-userenv.sh; do
    grep -q '\.done' "$f" || {
      echo "$f has no done-marker guard" >&2
      return 1
    }
  done
}

@test "invariant: provision scripts stop on the first error" {
  local f
  for f in vm/provision/*.sh; do
    grep -q '^set -eux$' "$f" || {
      echo "$f does not set -eux" >&2
      return 1
    }
  done
}

# --- the repo itself ---------------------------------------------------------

@test "invariant: no secrets are committed" {
  # Fake values in tests are marked EXAMPLE and excluded deliberately.
  run bash -c "git ls-files -z | xargs -0 grep -nE 'sk-ant-[A-Za-z0-9-]{8}|-----BEGIN [A-Z ]*PRIVATE KEY|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{20}' | grep -v EXAMPLE"
  [ "$status" -ne 0 ]
}

@test "invariant: the verification script checks what matters" {
  # A verify.sh that quietly stopped testing the wall would be worse than none.
  grep -q 'sudo -n true' vm/verify.sh
  grep -q 'noproxy' vm/verify.sh
  grep -q 'mount -t virtiofs' vm/verify.sh
  grep -q 'exit 1' vm/verify.sh
}
