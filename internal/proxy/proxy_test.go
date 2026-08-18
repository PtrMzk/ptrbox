package proxy_test

import (
	"bytes"
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
