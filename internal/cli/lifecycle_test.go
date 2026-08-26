package cli

// rm/start/stop, and the proxy VM's lifecycle coupling: any sandbox coming up
// starts the proxy, the last one going away stops it - and every ambiguity
// errs toward the proxy LINGERING, because a proxy that is down under a live
// sandbox bricks the agent's network while a lingering one only costs idle
// RAM.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/proxy"
)

// --- rm ----------------------------------------------------------------------

func TestRmDeletesTheVMAndItsArtifacts(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("rm", "demo")

	h.assertCalled("delete -f demo")
	if h.exists(config.GeneratedConfig("demo")) {
		t.Error("the generated config survived rm")
	}
	if h.exists(config.SSHConfigLink("demo")) {
		t.Error("the ssh config link survived rm")
	}
}

func TestRmNeverTouchesTheRepoOnTheHost(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	work := filepath.Join(h.repos, "demo", "file.txt")
	write(t, work, "work\n")

	h.mustRun("rm", "demo")
	if !h.exists(work) {
		t.Error("rm deleted work in the repo")
	}
	if !h.exists(filepath.Join(h.repos, "demo", ".git")) {
		t.Error("rm deleted the repo's git dir")
	}
}

func TestRmAcceptsARepoPathAsWellAsAVMName(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("rm", filepath.Join(h.repos, "demo"))
	h.assertCalled("delete -f demo")
}

func TestRmRefusesToGuessWhenTheVMDoesNotExist(t *testing.T) {
	h := newHarness(t)
	err := h.run("rm", "nosuchvm")
	if err == nil || !strings.Contains(err.Error(), "no VM named") {
		t.Errorf("err = %v", err)
	}
	h.assertNotCalled("delete")
}

func TestRmRequiresAnArgument(t *testing.T) {
	h := newHarness(t)
	if err := h.run("rm"); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("err = %v", err)
	}
}

func TestARepoCanBeReSandboxedAfterTeardown(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("rm", "demo")
	h.mustRun("new", "demo")
	h.generated("demo")
}

// --- new brings the proxy up -------------------------------------------------

func TestNewProvisionsTheProxyBeforeTheSandbox(t *testing.T) {
	// Order matters: from the post-provision reboot on, the proxy is the
	// sandbox's only way out.
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.assertOrder("start --name ptrbox-proxy", "start --name demo")
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Error("the proxy is not running")
	}
}

func TestNewSyncsTheSquidConfigIntoAProxyItJustCreated(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	if !strings.Contains(h.proxyFile("/etc/squid/squid.conf"), "http_port 8888") {
		t.Error("the pushed squid config has no http_port")
	}
	if !strings.Contains(h.proxyFile("/etc/squid/allowed_domains.txt"), "api.anthropic.com") {
		t.Error("the pushed allowlist has no Claude API entry")
	}
}

func TestNewWithTheProxyAlreadyRunningDoesNotRestartIt(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	h.fake.Reset()
	h.mustRun("new", "demo")
	h.assertNotCalled("start --name ptrbox-proxy")
	h.assertNotCalled("^start ptrbox-proxy")
}

func TestNewRestartsAStoppedProxy(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	h.fake.SetStatus(config.ProxyVM, "Stopped")
	h.mustRun("new", "demo")
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Error("the proxy did not come back up")
	}
}

func TestTheSandboxTemplateStillPointsAtTheHostSideProxyAddress(t *testing.T) {
	// Same gateway address as ever; the port is the VM's own allocation since
	// item 37, and the first sandbox gets the first slot above the base port.
	// Env var and firewall rule must name the same destination.
	h := newHarness(t)
	h.mustRun("new", "demo")
	body := h.generated("demo")
	if !strings.Contains(body, `HTTPS_PROXY="http://192.168.5.2:8889"`) {
		t.Error("the guest proxy env var does not point at the VM's allocated port")
	}
	if !strings.Contains(body, "ip daddr 192.168.5.2 tcp dport 8889 accept") {
		t.Error("the firewall rule does not name the VM's allocated port")
	}
}

func TestEachSandboxDialsItsOwnProxyPort(t *testing.T) {
	// The port is the VM's identity at squid - two sandboxes sharing one
	// would share an allowlist once item 38 lands.
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("new", "other")
	if !strings.Contains(h.generated("demo"), "tcp dport 8889 accept") {
		t.Error("the first sandbox does not dial the first slot")
	}
	if !strings.Contains(h.generated("other"), "tcp dport 8890 accept") {
		t.Error("the second sandbox does not dial the second slot")
	}
}

