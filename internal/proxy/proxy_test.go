package proxy_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/lima/limafake"
	"github.com/PtrMzk/ptrbox/internal/proxy"
	"github.com/PtrMzk/ptrbox/internal/ui"
)

type harness struct {
	*proxy.Proxy
	fake *limafake.Fake
	out  *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, key := range config.Keys {
		if _, ok := os.LookupEnv("PTRBOX_" + key); ok {
			t.Setenv("PTRBOX_"+key, "")
		}
		os.Unsetenv("PTRBOX_" + key)
	}
	t.Setenv("HOME", home)
	t.Setenv("PTRBOX_CONFIG", filepath.Join(tmp, "ptrbox.conf"))
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	fake := limafake.New()
	out := &bytes.Buffer{}
	return &harness{
		Proxy: &proxy.Proxy{
			Cfg:    cfg,
			Lima:   &lima.Client{Runner: fake, Stdout: io.Discard, Stderr: io.Discard},
			Assets: ptrbox.Assets,
			Out:    ui.Printer{W: out},
		},
		fake: fake,
		out:  out,
	}
}

func (h *harness) vmFile(t *testing.T, path string) string {
	t.Helper()
	body, ok := h.fake.ReadFile(config.ProxyVM, path)
	if !ok {
		t.Fatalf("the proxy VM has no %s", path)
	}
	return body
}

func (h *harness) mustEnsure(t *testing.T) bool {
	t.Helper()
	changed, err := h.Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return changed
}

func (h *harness) assertCalled(t *testing.T, pattern string) {
	t.Helper()
	if !h.fake.Called(pattern) {
		t.Errorf("expected a call matching %q; got:\n%s", pattern, h.fake.CallLog())
	}
}

func (h *harness) assertNotCalled(t *testing.T, pattern string) {
	t.Helper()
	if h.fake.Called(pattern) {
		t.Errorf("unexpected call matching %q; got:\n%s", pattern, h.fake.CallLog())
	}
}

// --- first run ---------------------------------------------------------------

func TestEnsureSeedsTheAllowlistAndProvisionsTheVM(t *testing.T) {
	h := newHarness(t)
	if !h.mustEnsure(t) {
		t.Error("Ensure reported nothing changed on a fresh host")
	}

	if _, err := os.Stat(config.AllowlistPath()); err != nil {
		t.Errorf("no host allowlist: %v", err)
	}
	h.assertCalled(t, `start --name ptrbox-proxy`)
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Errorf("proxy is %q", h.fake.VMStatus(config.ProxyVM))
	}
}

func TestEnsurePushesTheRenderedSquidConfig(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)

	conf := h.vmFile(t, proxy.ConfPath)
	if !strings.Contains(conf, "http_port 8888") {
		t.Errorf("pushed config has no http_port:\n%s", conf)
	}
	if !strings.Contains(conf, "/etc/squid/allowed_domains.txt") {
		t.Error("pushed config does not reference the allowlist")
	}
	if strings.Contains(conf, "__PROXY_PORT__") {
		t.Error("pushed config still has placeholders")
	}
}

func TestThePushedConfigListensOnEverySandboxSlot(t *testing.T) {
	// The listener set is static - base port plus all 16 sandbox slots,
	// allocated or not - because the listeners decide the lima forwards, and
	// changing those means restarting the proxy VM under every live sandbox.
	h := newHarness(t)
	h.mustEnsure(t)

	conf := h.vmFile(t, proxy.ConfPath)
	for port := 8888; port <= 8904; port++ {
		if !strings.Contains(conf, fmt.Sprintf("\nhttp_port %d\n", port)) {
			t.Errorf("the pushed config does not listen on port %d", port)
		}
	}
}

func TestThePushedConfigLogsTheArrivalPort(t *testing.T) {
	// Every client is 127.0.0.1, so the local port is the only field that says
	// which sandbox a log line - above all a TCP_DENIED - belongs to.
	h := newHarness(t)
	h.mustEnsure(t)

	conf := h.vmFile(t, proxy.ConfPath)
	if !strings.Contains(conf, "localport=%lp") {
		t.Error("the access log format no longer carries the arrival port")
	}
	if !strings.Contains(conf, "access_log daemon:/var/log/squid/access.log ptrbox") {
		t.Error("the access log does not use the ptrbox format")
	}
}

