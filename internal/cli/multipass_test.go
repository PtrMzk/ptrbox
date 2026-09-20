package cli

// The lifecycle on a simulated Windows PC: the same commands against the
// Multipass fake, with Windows paths, the Credential Manager and Ubuntu as
// the default. What these prove is the wiring - that every step of `new`,
// `start`, `shell` and `rm` reaches the guest through this backend's
// spellings - not the guest, which is the same guestfake as lima's.

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/backend"
	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/multipass"
	"github.com/PtrMzk/ptrbox/internal/multipass/multipassfake"
)

// newMultipassHarness is newHarness on a PC: Windows paths under the temp
// home, the Credential Manager, multipass as the backend, and the distro
// default that OS has. Everything else - the config file, the repo root,
// the fake keychain's token - is the same harness.
func newMultipassHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	realHost, realDistro, realOS := config.Host, config.DefaultDistro, hostOS
	config.Host, config.DefaultDistro, hostOS = config.Windows, "ubuntu2404", windowsHost
	t.Cleanup(func() { config.Host, config.DefaultDistro, hostOS = realHost, realDistro, realOS })
	t.Setenv("USERPROFILE", h.home)
	t.Setenv("APPDATA", filepath.Join(h.home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h.home, "AppData", "Local"))

	h.mp = multipassfake.New()
	h.keychain.like = CredentialManager{}
	// The PC holds the switch address: the second PowerShell line was run.
	realAddrs := multipass.InterfaceAddrs
	multipass.InterfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP(multipass.HostAddr), Mask: net.CIDRMask(24, 32)}}, nil
	}
	t.Cleanup(func() { multipass.InterfaceAddrs = realAddrs })
	// Until the DirectAddress egress check exists, the proxy's reachability
	// is answered the lima way: by the proxy VM being up.
	h.portInUse = func(int) bool { return h.mp.VMStatus(config.ProxyVM) == backend.StatusRunning }
	return h
}

// --- install's preflight -----------------------------------------------------

func TestOnThePCInstallStopsBeforeAnyVMWhenTheSwitchIsMissing(t *testing.T) {
	h := newMultipassHarness(t)
	h.mp.NetworkMissing = true
	err := h.run("install")
	if err == nil || !strings.Contains(err.Error(), "New-VMSwitch -Name ptrbox -SwitchType Internal") ||
		!strings.Contains(err.Error(), "New-NetIPAddress") {
		t.Errorf("err = %v, want the two PowerShell lines", err)
	}
	if h.mp.Called("launch") {
		t.Errorf("a VM was launched on a PC with no switch:\n%s", h.mp.CallLog())
	}
}

func TestOnThePCInstallStopsBeforeAnyVMWhenMountsAreDisabled(t *testing.T) {
	h := newMultipassHarness(t)
	h.mp.MountsDisabled = true
	err := h.run("install")
	if err == nil || !strings.Contains(err.Error(), "multipass set local.privileged-mounts=true") {
		t.Errorf("err = %v, want the setting to turn on", err)
	}
	// Said here, with no VM to be saved and resumed by the daemon restart
	// that setting causes.
	if h.mp.Called("launch") {
		t.Errorf("a VM was launched before mounts were on:\n%s", h.mp.CallLog())
	}
}

func TestOnThePCAReadyHostInstallsTheProxy(t *testing.T) {
	h := newMultipassHarness(t)
	h.mustRun("install")
	if !h.mp.InOrder("networks --format json", "launch --name ptrbox-proxy") ||
		!h.mp.InOrder("get local.privileged-mounts", "launch --name ptrbox-proxy") {
		t.Errorf("the preflight did not come before the launch:\n%s", h.mp.CallLog())
	}
}

