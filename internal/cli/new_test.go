package cli

// Full lifecycle against fakes: create a VM, verify it, tear it down - with no
// Lima, no Squid, no Keychain and no Mac anywhere in sight.
//
// These are the tests that stand in for "did provisioning work", so they
// assert the things a human would otherwise have to eyeball: the order of
// operations, what got written where, that credentials travel on stdin, and
// that teardown removes the VM without touching the repo.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
)

func TestNewCreatesTheRepoDirectoryAndGitInitsIt(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	if !h.exists(filepath.Join(h.repos, "demo", ".git")) {
		t.Error("no git repo at the repo path")
	}
}

func TestNewNeutralisesGitHooksOnTheHostClone(t *testing.T) {
	// Agent-written hooks execute on the HOST when you run git there.
	h := newHarness(t)
	h.mustRun("new", "demo")
	if got := gitConfig(t, filepath.Join(h.repos, "demo"), "core.hooksPath"); got != "/dev/null" {
		t.Errorf("core.hooksPath = %q", got)
	}
}

func TestNewWritesAGeneratedConfigAndValidatesItBeforeStarting(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.generated("demo") // fails the test if it is not there
	h.assertOrder("validate", "start --name demo")
}

func TestTheGeneratedConfigCarriesTheRepoPathAndNoPlaceholders(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	body := h.generated("demo")
	if !strings.Contains(body, `location: "`+filepath.Join(h.repos, "demo")+`"`) {
		t.Error("the mount does not point at this repo")
	}
	if matches(body, `__[A-Z][A-Z0-9_]*__`) {
		t.Error("the generated config still has placeholders")
	}
}

func TestNewBuildsADebianVMByDefault(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	if !matches(h.generated("demo"), `location: "https://cloud\.debian\.org/.*debian-13`) {
		t.Error("not a Debian image")
	}
}

func TestDistroUbuntuBuildsAnUbuntuVM(t *testing.T) {
	h := newHarness(t)
	t.Setenv("PTRBOX_DISTRO", "ubuntu2404")
	h.mustRun("new", "demo")
	body := h.generated("demo")
	if !matches(body, `location: "https://cloud-images\.ubuntu\.com/.*24\.04`) {
		t.Error("not an Ubuntu image")
	}
	// Same provisioning either way: both distros are apt-based with identical
	// package names.
	if !strings.Contains(body, "apt-get install -y curl git build-essential") {
		t.Error("the base package install changed with the distro")
	}
}

func TestExtraPackagesLandInTheGeneratedConfigAsALiteral(t *testing.T) {
	h := newHarness(t)
	t.Setenv("PTRBOX_EXTRA_PACKAGES", "ripgrep sqlite3")
	h.mustRun("new", "demo")
	if !strings.Contains(h.generated("demo"), `EXTRA_PACKAGES="ripgrep sqlite3"`) {
		t.Error("the package list is not in the generated config")
	}
}

func TestNoExtraPackagesRendersAnEmptyInertInstallStep(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	if !strings.Contains(h.generated("demo"), `EXTRA_PACKAGES=""`) {
		t.Error("the empty package list is not rendered as an empty literal")
	}
}

// --- per-VM overrides --------------------------------------------------------

// The end-to-end version of the layering: a per-VM file reaches the guest.
// internal/config proves the precedence rules; this proves `new` consults
// them at all, and at a moment early enough to matter.
func TestAPerVMConfigFileReachesTheGeneratedConfig(t *testing.T) {
	h := newHarness(t)
	h.writeVMConfig("demo", "PTRBOX_EXTRA_PACKAGES=\"texlive-latex-recommended latexmk\"\n")
	h.mustRun("new", "demo")

	if !strings.Contains(h.generated("demo"), `EXTRA_PACKAGES="texlive-latex-recommended latexmk"`) {
		t.Error("the per-VM package list is not in the generated config")
	}
	// The file is named in the summary: editing it later changes nothing
	// until a re-create, so create time is when to say which one was read.
	if !strings.Contains(h.stderr, config.VMConfigPath("demo")) {
		t.Errorf("the summary does not name the per-VM file:\n%s", h.stderr)
	}
}