func TestThePushedConfigPinsSquidsMemory(t *testing.T) {
	// cache_mem left unset is squid's 256 MB default against a 512 MiB VM with
	// no swap. The OOM kill at the end of that is not squid's problem but every
	// running sandbox's - the proxy is their only route out. The number is a
	// judgement call and may change; leaving the decision to squid may not.
	h := newHarness(t)
	h.mustEnsure(t)

	conf := h.vmFile(t, proxy.ConfPath)
	pinned := false
	for _, line := range strings.Split(conf, "\n") {
		if strings.HasPrefix(line, "cache_mem ") {
			pinned = true
		}
	}
	if !pinned {
		t.Errorf("the pushed config does not pin cache_mem:\n%s", conf)
	}
}

func TestThePushedConfigTurnsOffWhatThisProxyDoesNotUse(t *testing.T) {
	// The proxy serves CONNECT tunnels to one loopback client; everything else
	// squid can do - caching, peer protocols, the raw-socket pinger, SNMP,
	// per-client stats, naming its version on the 403 page - is parsing
	// surface and memory in a 512 MiB VM with no swap. Pinned even where the
	// value is the compiled default, for the cache_mem reason: the default may
	// change, leaving the decision to squid may not.
	h := newHarness(t)
	h.mustEnsure(t)

	conf := h.vmFile(t, proxy.ConfPath)
	for _, directive := range []string{
		"cache deny all",
		"icp_port 0",
		"htcp_port 0",
		"digest_generation off",
		"pinger_enable off",
		"snmp_port 0",
		"client_db off",
		"httpd_suppress_version_string on",
	} {
		if !strings.Contains(conf, "\n"+directive+"\n") {
			t.Errorf("the pushed config no longer sets %q on a line of its own", directive)
		}
	}
}

func TestEnsurePushesTheHostAllowlist(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)

	host, err := os.ReadFile(config.AllowlistPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := h.vmFile(t, proxy.AllowlistPath); got != string(host) {
		t.Error("the VM allowlist differs from the host's")
	}
	if !strings.Contains(string(host), "api.anthropic.com") {
		t.Error("the seeded allowlist does not carry the Claude API")
	}
}

func TestEnsureWritesAProxyConfigWithNoMountsAndALoopbackForward(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)

	body, err := os.ReadFile(config.GeneratedConfig(config.ProxyVM))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\nmounts: []") {
		t.Error("the proxy VM config does not declare an empty mount list")
	}
	if !strings.Contains(string(body), `hostIP: "127.0.0.1"`) {
		t.Error("the proxy VM's port forward is not loopback-bound")
	}
}

func TestEnsureRestartsSquidAfterPushingANewConfig(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	h.assertCalled(t, `sudo systemctl restart squid`)
}

// --- idempotence -------------------------------------------------------------

func TestASecondEnsureChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	h.fake.Reset()

	if h.mustEnsure(t) {
		t.Error("Ensure reported a change on an already-configured host")
	}
	h.assertNotCalled(t, `limactl start|^start`)
	h.assertNotCalled(t, `systemctl restart`)
	h.assertNotCalled(t, `squid -k reconfigure`)
}

func TestEnsureRestartsAStoppedProxy(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	h.fake.SetStatus(config.ProxyVM, "Stopped")
	h.fake.Reset()

	if !h.mustEnsure(t) {
		t.Error("Ensure reported no change after starting a stopped proxy")
	}
	h.assertCalled(t, `^start ptrbox-proxy`)
	if h.fake.VMStatus(config.ProxyVM) != lima.StatusRunning {
		t.Error("the proxy did not come up")
	}
}

// --- validation --------------------------------------------------------------

func TestTheCandidateConfigIsValidatedBeforeItIsActivated(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)

	// -f names the candidate: the live path must never be the thing under test.
	h.assertCalled(t, `sudo squid -f /etc/squid/squid.conf.ptrbox-new -k parse`)
	if !h.fake.InOrder(`squid -f .* -k parse`, `sudo mv /etc/squid/squid.conf.ptrbox-new /etc/squid/squid.conf`) {
		t.Errorf("the candidate was moved into place before it was parsed:\n%s", h.fake.CallLog())
	}
	if !h.fake.InOrder(`sudo mv .*squid.conf`, `sudo systemctl restart squid`) {
		t.Errorf("squid was restarted before the config was in place:\n%s", h.fake.CallLog())
	}
}