func TestOnThePCNewLaunchesRebootsVerifiesAndInjectsThroughTransfer(t *testing.T) {
	h := newMultipassHarness(t)
	h.keychain.token = "sk-ant-oat-EXAMPLE"
	h.mustRun("new", "demo")

	log := h.mp.CallLog()
	for _, want := range []string{
		// The proxy first, without a mount; then the sandbox with its mount
		// and the alias, on the switch.
		`launch --name ptrbox-proxy --cpus 1 --memory 512M --disk 4G --cloud-init <script:\d+> --network name=ptrbox,mode=manual --timeout 1200 24\.04`,
		// (The mount argument is a long path, which the log files out of line.)
		`launch --name demo --cpus \d+ --memory \d+G --disk \d+G --cloud-init <script:\d+> --network name=ptrbox,mode=manual --timeout 1200 --mount <script:\d+> 24\.04`,
		// Every launch and start is followed by cloud-init, then the mount.
		`exec demo --no-map-working-directory -- cloud-init status --wait`,
		`exec demo --no-map-working-directory -- cat /proc/mounts`,
		// The reboot that raises the wall.
		`stop demo`, `start demo`,
		// Verification as the agent, through the daemon user's sudo.
		`exec demo --no-map-working-directory -- sudo -n -u agent -H bash -lc <script:\d+>`,
		// The token: transferred into the daemon user's private directory,
		// then redirected into the agent's command.
		`transfer - demo:/home/ubuntu/\.ptrbox/stdin-[0-9a-f]+`,
		`exec demo --no-map-working-directory -- sh -c <script:\d+> sh /home/ubuntu/\.ptrbox/stdin-[0-9a-f]+ bash -c <script:\d+>`,
	} {
		if !regexp.MustCompile(want).MatchString(log) {
			t.Errorf("no call matching %q in:\n%s", want, log)
		}
	}
	if stdins := strings.Join(h.mp.Stdins, ""); !strings.Contains(stdins, `CLAUDE_CODE_OAUTH_TOKEN="sk-ant-oat-EXAMPLE"`) {
		t.Errorf("the token did not reach the guest command's stdin: %q", stdins)
	}
	if strings.Contains(log, "sk-ant-oat") {
		t.Error("the token appeared on a multipass argv")
	}
	// Windows paths: the rendered config under %LOCALAPPDATA%, no ssh link.
	if !h.exists(filepath.Join(h.home, "AppData", "Local", "ptrbox", "generated", "demo.yaml")) {
		t.Error("the rendered config is not under LOCALAPPDATA")
	}
	if entries, _ := os.ReadDir(filepath.Join(h.home, ".ssh")); len(entries) != 0 {
		t.Errorf("ssh config written on a backend with no ssh: %v", entries)
	}
	h.assertOutputContains("ptrbox shell demo")
}

func TestOnThePCTheRenderedConfigIsCloudInitWithTheSandboxsSlotAddress(t *testing.T) {
	h := newMultipassHarness(t)
	h.mustRun("new", "demo")
	body, err := os.ReadFile(filepath.Join(h.home, "AppData", "Local", "ptrbox", "generated", "demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "#cloud-config\n") {
		t.Error("the sandbox config is not cloud-init user-data")
	}
	// The first allocation is the first slot.
	if !strings.Contains(string(body), "addresses: [172.31.255.16/24]") {
		t.Error("the first sandbox is not at the first slot address")
	}
	if !strings.Contains(string(body), `DAEMON_USER="ubuntu"`) || !strings.Contains(string(body), "ip daddr 172.31.255.2 tcp dport 8889 accept") {
		t.Error("the config was not rendered with the Multipass facts")
	}
}

func TestOnThePCALaunchThatSkippedTheMountIsAFailedNew(t *testing.T) {
	h := newMultipassHarness(t)
	h.mp.MountsDisabled = true
	err := h.run("new", "demo")
	if err == nil || !strings.Contains(err.Error(), "privileged-mounts") {
		t.Errorf("err = %v, want the mount check naming the setting", err)
	}
	if len(h.mp.Stdins) != 0 {
		t.Error("a token was sent to a sandbox with no repo in it")
	}
}

func TestOnThePCADistroMultipassHasNoImageForIsRefused(t *testing.T) {
	h := newMultipassHarness(t)
	t.Setenv("PTRBOX_DISTRO", "debian13")
	err := h.run("new", "demo")
	if err == nil || !strings.Contains(err.Error(), "debian13") {
		t.Errorf("err = %v", err)
	}
}

func TestOnThePCStartRepairsAMountAHostRebootLost(t *testing.T) {
	h := newMultipassHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("stop", "demo")
	// The PC rebooted: the daemon brought the VM back with the mount on
	// record and absent from the guest.
	h.mp.SetStatus("demo", backend.StatusRunning)
	h.mp.Unmounted["demo"] = true
	h.mp.Reset()

	h.mustRun("start", "demo")
	if !h.mp.InOrder("cat /proc/mounts", "restart demo") {
		t.Errorf("the missing mount was not repaired with a restart:\n%s", h.mp.CallLog())
	}
}

func TestOnThePCShellIsTheAgentInTheWorkspace(t *testing.T) {
	h := newMultipassHarness(t)
	h.mustRun("new", "demo")
	h.mustRun("shell", "demo")
	if len(h.mp.Sessions) != 1 || h.mp.Sessions[0].VM != "demo" || h.mp.Sessions[0].Workdir != "/workspace" {
		t.Errorf("sessions = %+v", h.mp.Sessions)
	}
	if !h.mp.Called(`exec demo -d /workspace -- sudo -n -u agent -H bash -l`) {
		t.Errorf("the shell is not the agent's login shell:\n%s", h.mp.CallLog())
	}
}

func TestOnThePCRmPurgesAndStopsTheProxyWhenLast(t *testing.T) {
	h := newMultipassHarness(t)
	h.mustRun("new", "demo")
	h.mp.Reset()
	h.mustRun("rm", "demo")
	if !h.mp.InOrder("delete --purge demo", "stop ptrbox-proxy") {
		t.Errorf("want the sandbox purged and then the idle proxy stopped:\n%s", h.mp.CallLog())
	}
	if h.exists(filepath.Join(h.home, "AppData", "Local", "ptrbox", "generated", "demo.yaml")) {
		t.Error("the generated config survived rm")
	}
}
