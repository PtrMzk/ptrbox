// Package invariants asserts the sandbox's security properties.
//
// These are the rules from CLAUDE.md - one mount, no root, default-deny
// egress, no credentials in the VM - written as tests so that weakening one
// fails the suite instead of relying on somebody noticing during review. If a
// change here is deliberate, the diff to this file is the argument for it.
package invariants

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/rendertest"
)

// The rendered sandbox config, and the same thing with comments stripped.
//
// "Must not appear" assertions have to be about what the config DOES, not
// about prose: the header comments legitimately mention ~/.ssh and secrets
// while explaining why neither is present.
func sandbox(t *testing.T) (rendered, stripped string) {
	t.Helper()
	rendered = rendertest.Sandbox(t)
	return rendered, stripComments(rendered)
}

func proxyVM(t *testing.T) (rendered, stripped string) {
	t.Helper()
	rendered = rendertest.Proxy(t)
	return rendered, stripComments(rendered)
}

func stripComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// mountsBlock is just the mounts: section - `- location:` also appears under
// images:.
func mountsBlock(stripped string) string {
	var out []string
	in := false
	for _, line := range strings.Split(stripped, "\n") {
		if strings.HasPrefix(line, "mounts:") {
			in = true
			continue
		}
		if in && len(line) > 0 && isTopLevelKey(line[0]) {
			in = false
		}
		if in {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func isTopLevelKey(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func mustMatch(t *testing.T, body, pattern, why string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(body) {
		t.Errorf("%s\nexpected to find: %s", why, pattern)
	}
}

func mustNotMatch(t *testing.T, body, pattern, why string) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	if hits := re.FindAllString(body, -1); len(hits) > 0 {
		t.Errorf("%s\nfound: %q", why, hits)
	}
}

func countMatches(body, pattern string) int {
	return len(regexp.MustCompile(pattern).FindAllString(body, -1))
}

func asset(t *testing.T, name string) string { return rendertest.Asset(t, name) }

// --- one mount ---------------------------------------------------------------

func TestExactlyOneMount(t *testing.T) {
	_, stripped := sandbox(t)
	if n := countMatches(mountsBlock(stripped), `(?m)^  - location:`); n != 1 {
		t.Errorf("the sandbox declares %d mounts, want exactly 1", n)
	}
}

func TestTheOnlyMountIsTheProjectAtWorkspace(t *testing.T) {
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `location: "/Users/example/code/demo"`,
		"the mount does not point at the project directory")
	if n := countMatches(rendered, `mountPoint: "/workspace"`); n != 1 {
		t.Errorf("/workspace appears as a mountPoint %d times, want 1", n)
	}
}

func TestNoHomeDirectoryOrCredentialPathsAreMounted(t *testing.T) {
	// Lima's historic default mounted the whole home directory, which would
	// hand the agent ~/.ssh, ~/.aws and Documents.
	_, stripped := sandbox(t)
	mounts := mountsBlock(stripped)
	mustNotMatch(t, mounts, `\$HOME|~/|\.ssh|\.aws|\.config/gh|Documents`,
		"a credential-bearing host path is mounted")
	mustNotMatch(t, mounts, `location: "/Users/[^/]*"`,
		"a whole home directory is mounted")
}

func TestTheGuestImageIsFetchedOverHTTPS(t *testing.T) {
	// Lima verifies nothing beyond TLS, so the transport is the only thing
	// standing between you and a swapped image.
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `location: "https://`, "the image is not fetched over https")
	mustNotMatch(t, rendered, `location: "http://`, "an image is fetched over plain http")
}

func TestTheTemplateDoesNotInheritALimaBaseConfig(t *testing.T) {
	// `base:` would pull in Lima's defaults, including the home-directory
	// mount this whole design exists to exclude.
	rendered, _ := sandbox(t)
	mustNotMatch(t, rendered, `(?m)^base:`, "the template inherits a Lima base config")
}

// --- no root -----------------------------------------------------------------

func TestPasswordlessSudoIsRemoved(t *testing.T) {
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `rm -f /etc/sudoers\.d/90-cloud-init-users`,
		"cloud-init's passwordless sudo is not removed")
	mustMatch(t, rendered, `grep -rl 'NOPASSWD' /etc/sudoers\.d/`,
		"leftover NOPASSWD drop-ins are not swept")
}

func TestNothingGrantsNOPASSWDBack(t *testing.T) {
	// Any line that WRITES a NOPASSWD rule, as opposed to removing one.
	rendered, _ := sandbox(t)
	mustNotMatch(t, rendered, `(echo|printf|cat).*NOPASSWD`, "something writes a NOPASSWD rule")
	mustNotMatch(t, rendered, `visudo|sudoers\.d/[a-z0-9-]+ *<<`, "something authors a sudoers file")
}

func TestSudoRemovalIsNotSkippedOnLaterBoots(t *testing.T) {
	// It deliberately has no done-marker guard, so it re-asserts every boot.
	rendered, _ := sandbox(t)
	mustNotMatch(t, rendered, `ptrbox/nosudo\.done|sudo\.done`,
		"the sudo removal is guarded by a done-marker and so runs only once")
}