func TestAConfigSquidRejectsIsNeverActivated(t *testing.T) {
	h := newHarness(t)
	h.fake.SquidParseFails = true

	_, err := h.Ensure()
	if err == nil || !strings.Contains(err.Error(), "squid rejected") {
		t.Fatalf("Ensure err = %v", err)
	}
	if _, ok := h.fake.ReadFile(config.ProxyVM, proxy.ConfPath); ok {
		t.Error("a rejected config was activated")
	}
	// And no half-pushed candidate left lying around in the VM.
	if _, ok := h.fake.ReadFile(config.ProxyVM, proxy.ConfPath+".ptrbox-new"); ok {
		t.Error("the rejected candidate was left in the VM")
	}
	h.assertNotCalled(t, `systemctl restart`)
}

func TestARejectedAllowlistRollsTheVMBack(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	previous := h.vmFile(t, proxy.AllowlistPath)

	if err := os.WriteFile(config.AllowlistPath(), []byte("bad domain entry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.fake.SquidParseFails = true
	h.fake.Reset()

	result, err := h.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if result != proxy.Rejected {
		t.Fatalf("Sync = %v, want Rejected", result)
	}
	if got := h.vmFile(t, proxy.AllowlistPath); got != previous {
		t.Errorf("the VM allowlist was not restored:\n%s", got)
	}
	h.assertNotCalled(t, `squid -k reconfigure`)
}

// --- the allowlist -----------------------------------------------------------

func TestAnExistingAllowlistIsNeverOverwritten(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	if err := os.WriteFile(config.AllowlistPath(), []byte("my.private.registry\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	seeded, err := h.SeedAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Error("SeedAllowlist overwrote an existing allowlist")
	}
	h.mustEnsure(t)
	// ...and the user's version is what reaches the proxy VM.
	if got := h.vmFile(t, proxy.AllowlistPath); got != "my.private.registry\n" {
		t.Errorf("the VM serves %q", got)
	}
}

func TestAnAllowlistOnlyChangeReloadsRatherThanRestarting(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)

	body, err := os.ReadFile(config.AllowlistPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.AllowlistPath(), append(body, []byte("my.private.registry\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	h.fake.Reset()

	result, err := h.Sync()
	if err != nil || result != proxy.Applied {
		t.Fatalf("Sync = %v, %v", result, err)
	}
	// A restart severs every live VM tunnel; reconfigure drops nothing.
	h.assertCalled(t, `sudo squid -k reconfigure`)
	h.assertNotCalled(t, `systemctl restart`)
	if !h.fake.InOrder(`sudo squid -k parse`, `sudo squid -k reconfigure`) {
		t.Errorf("the new allowlist was reloaded before it was parsed:\n%s", h.fake.CallLog())
	}
}

func TestSyncWithoutAnAllowlistPointsAtInstall(t *testing.T) {
	h := newHarness(t)
	h.fake.AddVM(config.ProxyVM, lima.StatusRunning)
	if _, err := h.Sync(); err == nil || !strings.Contains(err.Error(), "ptrbox install") {
		t.Errorf("Sync err = %v", err)
	}
}

// --- per-VM allowlists -------------------------------------------------------

func TestSyncSeedsAndPushesAnAllocatedVMsAllowlist(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	if _, err := proxy.AllocatePort("demo"); err != nil {
		t.Fatal(err)
	}
	h.fake.Reset()

	result, err := h.Sync()
	if err != nil || result != proxy.Applied {
		t.Fatalf("Sync = %v, %v", result, err)
	}

	// Host-side: the file exists and carries the template's capabilities.
	body, err := os.ReadFile(config.VMAllowlistPath("demo"))
	if err != nil {
		t.Fatalf("no per-VM allowlist was seeded: %v", err)
	}
	if !strings.Contains(string(body), "api.anthropic.com") {
		t.Error("the seeded list does not carry the Claude API")
	}

	// VM-side: the list and the rules that point at it.
	if got := h.vmFile(t, "/etc/squid/allowed.d/demo.txt"); got != string(body) {
		t.Error("the pushed per-VM list differs from the host's")
	}
	rules := h.vmFile(t, proxy.VMAccessPath)
	for _, want := range []string{
		"acl vm_demo_port localport 8889",
		`acl vm_demo_domains dstdomain "/etc/squid/allowed.d/demo.txt"`,
		"http_access allow from_forward CONNECT vm_demo_port vm_demo_domains",
		"http_access deny vm_demo_port",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("the pushed rules are missing %q:\n%s", want, rules)
		}
	}

	// Sandbox churn is an ACL-level change: reload, never the restart that
	// severs every live tunnel.
	h.assertCalled(t, `sudo squid -k reconfigure`)
	h.assertNotCalled(t, `systemctl restart`)
}

func TestAnExistingPerVMAllowlistIsUsedNotReseeded(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	if err := os.MkdirAll(config.VMAllowlistDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.VMAllowlistPath("demo"), []byte("only.example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.AllocatePort("demo"); err != nil {
		t.Fatal(err)
	}

	if _, err := h.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := h.vmFile(t, "/etc/squid/allowed.d/demo.txt"); got != "only.example.com\n" {
		t.Errorf("the VM serves %q, want the user's own list untouched", got)
	}
}

func TestADeletedPerVMAllowlistIsReseededNotAParseError(t *testing.T) {
	// The generated rules reference the file, so a mapped VM without one
	// would stop squid on its next restart - which is every sandbox's
	// network. Deleting the file is documented as the reset to template.
	h := newHarness(t)
	h.mustEnsure(t)
	if _, err := proxy.AllocatePort("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(config.VMAllowlistPath("demo")); err != nil {
		t.Fatal(err)
	}

	if _, err := h.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.VMAllowlistPath("demo")); err != nil {
		t.Errorf("the deleted list was not re-seeded: %v", err)
	}
}

func TestARejectedSyncRollsBackThePerVMFilesToo(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	if _, err := proxy.AllocatePort("demo"); err != nil {
		t.Fatal(err)
	}
	previousRules := h.vmFile(t, proxy.VMAccessPath)
	h.fake.SquidParseFails = true
	h.fake.Reset()

	result, err := h.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if result != proxy.Rejected {
		t.Fatalf("Sync = %v, want Rejected", result)
	}
	// The rules were restored and the file that did not exist before is gone
	// again: a later squid restart in the VM must meet the state we knew was
	// good.
	if got := h.vmFile(t, proxy.VMAccessPath); got != previousRules {
		t.Errorf("the per-VM rules were not restored:\n%s", got)
	}
	if _, ok := h.fake.ReadFile(config.ProxyVM, "/etc/squid/allowed.d/demo.txt"); ok {
		t.Error("a rejected per-VM list was left in the VM")
	}
	h.assertNotCalled(t, `squid -k reconfigure`)
}

func TestARemovedAllocationRetiresItsRules(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	if _, err := proxy.AllocatePort("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := proxy.ReleasePort("demo"); err != nil {
		t.Fatal(err)
	}

	if _, err := h.Sync(); err != nil {
		t.Fatal(err)
	}
	if rules := h.vmFile(t, proxy.VMAccessPath); strings.Contains(rules, "vm_demo") {
		t.Errorf("a removed VM's rules linger:\n%s", rules)
	}
	// The host-side list survives: it is what makes a re-create reproducible.
	if _, err := os.Stat(config.VMAllowlistPath("demo")); err != nil {
		t.Errorf("the host-side list did not survive the removal: %v", err)
	}
}

// --- stopping ----------------------------------------------------------------

// --- verification ------------------------------------------------------------

func TestVerifyRunsTheEgressAssertionsInsideTheProxyVM(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	if err := h.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	h.assertCalled(t, `shell ptrbox-proxy -- bash -lc`)
}

func TestVerifySendsTheScriptItselfNotAPathToIt(t *testing.T) {
	// The proxy VM has no mounts, so there is no file there to name: the
	// script is embedded in the binary and piped in, the way verify.sh is for
	// a sandbox.
	h := newHarness(t)
	h.mustEnsure(t)
	if err := h.Verify(); err != nil {
		t.Fatal(err)
	}
	sent := strings.Join(h.fake.Scripts, "\n")
	for _, want := range []string{"squid listening", "denied domain refused", "/dev/tcp/"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the script the VM ran has no %q check", want)
		}
	}
}

func TestAFailedAssertionIsAFailedVerification(t *testing.T) {
	// The whole point of the exercise: a proxy whose config parses but whose
	// squid is not carrying traffic must not read as healthy.
	h := newHarness(t)
	h.mustEnsure(t)
	h.fake.ProxyVerifyFails = true

	err := h.Verify()
	if err == nil {
		t.Fatal("Verify passed a proxy that is not serving egress")
	}
	if !strings.Contains(err.Error(), "no network") {
		t.Errorf("the error does not say what it costs the user: %v", err)
	}
}

func TestEnsureDoesNotVerify(t *testing.T) {
	// Kept separate on purpose. Ensure runs on every `new`, `start` and `rm`,
	// where a live sandbox's own verify.sh already exercises the egress path
	// end to end; paying for a second round trip there would slow the common
	// command to re-prove what the next step proves anyway.
	h := newHarness(t)
	h.fake.ProxyVerifyFails = true
	if _, err := h.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h.assertNotCalled(t, `bash -lc`)
}

func TestStopIfIdleStopsTheProxyWhenNoSandboxIsRunning(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	// A sandbox that exists but is stopped needs no proxy.
	writeGenerated(t, "demo")
	h.fake.AddVM("demo", "Stopped")
	h.fake.Reset()

	if err := h.StopIfIdle(); err != nil {
		t.Fatal(err)
	}
	h.assertCalled(t, `^stop ptrbox-proxy`)
	// Stopped, not deleted: the next start must not pay provisioning again.
	if h.fake.VMStatus(config.ProxyVM) != "Stopped" {
		t.Errorf("proxy is %q", h.fake.VMStatus(config.ProxyVM))
	}
	h.assertNotCalled(t, `delete .*ptrbox-proxy`)
}

func TestStopIfIdleLeavesTheProxyUnderARunningSandbox(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	writeGenerated(t, "demo")
	h.fake.AddVM("demo", lima.StatusRunning)
	h.fake.Reset()

	if err := h.StopIfIdle(); err != nil {
		t.Fatal(err)
	}
	h.assertNotCalled(t, `stop ptrbox-proxy`)
}

func TestAForeignLimaVMDoesNotHoldTheProxyUp(t *testing.T) {
	// Only VMs with a rendered config in the generated dir count as
	// sandboxes; a hand-made lima VM neither holds the proxy up nor gets torn
	// down.
	h := newHarness(t)
	h.mustEnsure(t)
	h.fake.AddVM("somebody-elses-vm", lima.StatusRunning)
	h.fake.Reset()

	if err := h.StopIfIdle(); err != nil {
		t.Fatal(err)
	}
	h.assertCalled(t, `^stop ptrbox-proxy`)
}

func TestStopIfIdleLeavesTheProxyUpWhenTheListingFails(t *testing.T) {
	// Uncertainty errs toward lingering: stopping the proxy under a live
	// sandbox bricks the agent's network, lingering costs idle RAM.
	h := newHarness(t)
	h.mustEnsure(t)
	h.fake.Reset()
	h.fake.ListFails = true

	if err := h.StopIfIdle(); err != nil {
		t.Fatal(err)
	}
	h.assertNotCalled(t, `stop ptrbox-proxy`)
}

func TestStopIfIdleDoesNothingWhenTheProxyIsAlreadyDown(t *testing.T) {
	h := newHarness(t)
	h.mustEnsure(t)
	h.fake.SetStatus(config.ProxyVM, "Stopped")
	h.fake.Reset()

	if err := h.StopIfIdle(); err != nil {
		t.Fatal(err)
	}
	h.assertNotCalled(t, `stop`)
}

func writeGenerated(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(config.GeneratedDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GeneratedConfig(name), []byte("vmType: vz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