// One sandbox's file must not reach another's.
func TestAPerVMConfigFileAppliesOnlyToItsOwnVM(t *testing.T) {
	h := newHarness(t)
	h.writeVMConfig("demo", "PTRBOX_EXTRA_PACKAGES=latexmk\n")
	h.mustRun("new", "other")

	if strings.Contains(h.generated("other"), "latexmk") {
		t.Error("another VM's per-VM config leaked into this one")
	}
	if !strings.Contains(h.generated("other"), `EXTRA_PACKAGES=""`) {
		t.Error("the VM without a per-VM file should have no extra packages")
	}
}

// A per-VM distro has to reach the image URL, not just the label - the
// derivation is re-run rather than the field patched.
func TestAPerVMDistroChangesTheImageAndTheNarration(t *testing.T) {
	h := newHarness(t)
	h.writeVMConfig("demo", "PTRBOX_DISTRO=ubuntu2404\n")
	h.mustRun("new", "demo")

	body := h.generated("demo")
	if !strings.Contains(body, "ubuntu-24.04-server-cloudimg-arm64.img") {
		t.Error("the generated config does not use the ubuntu image")
	}
	if strings.Contains(body, "debian-13-genericcloud") {
		t.Error("the generated config still names the debian image")
	}
	if h.narrator.Image != "ubuntu2404" {
		t.Errorf("narrator.Image = %q, want the per-VM distro", h.narrator.Image)
	}
}

// Refused, not ignored, and refused before any VM state is touched.
func TestAHostGlobalKeyInAPerVMFileFailsNewBeforeAnyVMIsTouched(t *testing.T) {
	h := newHarness(t)
	h.writeVMConfig("demo", "PTRBOX_PROXY_PORT=9999\n")

	err := h.run("new", "demo")
	if err == nil {
		t.Fatal("new accepted a host-wide setting in a per-VM file")
	}
	if !strings.Contains(err.Error(), "PROXY_PORT") {
		t.Errorf("error = %v, want it to name the offending key", err)
	}
	h.assertNotCalled("start")
}

// An invalid per-VM value is caught with the same force as an invalid one in
// the main config: before the several minutes of provisioning, not after.
func TestAnInvalidPerVMPackageAbortsNewBeforeAnyVMIsTouched(t *testing.T) {
	h := newHarness(t)
	h.writeVMConfig("demo", "PTRBOX_EXTRA_PACKAGES=\"latexmk; reboot\"\n")

	if err := h.run("new", "demo"); err == nil {
		t.Fatal("new accepted an injected package name from a per-VM file")
	}
	h.assertNotCalled("start")
}

func TestAnInvalidExtraPackageAbortsNewBeforeAnyVMIsTouched(t *testing.T) {
	h := newHarness(t)
	t.Setenv("PTRBOX_EXTRA_PACKAGES", "ripgrep; reboot")
	if err := h.run("new", "demo"); err == nil {
		t.Fatal("new accepted an injected package name")
	}
	h.assertNotCalled("start")
}

func TestNewRebootsTheVMSoTheFirewallClamps(t *testing.T) {
	// Boot 1 provisions over an open network; the wall only goes up on reboot.
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.assertOrder("start --name demo", "stop demo")
	h.assertOrder("stop demo", "^start demo$")
}

func TestNewLinksTheVMsSSHConfig(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	link := filepath.Join(h.home, ".ssh", "config.d", "lima-demo")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("no ssh config link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the ssh config entry is not a symlink")
	}
	if _, err := os.Stat(link); err != nil {
		t.Errorf("the ssh config link dangles: %v", err)
	}
}

func TestNewRunsTheVerificationScriptInsideTheVM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	h.assertCalled(`shell demo -- bash -lc`)
	// It is really vm/verify.sh that runs, not something improvised.
	scripts := strings.Join(h.fake.Scripts, "\n")
	for _, want := range []string{"assert a sandbox VM's security properties", "sudo removed"} {
		if !strings.Contains(scripts, want) {
			t.Errorf("the verification script does not mention %q", want)
		}
	}
}

func TestNewRefusesToDoubleProvisionAnExistingVM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("new", "demo")
	err := h.run("new", "demo")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
}

func TestNewRequiresAnArgument(t *testing.T) {
	h := newHarness(t)
	err := h.run("new")
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("err = %v", err)
	}
}