func TestRmFreesTheProxyPortForTheNextSandbox(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("new", "other")
	h.mustRun("rm", "--no-archive", "demo")
	if h.exists(proxy.PortFile("demo")) {
		t.Error("rm left the port sidecar behind")
	}

	h.mustRun("new", "third")
	if !strings.Contains(h.generated("third"), "tcp dport 8889 accept") {
		t.Error("the freed slot was not reused")
	}
}

func TestNewSeedsThePerVMAllowlistAndPointsSquidAtIt(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")

	body, err := os.ReadFile(config.VMAllowlistPath("demo"))
	if err != nil {
		t.Fatalf("new did not seed the VM's allowlist: %v", err)
	}
	if !strings.Contains(string(body), "api.anthropic.com") {
		t.Error("the seeded list does not carry the Claude API")
	}

	rules := h.proxyFile(proxy.VMAccessPath)
	if !strings.Contains(rules, "acl vm_demo_port localport 8889") {
		t.Errorf("squid's rules do not key on the VM's port:\n%s", rules)
	}
	if h.proxyFile("/etc/squid/allowed.d/demo.txt") != string(body) {
		t.Error("the pushed list differs from the host's")
	}
	h.assertOutputContains("egress   proxy port 8889")
}

func TestAPreDeclaredAllowlistIsUsedInsteadOfTheTemplate(t *testing.T) {
	// The file outliving (and predating) the VM is what makes declaring
	// egress before the create - and reproducing it after an rm - work.
	h := newHarness(t)
	mkdir(t, config.VMAllowlistDir())
	if err := os.WriteFile(config.VMAllowlistPath("demo"), []byte("api.anthropic.com\nonly.example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.mustRun("new", "demo")
	if got := h.proxyFile("/etc/squid/allowed.d/demo.txt"); got != "api.anthropic.com\nonly.example.com\n" {
		t.Errorf("the VM serves %q, want the pre-declared list", got)
	}
}

func TestRmKeepsTheHostAllowlistButRetiresTheSquidRules(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("new", "other") // keeps the proxy running through the rm
	h.mustRun("rm", "demo")

	if _, err := os.Stat(config.VMAllowlistPath("demo")); err != nil {
		t.Errorf("rm removed the host-side allowlist: %v", err)
	}
	rules := h.proxyFile(proxy.VMAccessPath)
	if strings.Contains(rules, "vm_demo") {
		t.Errorf("the removed VM's rules linger at the proxy:\n%s", rules)
	}
	if !strings.Contains(rules, "vm_other") {
		t.Errorf("the surviving VM's rules were lost:\n%s", rules)
	}
}

func TestSandboxChurnReloadsSquidWithoutRestartingIt(t *testing.T) {
	// The config file is static; a VM coming or going changes only the
	// generated include and the list files. A restart here would sever every
	// other sandbox's live tunnels, Claude's requests included.
	h := newHarness(t)
	h.mustRun("install")
	h.fake.Reset()

	h.mustRun("new", "demo")
	h.assertCalled(`sudo squid -k reconfigure`)
	h.assertNotCalled(`systemctl restart`)
}

// --- rm stops the proxy with the last sandbox --------------------------------

func TestRmOfTheLastSandboxStopsTheProxyButKeepsItAround(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("rm", "demo")

	h.assertCalled("stop ptrbox-proxy")
	// Stopped, not deleted: the next start must not pay provisioning again.
	if h.fake.VMStatus(config.ProxyVM) != "Stopped" {
		t.Errorf("the proxy is %q", h.fake.VMStatus(config.ProxyVM))
	}
	h.assertNotCalled("delete .*ptrbox-proxy")
}

func TestRmWithAnotherSandboxRunningLeavesTheProxyAlone(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("new", "other")
	h.fake.Reset()
	h.mustRun("rm", "demo")

	h.assertNotCalled("stop ptrbox-proxy")
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Error("the proxy went down under a live sandbox")
	}
}

func TestAStoppedSandboxDoesNotKeepTheProxyUp(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("new", "other")
	h.mustRun("stop", "other")
	h.fake.Reset()
	h.mustRun("rm", "demo")
	// demo was the last RUNNING sandbox; stopped ones need no proxy.
	h.assertCalled("stop ptrbox-proxy")
}

func TestRmRefusesToResolveTheProxyVM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	err := h.run("rm", "ptrbox-proxy")
	if err == nil || !strings.Contains(err.Error(), "not a sandbox") {
		t.Fatalf("err = %v", err)
	}
	h.assertNotCalled("delete")
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Error("the proxy was disturbed")
	}
}