// --- default-deny egress -----------------------------------------------------

func TestTheFirewallsDefaultVerdictIsDrop(t *testing.T) {
	_, stripped := sandbox(t)
	mustMatch(t, stripped, `policy drop;`, "the firewall chain does not default to drop")
}

func TestExactlyFiveEgressAllowancesAndNoMore(t *testing.T) {
	// Loopback, established, DNS over udp and tcp, and the proxy. Anything
	// else is a new hole in the wall.
	_, stripped := sandbox(t)
	if n := countMatches(stripped, `(?m)[ \t]accept[ \t]*$`); n != 5 {
		t.Errorf("the firewall has %d accept rules, want exactly 5", n)
	}
}

func TestTheOnlyRouteOutIsTheConfiguredProxy(t *testing.T) {
	_, stripped := sandbox(t)
	mustMatch(t, stripped, `ip daddr 192\.168\.5\.2 tcp dport 8888 accept`,
		"the proxy is not the destination of the one egress rule")
	// No blanket HTTPS egress, and no second address.
	mustNotMatch(t, stripped, `tcp dport (443|80) accept`, "there is blanket web egress")
}

func TestDNSIsPinnedToTheConfiguredResolvers(t *testing.T) {
	_, stripped := sandbox(t)
	mustMatch(t, stripped, `ip daddr \{ 9\.9\.9\.9, 1\.1\.1\.1 \} udp dport 53 accept`,
		"udp DNS is not pinned")
	mustMatch(t, stripped, `ip daddr \{ 9\.9\.9\.9, 1\.1\.1\.1 \} tcp dport 53 accept`,
		"tcp DNS is not pinned")
	// An unpinned port 53 would be a covert exfiltration channel.
	mustNotMatch(t, stripped, `(?m)^[^i]*dport 53 accept`, "there is unpinned port-53 egress")
}

func TestTheResolverCannotBeRewrittenBackToDHCPs(t *testing.T) {
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `chattr \+i /etc/resolv\.conf`, "resolv.conf is not made immutable")
}

func TestTheFirewallStartsOnEveryBoot(t *testing.T) {
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `systemctl enable sandbox-firewall\.service`,
		"the firewall service is not enabled")
	mustMatch(t, rendered, `WantedBy=multi-user\.target`, "the firewall unit has no install target")
}

func TestTheFirewallRulesetIsNotAgentReadable(t *testing.T) {
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `chmod 600 /etc/nftables-sandbox\.nft`,
		"the ruleset is readable by the agent")
}

// --- no credentials, no host reach -------------------------------------------

func TestSSHAgentForwardingIsOff(t *testing.T) {
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `forwardAgent: false`, "ssh agent forwarding is not disabled")
}

func TestNoCredentialsAreBakedIntoTheVMConfig(t *testing.T) {
	// The token reaches a VM over stdin at creation time; it must never be
	// part of the config, which persists on disk under ~/.lima/_generated.
	_, stripped := sandbox(t)
	mustNotMatch(t, stripped, `(?i)oauth|token|password|api[_-]?key|secret`,
		"the generated config mentions a credential")
}

func TestProxyEnvironmentPointsAtTheConfiguredProxyOnly(t *testing.T) {
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `export HTTPS_PROXY="http://192\.168\.5\.2:8888"`,
		"the guest does not point at the configured proxy")
	if n := countMatches(rendered, `HTTPS_PROXY=`); n != 1 {
		t.Errorf("HTTPS_PROXY is set %d times, want 1", n)
	}
}

// --- the proxy VM ------------------------------------------------------------
// Squid parses attacker-influenceable bytes from every sandbox; these pin the
// properties that make its VM a blast chamber rather than a second host.

func TestTheProxyVMMountsNothing(t *testing.T) {
	_, stripped := proxyVM(t)
	mustMatch(t, stripped, `(?m)^mounts: \[\]`, "the proxy VM does not declare an empty mount list")
	// Belt and braces: no mount entry anywhere outside the images block.
	mustNotMatch(t, stripped, `mountPoint|writable`, "the proxy VM has a mount")
}

func TestTheProxyVMsOnlyHostSurfaceIsALoopbackForward(t *testing.T) {
	rendered, stripped := proxyVM(t)
	mustMatch(t, rendered, `hostIP: "127\.0\.0\.1"`, "the forward is not loopback-bound")
	if n := countMatches(stripped, `hostPort`); n != 1 {
		t.Errorf("the proxy VM forwards %d host ports, want 1", n)
	}
	mustNotMatch(t, stripped, `hostIP: "(0\.0\.0\.0|::)?"`, "the forward is bound beyond loopback")
}

func TestNoCredentialsReachTheProxyVM(t *testing.T) {
	_, stripped := proxyVM(t)
	mustNotMatch(t, stripped, `(?i)oauth|token|password|api[_-]?key|secret|keychain`,
		"the proxy VM config mentions a credential")
}