func TestNewUsesAnExistingRepoAsIsWhenGivenAPath(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.tmp, "elsewhere", "thing")
	mkdir(t, dir)
	gitRun(t, dir, "init", "-q")
	h.mustRun("new", dir)
	h.assertCalled("start --name thing")
}

func TestNewRefusesTheReservedProxyVMName(t *testing.T) {
	h := newHarness(t)
	err := h.run("new", "ptrbox-proxy")
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("err = %v", err)
	}
	h.assertNotCalled("start")
}

func TestNewWithoutLimactlPointsAtInstall(t *testing.T) {
	h := newHarness(t)
	h.missing["limactl"] = true
	err := h.run("new", "demo")
	if err == nil || !strings.Contains(err.Error(), "ptrbox install") {
		t.Errorf("err = %v", err)
	}
}

// --- auth --------------------------------------------------------------------

func TestTheAuthTokenTravelsOnStdinAndNeverThroughArgv(t *testing.T) {
	h := newHarness(t)
	h.keychain.token = "sk-ant-oat-EXAMPLE"
	h.mustRun("new", "demo")

	stdins := strings.Join(h.fake.Stdins, "")
	if !strings.Contains(stdins, `CLAUDE_CODE_OAUTH_TOKEN="sk-ant-oat-EXAMPLE"`) {
		t.Errorf("the token did not travel on stdin: %q", stdins)
	}
	// ps and shell history must never see it.
	if strings.Contains(h.fake.CallLog(), "sk-ant-oat-EXAMPLE") {
		t.Error("the token reached argv")
	}
	// Nor may it be written into the generated config, which persists on disk.
	if strings.Contains(h.generated("demo"), "sk-ant-oat-EXAMPLE") {
		t.Error("the token reached the generated config")
	}
}

func TestAMissingKeychainEntryWarnsButStillLeavesAUsableVM(t *testing.T) {
	h := newHarness(t)
	h.keychain.token = ""
	h.mustRun("new", "demo")
	h.assertOutputContains("no Keychain entry")
}

func TestNoKeychainAtAllWarnsRatherThanFailing(t *testing.T) {
	h := newHarness(t)
	h.keychain.available = false
	h.mustRun("new", "demo")
	h.assertOutputContains("no macOS Keychain")
}

func TestVerificationFailureBlocksTheTokenAndFlagsTheVM(t *testing.T) {
	h := newHarness(t)
	h.keychain.token = "sk-ant-oat-EXAMPLE"
	h.fake.VerifyFails = true

	err := h.run("new", "demo")
	if err == nil || !strings.Contains(err.Error(), "verification FAILED") {
		t.Fatalf("err = %v", err)
	}
	// An unverified VM does not get credentials.
	if len(h.fake.Stdins) != 0 {
		t.Errorf("something was sent to an unverified VM: %q", h.fake.Stdins)
	}
}

func TestATokenWithAQuoteIsRefusedRatherThanWritten(t *testing.T) {
	// It would produce a ~/.profile that breaks every later login.
	h := newHarness(t)
	h.keychain.token = `sk-ant-"oat"-EXAMPLE`
	err := h.run("new", "demo")
	if err == nil || !strings.Contains(err.Error(), "quote or backslash") {
		t.Fatalf("err = %v", err)
	}
	if len(h.fake.Stdins) != 0 {
		t.Error("the malformed token was sent anyway")
	}
}

// --- helpers -----------------------------------------------------------------

func write(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheMountIsThePhysicalPathNotTheOneYouTyped(t *testing.T) {
	// `new` resolves the repo path before rendering it: Lima wants a real host
	// path for the mount.
	//
	// This is not academic on macOS, where /var is a symlink to /private/var
	// and so the tmpdir every test runs in has two spellings.
	h := newHarness(t)
	real := filepath.Join(h.tmp, "real-repo")
	mkdir(t, real)
	alias := filepath.Join(h.tmp, "aliased")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}

	h.mustRun("new", alias)

	body := h.generated("real-repo")
	if !strings.Contains(body, `location: "`+real+`"`) {
		t.Errorf("the mount is not the physical path; wanted %q in:\n%s", real, mountLines(body))
	}
	if strings.Contains(body, alias) {
		t.Error("the mount kept the symlinked spelling")
	}
}

// mountLines is just the location lines, so a failure prints something a
// person can read rather than the whole config.
func mountLines(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "location:") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