// --- start / stop ------------------------------------------------------------

func TestStartBringsTheProxyUpBeforeTheSandbox(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("stop", "demo") // the proxy stops too: demo was the last one
	h.fake.Reset()

	h.mustRun("start", "demo")
	h.assertOrder("^start ptrbox-proxy", "^start demo")
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning || h.fake.VMStatus("demo") != lima.StatusRunning {
		t.Error("something did not come up")
	}
}

func TestStartPushesAllowlistEditsMadeWhileTheProxyWasDown(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("stop", "demo")
	h.mustRun("allow", "demo", "deferred.example.com") // saved host-side only
	if containsLine(h.proxyFile("/etc/squid/allowed.d/demo.txt"), "deferred.example.com") {
		t.Fatal("the edit reached a stopped proxy")
	}
	h.mustRun("start", "demo")
	if !containsLine(h.proxyFile("/etc/squid/allowed.d/demo.txt"), "deferred.example.com") {
		t.Error("the deferred edit was not pushed on start")
	}
}

func TestStartOfAnAlreadyRunningSandboxSaysSo(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("start", "demo")
	h.assertOutputContains("already running")
}

func TestStopOfTheLastSandboxStopsTheProxy(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.fake.Reset()
	h.mustRun("stop", "demo")
	h.assertCalled("stop demo")
	h.assertCalled("stop ptrbox-proxy")
}

func TestStopWithAnotherSandboxRunningLeavesTheProxyUp(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("new", "other")
	h.fake.Reset()
	h.mustRun("stop", "demo")
	h.assertNotCalled("stop ptrbox-proxy")
}

func TestStartAndStopRefuseTheProxyVMName(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	if err := h.run("start", "ptrbox-proxy"); err == nil {
		t.Error("start accepted the proxy VM")
	}
	if err := h.run("stop", "ptrbox-proxy"); err == nil {
		t.Error("stop accepted the proxy VM")
	}
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Error("the proxy was disturbed")
	}
}

func TestStartOfAnUnknownVMPointsAtNew(t *testing.T) {
	h := newHarness(t)
	err := h.run("start", "nosuchvm")
	if err == nil || !strings.Contains(err.Error(), "ptrbox new") {
		t.Errorf("err = %v", err)
	}
}

func TestStartAndStopRequireAnArgument(t *testing.T) {
	h := newHarness(t)
	for _, verb := range []string{"start", "stop"} {
		if err := h.run(verb); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("%s err = %v", verb, err)
		}
	}
}

func TestStopOfAnAlreadyStoppedSandboxStillReapsAnIdleProxy(t *testing.T) {
	// A proxy left over from a crash gets cleaned up on the next explicit
	// stop rather than lingering forever (there is deliberately no janitor).
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.fake.SetStatus("demo", "Stopped")
	h.fake.Reset()

	h.mustRun("stop", "demo")
	h.assertCalled("stop ptrbox-proxy")
}

// --- erring toward lingering -------------------------------------------------

func TestAForeignLimaVMDoesNotCount(t *testing.T) {
	// Only VMs with a rendered config in the generated dir count as
	// sandboxes; a hand-made lima VM neither holds the proxy up nor gets torn
	// down.
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.fake.AddVM("somebody-elses-vm", lima.StatusRunning)
	h.fake.Reset()
	h.mustRun("rm", "demo")
	h.assertCalled("stop ptrbox-proxy")
}

func TestTheProxySyncIsRolledBackWhenSquidRejectsTheConfig(t *testing.T) {
	h := newHarness(t)
	h.mustRun("install")
	if err := os.WriteFile(config.AllowlistPath(), []byte("bad domain entry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.fake.SquidParseFails = true

	err := h.run("new", "demo")
	if err == nil || !strings.Contains(err.Error(), "squid rejected") {
		t.Fatalf("err = %v", err)
	}
	// The VM still serves what it served before the bad push.
	served := h.proxyFile("/etc/squid/allowed_domains.txt")
	if !strings.Contains(served, "api.anthropic.com") {
		t.Error("the proxy lost its previous allowlist")
	}
	if strings.Contains(served, "bad domain entry") {
		t.Error("the rejected allowlist stayed in the VM")
	}
}