func TestTheProxyVMGetsSquidAndNoneOfTheSandboxToolchain(t *testing.T) {
	rendered, stripped := proxyVM(t)
	mustMatch(t, rendered, `apt-get install -y squid`, "the proxy VM does not install squid")
	mustNotMatch(t, stripped, `nvm|node|claude|playwright|build-essential`,
		"the proxy VM installs sandbox toolchain")
}

func TestTheProxyVMForwardsNoSSHAgent(t *testing.T) {
	rendered, _ := proxyVM(t)
	mustMatch(t, rendered, `forwardAgent: false`, "the proxy VM forwards the ssh agent")
}

func TestTheSquidConfigKeepsDefaultDenyLast(t *testing.T) {
	// Rules evaluate top to bottom, first match wins; anything after "deny
	// all" is dead, anything missing it is an open proxy.
	var rules []string
	for _, line := range strings.Split(asset(t, "host/squid.conf.in"), "\n") {
		if strings.HasPrefix(line, "http_access") {
			rules = append(rules, line)
		}
	}
	if len(rules) == 0 {
		t.Fatal("the squid config has no http_access rules at all")
	}
	if last := rules[len(rules)-1]; last != "http_access deny all" {
		t.Errorf("the last http_access rule is %q, want \"http_access deny all\"", last)
	}
}

// --- provisioning safety -----------------------------------------------------

func TestEveryNetworkDependentProvisionStepIsGuarded(t *testing.T) {
	// Lima re-runs provision scripts on EVERY boot. Unguarded, a post-firewall
	// boot hangs on network calls until cloud-init gives up ten minutes later.
	for _, name := range []string{
		"vm/provision/10-base.sh",
		"vm/provision/15-extra-packages.sh",
		"vm/provision/20-firewall.sh",
		"vm/provision/30-toolchain.sh",
		"vm/provision/40-userenv.sh",
		"vm/provision-proxy/10-squid.sh",
	} {
		if !strings.Contains(asset(t, name), ".done") {
			t.Errorf("%s has no done-marker guard", name)
		}
	}
}

func TestTheExtraPackageListIsFixedAtRenderTime(t *testing.T) {
	// The list is substituted into the generated config on the host. A list
	// read inside the guest at boot - from a file, a command, or anything
	// under the repo mount - would let the agent install software into its own
	// sandbox on its next boot.
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `EXTRA_PACKAGES=""`, "the fixture's empty list is not rendered as a literal")
	mustNotMatch(t, rendered, "EXTRA_PACKAGES=.*(\\$\\(|`|<|cat |curl |workspace)",
		"the package list is computed inside the guest")
}

func TestProvisionScriptsNeverReadTheRepoMount(t *testing.T) {
	// Nothing inside /workspace may influence provisioning. 40-userenv.sh gets
	// a pass: it WRITES the string "/workspace" into Claude Code's settings
	// pre-seed, which is not a read.
	for _, name := range provisionScripts(t, "vm/provision") {
		if name == "vm/provision/40-userenv.sh" {
			continue
		}
		if strings.Contains(asset(t, name), "/workspace") {
			t.Errorf("%s references the repo mount", name)
		}
	}
}

func TestProvisionScriptsStopOnTheFirstError(t *testing.T) {
	scripts := append(provisionScripts(t, "vm/provision"), provisionScripts(t, "vm/provision-proxy")...)
	for _, name := range scripts {
		if !regexp.MustCompile(`(?m)^set -eux$`).MatchString(asset(t, name)) {
			t.Errorf("%s does not set -eux", name)
		}
	}
}

func provisionScripts(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := ptrbox.Assets.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sh") {
			names = append(names, dir+"/"+entry.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("no provision scripts found in %s", dir)
	}
	return names
}

// --- the repo itself ---------------------------------------------------------

func TestNoSecretsAreCommitted(t *testing.T) {
	// Fake values in tests are marked EXAMPLE and excluded deliberately.
	secret := regexp.MustCompile(
		`sk-ant-[A-Za-z0-9-]{8}|-----BEGIN [A-Z ]*PRIVATE KEY|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{20}`)

	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(body), "\n") {
			if secret.MatchString(line) && !strings.Contains(line, "EXAMPLE") {
				t.Errorf("%s:%d looks like a committed secret", name, i+1)
			}
		}
	}
}

func TestTheVerificationScriptChecksWhatMatters(t *testing.T) {
	// A verify.sh that quietly stopped testing the wall would be worse than
	// none.
	verify := asset(t, "vm/verify.sh")
	for _, want := range []string{"sudo -n true", "noproxy", "mount -t virtiofs", "exit 1"} {
		if !strings.Contains(verify, want) {
			t.Errorf("vm/verify.sh no longer checks %q", want)
		}
	}
}

// The "every declared dependency is actually used" invariant lives with the
// dependency table it guards, in internal/cli.

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}
