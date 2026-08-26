// Package invariants asserts the sandbox's security properties.
//
// These are the rules from CLAUDE.md - one mount, no root, default-deny
// egress, no credentials in the VM - written as tests so that weakening one
// fails the suite instead of relying on somebody noticing during review. If a
// change here is deliberate, the diff to this file is the argument for it.
package invariants

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/config"
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

// renderedSquidConf is the squid config as the proxy VM receives it - the
// listener block is generated, so assertions about it need the rendered form.
func renderedSquidConf(t *testing.T) string { return rendertest.SquidConf(t) }

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
	// 8889 is this VM's OWN allocated proxy port (the fixture's first
	// allocation), not the shared base port: the port is the sandbox's
	// identity at squid, and the firewall pinning it to exactly one is what
	// makes that identity kernel-enforced rather than claimed.
	_, stripped := sandbox(t)
	mustMatch(t, stripped, `ip daddr 192\.168\.5\.2 tcp dport 8889 accept`,
		"the proxy is not the destination of the one egress rule")
	// No blanket HTTPS egress, and no second address.
	mustNotMatch(t, stripped, `tcp dport (443|80) accept`, "there is blanket web egress")
}

func TestTheSandboxDialsExactlyOneProxyPort(t *testing.T) {
	// One dport rule toward the proxy host. A second one would let this VM
	// borrow another sandbox's identity - and with it, in item 38, another
	// sandbox's allowlist.
	_, stripped := sandbox(t)
	if n := countMatches(stripped, `ip daddr 192\.168\.5\.2 tcp dport \d+ accept`); n != 1 {
		t.Errorf("the firewall allows %d ports toward the proxy host, want exactly 1", n)
	}
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
	// The same per-VM port the firewall allows: the cooperative layer and the
	// enforcement layer must name the same destination.
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `export HTTPS_PROXY="http://192\.168\.5\.2:8889"`,
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

func TestTheProxyVMsOnlyHostSurfaceIsLoopbackForwards(t *testing.T) {
	// Two forwards since item 37: the base port and the per-sandbox range.
	// Every one of them loopback-bound - nothing on the LAN may reach squid.
	_, stripped := proxyVM(t)
	forwards := countMatches(stripped, `(?m)^  - guestPort`)
	if forwards != 2 {
		t.Errorf("the proxy VM declares %d forwards, want exactly 2 (base port + sandbox range)", forwards)
	}
	if n := countMatches(stripped, `hostIP: "127\.0\.0\.1"`); n != forwards {
		t.Errorf("%d of %d forwards are loopback-bound; every one must be", n, forwards)
	}
	mustNotMatch(t, stripped, `hostIP: "(0\.0\.0\.0|::)?"`, "a forward is bound beyond loopback")
	// The range is the sandbox slots and nothing more.
	mustMatch(t, stripped, `guestPortRange: \[8889, 8904\]`,
		"the sandbox port range is not the 16 slots above the base port")
	mustMatch(t, stripped, `hostPortRange: \[8889, 8904\]`,
		"the host side of the range does not mirror the guest side")
}

func TestEverySquidListenerIsForwardedAndViceVersa(t *testing.T) {
	// The listener set and the forward set are the same 17 ports. A listener
	// without a forward is a sandbox slot that cannot carry traffic; a forward
	// without a listener never comes up at all (lima publishes a forward only
	// while the guest port has one) and fails exactly like the first case:
	// an agent with no network, minutes later.
	conf := renderedSquidConf(t)
	if !strings.Contains(conf, "\nhttp_port 8888\n") {
		t.Error("squid does not listen on the base port")
	}
	for port := 8889; port <= 8904; port++ {
		if !strings.Contains(conf, fmt.Sprintf("\nhttp_port %d\n", port)) {
			t.Errorf("squid does not listen on sandbox port %d", port)
		}
	}
	if n := countMatches(conf, `(?m)^http_port `); n != 17 {
		t.Errorf("squid listens on %d ports, want exactly 17 (base + 16 sandbox slots)", n)
	}
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

func TestTheSquidConfigDeniesTunnelsIntoPrivateAddressSpace(t *testing.T) {
	// The allowlist checks the NAME a tunnel asks for; to_internal checks the
	// ADDRESS it resolves to. Without it, an allowlisted domain that resolves
	// (or rebinds) to a private address opens a tunnel from the proxy VM's
	// network position - the Mac's LAN - which no sandbox has business
	// reaching.
	conf := asset(t, "host/squid.conf.in")
	for _, network := range []string{
		"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "::1", "fc00::/7", "fe80::/10",
	} {
		if !regexp.MustCompile(`(?m)^acl to_internal dst .*` + regexp.QuoteMeta(network)).MatchString(conf) {
			t.Errorf("the to_internal ACL no longer covers %s", network)
		}
	}

	// Ordering is the teeth: rules evaluate top to bottom, first match wins,
	// so a deny BELOW the allow never sees an allowlisted name - and the
	// allowlisted-name-resolving-inward case is the whole point.
	deny, allow := -1, -1
	for i, line := range strings.Split(conf, "\n") {
		switch {
		case strings.HasPrefix(line, "http_access deny to_internal"):
			deny = i
		case strings.HasPrefix(line, "http_access allow"):
			allow = i
		}
	}
	if deny == -1 {
		t.Fatal("the squid config no longer denies to_internal")
	}
	if allow != -1 && deny > allow {
		t.Error("the to_internal deny sits below the allow rule, where an allowlisted name has already matched")
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

// Invariant 3, for the per-VM config layer. Per-project settings idiomatically
// live in a dotfile in the project, and that is the one place these may never
// be: the repo is mounted into the sandbox, so a package list living there
// would let the agent choose what gets installed into its own VM on the next
// re-create. Keyed by VM name, under the config directory, always.
func TestPerVMConfigLivesBesideTheMainConfigAndNeverInARepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PTRBOX_CONFIG", "")
	os.Unsetenv("PTRBOX_CONFIG")
	t.Setenv("PTRBOX_REPO_ROOT", filepath.Join(home, "code"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, vm := range []string{"demo", "my-api"} {
		path := config.VMConfigPath(vm)
		if dir := filepath.Dir(path); dir != config.VMDir() {
			t.Errorf("per-VM config for %q resolved outside the per-VM directory: %s", vm, path)
		}
		if !strings.HasPrefix(path, config.Dir()+string(filepath.Separator)) {
			t.Errorf("per-VM config for %q is not under the config directory: %s", vm, path)
		}
		// The repo root is where every mounted repo lives; nothing ptrbox
		// reads as configuration may come from under it.
		if strings.HasPrefix(path, cfg.RepoRoot+string(filepath.Separator)) {
			t.Errorf("per-VM config for %q is under the repo root: %s", vm, path)
		}
	}

	// And a name that would climb out is refused rather than resolved.
	for _, escape := range []string{"../config", "a/b", "../../etc/passwd"} {
		if _, err := cfg.Overlay(escape); err == nil {
			t.Errorf("Overlay(%q) was accepted", escape)
		}
	}
}

// The toolchain record is the same shape of contract as the package marker
// below: 30-toolchain.sh writes it, vm/verify.sh reads it, and nothing
// connects the two but the path. A typo on either side is invisible - the
// writer still writes, the reader still prints a verdict - and the result is
// a VM whose runtimes nobody checked.
func TestTheToolchainRecordIsSpelledTheSameInBothScripts(t *testing.T) {
	const record = ".ptrbox/toolchain"
	for _, name := range []string{"vm/provision/30-toolchain.sh", "vm/verify.sh"} {
		if !strings.Contains(asset(t, name), record) {
			t.Errorf("%s does not mention %s", name, record)
		}
	}
}

// The runtime list is fixed on the host and substituted in, exactly like the
// apt list. A list computed inside the guest - from a file, a command, or
// anything under the repo mount - would let the agent choose what its own
// sandbox contains on the next boot.
func TestTheToolchainListIsFixedAtRenderTime(t *testing.T) {
	rendered, _ := sandbox(t)
	mustMatch(t, rendered, `TOOLCHAIN="node uv"`, "the fixture's runtime list is not rendered as a literal")
	mustNotMatch(t, rendered, "TOOLCHAIN=.*(\\$\\(|`|<|cat |curl |workspace)",
		"the runtime list is computed inside the guest")
	mustNotMatch(t, rendered, "NODE_VERSION=.*(\\$\\(|`|<|cat |curl |workspace)",
		"the node version is computed inside the guest")
}

// Claude Code is not part of the configurable list: a sandbox without it is
// not a sandbox this project builds. The runtimes around it are optional; it
// is not, and it must not become optional by accident when someone extends
// config.Toolchains.
func TestClaudeCodeIsInstalledUnconditionally(t *testing.T) {
	script := asset(t, "vm/provision/30-toolchain.sh")
	install := regexp.MustCompile(`(?m)^curl [^\n]*claude\.ai[^\n]*$`)
	if !install.MatchString(script) {
		t.Error("the Claude Code install is not at the top level of 30-toolchain.sh " +
			"(indented means it sits inside a conditional)")
	}
	for _, tool := range config.Toolchains {
		if tool == "claude" {
			t.Error("claude is in config.Toolchains, which would make it optional")
		}
	}
	// And verify.sh asks for it whatever the list says.
	if !strings.Contains(asset(t, "vm/verify.sh"), "for tool in claude git") {
		t.Error("vm/verify.sh does not check claude unconditionally")
	}
}

func TestTheFailedPackageMarkerIsSpelledTheSameInBothScripts(t *testing.T) {
	// 15-extra-packages.sh writes the marker and vm/verify.sh reads it, and
	// nothing connects the two but the file name. A typo on either side is
	// invisible - the writer still fails the boot script, the reader still
	// prints OK - and puts the VM back to where an unavailable package is
	// found days later by whatever needed it.
	const marker = "extra-packages.failed"
	for _, name := range []string{"vm/provision/15-extra-packages.sh", "vm/verify.sh"} {
		if !strings.Contains(asset(t, name), marker) {
			t.Errorf("%s does not mention %s", name, marker)
		}
	}
	// Same directory, too: the marker is only useful where the reader looks.
	writer := regexp.MustCompile(`fail_marker="([^"]+)"`).FindStringSubmatch(
		asset(t, "vm/provision/15-extra-packages.sh"))
	reader := regexp.MustCompile(`\[ -e "([^"]+)" \]`).FindStringSubmatch(asset(t, "vm/verify.sh"))
	if writer == nil || reader == nil {
		t.Fatal("the marker is no longer written or read the way this test reads it")
	}
	if writer[1] != reader[1] {
		t.Errorf("15-extra-packages.sh writes %s, verify.sh reads %s", writer[1], reader[1])
	}
}

func TestTheExtraPackageCheckNeedsNoNetwork(t *testing.T) {
	// It re-runs on the reboot that raises the firewall, where an apt-get that
	// reaches for a mirror hangs until cloud-init gives up. --simulate and
	// dpkg-query both work off the lists already on disk; an `apt-get update`
	// or a download here would turn a wrong package name into a hung boot.
	script := asset(t, "vm/provision/15-extra-packages.sh")
	for _, forbidden := range []string{"apt-get update", "curl", "wget"} {
		if strings.Contains(stripComments(script), forbidden) {
			t.Errorf("15-extra-packages.sh runs %q, which the post-firewall re-run cannot do", forbidden)
		}
	}
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
	for _, want := range []string{"sudo -n true", "noproxy", "mount -t virtiofs",
		"extra-packages.failed", "exit 1"} {
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

func TestProbesDoNotDependOnASuperuserPATH(t *testing.T) {
	// Lima runs probes over ssh as the unprivileged default user, whose PATH
	// on Debian and Ubuntu excludes /usr/sbin. A probe that resolves an
	// sbin binary by name alone can never pass, and `limactl start` then
	// blocks for its whole timeout with the guest sitting there perfectly
	// healthy - a failure mode that looks exactly like a hang.
	//
	// Tools known to live on a plain user's PATH are fine by name; anything
	// else needs an absolute-path check alongside.
	onUserPath := map[string]bool{"git": true, "curl": true, "node": true, "python3": true}

	commandV := regexp.MustCompile(`command -v ([a-z0-9_.-]+)`)
	for _, tc := range []struct{ name, body string }{
		{"vm/claude-repo.yaml", rendertest.Sandbox(t)},
		{"vm/proxy.yaml", rendertest.Proxy(t)},
	} {
		probes := probeSection(tc.body)
		if probes == "" {
			t.Errorf("%s declares no probes", tc.name)
			continue
		}
		for _, match := range commandV.FindAllStringSubmatch(probes, -1) {
			tool := match[1]
			if onUserPath[tool] {
				continue
			}
			if !strings.Contains(probes, "/usr/sbin/"+tool) && !strings.Contains(probes, "/sbin/"+tool) {
				t.Errorf("%s probes for %q by name only; it is not on an unprivileged PATH, "+
					"so add an absolute-path check or limactl start will hang", tc.name, tool)
			}
		}
	}
}

// probeSection is the probes: block of a rendered config.
func probeSection(body string) string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "probes:") {
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

func TestEmbeddedGuestScriptsAreValidShell(t *testing.T) {
	// tests/lint.sh runs `bash -n` over the provision scripts, but a heredoc's
	// contents are opaque to it - a syntax error in a script that a provision
	// step writes out would surface only inside a VM, on first boot, as a
	// feature that silently does nothing.
	body := asset(t, "vm/provision/40-userenv.sh")
	for _, embedded := range []struct{ name, marker string }{
		{"statusline-command.sh", "STATUSLINE"},
	} {
		script := heredoc(t, body, embedded.marker)
		if script == "" {
			t.Errorf("no %s heredoc in 40-userenv.sh", embedded.marker)
			continue
		}
		path := filepath.Join(t.TempDir(), embedded.name)
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s is not valid shell: %v\n%s", embedded.name, err, out)
		}
	}
}

func TestTheStatuslineIsWiredIntoClaudeSettings(t *testing.T) {
	rendered, _ := sandbox(t)

	// A quoted heredoc, or the guest's provisioning shell would expand the
	// script's own $vars and $(...) as it wrote the file out.
	mustMatch(t, rendered, `<<'STATUSLINE'`, "the statusline heredoc is not quoted")
	// Rendered verbatim - no placeholder substitution has chewed it up.
	mustMatch(t, rendered, `part_hash="\\033\[1;34m#\\033\[0m"`,
		"the statusline script did not survive rendering intact")
	mustMatch(t, rendered, `chmod 755 "\$HOME/\.claude/statusline-command\.sh"`,
		"the statusline is not made executable")
	mustMatch(t, rendered, `"statusLine": \{"type": "command"`,
		"settings.json does not point at the statusline")

	// Lima's guest home carries a version-dependent suffix, so the path is
	// built from $HOME rather than written out.
	mustNotMatch(t, rendered, `command": "/home/`, "the guest home path is hardcoded")
}

func TestClaudeSettingsPreSeedIsValidJSON(t *testing.T) {
	rendered, _ := sandbox(t)

	// The pre-seed is one printf format string, which is the whole file: a
	// comma slip in it produces a settings.json Claude Code discards without
	// saying so, and the VM comes up with none of these defaults. Parse what
	// the guest will actually write.
	m := regexp.MustCompile(`(?m)^\s*printf '(\{.*\})\\n' \\$`).FindStringSubmatch(rendered)
	if m == nil {
		t.Fatal("no settings.json printf in the rendered config")
	}
	var settings struct {
		Model       string `json:"model"`
		Permissions struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
		StatusLine struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	// %s is $HOME, filled in by the guest's shell.
	body := strings.ReplaceAll(m[1], "%s", "/home/agent")
	if err := json.Unmarshal([]byte(body), &settings); err != nil {
		t.Fatalf("settings.json pre-seed is not valid JSON: %v\n%s", err, body)
	}
	if settings.Model == "" {
		t.Error("no model in the settings.json pre-seed")
	}
	// Approvals are a convenience layer in a ptrbox VM - the containment is
	// the VM, under any mode - so sessions start in Claude Code's own auto
	// mode rather than asking about every call.
	if got := settings.Permissions.DefaultMode; got != "auto" {
		t.Errorf("permission mode is %q, want auto", got)
	}
	if settings.StatusLine.Command != "/home/agent/.claude/statusline-command.sh" {
		t.Errorf("statusline command is %q", settings.StatusLine.Command)
	}
}

func TestOnlyPtrboxSpeaksAtLogin(t *testing.T) {
	rendered, _ := sandbox(t)

	// sshd's "Last login: ... from UNKNOWN" and Debian's motd are suppressed
	// by ~/.hushlogin, leaving the sandbox banner as the only line an ssh
	// login prints.
	mustMatch(t, rendered, `touch "\$HOME/\.hushlogin"`,
		"the stock login banner is not suppressed")
	// The banner itself is ours, printed from .bashrc, and unaffected by
	// hushlogin - if it went away the login would say nothing at all.
	mustMatch(t, rendered, `ptrbox sandbox - repo: /workspace`,
		"the sandbox banner is gone")
	// Interactive-only, still: hushlogin must not have moved it above the
	// stock .bashrc guard, where it would land in `bash -lc` output.
	mustMatch(t, rendered, `if shopt -q login_shell; then`,
		"the banner is no longer behind the login-shell guard")
}

// heredoc returns the body of a <<'MARKER' block, or "".
func heredoc(t *testing.T, body, marker string) string {
	t.Helper()
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		if in {
			if line == marker {
				return strings.Join(out, "\n") + "\n"
			}
			out = append(out, line)
			continue
		}
		if strings.HasSuffix(line, "<<'"+marker+"'") {
			in = true
		}
	}
	return ""
}
